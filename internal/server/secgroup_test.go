package server

import (
	"strings"
	"testing"
	"time"

	"github.com/fhazal/beeircd/internal/config"
	"github.com/fhazal/beeircd/internal/state"
)

func repOf(s *Server, ip string) int {
	n := 0
	s.onLoop(func() { n = s.repScore(ip) })
	return n
}

func setRep(s *Server, ip string, score int) {
	s.onLoop(func() { s.repSet(ip, score, time.Now()) })
}

// Scores grow while people chat in a channel of three or more (two
// points when one of them is logged in, once per IP), are saved, and old
// low ones go.
func TestReputationBump(t *testing.T) {
	s := testServer(t, nil)
	a := registered(t, s, "alice")
	b := login(t, s, "bob")
	c := login(t, s, "carol")
	a.send("JOIN #x")
	a.expect(" 366 ")
	b.send("JOIN #x")
	b.expect(" 366 ")
	s.onLoop(func() { s.repBump(time.Now()) })
	if n := repOf(s, "127.0.0.1"); n != 0 {
		t.Fatalf("two in the channel: score %d, want 0", n)
	}
	c.send("JOIN #x")
	c.expect(" 366 ")
	s.onLoop(func() { s.repBump(time.Now()) })
	if n := repOf(s, "127.0.0.1"); n != 2 {
		t.Fatalf("after one round: score %d, want 2 (one IP, someone logged in)", n)
	}
	s.onLoop(func() { s.repBump(time.Now()) })
	if n := repOf(s, "127.0.0.1"); n != 4 {
		t.Fatalf("after two rounds: score %d, want 4", n)
	}
	s.onLoop(func() {
		s.saveReputation(time.Now())
		all, err := s.store.Reputations()
		if err != nil || len(all) != 1 || all[0].Score != 4 {
			t.Errorf("saved: %v %v", all, err)
		}
		s.rep.scores["192.0.2.1"] = &repEntry{score: 2, seen: time.Now().Add(-2 * time.Hour).Unix()}
		s.rep.scores["192.0.2.2"] = &repEntry{score: 40, seen: time.Now().Add(-2 * time.Hour).Unix()}
		s.repExpire(time.Now())
		if s.rep.scores["192.0.2.1"] != nil || s.rep.scores["192.0.2.2"] == nil {
			t.Error("expiry: a low score unseen for 2 hours goes, a high one stays")
		}
	})
}

// The oper REPUTATION command and the WHOIS lines opers see.
func TestReputationCommandAndWhois(t *testing.T) {
	s, _, _ := fileServerWith(t, "", nil)
	b := boss(t, s)
	x := login(t, s, "x")
	x.send("REPUTATION")
	x.expect(" 481 x :Permission denied")
	b.send("REPUTATION x")
	b.expect("No reputation record found for IP 127.0.0.1")
	b.send("REPUTATION 999.1.1.1")
	b.expect("REPUTATION: 999.1.1.1 is not an IP address")
	setRep(s, "127.0.0.1", 3)
	b.send("REPUTATION x")
	b.expect("    Score: 3")
	b.send("REPUTATION 127.0.0.1 30")
	b.expect("Reputation of IP 127.0.0.1 set to 30")
	b.send("REPUTATION <31")
	b.expect("x!~x@127.0.0.1 [127.0.0.1] score: 30")
	b.send("REPUTATION")
	b.expect("Current number of records (IP's): 1")
	b.send("WHOIS x")
	b.expect(" 320 Boss x :is in security-groups: known-users")
	b.expect(" 320 Boss x :is using an IP with a reputation score of 30")
	x.send("WHOIS x")
	x.quiet("reputation score")
}

// Who is in which group: each item, exclude, groups in groups,
// unknown-users.
func TestSecurityGroupMatch(t *testing.T) {
	s := testServer(t, func(c *config.Config) {
		c.SecurityGroups = []config.SecurityGroup{
			{Name: "dutch", GroupMatch: config.GroupMatch{Mask: []string{"~C:NL"}}, Exclude: config.GroupMatch{Identified: true}},
			{Name: "lan", GroupMatch: config.GroupMatch{IP: []string{"10.0.0.0/8"}}},
			{Name: "old", GroupMatch: config.GroupMatch{ConnectTime: config.Duration{Duration: time.Hour}}},
			{Name: "trusted", GroupMatch: config.GroupMatch{SecurityGroup: []string{"lan", "old"}}},
			{Name: "tls-users", GroupMatch: config.GroupMatch{TLS: true, ReputationScore: 100}},
		}
	})
	now := time.Now()
	nl := &state.User{UID: "0BEAAAAA1", Nick: "a", IP: "10.1.2.3", Country: "NL", SignOn: now, Modes: map[byte]bool{}}
	acct := &state.User{UID: "0BEAAAAA2", Nick: "b", IP: "192.0.2.1", Country: "NL", Account: "b", SignOn: now.Add(-2 * time.Hour), Modes: map[byte]bool{}}
	rep := &state.User{UID: "0BEAAAAA3", Nick: "c", IP: "192.0.2.9", SignOn: now, Modes: map[byte]bool{}}
	s.onLoop(func() {
		s.rep.scores["192.0.2.9"] = &repEntry{score: 120}
		for _, tc := range []struct {
			u    *state.User
			want string
		}{
			{nl, "unknown-users,dutch,lan,trusted"},
			{acct, "known-users,old,trusted"},
			{rep, "known-users,tls-users"},
		} {
			if got := strings.Join(s.groupsOf(tc.u, now), ","); got != tc.want {
				t.Errorf("%s: groups %s, want %s", tc.u.Nick, got, tc.want)
			}
		}
		nl.Groups = s.groupsOf(nl, now)
		if !state.MatchUser("~security-group:lan", nl) || !state.MatchUser("~G:dutch", nl) || state.MatchUser("~G:old", nl) {
			t.Error("~G: extban")
		}
	})
}

