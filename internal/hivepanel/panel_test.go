package hivepanel

import (
	"bufio"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fhazal/beeircd/internal/control"
)

// fakeServer answers like BeeIRCd would for oper "Bee" (password "pw").
type fakeServer struct {
	mu      sync.Mutex
	gone    bool // the oper entry was removed
	reqs    []control.Request
	offline bool
	bkDir   string // backup-get answers with files here
}

func (f *fakeServer) ask(r control.Request, _ time.Duration) (*control.Response, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reqs = append(f.reqs, r)
	if f.offline {
		return nil, control.ErrNotRunning
	}
	if r.Cmd == control.CmdPanelHello {
		return &control.Response{Hello: &control.Hello{Members: true}}, nil
	}
	if r.As == "Bel" && r.Seal == "m1" { // a member session: only its own pages
		if r.Cmd == control.CmdMyNick || r.Cmd == control.CmdPanelWho {
			return &control.Response{MyNick: &control.MyNick{Name: "Bel"}, Oper: &control.PanelOper{Account: "Bel", Member: true, Seal: "m1"}}, nil
		}
		switch r.Cmd {
		case control.CmdMyHome:
			return &control.Response{Home: &control.MyHome{Name: "Bel", Unread: 1}}, nil
		case control.CmdMyMemos:
			return &control.Response{Memos: &control.MyMemos{Memos: []control.MyMemo{{ID: 4, From: "ayu", Text: "hi"}}, Limit: 20}}, nil
		case control.CmdMyChan:
			return &control.Response{Chan: &control.MyChan{Name: r.Name, Level: "Founder"}}, nil
		case control.CmdChanDo, control.CmdChanMemo:
			return &control.Response{Done: &control.Done{Text: fmt.Sprintf("%s %s %s %s %s %s %d", r.Cmd, r.Name, r.Kind, r.Value, r.Mask, r.Reason, r.ID)}}, nil
		case control.CmdMemoRead, control.CmdMemoDel, control.CmdMemoSend:
			return &control.Response{Done: &control.Done{Text: fmt.Sprintf("%s %d %s %s", r.Cmd, r.ID, r.Name, r.Value)}}, nil
		}
		return &control.Response{Error: "Only opers can do that.", Denied: "flag"}, nil
	}
	if r.Cmd == control.CmdPanelLogin {
		if r.Nick == "Bel" && r.Password == "mpw" && r.IP != "" {
			return &control.Response{Oper: &control.PanelOper{Account: "Bel", Member: true, Seal: "m1"}}, nil
		}
		if r.Nick == "Bee" && r.Password == "pw" && r.IP != "" {
			return &control.Response{Oper: &control.PanelOper{Account: "Bee", Flags: "acghks", Owner: true, Seal: "s1"}}, nil
		}
		return &control.Response{Error: "Wrong nick or password.", Denied: "signin"}, nil
	}
	if r.As != "Bee" || r.Seal != "s1" || f.gone {
		return &control.Response{Error: "Please sign in again.", Denied: "signin"}, nil
	}
	switch r.Cmd {
	case control.CmdStatus:
		return &control.Response{Status: &control.Status{Server: "irc.test", Online: 3, MaxUsers: 10}}, nil
	case control.CmdPanelLog:
		return &control.Response{Events: []control.Event{{Who: "Bee", What: "signed in"}}}, nil
	case control.CmdPanelWho:
		return &control.Response{Oper: &control.PanelOper{Account: "Bee", Flags: "acghks", Owner: true, Seal: "s1"}}, nil
	case control.CmdPeople:
		return &control.Response{People: &control.People{Online: 2, Matched: 1, Rows: []control.PersonBrief{{UID: "0BEAAAAAB", Nick: r.Query}}}}, nil
	case control.CmdOperSave:
		if r.Password != "" && r.Name == "ember" && r.New && r.Hosts == "*@*" && r.Flags == "g" {
			return &control.Response{Changed: &control.Changed{Name: "ember", New: "g", ID: 9}}, nil
		}
		return &control.Response{Error: "unexpected"}, nil
	case control.CmdBackupGet:
		if r.Password != "pw" {
			return &control.Response{Error: "Wrong password."}, nil
		}
		if r.Name == "beeircd-2026-11-13-0001.tar.gz" { // a strange answer
			return &control.Response{Backup: &control.Backup{File: filepath.Join(f.bkDir, "beeircd.conf")}}, nil
		}
		return &control.Response{Backup: &control.Backup{File: filepath.Join(f.bkDir, r.Name)}}, nil
	case control.CmdMapPlace:
		if r.Lat == nil {
			return &control.Response{Done: &control.Done{Text: "off " + r.Name}}, nil
		}
		return &control.Response{Done: &control.Done{Text: fmt.Sprintf("%s %s %.4f %.4f", r.Name, r.Value, *r.Lat, *r.Lon)}}, nil
	case control.CmdChanSuspend:
		return &control.Response{Done: &control.Done{Text: r.Cmd + " " + r.Name + " " + r.Reason + " " + r.Life}}, nil
	case control.CmdLinkSave:
		if r.Block == nil || r.Block.Name != "leaf.example.net" || r.Block.Port != 7001 || !r.Block.Autoconnect || !r.New {
			return &control.Response{Error: "unexpected"}, nil
		}
		return &control.Response{Done: &control.Done{Text: "Saved and applied."}}, nil
	case control.CmdLinkSplit:
		return &control.Response{Done: &control.Done{Text: "split " + r.Name + " " + r.Reason}}, nil
	case control.CmdBanAdd, control.CmdBanDel:
		return &control.Response{Done: &control.Done{Text: r.Cmd + " " + r.Kind + " " + r.Mask + " " + r.Reason + " " + r.Life}}, nil
	case control.CmdBanMatch:
		return &control.Response{Match: &control.Match{Mask: r.Mask, People: 2}}, nil
	case control.CmdKill, control.CmdNotice, control.CmdBan:
		return &control.Response{Done: &control.Done{Text: r.Cmd + " " + r.UID + " " + r.Value + " " + r.Life}}, nil
	case control.CmdSet:
		return &control.Response{Changed: &control.Changed{Name: r.Name, Old: "5", New: r.Value, ID: 7}}, nil
	}
	return &control.Response{Error: "nope"}, nil
}

