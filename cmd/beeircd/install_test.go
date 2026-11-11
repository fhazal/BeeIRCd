package main

import (
	"bufio"
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fhazal/beeircd/internal/config"
	"github.com/fhazal/beeircd/internal/passwd"
	"github.com/fhazal/beeircd/internal/tlsutil"
)

// testInstaller runs against a temp root with scripted answers; ran
// records the commands it would have run.
func testInstaller(t *testing.T, answers string, ran *[]string) (*installer, *bytes.Buffer) {
	t.Helper()
	out := &bytes.Buffer{}
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "etc"), 0o755)
	os.WriteFile(filepath.Join(root, "etc", "os-release"), []byte("ID=debian\nVERSION_ID=\"13\"\n"), 0o644)
	os.WriteFile(filepath.Join(root, "etc", "timezone"), []byte("Asia/Kuala_Lumpur\n"), 0o644)
	in := &installer{
		root: root, in: bufio.NewReader(strings.NewReader(answers)), out: out, c: &checker{w: out},
		env: func(k string) string {
			if k == "SSH_CONNECTION" {
				return "203.0.113.5 50000 10.0.0.2 22"
			}
			return ""
		},
		exe: os.Args[0], now: time.Date(2026, 11, 12, 0, 0, 0, 0, time.UTC), uid: -1, gid: -1,
		portFree: func(port int) bool { return port != 7777 },
		run: func(name string, args ...string) (string, error) {
			*ran = append(*ran, name+" "+strings.Join(args, " "))
			if name == "ufw" && len(args) == 1 {
				return "Status: active", nil
			}
			return "", nil
		},
	}
	in.readSecret = in.line
	return in, out
}

