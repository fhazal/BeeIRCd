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
	syncWait  = 30 * time.Second
	maxMsg    = 64 << 10 // biggest Sync message
)

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
	var mu sync.Mutex
	var cur *link // the running child's sockets; nil between children
	front := newFront(func(conn net.Conn, info Info) bool {
		mu.Lock()
		l := cur
		mu.Unlock()
		return l != nil && l.deliver(conn, info)
	})
	defer front.Close()
	sigs := make(chan os.Signal, 4)
	signal.Notify(sigs, syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP)
	defer signal.Stop(sigs)
	stopping := false
	for {
		cmd, l, err := startChild(exe, args)
		if err != nil {
			logger.Printf("gate: can't start the server: %v", err)
			return 1
		}
		mu.Lock()
		cur = l
		mu.Unlock()
		go l.serve(front)
		exited := make(chan error, 1)
		go func() { exited <- cmd.Wait() }()
		var werr error
	wait:
		for {
			select {
			case sig := <-sigs:
				if sig != syscall.SIGHUP && !stopping {
					// No new clients; the ones here keep their pipes
					// until the server has said goodbye.
					stopping = true
					front.Close()
				}
				cmd.Process.Signal(sig)
			case werr = <-exited:
				break wait
			}
		}
		mu.Lock()
		cur = nil
		mu.Unlock()
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
			return code
		case code == ExitRestart:
			logger.Printf("gate: starting the server again")
		case code == ExitRegate:
			front.Close()
			signal.Stop(sigs)
			err := syscall.Exec(exe, append([]string{os.Args[0]}, args...), os.Environ())
			logger.Printf("gate: can't start the new gate: %v", err)
			return 1
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

// startChild starts one server process with its two sockets on fd 3 and 4.
func startChild(exe string, args []string) (*exec.Cmd, *link, error) {
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
	err = cmd.Start()
	conns[1].Close()
	ctl[1].Close()
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

// link is the gate's end of one child's two sockets.
type link struct {
	conns, ctl *net.UnixConn
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
	return &link{conns: c, ctl: k}, nil
}

func (l *link) close() {
	l.conns.Close()
	l.ctl.Close()
}

// serve answers the child's Sync requests until its socket closes.
func (l *link) serve(front Front) {
	buf := make([]byte, maxMsg)
	for {
		n, err := l.ctl.Read(buf)
		if err != nil {
			return
		}
		var spec Spec
		var res Result
		if err := json.Unmarshal(buf[:n], &spec); err != nil {
			res.Err = "gate: " + err.Error()
		} else {
			res = front.Apply(spec)
		}
		out, _ := json.Marshal(res)
		if _, err := l.ctl.Write(out); err != nil {
			return
		}
	}
}

// deliver hands one client to the child. A plain TCP client goes over as
// its own socket; anything else (TLS, WebSocket, PROXY) gets a pipe that
// pump feeds.
func (l *link) deliver(conn net.Conn, info Info) bool {
	if tc, ok := conn.(*net.TCPConn); ok {
		info.Pipe = false
		rc, err := tc.SyscallConn()
		if err != nil {
			return false
		}
		var serr error
		if err := rc.Control(func(fd uintptr) { serr = l.send(info, int(fd)) }); err != nil || serr != nil {
			return false
		}
		tc.Close() // the child holds its own copy now
		return true
	}
	info.Pipe = true
	fds, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM|syscall.SOCK_CLOEXEC|syscall.SOCK_NONBLOCK, 0)
	if err != nil {
		return false
	}
	syscall.SetsockoptInt(fds[0], syscall.SOL_SOCKET, syscall.SO_SNDBUF, pipeBuf)
	syscall.SetsockoptInt(fds[1], syscall.SOL_SOCKET, syscall.SO_SNDBUF, pipeBuf)
	err = l.send(info, fds[1])
	syscall.Close(fds[1])
	if err != nil {
		syscall.Close(fds[0])
		return false
	}
	pipe, err := fileConn(os.NewFile(uintptr(fds[0]), "client pipe"))
	if err != nil {
		return false // the child reads EOF on its end
	}
	go pump(conn, pipe)
	return true
}

func (l *link) send(info Info, fd int) error {
	msg, err := json.Marshal(info)
	if err != nil {
		return err
	}
	_, _, err = l.conns.WriteMsgUnix(msg, syscall.UnixRights(fd), nil)
	return err
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
	smu        sync.Mutex // one Sync at a time
	buf, oob   []byte     // Accept's; one caller
	closed     atomic.Bool
	goneOnce   sync.Once
	gone       chan struct{}
}

// ChildFromEnv returns the gate's end for a server started by the gate,
// nil when the server was started directly.
func ChildFromEnv() (*Child, error) {
	v, ok := os.LookupEnv(envKey)
	if !ok {
		return nil, nil
	}
	os.Unsetenv(envKey) // not for programs the server starts
	if v != Proto {
		return nil, ErrRegate
	}
	c, err := newChild(os.NewFile(3, "gate clients"), os.NewFile(4, "gate control"))
	if err != nil {
		return nil, fmt.Errorf("gate: %w", err)
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
// *Conn. It fails once Close was called or the gate is gone.
func (c *Child) Accept() (net.Conn, error) {
	for {
		n, oobn, flags, _, err := c.conns.ReadMsgUnix(c.buf, c.oob)
		if err != nil {
			if !c.closed.Load() {
				c.goneOnce.Do(func() { close(c.gone) })
			}
			return nil, err
		}
		var fds []int
		if msgs, err := syscall.ParseSocketControlMessage(c.oob[:oobn]); err == nil {
			for _, m := range msgs {
				if got, err := syscall.ParseUnixRights(&m); err == nil {
					fds = append(fds, got...)
				}
			}
		}
		var info Info
		if len(fds) != 1 || flags&(syscall.MSG_TRUNC|syscall.MSG_CTRUNC) != 0 || json.Unmarshal(c.buf[:n], &info) != nil {
			for _, fd := range fds {
				syscall.Close(fd)
			}
			continue // not a client message; never happens between matching versions
		}
		f := os.NewFile(uintptr(fds[0]), "client")
		conn, err := net.FileConn(f) // its own copy of the fd
		f.Close()
		if err != nil {
			continue
		}
		if !info.Pipe {
			return conn, nil
		}
		u, ok := conn.(*net.UnixConn)
		if !ok {
			conn.Close()
			continue
		}
		return newConn(u, info), nil
	}
}

// Sync sends the whole client-facing setup to the gate and waits for the
// answer.
func (c *Child) Sync(spec Spec) (Result, error) {
	c.smu.Lock()
	defer c.smu.Unlock()
	msg, err := json.Marshal(spec)
	if err != nil {
		return Result{}, err
	}
	c.ctl.SetDeadline(time.Now().Add(syncWait))
	defer c.ctl.SetDeadline(time.Time{})
	if _, err := c.ctl.Write(msg); err != nil {
		return Result{}, fmt.Errorf("gate: %w", err)
	}
	buf := make([]byte, maxMsg)
	n, err := c.ctl.Read(buf)
	if err != nil {
		return Result{}, fmt.Errorf("gate: %w", err)
	}
	var res Result
	if err := json.Unmarshal(buf[:n], &res); err != nil {
		return Result{}, fmt.Errorf("gate: %w", err)
	}
	if len(res.Addrs) != len(spec.Ports) || len(res.Errors) != len(spec.Ports) {
		if res.Err != "" {
			return res, nil
		}
		return Result{}, errors.New("gate: short answer")
	}
	return res, nil
}

// Gone is closed when the gate went away (not after Close).
func (c *Child) Gone() <-chan struct{} { return c.gone }

// Close stops Accept and Sync (the server is stopping).
func (c *Child) Close() error {
	c.closed.Store(true)
	c.conns.Close()
	return c.ctl.Close()
}

// Loopback joins a Front and a Child inside one process, without a child
// process: for tests of the server behind the gate.
func Loopback(newFront func(Deliver) Front) (*Child, func(), error) {
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
	front := newFront(l.deliver)
	go l.serve(front)
	return child, func() { front.Close(); l.close() }, nil
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
