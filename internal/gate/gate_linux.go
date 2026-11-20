//go:build linux

package gate

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// Supported: the gate runs on this system (Linux only; elsewhere `beeircd
// run` stays one process).
const Supported = true

const (
	inBuf     = 1024             // client to server: one per client, like the server's reader
	outBuf    = 16 << 10         // server to client: pooled, held only while copying
	pipeBuf   = 32 << 10         // SO_SNDBUF of each pipe end (the kernel doubles it)
	writeWait = 30 * time.Second // like the server's own write limit
	maxMsg    = 64 << 10         // biggest Sync message

	envResume = "BEEIRCD_GATE_RESUME" // set for a server that takes over: how many clients come
)

// Limits; tests shorten them.
var (
	// syncWait: for one answer on the control socket, and for the whole
	// hand-over to cross between gate and server.
	syncWait = 30 * time.Second
	// handWait: from the request until the old server has exited. After
	// it the gate kills that server and starts the next one empty.
	handWait = 30 * time.Second
	// holdWait: how long a client that connects during a hand-over waits
	// for the next server.
	holdWait = 60 * time.Second
)

// goodbye is what clients read when a hand-over failed and nobody else is
// left to tell them: like the server's own line at a restart.
const goodbye = "ERROR :Closing Link: (Server restarting)\r\n"

// Run is the gate's main. It starts the server as a child process with the
// same arguments, lends it the client ports through newFront, and returns
// the exit code for `beeircd run`.
func Run(logger *log.Logger, newFront func(Deliver) Front) int {
	exe, err := os.Executable()
	if err != nil {
		logger.Printf("gate: %v", err)
		return 1
	}
	return run(logger, exe, os.Args[1:], newFront)
}

func run(logger *log.Logger, exe string, args []string, newFront func(Deliver) Front) int {
	d := &door{}
	front := newFront(d.deliver)
	defer front.Close()
	defer d.stop()
	sigs := make(chan os.Signal, 4)
	signal.Notify(sigs, syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP, syscall.SIGUSR2)
	defer signal.Stop(sigs)
	stopping := false
	var next *payload // the last server's clients, for the one about to start
	for {
		cmd, l, err := startChild(exe, args, next)
		if err != nil {
			next.drop()
			logger.Printf("gate: can't start the server: %v", err)
			return 1
		}
		// taking: this server takes over the last one's clients and
		// hasn't said yet how that went.
		taking := next != nil
		if taking {
			if err := l.sendOld(next); err != nil {
				logger.Printf("gate: hand-over: %v", err) // the server sees it too and starts empty
			}
			next = nil
		}
		d.set(l)
		go l.serve(front)
		exited := make(chan error, 1)
		go func() { exited <- cmd.Wait() }()
		var werr error
		switching := false        // this server was asked to hand over
		var late <-chan time.Time // it must be gone by then
		begin := func() {
			if stopping || switching {
				return
			}
			switching = true
			logger.Printf("gate: hand-over: asking the server to pass its clients on")
			d.hold()
			if err := l.askHandover(); err != nil {
				logger.Printf("gate: hand-over: %v", err)
			}
			late = time.After(handWait)
		}
	wait:
		for {
			select {
			case sig := <-sigs:
				if sig == syscall.SIGUSR2 {
					begin()
					continue
				}
				if sig != syscall.SIGHUP && !stopping {
					// No new clients; the ones here keep their pipes
					// until the server has said goodbye.
					stopping = true
					front.Close()
					d.stop()
				}
				cmd.Process.Signal(sig)
			case <-l.asked:
				begin()
			case p := <-l.got:
				next.drop()
				next = p
			case r := <-l.resumed:
				taking = false
				if r.err != "" {
					logger.Printf("gate: hand-over failed in the new server (%s); its clients were dropped", r.err)
				} else {
					logger.Printf("gate: hand-over done: %d clients stayed connected", r.n)
				}
			case <-late:
				late = nil
				logger.Printf("gate: the server didn't hand over within %v; stopping it", handWait)
				cmd.Process.Kill()
			case werr = <-exited:
				break wait
			}
		}
		d.clear(l)
		select {
		case p := <-l.got:
			next.drop()
			next = p
		default:
		}
		l.close()
		code := 0
		if werr != nil {
			code = 1 // killed by a signal, or Wait failed
			var ee *exec.ExitError
			if errors.As(werr, &ee) && ee.ExitCode() > 0 {
				code = ee.ExitCode()
			}
		}
		switch {
		case stopping:
			next.drop()
			return code
		case switching:
			// The old server is gone, with or without handing over: the
			// next one starts either way, as after a restart.
			if next == nil {
				logger.Printf("gate: the server ended without handing over (exit %d); its clients are gone. Starting the next one", code)
			} else {
				logger.Printf("gate: hand-over: starting the next server for %d clients", len(next.fds))
			}
		case code == ExitRegate:
			front.Close()
			signal.Stop(sigs)
			err := syscall.Exec(exe, append([]string{os.Args[0]}, args...), os.Environ())
			logger.Printf("gate: can't start the new gate: %v", err)
			return 1
		case taking:
			// It died while taking over (the clients went with it): once
			// more, empty. That one is on its own: no loop.
			logger.Printf("gate: the server ended while taking over (exit %d); starting it again without the clients", code)
		case code == ExitRestart:
			logger.Printf("gate: starting the server again")
		default:
			if code != 0 {
				logger.Printf("gate: the server process ended (%v)", werr)
			}
			// An unexpected end closes every client with it (their pipes
			// and sockets die with the process). The gate exits too, so
			// the service manager restarts the whole thing as before.
			return code
		}
	}
}

