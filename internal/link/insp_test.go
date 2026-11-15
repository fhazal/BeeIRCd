package link

import (
	"strings"
	"testing"

	"github.com/fhazal/beeircd/internal/config"
	"github.com/fhazal/beeircd/internal/irc"
)

const fpInsp = "CCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCA="

// inspCapab is the CAPAB block InspIRCd 4.12 sends (shortened lists).
var inspCapab = []string{
	"CAPAB START 1206",
	"CAPAB CAPABILITIES :CASEMAPPING=ascii MAXNICK=30 PROTOCOL=1206",
	"CAPAB CHANMODES :list:ban=b simple:inviteonly=i param:key=k param-set:limit=l prefix:30000:op=@o prefix:10000:voice=+v simple:noextmsg=n simple:topiclock=t simple:moderated=m simple:secret=s",
	"CAPAB USERMODES :simple:invisible=i simple:oper=o param-set:snomask=s simple:cloak=x simple:wallops=w",
	"CAPAB END",
}

func inspLocal(blocks ...config.LinkServer) Local {
	me := local("bee.test", "0BE", blocks...)
	me.Casemapping = "ascii"
	return me
}

// talk feeds lines to h and returns what it sent back.
func talk(t *testing.T, h Handshake, lines ...string) ([]string, error) {
	t.Helper()
	var out []string
	for _, l := range lines {
		m, err := irc.Parse(l)
		if err != nil {
			t.Fatalf("bad line %q", l)
		}
		o, err := h.Handle(m)
		out = append(out, o...)
		if err != nil {
			return out, err
		}
	}
	return out, nil
}

func with(lines []string, more ...string) []string {
	return append(append([]string{}, lines...), more...)
}

func TestInspOutgoing(t *testing.T) {
	blk := config.LinkServer{Name: "insp.test", Host: "h", Port: 7000, Fingerprint: fpInsp, Password: "pw", Protocol: "inspircd"}
	h := NewInsp(inspLocal(), true, &blk, fpInsp)
	start := h.Start()
	if len(start) != 3 || start[0] != "CAPAB START 1206" || !strings.Contains(start[1], "CASEMAPPING=ascii") {
		t.Fatalf("start = %q", start)
	}
	out, err := talk(t, h, inspCapab...)
	if err != nil || len(out) != 1 || out[0] != "SERVER bee.test pw 0BE :test bee.test" {
		t.Fatalf("after CAPAB END: %q %v", out, err)
	}
	if _, err := talk(t, h, "SERVER insp.test pw 1IN :InspIRCd test"); err != nil || !h.Done() {
		t.Fatalf("SERVER: %v done %v", err, h.Done())
	}
	p := h.Peer()
	if p.Name != "insp.test" || p.SID != "1IN" || p.Software != "InspIRCd" || p.Protocol != 1206 {
		t.Errorf("peer %+v", p)
	}
	if _, ok := h.Codec().(interface{ TellsRenames() bool }); !ok {
		t.Error("codec doesn't tell renames")
	}
	if _, ok := h.(Bridger); !ok {
		t.Error("not a Bridger")
	}
}

func TestInspIncoming(t *testing.T) {
	blk := config.LinkServer{Name: "insp.test", Fingerprint: fpInsp, Password: "pw", Protocol: "inspircd"}
	h := NewInsp(inspLocal(blk), false, nil, fpInsp)
	if s := h.Start(); s != nil {
		t.Fatalf("incoming start = %q", s)
	}
	m, _ := irc.Parse(inspCapab[0])
	if !LooksInsp(m) {
		t.Fatal("CAPAB not seen as InspIRCd")
	}
	out, err := talk(t, h, inspCapab...)
	if err != nil || len(out) != 3 || out[0] != "CAPAB START 1206" {
		t.Fatalf("capab reply %q %v", out, err)
	}
	out, err = talk(t, h, "SERVER insp.test pw 1IN :InspIRCd test")
	if err != nil || !h.Done() || len(out) != 1 || !strings.HasPrefix(out[0], "SERVER bee.test pw 0BE ") {
		t.Fatalf("server reply %q %v", out, err)
	}
}

