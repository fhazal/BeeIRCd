package link

import (
	"encoding/base64"
	"testing"
)

// A realname ban (SNLINE) goes to UnrealIRCd as G ~realname:mask (a space
// written _), to InspIRCd as an R-line, and both come back in as our own
// realname ban. A /regex/ has no form on either, so it stays here.
func TestRealnameBansOnLinks(t *testing.T) {
	enc := func(m string) string { return base64.RawURLEncoding.EncodeToString([]byte(m)) }
	ban := &Ban{Source: "0BE", Add: true, Type: BanRealname, User: "*", Host: enc("free bitcoin*"), SetBy: "Boss", SetAt: 1700000000, Expires: 1700003600, Reason: "spam"}
	unreal := &unrealCodec{unreal: true}
	if got := unreal.Encode(ban); len(got) != 1 || got[0] != ":0BE TKL + G ~realname: free_bitcoin* Boss 1700003600 1700000000 :spam" {
		t.Errorf("to UnrealIRCd: %q", got)
	}
	del := *ban
	del.Add = false
	if got := unreal.Encode(&del); len(got) != 1 || got[0] != ":0BE TKL - G ~realname: free_bitcoin* Boss 1700003600 1700000000 :spam" {
		t.Errorf("removal to UnrealIRCd: %q", got)
	}
	re := *ban
	re.Host = enc("/^bot.*$/")
	if got := unreal.Encode(&re); len(got) != 0 {
		t.Errorf("regex sent to UnrealIRCd: %q", got)
	}
	// BeeIRCd keeps getting its own form.
	bee := &unrealCodec{bee: true}
	if got := bee.Encode(ban); len(got) != 1 || got[0] != ":0BE TKL + n * "+ban.Host+" Boss 1700003600 1700000000 :spam" {
		t.Errorf("to BeeIRCd: %q", got)
	}

	in, _ := unreal.decodeBan("001", []string{"+", "G", "~realname:", "free_bitcoin*", "admin", "0", "1700000000", "spam"}).(*Ban)
	if in == nil || in.Type != BanRealname || in.User != "*" || in.Host != enc("free bitcoin*") || in.Unsupported {
		t.Errorf("from UnrealIRCd: %+v", in)
	}
	in, _ = unreal.decodeBan("001", []string{"-", "G", "~realname:", "free_bitcoin*", "admin"}).(*Ban)
	if in == nil || in.Add || in.Type != BanRealname || in.Host != enc("free bitcoin*") {
		t.Errorf("removal from UnrealIRCd: %+v", in)
	}

	b := testBridge(t)
	b.Out(":0BE EOS") // the first line out carries the burst
	if got := b.Out(":0BE TKL + G ~realname: free_bitcoin* Boss 1700003600 1700000000 :spam"); !has(got, "ADDLINE R free_bitcoin* Boss 1700000000 3600 :spam") {
		t.Errorf("to InspIRCd: %q", got)
	}
	if got := b.Out(":0BE TKL - G ~realname: free_bitcoin* Boss"); !has(got, "DELLINE R free_bitcoin*") {
		t.Errorf("removal to InspIRCd: %q", got)
	}
	run, _ := b.In(":1INAAAAAA ADDLINE R free_bitcoin* oper 1700000000 3600 :spam")
	if !has(run, "TKL + G ~realname: free_bitcoin* oper 1700003600 1700000000 :spam") {
		t.Errorf("from InspIRCd: %q", run)
	}
	run, _ = b.In(":1INAAAAAA DELLINE R free_bitcoin*")
	if !has(run, "TKL - G ~realname: free_bitcoin*") {
		t.Errorf("removal from InspIRCd: %q", run)
	}
}