type rig struct {
	t   *testing.T
	p   *Panel
	f   *fakeServer
	now time.Time
}

func newRig(t *testing.T) *rig {
	rg := &rig{t: t, f: &fakeServer{}, now: time.Date(2026, 11, 12, 20, 0, 0, 0, time.UTC)}
	rg.p = New(Options{Ask: rg.f.ask, Idle: 30 * time.Minute, Now: func() time.Time { return rg.now }, TrustedProxy: []string{"10.0.0.1"}})
	return rg
}

// do sends a request as if from a browser at 127.0.0.1.
func (rg *rig) do(method, path, body, cookie string, hdr map[string]string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "http://127.0.0.1:8787"+path, strings.NewReader(body))
	r.RemoteAddr = "127.0.0.1:50000"
	if method == http.MethodPost {
		r.Header.Set("X-Hive", "1")
		r.Header.Set("Content-Type", "application/json")
	}
	if cookie != "" {
		r.AddCookie(&http.Cookie{Name: cookieName, Value: cookie})
	}
	for k, v := range hdr {
		if v == "" {
			r.Header.Del(k)
		} else {
			r.Header.Set(k, v)
		}
	}
	w := httptest.NewRecorder()
	rg.p.ServeHTTP(w, r)
	return w
}

func (rg *rig) login() string {
	w := rg.do("POST", "/api/login", `{"nick":"Bee","password":"pw"}`, "", nil)
	if w.Code != 200 {
		rg.t.Fatalf("login: %d %s", w.Code, w.Body)
	}
	for _, c := range w.Result().Cookies() {
		if c.Name == cookieName {
			if !c.HttpOnly || c.SameSite != http.SameSiteStrictMode || c.Secure {
				rg.t.Errorf("cookie flags: %+v", c)
			}
			return c.Value
		}
	}
	rg.t.Fatal("no session cookie")
	return ""
}

func TestPageAndHeaders(t *testing.T) {
	rg := newRig(t)
	w := rg.do("GET", "/", "", "", nil)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `<script src="app.js" defer>`) {
		t.Fatalf("page: %d", w.Code)
	}
	csp := w.Header().Get("Content-Security-Policy")
	if !strings.Contains(csp, "script-src 'self'") || strings.Contains(csp, "unsafe") || w.Header().Get("X-Frame-Options") != "DENY" {
		t.Errorf("headers: %v", w.Header())
	}
	for _, f := range []string{"/app.js", "/app.css", "/fonts/inter.woff2", "/fonts/space-grotesk.woff2", "/fonts/jetbrains-mono.woff2"} {
		if w := rg.do("GET", f, "", "", nil); w.Code != 200 || w.Body.Len() < 1000 {
			t.Errorf("%s: %d %d bytes", f, w.Code, w.Body.Len())
		}
	}
	// A new build must show at the next load: no cached code or styles.
	for _, f := range []string{"/app.js", "/app.css", "/netmap.js"} {
		if cc := rg.do("GET", f, "", "", nil).Header().Get("Cache-Control"); cc != "no-cache" {
			t.Errorf("%s Cache-Control = %q", f, cc)
		}
	}
	if w := rg.do("GET", "/fonts/", "", "", nil); w.Code == 200 && strings.Contains(w.Body.String(), "<a href") {
		t.Errorf("font folder lists its files")
	}
	// DNS rebinding: another name pointing at 127.0.0.1 gets nothing.
	r := httptest.NewRequest("GET", "http://evil.example:8787/", nil)
	r.RemoteAddr = "127.0.0.1:50000"
	w = httptest.NewRecorder()
	rg.p.ServeHTTP(w, r)
	if w.Code != http.StatusMisdirectedRequest {
		t.Errorf("foreign host name: %d", w.Code)
	}
}

