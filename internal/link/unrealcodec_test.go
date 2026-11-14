package link

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/fhazal/beeircd/internal/irc"
)

func decode(t *testing.T, c Codec, line string) any {
	t.Helper()
	m, err := irc.Parse(line)
	if err != nil {
		t.Fatalf("parse %q: %v", line, err)
	}
	return c.Decode(m)
}

func TestUnrealCodecUser(t *testing.T) {
	c := &unrealCodec{paramModes: "kfLlFH"}
	u := User{UID: "0BEAAAAAB", Nick: "Bee", NickTS: 1794434552, Hops: 1, Ident: "~bee", RealHost: "203.0.113.9",
		Host: "BeeNet-3f9a.cloak", IP: "203.0.113.9", Account: "Bee", Modes: "iswx", Realname: "Busy Bee"}
	lines := c.User(u, "0BE")
	want := ":0BE UID Bee 1 1794434552 ~bee 203.0.113.9 0BEAAAAAB Bee +iwx * BeeNet-3f9a.cloak ywBxCQ== :Busy Bee"
	if len(lines) != 1 || lines[0] != want {
		t.Fatalf("got %q\nwant %q", lines, want)
	}
	got := decode(t, c, lines[0]).(*User)
	u.Modes = "iwx"  // s doesn't travel in UID
	u.Cloak = u.Host // the cloak field comes back as the cloak
	if !reflect.DeepEqual(*got, u) {
		t.Errorf("round trip:\n got %+v\nwant %+v", *got, u)
	}
	// a real UnrealIRCd line: no account, no cloak shown (-x), IPv6
	got = decode(t, c, ":0SD UID Ember 1 1794434000 ember 2001:db8::5 0SDAAAAAC 0 +iw * Sand-1.ip IAENuAAAAAAAAAAAAAAABQ== :Ember").(*User)
	if got.Account != "" || got.Host != "2001:db8::5" || got.IP != "2001:db8::5" || got.Modes != "iw" {
		t.Errorf("unreal user %+v", got)
	}
	// +x with a vhost (+t): the vhost is shown
	got = decode(t, c, ":0SD UID V 1 1 v real.host 0SDAAAAAD acct +ixt my.vhost cloak.host * :V").(*User)
	if got.Host != "my.vhost" || got.Account != "acct" || got.IP != "" {
		t.Errorf("vhost user %+v", got)
	}
}

func TestUnrealCodecChannel(t *testing.T) {
	c := &unrealCodec{paramModes: "kfLlFH"}
	ch := Channel{Name: "#bee", TS: 1700000000, Modes: "klnt", Args: map[byte]string{'k': "sesame", 'l': "50"},
		Members: []Member{{UID: "0BEAAAAAB", Ranks: "qo"}, {UID: "0BEAAAAAC", Ranks: "v"}, {UID: "0BEAAAAAD"}},
		Lists:   []ListEntry{{Mode: 'b', Mask: "*!*@bad.example", SetBy: "Bee", SetAt: 1700000100}, {Mode: 'I', Mask: "*!*@good.example", SetBy: "Bee", SetAt: 1700000200}}}
	lines := c.Channel(ch, "0BE")
	want := `:0BE SJOIN 1700000000 #bee +klnt sesame 50 :*@0BEAAAAAB +0BEAAAAAC 0BEAAAAAD <1700000100,Bee>&*!*@bad.example <1700000200,Bee>'*!*@good.example`
	if len(lines) != 1 || lines[0] != want {
		t.Fatalf("got %q\nwant %q", lines, want)
	}
	got := decode(t, c, lines[0]).(*Channel)
	if !reflect.DeepEqual(*got, ch) {
		t.Errorf("round trip:\n got %+v\nwant %+v", *got, ch)
	}

	// many members: several lines, each within the limit, nothing lost
	big := Channel{Name: "#big", TS: 1, Args: map[byte]string{}}
	for i := 0; i < 200; i++ {
		big.Members = append(big.Members, Member{UID: fmt.Sprintf("0BEA%05d", i), Ranks: "o"})
	}
	lines = c.Channel(big, "0BE")
	if len(lines) < 3 {
		t.Fatalf("200 members in %d lines", len(lines))
	}
	n := 0
	for _, l := range lines {
		if len(l) > sjoinLineMax {
			t.Errorf("line of %d bytes", len(l))
		}
		n += len(decode(t, c, l).(*Channel).Members)
	}
	if n != 200 {
		t.Errorf("%d members came back", n)
	}

	// UnrealIRCd's +f takes a parameter (its CHANMODES group B)
	got = decode(t, c, ":0SD SJOIN 5 #f +fnt [5j]:10 :@0SDAAAAAB").(*Channel)
	if got.Modes != "fnt" || got.Args['f'] != "[5j]:10" || len(got.Members) != 1 || got.Members[0].Ranks != "o" {
		t.Errorf("+f channel %+v", got)
	}
	// a members-only line (no modes)
	got = decode(t, c, ":0SD SJOIN 5 #f :+0SDAAAAAC").(*Channel)
	if got.Modes != "" || len(got.Members) != 1 || got.Members[0].Ranks != "v" {
		t.Errorf("members-only %+v", got)
	}
}

