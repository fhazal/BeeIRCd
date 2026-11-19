package link

import (
	"testing"
)

// The oper tools, caller ID and ALLTIME cross an InspIRCd link in both
// directions, in InspIRCd's own words.
func TestInspBridgeOperToolsAndAccept(t *testing.T) {
	b := testBridge(t)
	b.Out(":0BE EOS")
	for _, c := range []struct{ in, want string }{
		{":0BEAAAAAA SAJOIN 1INAAAAAA #hive", ":0BEAAAAAA SAJOIN 1INAAAAAA #hive"},
		{":0BEAAAAAA SAPART 1INAAAAAA #hive :go now", ":0BEAAAAAA SAPART 1INAAAAAA #hive :go now"},
		{":0BEAAAAAA SANICK 1INAAAAAA newnick", ":0BEAAAAAA SANICK 1INAAAAAA newnick"},
		{":0BEAAAAAA ACCEPT 1INAAAAAA", ":0BEAAAAAA ACCEPT 1INAAAAAA"},
		{":0BEAAAAAA ACCEPT -1INAAAAAA", ":0BEAAAAAA ACCEPT -1INAAAAAA"},
		{":0BEAAAAAA TSCTL alltime", ":0BEAAAAAA ALLTIME"},
	} {
		if got := b.Out(c.in); !has(got, c.want) {
			t.Errorf("out %s\n  sent %q\n  want %q", c.in, got, c.want)
		}
	}
	if got := b.Out(":0BEAAAAAA TSCTL offset +1"); len(got) != 0 {
		t.Errorf("another TSCTL went out: %q", got)
	}
	b.In(":1IN ENDBURST")
	for _, c := range []struct{ in, want string }{
		{":1INAAAAAA ENCAP 0BE SAJOIN 0BEAAAAAA #hive", ":1INAAAAAA SAJOIN 0BEAAAAAA #hive"},
		{":1INAAAAAA ENCAP 0BE SAPART 0BEAAAAAA #hive :go now", ":1INAAAAAA SAPART 0BEAAAAAA #hive :go now"},
		{":1INAAAAAA ENCAP 0BE SANICK 0BEAAAAAA newnick", ":1INAAAAAA SANICK 0BEAAAAAA newnick"},
		{":1INAAAAAA ENCAP * ALLTIME", ":1INAAAAAA TSCTL alltime"},
		{":1INAAAAAA ACCEPT 0BEAAAAAA", ":1INAAAAAA ACCEPT 0BEAAAAAA"},
		{":1INAAAAAA ACCEPT -0BEAAAAAA", ":1INAAAAAA ACCEPT -0BEAAAAAA"},
		{":1IN METADATA 1INAAAAAA callerid_data :1700000000,0BEAAAAAA,0BEAAAAAB", ":1INAAAAAA ACCEPT 0BEAAAAAB"},
	} {
		if got, _ := b.In(c.in); !has(got, c.want) {
			t.Errorf("in %s\n  ran %q\n  want %q", c.in, got, c.want)
		}
	}
}

// A ban only goes to InspIRCd in a form it has: an extban it lacks is left
// out, and a forward ban becomes banredirect's mask#channel.
func TestInspBanOut(t *testing.T) {
	b := testBridge(t)
	b.theirExt = parseInspExtbans("matching:account=R acting:mute=m matching:realname=r")
	for in, want := range map[string]string{
		"*!*@host":                "*!*@host",
		"~a:Bee":                  "account:Bee",
		"~q:*!*@loud":             "mute:*!*@loud",
		"~q:~a:Bee":               "mute:account:Bee",
		"~f:#over:*!*@fwd":        "*!*@fwd#over",
		"~r:*a:b*":                "realname:*a:b*",
		"~t:5:*!*@short":          "*!*@short",
		"~server:*.far":           "",
		"~q:~server:*.far":        "",
		"~f:#over:~a:Bee":         "",
		"~noctcp:*!*@x":           "",
		"~j:*!*@nojoin":           "",
		"~security-group:unknown": "",
	} {
		got, ok := b.banOut(in)
		if ok != (want != "") || got != want {
			t.Errorf("banOut(%q) = %q, %v; want %q", in, got, ok, want)
		}
	}
	b.theirExt["redirect"] = "acting"
	if got, ok := b.banOut("~f:#over:*!*@fwd"); !ok || got != "redirect:#over:*!*@fwd" {
		t.Errorf("with their redirect extban: %q, %v", got, ok)
	}
	if got, ok := b.banOut("~f:#over:~a:Bee"); !ok || got != "redirect:#over:account:Bee" {
		t.Errorf("an extban inside their redirect extban: %q, %v", got, ok)
	}
	b.theirExt = nil // an older server that sent no list: as before
	if got, ok := b.banOut("~server:*.far"); !ok || got != "server:*.far" {
		t.Errorf("without their list: %q, %v", got, ok)
	}
}

