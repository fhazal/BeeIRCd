// Package hivepanel is the Hive Panel: BeeIRCd's web admin, served by the
// beeircd-web program. It answers only on a loopback address and talks to
// the server only through the control socket. The page loads once; every
// action after that is a small JSON request (AJAX), and the live parts
// arrive over one Server-Sent Events stream.
//
// Who may do what is decided by the server, not here: each request names
// the signed-in oper and the server checks their oper entry and flags
// again. This side keeps the browser sessions, limits wrong sign-ins per
// address and guards against cross-site requests.
package hivepanel

import (
	"bytes"
	"compress/gzip"
	"crypto/rand"
	"crypto/sha256"
	"embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/fhazal/beeircd/internal/control"
)

//go:embed ui
var uiFiles embed.FS

const (
	cookieName = "hive_session"
	// maxSession is the longest a sign-in lasts, clicks or not.
	maxSession = 12 * time.Hour
	// Wrong sign-ins from one address: after ipMaxFails, wait ipLockFor.
	ipMaxFails = 5
	ipLockFor  = 10 * time.Minute
	// livePeriod is how often the live stream sends fresh numbers.
	livePeriod = 3 * time.Second
	maxBody    = 16 << 10
)

// Asker sends one request to the server (control.Ask in real use).
type Asker func(control.Request, time.Duration) (*control.Response, error)

// Options set up a Panel.
type Options struct {
	Ask          Asker
	TrustedProxy []string
	Idle         time.Duration // sign out after this long without a click
	ChatURL      string        // BeeRoom ([web] chat_url): the only page the panel may frame
	// HostAddr is the Docker host as the container sees it (its network's
	// gateway). Requests from it came through the port compose.yaml
	// publishes on the host's 127.0.0.1, so they count as 127.0.0.1.
	HostAddr string
	Now      func() time.Time
}

// Panel is the web handler.
type Panel struct {
	ask     Asker
	csp     string // Content-Security-Policy, with BeeRoom as the one frame source
	proxies map[string]bool
	host    string // Options.HostAddr
	idle    time.Duration
	now     func() time.Time
	mux     *http.ServeMux

	mu       sync.Mutex
	sessions map[string]*session // by sha256 of the cookie value
	fails    map[string]*ipFails
}

type session struct {
	oper    control.PanelOper
	ip      string
	started time.Time
	last    time.Time
}

type ipFails struct {
	n     int
	until time.Time
	last  time.Time
}

