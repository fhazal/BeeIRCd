package server

import (
	"archive/tar"
	"compress/gzip"
	"crypto/tls"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/fhazal/beeircd/internal/config"
	"github.com/fhazal/beeircd/internal/control"
	"github.com/fhazal/beeircd/internal/store"
	"github.com/fhazal/beeircd/internal/tlsutil"
)

// Step 7b: control socket, status, check, backup, REHASH, cert reload.

func askOK(t *testing.T, s *Server, cmd, path string) *control.Response {
	t.Helper()
	resp, err := control.Ask(s.cfg.Control.Socket, control.Request{Cmd: cmd, Path: path}, 20*time.Second)
	if err != nil {
		t.Fatalf("%s: %v", cmd, err)
	}
	return resp
}

func tarNames(t *testing.T, path string) []string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	zr, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(zr)
	var names []string
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		names = append(names, h.Name)
		if h.Name == "services.db" {
			out := filepath.Join(t.TempDir(), "restored.db")
			w, _ := os.Create(out)
			io.Copy(w, tr)
			w.Close()
			if sum, err := store.CheckFile(out); err != nil || sum.Nicks < 1 {
				t.Errorf("backed up database: %+v %v", sum, err)
			}
		}
	}
	sort.Strings(names)
	return names
}

func TestControlStatusCheckBackup(t *testing.T) {
	s, _, _ := fileServer(t, "[history]\ndefault = \"10:1d\"\nsecret = \"backup-history-secret\"\n")
	b := boss(t, s)
	b.send("JOIN #hive")
	b.expect(" 366 Boss #hive ")
	b.send("PRIVMSG #hive :kept")
	b.sync()

	st := askOK(t, s, control.CmdStatus, "").Status
	if st.Server != "irc.test.local" || st.Online != 1 || st.Opers != 1 || st.Channels != 1 || len(st.Ports) != 2 || !st.Ports[1].TLS {
		t.Errorf("status: %+v", st)
	}
	if st.Services[0] != "NickServ" || st.HistoryChannels != 1 || st.Cert == nil || st.Cert.Source != "self-signed" || st.PID != os.Getpid() {
		t.Errorf("status details: %+v %+v", st.Services, st.Cert)
	}

	ck := askOK(t, s, control.CmdCheck, "").Check
	if ck.DBError != "" || ck.Nicks != 1 {
		t.Errorf("check: %+v", ck)
	}

	bk := askOK(t, s, control.CmdBackup, "").Backup
	if filepath.Dir(bk.File) != s.cfg.Backup.Dir || bk.Bytes == 0 {
		t.Fatalf("backup: %+v", bk)
	}
	if got := strings.Join(tarNames(t, bk.File), " "); got != "beeircd.conf history.db services.db tls/cert.pem tls/key.pem" {
		t.Errorf("backup holds %s", got)
	}
	if fi, _ := os.Stat(bk.File); fi.Mode().Perm()&0o077 != 0 && os.PathSeparator == '/' {
		t.Errorf("backup readable by others: %v", fi.Mode())
	}
	// A path in the request is never written: the server stays inside its
	// backup folder, under a name Prune and LastBackup skip.
	elsewhere := filepath.Join(t.TempDir(), "mine.tar.gz")
	got := askOK(t, s, control.CmdBackup, elsewhere).Backup.File
	if filepath.Dir(got) != s.cfg.Backup.Dir || !strings.HasPrefix(filepath.Base(got), ".named-") {
		t.Errorf("named backup went to %s", got)
	}
	if _, err := os.Stat(elsewhere); err == nil {
		t.Error("the server wrote the path from the request")
	}
	if b := control.Backups(s.cfg.Backup.Dir); len(b) != 1 {
		t.Errorf("dated backups: %v", b)
	}
	if _, err := control.Ask(s.cfg.Control.Socket, control.Request{Cmd: "fly"}, 5*time.Second); err == nil {
		t.Error("unknown command accepted")
	}
}