func TestLoginSessionAndCSRF(t *testing.T) {
	rg := newRig(t)
	if w := rg.do("GET", "/api/status", "", "", nil); w.Code != 401 || !strings.Contains(w.Body.String(), `"signin":true`) {
		t.Errorf("no session: %d %s", w.Code, w.Body)
	}
	tok := rg.login()
	if w := rg.do("GET", "/api/status", "", tok, nil); w.Code != 200 || !strings.Contains(w.Body.String(), `"online":3`) {
		t.Errorf("status: %d %s", w.Code, w.Body)
	}
	last := rg.f.reqs[len(rg.f.reqs)-1]
	if last.As != "Bee" || last.IP != "127.0.0.1" {
		t.Errorf("request to the server doesn't name the oper: %+v", last)
	}
	// Cross-site: no X-Hive header, or another Origin.
	if w := rg.do("POST", "/api/set", `{"name":"max-users","value":"9"}`, tok, map[string]string{"X-Hive": ""}); w.Code != 403 {
		t.Errorf("POST without X-Hive: %d", w.Code)
	}
	if w := rg.do("POST", "/api/set", `{"name":"max-users","value":"9"}`, tok, map[string]string{"Origin": "http://evil.example"}); w.Code != 403 {
		t.Errorf("POST from another origin: %d", w.Code)
	}
	if w := rg.do("POST", "/api/set", `{"name":"max-users","value":"9"}`, tok, map[string]string{"Origin": "http://127.0.0.1:8787"}); w.Code != 200 || !strings.Contains(w.Body.String(), `"new":"9"`) {
		t.Errorf("POST from the panel: %d %s", w.Code, w.Body)
	}
	// Same cookie from another address: not this session.
	r := httptest.NewRequest("GET", "http://127.0.0.1:8787/api/status", nil)
	r.RemoteAddr = "127.0.0.2:5000"
	r.AddCookie(&http.Cookie{Name: cookieName, Value: tok})
	w := httptest.NewRecorder()
	rg.p.ServeHTTP(w, r)
	if w.Code != 401 {
		t.Errorf("cookie from another address: %d", w.Code)
	}
	// Idle too long.
	tok = rg.login()
	rg.now = rg.now.Add(31 * time.Minute)
	if w := rg.do("GET", "/api/status", "", tok, nil); w.Code != 401 {
		t.Errorf("after 31 idle minutes: %d", w.Code)
	}
	// Sign out.
	tok = rg.login()
	rg.do("POST", "/api/logout", "", tok, nil)
	if w := rg.do("GET", "/api/status", "", tok, nil); w.Code != 401 {
		t.Errorf("after sign out: %d", w.Code)
	}
	// The server says the oper is gone: signed out at once.
	tok = rg.login()
	rg.f.gone = true
	if w := rg.do("GET", "/api/status", "", tok, nil); w.Code != 401 {
		t.Errorf("removed oper: %d", w.Code)
	}
	rg.f.gone = false
	if w := rg.do("GET", "/api/status", "", tok, nil); w.Code != 401 {
		t.Errorf("session survived the server's sign-out: %d", w.Code)
	}
	// Server down.
	tok = rg.login()
	rg.f.offline = true
	if w := rg.do("GET", "/api/status", "", tok, nil); w.Code != 503 || !strings.Contains(w.Body.String(), `"offline":true`) {
		t.Errorf("offline: %d %s", w.Code, w.Body)
	}
}