func TestInspRefusals(t *testing.T) {
	blk := []config.LinkServer{{Name: "insp.test", Fingerprint: fpInsp, Password: "pw", Protocol: "inspircd"}}
	cases := []struct {
		name, want string
		fp         string
		blocks     []config.LinkServer
		lines      []string
	}{
		{"old protocol", "too old", fpInsp, blk, []string{"CAPAB START 1205"}},
		{"casemapping", "casemapping differs", fpInsp, blk, []string{"CAPAB START 1206", "CAPAB CAPABILITIES :CASEMAPPING=rfc1459", "CAPAB END"}},
		{"no block", "no [[link.server]] block", fpInsp, nil, with(inspCapab, "SERVER insp.test pw 1IN :x")},
		{"fingerprint", "fingerprint", fpLeaf, blk, with(inspCapab, "SERVER insp.test pw 1IN :x")},
		{"password", "password", fpInsp, blk, with(inspCapab, "SERVER insp.test nope 1IN :x")},
		{"hmac", "CHALLENGE", fpInsp, blk, with(inspCapab, "SERVER insp.test AUTH:abc 1IN :x")},
		{"bad sid", "Invalid format server ID", fpInsp, blk, with(inspCapab, "SERVER insp.test pw IN1 :x")},
		{"our sid", "already exists", fpInsp, blk, with(inspCapab, "SERVER insp.test pw 0BE :x")},
		{"server first", "before CAPAB END", fpInsp, blk, []string{"SERVER insp.test pw 1IN :x"}},
		{"other protocol", "not an InspIRCd", fpInsp, blk, []string{"PASS :pw"}},
	}
	for _, c := range cases {
		h := NewInsp(inspLocal(c.blocks...), false, nil, c.fp)
		_, err := talk(t, h, c.lines...)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err %v, want %q", c.name, err, c.want)
		}
		if h.Done() {
			t.Errorf("%s: linked anyway", c.name)
		}
	}
}

func testBridge(t *testing.T) *inspBridge {
	t.Helper()
	blk := config.LinkServer{Name: "insp.test", Host: "h", Fingerprint: fpInsp, Password: "pw", Protocol: "inspircd"}
	h := NewInsp(inspLocal(), true, &blk, fpInsp)
	h.Start()
	if _, err := talk(t, h, with(inspCapab, "SERVER insp.test pw 1IN :x")...); err != nil {
		t.Fatal(err)
	}
	return h.(Bridger).Bridge().(*inspBridge)
}

func has(lines []string, want string) bool {
	for _, l := range lines {
		if strings.Contains(l, want) {
			return true
		}
	}
	return false
}

