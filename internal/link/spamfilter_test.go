package link

import (
	"testing"

	"github.com/fhazal/beeircd/internal/irc"
)

// The TKL F line is UnrealIRCd 6's, field for field (checked against
// src/modules/tkl.c of 6.2.7), and reads back the same.
func TestSpamFilterTKLLine(t *testing.T) {
	c := &unrealCodec{}
	it := &SpamFilter{Source: "0BE", Add: true, Targets: "cp", Action: 'g', SetBy: "Boss", SetAt: 1700000000, BanTime: 86400,
		Reason: "No spam_here", MatchType: "simple", Match: "*free nitro*"}
	lines := c.Encode(it)
	want := ":0BE TKL + F cp g Boss 0 1700000000 86400 No_spam__here simple :*free nitro*"
	if len(lines) != 1 || lines[0] != want {
		t.Fatalf("got %q\nwant %q", lines, want)
	}
	m, err := irc.Parse(lines[0])
	if err != nil {
		t.Fatal(err)
	}
	back, ok := c.Decode(m).(*SpamFilter)
	if !ok || back.Reason != "No spam_here" || back.Action != 'g' || back.Match != "*free nitro*" || back.BanTime != 86400 || !back.Add || back.Source != "0BE" {
		t.Fatalf("decoded %+v", back)
	}
	// A line from UnrealIRCd with a regex and a removal.
	m, _ = irc.Parse(":001 TKL - F u K admin 0 1700000001 0 Bot_nicks regex :^bot[0-9]+!")
	back, ok = c.Decode(m).(*SpamFilter)
	if !ok || back.Add || back.MatchType != "regex" || back.Action != 'K' || back.Reason != "Bot nicks" {
		t.Fatalf("decoded %+v", back)
	}
	// Too short: dropped, never a crash.
	m, _ = irc.Parse(":001 TKL + F cp")
	if c.Decode(m) != nil {
		t.Fatal("short TKL F decoded")
	}
}
