package server

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fhazal/beeircd/internal/control"
)

// fakeReports stands in for DroneBL's RPC2 and an http report place; the
// live DroneBL is never called from tests.
type fakeReports struct {
	mu          sync.Mutex
	reqs        []string // "path?query body"
	droneAnswer string
}

func newFakeReports(t *testing.T) (*fakeReports, *httptest.Server) {
	f := &fakeReports{droneAnswer: `<?xml version="1.0"?><response type="success"></response>`}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.reqs = append(f.reqs, r.Method+" "+r.URL.RequestURI()+" "+string(body))
		answer := f.droneAnswer
		f.mu.Unlock()
		if strings.HasPrefix(r.URL.Path, "/hook") {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		io.WriteString(w, answer)
	}))
	t.Cleanup(srv.Close)
	return f, srv
}

func (f *fakeReports) take() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	r := f.reqs
	f.reqs = nil
	return r
}

func (f *fakeReports) answer(a string) {
	f.mu.Lock()
	f.droneAnswer = a
	f.mu.Unlock()
}

const testRPCKey = "TestKey-0123456789abcdef"

func spamReportConf(url string) string {
	return `
[[spamreport]]
name = "dronebl"
rpckey = "` + testRPCKey + `"
url = "` + url + `/RPC2"
auto_report = ["spamfilter"]

[[spamreport]]
name = "hook"
type = "http"
url = "` + url + `/hook?ip=$ip&nick=$nick&why=$reason"
rate_limit = "2:1m"
`
}

// /SPAMREPORT sends to every place, once an hour per address, within the
// rate limit, and never for LAN addresses, opers or exceptions.
func TestSpamReport(t *testing.T) {
	f, srv := newFakeReports(t)
	s, _, _ := fileServerWith(t, spamReportConf(srv.URL), func(s *Server) {
		s.dnsblSkip = func(string) bool { return false } // the test box is on loopback
	})
	b := boss(t, s)
	v := login(t, s, "spammer")

	v.send("SPAMREPORT Boss")
	v.expect(" 481 spammer ")

	b.send("SPAMREPORT spammer")
	b.expect("*** Reported 127.0.0.1 (spammer) to dronebl.")
	b.expect("*** Reported 127.0.0.1 (spammer) to hook.")
	reqs := f.take()
	if len(reqs) != 2 {
		t.Fatalf("requests %q", reqs)
	}
	drone, hook := reqs[0], reqs[1]
	if strings.HasPrefix(drone, "GET /hook") {
		drone, hook = hook, drone
	}
	for _, want := range []string{"POST /RPC2 ", `<request key="` + testRPCKey + `">`, `<add ip="127.0.0.1" type="6" comment="Spam on IRC: reported by Boss"/>`} {
		if !strings.Contains(drone, want) {
			t.Errorf("DroneBL request %q lacks %q", drone, want)
		}
	}
	if strings.Contains(drone, "staging") {
		t.Errorf("staging without asking: %q", drone)
	}
	if !strings.HasPrefix(hook, "GET /hook?ip=127.0.0.1&nick=spammer&why=reported+by+Boss ") {
		t.Errorf("http request %q", hook)
	}

	// Once an hour per address and place.
	b.send("SPAMREPORT 127.0.0.1")
	b.expect("*** 127.0.0.1 was sent to dronebl less than an hour ago.")
	b.expect("*** 127.0.0.1 was sent to hook less than an hour ago.")

	// A DroneBL error, and only one place named.
	f.answer(`<?xml version="1.0"?><response type="error"><code>403</code><message>Invalid key</message></response>`)
	b.send("SPAMREPORT 203.0.113.7 dronebl")
	b.expect("*** Reporting 203.0.113.7 to dronebl failed: DroneBL says: Invalid key")
	f.answer(`not xml`)
	b.send("SPAMREPORT 203.0.113.7 dronebl") // a failure isn't counted as sent
	b.expect("*** Reporting 203.0.113.7 to dronebl failed: DroneBL's answer wasn't one I know")
	f.answer(`<?xml version="1.0"?><response type="success"></response>`)
	b.send("SPAMREPORT 203.0.113.7 nosuch")
	b.expect("*** No spam report place called nosuch is on.")

	// The rate limit: hook takes 2 a minute.
	b.send("SPAMREPORT 203.0.113.8 hook")
	b.expect("*** Reported 203.0.113.8 to hook.")
	b.send("SPAMREPORT 203.0.113.9 hook")
	b.expect("*** hook has had its 2 reports in ")
	f.take()

	// Never reported: LAN, opers, exceptions, services, unknown nicks.
	s.onLoop(func() { s.dnsblSkip = dnsblSkipped })
	b.send("SPAMREPORT 192.168.1.5")
	b.expect("*** 192.168.1.5 is a local network address; those are never reported.")
	s.onLoop(func() { s.dnsblSkip = func(string) bool { return false } })
	b.send("SPAMREPORT Boss")
	b.expect("*** Boss is an oper; opers are never reported.")
	b.send("OS EXCEPTION BAN ADD *@203.0.113.20 friends")
	b.expect("203.0.113.20")
	b.send("SPAMREPORT 203.0.113.20")
	b.expect("*** 203.0.113.20 is covered by an EXCEPTION BAN, so it isn't reported.")
	b.send("SPAMREPORT NickServ")
	b.expect("*** NickServ is a service.")
	b.send("SPAMREPORT nobody")
	b.expect("*** No one is using the nick nobody.")
	if r := f.take(); len(r) != 0 {
		t.Errorf("sent anyway: %q", r)
	}
}