// New makes the panel.
func New(o Options) *Panel {
	p := &Panel{ask: o.Ask, idle: o.Idle, now: o.Now, proxies: map[string]bool{}, host: o.HostAddr,
		sessions: map[string]*session{}, fails: map[string]*ipFails{}}
	if p.now == nil {
		p.now = time.Now
	}
	p.csp = "default-src 'none'; script-src 'self'; style-src 'self'; font-src 'self'; img-src 'self' data:; connect-src 'self'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'"
	if u, err := url.Parse(o.ChatURL); err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != "" {
		p.csp += "; frame-src " + u.Scheme + "://" + u.Host
	}
	if p.idle <= 0 {
		p.idle = 30 * time.Minute
	}
	for _, ip := range o.TrustedProxy {
		if a := net.ParseIP(ip); a != nil {
			p.proxies[a.String()] = true
		}
	}
	ui, _ := fs.Sub(uiFiles, "ui")
	files := http.FileServer(http.FS(ui))
	m := http.NewServeMux()
	m.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		b, _ := uiFiles.ReadFile("ui/index.html")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		w.Write(b)
	})
	// The page's own code and styles: always asked for again, so a new
	// panel build shows at the next load (embedded files have no date for
	// the browser to check against, so without this it kept old copies).
	fresh := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-cache")
		files.ServeHTTP(w, r)
	})
	m.Handle("GET /app.js", fresh)
	m.Handle("GET /netmap.js", fresh)
	m.Handle("GET /app.css", fresh)
	m.Handle("GET /fonts/{name}", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Only the font files themselves: no folder listing, no licences.
		if !strings.HasSuffix(r.PathValue("name"), ".woff2") {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Cache-Control", "public, max-age=604800")
		files.ServeHTTP(w, r)
	}))
	m.HandleFunc("GET /map/{name}", p.mapFile)
	m.HandleFunc("POST /api/login", p.login)
	m.HandleFunc("GET /api/hello", p.hello)
	m.HandleFunc("POST /api/logout", p.logout)
	m.HandleFunc("GET /api/me", p.withOper(p.me))
	m.HandleFunc("GET /api/status", p.withOper(p.simple(control.CmdStatus)))
	m.HandleFunc("GET /api/events", p.withOper(p.simple(control.CmdPanelLog)))
	m.HandleFunc("GET /api/settings", p.withOper(p.simple(control.CmdSettings)))
	m.HandleFunc("POST /api/set", p.withOper(p.set))
	m.HandleFunc("POST /api/undo", p.withOper(p.undo))
	m.HandleFunc("POST /api/check", p.withOper(p.simple(control.CmdCheck)))
	m.HandleFunc("POST /api/rehash", p.withOper(p.simple(control.CmdRehash)))
	m.HandleFunc("POST /api/backup", p.withOper(p.simple(control.CmdBackup)))
	m.HandleFunc("GET /api/backups", p.withOper(p.simple(control.CmdBackups)))
	m.HandleFunc("POST /api/backup-get", p.withOper(p.backupGet))
	m.HandleFunc("GET /api/people", p.withOper(p.people))
	m.HandleFunc("GET /api/person", p.withOper(p.person))
	m.HandleFunc("POST /api/notice", p.withOper(p.act(control.CmdNotice)))
	m.HandleFunc("POST /api/kill", p.withOper(p.act(control.CmdKill)))
	m.HandleFunc("POST /api/ban", p.withOper(p.act(control.CmdBan)))
	m.HandleFunc("GET /api/bans", p.withOper(p.bans))
	m.HandleFunc("GET /api/spam", p.withOper(p.simple(control.CmdSpam)))
	m.HandleFunc("POST /api/spam-check", p.withOper(p.spamRule(control.CmdSpamCheck)))
	m.HandleFunc("POST /api/spam-add", p.withOper(p.spamRule(control.CmdSpamAdd)))
	m.HandleFunc("POST /api/spam-del", p.withOper(p.spamDel))
	m.HandleFunc("POST /api/spam-test", p.withOper(p.spamTest))
	m.HandleFunc("GET /api/ban-match", p.withOper(p.banMatch))
	m.HandleFunc("POST /api/ban-add", p.withOper(p.banChange(control.CmdBanAdd)))
	m.HandleFunc("POST /api/ban-del", p.withOper(p.banChange(control.CmdBanDel)))
	m.HandleFunc("GET /api/opers", p.withOper(p.simple(control.CmdOpers)))
	m.HandleFunc("POST /api/oper-save", p.withOper(p.operSave))
	m.HandleFunc("POST /api/oper-del", p.withOper(p.operDel))
	m.HandleFunc("GET /api/links", p.withOper(p.simple(control.CmdLinks)))
	m.HandleFunc("POST /api/link-connect", p.withOper(p.linkAct(control.CmdLinkConnect)))
	m.HandleFunc("POST /api/link-split", p.withOper(p.linkAct(control.CmdLinkSplit)))
	m.HandleFunc("POST /api/link-del", p.withOper(p.linkAct(control.CmdLinkDel)))
	m.HandleFunc("POST /api/link-save", p.withOper(p.linkSave))
	m.HandleFunc("POST /api/map-place", p.withOper(p.mapPlace))
	m.HandleFunc("GET /api/channels", p.withOper(p.channels))
	m.HandleFunc("GET /api/channel", p.withOper(p.channel))
	m.HandleFunc("POST /api/chan-notice", p.withOper(p.chanAct(control.CmdChanNotice)))
	m.HandleFunc("POST /api/chan-topic", p.withOper(p.chanAct(control.CmdChanTopic)))
	m.HandleFunc("POST /api/chan-suspend", p.withOper(p.chanAct(control.CmdChanSuspend)))
	m.HandleFunc("POST /api/chan-unsuspend", p.withOper(p.chanAct(control.CmdChanUnsuspend)))
	m.HandleFunc("GET /api/my-nick", p.withOper(p.simple(control.CmdMyNick)))
	m.HandleFunc("POST /api/my-set", p.withOper(p.mySet))
	m.HandleFunc("POST /api/chat-key", p.withOper(p.simple(control.CmdChatKey)))
	m.HandleFunc("GET /api/my-home", p.withOper(p.simple(control.CmdMyHome)))
	m.HandleFunc("GET /api/my-memos", p.withOper(p.simple(control.CmdMyMemos)))
	m.HandleFunc("POST /api/memo-read", p.withOper(p.memoAct(control.CmdMemoRead)))
	m.HandleFunc("POST /api/memo-del", p.withOper(p.memoAct(control.CmdMemoDel)))
	m.HandleFunc("POST /api/memo-send", p.withOper(p.memoSend))
	m.HandleFunc("GET /api/my-chan", p.withOper(p.myChan))
	m.HandleFunc("POST /api/chan-do", p.withOper(p.chanDo))
	m.HandleFunc("POST /api/chan-memo", p.withOper(p.chanMemo))
	m.HandleFunc("GET /api/live", p.live)
	p.mux = m
	return p
}

