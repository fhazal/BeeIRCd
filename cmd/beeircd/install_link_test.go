package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fhazal/beeircd/internal/config"
	"github.com/fhazal/beeircd/internal/link"
)

// leafAnswers are the nine questions for a leaf, then the hub's three.
func leafAnswers(hubName, hubAddr, hubFP string) string {
	return strings.Join([]string{
		"leaf1.beenet.org", "", "Bee", "correct horse battery", "correct horse battery", "",
		"", "2", "", "3", "", // 9 leaf, link port 7000
		hubName, hubAddr, hubFP, "", "y", // "" = cloak secret later
	}, "\n") + "\n"
}

// Step 2f: a leaf asks for its hub, writes the [[link.server]] block, makes
// its link certificate at install and prints the block the hub pastes.
func TestInstallLeafWithHub(t *testing.T) {
	hubFP := "nYOJpqgJ3EM6dJbZ6r0vNcGoxJk1LTw0Ny1bwTl1Ezs="
	var ran []string
	in, out := testInstaller(t, strings.Join([]string{
		"leaf1.beenet.org", "", "Bee", "correct horse battery", "correct horse battery", "",
		"", "2", "", "3", "",
		"leaf1.beenet.org", // own name: asked again
		"hub.beenet.org",
		"hub.beenet.org:99999", // bad port: asked again
		"",                     // hub.beenet.org:7000
		"not-a-fingerprint",    // asked again
		hubFP,
		"too-short", // cloak secret: asked again
		"hub-cloak-secret-0123456789", "y",
	}, "\n")+"\n", &ran)
	p, err := in.ask()
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if err := in.apply(p); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	in.linkSteps(p)
	text := out.String()
	for _, want := range []string{"that is this server's own name", "a port number after the colon", "44 characters from beeircd link-info",
		"leaf, link port 7000 (TLS), hub hub.beenet.org at hub.beenet.org:7000", "link certificate made (fingerprint " + p.LinkFP + ")",
		"On the hub (BeeIRCd)", `name = "leaf1.beenet.org"`, `fingerprint = "` + p.LinkFP + `"`, "# no host: this leaf connects to the hub",
		"Links to      hub.beenet.org (connects to hub.beenet.org:7000; fingerprint set)"} {
		if !strings.Contains(text, want) {
			t.Errorf("output lacks %q:\n%s", want, text)
		}
	}
	cfg, err := config.Load(in.path(instConf))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Cloak.Secret != "hub-cloak-secret-0123456789" || strings.Contains(text, "hub-cloak-secret") || strings.Contains(text, "too-short") || !strings.Contains(text, "16+ characters") {
		t.Errorf("cloak secret %q", cfg.Cloak.Secret)
	}
	if cfg.Services.On() || strings.Contains(text, "cloak secret later") {
		t.Error("a leaf's services must be off; cloak secret was given")
	}
	if cfg.Link.Role != "leaf" || len(cfg.Link.Servers) != 1 {
		t.Fatalf("link %+v", cfg.Link)
	}
	s := cfg.Link.Servers[0]
	if s.Name != "hub.beenet.org" || s.Host != "hub.beenet.org" || s.Port != 7000 || s.Fingerprint != hubFP || !s.Autoconnect {
		t.Errorf("server %+v", s)
	}
	fp, err := link.FileFingerprint(in.path(filepath.Join(instData, "link-cert.pem")))
	if err != nil || fp != p.LinkFP || !config.ValidFingerprint(fp) {
		t.Errorf("link cert %q %v (plan %q)", fp, err, p.LinkFP)
	}
	if fi, err := os.Stat(in.path(filepath.Join(instData, "link-key.pem"))); err != nil || (os.PathSeparator == '/' && fi.Mode().Perm() != 0o600) {
		t.Errorf("link key: %v", err)
	}
}

// Enter for the hub's fingerprint writes the block commented out, so the
// config still loads, and says how to finish it.
func TestInstallLeafFingerprintLater(t *testing.T) {
	var ran []string
	in, out := testInstaller(t, leafAnswers("hub.beenet.org", "203.0.113.9:7001", ""), &ran)
	p, err := in.ask()
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if err := in.apply(p); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	in.linkSteps(p)
	text := out.String()
	for _, want := range []string{"hub hub.beenet.org at 203.0.113.9:7001 (fingerprint later)", "fingerprint MISSING", "commented out", "(cloak secret later)", "Copy [cloak] secret from the hub"} {
		if !strings.Contains(text, want) {
			t.Errorf("output lacks %q:\n%s", want, text)
		}
	}
	conf, _ := os.ReadFile(in.path(instConf))
	if !strings.Contains(string(conf), "#[[link.server]]\n#name = \"hub.beenet.org\"\n#host = \"203.0.113.9\"\n#port = 7001") {
		t.Errorf("config:\n%s", conf)
	}
	cfg, err := config.Load(in.path(instConf))
	if err != nil || len(cfg.Link.Servers) != 0 {
		t.Fatalf("%v %+v", err, cfg)
	}
}

// A hub's block tells leaves where to connect; a standalone server prints
// nothing.
func TestLinkInfoTextHub(t *testing.T) {
	text := linkInfoText("hub.beenet.org", "0HB", "hub", 7000, "FP=", nil)
	for _, want := range []string{"On each leaf (BeeIRCd)", `host = "hub.beenet.org"`, "port = 7000", "autoconnect = true",
		"outgoing { hostname hub.beenet.org; port 7000;", `password "FP=" { spkifp; }`, "Every server on a network needs"} {
		if !strings.Contains(text, want) {
			t.Errorf("lacks %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "Links to") {
		t.Error("no servers, but a Links to line")
	}
	var ran []string
	in, out := testInstaller(t, "", &ran)
	in.linkSteps(&plan{Role: "standalone"})
	if out.Len() != 0 {
		t.Errorf("standalone printed %q", out.String())
	}
}