func TestRehash(t *testing.T) {
	s, path, _ := fileServer(t, "")
	b := boss(t, s)
	conf, _ := os.ReadFile(path)
	edited := strings.Replace(string(conf), "session_limit = 100", "session_limit = 50\nconnect_rate = \"10:60\"", 1)
	edited = strings.Replace(edited, "address = \"127.0.0.1:0\"\n\n[[listen]]", "address = \"127.0.0.1:1\"\n\n[[listen]]", 1)
	edited = strings.Replace(edited, "[motd]", "[motd]\n# new", 1)
	os.WriteFile(path, []byte(edited), 0o600)

	r := askOK(t, s, control.CmdRehash, "").Rehash
	applied, restart := strings.Join(r.Applied, " | "), strings.Join(r.NeedsRestart, " | ")
	if !strings.Contains(applied, "session_limit 100 → 50") || !strings.Contains(applied, "connect_rate 5:60 → 10:60") || restart != "ports" {
		t.Errorf("applied %q, restart %q", applied, restart)
	}
	b.expect("re-read beeircd.conf: applied")
	limit, ports := 0, 0
	s.onLoop(func() { limit, ports = s.cfg.Limits.SessionLimit, len(s.cfg.Listen) })
	if limit != 50 || ports != 2 {
		t.Errorf("live config: session_limit %d", limit)
	}
	var addr string
	s.onLoop(func() { addr = s.cfg.Listen[0].Address })
	if addr != "127.0.0.1:0" {
		t.Errorf("a port change was applied live: %s", addr)
	}

	// A broken file changes nothing.
	os.WriteFile(path, []byte(edited+"\n[limits]\nbogus = 1\n"), 0o600)
	if _, err := control.Ask(s.cfg.Control.Socket, control.Request{Cmd: control.CmdRehash}, 5*time.Second); err == nil || !strings.Contains(err.Error(), "not used") {
		t.Errorf("broken config: %v", err)
	}

	// The REHASH command, for opers with flag a.
	os.WriteFile(path, []byte(edited), 0o600)
	b.send("REHASH")
	b.expectNumeric("382", "beeircd.conf", ":Rehashing")
	b.expect("*** nothing changed")
	n := login(t, s, "nobody")
	n.send("REHASH")
	n.expectNumeric("481")
}

func TestCertReload(t *testing.T) {
	s := testServer(t, nil)
	var cert, key string
	s.onLoop(func() { cert, key = s.cfg.TLS.Cert, s.cfg.TLS.Key })
	time.Sleep(20 * time.Millisecond)
	if err := tlsutil.MakeSelfSigned(cert, key, "renewed.test.local"); err != nil {
		t.Fatal(err)
	}
	var name string
	s.onLoop(func() {
		s.certTick(time.Now().Add(2 * time.Minute))
		name = s.certs.Leaf().DNSNames[0]
	})
	if name != "renewed.test.local" {
		t.Errorf("certificate not reloaded: %s", name)
	}
	c := tlsLoginAny(t, s)
	if got := c.ConnectionState().PeerCertificates[0].DNSNames[0]; got != "renewed.test.local" {
		t.Errorf("new connections still get %s", got)
	}
}

func TestSafetyCounts(t *testing.T) {
	s := testServerWith(t, func(c *config.Config) { c.Limits.ConnectRate = "1:60" }, noExempt)
	login(t, s, "first")
	closedWith(t, s.Addrs()[0].String())
	var sf control.Safety
	s.onLoop(func() { sf = s.statusInfo().Safety })
	if sf.Throttled != 1 {
		t.Errorf("safety: %+v", sf)
	}
}

// tlsLoginAny opens a TLS connection to s's TLS port.
func tlsLoginAny(t *testing.T, s *Server) *tls.Conn {
	t.Helper()
	conn, err := tls.Dial("tcp", s.Addrs()[1].String(), &tls.Config{InsecureSkipVerify: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	return conn
}

// TestStatusSystem: Status tells the Hive Panel what the server runs on
// (Server details card): OS, architecture and CPU count.
func TestStatusSystem(t *testing.T) {
	s := testServer(t, nil)
	var st *control.Status
	s.onLoop(func() { st = s.statusInfo() })
	if st.OS != runtime.GOOS || st.Arch != runtime.GOARCH || st.CPUs != runtime.NumCPU() || st.CPUs < 1 {
		t.Errorf("system: os %q arch %q cpus %d", st.OS, st.Arch, st.CPUs)
	}
	if st.LinkPort != 0 {
		t.Errorf("link port %d with no link listener", st.LinkPort)
	}
}
