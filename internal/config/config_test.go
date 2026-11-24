package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func write(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "beeircd.conf")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

const good = `
[server]
name = "irc.beenet.org"
network = "BeeNet"
sid = "0BE"

[[listen]]
address = ":6667"

[[listen]]
address = ":6697"
tls = true

[tls]
cert = "tls/cert.pem"
key = "tls/key.pem"
self_signed = true

[motd]
file = "motd.txt"

[cloak]
secret = "0123456789abcdef-test"

[limits]
ping_interval = "2m"
`

func TestLoadGood(t *testing.T) {
	p := write(t, good)
	c, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if c.Limits.PingInterval.Duration != 2*time.Minute || c.Limits.PingTimeout.Duration != 90*time.Second {
		t.Errorf("durations: %v %v", c.Limits.PingInterval, c.Limits.PingTimeout)
	}
	if c.TLS.Cert != filepath.Join(filepath.Dir(p), "tls", "cert.pem") {
		t.Errorf("cert path not made absolute: %s", c.TLS.Cert)
	}
	if !c.ResolveHosts() || !c.NeedsTLS() || !c.CloakEnabled() || c.Limits.FloodBurst != 10 || c.Server.Description != "BeeNet IRC" {
		t.Errorf("defaults wrong: %+v", c.Server)
	}
	os.WriteFile(filepath.Join(filepath.Dir(p), "motd.txt"), []byte("one\r\ntwo\n\n"), 0o600)
	if got := c.MOTDLines(); len(got) != 2 || got[1] != "two" {
		t.Errorf("MOTDLines = %q", got)
	}
}

func TestLoadErrors(t *testing.T) {
	cases := map[string]string{
		"unknown setting server.nmae": strings.Replace(good, "name =", "nmae =", 1),
		"must look like irc.example":  strings.Replace(good, `"irc.beenet.org"`, `"localhost"`, 1),
		"sid":                         strings.Replace(good, `"0BE"`, `"BEE"`, 1),
		"needs [tls] cert":            strings.Replace(good, `cert = "tls/cert.pem"`, "", 1),
		"not a time":                  strings.Replace(good, `"2m"`, `"soon"`, 1),
		"[cloak] secret":              strings.Replace(good, `"0123456789abcdef-test"`, `"short"`, 1),
		"max_accounts_per_email -1":   good + "\n[services]\nmax_accounts_per_email = -1\n",
	}
	for want, body := range cases {
		_, err := Load(write(t, body))
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("want error containing %q, got %v", want, err)
		}
	}
}

func TestACMEConfig(t *testing.T) {
	acme := strings.Replace(good, "cert = \"tls/cert.pem\"\nkey = \"tls/key.pem\"\nself_signed = true", "acme = true\nacme_email = \"admin@beenet.org\"", 1)
	c, err := Load(write(t, acme))
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(c.TLS.Cert) != "cert.pem" || !filepath.IsAbs(c.TLS.Cert) || filepath.Base(c.TLS.AccountKey()) != "acme-account.key" {
		t.Errorf("paths %q %q", c.TLS.Cert, c.TLS.AccountKey())
	}
	if d := c.TLS.Domains(c.Server.Name); len(d) != 1 || d[0] != "irc.beenet.org" || !c.STSOn() {
		t.Errorf("domains %v sts %v", d, c.STSOn())
	}
	if self, _ := Load(write(t, good)); self.STSOn() {
		t.Error("sts on with a self-signed certificate")
	}
	local := strings.Replace(acme, `"irc.beenet.org"`, `"test.beenet.local"`, 1)
	if _, err := Load(write(t, local)); err == nil || !strings.Contains(err.Error(), "set acme_domains") {
		t.Errorf("private server name: %v", err)
	}
	if _, err := Load(write(t, local+"\n[tls]\n")); err == nil {
		t.Error("duplicate table accepted")
	}
	withDomain := strings.Replace(local, "acme = true", "acme = true\nacme_domains = [\"test.beeircd.org\"]", 1)
	if c, err := Load(write(t, withDomain)); err != nil || c.TLS.Domains(c.Server.Name)[0] != "test.beeircd.org" {
		t.Errorf("acme_domains: %v", err)
	}
	for want, body := range map[string]string{
		"not both":              strings.Replace(acme, "acme = true", "acme = true\nself_signed = true", 1),
		"isn't an email":        strings.Replace(acme, "admin@beenet.org", "admin", 1),
		"need acme = true":      strings.Replace(good, "self_signed = true", "self_signed = true\nacme_email = \"a@b.org\"", 1),
		"acme_http \"80\" must": strings.Replace(acme, "acme = true", "acme = true\nacme_http = \"80\"", 1),
		"https:// URL":          strings.Replace(acme, "acme = true", "acme = true\nacme_directory = \"http://ca\"", 1),
		"no [[listen]] port":    strings.Replace(acme, "tls = true", "", 1),
		"acme_domains: 10.0.":   strings.Replace(acme, "acme = true", "acme = true\nacme_domains = [\"10.0.0.1\"]", 1),
	} {
		if _, err := Load(write(t, body)); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("want %q, got %v", want, err)
		}
	}
}

