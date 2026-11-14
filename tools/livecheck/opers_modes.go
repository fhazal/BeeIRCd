package main

import (
	"fmt"
	"strings"
	"time"
)

// opers: the oper side as a non-oper sees it.
func (lc *liveCheck) opers() {
	const deny = "AdminServ is for server admins|OperServ is for IRC operators"
	a := lc.connect("zt8a", "")
	a.clear()
	for _, tc := range []struct{ cmd, name string }{
		{"AS CLASS LIST", "AS CLASS LIST refused for non-opers"},
		{"AS CLASS zthelp ADD h", "AS CLASS ADD refused"},
		{"AS HISTORY", "AS HISTORY refused"},
		{"AS DIFF 1", "AS DIFF refused"},
		{"AS UNDO 1", "AS UNDO refused"},
		{"AS OPER zt8a DISABLE test", "AS OPER DISABLE refused"},
		{"OS OPER LIST", "OS OPER LIST refused"},
		{"OS OPER INFO zt8a", "OS OPER INFO refused"},
		{"OS OPER ADD zt8a helper", "OS OPER ADD refused"},
	} {
		a.send(tc.cmd)
		lc.ok(tc.name, a.wait(deny))
	}
	a.send("OPER zt8a whatever")
	lc.ok("/oper unknown entry -> 491", a.wait(" 491 "))
	if lc.admin != "" {
		// One wrong try counts toward that account's oper lockout (5 in 10 min).
		a.send("OPER " + lc.admin + " wrong-password-zt8")
		lc.ok("/oper "+lc.admin+" wrong password refused", a.wait(" (464|491) "))
	}
	a.send("MODE zt8a")
	m := a.wait(" 221 ")
	f := strings.Fields(m)
	lc.check("still not opered (no +o)", m != "" && !strings.Contains(f[len(f)-1], "o"))
	a.send("QUIT :done")
}

