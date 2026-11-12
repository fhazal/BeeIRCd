package link

import (
	"bufio"
	"os"
	"strings"
	"testing"

	"github.com/fhazal/beeircd/internal/config"
	"github.com/fhazal/beeircd/internal/irc"
)

// TestUnrealReplay feeds a real UnrealIRCd 6.2.7 link (recorded on the
// homelab sandbox, 12 Nov 2026, Unreal connecting to a BeeIRCd hub) to the
// hub's handshake and checks it answers exactly what that UnrealIRCd
// accepted.
func TestUnrealReplay(t *testing.T) {
	f, err := os.Open("testdata/unreal-6.2.7-to-hub.txt")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	block := config.LinkServer{Name: "sand.unreal.local", Fingerprint: "TQkRhe43cfDP6MV5rP2QNJOBi+qS+/hP4+I+2UHP2U8="}
	me := local("bee.sand.local", "0BE", block)
	me.Network, me.Description, me.Version, me.Booted = "SandUnreal", "BeeIRCd 2a link test", "0.0.0-dev", 1794434541
	me.Now = func() int64 { return 1794434552 }
	me.ChanModes, me.UserModes = "beI,k,BEFHJLfl,CDKMNOPQRSTVZcimnprstz", "BDHRTWZdiopqrswxz"
	h := NewUnreal(me, false, nil, block.Fingerprint, none)
	var sent []string
	sc := bufio.NewScanner(f)
	sc.Buffer(nil, 1<<16)
	for sc.Scan() && !h.Done() {
		m, err := irc.Parse(sc.Text())
		if err != nil {
			t.Fatalf("parse %q: %v", sc.Text(), err)
		}
		out, err := h.Handle(m)
		if err != nil {
			t.Fatalf("refused %q: %v", sc.Text(), err)
		}
		sent = append(sent, out...)
	}
	if !h.Done() {
		t.Fatal("handshake not done after SERVER")
	}
	want := []string{
		"PASS :*",
		"PROTOCTL EAUTH=bee.sand.local,6100,,BeeIRCd-0.0.0-dev",
		"PROTOCTL SERVERS=*",
		"PROTOCTL NOQUIT NICKv2 SJOIN SJOIN2 UMODE2 VL SJ3 TKLEXT TKLEXT2 NICKIP ESVID NEXTBANS SJSBY MTAGS BEE=1 BEECASE=ascii BEESVC=0",
		"PROTOCTL CHANMODES=beI,k,BEFHJLfl,CDKMNOPQRSTVZcimnprstz USERMODES=BDHRTWZdiopqrswxz BOOTED=1794434541 PREFIX=(ov)@+ SID=0BE MLOCK TS=1794434552 EXTSWHOIS",
		"PROTOCTL NICKCHARS= CHANNELCHARS=utf8 BIGLINES",
		"SERVER bee.sand.local 1 :U6100-B-0BE BeeIRCd 2a link test",
	}
	if strings.Join(sent, "\n") != strings.Join(want, "\n") {
		t.Errorf("sent:\n%s\nwant:\n%s", strings.Join(sent, "\n"), strings.Join(want, "\n"))
	}
	p := h.Peer()
	if p.Name != "sand.unreal.local" || p.SID != "0SD" || p.Bee || p.Software != "UnrealIRCd-6.2.7" || p.Description != "UnrealIRCd 6 sandbox for PyLink fork" {
		t.Errorf("peer %+v", p)
	}
	if p.Tokens["PREFIX"] != "(ov)@+" || p.Tokens["CHANMODES"] != "beI,fkL,lFH,cdimnprstzCDGKMNOPQRSTVZ" {
		t.Errorf("tokens %v", p.Tokens)
	}
	if got := h.Burst(nil, nil); len(got) != 2 || got[1] != ":0BE EOS" || !strings.HasPrefix(got[0], "NETINFO 0 1794434552 6100 abc 0 0 0 :SandUnreal") {
		t.Errorf("burst %q", got)
	}
}