func TestLoginLimitPerAddress(t *testing.T) {
	rg := newRig(t)
	var last string
	for i := 0; i < 5; i++ {
		w := rg.do("POST", "/api/login", `{"nick":"Bee","password":"bad"}`, "", nil)
		last = w.Body.String()
		if w.Code != 401 {
			t.Fatalf("try %d: %d", i, w.Code)
		}
		if i == 0 && (!strings.Contains(last, "4 tries left") || !strings.Contains(last, "registered with NickServ")) {
			t.Errorf("first wrong try: %s", last)
		}
	}
	if !strings.Contains(last, "wait 10 minutes") {
		t.Errorf("fifth wrong try: %s", last)
	}
	n := len(rg.f.reqs)
	if w := rg.do("POST", "/api/login", `{"nick":"Bee","password":"pw"}`, "", nil); w.Code != 429 || len(rg.f.reqs) != n {
		t.Errorf("locked address still asks the server: %d", w.Code)
	}
	rg.now = rg.now.Add(11 * time.Minute)
	rg.login()
}

func TestProxyAddress(t *testing.T) {
	rg := newRig(t)
	r := httptest.NewRequest("POST", "https://panel.example/api/login", strings.NewReader(`{"nick":"Bee","password":"pw"}`))
	r.RemoteAddr = "10.0.0.1:443"
	r.Header.Set("X-Hive", "1")
	r.Header.Set("X-Forwarded-For", "198.51.100.7")
	r.Header.Set("X-Forwarded-Proto", "https")
	w := httptest.NewRecorder()
	rg.p.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("login via proxy: %d %s", w.Code, w.Body)
	}
	if ip := rg.f.reqs[len(rg.f.reqs)-1].IP; ip != "198.51.100.7" {
		t.Errorf("address via proxy: %s", ip)
	}
	if c := w.Result().Cookies(); len(c) == 0 || !c[0].Secure {
		t.Errorf("cookie not Secure behind https proxy: %+v", c)
	}
	// Anyone else can't claim an address.
	r = httptest.NewRequest("GET", "http://127.0.0.1:8787/", nil)
	r.RemoteAddr = "127.0.0.1:5"
	r.Header.Set("X-Forwarded-For", "198.51.100.7")
	if ip := rg.p.clientIP(r); ip != "127.0.0.1" {
		t.Errorf("untrusted X-Forwarded-For believed: %s", ip)
	}
}

func TestLiveStream(t *testing.T) {
	rg := newRig(t)
	tok := rg.login()
	srv := httptest.NewServer(rg.p)
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", srv.URL+"/api/live", nil)
	req.AddCookie(&http.Cookie{Name: cookieName, Value: tok})
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatalf("content type %q", resp.Header.Get("Content-Type"))
	}
	sc := bufio.NewScanner(resp.Body)
	got := map[string]string{}
	var ev string
	for sc.Scan() && len(got) < 2 {
		line := sc.Text()
		if strings.HasPrefix(line, "event: ") {
			ev = line[7:]
		} else if strings.HasPrefix(line, "data: ") {
			got[ev] = line[6:]
		}
	}
	var st control.Status
	if json.Unmarshal([]byte(got["status"]), &st) != nil || st.Online != 3 || !strings.Contains(got["events"], "signed in") {
		t.Errorf("live: %v", got)
	}
}

func TestPeopleRoutes(t *testing.T) {
	rg := newRig(t)
	tok := rg.login()
	if w := rg.do("GET", "/api/people?q=rov&filter=guests&offset=50", "", tok, nil); w.Code != 200 || !strings.Contains(w.Body.String(), `"nick":"rov"`) {
		t.Errorf("people: %d %s", w.Code, w.Body)
	}
	last := rg.f.reqs[len(rg.f.reqs)-1]
	if last.Query != "rov" || last.Filter != "guests" || last.Offset != 50 || last.Limit != 50 {
		t.Errorf("people request: %+v", last)
	}
	w := rg.do("POST", "/api/ban", `{"uid":"0BEAAAAAB","text":"spam","life":"7d"}`, tok, nil)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "ban 0BEAAAAAB spam 7d") {
		t.Errorf("ban: %d %s", w.Code, w.Body)
	}
	if w := rg.do("POST", "/api/kill", `{"text":"no uid"}`, tok, nil); w.Code != 400 {
		t.Errorf("kill without uid: %d", w.Code)
	}
	if w := rg.do("POST", "/api/kill", `{"uid":"x"}`, tok, map[string]string{"X-Hive": ""}); w.Code != 403 {
		t.Errorf("kill without X-Hive: %d", w.Code)
	}
}

