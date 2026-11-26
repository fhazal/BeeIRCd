package server

import (
	"context"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
)

// OperServ extras, ChanServ SEEN, news kinds and DNSBL.

func TestOperServSvsAndLists(t *testing.T) {
	s, _, _ := fileServer(t, "")
	b := boss(t, s)
	u := login(t, s, "user")
	joinAll("#room", u)

	b.send("OS SVSNICK user renamed")
	u.expect(":user!~user@127.0.0.1 NICK renamed")
	b.expect(osLine("Boss", "The nick user is now being changed to renamed."))
	b.send("OS SVSNICK renamed Boss")
	b.expect(osLine("Boss", "Boss is in use."))
	b.send("OS SVSNICK NickServ x")
	b.expect(osLine("Boss", "NickServ is a service."))

	b.send("OS SVSJOIN renamed #pulled")
	u.expect(":renamed!~user@127.0.0.1 JOIN #pulled")
	u.expect(" 366 renamed #pulled ")
	b.expect(osLine("Boss", "renamed has been joined to #pulled."))
	b.send("OS SVSJOIN renamed #pulled")
	b.expect(osLine("Boss", "renamed is already in #pulled."))
	b.send("OS SVSPART renamed #pulled time to go")
	u.expect(":renamed!~user@127.0.0.1 PART #pulled :time to go")
	b.expect(osLine("Boss", "renamed has been parted from #pulled."))

	b.send("OS UMODE renamed -i+w")
	u.expect(":OperServ!services@irc.test.local MODE renamed -i+w")
	u.expect(osLine("renamed", "Boss changed your usermodes to -i+w."))
	b.expect(osLine("Boss", "Changed usermodes of renamed to -i+w."))
	b.send("OS UMODE renamed +o")
	b.expect(osLine("Boss", "Changed usermodes of renamed to +o."))
	u.send("MODE renamed")
	u.expect(" 221 renamed +w") // +o is never given by MODE

	b.send("OS USERLIST *!~user@*")
	b.expect(osLine("Boss", "Users list:"))
	b.expect(osLine("Boss", "  renamed          ~user@127.0.0.1"))
	b.expect(osLine("Boss", "End of users list. 1 user shown."))
	b.send("OS USERLIST #room")
	b.expect(osLine("Boss", "#room users list:"))
	b.expect("renamed")
	b.send("OS USERLIST * INVISIBLE") // renamed is -i now
	b.expect(osLine("Boss", "  Boss "))
	b.expect(osLine("Boss", "End of users list. 1 user shown."))
	b.send("OS CHANLIST")
	b.expect(osLine("Boss", "Channel list:"))
	b.expect(osLine("Boss", "  #room "))
	b.send("OS CHANLIST renamed")
	b.expect(osLine("Boss", "renamed channel list:"))
	b.expect(osLine("Boss", "End of channel list. 1 channel shown."))
	b.send("OS CHANLIST * SECRET")
	b.expect(osLine("Boss", "End of channel list. 0 channels shown."))
}

