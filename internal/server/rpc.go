package server

import (
	"bufio"
	"bytes"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fhazal/beeircd/internal/config"
	"github.com/fhazal/beeircd/internal/irc"
	"github.com/fhazal/beeircd/internal/passwd"
)

// The JSON-RPC admin API ([rpc]): JSON-RPC 2.0 with UnrealIRCd's method
// names, so tools written for UnrealIRCd's API work here. Three ways in:
//   - HTTPS: POST /api with HTTP Basic auth, one call per request;
//   - WebSocket on the same address and path, for many calls and for the
//     live log (log.subscribe);
//   - a local socket file, one JSON object per line, no password.
//
// Every call runs on the loop, like a command from a client.

const (
	rpcMaxBody    = 1 << 20
	rpcHTTPWait   = 30 * time.Second
	rpcFailWindow = time.Minute
	rpcFailMax    = 5
	rpcQueue      = 256 // lines waiting for a slow reader of the live log
)

// JSON-RPC error codes: the standard ones and UnrealIRCd's.
const (
	rpcParseError     = -32700
	rpcInvalidRequest = -32600
	rpcNoMethod       = -32601
	rpcInvalidParams  = -32602
	rpcInternal       = -32603
	rpcDenied         = -32000
	rpcNotFound       = -1000
	rpcExists         = -1001
	rpcInvalidName    = -1002
	rpcRefused        = -1005
)

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func rpcErr(code int, format string, a ...any) *rpcError {
	return &rpcError{Code: code, Message: fmt.Sprintf(format, a...)}
}

// rpcConn is one caller: an HTTPS request, a WebSocket or a socket
// connection.
type rpcConn struct {
	name   string         // the [[rpc.user]], or "local" on the socket file
	user   config.RPCUser // rights (all on the socket file)
	issuer string         // rpc.set_issuer: who is behind the tool
	out    chan []byte    // lines to write (streams only; nil for one HTTPS call)
	stop   chan struct{}  // closed when a stream ends (streams only)
	// The live log (on the loop).
	logSources []string
	// rpc.add_timer: each timer's stop channel by timer_id (on the loop).
	timers map[string]chan struct{}
}

// by names the caller for bans, notices and the log.
func (rc *rpcConn) by() string {
	if rc.issuer != "" {
		return rc.issuer
	}
	return rc.name
}

// rpcState is what the API keeps besides the loop's own data.
type rpcState struct {
	ln, sock net.Listener
	sockPath string

	mu     sync.Mutex
	conns  map[net.Conn]bool
	okPass map[string][32]byte    // name + stored hash -> SHA-256 of the password that matched
	fails  map[string][]time.Time // wrong passwords by IP
	hashes chan struct{}          // at most two password checks at once

	subs   map[*rpcConn]bool // log.subscribe (on the loop)
	recent []rpcLogEvent     // the last lines, for log.list (on the loop)
}

// openRPC opens the API's address and socket file.
func (s *Server) openRPC() error {
	r := &s.rpc
	cfg := s.cfg.RPC
	if cfg.Listen == "" && cfg.Socket == "" {
		return nil // off: nothing is kept for it either
	}
	r.conns, r.okPass, r.fails = map[net.Conn]bool{}, map[string][32]byte{}, map[string][]time.Time{}
	r.hashes = make(chan struct{}, 2)
	r.subs = map[*rpcConn]bool{}
	if cfg.Listen != "" {
		ln, err := net.Listen("tcp", cfg.Listen)
		if err != nil {
			return fmt.Errorf("rpc: listen on %s: %w", cfg.Listen, err)
		}
		r.ln = ln
	}
	if cfg.Socket != "" {
		if _, err := os.Stat(cfg.Socket); err == nil {
			if c, err := net.DialTimeout("unix", cfg.Socket, time.Second); err == nil {
				c.Close()
				return fmt.Errorf("rpc socket %s: another server is answering there", cfg.Socket)
			}
			os.Remove(cfg.Socket)
		}
		if err := os.MkdirAll(filepath.Dir(cfg.Socket), 0o750); err != nil {
			return err
		}
		ln, err := net.Listen("unix", cfg.Socket)
		if err != nil {
			return fmt.Errorf("rpc socket %s: %w", cfg.Socket, err)
		}
		os.Chmod(cfg.Socket, 0o660) // the beeircd user and its admin group
		r.sock, r.sockPath = ln, cfg.Socket
	}
	return nil
}