// door is where the Front leaves its clients: with the running server, or
// waiting for the next one while a hand-over switches servers.
type door struct {
	mu   sync.Mutex
	cur  *link         // the running server's sockets; nil between servers
	wait chan struct{} // non-nil during a hand-over; closed when the next server is there
	off  bool          // the gate is stopping: nobody waits
}

func (d *door) set(l *link) {
	d.mu.Lock()
	d.cur = l
	if d.wait != nil {
		close(d.wait)
		d.wait = nil
	}
	d.mu.Unlock()
}

func (d *door) clear(l *link) {
	d.mu.Lock()
	if d.cur == l {
		d.cur = nil
	}
	d.mu.Unlock()
}

// hold makes new clients wait for the next server.
func (d *door) hold() {
	d.mu.Lock()
	if d.wait == nil && !d.off {
		d.wait = make(chan struct{})
	}
	d.mu.Unlock()
}

// stop lets every waiting client go (they are refused).
func (d *door) stop() {
	d.mu.Lock()
	d.off = true
	if d.wait != nil {
		close(d.wait)
		d.wait = nil
	}
	d.mu.Unlock()
}

// deliver hands one client to the running server; during a hand-over it
// waits for the next one. False: nobody took it, the caller closes conn.
func (d *door) deliver(conn net.Conn, info Info) bool {
	var giveUp *time.Timer
	for {
		d.mu.Lock()
		l, w := d.cur, d.wait
		d.mu.Unlock()
		if l != nil {
			ok, handing := l.deliver(conn, info)
			if !handing {
				return ok
			}
			// l takes no more clients. hold ran before that, so either
			// the wait is on or the next server is there already.
			d.mu.Lock()
			w = d.wait
			moved := d.cur != l
			d.mu.Unlock()
			if moved {
				continue
			}
		}
		if w == nil {
			return false
		}
		if giveUp == nil {
			giveUp = time.NewTimer(holdWait)
			defer giveUp.Stop()
		}
		select {
		case <-w:
		case <-giveUp.C:
			return false
		}
	}
}

// startChild starts one server process with its two sockets on fd 3 and 4.
// With next it also gets the hand-over snapshot on fd 5 and is told how
// many clients follow.
func startChild(exe string, args []string, next *payload) (*exec.Cmd, *link, error) {
	conns, err := seqpacketPair()
	if err != nil {
		return nil, nil, err
	}
	ctl, err := seqpacketPair()
	if err != nil {
		conns[0].Close()
		conns[1].Close()
		return nil, nil, err
	}
	cmd := exec.Command(exe, args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	cmd.Env = append(os.Environ(), envKey+"="+Proto)
	cmd.ExtraFiles = []*os.File{conns[1], ctl[1]} // fd 3, fd 4
	var snap *os.File
	if next != nil {
		snap = os.NewFile(uintptr(next.snap), "hand-over")
		next.snap = -1                                // snap owns it now
		cmd.ExtraFiles = append(cmd.ExtraFiles, snap) // fd 5
		cmd.Env = append(cmd.Env, envResume+"="+strconv.Itoa(len(next.fds)))
	}
	err = cmd.Start()
	conns[1].Close()
	ctl[1].Close()
	if snap != nil {
		snap.Close()
	}
	if err != nil {
		conns[0].Close()
		ctl[0].Close()
		return nil, nil, err
	}
	l, err := newLink(conns[0], ctl[0])
	if err != nil {
		cmd.Process.Kill()
		cmd.Wait()
		return nil, nil, err
	}
	return cmd, l, nil
}

func seqpacketPair() ([2]*os.File, error) {
	fds, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_SEQPACKET|syscall.SOCK_CLOEXEC, 0)
	if err != nil {
		return [2]*os.File{}, fmt.Errorf("socketpair: %w", err)
	}
	return [2]*os.File{os.NewFile(uintptr(fds[0]), "gate"), os.NewFile(uintptr(fds[1]), "gate")}, nil
}