func TestOperInfoAndIgnore(t *testing.T) {
	s, _, _ := fileServer(t, "")
	b := boss(t, s)
	u := registered(t, s, "user")
	b.send("OS INFO ADD user watch for clones")
	b.expect(osLine("Boss", "Added info to user."))
	b.send("OS INFO ADD user WATCH FOR CLONES")
	b.expect(osLine("Boss", "That oper info is on user already."))
	b.send("OS INFO ADD nobody x")
	b.expect(osLine("Boss", "nobody isn't a registered nick."))
	b.send("NS INFO user")
	b.expect(nsLine("Boss", "  Oper info     (by Boss on "))
	u.send("NS INFO user")
	u.expect(nsLine("user", "  Options "))
	u.quiet("Oper info")
	b.send("OS INFO DEL user nope")
	b.expect(osLine("Boss", `No such info "nope" on user.`))
	b.send("OS INFO DEL user watch for clones")
	b.expect(osLine("Boss", "Deleted info from user."))
	b.send("OS INFO CLEAR user")
	b.expect(osLine("Boss", "The oper info list for user is empty."))

	// IGNORE: services drop what a match says to them; opers never.
	b.send("OS IGNORE ADD 0 user spamming the bots")
	b.expect(osLine("Boss", "*!*@127.0.0.1 will now permanently be ignored."))
	u.send("NS INFO user")
	u.quiet("NickServ")
	b.send("NS INFO user")
	b.expect(nsLine("Boss", "user · registered "))
	b.send("OS IGNORE LIST")
	b.expect(osLine("Boss", "Services ignore list:"))
	b.expect("*!*@127.0.0.1")
	b.send("OS IGNORE DEL user")
	b.expect(osLine("Boss", "*!*@127.0.0.1 will no longer be ignored."))
	u.send("NS INFO user")
	u.expect(nsLine("user", "user · registered "))
	b.send("OS IGNORE ADD 2 bad!*@* short")
	b.expect(osLine("Boss", "bad!*@* will now be ignored for 2 seconds."))
	b.send("OS IGNORE ADD soon x")
	b.expect(osLine("Boss", "soon isn't a time."))
	b.send("OS IGNORE CLEAR")
	b.expect(osLine("Boss", "The ignore list has been cleared."))
	b.send("OS IGNORE LIST")
	b.expect(osLine("Boss", "The ignore list is empty."))
}

func TestNewsKinds(t *testing.T) {
	s, _, _ := fileServer(t, "")
	b := boss(t, s)
	for _, l := range []string{"one", "two", "three", "four"} {
		b.send("OS LOGONNEWS ADD logon " + l)
		b.expect("Added new logon news item.")
	}
	b.send("OS RANDOMNEWS ADD random A")
	b.expect("Added new random news item.")
	b.send("OS RANDOMNEWS ADD random B")
	b.expect("Added new random news item.")
	b.send("OS OPERNEWS ADD opers read this")
	b.expect("Added new oper news item.")

	// The newest 3 logon lines, then one random line, in turn.
	first := login(t, s, "first")
	first.expect("] logon two")
	first.expect("] logon three")
	first.expect("] logon four")
	first.expect("Random News")
	second := login(t, s, "second")
	second.expect("] logon four")
	l := second.expect("Random News")
	if !strings.HasSuffix(l, "random B") {
		t.Errorf("second connection should get the next random line: %q", l)
	}
	first.quiet("opers read this")

	first.send("NS REGISTER " + pw) // opers need an account
	first.expect(" 900 first ")
	b.send("AS OPER first ADD FLAGS g HOST *@127.0.0.1 PASSWORD First-oper-1")
	b.expect("first is an oper now")
	first.send("OPER first First-oper-1")
	first.expect(" 381 first ")
	first.expect(":OperServ!services@irc.test.local NOTICE first :[")
	b.send("OS OPERNEWS DEL ALL")
	b.expect(osLine("Boss", "All oper news items deleted (1)."))
	b.send("OS RANDOMNEWS LIST")
	b.expect(osLine("Boss", "Random news items:"))
}