// ServeHTTP adds the safety headers and refuses requests from elsewhere.
func (p *Panel) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if p.host != "" {
		if h, port, err := net.SplitHostPort(r.RemoteAddr); err == nil && h == p.host {
			r.RemoteAddr = net.JoinHostPort("127.0.0.1", port)
		}
	}
	h := w.Header()
	h.Set("Content-Security-Policy", p.csp)
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("X-Frame-Options", "DENY")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("Cross-Origin-Opener-Policy", "same-origin")
	h.Set("Cross-Origin-Resource-Policy", "same-origin")
	if !p.hostOK(r) {
		http.Error(w, "Not here.", http.StatusMisdirectedRequest)
		return
	}
	if r.Method == http.MethodPost {
		// A page on another site can't add this header without asking
		// first (CORS), and the panel never says yes.
		if r.Header.Get("X-Hive") != "1" || !p.sameOrigin(r) {
			writeErr(w, http.StatusForbidden, "This request didn't come from the Hive Panel.")
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, maxBody)
	}
	if strings.HasPrefix(r.URL.Path, "/api/") {
		h.Set("Cache-Control", "no-store")
	}
	p.mux.ServeHTTP(w, r)
}

// fromProxy reports whether r came through a trusted reverse proxy.
func (p *Panel) fromProxy(r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	return ip != nil && p.proxies[ip.String()]
}

// hostOK refuses names other than this machine's (DNS rebinding), unless
// a trusted proxy sent the request (it checks its own names).
func (p *Panel) hostOK(r *http.Request) bool {
	if p.fromProxy(r) {
		return true
	}
	host := r.Host
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	host = strings.Trim(host, "[]")
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func (p *Panel) sameOrigin(r *http.Request) bool {
	o := r.Header.Get("Origin")
	if o == "" {
		return true
	}
	o = strings.TrimPrefix(strings.TrimPrefix(o, "http://"), "https://")
	return o == r.Host
}

// clientIP is the address of the person: the proxy's word for it when a
// trusted proxy sent the request, else the connection's.
func (p *Panel) clientIP(r *http.Request) string {
	host, _, _ := net.SplitHostPort(r.RemoteAddr)
	if p.fromProxy(r) {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			parts := strings.Split(xff, ",")
			if ip := net.ParseIP(strings.TrimSpace(parts[len(parts)-1])); ip != nil {
				return ip.String()
			}
		}
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.String()
	}
	return host
}

func (p *Panel) secure(r *http.Request) bool {
	return r.TLS != nil || p.fromProxy(r) && strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]any{"error": msg})
}

func tokenKey(tok string) string {
	sum := sha256.Sum256([]byte(tok))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// current finds r's session; touch counts this request as a click.
func (p *Panel) current(r *http.Request, touch bool) (string, *session) {
	c, err := r.Cookie(cookieName)
	if err != nil || c.Value == "" {
		return "", nil
	}
	key := tokenKey(c.Value)
	now := p.now()
	p.mu.Lock()
	defer p.mu.Unlock()
	s := p.sessions[key]
	if s == nil {
		return "", nil
	}
	if now.Sub(s.last) > p.idle || now.Sub(s.started) > maxSession || s.ip != p.clientIP(r) {
		delete(p.sessions, key)
		return "", nil
	}
	if touch {
		s.last = now
	}
	return key, s
}

func (p *Panel) drop(key string) {
	p.mu.Lock()
	delete(p.sessions, key)
	p.mu.Unlock()
}

// operCtx is what a signed-in request carries.
type operCtx struct {
	key string
	s   *session
	ip  string
}

func (p *Panel) withOper(f func(http.ResponseWriter, *http.Request, operCtx)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// The page's own timed refreshes say X-Hive-Auto: they are not a
		// click, so an open but unattended panel still signs out.
		key, s := p.current(r, r.Header.Get("X-Hive-Auto") != "1")
		if s == nil {
			writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "Please sign in.", "signin": true})
			return
		}
		f(w, r, operCtx{key: key, s: s, ip: s.ip})
	}
}