// AdminServ SPAMREPORT: LIST, KEY and STAGING; the key is never shown.
func TestAdminServSpamReport(t *testing.T) {
	f, srv := newFakeReports(t)
	s, path, _ := fileServerWith(t, spamReportConf(srv.URL), func(s *Server) {
		s.dnsblSkip = func(string) bool { return false }
	})
	b := boss(t, s)
	noKey := func(line string) {
		t.Helper()
		if strings.Contains(line, testRPCKey) || strings.Contains(line, "NewKey") {
			t.Errorf("a key shown: %s", line)
		}
	}

	b.send("AS SPAMREPORT LIST")
	b.expect(asLine("Boss", "Spam report places:"))
	noKey(b.expect("dronebl          dronebl · key set · class 6"))
	noKey(b.expect("hook             http · 127.0.0.1:"))

	b.send("AS SPAMREPORT STAGING dronebl ON")
	b.expect(asLine("Boss", "Reports to dronebl go to DroneBL's test list now (nothing is really listed)."))
	b.send("SPAMREPORT 203.0.113.30 dronebl")
	b.expect("*** Reported 203.0.113.30 to dronebl.")
	if r := f.take(); len(r) != 1 || !strings.Contains(r[0], `<request key="`+testRPCKey+`" staging="1">`) {
		t.Errorf("staging request %q", r)
	}
	b.send("AS SPAMREPORT STAGING hook ON")
	b.expect(asLine("Boss", "hook is an http place; staging is for DroneBL."))

	b.send("AS SPAMREPORT KEY dronebl NewKey-abc123")
	b.expect(asLine("Boss", "The DroneBL key of dronebl is saved. /SPAMREPORT <nick|ip> now sends there."))
	b.send("AS SPAMREPORT KEY dronebl bad<key")
	b.expect(asLine("Boss", "That doesn't look like a DroneBL key."))
	b.send("AS SPAMREPORT KEY hook abc")
	b.expect(asLine("Boss", "hook is an http place; it has no rpckey."))
	b.send("AS HISTORY 1")
	b.expect("SPAMREPORT dronebl rpckey")
	var id int64
	s.onLoop(func() {
		chs, _ := s.store.Changes(1)
		if len(chs) == 1 {
			id = chs[0].ID
		}
	})
	b.send(fmt.Sprintf("AS DIFF %d", id))
	noKey(b.expect("key set"))
	b.send(fmt.Sprintf("AS UNDO %d", id))
	b.expect(asLine("Boss", fmt.Sprintf("Spam report places are changed back with SPAMREPORT KEY or STAGING (#%d: dronebl rpckey).", id)))
	b.send("AS SPAMREPORT LIST")
	b.expect("Spam report places:")
	noKey(b.expect("test list (staging)"))
	conf, _ := os.ReadFile(path)
	if !strings.Contains(string(conf), `rpckey = "NewKey-abc123"`) || !strings.Contains(string(conf), "staging = true") {
		t.Errorf("not saved to the file:\n%s", conf)
	}

	b.send("AS SPAMREPORT KEY dronebl OFF")
	b.expect(asLine("Boss", "The DroneBL key of dronebl is removed."))
	b.send("SPAMREPORT 203.0.113.31 dronebl")
	b.expect("*** dronebl has no rpckey yet. Get one at https://dronebl.org/rpckey_signup")

	// A new place from KEY alone.
	b.send("AS SPAMREPORT KEY second Other-key-1")
	b.expect(asLine("Boss", "The DroneBL key of second is saved."))
	b.send("AS SPAMREPORT LIST")
	b.expect("second           dronebl · key set")
	b.send("AS SPAMREPORT KEY")
	b.expect(asLine("Boss", "Syntax: SPAMREPORT KEY <name> <rpckey|OFF>"))
	b.send("AS SPAMREPORT STAGING dronebl maybe")
	b.expect(asLine("Boss", "Syntax: SPAMREPORT STAGING <name> ON|OFF"))
}