func TestBanRoutes(t *testing.T) {
	rg := newRig(t)
	tok := rg.login()
	w := rg.do("POST", "/api/ban-add", `{"kind":"zline","mask":"198.51.100.0/24","reason":"bots","life":"1h"}`, tok, nil)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "ban-add zline 198.51.100.0/24 bots 1h") {
		t.Errorf("ban-add: %d %s", w.Code, w.Body)
	}
	if w := rg.do("POST", "/api/ban-del", `{"kind":"zline"}`, tok, nil); w.Code != 400 {
		t.Errorf("ban-del without mask: %d", w.Code)
	}
	if w := rg.do("GET", "/api/ban-match?kind=akill&mask=x", "", tok, nil); w.Code != 200 || !strings.Contains(w.Body.String(), `"people":2`) {
		t.Errorf("ban-match: %d %s", w.Code, w.Body)
	}
}

func TestOperRoutes(t *testing.T) {
	rg := newRig(t)
	tok := rg.login()
	w := rg.do("POST", "/api/oper-save", `{"name":"ember","flags":"g","hosts":"*@*","password":"Secret-123","new":true}`, tok, nil)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"id":9`) {
		t.Errorf("oper-save: %d %s", w.Code, w.Body)
	}
	if w := rg.do("POST", "/api/oper-del", `{}`, tok, nil); w.Code != 400 {
		t.Errorf("oper-del without name: %d", w.Code)
	}
}

func TestLinkRoutes(t *testing.T) {
	rg := newRig(t)
	tok := rg.login()
	w := rg.do("POST", "/api/link-save", `{"name":"leaf.example.net","host":"10.0.0.2","port":7001,"fingerprint":"x","autoconnect":true,"new":true}`, tok, nil)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "Saved and applied") {
		t.Errorf("link-save: %d %s", w.Code, w.Body)
	}
	if w := rg.do("POST", "/api/link-split", `{"name":"leaf.example.net","reason":"maintenance"}`, tok, nil); w.Code != 200 || !strings.Contains(w.Body.String(), "split leaf.example.net maintenance") {
		t.Errorf("link-split: %d %s", w.Code, w.Body)
	}
	if w := rg.do("POST", "/api/link-connect", `{}`, tok, nil); w.Code != 400 {
		t.Errorf("link-connect without name: %d", w.Code)
	}
}

func TestSealNeverReachesBrowser(t *testing.T) {
	rg := newRig(t)
	w := rg.do("POST", "/api/login", `{"nick":"Bee","password":"pw"}`, "", nil)
	if strings.Contains(w.Body.String(), "seal") || strings.Contains(w.Body.String(), "s1") {
		t.Errorf("login answer: %s", w.Body)
	}
	tok := ""
	for _, c := range w.Result().Cookies() {
		tok = c.Value
	}
	if w := rg.do("GET", "/api/me", "", tok, nil); w.Code != 200 || strings.Contains(w.Body.String(), "seal") {
		t.Errorf("me: %d %s", w.Code, w.Body)
	}
}

// Old wrong-try records are forgotten, so the list can't grow forever.
func TestLoginFailsForgotten(t *testing.T) {
	rg := newRig(t)
	for i := 0; i < 50; i++ {
		rg.p.ipFailed(fmt.Sprintf("192.0.2.%d", i))
	}
	rg.now = rg.now.Add(11 * time.Minute)
	rg.p.ipFailed("198.51.100.1")
	if n := len(rg.p.fails); n != 1 {
		t.Errorf("%d records kept", n)
	}
}

func TestChannelRoutes(t *testing.T) {
	rg := newRig(t)
	tok := rg.login()
	w := rg.do("POST", "/api/chan-suspend", `{"name":"#spam","reason":"bots","life":"7d"}`, tok, nil)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "chan-suspend #spam bots 7d") {
		t.Errorf("chan-suspend: %d %s", w.Code, w.Body)
	}
	if w := rg.do("POST", "/api/chan-topic", `{"text":"no name"}`, tok, nil); w.Code != 400 {
		t.Errorf("chan-topic without name: %d", w.Code)
	}
}

func TestBackupDownload(t *testing.T) {
	rg := newRig(t)
	rg.f.bkDir = t.TempDir()
	name := "beeircd-2026-11-13-0400.tar.gz"
	os.WriteFile(filepath.Join(rg.f.bkDir, name), []byte("tar-bytes"), 0o600)
	os.WriteFile(filepath.Join(rg.f.bkDir, "beeircd.conf"), []byte("secret"), 0o600)
	tok := rg.login()

	w := rg.do("POST", "/api/backup-get", `{"name":"`+name+`","password":"pw"}`, tok, nil)
	if w.Code != 200 || w.Body.String() != "tar-bytes" || w.Header().Get("Content-Type") != "application/gzip" ||
		w.Header().Get("Content-Disposition") != `attachment; filename="`+name+`"` || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("download: %d %v %q", w.Code, w.Header(), w.Body)
	}
	if last := rg.f.reqs[len(rg.f.reqs)-1]; last.Cmd != control.CmdBackupGet || last.Password != "pw" || last.As != "Bee" || last.Seal != "s1" {
		t.Errorf("asked: %+v", last)
	}
	if w := rg.do("POST", "/api/backup-get", `{"name":"`+name+`","password":"no"}`, tok, nil); w.Code != 422 || !strings.Contains(w.Body.String(), "Wrong password.") {
		t.Errorf("wrong password: %d %s", w.Code, w.Body)
	}
	if rg.p.sessions[tokenKey(tok)] == nil {
		t.Error("a wrong password signed the owner out")
	}
	n := len(rg.f.reqs)
	for _, bad := range []string{"../beeircd.conf", "beeircd.conf", "", "beeircd-x.tar.gz/../a"} {
		if w := rg.do("POST", "/api/backup-get", `{"name":"`+bad+`","password":"pw"}`, tok, nil); w.Code != 400 {
			t.Errorf("%q: %d", bad, w.Code)
		}
	}
	if len(rg.f.reqs) != n {
		t.Error("bad names reached the server")
	}
	// The server names another file: nothing is sent.
	if w := rg.do("POST", "/api/backup-get", `{"name":"beeircd-2026-11-13-0001.tar.gz","password":"pw"}`, tok, nil); w.Code != 502 || strings.Contains(w.Body.String(), "secret") {
		t.Errorf("strange answer: %d %s", w.Code, w.Body)
	}
	// Gone from disk between the check and the read.
	if w := rg.do("POST", "/api/backup-get", `{"name":"beeircd-2026-11-12-0400.tar.gz","password":"pw"}`, tok, nil); w.Code != 500 {
		t.Errorf("missing file: %d %s", w.Code, w.Body)
	}
	// Cross-site and signed-out requests.
	if w := rg.do("POST", "/api/backup-get", `{"name":"`+name+`","password":"pw"}`, tok, map[string]string{"X-Hive": ""}); w.Code != 403 {
		t.Errorf("no X-Hive: %d", w.Code)
	}
	if w := rg.do("POST", "/api/backup-get", `{"name":"`+name+`","password":"pw"}`, "", nil); w.Code != 401 {
		t.Errorf("signed out: %d", w.Code)
	}
	if w := rg.do("GET", "/api/backup-get?name="+name, "", tok, nil); w.Code != 405 {
		t.Errorf("GET: %d", w.Code)
	}
}

func TestMapRoutes(t *testing.T) {
	rg := newRig(t)
	// The outlines need no sign-in, come gzipped when asked, and are JSON.
	r := httptest.NewRequest("GET", "http://127.0.0.1:8787/map/world-110.json", nil)
	r.RemoteAddr = "127.0.0.1:50000"
	r.Header.Set("Accept-Encoding", "gzip, br")
	w := httptest.NewRecorder()
	rg.p.ServeHTTP(w, r)
	if w.Code != 200 || w.Header().Get("Content-Encoding") != "gzip" || w.Header().Get("Content-Type") != "application/json" || w.Body.Len() > 40000 {
		t.Fatalf("gzip: %d %v %d bytes", w.Code, w.Header(), w.Body.Len())
	}
	zr, err := gzip.NewReader(w.Body)
	if err != nil {
		t.Fatal(err)
	}
	var pack struct {
		V     int
		Src   string
		Rings [][]int
	}
	if err := json.NewDecoder(zr).Decode(&pack); err != nil || pack.V != 1 || len(pack.Rings) < 200 || !strings.Contains(pack.Src, "Natural Earth") {
		t.Fatalf("pack: %v %d %q", err, len(pack.Rings), pack.Src)
	}
	if w := rg.do("GET", "/map/world-50.json", "", "", nil); w.Code != 200 || w.Header().Get("Content-Encoding") != "" || !strings.HasPrefix(w.Body.String(), `{"v":1`) {
		t.Errorf("plain: %d %v", w.Code, w.Header())
	}
	for _, bad := range []string{"/map/world-50.json%23raw", "/map/nope.json", "/map/../app.js"} {
		if w := rg.do("GET", bad, "", "", nil); w.Code == 200 {
			t.Errorf("%s: %d", bad, w.Code)
		}
	}
	if w := rg.do("GET", "/netmap.js", "", "", nil); w.Code != 200 || !strings.Contains(w.Body.String(), "HiveNetMap") {
		t.Errorf("netmap.js: %d", w.Code)
	}
	tok := rg.login()
	if w := rg.do("POST", "/api/map-place", `{"name":"eu.x.net","place":"Frankfurt","lat":50.11,"lon":8.68}`, tok, nil); w.Code != 200 || !strings.Contains(w.Body.String(), "eu.x.net Frankfurt 50.1100 8.6800") {
		t.Errorf("place: %d %s", w.Code, w.Body)
	}
	if w := rg.do("POST", "/api/map-place", `{"name":"eu.x.net"}`, tok, nil); w.Code != 200 || !strings.Contains(w.Body.String(), "off eu.x.net") {
		t.Errorf("remove: %d %s", w.Code, w.Body)
	}
	for _, bad := range []string{`{"name":"eu.x.net","lat":1}`, `{"lat":1,"lon":2}`, `{"name":"a","lat":"x","lon":1}`} {
		if w := rg.do("POST", "/api/map-place", bad, tok, nil); w.Code != 400 {
			t.Errorf("%s: %d", bad, w.Code)
		}
	}
}

// TestAutoRefreshIsNotAClick: the page's own timed refreshes (X-Hive-Auto)
// must not keep a session alive, or an open, unattended Overview never
// signs out. A real click still does.
func TestAutoRefreshIsNotAClick(t *testing.T) {
	rg := newRig(t)
	tok := rg.login()
	auto := map[string]string{"X-Hive-Auto": "1"}
	for i := 0; i < 4; i++ { // a card refreshing every 10 minutes for 40 minutes
		rg.now = rg.now.Add(10 * time.Minute)
		rg.do("GET", "/api/links", "", tok, auto)
	}
	if w := rg.do("GET", "/api/status", "", tok, auto); w.Code != 401 {
		t.Errorf("auto refreshes kept the session alive past the idle limit: %d", w.Code)
	}
	tok = rg.login()
	rg.now = rg.now.Add(20 * time.Minute)
	rg.do("GET", "/api/status", "", tok, nil) // a click
	rg.now = rg.now.Add(20 * time.Minute)
	if w := rg.do("GET", "/api/status", "", tok, auto); w.Code != 200 {
		t.Errorf("a click 20 minutes ago should keep the session: %d", w.Code)
	}
}

// A member session (NickServ password, Hive Chat h1) gets its own pages;
// the live overview and the admin routes stay closed to it. The sign-in
// page may ask whether members can sign in, without a session.
func TestMemberSession(t *testing.T) {
	rg := newRig(t)
	if w := rg.do("GET", "/api/hello", "", "", nil); w.Code != 200 || !strings.Contains(w.Body.String(), `"members":true`) {
		t.Fatalf("hello: %d %s", w.Code, w.Body)
	}
	w := rg.do("POST", "/api/login", `{"nick":"Bel","password":"mpw"}`, "", nil)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"member":true`) || strings.Contains(w.Body.String(), "m1") {
		t.Fatalf("member login: %d %s", w.Code, w.Body)
	}
	var tok string
	for _, c := range w.Result().Cookies() {
		if c.Name == cookieName {
			tok = c.Value
		}
	}
	if w := rg.do("GET", "/api/my-nick", "", tok, nil); w.Code != 200 || !strings.Contains(w.Body.String(), `"name":"Bel"`) {
		t.Errorf("my-nick: %d %s", w.Code, w.Body)
	}
	if w := rg.do("GET", "/api/live", "", tok, nil); w.Code != http.StatusForbidden {
		t.Errorf("live for a member: %d %s", w.Code, w.Body)
	}
	if w := rg.do("GET", "/api/status", "", tok, nil); w.Code != http.StatusForbidden {
		t.Errorf("status for a member: %d %s", w.Code, w.Body)
	}
}