func TestSeen(t *testing.T) {
	s, _, _ := fileServer(t, "")
	b := boss(t, s)
	u := login(t, s, "wanderer")
	joinAll("#open", u)
	b.send("CS SEEN wanderer")
	b.expect(csLine("Boss", "wanderer (~wanderer@127.0.0.1) was last seen joining #open "))
	u.send("NICK walker")
	u.expect("NICK walker")
	b.send("CS SEEN wanderer")
	if l := b.expect(csLine("Boss", "wanderer (~wanderer@127.0.0.1) was last seen changing nick to walker ")); !strings.HasSuffix(l, ". walker is still online.") {
		t.Errorf("nick change line: %q", l)
	}
	u.send("MODE #open +s")
	u.send("PART #open")
	u.expect("PART #open")
	b.send("CS SEEN walker")
	b.expect(csLine("Boss", "walker (~wanderer@127.0.0.1) was last seen parting #open"))
	u.send("QUIT :gone fishing")
	u.expect("ERROR")
	b.send("CS SEEN walker")
	b.expect(csLine("Boss", "walker (~wanderer@127.0.0.1) was last seen quitting (Quit: gone fishing) "))
	b.send("CS SEEN Boss")
	b.expect(csLine("Boss", "You might see yourself in the mirror, Boss."))
	b.send("CS SEEN ChanServ")
	b.expect(csLine("Boss", "ChanServ is a client on services."))
	b.send("CS SEEN never")
	b.expect(csLine("Boss", "Sorry, I have not seen never."))

	b.send("OS SEEN STATS")
	b.expect(osLine("Boss", "3 nicks are stored in the database"))
	b.send("OS SEEN CLEAR 1h")
	b.expect(osLine("Boss", "Database cleared, removed 3 nicks that were added after "))
	b.send("CS SEEN walker")
	b.expect(csLine("Boss", "Sorry, I have not seen walker."))
}

func TestChanKill(t *testing.T) {
	s, _, _ := fileServer(t, "")
	b := boss(t, s)
	d1, d2 := login(t, s, "drone1"), login(t, s, "drone2")
	joinAll("#spamhole", d1, d2, b)
	b.send("OS CHANKILL #spamhole")
	b.expect(osLine("Boss", "Syntax: CHANKILL [+expiry] #chan <reason>"))
	b.send("OS CHANKILL +1d #spamhole drone channel")
	d1.expect("ERROR :Closing Link: 127.0.0.1 (Banned: drone channel)")
	d2.expect("ERROR :Closing Link: 127.0.0.1 (Banned: drone channel)")
	b.expect(osLine("Boss", "AKILLed everyone in #spamhole for 1 day (drone channel): 2 people removed."))
	b.send("OS AKILL LIST")
	b.expect("*@127.0.0.1  by Boss")
}

// fakeDNS answers DNSBL queries from a table; slow names wait for the
// context.
type fakeDNS struct {
	mu      sync.Mutex
	answers map[string][]string
	slow    map[string]bool
	asked   []string
}

