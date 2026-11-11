package main

import (
	"os"
	"strings"
	"testing"

	"github.com/fhazal/beeircd/internal/config"
)

func TestLinkFileWarnings(t *testing.T) {
	cfg := &config.Config{Path: "/etc/beeircd/beeircd.conf"}
	cfg.Link.Key = "/var/lib/beeircd/link-key.pem"
	modes := map[string]os.FileMode{cfg.Path: 0o644, cfg.Link.Key: 0o600}
	mode := func(p string) (os.FileMode, bool) { m, ok := modes[p]; return m, ok }

	if w := linkFileWarnings(cfg, mode); len(w) != 0 {
		t.Errorf("no password, private key: %v", w)
	}
	cfg.Link.Servers = []config.LinkServer{{Name: "a.b.c", Password: "x"}}
	if w := linkFileWarnings(cfg, mode); len(w) != 1 || !strings.Contains(w[0], "holds a link password") {
		t.Errorf("readable conf with password: %v", w)
	}
	modes[cfg.Path] = 0o640
	modes[cfg.Link.Key] = 0o644
	if w := linkFileWarnings(cfg, mode); len(w) != 1 || !strings.Contains(w[0], "link key") {
		t.Errorf("readable key: %v", w)
	}
	modes[cfg.Link.Key] = 0o600
	if w := linkFileWarnings(cfg, mode); len(w) != 0 {
		t.Errorf("all private: %v", w)
	}
}
