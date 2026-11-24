package services

import (
	"strings"
	"testing"

	"github.com/fhazal/beeircd/internal/state"
)

// [services] max_accounts_per_email (Anope's ns_maxemail).
func TestMaxAccountsPerEmail(t *testing.T) {
	svc, h := newServices(t)
	svc.cfg.Services.MaxAccountsPerEmail = 1
	ns := h.net.UserByNick("NickServ")
	bee := helpUser(t, h, "1LFAAAAAA", "Bee")
	wasp := helpUser(t, h, "1LFAAAAAB", "Wasp")

	svc.Message(bee.UID, ns, "REGISTER honeypot42 bee@example.org")
	h.waitFor("Bee", "registered")
	svc.Message(wasp.UID, ns, "REGISTER honeypot42 BEE@Example.ORG")
	svc.Flush()
	if got := noticeTo(h, "Wasp"); !strings.Contains(got, "reached its limit of accounts here (1)") {
		t.Fatalf("second REGISTER on the same email: %q", got)
	}
	svc.Message(wasp.UID, ns, "REGISTER honeypot42 wasp@example.org")
	h.waitFor("Wasp", "registered")

	svc.Message(wasp.UID, ns, "SET EMAIL Bee@example.org")
	svc.Flush()
	if got := noticeTo(h, "Wasp"); !strings.Contains(got, "reached its limit") {
		t.Fatalf("SET EMAIL to a full address: %q", got)
	}
	// Setting your own address again isn't refused.
	svc.Message(bee.UID, ns, "SET EMAIL BEE@example.org")
	svc.Flush()
	if got := noticeTo(h, "Bee"); strings.Contains(got, "reached its limit") {
		t.Fatalf("SET EMAIL to own address: %q", got)
	}

	// An oper's SASET isn't held to the limit.
	h.opers = map[string]string{bee.UID: "s"}
	svc.Message(bee.UID, ns, "SASET EMAIL Wasp bee@example.org")
	svc.Flush()
	if got := noticeTo(h, "Bee"); strings.Contains(got, "reached its limit") {
		t.Fatalf("SASET EMAIL: %q", got)
	}
	if a, _ := svc.store.AccountByNick("Wasp"); a == nil || a.Email != "bee@example.org" {
		t.Fatalf("Wasp after SASET: %+v", a)
	}

	// 0 is no limit.
	svc.cfg.Services.MaxAccountsPerEmail = 0
	drone := helpUser(t, h, "1LFAAAAAC", "Drone")
	svc.Message(drone.UID, ns, "REGISTER honeypot42 bee@example.org")
	h.waitFor("Drone", "registered")
}

// [services] nick_fields and chan_fields (Anope's ns_set_misc and
// cs_set_misc): SET <field> [text] and INFO.
func TestExtraInfoFields(t *testing.T) {
	svc, h := newServices(t)
	ns, cs := h.net.UserByNick("NickServ"), h.net.UserByNick("ChanServ")
	svc.cfg.Services.NickFields = []string{"ICQ", "Twitter"}
	svc.cfg.Services.ChanFields = []string{"Discord"}
	bee := regChannel(t, svc, h, "1LFAAAAAA", "Bee")
	noticeTo(h, "Bee")

	svc.Message(bee.UID, ns, "SET icq 12345 \x02bold\x02")
	svc.Flush()
	if got := noticeTo(h, "Bee"); !strings.Contains(got, "ICQ for Bee set to 12345 bold.") {
		t.Fatalf("SET ICQ: %q", got)
	}
	svc.Message(bee.UID, ns, "SET AIM x")
	svc.Flush()
	if got := noticeTo(h, "Bee"); !strings.Contains(got, "Syntax: SET") {
		t.Fatalf("SET of a field not in the config: %q", got)
	}
	svc.Message(bee.UID, ns, "SET TWITTER "+strings.Repeat("x", 300))
	svc.Flush()
	noticeTo(h, "Bee")
	if v := svc.store.AccountFields(svc.AccountID(bee.UID))["twitter"]; len(v) != maxFieldLen {
		t.Errorf("long field kept %d characters", len(v))
	}
	svc.Message(bee.UID, ns, "INFO Bee")
	svc.Flush()
	if got := noticeTo(h, "Bee"); !strings.Contains(got, "  ICQ           12345 bold") || !strings.Contains(got, "  Twitter       xxx") {
		t.Fatalf("INFO: %q", got)
	}

	// A name taken out of the config hides the data but keeps it.
	svc.cfg.Services.NickFields = []string{"Twitter"}
	svc.Message(bee.UID, ns, "INFO Bee")
	svc.Flush()
	if got := noticeTo(h, "Bee"); strings.Contains(got, "ICQ") {
		t.Fatalf("INFO shows a field not in the config: %q", got)
	}
	svc.cfg.Services.NickFields = []string{"ICQ", "Twitter"}

	svc.Message(bee.UID, ns, "SET ICQ")
	svc.Flush()
	if got := noticeTo(h, "Bee"); !strings.Contains(got, "ICQ for Bee unset.") {
		t.Fatalf("SET ICQ without text: %q", got)
	}

	// Opers: SASET <field> <nick> [text], also to clear.
	wasp := helpUser(t, h, "1LFAAAAAB", "Wasp")
	h.opers = map[string]string{wasp.UID: "s"}
	svc.Message(wasp.UID, ns, "SASET ICQ Bee 777")
	svc.Flush()
	if got := noticeTo(h, "Wasp"); !strings.Contains(got, "ICQ for Bee set to 777.") {
		t.Fatalf("SASET ICQ: %q", got)
	}
	svc.Message(wasp.UID, ns, "SASET twitter Bee")
	svc.Flush()
	if got := noticeTo(h, "Wasp"); !strings.Contains(got, "Twitter for Bee unset.") {
		t.Fatalf("SASET TWITTER to clear: %q", got)
	}

	// ChanServ.
	svc.Message(bee.UID, cs, "SET #hive discord https://discord.example/hive")
	svc.Flush()
	if got := noticeTo(h, "Bee"); !strings.Contains(got, "Discord of #hive is now: https://discord.example/hive") {
		t.Fatalf("CS SET DISCORD: %q", got)
	}
	svc.Message(bee.UID, cs, "SET #hive SLACK x")
	svc.Flush()
	if got := noticeTo(h, "Bee"); !strings.Contains(got, "Unknown option SLACK") {
		t.Fatalf("CS SET of a field not in the config: %q", got)
	}
	svc.Message(bee.UID, cs, "INFO #hive")
	svc.Flush()
	if got := noticeTo(h, "Bee"); !strings.Contains(got, "  Discord       https://discord.example/hive") {
		t.Fatalf("CS INFO: %q", got)
	}
	svc.Message(wasp.UID, cs, "SET #hive DISCORD x")
	svc.Flush()
	if got := noticeTo(h, "Wasp"); strings.Contains(got, "is now") {
		t.Fatalf("CS SET by someone without access: %q", got)
	}
	svc.Message(bee.UID, cs, "SET DISCORD #hive")
	svc.Flush()
	if got := noticeTo(h, "Bee"); !strings.Contains(got, "Discord of #hive unset.") {
		t.Fatalf("CS SET DISCORD without text: %q", got)
	}
}

