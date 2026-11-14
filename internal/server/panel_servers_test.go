package server

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fhazal/beeircd/internal/control"
	"github.com/fhazal/beeircd/internal/link"
)

// The Linked servers page.

func TestPanelLinksMapSplitConnect(t *testing.T) {
	hub, leaf := linkedPair(t)
	po := &control.PanelOper{Account: "Boss", Flags: allFlags, Owner: true}
	var l *control.Links
	hub.s.onLoop(func() { l = hub.s.panelLinks() })
	if l.Role != "hub" || l.Self != "hub.test.local" || l.Fingerprint != hub.fp || len(l.Blocks) != 1 || l.Blocks[0].Name != "leaf.test.local" {
		t.Fatalf("links: %+v", l)
	}
	names := map[string]string{}
	for _, m := range l.Map {
		names[m.Name] = m.Uplink
	}
	if _, ok := names["leaf.test.local"]; !ok || !l.Map[0].Self || len(l.Status) != 1 || l.Status[0].State != "up" {
		t.Errorf("map/status: %+v %+v", l.Map, l.Status)
	}

	// Connect: the hub has no address for the leaf (it connects to us).
	var r control.Response
	hub.s.onLoop(func() { r = hub.s.panelLinkConnect(po, "127.0.0.1", "leaf.test.local") })
	if !strings.Contains(r.Error, "connects to us") {
		t.Errorf("connect without host: %+v", r)
	}
	leaf.s.onLoop(func() { r = leaf.s.panelLinkConnect(po, "127.0.0.1", "hub.test.local") })
	if !strings.Contains(r.Error, "linked or linking already") {
		t.Errorf("connect while linked: %+v", r)
	}
	// Split: the link drops; the leaf connects again by itself.
	hub.s.onLoop(func() { r = hub.s.panelLinkSplit(po, "127.0.0.1", "leaf.test.local", "panel test") })
	if r.Done == nil {
		t.Fatalf("split: %+v", r)
	}
	waitLink(t, "split seen", func() bool { return hub.logHas("panel test") })
	hub.s.onLoop(func() { r = hub.s.panelLinkSplit(po, "127.0.0.1", "nowhere.test", "") })
	if !strings.Contains(r.Error, "isn't linked") {
		t.Errorf("split unknown: %+v", r)
	}
}