// ~G: bans: new people are kept out of a channel until their score makes
// them known.
func TestSecurityGroupBan(t *testing.T) {
	s := testServer(t, nil)
	op := login(t, s, "op")
	op.send("JOIN #quiet")
	op.expect(" 366 ")
	op.send("MODE #quiet +b ~security-group:unknown-users")
	op.expect("MODE #quiet +b ~G:unknown-users")
	op.send("MODE #quiet +b ~q:~G:nosuchgroup")
	op.expect(" 696 op #quiet b ~q:~G:nosuchgroup :There is no security group nosuchgroup on this server")
	x := login(t, s, "x")
	x.send("JOIN #quiet")
	x.expect(" 474 x #quiet ")
	setRep(s, "127.0.0.1", 25)
	x.send("JOIN #quiet")
	x.expect(" 366 x #quiet ")
}

// [[restrict]]: commands, messages and new channels new people may not
// use yet; services can always be reached.
func TestRestrictCommands(t *testing.T) {
	s := testServer(t, func(c *config.Config) {
		c.Restricts = []config.Restrict{
			{Command: "list", ConnectTime: config.Duration{Duration: time.Hour}},
			{Command: "private-message", ConnectTime: config.Duration{Duration: time.Hour}, Except: []string{"known-users"}},
			{Command: "channel-create"},
		}
	})
	a := login(t, s, "a")
	b := login(t, s, "b")
	a.send("LIST")
	a.expect(" 421 a LIST :You must be connected for at least 3600 seconds before you can use this command")
	a.send("PRIVMSG b :hi")
	a.expect(" 531 a b :You cannot send messages to users until you've been connected for 3600 seconds or more")
	a.send("PRIVMSG NickServ :help")
	a.expect("NickServ")
	a.send("JOIN #new")
	a.expect(" 972 a JOIN :Creation of new channels has been restricted by the network administrator.")
	setRep(s, "127.0.0.1", 25)
	a.send("PRIVMSG b :hi now")
	b.expect("PRIVMSG b :hi now")
}

// [connthrottle]: past the rate, people not in known-users are refused;
// known ones still get in; opers can turn it off.
func TestConnThrottle(t *testing.T) {
	s, _, _ := fileServerWith(t, "\n[connthrottle]\nlocal = \"3:60\"\nstart_delay = \"1ns\"\ngathering = \"1ns\"\n", nil)
	b := boss(t, s)
	login(t, s, "a")
	login(t, s, "b")
	c := raw(t, s)
	c.send("NICK c")
	c.send("USER c 0 * :c")
	c.expect("Throttled: Too many users trying to connect, please wait a while and try again")
	b.expect("[ConnThrottle] Connection throttling has been ACTIVATED")
	b.send("THROTTLE")
	b.expect("This server: 3 of 3 in 60 seconds")
	setRep(s, "127.0.0.1", 25)
	login(t, s, "known")
	setRep(s, "127.0.0.1", 0)
	b.send("THROTTLE OFF")
	b.expect("turned the new-user throttle off")
	login(t, s, "d")
}

// Scores travel over links: a server with a higher score answers, and
// the other one takes it.
func TestReputationOverLinks(t *testing.T) {
	hub, leaf := linkedPair(t)
	setRep(hub.s, "127.0.0.1", 40)
	login(t, leaf.s, "newbie")
	waitLink(t, "leaf takes the hub's score", func() bool { return repOf(leaf.s, "127.0.0.1") == 40 })
	// an oper's change goes everywhere, even down
	leaf.s.onLoop(func() {
		leaf.s.repSet("127.0.0.1", 5, time.Now())
		leaf.s.linkBroadcast(nil, ":"+leaf.s.cfg.Server.SID+" REPUTATION 127.0.0.1 *5*")
	})
	waitLink(t, "hub takes the forced score", func() bool { return repOf(hub.s, "127.0.0.1") == 5 })
}