// asked asks the server for oc's oper. On any failure it answers the
// browser itself and returns nil.
func (p *Panel) asked(w http.ResponseWriter, oc operCtx, req control.Request, timeout time.Duration) *control.Response {
	p.mu.Lock()
	req.As, req.IP, req.Seal = oc.s.oper.Account, oc.ip, oc.s.oper.Seal
	p.mu.Unlock()
	resp, err := p.ask(req, timeout)
	switch {
	case errors.Is(err, control.ErrNotRunning):
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "The server isn't running.", "offline": true})
		return nil
	case resp == nil && err != nil:
		writeErr(w, http.StatusBadGateway, "The server didn't answer: "+err.Error())
		return nil
	case resp.Denied == "signin":
		p.drop(oc.key)
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": resp.Error, "signin": true})
		return nil
	case resp.Denied == "flag":
		writeErr(w, http.StatusForbidden, resp.Error)
		return nil
	case resp.Error != "":
		writeErr(w, http.StatusUnprocessableEntity, resp.Error)
		return nil
	}
	return resp
}

// send asks the server for oc's oper and answers the browser. It returns
// the answer when it went well.
func (p *Panel) send(w http.ResponseWriter, oc operCtx, req control.Request, timeout time.Duration) *control.Response {
	resp := p.asked(w, oc, req, timeout)
	if resp == nil {
		return nil
	}
	if resp.Oper != nil {
		p.mu.Lock()
		oc.s.oper = *resp.Oper
		p.mu.Unlock()
		out := *resp
		o := *resp.Oper
		o.Seal = "" // stays between the panel and the server
		o.IP = oc.ip
		out.Oper = &o
		writeJSON(w, http.StatusOK, &out)
		return resp
	}
	writeJSON(w, http.StatusOK, resp)
	return resp
}

func (p *Panel) simple(cmd string) func(http.ResponseWriter, *http.Request, operCtx) {
	timeout := 10 * time.Second
	if cmd == control.CmdBackup {
		timeout = 10 * time.Minute
	}
	return func(w http.ResponseWriter, r *http.Request, oc operCtx) {
		p.send(w, oc, control.Request{Cmd: cmd}, timeout)
	}
}

func (p *Panel) me(w http.ResponseWriter, r *http.Request, oc operCtx) {
	p.send(w, oc, control.Request{Cmd: control.CmdPanelWho}, 10*time.Second)
}

func (p *Panel) set(w http.ResponseWriter, r *http.Request, oc operCtx) {
	var in struct{ Name, Value string }
	if json.NewDecoder(r.Body).Decode(&in) != nil || in.Name == "" {
		writeErr(w, http.StatusBadRequest, "Bad request.")
		return
	}
	p.send(w, oc, control.Request{Cmd: control.CmdSet, Name: in.Name, Value: in.Value}, 10*time.Second)
}

// mySet changes a member's own NickServ setting (My nick).
func (p *Panel) mySet(w http.ResponseWriter, r *http.Request, oc operCtx) {
	var in struct{ Name, Value string }
	if json.NewDecoder(r.Body).Decode(&in) != nil || in.Name == "" || len(in.Value) > 400 {
		writeErr(w, http.StatusBadRequest, "Bad request.")
		return
	}
	p.send(w, oc, control.Request{Cmd: control.CmdMySet, Name: in.Name, Value: in.Value}, 10*time.Second)
}

// memoAct reads or deletes one of the signed-in person's own memos.
func (p *Panel) memoAct(cmd string) func(http.ResponseWriter, *http.Request, operCtx) {
	return func(w http.ResponseWriter, r *http.Request, oc operCtx) {
		var in struct{ ID int64 }
		if json.NewDecoder(r.Body).Decode(&in) != nil || in.ID <= 0 {
			writeErr(w, http.StatusBadRequest, "Bad request.")
			return
		}
		p.send(w, oc, control.Request{Cmd: cmd, ID: in.ID}, 10*time.Second)
	}
}

// myChan is one channel of the channel manager (?name=#chan).
func (p *Panel) myChan(w http.ResponseWriter, r *http.Request, oc operCtx) {
	name := r.URL.Query().Get("name")
	if name == "" || len(name) > 64 {
		writeErr(w, http.StatusBadRequest, "Bad request.")
		return
	}
	p.send(w, oc, control.Request{Cmd: control.CmdMyChan, Name: name}, 10*time.Second)
}

// chanDo is one ChanServ change: Name (#chan), Kind (the command), Value,
// Mask and Reason (its words; see control.CmdChanDo).
func (p *Panel) chanDo(w http.ResponseWriter, r *http.Request, oc operCtx) {
	var in struct{ Name, Kind, Value, Mask, Reason string }
	if json.NewDecoder(r.Body).Decode(&in) != nil || in.Name == "" || in.Kind == "" || len(in.Name)+len(in.Value)+len(in.Mask) > 300 || len(in.Reason) > 400 {
		writeErr(w, http.StatusBadRequest, "Bad request.")
		return
	}
	p.send(w, oc, control.Request{Cmd: control.CmdChanDo, Name: in.Name, Kind: in.Kind, Value: in.Value, Mask: in.Mask, Reason: in.Reason}, 10*time.Second)
}

