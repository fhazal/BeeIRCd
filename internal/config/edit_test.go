package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const sample = `# BeeIRCd test config
# keep this comment

[server]
name = "irc.test.local"   # the name people see
network = "BeeNet"
sid = "0BE"

[[listen]]
address = "127.0.0.1:6667"

[[listen]]
address = "127.0.0.1:6697"
tls = true

# TLS comes from here
[tls]
cert = "cert.pem"
key = "key.pem"
self_signed = true

[cloak]
enabled = false
`

func writeSample(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "beeircd.conf")
	if err := os.WriteFile(p, []byte(sample), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestEditKeepsComments(t *testing.T) {
	p := writeSample(t)
	cfg, err := Edit(p, func(d *Doc) error {
		d.Set("server", "name", Quote("irc.bee.example"))
		d.Set("limits", "max_users", "500")
		d.Set("services", "nick_expire", Quote("120d"))
		d.Set("cloak", "enabled", "false")
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Server.Name != "irc.bee.example" || cfg.Limits.MaxUsers != 500 || cfg.Services.NickExpire.Duration != 120*24*time.Hour {
		t.Fatalf("values not applied: %+v", cfg)
	}
	if cfg.Path != p {
		t.Fatalf("path %q", cfg.Path)
	}
	b, _ := os.ReadFile(p)
	text := string(b)
	for _, want := range []string{"# keep this comment", `name = "irc.bee.example" # the name people see`, "# TLS comes from here\n[tls]", "[limits]\nmax_users = 500"} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in:\n%s", want, text)
		}
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(p), ".beeircd.conf.new")); !os.IsNotExist(err) {
		t.Error("temp file left behind")
	}
}

func TestEditRefusesBrokenConfig(t *testing.T) {
	p := writeSample(t)
	_, err := Edit(p, func(d *Doc) error {
		d.Set("server", "sid", Quote("bad"))
		return nil
	})
	if err == nil || !strings.Contains(err.Error(), "would break") {
		t.Fatalf("err = %v", err)
	}
	b, _ := os.ReadFile(p)
	if string(b) != sample {
		t.Fatal("file changed although the edit was refused")
	}
}

func TestEditListen(t *testing.T) {
	p := writeSample(t)
	cfg, err := Edit(p, func(d *Doc) error {
		d.AddListen("127.0.0.1:7777", true)
		if !d.DelListen("127.0.0.1:6667") {
			t.Error("DelListen found nothing")
		}
		if d.DelListen("127.0.0.1:1") {
			t.Error("DelListen removed a missing port")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Listen) != 2 || cfg.Listen[0].Address != "127.0.0.1:6697" || cfg.Listen[1].Address != "127.0.0.1:7777" || !cfg.Listen[1].TLS {
		t.Fatalf("listen = %+v", cfg.Listen)
	}
	b, _ := os.ReadFile(p)
	if !strings.Contains(string(b), "tls = true\n\n[[listen]]\naddress = \"127.0.0.1:7777\"\ntls = true\n\n# TLS comes from here") {
		t.Errorf("layout:\n%s", b)
	}
}

func TestQuoteAndDurations(t *testing.T) {
	if got := Quote(`say "hi" \ bye`); got != `"say \"hi\" \\ bye"` {
		t.Errorf("Quote = %s", got)
	}
	for d, want := range map[time.Duration]string{90 * 24 * time.Hour: "90d", 12 * time.Hour: "12h", 5 * time.Minute: "5m", 90 * time.Second: "90s"} {
		if got := FormatDuration(d); got != want {
			t.Errorf("FormatDuration(%v) = %s", d, got)
		}
		if back, err := ParseDuration(want); err != nil || back != d {
			t.Errorf("ParseDuration(%s) = %v, %v", want, back, err)
		}
	}
}

func TestEditOperClassBlocks(t *testing.T) {
	path := writeSample(t)
	cfg, err := Edit(path, func(d *Doc) error {
		d.SetBlock("operclass", []KV{{"name", Quote("staff")}, {"flags", Quote("hk")}, {"vhost", Quote("staff.example.org")}, {"swhois", QuoteList([]string{"looks very helpful"})}})
		d.SetBlock("operclass", []KV{{"name", Quote("helper")}, {"flags", Quote("h")}})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if oc, ok := cfg.Class("STAFF"); !ok || oc.Flags != "hk" || oc.Vhost != "staff.example.org" || len(oc.Swhois) != 1 {
		t.Fatalf("staff = %+v %v", oc, ok)
	}
	cfg, err = Edit(path, func(d *Doc) error {
		d.SetBlock("operclass", []KV{{"name", Quote("staff")}, {"flags", Quote("ghk")}})
		if !d.DelBlock("operclass", "name", "helper") {
			t.Error("helper block not found")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if oc, _ := cfg.Class("staff"); oc.Flags != "ghk" || oc.Vhost != "" {
		t.Fatalf("staff after SET = %+v", oc)
	}
	if len(cfg.OperClasses) != 1 {
		t.Fatalf("blocks = %+v", cfg.OperClasses)
	}
	if oc, ok := cfg.Class("helper"); !ok || oc.Flags != "h" {
		t.Fatalf("built-in helper lost: %+v", oc)
	}
	raw, _ := os.ReadFile(path)
	if !strings.Contains(string(raw), "# the admin") && !strings.Contains(string(raw), "#") {
		t.Fatal("comments lost")
	}
	if _, err := Edit(path, func(d *Doc) error {
		d.SetBlock("operclass", []KV{{"name", Quote("bad one")}, {"flags", Quote("hz")}})
		return nil
	}); err == nil {
		t.Fatal("a broken class was saved")
	}
}