// modes: the channel modes.
func (lc *liveCheck) modes() {
	join := func(c *client, ch string) string {
		c.send("JOIN " + ch)
		return c.wait(fmt.Sprintf(" 366 %s %s ", c.nick, ch))
	}
	suffix := fmt.Sprint(time.Now().Unix() % 10000)
	A, B, C3 := "#zt9a"+suffix, "#zt9b"+suffix, "#zt9z"+suffix
	op, u, w := lc.connect("zt7op", ""), lc.connect("zt7u", ""), lc.connect("zt7w", "")
	for _, c := range []*client{op, u, w} {
		c.clear()
	}
	op.send("VERSION")
	l := op.wait("CHANMODES=")
	lc.check("005 has the new modes", strings.Contains(l, "CHANMODES=beI,k,BEFHJLfl,CDKMNOPQRSTVZcimnprstz"))
	join(op, A)
	join(u, A)
	join(w, A)
	op.send("MODE " + A + " +cCT")
	lc.ok("+cCT set", w.wait("MODE "+A+` \+cCT`))
	u.send("PRIVMSG " + A + " :\x034red")
	lc.ok("+c refuses colour", u.wait(" 404 zt7u "+A+" :Color is not permitted"))
	u.send("PRIVMSG " + A + " :\x01VERSION\x01")
	lc.ok("+C refuses CTCP", u.wait("CTCPs are not permitted"))
	u.send("PRIVMSG " + A + " :\x01ACTION waves\x01")
	lc.ok("+C lets ACTION through", w.wait("ACTION waves"))
	op.send("MODE " + A + " -c+S")
	w.wait("MODE " + A + ` -c\+S`)
	u.send("PRIVMSG " + A + " :\x0304,01colour\x03 gone")
	lc.ok("+S strips colour", w.wait("PRIVMSG "+A+" :colour gone"))
	op.send("MODE " + A + " +NVQ")
	w.wait("MODE " + A + ` \+NVQ`)
	u.send("NICK zt7u2")
	lc.ok("+N refuses nick change", u.wait(" 447 zt7u :Can not change nickname while on"))
	op.send("INVITE zt7w " + A)
	lc.ok("+V refuses INVITE", op.wait(" 518 "))
	op.send("KICK " + A + " zt7u")
	lc.ok("+Q refuses KICK", op.wait(` 972 zt7op KICK :channel is \+Q`))
	op.send("MODE " + A + " +P")
	lc.ok("+P is for opers", op.wait(" 481 "))
	op.send("MODE " + A + " -NVQ+f [5x]:10")
	lc.ok("+f checks its value", op.wait(" 696 zt7op "+A+` f \[5x\]:10 `))
	op.send("MODE " + A + " +J 30")
	w.wait("MODE " + A + ` \+J 30`)
	op.send("MODE " + A + " +fE [3t]:30 2:60")
	lc.ok("+fE set", w.wait("MODE "+A+` \+fE \[3t\]:30 2:60`))
	u.send("PRIVMSG " + A + " :again")
	w.wait("PRIVMSG " + A + " :again")
	u.send("PRIVMSG " + A + " :again")
	lc.ok("+E refuses the repeat", u.wait("repeats what you sent before"))
	for _, t := range []string{"one", "two", "three"} {
		u.send("PRIVMSG " + A + " :" + t)
	}
	lc.ok("+f t: 4th line is a kick", w.wait("KICK "+A+" zt7u :Flooding"))
	u.send("JOIN " + A)
	lc.ok("+J no rejoin after kick", u.wait(" 495 zt7u "+A+" "))
	op.send("MODE " + A + " +B 10:70")
	w.wait("MODE " + A + ` \+B`)
	w.send("PRIVMSG " + A + " :THIS IS VERY LOUD TALK")
	lc.ok("+B refuses caps", w.wait("capital letters"))

	// +L and +l
	join(op, B)
	op.send("MODE " + B + " +lL 1 " + A)
	op.wait("MODE " + B + ` \+lL 1`)
	x := lc.connect("zt7x", "")
	x.clear()
	x.send("JOIN " + B)
	lc.ok("+L sends on when full", x.wait(" 470 zt7x "+B+" "+A+" "))
	lc.ok("... and joins the link", x.wait("JOIN "+A))

	// +D
	op.send("MODE " + B + " +D")
	op.wait("MODE " + B + ` \+D`)
	op.send("MODE " + B + " -l")
	op.wait("MODE " + B + " -l")
	join(w, B)
	op.send("NAMES " + B)
	n := op.wait(" 353 ")
	lc.check("+D hides new members in NAMES", n != "" && !strings.Contains(n, "zt7w"))
	w.send("PRIVMSG " + B + " :here I am")
	lc.ok("+D: talking shows the JOIN", op.wait(":zt7w!.* JOIN "+B))
	lc.ok("... then the message", op.wait("PRIVMSG "+B+" :here I am"))

	// +z / +Z
	if lc.tlsAddr != "" {
		t1 := lc.connectTLS("zt7tls", false, false)
		t1.clear()
		join(t1, C3)
		t1.send("MODE " + C3 + " +z")
		lc.ok("+z set", t1.wait("MODE "+C3+` \+z`))
		lc.ok("+Z follows (everyone on TLS)", t1.wait("MODE "+C3+` \+Z`))
		x.send("JOIN " + C3)
		lc.ok("+z refuses plain connections", x.wait(" 489 zt7x "+C3+" "))
		t1.send("QUIT :done")
	} else {
		println("(no -tls: +z/+Z checks NOT TESTED)")
	}
	op.send("HELPOP CHMODES")
	lc.ok("HELPOP CHMODES", op.wait(` 704 zt7op CHMODES :\*\*\*\*\* Channel modes`))
	for _, c := range []*client{op, u, w, x} {
		c.send("QUIT :done")
	}
	time.Sleep(500 * time.Millisecond)
}