// chanMemo reads or deletes a memo of a channel's box.
func (p *Panel) chanMemo(w http.ResponseWriter, r *http.Request, oc operCtx) {
	var in struct {
		Name, Verb string
		ID         int64
	}
	if json.NewDecoder(r.Body).Decode(&in) != nil || in.Name == "" || in.ID <= 0 || (in.Verb != "read" && in.Verb != "del") {
		writeErr(w, http.StatusBadRequest, "Bad request.")
		return
	}
	p.send(w, oc, control.Request{Cmd: control.CmdChanMemo, Name: in.Name, ID: in.ID, Value: in.Verb}, 10*time.Second)
}

// memoSend is MemoServ SEND: To (a nick or #channel) and Text.
func (p *Panel) memoSend(w http.ResponseWriter, r *http.Request, oc operCtx) {
	var in struct{ To, Text string }
	if json.NewDecoder(r.Body).Decode(&in) != nil || in.To == "" || in.Text == "" || len(in.To) > 64 || len(in.Text) > 1000 {
		writeErr(w, http.StatusBadRequest, "Bad request.")
		return
	}
	p.send(w, oc, control.Request{Cmd: control.CmdMemoSend, Name: in.To, Value: in.Text}, 10*time.Second)
}

func (p *Panel) undo(w http.ResponseWriter, r *http.Request, oc operCtx) {
	var in struct{ ID int64 }
	if json.NewDecoder(r.Body).Decode(&in) != nil || in.ID <= 0 {
		writeErr(w, http.StatusBadRequest, "Bad request.")
		return
	}
	p.send(w, oc, control.Request{Cmd: control.CmdUndo, ID: in.ID}, 10*time.Second)
}

func (p *Panel) people(w http.ResponseWriter, r *http.Request, oc operCtx) {
	q := r.URL.Query()
	off, _ := strconv.Atoi(q.Get("offset"))
	p.send(w, oc, control.Request{Cmd: control.CmdPeople, Query: q.Get("q"), Filter: q.Get("filter"), Offset: off, Limit: 50}, 10*time.Second)
}

func (p *Panel) person(w http.ResponseWriter, r *http.Request, oc operCtx) {
	p.send(w, oc, control.Request{Cmd: control.CmdPerson, UID: r.URL.Query().Get("uid")}, 10*time.Second)
}

// act is an action on one person: {"uid", "text", "life"}.
func (p *Panel) act(cmd string) func(http.ResponseWriter, *http.Request, operCtx) {
	return func(w http.ResponseWriter, r *http.Request, oc operCtx) {
		var in struct{ UID, Text, Life string }
		if json.NewDecoder(r.Body).Decode(&in) != nil || in.UID == "" {
			writeErr(w, http.StatusBadRequest, "Bad request.")
			return
		}
		p.send(w, oc, control.Request{Cmd: cmd, UID: in.UID, Value: in.Text, Life: in.Life}, 10*time.Second)
	}
}

func (p *Panel) bans(w http.ResponseWriter, r *http.Request, oc operCtx) {
	p.send(w, oc, control.Request{Cmd: control.CmdBans, Kind: r.URL.Query().Get("kind")}, 10*time.Second)
}

func (p *Panel) banMatch(w http.ResponseWriter, r *http.Request, oc operCtx) {
	q := r.URL.Query()
	p.send(w, oc, control.Request{Cmd: control.CmdBanMatch, Kind: q.Get("kind"), Mask: q.Get("mask")}, 10*time.Second)
}

// banChange is ban-add or ban-del: {"kind", "mask", "reason", "life"}.
func (p *Panel) banChange(cmd string) func(http.ResponseWriter, *http.Request, operCtx) {
	return func(w http.ResponseWriter, r *http.Request, oc operCtx) {
		var in struct{ Kind, Mask, Reason, Life string }
		if json.NewDecoder(r.Body).Decode(&in) != nil || in.Kind == "" || in.Mask == "" {
			writeErr(w, http.StatusBadRequest, "Bad request.")
			return
		}
		p.send(w, oc, control.Request{Cmd: cmd, Kind: in.Kind, Mask: in.Mask, Reason: in.Reason, Life: in.Life}, 10*time.Second)
	}
}

