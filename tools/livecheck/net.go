package main

import (
	"bufio"
	"net"
	"os/exec"
	"sort"
	"strings"
	"time"
)

// netCheck is the multi-server suite (Stage 2f): three linked servers,
// the first one running services. -net names them "name=host:port,...";
// -cut and -heal are shell commands that cut the third server's link and
// give it back (the test network uses iptables in its network namespace).
type netCheck struct {
	names, addrs []string
	cut, heal    string
}

// connectTo registers nick on addr (plain), asking for caps first.
func (lc *liveCheck) connectTo(addr, nick, caps string) *client {
	conn, err := net.DialTimeout("tcp", addr, 10*time.Second)
	if err != nil {
		lc.fatal("connect %s to %s: %v", nick, addr, err)
	}
	c := &client{nick: nick, conn: conn, r: bufio.NewReader(conn)}
	lc.all = append(lc.all, c)
	if caps != "" {
		c.send("CAP REQ :" + caps)
		c.send("CAP END")
	}
	c.send("NICK " + nick)
	c.send("USER zt 0 * :live test")
	c.wait(" 001 ")
	return c
}

func (lc *liveCheck) shell(what, cmd string) {
	if out, err := exec.Command("sh", "-c", cmd).CombinedOutput(); err != nil {
		lc.fatal("%s (%s): %v %s", what, cmd, err, out)
	}
}

// names returns channel's members as c sees them (prefixes kept), sorted.
func names(c *client, channel string) string {
	c.lines = nil
	c.send("NAMES " + channel)
	var all []string
	end := time.Now().Add(5 * time.Second)
	for time.Now().Before(end) {
		l := c.wait(` (353|366) `, time.Until(end))
		if l == "" || strings.Contains(l, " 366 ") {
			break
		}
		if i := strings.LastIndex(l, " :"); i >= 0 {
			all = append(all, strings.Fields(l[i+2:])...)
		}
	}
	sort.Strings(all)
	return strings.Join(all, " ")
}

// alive says whether c's connection still answers.
func alive(c *client) bool {
	c.send("PING :zt-alive")
	return c.wait(`PONG .*zt-alive`, 5*time.Second) != ""
}

