package link

import (
	"strings"
	"testing"

	"github.com/fhazal/beeircd/internal/irc"
)

// UnrealIRCd gets its extban names; what it sends becomes ours.
func TestUnrealExtbans(t *testing.T) {
	c := &unrealCodec{unreal: true, listModes: "beI", alwaysModes: "kLf", setModes: "lFH", rankModes: "qaohv"}
	out := c.Encode(&Mode{Source: "001AAAAAA", Channel: "#hive", Items: []ModeItem{
		{Add: true, Mode: 'b', Arg: "~m:x!*@*"}, {Add: true, Mode: 'e', Arg: "~a:Bee"}, {Add: true, Mode: 'l', Arg: "5"}}})
	if len(out) != 1 || !strings.HasSuffix(out[0], "MODE #hive +bel ~quiet:x!*@* ~account:Bee 5") {
		t.Fatalf("to UnrealIRCd: %q", out)
	}
	lines := c.Channel(Channel{Name: "#hive", TS: 1, Lists: []ListEntry{{Mode: 'b', Mask: "~t:5:~j:x!*@*", SetBy: "op", SetAt: 1}}}, "001")
	if !strings.Contains(lines[0], "&~time:5:~join:x!*@*") {
		t.Fatalf("SJOIN to UnrealIRCd: %q", lines)
	}
	m, _ := c.Decode(parse(t, ":001AAAAAA MODE #hive +bb ~quiet:x!*@* ~country:NL")).(*Mode)
	if m == nil || m.Items[0].Arg != "~q:x!*@*" || m.Items[1].Arg != "~C:NL" {
		t.Fatalf("from UnrealIRCd: %+v", m)
	}
	ch, _ := c.Decode(parse(t, ":001 SJOIN 1 #hive :&~nickchange:x!*@* \"~msgbypass:external:~account:Bee")).(*Channel)
	if ch == nil || len(ch.Lists) != 2 || ch.Lists[0].Mask != "~n:x!*@*" || ch.Lists[1].Mask != "~m:external:~a:Bee" {
		t.Fatalf("SJOIN from UnrealIRCd: %+v", ch)
	}
	bee := &unrealCodec{bee: true, listModes: "beI"}
	if out := bee.Encode(&Mode{Source: "001AAAAAA", Channel: "#hive", Items: []ModeItem{{Add: true, Mode: 'b', Arg: "~m:x!*@*"}}}); !strings.HasSuffix(out[0], "+b ~m:x!*@*") {
		t.Fatalf("to BeeIRCd: %q", out)
	}
}

// InspIRCd gets its names (and nothing it doesn't have); what it sends
// becomes ours.
func TestInspExtbans(t *testing.T) {
	b := testBridge(t)
	b.Out(":0BE SJOIN 1700000000 #hive +nt :@0BEAAAAAA")
	cases := []struct{ in, want string }{
		{":0BE SJOIN 1700000000 #hive +nt :@0BEAAAAAA &~m:x!*@* &~a:0", "LMODE #hive 1700000000 b mute:x!*@* 0BE 0 unauthed:*!*@* 0BE 0"},
		{":0BEAAAAAA MODE #hive +bb ~q:~a:Bee ~j:x!*@*", "FMODE #hive 1700000000 +b mute:account:Bee"},
	}
	for _, c := range cases {
		if got := b.Out(c.in); !has(got, c.want) {
			t.Errorf("%s\n  sent %q\n  want %q", c.in, got, c.want)
		}
	}
	if got := b.Out(":0BEAAAAAA MODE #hive +b ~p:x!*@*"); len(got) != 0 {
		t.Errorf("~p (no such thing in InspIRCd) sent: %q", got)
	}
	run := func(l string) []string { r, _ := b.In(l); return r }
	if got := run(":1INAAAAAA FMODE #hive 1700000000 +bb m:R:Bee !R:x"); !has(got, "MODE #hive +b ~q:~a:Bee") || has(got, "!R") {
		t.Errorf("FMODE from InspIRCd: %q", got)
	}
	if got := run(":1IN LMODE #hive 1700000000 b nonick:*!*@x setter 1700000000"); !has(got, "&~n:*!*@x") {
		t.Errorf("LMODE from InspIRCd: %q", got)
	}
	// G-lines by place: InspIRCd has none, so it gets nothing
	if got := b.Out(":0BE TKL + G ~C: NL Boss 0 1700000000 :no"); len(got) != 0 {
		t.Errorf("place ban sent to InspIRCd: %q", got)
	}
	if got := b.Out(":0BE TKL + G * 192.0.2.1 Boss 0 1700000000 :no"); !has(got, "ADDLINE G *@192.0.2.1") {
		t.Errorf("plain G-line: %q", got)
	}
}

// Network bans by place: UnrealIRCd's ~country: NL, BeeIRCd's ~C: NL.
func TestPlaceBansOnLinks(t *testing.T) {
	ban := &Ban{Source: "0BE", Add: true, Type: BanGline, User: "~C:", Host: "NL", SetBy: "Boss", SetAt: 1700000000, Reason: "no"}
	unreal := &unrealCodec{unreal: true}
	if got := unreal.encodeBan(ban); got != ":0BE TKL + G ~country: NL Boss 0 1700000000 :no" {
		t.Errorf("to UnrealIRCd: %q", got)
	}
	bee := &unrealCodec{bee: true}
	if got := bee.encodeBan(ban); got != ":0BE TKL + G ~C: NL Boss 0 1700000000 :no" {
		t.Errorf("to BeeIRCd: %q", got)
	}
	in, _ := unreal.decodeBan("001", []string{"+", "G", "~asn:", "64500", "admin", "0", "1700000000", "no"}).(*Ban)
	if in == nil || in.User != "~asn:" || in.Host != "64500" {
		t.Errorf("from UnrealIRCd: %+v", in)
	}
}

func parse(t *testing.T, l string) irc.Message {
	t.Helper()
	m, err := irc.Parse(l)
	if err != nil {
		t.Fatal(err)
	}
	return m
}
