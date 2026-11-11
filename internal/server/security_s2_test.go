package server

import (
	"strings"
	"testing"
)

// S2 regression tests (audit 11 Nov 2026): services acting for someone
// never give more than that person may do themselves.

func TestOperModesThroughServices(t *testing.T) {
	s, _, _ := fileServer(t, "")
	b := boss(t, s)
	bee := founded(t, s, "Bee")

	// Negative: a founder who isn't an oper gets neither +P nor +O, set or
	// unset, through ChanServ.
	for _, m := range []string{"+P", "+O", "+PO"} {
		bee.send("CS MODE #bchat SET " + m)
		bee.expect(" 481 Bee :Permission Denied - only IRC operators may set +" + m[1:2])
	}
	bee.send("MODE #bchat")
	if line := bee.expect(" 324 Bee #bchat "); strings.ContainsAny(strings.Fields(line)[4], "OP") {
		t.Fatalf("founder got an oper-only mode: %q", line)
	}
	// Nor through the mode lock.
	bee.send("CS MODE #bchat LOCK ADD +P")
	bee.expect(csLine("Bee", "A mode lock can hold CDKMNQRSTVcimnpstz, k, l and BEFJLf, not P."))

	// Positive: an oper may, directly or through OperServ.
	b.send("OS MODE #bchat +P")
	bee.expect("MODE #bchat +P")
	// Negative again: the founder can't take it off through ChanServ.
	bee.send("CS MODE #bchat SET -P")
	bee.expect(" 481 Bee :Permission Denied - only IRC operators may set +P")
	bee.send("MODE #bchat")
	bee.expect(" 324 Bee #bchat +Pnrt")

	// Positive: an oper who is a founder may through ChanServ too.
	joinAll("#boss", b)
	b.send("CS REGISTER #boss mine")
	b.expect("#boss is registered to Boss.")
	b.send("CS MODE #boss SET +P")
	b.expect("MODE #boss +P")
}

func TestAjoinFollowsJoinRules(t *testing.T) {
	s := testServer(t, nil)
	bee := founded(t, s, "Bee")
	host := login(t, s, "host")
	joinAll("#tls", host)
	host.send("MODE #tls +z")
	host.expect("MODE #tls +z")

	// Banned from its own channel: Bee has INVITE there, so AJOIN still
	// gets it in (an invite beats a ban, as for JOIN).
	bee.send("MODE #bchat +b Bee!*@*")
	bee.expect("MODE #bchat +b Bee!*@*")
	bee.send("PART #bchat")
	bee.expect("PART #bchat")

	bee.send("NS AJOIN ADD #tls,#open,#bchat")
	bee.expect("added to Bee's auto join list.")
	bee.send("NS LOGOUT")
	bee.expect(" 901 ")
	bee.send("NS IDENTIFY " + pw)
	bee.expect("JOIN #open")
	bee.expect("JOIN #bchat")
	// +z: a plain connection never gets in, not even through AJOIN.
	host.quiet("JOIN #tls")
	bee.send("NAMES #tls")
	bee.expect(" 366 Bee #tls ")
}

func TestModeSetNeedsStatusPrivileges(t *testing.T) {
	s := testServer(t, nil)
	bee := founded(t, s, "Bee")
	nova := registered(t, s, "nova")
	joinAll("#bchat", nova)
	bee.send("CS AOP #bchat ADD nova")
	bee.expect("nova added to the AOP list of #bchat.")
	bee.send("CS LEVELS #bchat SET MODE 5")
	bee.expect(csLine("Bee", "MODE on #bchat now needs level 5."))

	// Plain modes and opping yourself (OPME at AOP) are fine.
	nova.send("CS MODE #bchat SET +m")
	nova.expect("MODE #bchat +m")
	// Owner or admin for yourself (OWNERME, PROTECTME): no. Taking the
	// founder's op: PEACE says no (Anope's replies).
	nova.send("CS MODE #bchat SET +q-o nova Bee")
	nova.expect(csLine("nova", "You do not have access to set mode q."))
	nova.expect(csLine("nova", "You do not have the access to change Bee's modes."))
	nova.send("CS MODE #bchat SET +a nova")
	nova.expect(csLine("nova", "You do not have access to set mode a."))
	nova.send("CS MODE #bchat SET -o ChanServ")
	nova.expect(csLine("nova", "Access denied."))
	nova.send("CS MODE #bchat SET +v ghost")
	nova.expect(csLine("nova", "Nick ghost isn't currently in use."))
	// Positive: VOICE at AOP lets it voice someone below.
	plain := login(t, s, "plain")
	joinAll("#bchat", plain)
	nova.send("CS MODE #bchat SET +v plain")
	plain.expect("MODE #bchat +v plain")
	bee.send("NAMES #bchat")
	if line := bee.expect(" 353 "); !strings.Contains(line, "@Bee") {
		t.Fatalf("the founder lost op: %q", line)
	}
	nova.quiet("MODE #bchat +q")
}