func TestPanelLinkBlocks(t *testing.T) {
	port := freePort(t)
	s, path, _ := fileServer(t, fmt.Sprintf("\n[link]\nrole = \"hub\"\nport = %d\n", port))
	dir := t.TempDir()
	_, fp, err := link.LoadOrCreate(filepath.Join(dir, "c.pem"), filepath.Join(dir, "k.pem"), "leaf.example.net")
	if err != nil {
		t.Fatal(err)
	}
	// As a hub this test server's services answer from services.test.local,
	// which the IRC login helpers don't expect: call the panel side directly.
	po := &control.PanelOper{Account: "Boss", Flags: allFlags, Owner: true}
	ask := func(_ string, r control.Request) *control.Response {
		var out control.Response
		s.onLoop(func() {
			switch r.Cmd {
			case control.CmdLinks:
				out.Links = s.panelLinks()
			case control.CmdLinkSave:
				out = s.panelLinkSave(po, "127.0.0.1", r.Block, r.New)
			case control.CmdLinkDel:
				out = s.panelLinkDel(po, "127.0.0.1", r.Name)
			}
		})
		return &out
	}
	save := func(blk control.LinkBlock, isNew bool) *control.Response {
		return ask("Boss", control.Request{Cmd: control.CmdLinkSave, Block: &blk, New: isNew})
	}
	if r := save(control.LinkBlock{Name: "leaf.example.net", Fingerprint: "nope"}, true); !strings.Contains(r.Error, "Fingerprint must be") {
		t.Errorf("bad fingerprint: %+v", r)
	}
	if r := save(control.LinkBlock{Name: "leaf.example.net", Fingerprint: fp, Autoconnect: true}, true); !strings.Contains(r.Error, "needs a host") {
		t.Errorf("autoconnect without host: %+v", r)
	}
	if r := save(control.LinkBlock{Name: "leaf.example.net", Host: "127.0.0.1", Fingerprint: fp, Password: "linkpw"}, true); r.Done == nil {
		t.Fatalf("add: %+v", r)
	}
	if r := save(control.LinkBlock{Name: "leaf.example.net", Fingerprint: fp}, true); !strings.Contains(r.Error, "already") {
		t.Errorf("add twice: %+v", r)
	}
	raw, _ := os.ReadFile(path)
	if !strings.Contains(string(raw), "[[link.server]]\nname = \"leaf.example.net\"\nhost = \"127.0.0.1\"\nport = 7000\nfingerprint = \""+fp+"\"\npassword = \"linkpw\"\nautoconnect = false") {
		t.Errorf("block not saved:\n%s", raw)
	}
	l := ask("Boss", control.Request{Cmd: control.CmdLinks}).Links
	if len(l.Blocks) != 1 || !l.Blocks[0].HasPassword || l.Blocks[0].Password != "" || l.Blocks[0].Port != 7000 {
		t.Errorf("blocks (no password sent): %+v", l.Blocks)
	}
	// Change without a password keeps it; then remove it.
	if r := save(control.LinkBlock{Name: "leaf.example.net", Host: "127.0.0.1", Port: 7001, Fingerprint: fp}, false); r.Done == nil {
		t.Fatalf("change: %+v", r)
	}
	raw, _ = os.ReadFile(path)
	if !strings.Contains(string(raw), "port = 7001") || !strings.Contains(string(raw), `password = "linkpw"`) {
		t.Errorf("change lost the password:\n%s", raw)
	}
	if r := save(control.LinkBlock{Name: "leaf.example.net", Host: "127.0.0.1", Port: 7001, Fingerprint: fp, NoPassword: true}, false); r.Done == nil {
		t.Fatalf("remove password: %+v", r)
	}
	if raw, _ = os.ReadFile(path); strings.Contains(string(raw), "linkpw") {
		t.Errorf("password still there:\n%s", raw)
	}
	if r := ask("Boss", control.Request{Cmd: control.CmdLinkDel, Name: "leaf.example.net"}); r.Done == nil {
		t.Fatalf("remove: %+v", r)
	}
	if raw, _ = os.ReadFile(path); strings.Contains(string(raw), "leaf.example.net") {
		t.Errorf("block still there:\n%s", raw)
	}
	if l := ask("Boss", control.Request{Cmd: control.CmdLinks}).Links; len(l.Blocks) != 0 {
		t.Errorf("blocks after remove: %+v", l.Blocks)
	}
}

func TestPanelLinksStandalone(t *testing.T) {
	s, _, _ := fileServer(t, "")
	b := boss(t, s)
	registered(t, s, "ember")
	b.send("AS OPER ember ADD CLASS helper HOST *@127.0.0.0/8 PASSWORD " + emberPW) // no c
	b.expect("ember is an oper now")
	if r := panelAsk(t, s, control.Request{Cmd: control.CmdLinks, As: "ember", IP: "127.0.0.1"}); r.Denied != "flag" {
		t.Errorf("links without c: %+v", r)
	}
	r := panelAsk(t, s, control.Request{Cmd: control.CmdLinkSave, As: "Boss", IP: "127.0.0.1", New: true,
		Block: &control.LinkBlock{Name: "leaf.example.net", Fingerprint: "x"}})
	if !strings.Contains(r.Error, "doesn't link") {
		t.Errorf("standalone: %+v", r)
	}
	if l := panelAsk(t, s, control.Request{Cmd: control.CmdLinks, As: "Boss", IP: "127.0.0.1"}).Links; l == nil || l.Role != "standalone" || l.Fingerprint != "" {
		t.Errorf("standalone links: %+v", l)
	}
}