func TestMemberHomeAndMemoRoutes(t *testing.T) {
	rg := newRig(t)
	w := rg.do("POST", "/api/login", `{"nick":"Bel","password":"mpw"}`, "", nil)
	var tok string
	for _, c := range w.Result().Cookies() {
		if c.Name == cookieName {
			tok = c.Value
		}
	}
	for path, want := range map[string]string{"/api/my-home": `"unread":1`, "/api/my-memos": `"from":"ayu"`} {
		if w := rg.do("GET", path, "", tok, nil); w.Code != 200 || !strings.Contains(w.Body.String(), want) {
			t.Errorf("%s: %d %s", path, w.Code, w.Body)
		}
	}
	for _, c := range []struct{ path, body, want string }{
		{"/api/memo-read", `{"id":4}`, "memo-read 4"},
		{"/api/memo-del", `{"id":4}`, "memo-del 4"},
		{"/api/memo-send", `{"to":"ayu","text":"see you at 9"}`, "memo-send 0 ayu see you at 9"},
	} {
		if w := rg.do("POST", c.path, c.body, tok, nil); w.Code != 200 || !strings.Contains(w.Body.String(), c.want) {
			t.Errorf("%s: %d %s", c.path, w.Code, w.Body)
		}
	}
	for _, c := range []struct{ path, body string }{
		{"/api/memo-read", `{"id":0}`},
		{"/api/memo-del", `{}`},
		{"/api/memo-send", `{"to":"ayu"}`},
		{"/api/memo-send", `{"to":"ayu","text":"` + strings.Repeat("x", 1001) + `"}`},
	} {
		if w := rg.do("POST", c.path, c.body, tok, nil); w.Code != http.StatusBadRequest {
			t.Errorf("%s %s: %d", c.path, c.body[:min(len(c.body), 30)], w.Code)
		}
	}
	// Not signed in: nothing.
	if w := rg.do("GET", "/api/my-memos", "", "", nil); w.Code == 200 {
		t.Errorf("my-memos without a session: %d", w.Code)
	}
	// From another site: refused like every POST.
	if w := rg.do("POST", "/api/memo-send", `{"to":"ayu","text":"x"}`, tok, map[string]string{"X-Hive": ""}); w.Code == 200 {
		t.Errorf("memo-send without X-Hive: %d", w.Code)
	}
}