func (p *Panel) operSave(w http.ResponseWriter, r *http.Request, oc operCtx) {
	var in struct {
		Name, Class, Flags, Hosts, Password string
		New                                 bool
	}
	if json.NewDecoder(r.Body).Decode(&in) != nil || in.Name == "" {
		writeErr(w, http.StatusBadRequest, "Bad request.")
		return
	}
	p.send(w, oc, control.Request{Cmd: control.CmdOperSave, Name: in.Name, Class: in.Class, Flags: in.Flags,
		Hosts: in.Hosts, Password: in.Password, New: in.New}, 20*time.Second)
}

func (p *Panel) operDel(w http.ResponseWriter, r *http.Request, oc operCtx) {
	var in struct{ Name string }
	if json.NewDecoder(r.Body).Decode(&in) != nil || in.Name == "" {
		writeErr(w, http.StatusBadRequest, "Bad request.")
		return
	}
	p.send(w, oc, control.Request{Cmd: control.CmdOperDel, Name: in.Name}, 10*time.Second)
}

// linkAct is connect, split or remove: {"name", "reason"}.
func (p *Panel) linkAct(cmd string) func(http.ResponseWriter, *http.Request, operCtx) {
	return func(w http.ResponseWriter, r *http.Request, oc operCtx) {
		var in struct{ Name, Reason string }
		if json.NewDecoder(r.Body).Decode(&in) != nil || in.Name == "" {
			writeErr(w, http.StatusBadRequest, "Bad request.")
			return
		}
		p.send(w, oc, control.Request{Cmd: cmd, Name: in.Name, Reason: in.Reason}, 15*time.Second)
	}
}

func (p *Panel) linkSave(w http.ResponseWriter, r *http.Request, oc operCtx) {
	var in struct {
		control.LinkBlock
		New bool `json:"new"`
	}
	if json.NewDecoder(r.Body).Decode(&in) != nil || in.Name == "" {
		writeErr(w, http.StatusBadRequest, "Bad request.")
		return
	}
	b := in.LinkBlock
	p.send(w, oc, control.Request{Cmd: control.CmdLinkSave, Block: &b, New: in.New}, 15*time.Second)
}

func (p *Panel) channels(w http.ResponseWriter, r *http.Request, oc operCtx) {
	q := r.URL.Query()
	off, _ := strconv.Atoi(q.Get("offset"))
	p.send(w, oc, control.Request{Cmd: control.CmdChannels, Query: q.Get("q"), Filter: q.Get("filter"), Offset: off, Limit: 50}, 10*time.Second)
}

func (p *Panel) channel(w http.ResponseWriter, r *http.Request, oc operCtx) {
	p.send(w, oc, control.Request{Cmd: control.CmdChannel, Name: r.URL.Query().Get("name")}, 10*time.Second)
}

// chanAct is an action on one channel: {"name", "text", "reason", "life"}.
func (p *Panel) chanAct(cmd string) func(http.ResponseWriter, *http.Request, operCtx) {
	return func(w http.ResponseWriter, r *http.Request, oc operCtx) {
		var in struct{ Name, Text, Reason, Life string }
		if json.NewDecoder(r.Body).Decode(&in) != nil || in.Name == "" {
			writeErr(w, http.StatusBadRequest, "Bad request.")
			return
		}
		p.send(w, oc, control.Request{Cmd: cmd, Name: in.Name, Value: in.Text, Reason: in.Reason, Life: in.Life}, 10*time.Second)
	}
}

// backupGet sends one backup file once the server has checked the
// owner's password again: {"name", "password"}.
func (p *Panel) backupGet(w http.ResponseWriter, r *http.Request, oc operCtx) {
	var in struct{ Name, Password string }
	if json.NewDecoder(r.Body).Decode(&in) != nil || !control.IsBackupName(in.Name) {
		writeErr(w, http.StatusBadRequest, "Bad request.")
		return
	}
	resp := p.asked(w, oc, control.Request{Cmd: control.CmdBackupGet, Name: in.Name, Password: in.Password}, 25*time.Second)
	if resp == nil {
		return
	}
	if resp.Backup == nil || filepath.Base(resp.Backup.File) != in.Name {
		writeErr(w, http.StatusBadGateway, "The server sent a strange answer.")
		return
	}
	f, err := os.Open(resp.Backup.File)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "The panel can't read the backup folder: "+err.Error())
		return
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() {
		writeErr(w, http.StatusInternalServerError, "The panel can't read that backup.")
		return
	}
	h := w.Header()
	h.Set("Content-Type", "application/gzip")
	h.Set("Content-Disposition", `attachment; filename="`+in.Name+`"`)
	h.Set("Content-Length", strconv.FormatInt(fi.Size(), 10))
	h.Set("Cache-Control", "no-store")
	io.Copy(w, f)
}