// A spam filter rule that removes someone reports them on its own; one
// that only warns or blocks doesn't.
func TestSpamFilterAutoReport(t *testing.T) {
	f, srv := newFakeReports(t)
	s, _, _ := fileServerWith(t, spamReportConf(srv.URL), func(s *Server) {
		s.dnsblSkip = func(string) bool { return false }
	})
	b := boss(t, s)
	spamRule(t, b, "p block - No_ads *buy now*")
	spamRule(t, b, "p kill - Bye *kill me now*")
	w := login(t, s, "Wes")
	w.send("PRIVMSG Boss :buy now")
	w.sync()
	v := login(t, s, "Vic")
	v.send("PRIVMSG Boss :kill me now please")
	v.expect("ERROR :Closing Link")
	b.expect("Reported 127.0.0.1 (Vic) to dronebl (spam filter: Bye)")
	time.Sleep(100 * time.Millisecond)
	r := f.take()
	if len(r) != 1 || !strings.Contains(r[0], "/RPC2") || !strings.Contains(r[0], "Spam on IRC: spam filter: Bye") {
		t.Errorf("auto reports %q (only dronebl has auto_report)", r)
	}
}

// The Hive Panel: the place list without keys, KEY, STAGING and the
// People page's Report spam.
func TestPanelSpamReport(t *testing.T) {
	f, srv := newFakeReports(t)
	s, _, _ := fileServerWith(t, spamReportConf(srv.URL), func(s *Server) {
		s.dnsblSkip = func(string) bool { return false }
	})
	boss(t, s)
	e := registered(t, s, "ember")
	_ = e
	login(t, s, "spammer")
	ask := func(as string, r control.Request) *control.Response {
		r.As, r.IP = as, "127.0.0.1"
		return panelAsk(t, s, r)
	}

	r := ask("Boss", control.Request{Cmd: control.CmdSpamReports})
	if r.SpamReports == nil || len(r.SpamReports.Places) != 2 {
		t.Fatalf("places: %+v", r)
	}
	if p := r.SpamReports.Places[0]; p.Name != "dronebl" || !p.KeySet || p.Staging || strings.Contains(p.Line, testRPCKey) {
		t.Errorf("dronebl: %+v", p)
	}
	r = ask("Boss", control.Request{Cmd: control.CmdSpamReportStaging, Name: "dronebl", Value: "on"})
	if r.Done == nil || r.SpamReports == nil || !r.SpamReports.Places[0].Staging {
		t.Errorf("staging: %+v", r)
	}
	r = ask("Boss", control.Request{Cmd: control.CmdSpamReportKey, Name: "dronebl", Value: "bad key"})
	if r.Error != "That doesn't look like a DroneBL key." {
		t.Errorf("bad key: %+v", r)
	}

	var uid, bossUID string
	s.onLoop(func() {
		uid = s.net.UserByNick("spammer").UID
		bossUID = s.net.UserByNick("Boss").UID
	})
	r = ask("Boss", control.Request{Cmd: control.CmdSpamReport, UID: uid})
	if r.Done == nil || !strings.Contains(r.Done.Text, "Reported 127.0.0.1 (spammer) to dronebl.") {
		t.Errorf("report: %+v", r)
	}
	if got := f.take(); len(got) != 2 {
		t.Errorf("requests %q", got)
	}
	r = ask("Boss", control.Request{Cmd: control.CmdSpamReport, UID: bossUID})
	if r.Error != "Boss is an oper; opers are never reported." {
		t.Errorf("oper: %+v", r)
	}

	// Flags: KEY and STAGING need a, a report needs k.
	if r := ask("ember", control.Request{Cmd: control.CmdSpamReports}); r.Denied == "" {
		t.Errorf("ember sees places: %+v", r)
	}
	r = ask("Boss", control.Request{Cmd: control.CmdSpamReportKey, Name: "dronebl", Value: ""})
	if r.Done == nil || r.SpamReports.Places[0].KeySet {
		t.Errorf("key removed: %+v", r)
	}
}
