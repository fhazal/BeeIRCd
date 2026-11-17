package link

import "testing"

// A soft ban (% before the user) keeps its % to and from UnrealIRCd, and
// is never sent to InspIRCd, which has no soft bans.
func TestSoftBansOnLinks(t *testing.T) {
	ban := &Ban{Source: "0BE", Add: true, Type: BanGline, User: "%*", Host: "203.0.113.0/24", SetBy: "Boss", SetAt: 1700000000, Reason: "log in"}
	unreal := &unrealCodec{unreal: true}
	if got := unreal.encodeBan(ban); got != ":0BE TKL + G %* 203.0.113.0/24 Boss 0 1700000000 :log in" {
		t.Errorf("to UnrealIRCd: %q", got)
	}
	in, _ := unreal.decodeBan("001", []string{"+", "G", "%*", "203.0.113.0/24", "admin", "0", "1700000000", "log in"}).(*Ban)
	if in == nil || in.User != "%*" || in.Host != "203.0.113.0/24" {
		t.Errorf("from UnrealIRCd: %+v", in)
	}
	in, _ = unreal.decodeBan("001", []string{"+", "G", "%", "203.0.113.0/24", "admin", "0", "1700000000", "log in"}).(*Ban)
	if in == nil || in.User != "%*" {
		t.Errorf("a bare %% from UnrealIRCd: %+v", in)
	}

	b := testBridge(t)
	b.Out(":0BE EOS") // the first line out carries the burst
	if got := b.Out(":0BE TKL + G %* 203.0.113.0/24 Boss 0 1700000000 :log in"); len(got) != 0 {
		t.Errorf("soft ban sent to InspIRCd: %q", got)
	}
	if got := b.Out(":0BE TKL + G * 203.0.113.9 Boss 0 1700000000 :no"); !has(got, "ADDLINE G *@203.0.113.9") {
		t.Errorf("hard ban not sent: %q", got)
	}
}
