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
	}
	for want, body := range cases {
		_, err := Load(write(t, body))
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("want error containing %q, got %v", want, err)
		}
	}
}