func TestChannelManagerRoutes(t *testing.T) {
	rg := newRig(t)
	w := rg.do("POST", "/api/login", `{"nick":"Bel","password":"mpw"}`, "", nil)
	var tok string
	for _, c := range w.Result().Cookies() {
		if c.Name == cookieName {
			tok = c.Value
		}
	}
	if w := rg.do("GET", "/api/my-chan?name=%23honey", "", tok, nil); w.Code != 200 || !strings.Contains(w.Body.String(), `"name":"#honey"`) {
		t.Errorf("my-chan: %d %s", w.Code, w.Body)
	}
	if w := rg.do("GET", "/api/my-chan", "", tok, nil); w.Code != http.StatusBadRequest {
		t.Errorf("my-chan without a name: %d", w.Code)
	}
	if w := rg.do("POST", "/api/chan-do", `{"name":"#honey","kind":"ACCESS","value":"ADD","mask":"ayu","reason":"AOP"}`, tok, nil); w.Code != 200 || !strings.Contains(w.Body.String(), "chan-do #honey ACCESS ADD ayu AOP") {
		t.Errorf("chan-do: %d %s", w.Code, w.Body)
	}
	if w := rg.do("POST", "/api/chan-memo", `{"name":"#honey","id":3,"verb":"del"}`, tok, nil); w.Code != 200 || !strings.Contains(w.Body.String(), "chan-memo #honey  del   3") {
		t.Errorf("chan-memo: %d %s", w.Code, w.Body)
	}
	for _, c := range []struct{ path, body string }{
		{"/api/chan-do", `{"name":"#honey"}`},
		{"/api/chan-memo", `{"name":"#honey","id":3,"verb":"wipe"}`},
		{"/api/chan-memo", `{"name":"#honey","verb":"read"}`},
	} {
		if w := rg.do("POST", c.path, c.body, tok, nil); w.Code != http.StatusBadRequest {
			t.Errorf("%s %s: %d", c.path, c.body, w.Code)
		}
	}
}