// mapPlace puts a server on the network map: {"name", "place", "lat",
// "lon"}; no lat/lon takes it off.
func (p *Panel) mapPlace(w http.ResponseWriter, r *http.Request, oc operCtx) {
	var in struct {
		Name, Place string
		Lat, Lon    *float64
	}
	if json.NewDecoder(r.Body).Decode(&in) != nil || in.Name == "" || (in.Lat == nil) != (in.Lon == nil) {
		writeErr(w, http.StatusBadRequest, "Bad request.")
		return
	}
	p.send(w, oc, control.Request{Cmd: control.CmdMapPlace, Name: in.Name, Value: in.Place, Lat: in.Lat, Lon: in.Lon}, 10*time.Second)
}

// mapFiles are the network map's outlines, gzipped once at start. They
// hold no secrets (public domain country outlines), so no sign-in.
var mapFiles = func() map[string][]byte {
	out := map[string][]byte{}
	ents, _ := uiFiles.ReadDir("ui/map")
	for _, e := range ents {
		if !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		raw, _ := uiFiles.ReadFile("ui/map/" + e.Name())
		var b bytes.Buffer
		zw, _ := gzip.NewWriterLevel(&b, gzip.BestCompression)
		zw.Write(raw)
		zw.Close()
		out[e.Name()] = b.Bytes()
		out[e.Name()+"#raw"] = raw
	}
	return out
}()

func (p *Panel) mapFile(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	gz, ok := mapFiles[name]
	if !ok || strings.Contains(name, "#") {
		http.NotFound(w, r)
		return
	}
	h := w.Header()
	h.Set("Content-Type", "application/json")
	h.Set("Cache-Control", "public, max-age=604800")
	h.Set("Vary", "Accept-Encoding")
	if strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
		h.Set("Content-Encoding", "gzip")
		w.Write(gz)
		return
	}
	w.Write(mapFiles[name+"#raw"])
}

// ipLocked reports how long ip must still wait after wrong sign-ins.
func (p *Panel) ipLocked(ip string) time.Duration {
	p.mu.Lock()
	defer p.mu.Unlock()
	f := p.fails[ip]
	if f == nil {
		return 0
	}
	return f.until.Sub(p.now())
}

// ipFailed counts a wrong sign-in from ip and says how many are left.
func (p *Panel) ipFailed(ip string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := p.now()
	for k, old := range p.fails { // forget addresses whose tries and lock are over
		if now.Sub(old.last) > ipLockFor && now.After(old.until) {
			delete(p.fails, k)
		}
	}
	f := p.fails[ip]
	if f == nil || now.Sub(f.last) > ipLockFor {
		f = &ipFails{}
		p.fails[ip] = f
	}
	f.n++
	f.last = now
	if f.n >= ipMaxFails {
		f.n = 0
		f.until = now.Add(ipLockFor)
		return 0
	}
	return ipMaxFails - f.n
}

func waitWords(d time.Duration) string {
	m := int((d + time.Minute - 1) / time.Minute)
	if m <= 1 {
		return "a minute"
	}
	return strconv.Itoa(m) + " minutes"
}

// waitNoun is waitWords before a noun: "a 10 minute wait".
func waitNoun(d time.Duration) string {
	m := int((d + time.Minute - 1) / time.Minute)
	return strconv.Itoa(max(m, 1)) + " minute"
}