func (lc *liveCheck) network(n netCheck) {
	if len(n.addrs) != 3 {
		lc.fatal("-net needs three servers: hub (with services), leaf1, leaf2 (the one -cut splits)")
	}
	hubName, leaf2 := n.names[0], n.names[2]
	PW, ch := lc.pw, "#ztnet"
	h := lc.connectTo(n.addrs[0], "ztnh", "batch")
	l1 := lc.connectTo(n.addrs[1], "ztn1", "")
	l2 := lc.connectTo(n.addrs[2], "ztn2", "batch")

	// One channel across three servers; leaf1's user makes it (op), so it
	// can register it with ChanServ from the leaf.
	for _, c := range []*client{l1, h, l2} {
		c.send("JOIN " + ch)
		c.wait(" 366 ")
	}
	lc.ok("hub sees leaf2's user join", h.wait(`:ztn2!.* JOIN :?#ztnet`))
	l2.send("PRIVMSG " + ch + " :from leaf2")
	lc.ok("leaf2 -> hub in the channel", h.wait(`:ztn2!.* PRIVMSG #ztnet :from leaf2`))
	lc.ok("leaf2 -> leaf1 in the channel (two hops)", l1.wait(`:ztn2!.* PRIVMSG #ztnet :from leaf2`))
	l1.send("PRIVMSG ztn2 :pm leaf1 to leaf2")
	lc.ok("private message leaf1 -> leaf2", l2.wait(`:ztn1!.* PRIVMSG ztn2 :pm leaf1 to leaf2`))
	h.send("WHOIS ztn2")
	lc.ok("WHOIS on the hub names leaf2's server", h.wait(` 312 ztnh ztn2 `+leaf2))
	h.send("LINKS")
	lc.ok("LINKS lists leaf2", h.wait(` 364 ztnh `+leaf2))

	// Services from a leaf: NickServ and ChanServ run on the hub.
	l1.send("NS REGISTER " + PW + " ztn1@example.org")
	if l1.wait("at least 30 seconds", 3*time.Second) != "" {
		println("waiting out reg_delay (30 s)...")
		time.Sleep(31 * time.Second)
		l1.send("NS REGISTER " + PW + " ztn1@example.org")
	}
	lc.ok("NS REGISTER from leaf1", l1.wait("registered", 8*time.Second))
	l1.send("CS REGISTER " + ch + " net test")
	lc.ok("CS REGISTER from leaf1", l1.wait("is registered to"))
	lc.ok("ChanServ's +r (set on the hub) reaches leaf2", l2.wait(`ChanServ!\S* MODE #ztnet \+\S*r`))
	l2.send("NS INFO ztn1")
	lc.ok("NS INFO from leaf2", l2.wait(`NickServ!.* NOTICE ztn2 :.*ztn1`))

	// Opers on a leaf: entries live on the services server. The test
	// network's admin is ztnadmin (its first /oper sets its password); it
	// adds ztnop, who opers on leaf1.
	adm := lc.connectTo(n.addrs[0], "ztnadmin", "")
	adm.send("NS REGISTER " + PW + " ztnadmin@example.org")
	adm.wait("registered", 8*time.Second)
	adm.send("OPER ztnadmin " + PW + "-oper")
	lc.ok("admin opers on the hub", adm.wait(" 381 ", 8*time.Second))
	op := lc.connectTo(n.addrs[1], "ztnop", "")
	op.send("NS REGISTER " + PW + " ztnop@example.org")
	op.wait("registered", 8*time.Second)
	adm.send("AS OPER ztnop ADD CLASS globop HOST *@* PASSWORD " + PW + "-op2")
	lc.ok("oper entry added on the hub", adm.wait("is an oper now", 8*time.Second))
	op.send("OPER ztnop wrong-password")
	lc.ok("leaf1: wrong oper password refused (checked on the hub)", op.wait(" 464 ztnop ", 8*time.Second))
	op.send("OPER ztnop " + PW + "-op2")
	lc.ok("leaf1: oper from the hub's entry", op.wait(" 381 ztnop ", 8*time.Second))
	h.send("WHOIS ztnop")
	lc.ok("hub sees leaf1's oper", h.wait(` 313 ztnh ztnop `))
	adm.send("AS OPER ztnop DISABLE live test")
	lc.ok("leaf1: entry disabled on the hub de-opers there at once", op.wait(`disabled \(live test\), so you are no longer an operator`, 8*time.Second))

	// Split: cut leaf2's link.
	h.lines, l1.lines, l2.lines = nil, nil, nil
	lc.shell("cut", n.cut)
	reason := hubName + " " + leaf2
	lc.ok("split: the hub opens a netsplit batch", h.wait(`BATCH \+\S+ netsplit `+hubName+" "+leaf2, 90*time.Second))
	lc.ok("split: hub sees leaf2's user quit with the two server names", h.wait(`:ztn2!.* QUIT :`+reason))
	lc.ok("split: leaf1 sees it too", l1.wait(`:ztn2!.* QUIT :`+reason, 10*time.Second))
	lc.ok("split: leaf2 sees the hub's user quit", l2.wait(`:ztnh!.* QUIT :`, 90*time.Second))

	// Changes on both sides while split, and a nick taken on both.
	l1.send("TOPIC " + ch + " :set during the split")
	l1.wait("TOPIC")
	ch1 := lc.connectTo(n.addrs[0], "ztncoll", "")
	time.Sleep(2 * time.Second) // the older nick keeps it (same second: both renamed)
	ch2 := lc.connectTo(n.addrs[2], "ztncoll", "")
	ch1.send("JOIN " + ch)
	ch2.send("JOIN " + ch)
	ch1.wait(" 366 ")
	ch2.wait(" 366 ")

	// Rejoin: leaf2 links again by itself (autoconnect).
	lc.shell("heal", n.heal)
	lc.ok("rejoin: leaf2 sees the hub's user again", l2.wait(`:ztnh!.* JOIN :?#ztnet`, 120*time.Second))
	time.Sleep(2 * time.Second) // let the burst settle
	l2.send("TOPIC " + ch)
	lc.ok("rejoin: leaf2 has the topic set during the split", l2.wait(` 332 ztn2 #ztnet :set during the split`))
	was := func(c *client) bool { return c.wait(`:ztncoll!\S* NICK :?\S+`, 3*time.Second) != "" }
	r1, r2 := was(ch1), was(ch2)
	lc.check("rejoin: the nick clash renamed the newer one only", !r1 && r2)
	lc.check("rejoin: both clash users are still connected", alive(ch1) && alive(ch2))
	nh, n1, n2 := names(h, ch), names(l1, ch), names(l2, ch)
	lc.check("rejoin: all three servers agree on the channel ("+nh+")", nh != "" && nh == n1 && n1 == n2)
	if nh != n1 || n1 != n2 {
		println("  hub:   " + nh + "\n  leaf1: " + n1 + "\n  leaf2: " + n2)
	}
	l2.send("PRIVMSG " + ch + " :after the rejoin")
	lc.ok("rejoin: messages flow again", h.wait(`:ztn2!.* PRIVMSG #ztnet :after the rejoin`))

	// Clean up.
	adm.send("AS OPER ztnop DEL")
	lc.ok("oper entry removed", adm.wait("ztnop", 8*time.Second))
	op.send("NS DROP ztnop")
	lc.ok("oper's account dropped", op.wait("has been dropped"))
	adm.send("NS DROP ztnadmin")
	adm.wait("has been dropped|can't|cannot", 5*time.Second)
	l1.send("CS DROP " + ch + " " + ch)
	lc.ok("channel dropped", l1.wait("no longer registered"))
	l1.send("NS DROP ztn1")
	lc.ok("account dropped", l1.wait("has been dropped"))
}