func TestInspBridgeOut(t *testing.T) {
	b := testBridge(t)
	first := b.Out(":0BE SID leaf.test 2 0LF :a leaf")
	if len(first) == 0 || !strings.HasPrefix(first[0], ":0BE BURST ") || !has(first, "SINFO rawversion") || !has(first, ":0BE SERVER leaf.test 0LF :a leaf") {
		t.Fatalf("first out %q", first)
	}
	cases := []struct{ in, want string }{
		{":0BE EOS", ":0BE ENDBURST"},
		{":0BE UID bee 1 1700000000 ~bee host.example 0BEAAAAAA Bee +iox * cloak.example fwAAAQ== :The Bee", "UID 0BEAAAAAA 1700000000 bee host.example cloak.example ~bee ~bee 127.0.0.1 "},
		{":0BE UID bee 1 1700000000 ~bee host.example 0BEAAAAAA Bee +iox * cloak.example fwAAAQ== :The Bee", "OPERTYPE"},
		{":0BE UID bee 1 1700000000 ~bee host.example 0BEAAAAAA Bee +iox * cloak.example fwAAAQ== :The Bee", "METADATA 0BEAAAAAA accountname :Bee"},
		{":0BE SJOIN 1700000000 #hive +ntk key :@0BEAAAAAA +0BEAAAAAB &*!*@spam", "FJOIN #hive 1700000000 +ntk key :o,0BEAAAAAA v,0BEAAAAAB"},
		{":0BE SJOIN 1700000000 #hive +ntk key :@0BEAAAAAA +0BEAAAAAB &*!*@spam", "LMODE #hive 1700000000 b *!*@spam"},
		{":0BEAAAAAA MODE #hive +l 10", "FMODE #hive 1700000000 +l 10"},
		{":0BEAAAAAA TOPIC #hive 0BEAAAAAA 1700000001 :hi there", "FTOPIC #hive 1700000000 1700000001"},
		{":0BEAAAAAA PRIVMSG #hive :hello there", ":0BEAAAAAA PRIVMSG #hive :hello there"},
		{":0BEAAAAAA AWAY :lunch", ":0BEAAAAAA AWAY 1800000000 :lunch"},
		{":0BE SVSNICK 1INAAAAAA 1INAAAAAA 0", "SAVE 1INAAAAAA"},
		{":0BE TKL + G ~bot *.example oper 1700003600 1700000000 :no bots", "ADDLINE G ~bot@*.example oper 1700000000 3600 :no bots"},
		{":0BE TKL - G ~bot *.example oper", "DELLINE G ~bot@*.example"},
		{":0BE PING 0BE", "PING 1IN"},
		{":0BEAAAAAA CHGHOST 1INAAAAAA new.host", "ENCAP 1IN CHGHOST 1INAAAAAA new.host"},
		{":0BE 311 1INAAAAAA bee ~bee host * :The Bee", "NUM 0BE 1INAAAAAA 311"},
	}
	for _, c := range cases {
		got := b.Out(c.in)
		if !has(got, c.want) {
			t.Errorf("%s\n  sent %q\n  want %q", c.in, got, c.want)
		}
	}
	for _, drop := range []string{":0BE NETINFO 1 2 3 4 5 6 7 :BeeNet", ":0BE BEESESS x", ":0BEAAAAAA WHOIS 1INAAAAAA :x"} {
		if got := b.Out(drop); len(got) != 0 {
			t.Errorf("%s not dropped: %q", drop, got)
		}
	}
}

func TestInspBridgeIn(t *testing.T) {
	b := testBridge(t)
	run := func(l string) []string {
		r, _ := b.In(l)
		return r
	}
	// in the burst the UID waits for its METADATA account
	if r := run(":1IN UID 1INAAAAAA 1700000000 insp real.host cloak.host ~i ~i 192.0.2.1 1700000000 +i :Insp User"); len(r) != 0 {
		t.Fatalf("UID not held in the burst: %q", r)
	}
	if r := run(":1IN METADATA 1INAAAAAA accountname :Acct"); len(r) != 0 {
		t.Fatalf("METADATA ran early: %q", r)
	}
	r := run(":1IN FJOIN #hive 1700000000 +nt :o,1INAAAAAA")
	if !has(r, "UID insp 1 1700000000 ~i real.host 1INAAAAAA Acct +ix * cloak.host") || !has(r, "SJOIN 1700000000 #hive +nt :@1INAAAAAA") {
		t.Fatalf("burst UID/FJOIN: %q", r)
	}
	cases := []struct{ in, want string }{
		{":1IN SERVER leaf.insp 2IN :a leaf", ":1IN SID leaf.insp 2 2IN :a leaf"},
		{":1INAAAAAA OPERTYPE :NetAdmin", "UMODE2 +o"},
		{":1IN LMODE #hive 1700000000 b *!*@x setter 1700000000", "SJOIN 1700000000 #hive"},
		{":1INAAAAAA FMODE #hive 1700000000 +m", ":1INAAAAAA MODE #hive +m"},
		{":1INAAAAAA FTOPIC #hive 1700000000 1700000005 insp!~i@h :new topic", "TOPIC #hive"},
		{":1INAAAAAA NICK newnick 1700000009", "NICK newnick"},
		{":1IN SAVE 0BEAAAAAA 1700000000", "SVSNICK 0BEAAAAAA 0BEAAAAAA 100"},
		{":1INAAAAAA KICK #hive 0BEAAAAAA 5 :out", "KICK #hive 0BEAAAAAA :out"},
		{":1INAAAAAA ADDLINE G *@203.0.113.9 oper 1700000000 3600 :bad", "TKL + G * 203.0.113.9"},
		{":1INAAAAAA DELLINE G *@203.0.113.9", "TKL - G * 203.0.113.9"},
		{":1INAAAAAA FHOST new.cloak", "SETHOST new.cloak"},
		{":1IN NUM 1IN 0BEAAAAAA 311 insp ~i h * :x", ":1IN 311 0BEAAAAAA insp"},
		{":1IN ENDBURST", ":1IN EOS"},
	}
	for _, c := range cases {
		if got := run(c.in); !has(got, c.want) {
			t.Errorf("%s\n  ran %q\n  want %q", c.in, got, c.want)
		}
	}
	// a channel mode with a newer TS than ours for the channel is dropped
	if got := run(":1INAAAAAA FMODE #hive 1800000000 +s"); len(got) != 0 {
		t.Errorf("newer-TS FMODE ran: %q", got)
	}
	// after the burst a UID runs at once
	if r := run(":1IN UID 1INAAAAAB 1700000100 live h h u u 192.0.2.2 1700000100 + :x"); !has(r, "UID live ") {
		t.Errorf("live UID held: %q", r)
	}
	if _, rep := b.In(":1IN PING 0BE"); !has(rep, "PONG") {
		t.Errorf("PING not answered: %q", rep)
	}
	if _, rep := b.In(":1INAAAAAA IDLE 0BEAAAAAA"); !has(rep, ":0BEAAAAAA IDLE 1INAAAAAA") {
		t.Errorf("IDLE not answered: %q", rep)
	}
}