func TestMailRoutes(t *testing.T) {
	rg := newRig(t)
	tok := rg.login()
	w := rg.do("POST", "/api/mail-test", `{"mail":{"provider":"gmail","from":"bee@gmail.com","password":"pw"},"to":"me@example.org"}`, tok, nil)
	last := rg.f.reqs[len(rg.f.reqs)-1]
	if w.Code == 400 || last.Cmd != control.CmdMailTest || last.Mail == nil || last.Mail.Provider != "gmail" || last.Mail.Password != "pw" || last.Value != "me@example.org" || last.As != "Bee" {
		t.Errorf("mail-test: %d %+v", w.Code, last)
	}
	rg.do("POST", "/api/mail-reg", `{"value":"mail"}`, tok, nil)
	if last := rg.f.reqs[len(rg.f.reqs)-1]; last.Cmd != control.CmdMailReg || last.Value != "mail" {
		t.Errorf("mail-reg: %+v", last)
	}
	if w := rg.do("POST", "/api/mail-save", `{"mail":`, tok, nil); w.Code != 400 {
		t.Errorf("bad json: %d", w.Code)
	}
	if w := rg.do("POST", "/api/mail-save", `{"mail":{"provider":"off"}}`, "", nil); w.Code != 401 {
		t.Errorf("signed out: %d", w.Code)
	}
}