// fileConn turns f into a *net.UnixConn (on its own copy of the fd) and
// closes f.
func fileConn(f *os.File) (*net.UnixConn, error) {
	c, err := net.FileConn(f)
	f.Close()
	if err != nil {
		return nil, err
	}
	u, ok := c.(*net.UnixConn)
	if !ok {
		c.Close()
		return nil, fmt.Errorf("%s is not a unix socket", f.Name())
	}
	return u, nil
}

// connMsg is one message on fd 3, gate to server.
type connMsg struct {
	// Op: "" a new client (with its fd), "hand" hand over now (no fd),
	// "old" a client of the last server (with its fd; ID is that
	// server's name for it).
	Op string `json:"o,omitempty"`
	ID string `json:"i,omitempty"`
	Info
}

// ctlMsg is one request on fd 4, server to gate. The gate answers each
// with a Result ("sync") or an ack, except "fd".
type ctlMsg struct {
	// Op: "sync" the client ports (Spec), "ask" start a hand-over, "fd"
	// one client for the next server (with its fd, under ID; no answer),
	// "snap" the snapshot (with its fd) after N clients, "resumed" the
	// new server took over N clients (Err: it didn't, and why).
	Op   string `json:"op"`
	Spec *Spec  `json:"spec,omitempty"`
	ID   string `json:"id,omitempty"`
	N    int    `json:"n,omitempty"`
	Err  string `json:"err,omitempty"`
}

type ack struct {
	Err string `json:"err,omitempty"`
}

// payload is what one server hands the next: the snapshot and its clients'
// sockets, held by the gate in between.
type payload struct {
	snap int // -1: none (yet), or passed on
	ids  []string
	fds  []int
}

// drop closes every socket in p: those clients are disconnected, with a
// goodbye line (best effort). A nil p is fine.
func (p *payload) drop() {
	if p == nil {
		return
	}
	for _, fd := range p.fds {
		unix.Sendto(fd, []byte(goodbye), unix.MSG_DONTWAIT|unix.MSG_NOSIGNAL, nil)
		syscall.Close(fd)
	}
	p.fds, p.ids = nil, nil
	if p.snap >= 0 {
		syscall.Close(p.snap)
		p.snap = -1
	}
}

type resumeNote struct {
	n   int
	err string
}

// link is the gate's end of one child's two sockets.
type link struct {
	conns, ctl *net.UnixConn
	mu         sync.Mutex      // one message on conns at a time; guards handing
	handing    bool            // asked to hand over: no more clients for this server
	asked      chan struct{}   // the server asks for a hand-over
	got        chan *payload   // its hand-over, complete
	resumed    chan resumeNote // it took over the last server's clients (or couldn't)
}

func newLink(conns, ctl *os.File) (*link, error) {
	c, err := fileConn(conns)
	if err != nil {
		ctl.Close()
		return nil, err
	}
	k, err := fileConn(ctl)
	if err != nil {
		c.Close()
		return nil, err
	}
	return &link{conns: c, ctl: k, asked: make(chan struct{}, 1), got: make(chan *payload, 1), resumed: make(chan resumeNote, 1)}, nil
}

func (l *link) close() {
	l.conns.Close()
	l.ctl.Close()
}

// rights returns the file descriptors that came with a message.
func rights(oob []byte) []int {
	var fds []int
	if msgs, err := syscall.ParseSocketControlMessage(oob); err == nil {
		for _, m := range msgs {
			if got, err := syscall.ParseUnixRights(&m); err == nil {
				fds = append(fds, got...)
			}
		}
	}
	return fds
}

func closeAll(fds []int) {
	for _, fd := range fds {
		syscall.Close(fd)
	}
}