// Found while linking a real InspIRCd: it refuses a nick TS of 0.
func TestInspNoZeroTS(t *testing.T) {
	b := testBridge(t)
	for _, l := range b.Out(":0BE UID NickServ 1 0 services services.test 0BEAAAAAS 0 +io * * * :svc") {
		if f := strings.Fields(l); len(f) > 3 && f[1] == "UID" && f[3] == "0" {
			t.Errorf("TS 0 sent: %q", l)
		}
	}
}

var inspSeeds = []string{
	":1IN UID 1INAAAAAA 1700000000 insp real.host cloak.host ~i ~i 192.0.2.1 1700000000 +is +cC :Insp User",
	":1IN METADATA 1INAAAAAA accountname :Acct",
	":1IN METADATA * saslmechlist :PLAIN,EXTERNAL",
	":1IN FJOIN #hive 1700000000 +ntkl key 10 :o,1INAAAAAA:5 v,1INAAAAAB ,1INAAAAAC",
	":1IN IJOIN #hive 7 1700000000 o",
	":1IN LMODE #hive 1700000000 b *!*@x setter 1700000000 e *!*@y",
	":1INAAAAAA FMODE #hive 1700000000 +ov-b 1INAAAAAB 0BEAAAAAA *!*@x",
	":1INAAAAAA MODE 1INAAAAAA +iw-x",
	":1IN FTOPIC #hive 1700000000 1700000005 :topic",
	":1IN ENCAP 0BE SASL 1INAAAAAA * S PLAIN",
	":1IN ENCAP * CHGHOST 0BEAAAAAA new.host",
	":1INAAAAAA ADDLINE Z 203.0.113.0/24 oper 1700000000 0 :x",
	":1IN SAVE 0BEAAAAAA 1700000000",
	":1IN NUM 1IN 0BEAAAAAA 318 :End",
	":1IN SERVER leaf 2IN :x",
	":1IN SQUIT 2IN :gone",
	"CAPAB START 1206",
}

// FuzzInspLine runs fuzzed InspIRCd lines in and our lines out: nothing
// may panic, and every line sent on must stay one clean line.
func FuzzInspLine(f *testing.F) {
	for _, s := range inspSeeds {
		f.Add(s)
	}
	for _, s := range linkSeeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, l string) {
		b := testBridge(t)
		run, reply := b.In(l)
		clean(t, l, reply)
		for _, r := range run {
			clean(t, l, []string{r})
		}
		// Out only gets the server's own lines, which always name a clean
		// source (as in roundTrip).
		if m, err := irc.Parse(l); err == nil && m.Source != "" && !strings.ContainsAny(m.Source, "\r\n\x00 ") {
			clean(t, l, b.Out(l))
		}
	})
}
