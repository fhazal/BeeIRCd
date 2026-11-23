package server

// WebSocket ports ([[listen]] websocket = true): IRC from a web page, as
// in the IRCv3 WebSocket spec. Each WebSocket message is one IRC line
// without CR LF; the subprotocols binary.ircv3.net and text.ircv3.net are
// offered (text when the page asks for none). After the handshake the
// connection is wrapped as a net.Conn that reads and writes lines, so the
// rest of the server sees an ordinary client.

import (
	"bufio"
	"bytes"
	"crypto/sha1"
	"crypto/tls"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/fhazal/beeircd/internal/irc"
)

const (
	wsGUID       = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"
	wsWait       = 10 * time.Second // for the TLS and HTTP handshake
	wsMaxMessage = maxInput + 2
	wsBinary     = "binary.ircv3.net"
	wsText       = "text.ircv3.net"
	wsReplace    = "�"
)

var (
	errWSProtocol = errors.New("websocket: protocol error")
	errWSTooBig   = errors.New("websocket: message too big")
	errWSBadText  = errors.New("websocket: text message is not UTF-8")
)

// acceptWebSocket does the TLS and HTTP handshake off the loop, then hands
// the connection on like any other.
func (a *acceptor) acceptWebSocket(conn net.Conn, l listener) {
	defer a.wg.Done()
	if l.proxy && !l.proxyAllowed(conn.RemoteAddr()) {
		conn.Close() // not our proxy: nobody else may claim an address
		return
	}
	raw := conn
	if l.tls {
		conn = tls.Server(conn, l.tlsConf)
	}
	conn.SetDeadline(time.Now().Add(wsWait))
	r := bufio.NewReaderSize(conn, 4096)
	ws, err := wsHandshake(conn, r, l)
	if err != nil {
		raw.Close()
		return
	}
	conn.SetDeadline(time.Time{})
	if !a.hand(ws, ws.secure) {
		raw.Close()
	}
}

// wsHandshake reads the HTTP upgrade request and answers it.
func wsHandshake(conn net.Conn, r *bufio.Reader, l listener) (*wsConn, error) {
	req, err := http.ReadRequest(r)
	if err != nil {
		return nil, err
	}
	refuse := func(code int, text string) (*wsConn, error) {
		fmt.Fprintf(conn, "HTTP/1.1 %d %s\r\nContent-Type: text/plain\r\nContent-Length: %d\r\nConnection: close\r\n\r\n%s",
			code, http.StatusText(code), len(text), text)
		return nil, errors.New(text)
	}
	key := req.Header.Get("Sec-WebSocket-Key")
	switch {
	case req.Method != http.MethodGet:
		return refuse(http.StatusMethodNotAllowed, "This is an IRC WebSocket port.\n")
	case !headerHas(req.Header, "Upgrade", "websocket") || !headerHas(req.Header, "Connection", "upgrade"):
		return refuse(http.StatusUpgradeRequired, "This is an IRC WebSocket port: connect with an IRC web client.\n")
	case req.Header.Get("Sec-WebSocket-Version") != "13":
		return refuse(http.StatusBadRequest, "WebSocket version 13 is needed.\n")
	}
	if k, err := base64.StdEncoding.DecodeString(key); err != nil || len(k) != 16 {
		return refuse(http.StatusBadRequest, "Bad Sec-WebSocket-Key.\n")
	}
	if !l.originAllowed(req.Header.Get("Origin")) {
		return refuse(http.StatusForbidden, "This web page may not connect here.\n")
	}
	proto := ""
	for _, p := range headerTokens(req.Header, "Sec-WebSocket-Protocol") {
		if p == wsBinary || p == wsText && proto == "" {
			proto = p
		}
	}
	ws := &wsConn{Conn: conn, r: r, text: proto != wsBinary, secure: l.tls}
	if l.proxy {
		// The proxy in front (nginx, Apache) gives the visitor's address.
		if ip := forwardedIP(req.Header); ip != nil {
			ws.remote = &net.TCPAddr{IP: ip}
		}
		if strings.EqualFold(req.Header.Get("X-Forwarded-Proto"), "https") {
			ws.secure = true
		}
	}
	sum := sha1.Sum([]byte(key + wsGUID))
	var b strings.Builder
	b.WriteString("HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n")
	b.WriteString("Sec-WebSocket-Accept: " + base64.StdEncoding.EncodeToString(sum[:]) + "\r\n")
	if proto != "" {
		b.WriteString("Sec-WebSocket-Protocol: " + proto + "\r\n")
	}
	b.WriteString("\r\n")
	if _, err := io.WriteString(conn, b.String()); err != nil {
		return nil, err
	}
	return ws, nil
}