// [services] help_channel (Anope's m_helpchan): op there gives +h.
func TestHelpChannel(t *testing.T) {
	svc, h := newServices(t)
	svc.cfg.Services.HelpChannel = "#Help"
	bee := helpUser(t, h, "1LFAAAAAA", "Bee")   // on another server
	wasp := helpUser(t, h, "1LFAAAAAB", "Wasp") // an oper
	h.opers = map[string]string{wasp.UID: "k"}
	emit := func(ev state.Event) {
		t.Helper()
		if err := h.Emit(ev); err != nil {
			t.Fatal(err)
		}
		svc.Flush()
	}
	op := func(add bool, u *state.User) {
		emit(&state.ChannelMode{Source: wasp.UID, Channel: "#help", Changes: []state.ModeChange{{Add: add, Mode: 'o', UID: u.UID}}})
	}
	emit(&state.Join{UID: wasp.UID, Channel: "#help", TS: 1, Ranks: state.RankOp, Created: true})
	emit(&state.Join{UID: bee.UID, Channel: "#help", TS: 1})
	if bee.HasMode('h') {
		t.Fatal("+h without op")
	}
	op(true, bee)
	if !bee.HasMode('h') {
		t.Fatal("op in the help channel didn't give +h")
	}
	op(false, bee)
	if bee.HasMode('h') {
		t.Fatal("deop didn't take +h away")
	}
	op(true, bee)
	emit(&state.Part{UID: bee.UID, Channel: "#help"})
	if bee.HasMode('h') {
		t.Fatal("part didn't take +h away")
	}
	emit(&state.Join{UID: bee.UID, Channel: "#help", TS: 1, Ranks: state.RankOwner})
	if !bee.HasMode('h') {
		t.Fatal("joining as owner didn't give +h")
	}
	emit(&state.Kick{Source: wasp.UID, UID: bee.UID, Channel: "#help"})
	if bee.HasMode('h') {
		t.Fatal("kick didn't take +h away")
	}

	// An oper keeps +h after the deop; so does someone who had it already.
	if !wasp.HasMode('h') {
		t.Fatal("the oper who opened the channel has no +h")
	}
	op(false, wasp)
	if !wasp.HasMode('h') {
		t.Fatal("deop took an oper's +h")
	}
	emit(&state.UserMode{UID: bee.UID, Changes: umodes(true, "h")})
	emit(&state.Join{UID: bee.UID, Channel: "#help", TS: 1})
	op(true, bee)
	op(false, bee)
	if !bee.HasMode('h') {
		t.Fatal("deop took a +h given before the op")
	}

	// Other channels don't count.
	drone := helpUser(t, h, "1LFAAAAAC", "Drone")
	emit(&state.Join{UID: drone.UID, Channel: "#hive", TS: 1, Ranks: state.RankOp, Created: true})
	if drone.HasMode('h') {
		t.Fatal("op in another channel gave +h")
	}
}