// RPCAddr is where the API answers over HTTPS, nil when it is off.
func (s *Server) RPCAddr() net.Addr {
	if s.rpc.ln == nil {
		return nil
	}
	return s.rpc.ln.Addr()
}

func (s *Server) startRPC() {
	if ln := s.rpc.ln; ln != nil {
		s.wg.Add(1)
		go s.rpcAccept(ln, s.tlsConf, s.rpcHTTP)
	}
	if ln := s.rpc.sock; ln != nil {
		s.wg.Add(1)
		go s.rpcAccept(ln, nil, func(conn net.Conn) {
			s.rpcStream(conn, &rpcConn{name: "local"}, "\n")
		})
	}
}

func (s *Server) closeRPC() {
	r := &s.rpc
	if r.ln != nil {
		r.ln.Close()
	}
	if r.sock != nil {
		r.sock.Close()
		os.Remove(r.sockPath)
	}
	r.mu.Lock()
	for c := range r.conns {
		c.Close()
	}
	r.mu.Unlock()
}

func (s *Server) rpcAccept(ln net.Listener, tlsConf *tls.Config, serve func(net.Conn)) {
	defer s.wg.Done()
	for {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		if tlsConf != nil {
			conn = tls.Server(conn, tlsConf)
		}
		r := &s.rpc
		r.mu.Lock()
		r.conns[conn] = true
		r.mu.Unlock()
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			defer func() {
				r.mu.Lock()
				delete(r.conns, conn)
				r.mu.Unlock()
				conn.Close()
			}()
			serve(conn)
		}()
	}
}

// ---- HTTPS and WebSocket ----

func rpcHTTPAnswer(conn net.Conn, code int, ctype string, body []byte, extra string) {
	fmt.Fprintf(conn, "HTTP/1.1 %d %s\r\nContent-Type: %s\r\nContent-Length: %d\r\n%s\r\n", code, http.StatusText(code), ctype, len(body), extra)
	conn.Write(body)
}

// rpcHTTP serves one HTTPS connection: POST /api calls, or an upgrade to
// a WebSocket.
func (s *Server) rpcHTTP(conn net.Conn) {
	// One request (its headers and body) is never bigger than this.
	capped := &cappedReader{r: conn}
	r := bufio.NewReaderSize(capped, 8192)
	ip := net.ParseIP(remoteIP(conn))
	for {
		capped.left = rpcMaxBody + 64<<10
		conn.SetDeadline(time.Now().Add(rpcHTTPWait))
		req, err := http.ReadRequest(r)
		if err != nil {
			return
		}
		text := func(code int, msg, extra string) {
			io.Copy(io.Discard, io.LimitReader(req.Body, rpcMaxBody))
			rpcHTTPAnswer(conn, code, "text/plain", []byte(msg+"\n"), extra)
		}
		// The WebSocket also opens at / : unrealircd-webpanel and the
		// UnrealIRCd RPC libraries connect to wss://host:port.
		upgrade := headerHas(req.Header, "Upgrade", "websocket")
		if req.URL.Path != "/api" && !(upgrade && req.URL.Path == "/") {
			text(http.StatusNotFound, "The API is at /api.", "Connection: close\r\n")
			return
		}
		name, pass, ok := req.BasicAuth()
		rc, why := (*rpcConn)(nil), "Sign in with the name and password of an [[rpc.user]] block."
		if ok {
			rc, why = s.rpcLogin(name, pass, ip)
		}
		if rc == nil {
			code := http.StatusUnauthorized
			if strings.HasPrefix(why, "Too many") {
				code = http.StatusTooManyRequests
			}
			text(code, why, "WWW-Authenticate: Basic realm=\"BeeIRCd RPC\"\r\nConnection: close\r\n")
			return
		}
		if upgrade {
			key := req.Header.Get("Sec-WebSocket-Key")
			if k, err := base64.StdEncoding.DecodeString(key); req.Method != http.MethodGet || err != nil || len(k) != 16 ||
				req.Header.Get("Sec-WebSocket-Version") != "13" {
				text(http.StatusBadRequest, "Bad WebSocket request.", "Connection: close\r\n")
				return
			}
			sum := sha1.Sum([]byte(key + wsGUID))
			fmt.Fprintf(conn, "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: %s\r\n\r\n",
				base64.StdEncoding.EncodeToString(sum[:]))
			conn.SetDeadline(time.Time{})
			capped.left = 1 << 62 // a stream: each message has its own limit
			s.rpcStream(&wsConn{Conn: conn, r: r, text: true, secure: true}, rc, "\r\n")
			return
		}
		if req.Method != http.MethodPost {
			text(http.StatusMethodNotAllowed, "POST a JSON-RPC request to /api.", "Allow: POST\r\nConnection: close\r\n")
			return
		}
		body, err := io.ReadAll(io.LimitReader(req.Body, rpcMaxBody+1))
		if err != nil || len(body) > rpcMaxBody {
			text(http.StatusRequestEntityTooLarge, "The request is too big.", "Connection: close\r\n")
			return
		}
		extra := ""
		if req.Close {
			extra = "Connection: close\r\n"
		}
		rpcHTTPAnswer(conn, http.StatusOK, "application/json", s.rpcHandle(rc, body), extra)
		if req.Close {
			return
		}
	}
}