// headerTokens returns the comma-separated tokens of header name, lower case.
func headerTokens(h http.Header, name string) []string {
	var out []string
	for _, v := range h.Values(name) {
		for _, t := range strings.Split(v, ",") {
			if t = strings.ToLower(strings.TrimSpace(t)); t != "" {
				out = append(out, t)
			}
		}
	}
	return out
}

func headerHas(h http.Header, name, token string) bool {
	for _, t := range headerTokens(h, name) {
		if t == token {
			return true
		}
	}
	return false
}

// forwardedIP is the visitor's address from the proxy's headers: the last
// X-Forwarded-For entry (the one our proxy added), else X-Real-IP.
func forwardedIP(h http.Header) net.IP {
	if v := h.Values("X-Forwarded-For"); len(v) > 0 {
		parts := strings.Split(v[len(v)-1], ",")
		if ip := net.ParseIP(strings.TrimSpace(parts[len(parts)-1])); ip != nil {
			return ip
		}
	}
	return net.ParseIP(strings.TrimSpace(h.Get("X-Real-IP")))
}

// originAllowed reports whether a page from origin may connect: any page
// when the port has no origins list. Entries match the whole origin
// (https://chat.example.org) or its host (chat.example.org, *.example.org).
func (l listener) originAllowed(origin string) bool {
	if len(l.origins) == 0 {
		return true
	}
	origin = strings.ToLower(origin)
	host := origin
	if i := strings.Index(host, "://"); i >= 0 {
		host = host[i+3:]
	}
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	for _, o := range l.origins {
		o = strings.ToLower(o)
		if irc.Match(o, origin) || irc.Match(o, host) {
			return true
		}
	}
	return false
}

// wsConn is a WebSocket connection seen as a stream of IRC lines.
type wsConn struct {
	net.Conn
	r      *bufio.Reader
	remote net.Addr // from the proxy's headers; nil: the TCP peer
	text   bool     // text frames: what we send must be UTF-8
	secure bool     // TLS here or at the proxy in front

	// read side (one reader)
	pending []byte // lines ready for Read, CR LF ended
	msg     []byte // a fragmented message being put together
	inMsg   bool
	msgText bool
	err     error

	wmu   sync.Mutex // writes come from the write loop and from pings
	wpart []byte     // the start of a line a Write didn't finish
	once  sync.Once
}

func (w *wsConn) RemoteAddr() net.Addr {
	if w.remote != nil {
		return w.remote
	}
	return w.Conn.RemoteAddr()
}

// Read returns the received lines, each ended with CR LF.
func (w *wsConn) Read(p []byte) (int, error) {
	for len(w.pending) == 0 {
		if w.err != nil {
			return 0, w.err
		}
		if err := w.readMessage(); err != nil {
			switch {
			case errors.Is(err, errWSTooBig):
				w.sendClose(1009)
			case errors.Is(err, errWSBadText):
				w.sendClose(1007)
			case errors.Is(err, errWSProtocol):
				w.sendClose(1002)
			case err == io.EOF: // the page closed: answer its close frame
				w.sendClose(1000)
			}
			w.err = err
			return 0, err
		}
	}
	n := copy(p, w.pending)
	w.pending = w.pending[n:]
	return n, nil
}