func TestInstallSelfSigned(t *testing.T) {
	var ran []string
	answers := strings.Join([]string{
		"localhost",             // 1 not a DNS name: asked again
		"irc.beenet.org",        // 1
		"",                      // 2 Beenet (guessed)
		"NickServ",              // 3 reserved: asked again
		"Bee",                   // 3
		"short",                 // 4 too short: asked again
		"correct horse battery", // 4
		"correct horse battery", //   again
		"",                      // 5 *@203.0.113.5 from SSH_CONNECTION
		"7777 6697",             // 6 7777 busy: asked again
		"",                      // 6 6667 plain, 6697 TLS
		"2",                     // 7 self-signed
		"",                      // 8 @+
		"2",                     // 9 hub
		"6697",                  //   link port = a client port: asked again
		"",                      //   7000
		"y",                     // install
	}, "\n") + "\n"
	in, out := testInstaller(t, answers, &ran)
	p, err := in.ask()
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if err := in.apply(p); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	text := out.String()
	for _, want := range []string{"a DNS name like", "belongs to the services", "at least 8", "port 7777 is in use",
		"Server      irc.beenet.org   on network Beenet", "then /oper, from *@203.0.113.5", "6667 plain · 6697 TLS",
		"daily 04:00 Asia/Kuala_Lumpur", "service beeircd enabled", "firewall (ufw): 6667, 6697, 7000 open",
		"already for clients", "hub, link port 7000 (TLS; server linking arrives in Stage 2)"} {
		if !strings.Contains(text, want) {
			t.Errorf("output lacks %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "correct horse") {
		t.Error("password shown")
	}
	cfg, err := config.Load(in.path(instConf))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Server.Name != "irc.beenet.org" || cfg.Server.Network != "Beenet" || cfg.Services.Admin != "Bee" ||
		!cfg.TLS.SelfSigned || cfg.TLS.ACME || len(cfg.Listen) != 2 || !strings.HasSuffix(filepath.ToSlash(cfg.Control.Socket), instSocket) ||
		len(cfg.Services.AdminHosts) != 1 || cfg.Server.Prefixes != "@+" || len(cfg.History.Secret) != 40 ||
		cfg.Link.Role != "hub" || cfg.Link.Port != 7000 {
		t.Errorf("config %+v", cfg)
	}
	if !passwd.Check("correct horse battery", cfg.Services.AdminPassword) {
		t.Error("admin_password doesn't match")
	}
	unit, _ := os.ReadFile(in.path(instUnit))
	if strings.Contains(string(unit), "AmbientCapabilities") || !strings.Contains(string(unit), "ProtectSystem=strict") {
		t.Errorf("unit without low ports:\n%s", unit)
	}
	if fi, err := os.Stat(in.path(instBin)); err != nil || fi.Mode().Perm()&0o100 == 0 && os.PathSeparator == '/' {
		t.Errorf("program not copied: %v", err)
	}
	for _, f := range []string{instBkUnit, instBkTimer, instMOTD} {
		if _, err := os.Stat(in.path(f)); err != nil {
			t.Error(err)
		}
	}
	want := []string{"systemctl daemon-reload", "systemctl enable beeircd.service", "systemctl enable --now beeircd-backup.timer", "ufw allow 6667/tcp", "ufw allow 6697/tcp"}
	if got := strings.Join(ran, "|"); !strings.Contains(got, strings.Join(want[:3], "|")) || !strings.Contains(got, "ufw allow 6697/tcp") {
		t.Errorf("commands %v", ran)
	}

	// Uninstall keeps config and data; --purge removes them.
	in.in = bufio.NewReader(strings.NewReader("y\n"))
	if in.uninstall(false) != 0 {
		t.Fatal(out)
	}
	if _, err := os.Stat(in.path(instConf)); err != nil {
		t.Error("config removed without --purge")
	}
	if _, err := os.Stat(in.path(instUnit)); err == nil {
		t.Error("unit kept")
	}
	in.in = bufio.NewReader(strings.NewReader("y\n"))
	in.uninstall(true)
	if _, err := os.Stat(in.path(instEtc)); err == nil {
		t.Error("config kept with --purge")
	}
}

func TestInstallLetsEncryptAndTLSOnly(t *testing.T) {
	var ran []string
	answers := "irc.example.org\nExampleNet\nBee\npassword1\npassword1\n\n6697\n1\nadmin@example.org\n2\n\ny\n"
	in, out := testInstaller(t, answers, &ran)
	p, err := in.ask()
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if err := in.apply(p); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	cfg, err := config.Load(in.path(instConf))
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.TLS.ACME || cfg.TLS.ACMEEmail != "admin@example.org" || len(cfg.Listen) != 1 || !cfg.Listen[0].TLS || cfg.Server.Prefixes != "~&@%+" {
		t.Errorf("config %+v", cfg)
	}
	unit, _ := os.ReadFile(in.path(instUnit))
	if !strings.Contains(string(unit), "AmbientCapabilities=CAP_NET_BIND_SERVICE") {
		t.Error("Let's Encrypt needs CAP_NET_BIND_SERVICE for port 80")
	}
	if !strings.Contains(out.String(), "firewall (ufw): 6697, 80 open") {
		t.Errorf("firewall:\n%s", out)
	}
}

func TestInstallCertbotFiles(t *testing.T) {
	var ran []string
	in, out := testInstaller(t, "", &ran)
	live := "/etc/letsencrypt/live/irc.example.org"
	if err := tlsutil.MakeSelfSigned(in.path(live+"/fullchain.pem"), in.path(live+"/privkey.pem"), "irc.example.org"); err != nil {
		t.Fatal(err)
	}
	// Let's Encrypt is refused while port 80 is busy; then own files.
	in.portFree = func(port int) bool { return port != 80 }
	in.in = bufio.NewReader(strings.NewReader("irc.example.org\n\nBee\npassword1\npassword1\n\n\n1\n3\n" + live + "/fullchain.pem\n" + live + "/privkey.pem\n\n\ny\n"))
	p, err := in.ask()
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if !strings.Contains(out.String(), "port 80 is in use") {
		t.Errorf("no port-80 warning:\n%s", out)
	}
	if err := in.apply(p); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	cfg, _ := config.Load(in.path(instConf))
	if !strings.HasSuffix(filepath.ToSlash(cfg.TLS.Cert), instTLS+"/cert.pem") || cfg.TLS.SelfSigned {
		t.Errorf("tls %+v", cfg.TLS)
	}
	if _, err := tlsutil.Describe(in.path(instTLS+"/cert.pem"), in.path(instTLS+"/key.pem"), "irc.example.org", in.now); err != nil {
		t.Errorf("copied pair: %v", err)
	}
	hook, _ := os.ReadFile(in.path(instLEHook))
	if !strings.Contains(string(hook), live+"/privkey.pem") {
		t.Errorf("hook:\n%s", hook)
	}
}

func TestInstallStopsWithoutChanges(t *testing.T) {
	var ran []string
	in, _ := testInstaller(t, "irc.example.org\n", &ran) // input ends early (Ctrl+D)
	if _, err := in.ask(); err != errStopped {
		t.Errorf("err = %v", err)
	}
	in, _ = testInstaller(t, "irc.example.org\n\nBee\npassword1\npassword1\n\n\n2\n\n\nn\n", &ran)
	if _, err := in.ask(); err != errStopped {
		t.Errorf("answering n: %v", err)
	}
	if _, err := os.Stat(in.path(instConf)); err == nil || len(ran) != 0 {
		t.Error("something was written")
	}
}

func TestInstallHelpers(t *testing.T) {
	for in, want := range map[string][2]int{"6667 plain, 6697 TLS": {6667, 6697}, "6697": {0, 6697}, "7000,7001": {7000, 7001}} {
		if p, tl, why := parsePorts(in); p != want[0] || tl != want[1] || why != "" {
			t.Errorf("parsePorts(%q) = %d %d %q", in, p, tl, why)
		}
	}
	for _, bad := range []string{"", "6697 6697", "99999"} {
		if _, _, why := parsePorts(bad); why == "" {
			t.Errorf("parsePorts(%q) accepted", bad)
		}
	}
	if g := guessNetwork("irc.beenet.org"); g != "Beenet" {
		t.Errorf("guessNetwork = %q", g)
	}
	if s := makeSID("irc.beenet.org"); len(s) != 3 || s[0] < '0' || s[0] > '9' || s != makeSID("IRC.beenet.org") {
		t.Errorf("makeSID = %q", s)
	}
}