// An explicit 0s is kept ("never" / "no wait"); left out, the default.
func TestZeroDurationsKept(t *testing.T) {
	c, err := Load(write(t, good+"\n[services]\nunconfirmed_expire = \"0s\"\n\n[mail]\ndelay = \"0s\"\n"))
	if err != nil {
		t.Fatal(err)
	}
	if c.Services.UnconfirmedExpireOr() != 0 || c.Mail.DelayOr() != 0 {
		t.Errorf("0s became %v / %v", c.Services.UnconfirmedExpireOr(), c.Mail.DelayOr())
	}
	c, err = Load(write(t, good))
	if err != nil {
		t.Fatal(err)
	}
	if c.Services.UnconfirmedExpireOr() != 24*time.Hour || c.Mail.DelayOr() != 5*time.Minute {
		t.Errorf("defaults %v / %v", c.Services.UnconfirmedExpireOr(), c.Mail.DelayOr())
	}
}

// log_events picks the log channel's event lines; unset keeps the old
// default, and log_connects = false still drops connect, quit and nick.
func TestLogEvents(t *testing.T) {
	c, err := Load(write(t, good))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range LogEventNames {
		want := e != "umode" && e != "host" && e != "channel"
		if got := c.Services.LogEventOn(e); got != want {
			t.Errorf("default %s = %v, want %v", e, got, want)
		}
	}
	c, err = Load(write(t, good+"\n[services]\nlog_connects = false\n"))
	if err != nil {
		t.Fatal(err)
	}
	for e, want := range map[string]bool{"connect": false, "quit": false, "nick": false, "oper": true, "kill": true, "bans": true, "channel": false} {
		if got := c.Services.LogEventOn(e); got != want {
			t.Errorf("log_connects = false: %s = %v, want %v", e, got, want)
		}
	}
	// A set list wins over log_connects.
	c, err = Load(write(t, good+"\n[services]\nlog_connects = false\nlog_events = [\"connect\", \"Channel\"]\n"))
	if err != nil {
		t.Fatal(err)
	}
	for e, want := range map[string]bool{"connect": true, "channel": true, "quit": false, "oper": false} {
		if got := c.Services.LogEventOn(e); got != want {
			t.Errorf("list: %s = %v, want %v", e, got, want)
		}
	}
	c, err = Load(write(t, good+"\n[services]\nlog_events = [\"*\"]\n"))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range LogEventNames {
		if !c.Services.LogEventOn(e) {
			t.Errorf("\"*\": %s off", e)
		}
	}
	c, err = Load(write(t, good+"\n[services]\nlog_events = []\n"))
	if err != nil || c.Services.LogEventOn("oper") {
		t.Errorf("empty list: %v, oper %v", err, err == nil && c.Services.LogEventOn("oper"))
	}
	if _, err := Load(write(t, good+"\n[services]\nlog_events = [\"oper\", \"joins\"]\n")); err == nil || !strings.Contains(err.Error(), `log_events "joins" is not an event`) {
		t.Errorf("unknown event: %v", err)
	}
}