// serve answers the child's requests until its socket closes.
func (l *link) serve(front Front) {
	buf := make([]byte, maxMsg)
	oob := make([]byte, syscall.CmsgSpace(4*4))
	var pend *payload // the hand-over being collected
	defer func() { pend.drop() }()
	for {
		n, oobn, flags, _, err := l.ctl.ReadMsgUnix(buf, oob)
		if err != nil {
			return
		}
		fds := rights(oob[:oobn])
		var m ctlMsg
		var out any = ack{}
		if flags&(syscall.MSG_TRUNC|syscall.MSG_CTRUNC) != 0 {
			m.Op, out = "bad", ack{Err: "gate: message too long"}
		} else if err := json.Unmarshal(buf[:n], &m); err != nil {
			m.Op, out = "bad", Result{Err: "gate: " + err.Error()}
		}
		switch {
		case m.Op == "bad":
		case m.Op == "sync" && m.Spec != nil:
			out = front.Apply(*m.Spec)
		case m.Op == "ask":
			select {
			case l.asked <- struct{}{}:
			default:
			}
		case m.Op == "fd" && len(fds) == 1:
			if pend == nil {
				pend = &payload{snap: -1}
			}
			pend.ids, pend.fds = append(pend.ids, m.ID), append(pend.fds, fds[0])
			continue // no answer: the snapshot's answer covers them all
		case m.Op == "snap" && len(fds) == 1:
			if pend == nil {
				pend = &payload{snap: -1}
			}
			pend.snap, fds = fds[0], nil
			if len(pend.fds) != m.N {
				out = ack{Err: fmt.Sprintf("gate: got %d of %d clients", len(pend.fds), m.N)}
				pend.drop()
			} else {
				select {
				case l.got <- pend:
				default:
					pend.drop() // a second hand-over from one server: never happens
				}
			}
			pend = nil
		case m.Op == "resumed":
			select {
			case l.resumed <- resumeNote{m.N, m.Err}:
			default:
			}
		default:
			out = ack{Err: "gate: unknown request"}
		}
		closeAll(fds)
		reply, _ := json.Marshal(out)
		if _, err := l.ctl.Write(reply); err != nil {
			return
		}
	}
}

// askHandover tells the server to hand over. After it no client goes to
// this server any more.
func (l *link) askHandover() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.handing {
		return nil
	}
	l.handing = true
	msg, _ := json.Marshal(connMsg{Op: "hand"})
	_, err := l.conns.Write(msg)
	return err
}

// sendOld passes the last server's clients to this one, then lets go of
// them: from here on they live and die with this server.
func (l *link) sendOld(p *payload) error {
	defer p.drop() // whatever wasn't sent
	l.conns.SetWriteDeadline(time.Now().Add(syncWait))
	defer l.conns.SetWriteDeadline(time.Time{})
	for len(p.fds) > 0 {
		msg, _ := json.Marshal(connMsg{Op: "old", ID: p.ids[0]})
		if _, _, err := l.conns.WriteMsgUnix(msg, syscall.UnixRights(p.fds[0]), nil); err != nil {
			return fmt.Errorf("passing %d clients to the next server: %w", len(p.fds), err)
		}
		syscall.Close(p.fds[0])
		p.ids, p.fds = p.ids[1:], p.fds[1:]
	}
	return nil
}

// deliver hands one client to the child. A plain TCP client goes over as
// its own socket; anything else (TLS, WebSocket, PROXY) gets a pipe that
// pump feeds. handing: this server takes no more clients (conn is still
// the caller's).
func (l *link) deliver(conn net.Conn, info Info) (ok, handing bool) {
	if tc, isTCP := conn.(*net.TCPConn); isTCP {
		info.Pipe = false
		rc, err := tc.SyscallConn()
		if err != nil {
			return false, false
		}
		var serr error
		if err := rc.Control(func(fd uintptr) { handing, serr = l.send(info, int(fd)) }); err != nil || serr != nil || handing {
			return false, handing
		}
		tc.Close() // the child holds its own copy now
		return true, false
	}
	info.Pipe = true
	fds, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM|syscall.SOCK_CLOEXEC|syscall.SOCK_NONBLOCK, 0)
	if err != nil {
		return false, false
	}
	syscall.SetsockoptInt(fds[0], syscall.SOL_SOCKET, syscall.SO_SNDBUF, pipeBuf)
	syscall.SetsockoptInt(fds[1], syscall.SOL_SOCKET, syscall.SO_SNDBUF, pipeBuf)
	handing, err = l.send(info, fds[1])
	syscall.Close(fds[1])
	if err != nil || handing {
		syscall.Close(fds[0])
		return false, handing
	}
	pipe, err := fileConn(os.NewFile(uintptr(fds[0]), "client pipe"))
	if err != nil {
		return false, false // the child reads EOF on its end
	}
	go pump(conn, pipe)
	return true, false
}