func (p *Panel) login(w http.ResponseWriter, r *http.Request) {
	ip := p.clientIP(r)
	if d := p.ipLocked(ip); d > 0 {
		writeErr(w, http.StatusTooManyRequests, "Too many wrong tries from here. Try again in "+waitWords(d)+".")
		return
	}
	var in struct{ Nick, Password string }
	if json.NewDecoder(r.Body).Decode(&in) != nil {
		writeErr(w, http.StatusBadRequest, "Bad request.")
		return
	}
	resp, err := p.ask(control.Request{Cmd: control.CmdPanelLogin, Nick: in.Nick, Password: in.Password, IP: ip}, 25*time.Second)
	switch {
	case errors.Is(err, control.ErrNotRunning):
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "The server isn't running.", "offline": true})
		return
	case resp == nil && err != nil:
		writeErr(w, http.StatusBadGateway, "The server didn't answer: "+err.Error())
		return
	case resp.Oper == nil:
		msg := resp.Error
		if msg == "" {
			msg = "Wrong nick or password."
		}
		if strings.HasPrefix(msg, "Wrong") {
			switch left := p.ipFailed(ip); {
			case left == 0:
				msg += " Too many wrong tries: wait " + waitWords(ipLockFor) + "."
			case left == 1:
				msg += " 1 try left, then a " + waitNoun(ipLockFor) + " wait."
			default:
				msg += " " + strconv.Itoa(left) + " tries left, then a " + waitNoun(ipLockFor) + " wait."
			}
		}
		writeErr(w, http.StatusUnauthorized, msg)
		return
	}
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		writeErr(w, http.StatusInternalServerError, "Could not start a session.")
		return
	}
	tok := base64.RawURLEncoding.EncodeToString(raw[:])
	now := p.now()
	p.mu.Lock()
	delete(p.fails, ip)
	for k, s := range p.sessions { // tidy up old ones
		if now.Sub(s.last) > p.idle || now.Sub(s.started) > maxSession {
			delete(p.sessions, k)
		}
	}
	p.sessions[tokenKey(tok)] = &session{oper: *resp.Oper, ip: ip, started: now, last: now}
	p.mu.Unlock()
	http.SetCookie(w, &http.Cookie{Name: cookieName, Value: tok, Path: "/", HttpOnly: true,
		SameSite: http.SameSiteStrictMode, Secure: p.secure(r), MaxAge: int(maxSession / time.Second)})
	o := *resp.Oper
	o.Seal = "" // stays between the panel and the server
	o.IP = ip
	writeJSON(w, http.StatusOK, map[string]any{"oper": &o})
}

// hello tells the sign-in page whether members may sign in (no session
// needed, nothing else told).
func (p *Panel) hello(w http.ResponseWriter, r *http.Request) {
	resp, err := p.ask(control.Request{Cmd: control.CmdPanelHello}, 5*time.Second)
	if err != nil || resp == nil || resp.Hello == nil {
		writeJSON(w, http.StatusOK, map[string]any{"members": false})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"members": resp.Hello.Members})
}

func (p *Panel) logout(w http.ResponseWriter, r *http.Request) {
	if key, _ := p.current(r, false); key != "" {
		p.drop(key)
	}
	http.SetCookie(w, &http.Cookie{Name: cookieName, Value: "", Path: "/", HttpOnly: true,
		SameSite: http.SameSiteStrictMode, Secure: p.secure(r), MaxAge: -1})
	writeJSON(w, http.StatusOK, map[string]any{})
}

// live streams the overview: "status" every few seconds, "events" when
// What happened has something new, "signin" when the session ended. It
// does not count as a click, so an idle tab still signs out.
func (p *Panel) live(w http.ResponseWriter, r *http.Request) {
	key, s := p.current(r, false)
	if s == nil {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "Please sign in.", "signin": true})
		return
	}
	p.mu.Lock()
	member := s.oper.Member
	p.mu.Unlock()
	if member { // the live overview is for opers
		writeErr(w, http.StatusForbidden, "Only opers can do that.")
		return
	}
	fl, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, http.StatusInternalServerError, "No streaming here.")
		return
	}
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	sendEv := func(name string, v any) {
		b, _ := json.Marshal(v)
		w.Write([]byte("event: " + name + "\ndata: "))
		w.Write(b)
		w.Write([]byte("\n\n"))
		fl.Flush()
	}
	var lastEvents string
	tick := time.NewTicker(livePeriod)
	defer tick.Stop()
	for first := true; ; first = false {
		if !first {
			select {
			case <-r.Context().Done():
				return
			case <-tick.C:
			}
		}
		if k, s2 := p.current(r, false); s2 == nil || k != key {
			sendEv("signin", map[string]any{"error": "You were signed out."})
			return
		}
		p.mu.Lock()
		as, ip, seal := s.oper.Account, s.ip, s.oper.Seal
		p.mu.Unlock()
		resp, err := p.ask(control.Request{Cmd: control.CmdStatus, As: as, IP: ip, Seal: seal}, 5*time.Second)
		switch {
		case err != nil && resp == nil:
			sendEv("offline", map[string]any{"error": "The server isn't answering."})
			continue
		case resp.Denied == "signin":
			p.drop(key)
			sendEv("signin", map[string]any{"error": resp.Error})
			return
		}
		sendEv("status", resp.Status)
		ev, err := p.ask(control.Request{Cmd: control.CmdPanelLog, As: as, IP: ip, Seal: seal}, 5*time.Second)
		if err == nil && ev != nil {
			b, _ := json.Marshal(ev.Events)
			if first || string(b) != lastEvents {
				lastEvents = string(b)
				sendEv("events", ev.Events)
			}
		}
	}
}