func (f *fakeDNS) lookup(ctx context.Context, name string) ([]string, error) {
	f.mu.Lock()
	f.asked = append(f.asked, name)
	a, slow := f.answers[name], f.slow[name]
	f.mu.Unlock()
	if slow {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	if a == nil {
		return nil, &net.DNSError{Err: "no such host", Name: name, IsNotFound: true}
	}
	return a, nil
}

func (f *fakeDNS) count(name string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, a := range f.asked {
		if a == name {
			n++
		}
	}
	return n
}

const dnsblConf = `
[[dnsbl]]
name = "bad.example"
replies = [3, 5]
reason = "Your IP %i is listed in %b (%r)"

[[dnsbl]]
name = "slow.example"
`

func TestDNSBL(t *testing.T) {
	dns := &fakeDNS{
		answers: map[string][]string{"1.0.0.127.bad.example": {"127.0.0.3"}},
		slow:    map[string]bool{"1.0.0.127.slow.example": true},
	}
	s, _, _ := fileServerWith(t, dnsblConf, func(s *Server) {
		s.lookupA = dns.lookup
		s.dnsblSkip = func(string) bool { return false }
	})
	c := raw(t, s)
	c.send("NICK listed")
	c.send("USER listed 0 * :Listed")
	c.expect(" 465 listed :You are banned from this network: Your IP 127.0.0.1 is listed in bad.example (reply 3)")
	c.expect("ERROR :Closing Link: 127.0.0.1 (Banned: Your IP 127.0.0.1 is listed in bad.example (reply 3))")

	// The answer is cached: no second lookup.
	c2 := raw(t, s)
	c2.send("NICK again")
	c2.send("USER again 0 * :Again")
	c2.expect(" 465 ")
	if n := dns.count("1.0.0.127.bad.example"); n != 1 {
		t.Errorf("bad.example asked %d times, want 1 (cached)", n)
	}

	// An answer not in replies doesn't count; a list that never answers
	// holds registration for at most 2 seconds.
	dns.mu.Lock()
	dns.answers["1.0.0.127.bad.example"] = []string{"127.0.0.4"}
	dns.mu.Unlock()
	s.post(func() { s.dnsbl.cache = make(map[string]dnsblResult) })
	start := time.Now()
	ok := raw(t, s)
	ok.send("NICK fine")
	ok.send("USER fine 0 * :Fine")
	ok.expect(" 001 fine ")
	if d := time.Since(start); d > 3*time.Second {
		t.Errorf("registration waited %v", d)
	}

	b := boss(t, s)
	b.send("AS DNSBL LIST")
	b.expect(asLine("Boss", "DNSBL lists:"))
	b.expect(asLine("Boss", "  bad.example              on · answers 3,5 · reject · 2 hits"))
	b.expect(asLine("Boss", "  slow.example             on · every answer · reject · 0 hits"))
	b.send("AS DNSBL DISABLE slow.example")
	b.expect(asLine("Boss", "slow.example is off until the server restarts."))
	b.send("AS DNSBL CHECK 127.0.0.1")
	b.expect(asLine("Boss", "Looking 127.0.0.1 up in 1 list..."))
	b.expect(asLine("Boss", "  bad.example              answers [4], which this list doesn't count (see its replies)"))
	b.send("AS DNSBL CHECK 192.0.2.1")
	b.expect(asLine("Boss", "  bad.example              not listed"))
	b.send("OS STATS ALL")
	b.expect(osLine("Boss", "  DNSBL hits bad.example 2, slow.example 0 (off)"))
	b.send("AS DNSBL CHECK nonsense")
	b.expect(asLine("Boss", "Syntax: DNSBL CHECK <ip>"))
}

func TestDNSBLAction(t *testing.T) {
	dns := &fakeDNS{answers: map[string][]string{"1.0.0.127.bad.example": {"127.0.0.2"}}}
	s, _, _ := fileServerWith(t, "\n[[dnsbl]]\nname = \"bad.example\"\naction = \"akill\"\nban_time = \"2h\"\n", func(s *Server) {
		s.lookupA = dns.lookup
		s.dnsblSkip = func(string) bool { return false }
	})
	c := raw(t, s)
	c.send("NICK bot")
	c.send("USER bot 0 * :Bot")
	c.expect("ERROR :Closing Link: 127.0.0.1 (Banned: Your IP 127.0.0.1 is listed in bad.example (reply 2))")
	// The cached answer refuses the next one too; the AKILL it set is on the
	// list.
	c2 := raw(t, s)
	c2.send("NICK bot2")
	c2.send("USER bot2 0 * :Bot")
	c2.expect(" 465 bot2 :You are banned from this network: Your IP 127.0.0.1 is listed in bad.example")
	var akills []string
	done := make(chan struct{})
	s.post(func() {
		for k, n := range s.svc.BanCounts() {
			if n > 0 {
				akills = append(akills, k)
			}
		}
		close(done)
	})
	<-done
	if len(akills) != 1 || akills[0] != "akill" {
		t.Errorf("ban counts %v, want one akill", akills)
	}
}

func TestDNSBLQueryNames(t *testing.T) {
	for _, tc := range []struct{ ip, want string }{
		{"203.0.113.7", "7.113.0.203.zone.example"},
		{"2001:db8::1", "1.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.8.b.d.0.1.0.0.2.zone.example"},
		{"nonsense", ""},
	} {
		if got := dnsblQuery(tc.ip, "zone.example"); got != tc.want {
			t.Errorf("dnsblQuery(%s) = %q, want %q", tc.ip, got, tc.want)
		}
	}
	for ip, skip := range map[string]bool{"192.168.50.3": true, "127.0.0.1": true, "10.1.2.3": true, "fe80::1": true, "203.0.113.7": false} {
		if dnsblSkipped(ip) != skip {
			t.Errorf("dnsblSkipped(%s) = %v", ip, !skip)
		}
	}
}

// These commands' log lines, as Anope 2.0.20 writes them.
func TestLogChannelOperServ(t *testing.T) {
	dns := &fakeDNS{answers: map[string][]string{"1.0.0.127.bad.example": {"127.0.0.3"}}}
	s, _, _ := fileServerWith(t, "", func(s *Server) { s.lookupA = dns.lookup })
	b := boss(t, s)
	b.send("AS SET log-channel #services")
	b.expect("log-channel is now #services")
	joinAll("#services", b)
	osl := ":OperServ!services@irc.test.local " + logSrc
	adm := osl + "ADMIN: Boss!~boss@127.0.0.1 (Boss) used "
	u := registered(t, s, "user")
	b.send("OS SVSNICK user renamed")
	b.expect(adm + "SVSNICK to change user to renamed")
	b.send("OS SVSJOIN renamed #x")
	b.expect(adm + "SVSJOIN to force renamed to join #x")
	b.send("OS SVSPART renamed #x bye")
	b.expect(adm + "SVSPART to force renamed to part #x with reason bye")
	b.send("OS UMODE renamed +w")
	b.expect(adm + "UMODE +w on renamed")
	b.send("OS CHANLIST #s*")
	b.expect(adm + "CHANLIST for #s*")
	b.send("OS USERLIST")
	b.expect(adm + "USERLIST")
	b.send("OS INFO ADD user note")
	b.expect(adm + "INFO to add information to user")
	b.send("OS IGNORE ADD 1h renamed")
	b.expect(adm + "IGNORE to add an ignore on *!*@127.0.0.1 for 1 hour")
	b.send("OS IGNORE CLEAR")
	b.expect(adm + "IGNORE to CLEAR the list")
	b.send("OS OPERNEWS ADD hi")
	b.expect(adm + "OPERNEWS to add a news item")
	u.nick = "renamed"
	joinAll("#drones", u)
	b.send("OS CHANKILL #drones bots")
	b.expect(adm + "CHANKILL on #drones (bots)")
	b.send("OS SEEN CLEAR 1h")
	b.expect(adm + "SEEN CLEAR and removed ")

	// A DNSBL hit, from OperServ (Anope's m_dnsbl).
	s.post(func() {
		s.cfg.DNSBL = append(s.cfg.DNSBL, testDNSBL("bad.example"))
		s.dnsblSkip = func(string) bool { return false }
	})
	time.Sleep(50 * time.Millisecond)
	c := raw(t, s)
	c.send("NICK listed")
	c.send("USER listed 0 * :Listed")
	b.expect(osl + "M_DNSBL: listed!~listed@127.0.0.1 (127.0.0.1) appears in bad.example")

	// A connect-throttle ZLINE, also from OperServ.
	s.onLoop(func() {
		s.cfg.Limits.ConnectRate = "1:60"
		s.throttleSkip = func(string) bool { return false }
		s.cfg.DNSBL = nil
		clear(s.throttle.hits)
	})
	addr := s.Addrs()[0].String()
	for i := 0; i < 4; i++ { // 1 allowed, 3 refused
		closedWith(t, addr)
	}
	b.expect(osl + "THROTTLE: Z-lined 127.0.0.1 for 10 minutes: refused 3 times in 10 minutes for reconnecting too fast")
}

// OperServ UMODE follows OLINE's rank rule: an oper with only flag s took
// all of the admin's oper flags away with UMODE Boss -o.
func TestOperServUmodeHigherOper(t *testing.T) {
	s, _, _ := fileServer(t, "")
	b := boss(t, s)
	small := login(t, s, "Small")
	small.sync()
	b.send("OS OLINE Small +s")
	b.expect(osLine("Boss", "Small now has oper flags s until they quit."))
	small.expect(" 381 Small ")

	small.send("OS UMODE Boss -o")
	small.expect(osLine("Small", "Boss has oper flag a, which you don't have."))
	var flags string
	s.onLoop(func() { flags = s.clients[s.net.UserByNick("Boss").UID].operFlags })
	if flags == "" {
		t.Errorf("an oper with flag s de-opered the admin Boss with OS UMODE Boss -o")
	}
	small.send("OS UMODE Small +w") // their own modes are theirs
	small.expect(osLine("Small", "Changed usermodes of Small to +w."))
	b.send("OS UMODE Small -o") // and someone above may
	b.expect(osLine("Boss", "Changed usermodes of Small to -o."))
}

// OperServ KILL follows the same rank rule: an oper with only flag k could
// disconnect the admin.
func TestOperServKillHigherOper(t *testing.T) {
	s, _, _ := fileServer(t, "")
	b := boss(t, s)
	small := login(t, s, "Small")
	small.sync()
	b.send("OS OLINE Small +k")
	b.expect(osLine("Boss", "Small now has oper flags k until they quit."))
	small.expect(" 381 Small ")

	small.send("OS KILL Boss bye")
	small.expect(osLine("Small", "Boss has oper flag a, which you don't have."))
	var online bool
	s.onLoop(func() { online = s.net.UserByNick("Boss") != nil })
	if !online {
		t.Fatal("an oper with flag k disconnected the admin Boss with OS KILL")
	}
	other := login(t, s, "Other")
	other.sync()
	small.send("OS KILL Other bye") // someone without oper flags: as before
	small.expect(osLine("Small", "Other is disconnected."))
	b.send("OS KILL Small bye") // and someone above may
	b.expect(osLine("Boss", "Small is disconnected."))
}

// OperServ SVSNICK, SVSJOIN and SVSPART follow the same rank rule: an oper
// with only flag s could rename the admin or move them in and out of channels.
func TestOperServSvsHigherOper(t *testing.T) {
	s, _, _ := fileServer(t, "")
	b := boss(t, s)
	small := login(t, s, "Small")
	small.sync()
	b.send("OS OLINE Small +s")
	b.expect(osLine("Boss", "Small now has oper flags s until they quit."))
	small.expect(" 381 Small ")
	b.send("JOIN #here")
	b.expect(" JOIN #here")

	small.send("OS SVSNICK Boss Rude")
	small.expect(osLine("Small", "Boss has oper flag a, which you don't have."))
	small.send("OS SVSJOIN Boss #there")
	small.expect(osLine("Small", "Boss has oper flag a, which you don't have."))
	small.send("OS SVSPART Boss #here")
	small.expect(osLine("Small", "Boss has oper flag a, which you don't have."))
	var nick string
	var inThere, inHere bool
	s.onLoop(func() {
		u := s.net.UserByUID(s.clients[s.net.UserByNick("Boss").UID].uid)
		nick = u.Nick
		if ch := s.net.Channel("#there"); ch != nil {
			inThere = ch.Member(u) != nil
		}
		if ch := s.net.Channel("#here"); ch != nil {
			inHere = ch.Member(u) != nil
		}
	})
	if nick != "Boss" || inThere || !inHere {
		t.Fatalf("an oper with flag s moved the admin: nick %s, in #there %v, in #here %v", nick, inThere, inHere)
	}

	other := login(t, s, "Other")
	other.sync()
	small.send("OS SVSNICK Other Other2") // someone without oper flags: as before
	small.expect(osLine("Small", "The nick Other is now being changed to Other2."))
	b.send("OS SVSJOIN Small #there") // and someone above may
	b.expect(osLine("Boss", "Small has been joined to #there."))
}