// send passes one new client's fd to the server, unless it is handing
// over.
func (l *link) send(info Info, fd int) (handing bool, err error) {
	msg, err := json.Marshal(connMsg{Info: info})
	if err != nil {
		return false, err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.handing {
		return true, nil
	}
	_, _, err = l.conns.WriteMsgUnix(msg, syscall.UnixRights(fd), nil)
	return false, err
}

// pump copies bytes between a client and its pipe to the server until one
// side ends, then closes both. A clean close by the client shuts only the
// pipe's writing side (the server reads EOF and may still answer with its
// ERROR line); a read error closes the pipe (the server reads ErrReset).
func pump(client net.Conn, pipe *net.UnixConn) {
	go func() {
		buf := make([]byte, inBuf)
		for {
			n, err := client.Read(buf)
			if n > 0 {
				if _, werr := pipe.Write(buf[:n]); werr != nil {
					return // the server end is gone; the other half closes both
				}
			}
			if errors.Is(err, io.EOF) {
				pipe.CloseWrite()
				return
			}
			if err != nil {
				pipe.Close()
				return
			}
		}
	}()
	copyOut(client, pipe)
	client.Close() // TLS close_notify or WebSocket close frame, as before
	pipe.Close()
}

var outPool = sync.Pool{New: func() any { b := make([]byte, outBuf); return &b }}

// copyOut copies the server's bytes to the client. It borrows a buffer
// only while there is something to copy, so idle clients cost no buffer.
func copyOut(client net.Conn, pipe *net.UnixConn) {
	rc, err := pipe.SyscallConn()
	if err != nil {
		return
	}
	for {
		var bp *[]byte
		var n int
		var rerr error
		err := rc.Read(func(fd uintptr) bool {
			bp = outPool.Get().(*[]byte)
			for {
				n, rerr = syscall.Read(int(fd), *bp)
				if rerr != syscall.EINTR {
					break
				}
			}
			if rerr == syscall.EAGAIN {
				outPool.Put(bp)
				bp = nil
				return false // wait until readable
			}
			return true
		})
		if err != nil || rerr != nil || n <= 0 { // n == 0: the server closed it
			if bp != nil {
				outPool.Put(bp)
			}
			return
		}
		client.SetWriteDeadline(time.Now().Add(writeWait))
		_, werr := client.Write((*bp)[:n])
		outPool.Put(bp)
		if werr != nil {
			return
		}
	}
}

// Child is the server's end of the gate: it receives clients and syncs
// the client ports.
type Child struct {
	conns, ctl *net.UnixConn
	smu        sync.Mutex // one request on ctl at a time
	buf, oob   []byte     // Accept's; one caller
	closed     atomic.Bool
	goneOnce   sync.Once
	gone       chan struct{}
	hmu        sync.Mutex
	hand       *Handover // from the last server, until this one takes it
}

// ChildFromEnv returns the gate's end for a server started by the gate,
// nil when the server was started directly.
func ChildFromEnv() (*Child, error) {
	v, ok := os.LookupEnv(envKey)
	if !ok {
		return nil, nil
	}
	os.Unsetenv(envKey) // not for programs the server starts
	resume, resuming := os.LookupEnv(envResume)
	os.Unsetenv(envResume)
	if v != Proto {
		return nil, ErrRegate
	}
	c, err := newChild(os.NewFile(3, "gate clients"), os.NewFile(4, "gate control"))
	if err != nil {
		return nil, fmt.Errorf("gate: %w", err)
	}
	if resuming {
		n, _ := strconv.Atoi(resume)
		c.hand = c.load(os.NewFile(5, "hand-over"), n)
	}
	return c, nil
}

func newChild(conns, ctl *os.File) (*Child, error) {
	c, err := fileConn(conns)
	if err != nil {
		ctl.Close()
		return nil, err
	}
	k, err := fileConn(ctl)
	if err != nil {
		c.Close()
		return nil, err
	}
	return &Child{conns: c, ctl: k, buf: make([]byte, 4096), oob: make([]byte, syscall.CmsgSpace(4*4)), gone: make(chan struct{})}, nil
}

// Accept waits for the next client: a *net.TCPConn from a plain port or a
// *Conn. It fails once Close was called or the gate is gone, and with
// ErrHandover when the gate asks this server to hand over (no client
// comes after that).
func (c *Child) Accept() (net.Conn, error) {
	for {
		n, oobn, flags, _, err := c.conns.ReadMsgUnix(c.buf, c.oob)
		if err != nil {
			if !c.closed.Load() {
				c.goneOnce.Do(func() { close(c.gone) })
			}
			return nil, err
		}
		fds := rights(c.oob[:oobn])
		var m connMsg
		if flags&(syscall.MSG_TRUNC|syscall.MSG_CTRUNC) != 0 || json.Unmarshal(c.buf[:n], &m) != nil {
			closeAll(fds)
			continue // never happens between matching versions
		}
		if m.Op == "hand" && len(fds) == 0 {
			return nil, ErrHandover
		}
		if m.Op != "" || len(fds) != 1 {
			closeAll(fds) // not a new client (an "old" one nobody waited for)
			continue
		}
		f := os.NewFile(uintptr(fds[0]), "client")
		conn, err := net.FileConn(f) // its own copy of the fd
		f.Close()
		if err != nil {
			continue
		}
		if !m.Info.Pipe {
			return conn, nil
		}
		u, ok := conn.(*net.UnixConn)
		if !ok {
			conn.Close()
			continue
		}
		return newConn(u, m.Info), nil
	}
}

// request sends one message on the control socket, with fd when it is 0 or
// more, and reads the answer into reply (nil: this message has none).
func (c *Child) request(m ctlMsg, fd int, reply any) error {
	msg, err := json.Marshal(m)
	if err != nil {
		return err
	}
	c.smu.Lock()
	defer c.smu.Unlock()
	c.ctl.SetDeadline(time.Now().Add(syncWait))
	defer c.ctl.SetDeadline(time.Time{})
	var oob []byte
	if fd >= 0 {
		oob = syscall.UnixRights(fd)
	}
	if _, _, err := c.ctl.WriteMsgUnix(msg, oob, nil); err != nil {
		return fmt.Errorf("gate: %w", err)
	}
	if reply == nil {
		return nil
	}
	buf := make([]byte, maxMsg)
	n, err := c.ctl.Read(buf)
	if err != nil {
		return fmt.Errorf("gate: %w", err)
	}
	if err := json.Unmarshal(buf[:n], reply); err != nil {
		return fmt.Errorf("gate: %w", err)
	}
	return nil
}

// Sync sends the whole client-facing setup to the gate and waits for the
// answer.
func (c *Child) Sync(spec Spec) (Result, error) {
	var res Result
	if err := c.request(ctlMsg{Op: "sync", Spec: &spec}, -1, &res); err != nil {
		return Result{}, err
	}
	if len(res.Addrs) != len(spec.Ports) || len(res.Errors) != len(spec.Ports) {
		if res.Err != "" {
			return res, nil
		}
		return Result{}, errors.New("gate: short answer")
	}
	return res, nil
}

// RequestHandover asks the gate to replace this server with a new one
// while the clients stay connected (the gate's SIGUSR2 does the same). The
// gate answers by asking for the hand-over: Accept returns ErrHandover.
func (c *Child) RequestHandover() error {
	var a ack
	if err := c.request(ctlMsg{Op: "ask"}, -1, &a); err != nil {
		return err
	}
	if a.Err != "" {
		return errors.New(a.Err)
	}
	return nil
}

// Give hands the gate what the next server needs: the snapshot, and each
// client's connection under the name the snapshot knows it by. It returns
// once the gate holds everything; the caller then closes its connections
// (without a goodbye) and stops. After an error the gate holds nothing:
// the caller says goodbye to its clients as at a restart.
func (c *Child) Give(snapshot []byte, conns map[string]net.Conn) error {
	snap, err := snapFile(snapshot)
	if err != nil {
		return fmt.Errorf("gate: hand-over snapshot: %w", err)
	}
	defer snap.Close()
	for id, conn := range conns {
		sc, ok := conn.(syscall.Conn)
		if !ok {
			return fmt.Errorf("gate: client %s has no socket to pass on (%T)", id, conn)
		}
		rc, err := sc.SyscallConn()
		if err != nil {
			return fmt.Errorf("gate: client %s: %w", id, err)
		}
		var serr error
		if err := rc.Control(func(fd uintptr) { serr = c.request(ctlMsg{Op: "fd", ID: id}, int(fd), nil) }); err != nil {
			return fmt.Errorf("gate: client %s: %w", id, err)
		}
		if serr != nil {
			return serr
		}
	}
	rc, err := snap.SyscallConn()
	if err != nil {
		return fmt.Errorf("gate: hand-over snapshot: %w", err)
	}
	var a ack
	var serr error
	if err := rc.Control(func(fd uintptr) { serr = c.request(ctlMsg{Op: "snap", N: len(conns)}, int(fd), &a) }); err != nil {
		return fmt.Errorf("gate: hand-over snapshot: %w", err)
	}
	if serr != nil {
		return serr
	}
	if a.Err != "" {
		return errors.New(a.Err)
	}
	return nil
}

// snapFile puts the snapshot in a file with no name: in memory, or (old
// kernels) an unlinked temporary file. It never touches a path others can
// read: the snapshot holds addresses and login state.
func snapFile(b []byte) (*os.File, error) {
	var f *os.File
	if fd, err := unix.MemfdCreate("beeircd-handover", unix.MFD_CLOEXEC); err == nil {
		f = os.NewFile(uintptr(fd), "hand-over")
	} else {
		f, err = os.CreateTemp("", "beeircd-handover-*")
		if err != nil {
			return nil, err
		}
		os.Remove(f.Name())
	}
	if _, err := f.Write(b); err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}

// load reads what the gate passes a server that takes over: the snapshot
// and n clients. A hand-over that didn't arrive whole comes back with Err
// set and no clients.
func (c *Child) load(snap *os.File, n int) *Handover {
	h := &Handover{conns: make(map[string]int, n)}
	b, err := io.ReadAll(io.NewSectionReader(snap, 0, 1<<40))
	snap.Close()
	if err != nil {
		h.Err = fmt.Errorf("gate: hand-over snapshot: %w", err)
	}
	h.Snapshot = b
	c.conns.SetReadDeadline(time.Now().Add(syncWait))
	defer c.conns.SetReadDeadline(time.Time{})
	for i := 0; i < n; i++ {
		got, oobn, flags, _, err := c.conns.ReadMsgUnix(c.buf, c.oob)
		if err != nil {
			h.Err = fmt.Errorf("gate: hand-over: got %d of %d clients: %w", i, n, err)
			break
		}
		fds := rights(c.oob[:oobn])
		var m connMsg
		if flags&(syscall.MSG_TRUNC|syscall.MSG_CTRUNC) != 0 || json.Unmarshal(c.buf[:got], &m) != nil || m.Op != "old" || len(fds) != 1 {
			closeAll(fds)
			h.Err = fmt.Errorf("gate: hand-over: got %d of %d clients, then something else", i, n)
			break
		}
		if old, dup := h.conns[m.ID]; dup {
			syscall.Close(old)
		}
		h.conns[m.ID] = fds[0]
	}
	if h.Err != nil {
		h.Close()
	}
	return h
}

// Handover returns what the last server handed over, once; nil when this
// server starts with nothing to take over.
func (c *Child) Handover() *Handover {
	c.hmu.Lock()
	defer c.hmu.Unlock()
	h := c.hand
	c.hand = nil
	return h
}

// Resumed tells the gate how taking over went: kept clients stay
// connected, or problem says why none do.
func (c *Child) Resumed(kept int, problem string) error {
	var a ack
	return c.request(ctlMsg{Op: "resumed", N: kept, Err: problem}, -1, &a)
}

// Len is the number of clients not taken yet.
func (h *Handover) Len() int { return len(h.conns) }

// Has reports whether the client filed under id came along.
func (h *Handover) Has(id string) bool {
	_, ok := h.conns[id]
	return ok
}

// Conn takes the client the last server filed under id. info is what that
// server knew about a client behind a pipe (TLS, WebSocket, PROXY); nil
// for a plain client, whose own socket this is.
func (h *Handover) Conn(id string, info *Info) (net.Conn, error) {
	fd, ok := h.conns[id]
	if !ok {
		return nil, fmt.Errorf("gate: no client %s in the hand-over", id)
	}
	delete(h.conns, id)
	f := os.NewFile(uintptr(fd), "client")
	conn, err := net.FileConn(f) // its own copy of the fd
	f.Close()
	if err != nil {
		return nil, err
	}
	u, isPipe := conn.(*net.UnixConn)
	switch {
	case info == nil && !isPipe:
		return conn, nil
	case info != nil && isPipe:
		return newConn(u, *info), nil
	}
	conn.Close()
	return nil, fmt.Errorf("gate: client %s isn't the kind of connection the snapshot says", id)
}

// Close disconnects the clients nobody took, with a goodbye line.
func (h *Handover) Close() {
	for id, fd := range h.conns {
		unix.Sendto(fd, []byte(goodbye), unix.MSG_DONTWAIT|unix.MSG_NOSIGNAL, nil)
		syscall.Close(fd)
		delete(h.conns, id)
	}
}

// Gone is closed when the gate went away (not after Close).
func (c *Child) Gone() <-chan struct{} { return c.gone }

// Close stops Accept and Sync (the server is stopping).
func (c *Child) Close() error {
	c.closed.Store(true)
	if h := c.Handover(); h != nil {
		h.Close()
	}
	c.conns.Close()
	return c.ctl.Close()
}

// Loop joins a Front and a Child inside one process, without a child
// process: for tests of the server behind the gate. Handover switches to a
// new Child the way run switches server processes.
type Loop struct {
	d     *door
	front Front
	mu    sync.Mutex
	l     *link
	child *Child
}

// NewLoop makes a Loop with its first Child.
func NewLoop(newFront func(Deliver) Front) (*Loop, error) {
	lp := &Loop{d: &door{}}
	l, child, err := loopPair()
	if err != nil {
		return nil, err
	}
	lp.l, lp.child = l, child
	lp.front = newFront(lp.d.deliver)
	lp.d.set(l)
	go l.serve(lp.front)
	return lp, nil
}

func loopPair() (*link, *Child, error) {
	conns, err := seqpacketPair()
	if err != nil {
		return nil, nil, err
	}
	ctl, err := seqpacketPair()
	if err != nil {
		conns[0].Close()
		conns[1].Close()
		return nil, nil, err
	}
	l, err := newLink(conns[0], ctl[0])
	if err != nil {
		conns[1].Close()
		ctl[1].Close()
		return nil, nil, err
	}
	child, err := newChild(conns[1], ctl[1])
	if err != nil {
		l.close()
		return nil, nil, err
	}
	return l, child, nil
}

// Child is the server's end right now.
func (lp *Loop) Child() *Child {
	lp.mu.Lock()
	defer lp.mu.Unlock()
	return lp.child
}

// Handover asks the server on the current Child to hand over and waits up
// to wait for it. It returns the next Child, which holds the hand-over
// (Child.Handover). The caller sees to it that the old server has stopped
// before the next one starts. After an error the old Child takes no more
// clients, as in the gate; the caller starts over with a new Loop.
func (lp *Loop) Handover(wait time.Duration) (*Child, error) {
	lp.mu.Lock()
	defer lp.mu.Unlock()
	old := lp.l
	lp.d.hold()
	if err := old.askHandover(); err != nil {
		lp.d.stop()
		return nil, err
	}
	var p *payload
	select {
	case p = <-old.got:
	case <-time.After(wait):
		lp.d.stop()
		return nil, errors.New("gate: the server didn't hand over in time")
	}
	l, child, err := loopPair()
	if err != nil {
		p.drop()
		lp.d.stop()
		return nil, err
	}
	snap := os.NewFile(uintptr(p.snap), "hand-over")
	p.snap = -1
	sent := make(chan error, 1)
	n := len(p.fds)
	go func() { sent <- l.sendOld(p) }()
	child.hand = child.load(snap, n)
	<-sent
	lp.d.clear(old)
	old.close()
	lp.l, lp.child = l, child
	lp.d.set(l)
	go l.serve(lp.front)
	return child, nil
}

// Close stops the Front and closes the gate's end.
func (lp *Loop) Close() {
	lp.front.Close()
	lp.d.stop()
	lp.mu.Lock()
	lp.l.close()
	lp.mu.Unlock()
}

// Loopback is NewLoop for tests that need no hand-over: the Child and
// what stops the gate's side.
func Loopback(newFront func(Deliver) Front) (*Child, func(), error) {
	lp, err := NewLoop(newFront)
	if err != nil {
		return nil, nil, err
	}
	return lp.Child(), lp.Close, nil
}

// peerClosed reports whether the other end closed the socket completely
// (POLLHUP) rather than only shutting its writing side (POLLRDHUP alone).
func peerClosed(c *net.UnixConn) bool {
	rc, err := c.SyscallConn()
	if err != nil {
		return false
	}
	hup := false
	rc.Control(func(fd uintptr) {
		p := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN | unix.POLLRDHUP}}
		if n, err := unix.Poll(p, 0); err == nil && n > 0 {
			hup = p[0].Revents&unix.POLLHUP != 0
		}
	})
	return hup
}