// GLOBOPS goes to InspIRCd as its SNONOTICE G ("From nick: text") and an
// SNONOTICE G from it comes in as GLOBOPS. Other letters stay ignored.
func TestInspGlobops(t *testing.T) {
	b := testBridge(t)
	b.Out(":0BE EOS")
	b.Out(":0BE UID bee 1 1700000000 ~bee host.example 0BEAAAAAA Bee +iox * cloak.example fwAAAQ== :The Bee")
	if got := b.Out(":0BEAAAAAA GLOBOPS :maintenance at noon"); !has(got, ":0BE SNONOTICE G :From bee: maintenance at noon") {
		t.Errorf("out %q", got)
	}
	b.In(":1IN ENDBURST")
	if got, _ := b.In(":1IN SNONOTICE G :From carol: hello"); !has(got, ":1IN GLOBOPS :From carol: hello") {
		t.Errorf("in G %q", got)
	}
	if got, _ := b.In(":1IN SNONOTICE g :From carol: hello"); !has(got, ":1IN GLOBOPS :From carol: hello") {
		t.Errorf("in g %q", got)
	}
	if got, _ := b.In(":1IN SNONOTICE A :announcement"); len(got) != 0 {
		t.Errorf("another letter came in: %q", got)
	}
	// From one of their people it comes in from that person, so
	// UnrealIRCd shows "from r5ci: hi", not the InspIRCd server.
	b.In(":1IN UID 1INAAAAAC 1700000000 r5ci h h r5ci r5ci 192.0.2.3 1700000000 +o :x")
	if got, _ := b.In(":1IN SNONOTICE G :From r5ci: hi"); !has(got, ":1INAAAAAC GLOBOPS :hi") {
		t.Errorf("in from a user %q", got)
	}
}

// InspIRCd names an ADDLINE's or DELLINE's source in its notice
// (addline.cpp, delline.cpp: usr->nick), so a ban set or removed by
// someone online goes from their UID (UnrealIRCd's setter is
// nick!user@host); from anyone else it stays the server's.
func TestInspBanSourceIsSetter(t *testing.T) {
	b := testBridge(t)
	b.Out(":0BE EOS")
	b.Out(":0BE UID bee 1 1700000000 ~bee host.example 0BEAAAAAA Bee +iox * cloak.example fwAAAQ== :The Bee")
	cases := []struct{ in, want string }{
		{":0BE TKL + G ~bot *.example bee 1700003600 1700000000 :no bots", ":0BEAAAAAA ADDLINE G ~bot@*.example bee 1700000000 3600 :no bots"},
		{":0BE TKL + G ~bot *.example bee!~bee@host.example 1700003600 1700000000 :no bots", ":0BEAAAAAA ADDLINE G ~bot@*.example bee!~bee@host.example 1700000000 3600 :no bots"},
		{":0BE TKL - G ~bot *.example Bee", ":0BEAAAAAA DELLINE G ~bot@*.example"},
		{":0BE TKL + G ~bot *.example oper 1700003600 1700000000 :no bots", ":0BE ADDLINE G ~bot@*.example oper 1700000000 3600 :no bots"},
		{":0BE TKL - G ~bot *.example oper", ":0BE DELLINE G ~bot@*.example"},
	}
	for _, c := range cases {
		if got := b.Out(c.in); !has(got, c.want) {
			t.Errorf("%s\n  sent %q\n  want %q", c.in, got, c.want)
		}
	}
	// One of theirs with the setter's nick is not ours to speak for.
	b.In(":1IN ENDBURST")
	b.In(":1IN UID 1INAAAAAC 1700000000 carol h h carol carol 192.0.2.3 1700000000 + :x")
	if got := b.Out(":0BE TKL - G ~bot *.example carol"); !has(got, ":0BE DELLINE G ~bot@*.example") {
		t.Errorf("their nick: %q", got)
	}
}