// cappedReader reads at most left bytes, then fails.
type cappedReader struct {
	r    io.Reader
	left int64
}

func (c *cappedReader) Read(p []byte) (int, error) {
	if c.left <= 0 {
		return 0, errTooLong
	}
	if int64(len(p)) > c.left {
		p = p[:c.left]
	}
	n, err := c.r.Read(p)
	c.left -= int64(n)
	return n, err
}

// rpcLogin checks an [[rpc.user]] name and password from ip. why says why
// not, for the caller.
func (s *Server) rpcLogin(name, pass string, ip net.IP) (rc *rpcConn, why string) {
	r := &s.rpc
	const wrong = "Wrong name or password."
	now := time.Now()
	key := ip.String()
	r.mu.Lock()
	recent := r.fails[key][:0]
	for _, t := range r.fails[key] {
		if now.Sub(t) < rpcFailWindow {
			recent = append(recent, t)
		}
	}
	r.fails[key] = recent
	locked := len(recent) >= rpcFailMax
	r.mu.Unlock()
	if locked {
		return nil, "Too many wrong passwords. Try again in a minute."
	}
	// One answer and one count for every refusal, so a caller can't tell
	// which names exist; only the opers are told the reason.
	fail := func(note func()) (*rpcConn, string) {
		r.mu.Lock()
		if len(r.fails) > 10000 {
			clear(r.fails)
		}
		r.fails[key] = append(r.fails[key], now)
		r.mu.Unlock()
		s.post(note)
		return nil, wrong
	}
	wrongPass := func() { s.sno('a', "RPC: wrong name or password for '%s' from %s", name, key) }
	var u config.RPCUser
	found := false
	if !s.onLoop(func() {
		for _, x := range s.cfg.RPC.Users {
			if strings.EqualFold(x.Name, name) {
				u, found = x, true
			}
		}
	}) {
		return nil, "The server is stopping."
	}
	if !found || ip == nil {
		return fail(wrongPass)
	}
	if !u.From(ip) {
		return fail(func() {
			s.snoticeEvery("rpc-address:"+u.Name, time.Minute, 'a', "RPC: '%s' may not call from %s (see ips in its [[rpc.user]] block)", u.Name, key)
		})
	}
	sum := sha256.Sum256([]byte(pass))
	cacheKey := u.Name + "\x00" + u.Password
	r.mu.Lock()
	known, have := r.okPass[cacheKey]
	r.mu.Unlock()
	if have {
		if subtle.ConstantTimeCompare(known[:], sum[:]) != 1 {
			return fail(wrongPass)
		}
		return &rpcConn{name: u.Name, user: u}, ""
	}
	// The hash is slow on purpose: never more than two at once.
	select {
	case r.hashes <- struct{}{}:
	case <-s.done:
		return nil, "The server is stopping."
	}
	ok := passwd.Check(pass, u.Password)
	<-r.hashes
	if !ok {
		return fail(wrongPass)
	}
	r.mu.Lock()
	if len(r.okPass) > 100 {
		clear(r.okPass)
	}
	r.okPass[cacheKey] = sum
	r.mu.Unlock()
	return &rpcConn{name: u.Name, user: u}, ""
}