func TestUnrealCodecOthers(t *testing.T) {
	c := &unrealCodec{}
	if tp := decode(t, c, ":0SD TOPIC #bee Bee 1700000000 :hello world").(*Topic); tp.By != "Bee" || tp.At != 1700000000 || tp.Text != "hello world" {
		t.Errorf("topic %+v", tp)
	}
	if tp := decode(t, c, "TOPIC #bee Bee 1700000000 :no source").(*Topic); tp.Text != "no source" {
		t.Errorf("topic without source %+v", tp)
	}
	if a := decode(t, c, ":0SDAAAAAB AWAY :lunch").(*Away); a.UID != "0SDAAAAAB" || a.Message != "lunch" {
		t.Errorf("away %+v", a)
	}
	if k := decode(t, c, ":0SD KILL 0BEAAAAAB :Nick collision: a <- b").(*Kill); k.UID != "0BEAAAAAB" || !strings.HasPrefix(k.Reason, "Nick collision") {
		t.Errorf("kill %+v", k)
	}
	if decode(t, c, ":0SD SINFO x") != nil {
		t.Error("SINFO decoded")
	}
	if got := c.Topic(Topic{Channel: "#b", Text: "hi there", By: "Bee", At: 9}, "0BE"); got != ":0BE TOPIC #b Bee 9 :hi there" {
		t.Errorf("topic line %q", got)
	}
}

// Lines a real UnrealIRCd 6.2.7 sent in its burst (recorded in a
// sandbox), tags included.
func TestUnrealCodecRecorded(t *testing.T) {
	c := &unrealCodec{paramModes: "fkLlFH"}
	u := decode(t, c, "@s2s-md/server_port=18667;s2s-md/creationtime=1794436327;s2s-md/local_port=49972 :0SD UID ualice 0 1794436326 ualice localhost 0SDJR3001 0 +iwx * Clk-6D4627E9 fwAAAQ== :ualice test").(*User)
	if u.Nick != "ualice" || u.Hops != 0 || u.Host != "Clk-6D4627E9" || u.RealHost != "localhost" || u.IP != "127.0.0.1" || u.Account != "" || u.UID != "0SDJR3001" {
		t.Errorf("user %+v", u)
	}
	ch := decode(t, c, "@msgid=K7DSTkN8OOoM6E5RwjVSRw;time=2026-11-11T22:32:19.107Z :0SD SJOIN 1794436328 #mix +nt :@0SDJR3001 <1794436328,ualice!ualice@Clk-6D4627E9>&*!*@bad.unreal ").(*Channel)
	want := Channel{Name: "#mix", TS: 1794436328, Modes: "nt", Args: map[byte]string{},
		Members: []Member{{UID: "0SDJR3001", Ranks: "o"}},
		Lists:   []ListEntry{{Mode: 'b', Mask: "*!*@bad.unreal", SetBy: "ualice!ualice@Clk-6D4627E9", SetAt: 1794436328}}}
	if !reflect.DeepEqual(*ch, want) {
		t.Errorf("sjoin\n got %+v\nwant %+v", *ch, want)
	}
	tp := decode(t, c, "TOPIC #mix ualice!ualice@Clk-6D4627E9 1794436328 :unreal topic").(*Topic)
	if tp.Text != "unreal topic" || tp.By != "ualice!ualice@Clk-6D4627E9" {
		t.Errorf("topic %+v", tp)
	}
}