// readMessage reads frames until a whole message is in, and queues it as a
// line. Pings are answered on the way; a close frame ends the stream.
func (w *wsConn) readMessage() error {
	for {
		op, fin, data, err := w.frame()
		if err != nil {
			return err
		}
		switch op {
		case 0x1, 0x2:
			if w.inMsg {
				return errWSProtocol
			}
			w.msg, w.inMsg, w.msgText = append(w.msg[:0], data...), !fin, op == 0x1
		case 0x0:
			if !w.inMsg {
				return errWSProtocol
			}
			if len(w.msg)+len(data) > wsMaxMessage {
				return errWSTooBig
			}
			w.msg, w.inMsg = append(w.msg, data...), !fin
		case 0x8:
			return io.EOF
		case 0x9:
			w.wmu.Lock()
			_, err := w.Conn.Write(wsFrame(nil, 0xA, data))
			w.wmu.Unlock()
			if err != nil {
				return err
			}
			continue
		case 0xA:
			continue
		default:
			return errWSProtocol
		}
		if w.inMsg {
			continue
		}
		line := bytes.TrimRight(w.msg, "\r\n")
		if w.msgText && !utf8.Valid(line) {
			return errWSBadText
		}
		if len(line) == 0 {
			continue
		}
		w.pending = append(append(make([]byte, 0, len(line)+2), line...), '\r', '\n')
		return nil
	}
}

// frame reads one frame from the client, unmasked.
func (w *wsConn) frame() (op byte, fin bool, data []byte, err error) {
	var h [2]byte
	if _, err = io.ReadFull(w.r, h[:]); err != nil {
		return
	}
	fin, op = h[0]&0x80 != 0, h[0]&0x0f
	if h[0]&0x70 != 0 || h[1]&0x80 == 0 { // no extensions; clients must mask
		return 0, false, nil, errWSProtocol
	}
	n := uint64(h[1] & 0x7f)
	switch n {
	case 126:
		var b [2]byte
		if _, err = io.ReadFull(w.r, b[:]); err != nil {
			return
		}
		n = uint64(binary.BigEndian.Uint16(b[:]))
	case 127:
		var b [8]byte
		if _, err = io.ReadFull(w.r, b[:]); err != nil {
			return
		}
		n = binary.BigEndian.Uint64(b[:])
	}
	if op >= 0x8 && (n > 125 || !fin) {
		return 0, false, nil, errWSProtocol
	}
	if n > wsMaxMessage {
		return 0, false, nil, errWSTooBig
	}
	var key [4]byte
	if _, err = io.ReadFull(w.r, key[:]); err != nil {
		return
	}
	data = make([]byte, n)
	if _, err = io.ReadFull(w.r, data); err != nil {
		return
	}
	for i := range data {
		data[i] ^= key[i&3]
	}
	return op, fin, data, nil
}

// Write sends each CR LF ended line in p as one message.
func (w *wsConn) Write(p []byte) (int, error) {
	w.wmu.Lock()
	defer w.wmu.Unlock()
	data := append(w.wpart, p...)
	end := bytes.LastIndexByte(data, '\n')
	if end < 0 {
		w.wpart = data
		return len(p), nil
	}
	w.wpart = append([]byte(nil), data[end+1:]...)
	var out []byte
	for _, line := range bytes.Split(data[:end], []byte("\n")) {
		line = bytes.TrimRight(line, "\r")
		if len(line) == 0 {
			continue
		}
		op := byte(0x2)
		if w.text {
			op = 0x1
			if !utf8.Valid(line) {
				line = bytes.ToValidUTF8(line, []byte(wsReplace))
			}
		}
		out = wsFrame(out, op, line)
	}
	if _, err := w.Conn.Write(out); err != nil {
		return 0, err
	}
	return len(p), nil
}

// Close sends a close frame when no write is stuck, then closes.
func (w *wsConn) Close() error {
	w.sendClose(1000)
	return w.Conn.Close()
}

func (w *wsConn) sendClose(code uint16) {
	w.once.Do(func() {
		if !w.wmu.TryLock() {
			return // a write is stuck on a slow client: just close
		}
		defer w.wmu.Unlock()
		w.Conn.SetWriteDeadline(time.Now().Add(time.Second))
		w.Conn.Write(wsFrame(nil, 0x8, binary.BigEndian.AppendUint16(nil, code)))
	})
}

// wsFrame appends an unmasked, final frame (servers don't mask).
func wsFrame(out []byte, op byte, data []byte) []byte {
	out = append(out, 0x80|op)
	switch n := len(data); {
	case n < 126:
		out = append(out, byte(n))
	case n <= 0xffff:
		out = append(out, 126)
		out = binary.BigEndian.AppendUint16(out, uint16(n))
	default:
		out = append(out, 127)
		out = binary.BigEndian.AppendUint64(out, uint64(n))
	}
	return append(out, data...)
}
