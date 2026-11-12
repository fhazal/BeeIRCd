package server

import (
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/fhazal/beeircd/internal/state"
)

// Stage 2g rogue-server tests: a linked server (here the leaf, seen from
// the hub) sends lines it has no right to send. Each case runs on a fresh
// hub + leaf: alice and carol are on the hub (alice op in #x, both in
// it), bob on the leaf. The leaf's link then speaks for alice (a user not
// behind it) or uses services-only commands on her. Nothing may change on
// the hub, and carol must not see anything from it.
func TestRogueServerSpoofs(t *testing.T) {
	cases := []struct {
		name, line string // A = alice's UID, B = bob's, L = the leaf's SID
	}{
		{"message as a user not behind the link", ":A PRIVMSG #x :spoofmsg"},
		{"notice as a user not behind the link", ":A NOTICE carol :spoofmsg"},
		{"nick change for a user not behind the link", ":A NICK spoofnick 1700000000"},
		{"quit for a user not behind the link", ":A QUIT :spoofmsg"},
		{"part for a user not behind the link", ":A PART #x :spoofmsg"},
		{"topic as a user not behind the link", ":A TOPIC #x A 1700000000 :spoofmsg"},
		{"away for a user not behind the link", ":A AWAY :spoofmsg"},
		{"kick as a user not behind the link", ":A KICK #x carol :spoofmsg"},
		{"channel mode as a user not behind the link", ":A MODE #x +o B"},
		{"user mode for a user not behind the link", ":A UMODE2 +W"},
		{"SVSLOGIN from a server that isn't the services server", ":L SVSLOGIN * A Boss"},
		{"SVSKILL from a server that isn't the services server", ":L SVSKILL A :spoofmsg"},
		{"SVSJOIN from a server that isn't the services server", ":L SVSJOIN A #spoofchan"},
		{"SVSPART from a server that isn't the services server", ":L SVSPART A #x"},
		{"SVSNICK from a server that isn't the services server", ":L SVSNICK A spoofnick 1700000000"},
		{"SVSMODE from a server that isn't the services server", ":L SVSMODE A +W"},
		{"CHGHOST as a user not behind the link", ":A CHGHOST carol spoof.host"},
		{"CHGHOST by the server itself (not services)", ":L CHGHOST A spoof.host"},
		{"SETHOST for a user not behind the link", ":A SETHOST spoof.host"},
		{"CHGNAME by the server itself (not services)", ":L CHGNAME A :spoofmsg"},
		{"INVITE as a user not behind the link", ":A INVITE B #x"},
		{"oper flags for a user not behind the link", ":L MD client A bee-operflags :acghks"},
		{"KILL as a user not behind the link", ":A KILL carol :spoofmsg"},
		{"CHGHOST by a user who isn't an oper", ":B CHGHOST A spoof.host"},
		{"SAJOIN by a user who isn't an oper", ":B SAJOIN A #spoofchan"},
		{"SVSNICK from a non-services server to a nick", ":L SVSNICK A spoofnick 0"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			hub, leaf := linkedPair(t)
			alice := login(t, hub.s, "alice")
			carol := login(t, hub.s, "carol")
			bob := login(t, leaf.s, "bob")
			alice.send("JOIN #x")
			alice.expect("366")
			carol.send("JOIN #x")
			carol.expect("366")
			bob.send("JOIN #x")
			bob.expect("366")
			hub.seen(t, "bob")
			leaf.seen(t, "carol")
			waitLink(t, "bob in #x on the hub", func() bool {
				var in bool
				hub.s.onLoop(func() {
					ch := hub.s.net.Channel("#x")
					in = ch != nil && ch.Members[hub.s.net.UserByNick("bob").UID] != nil
				})
				return in
			})
			before := hubView(hub)
			var a, b string
			hub.s.onLoop(func() { a, b = hub.s.net.UserByNick("alice").UID, hub.s.net.UserByNick("bob").UID })
			line := strings.NewReplacer(":A ", ":"+a+" ", " A ", " "+a+" ", ":B ", ":"+b+" ", " B", " "+b, ":L ", ":"+leaf.sid+" ").Replace(tc.line)
			carol.quiet("spoof") // drain what came so far
			hub.fromLink(t, line)
			time.Sleep(100 * time.Millisecond)
			if after := hubView(hub); after != before {
				t.Errorf("%s changed the hub:\nbefore %s\nafter  %s", line, before, after)
			}
			carol.quiet("spoof")
		})
	}
}

// hubView is what the rogue tests compare: alice's and carol's nick,
// account, host, ident, realname, modes, oper flags; #x's members, ranks
// and topic; the channels alice is in.
func hubView(n *linkNode) string {
	var b strings.Builder
	n.s.onLoop(func() {
		for _, nick := range []string{"alice", "carol"} {
			u := n.s.net.UserByNick(nick)
			if u == nil {
				b.WriteString(nick + " gone; ")
				continue
			}
			modes := ""
			for _, m := range "BDHRTWZdiopqrswxz" {
				if u.Modes[byte(m)] {
					modes += string(m)
				}
			}
			flags := ""
			if c := n.s.clients[u.UID]; c != nil {
				flags = c.operFlags
			}
			b.WriteString(u.Nick + " acc=" + u.Account + " host=" + u.Host + " ident=" + u.Ident + " real=" + u.Realname +
				" modes=" + modes + " flags=" + flags + " operflags=" + u.OperFlags + " chans=" + strings.Join(chanNames(u.Channels), ",") + "; ")
		}
		if ch := n.s.net.Channel("#x"); ch != nil {
			b.WriteString("#x topic=" + ch.Topic + " members=")
			for _, m := range ch.SortedMembers() {
				b.WriteString(m.User.Nick + "/" + strconv.Itoa(int(m.Ranks)) + " ")
			}
		}
	})
	return b.String()
}

func chanNames(m map[string]*state.Channel) []string {
	var out []string
	for _, ch := range m {
		out = append(out, ch.Name)
	}
	sort.Strings(out)
	return out
}