// ---- streams: WebSocket and the socket file ----

// rpcStream reads one JSON-RPC request per line from conn and writes the
// answers, and log lines when subscribed, each ended with eol.
func (s *Server) rpcStream(conn net.Conn, rc *rpcConn, eol string) {
	rc.out = make(chan []byte, rpcQueue)
	stop := make(chan struct{})
	rc.stop = stop // ends the writer below and every rpc.add_timer timer
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		for {
			select {
			case line := <-rc.out:
				conn.SetWriteDeadline(time.Now().Add(rpcHTTPWait))
				if _, err := conn.Write(append(line, eol...)); err != nil {
					conn.Close()
					return
				}
			case <-stop:
				return
			case <-s.done:
				return
			}
		}
	}()
	defer func() {
		close(stop)
		s.onLoop(func() { delete(s.rpc.subs, rc) })
	}()
	r := bufio.NewReaderSize(conn, 65536)
	for {
		line, err := readLimited(r, rpcMaxBody)
		if err != nil {
			return
		}
		if line = bytes.TrimSpace(line); len(line) == 0 {
			continue
		}
		select {
		case rc.out <- s.rpcHandle(rc, line):
		case <-s.done:
			return
		}
	}
}

// readLimited reads one line of at most max bytes.
func readLimited(r *bufio.Reader, max int) ([]byte, error) {
	var line []byte
	for {
		part, err := r.ReadSlice('\n')
		line = append(line, part...)
		if len(line) > max {
			return nil, errTooLong
		}
		if err == bufio.ErrBufferFull {
			continue
		}
		return line, err
	}
}

// ---- one call ----

