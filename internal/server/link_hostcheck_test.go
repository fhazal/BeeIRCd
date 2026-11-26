package server

import (
	"strings"
	"testing"
	"time"
)

// A linked server sends a host or ident change with characters that break
// nick!user@host (or a very long one): the hub keeps the old one and tells
// its opers; a fine change still goes through.
func TestLinkBadHostChange(t *testing.T) {
	hub, leaf := linkedPair(t)
	login(t, leaf.s, "bob")
	hub.seen(t, "bob")
	b := hub.user("bob").UID
	before := hub.user("bob")
	for _, line := range []string{
		":B SETHOST evil@host.example",
		":B SETHOST evil!host.example",
		":B SETHOST *.example",
		":B SETHOST :two words",
		":B SETHOST " + strings.Repeat("a", 65),
		":B SETHOST ctl\x01host",
		":B SETIDENT a@b",
		":B SETIDENT a*b",
		":B SETIDENT waytoolongident",
	} {
		hub.fromLink(t, strings.Replace(line, ":B ", ":"+b+" ", 1))
	}
	time.Sleep(100 * time.Millisecond)
	if u := hub.user("bob"); u.Host != before.Host || u.Ident != before.Ident {
		t.Fatalf("bad change taken: %s@%s", u.Ident, u.Host)
	}
	if !hub.logHas("bad host") || !hub.logHas("bad ident") {
		t.Errorf("no notice about the refused changes")
	}
	if len(hub.servers()) == 0 || hub.servers()[leaf.sid] == "" {
		t.Fatal("link dropped")
	}
	hub.fromLink(t, ":"+b+" SETHOST user/bob_x.example")
	hub.fromLink(t, ":"+b+" SETIDENT bobby")
	waitLink(t, "good change", func() bool {
		u := hub.user("bob")
		return u.Host == "user/bob_x.example" && u.Ident == "bobby"
	})
	hub.fromLink(t, ":"+b+" SETHOST 2001:db8::1")
	waitLink(t, "IPv6 host", func() bool { return hub.user("bob").Host == "2001:db8::1" })
}

// A user introduction with a bad host or ident keeps the link and the
// user, with a safe host and ident in place of the bad ones.
func TestLinkBadHostInUID(t *testing.T) {
	hub, leaf := linkedPair(t)
	l := leaf.sid
	hub.fromLink(t, ":"+l+" UID mallory 1 1794444176 ev@l bad!host.example "+l+"ZZZZZZ 0 +i * * fwAAAQ== :x")
	hub.seen(t, "mallory")
	u := hub.user("mallory")
	if !validLinkHost(u.Host) || !validLinkHost(u.RealHost) || !validLinkIdent(u.Ident) {
		t.Fatalf("bad values kept: %s@%s (real %s)", u.Ident, u.Host, u.RealHost)
	}
	if u.Host != "127.0.0.1" {
		t.Errorf("host %q, want the user's IP", u.Host)
	}
	// cloaked (+x) with a bad vhost: the IP would uncover them, so the
	// valid cloak shows instead
	hub.fromLink(t, ":"+l+" UID trudy 1 1794444176 trudy real.example "+l+"ZZZZZY 0 +ix bad@vhost cloak.example fwAAAQ== :x")
	hub.seen(t, "trudy")
	if u := hub.user("trudy"); u.Host != "cloak.example" {
		t.Errorf("trudy shows %q, want the cloak", u.Host)
	}
	if !hub.logHas("bad host") {
		t.Error("no notice about the bad host")
	}
	if hub.servers()[l] == "" {
		t.Fatal("link dropped")
	}
}

func TestValidLinkHostIdent(t *testing.T) {
	for h, want := range map[string]bool{
		"host.example": true, "user/bob_x": true, "2001:db8::1": true, "a-b.c": true,
		"": false, "a@b": false, "a!b": false, "a*b": false, "a b": false, ":x": false,
		strings.Repeat("a", 64): true, strings.Repeat("a", 65): false, "é.example": false,
	} {
		if validLinkHost(h) != want {
			t.Errorf("validLinkHost(%q) = %v", h, !want)
		}
	}
	for id, want := range map[string]bool{
		"bob": true, "~bob": true, "b_o-b.x": true, "1234567890": true,
		"": false, "12345678901": false, "a@b": false, "a!b": false, "a*b": false, "a b": false, ":x": false,
	} {
		if validLinkIdent(id) != want {
			t.Errorf("validLinkIdent(%q) = %v", id, !want)
		}
	}
}
