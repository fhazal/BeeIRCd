package link

import (
	"encoding/base64"
	"regexp"
	"strings"
	"testing"
)

// A realname ban (SNLINE) goes to UnrealIRCd as G ~realname:mask (a space
// written _), to InspIRCd as an R-line (TestInspRline), and both come back in as our own
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

	// InspIRCd: see TestInspRline.
}

// An InspIRCd R-line is a regex over "nick!user@host realname"; a realname
// ban of ours goes out as one that only looks at the realname.
func TestInspRline(t *testing.T) {
	const bot = `^[^[:space:]]+[[:space:]].*[bB][oO][tT].*$`
	if got := rlineFromGlob("*bot*"); got != bot {
		t.Errorf("*bot* became %q", got)
	}
	if got := rlineFromGlob("a?c d.e"); got != `^[^[:space:]]+[[:space:]][aA].[cC][[:space:]][dD]\.[eE]$` {
		t.Errorf("escaping: %q", got)
	}

	b := testBridge(t)
	b.Out(":0BE EOS") // the first line out carries the burst
	got := b.Out(":0BE TKL + G ~realname: *bot* Boss 1700003600 1700000000 :spam")
	if !has(got, "ADDLINE R "+bot+" Boss 1700000000 3600 :spam") {
		t.Errorf("to InspIRCd: %q", got)
	}
	for _, l := range got {
		if strings.Contains(l, "ADDLINE R *bot*") || strings.Contains(l, "ADDLINE R .*") {
			t.Errorf("a glob went out as a regex: %q", l)
		}
	}
	if got := b.Out(":0BE TKL - G ~realname: *bot* Boss"); !has(got, "DELLINE R "+bot) {
		t.Errorf("removal to InspIRCd: %q", got)
	}

	// The same line back in is our glob again (a space shows as such in the base64).
	run, _ := b.In(":1INAAAAAA ADDLINE R " + bot + " oper 1700000000 3600 :spam")
	if !has(run, "TKL + G ~rline: "+rlineHost("*bot*")+" oper 1700003600 1700000000 :spam") {
		t.Errorf("from InspIRCd: %q", run)
	}
	run, _ = b.In(":1INAAAAAA DELLINE R " + bot)
	if !has(run, "TKL - G ~rline: "+rlineHost("*bot*")) {
		t.Errorf("removal from InspIRCd: %q", run)
	}

	// Round trip, with a space, upper case and metacharacters.
	for _, g := range []string{"*bot*", "Free Bitcoin*", "a?c d.e", "x+(y)[z]{1}|^$", `back\slash`, "under_score*", "ünï*"} {
		back, ok := globFromRline(rlineFromGlob(g))
		if !ok || back != g {
			t.Errorf("round trip of %q: %q %v", g, back, ok)
		}
	}

	// Our regex /bans/ have no InspIRCd form and are not sent.
	rx := &Ban{Source: "0BE", Add: true, Type: BanRealname, User: "*", Host: rlineHost("/bot+/"), SetBy: "Boss", Expires: 1700003600, SetAt: 1700000000, Reason: "x"}
	if got := NewUnrealCodec().Encode(rx); len(got) != 0 {
		t.Errorf("regex ban sent: %q", got)
	}

	// Other R-lines: the realname part after our prefix can be a regex of ours.
	mask, why := rlineIn(`^[^[:space:]]+[[:space:]](pay|buy)[[:space:]]now$`)
	if why != "" || mask != `/^(?:(pay|buy)[[:space:]]now$)/` {
		t.Errorf("prefixed regex: %q %q", mask, why)
	}
	re2 := regexp.MustCompile("(?i)" + mask[1:len(mask)-1])
	if !re2.MatchString("Pay Now") || re2.MatchString("pay nowhere") || re2.MatchString("x pay now") {
		t.Error("the copied regex matches the wrong realnames")
	}
	// ... and the ones over the whole text, or not readable, are not copied, with a reason.
	for _, rx := range []string{`bot`, `^bot`, `.*`, `^[^[:space:]]+[[:space:]](?=x)`, `^[^[:space:]]+[[:space:]]((`, `^[^[:space:]]+[[:space:]].*$`} {
		if m, why := rlineIn(rx); why == "" || m != "" {
			t.Errorf("R-line %q copied as %q", rx, m)
		}
	}
	run, _ = b.In(":1INAAAAAA ADDLINE R bot oper 1700000000 0 :spam")
	if len(run) != 1 || !strings.Contains(run[0], "SENDSNO G :InspIRCd R-line bot not copied: it is a regular expression over the whole") {
		t.Errorf("not-copied notice: %q", run)
	}
	if run, _ = b.In(":1INAAAAAA DELLINE R bot"); len(run) != 0 {
		t.Errorf("removal of a line never copied: %q", run)
	}
	// A line we cannot read as a glob still arrives as a regex ban.
	run, _ = b.In(":1INAAAAAA ADDLINE R ^[^[:space:]]+[[:space:]](pay|buy)[[:space:]]now$ oper 1700000000 0 :spam")
	if !has(run, "TKL + G ~rline: "+rlineHost(`/^(?:(pay|buy)[[:space:]]now$)/`)) {
		t.Errorf("regex R-line: %q", run)
	}
	// And the Unreal codec turns the bridge's TKL into our ban.
	u := NewUnrealCodec().(*unrealCodec)
	in, _ := u.decodeBan("001", []string{"+", "G", "~rline:", rlineHost("*bot*"), "oper", "0", "1700000000", "spam"}).(*Ban)
	if in == nil || in.Type != BanRealname || in.User != "*" || in.Host != rlineHost("*bot*") {
		t.Errorf("decoded: %+v", in)
	}
}