type rpcRequest struct {
	Version string          `json:"jsonrpc"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
	ID      json.RawMessage `json:"id"`
}

type rpcResponse struct {
	Version string          `json:"jsonrpc"`
	Method  string          `json:"method,omitempty"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

// rpcHandle runs one request and returns the answer as JSON.
func (s *Server) rpcHandle(rc *rpcConn, body []byte) []byte {
	var req rpcRequest
	resp := rpcResponse{Version: "2.0", ID: json.RawMessage("null")}
	answer := func() []byte {
		out, err := json.Marshal(resp)
		if err != nil {
			out, _ = json.Marshal(rpcResponse{Version: "2.0", Method: resp.Method, ID: resp.ID, Error: rpcErr(rpcInternal, "the answer could not be written")})
		}
		return out
	}
	if err := json.Unmarshal(body, &req); err != nil {
		resp.Error = rpcErr(rpcParseError, "Unparsable JSON data")
		return answer()
	}
	if len(req.ID) > 0 && len(req.ID) <= 128 {
		resp.ID = req.ID
	}
	resp.Method = req.Method
	params := bytes.TrimSpace(req.Params)
	switch {
	case req.Version != "2.0" || req.Method == "" || len(req.ID) == 0 || len(req.ID) > 128:
		resp.Method = ""
		resp.Error = rpcErr(rpcInvalidRequest, "Not a JSON-RPC 2.0 request with a method and an id")
		return answer()
	case !rpcNoParams(params) && params[0] != '{':
		resp.Error = rpcErr(rpcInvalidParams, "params must be an object")
		return answer()
	}
	m, ok := rpcMethods[req.Method]
	switch {
	case !ok:
		resp.Error = rpcErr(rpcNoMethod, "Unsupported method")
		return answer()
	case rc.name != "local" && !strings.HasPrefix(req.Method, "rpc.") && !rc.user.Allows(req.Method):
		resp.Error = rpcErr(rpcDenied, "API call denied: %s may not call %s", rc.name, req.Method)
		return answer()
	}
	if rpcNoParams(params) {
		params = []byte("{}")
	}
	if !s.onLoop(func() {
		if m.changes {
			s.sno('a', "RPC %s: %s %s", rc.by(), req.Method, irc.Truncate(string(params), 300))
		}
		resp.Result, resp.Error = m.run(s, rc, params)
		if resp.Error == nil && resp.Result == nil {
			resp.Result = true
		}
	}) {
		resp.Error = rpcErr(rpcInternal, "the server is stopping")
	}
	return answer()
}

// rpcNoParams tells if a call came without params: none, null, or an empty
// array, which is what PHP's json_encode makes of an empty params list
// (unrealircd-webpanel sends server_ban_exception.list that way).
func rpcNoParams(params []byte) bool {
	if len(params) == 0 || bytes.Equal(params, []byte("null")) {
		return true
	}
	n := len(params)
	return n >= 2 && params[0] == '[' && params[n-1] == ']' && len(bytes.TrimSpace(params[1:n-1])) == 0
}

// rpcParams reads the params object into v.
func rpcParams(params json.RawMessage, v any) *rpcError {
	if err := json.Unmarshal(params, v); err != nil {
		return rpcErr(rpcInvalidParams, "Invalid parameters: %v", err)
	}
	return nil
}

// ---- the live log ----

// rpcLogEvent is one log line as the API gives it (UnrealIRCd's fields).
type rpcLogEvent struct {
	Timestamp string `json:"timestamp"`
	Level     string `json:"level"`
	Subsystem string `json:"subsystem"`
	EventID   string `json:"event_id"`
	LogSource string `json:"log_source"`
	Msg       string `json:"msg"`
}

const rpcRecentLog = 500

// rpcLog hands a server notice to the API: kept for log.list and sent to
// everyone who subscribed; subsystem "link" files a link notice there. On
// the loop.
func (s *Server) rpcLog(text, subsystem string) {
	r := &s.rpc
	if r.subs == nil {
		return // the API is off
	}
	ev := rpcLogEvent{Timestamp: rpcTime(time.Now()), Level: "info", Subsystem: "server", EventID: "NOTICE", LogSource: s.cfg.Server.Name, Msg: text}
	switch {
	case subsystem == "link":
		ev.Subsystem, ev.EventID = "link", "LINK"
	case subsystem == "connect":
		ev.Subsystem, ev.EventID = "connect", "LOCAL_CLIENT_CONNECT"
	case subsystem == "quit":
		ev.Subsystem, ev.EventID = "connect", "LOCAL_CLIENT_DISCONNECT"
	case strings.HasPrefix(text, "Link "):
		ev.Subsystem, ev.EventID = "link", "LINK"
	case strings.HasPrefix(text, "RPC"):
		ev.Subsystem, ev.EventID = "rpc", "RPC_CALL"
	case strings.Contains(text, "is now a server operator of type"), strings.Contains(text, " oper account because "):
		ev.Subsystem, ev.EventID = "oper", "OPER"
	}
	r.recent = append(r.recent, ev)
	if len(r.recent) > rpcRecentLog {
		r.recent = append(r.recent[:0], r.recent[len(r.recent)-rpcRecentLog:]...)
	}
	for rc := range r.subs {
		if !rpcLogWanted(rc.logSources, ev) {
			continue
		}
		line, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "method": "log.event", "params": ev})
		if err != nil {
			continue
		}
		select {
		case rc.out <- line:
		default: // a reader that doesn't keep up misses lines, the server never waits
		}
	}
}

// rpcLogWanted applies UnrealIRCd's source list: "all", a subsystem or an
// event id; "!name" leaves one out.
func rpcLogWanted(sources []string, ev rpcLogEvent) bool {
	want := len(sources) == 0
	for _, src := range sources {
		neg := strings.HasPrefix(src, "!")
		src = strings.TrimPrefix(src, "!")
		if src == "all" || strings.EqualFold(src, ev.Subsystem) || strings.EqualFold(src, ev.EventID) || strings.EqualFold(src, ev.Level) {
			if neg {
				return false
			}
			want = true
		}
	}
	return want
}

// rpcTime is a time the way UnrealIRCd's API writes it.
func rpcTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format("2006-01-02T15:04:05.000Z")
}
