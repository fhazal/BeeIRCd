package state

import "testing"

func TestMatchBan(t *testing.T) {
	bee := &User{Nick: "Bee", Ident: "~bee", Host: "cloak.example", RealHost: "home.example", IP: "10.0.0.1", Realname: "Bee Real", Account: "Bee"}
	anon := &User{Nick: "x", Ident: "~x", Host: "h", Realname: "spam bot"}
	for _, c := range []struct {
		mask       string
		u          *User
		match, mut bool
	}{
		{"Bee!*@*", bee, true, false},
		{"*!*@home.example", bee, true, false}, // the real host counts for channel bans
		{"~a:bee", bee, true, false},
		{"~a:*", bee, true, false},
		{"~a:*", anon, false, false},
		{"~a:0", anon, true, false},
		{"~a:0", bee, false, false},
		{"~r:*spam*", anon, true, false},
		{"~r:*spam*", bee, false, false},
		{"~m:*!*@h", anon, true, true},
		{"~q:~a:bee", bee, true, true},
		{"~t:30:Bee!*@*", bee, true, false},
		{"~t:30:~m:Bee!*@*", bee, true, true},
		{"~z:anything", bee, false, false},
	} {
		m, mute := MatchBan(c.mask, c.u)
		if m != c.match || mute != c.mut {
			t.Errorf("MatchBan(%q, %s) = %v, %v; want %v, %v", c.mask, c.u.Nick, m, mute, c.match, c.mut)
		}
	}
	ch := &Channel{Lists: map[byte][]ListEntry{'b': {{Mask: "~m:*!*@h"}}}}
	if ch.IsBanned(anon) || !ch.IsMuted(anon) {
		t.Error("a mute must stop talking, not joining")
	}
	ch.Lists['e'] = []ListEntry{{Mask: "~r:spam*"}}
	if ch.IsMuted(anon) {
		t.Error("an exception must lift a mute")
	}
}

func TestMatchUserExtbans(t *testing.T) {
	bees := &Channel{Name: "#bees", Members: map[string]*Member{}}
	oper := &User{UID: "1", Nick: "Op", Ident: "op", Host: "h", Server: "hub.example", Account: "Op", CertFP: "abcd", OperClass: "netadmin", OperName: "Fhazal", Country: "NL", ASN: 64500,
		Modes: map[byte]bool{'o': true}, Channels: map[string]*Channel{"#bees": bees}}
	bees.Members["1"] = &Member{User: oper, Ranks: RankOp}
	bot := &User{UID: "2", Nick: "Bot", Ident: "bot", Host: "b", Realname: "a bot", Server: "leaf.example", Gateway: "beeroom",
		Modes: map[byte]bool{'B': true}, Channels: map[string]*Channel{"#bees": bees}}
	bees.Members["2"] = &Member{User: bot, Ranks: RankVoice}
	for _, c := range []struct {
		who  string
		u    *User
		want bool
	}{
		{"~c:#bees", oper, true}, {"~c:@#bees", oper, true}, {"~c:@#bees", bot, false}, {"~c:+#bees", bot, true}, {"~c:#other", oper, false},
		{"~O:net*", oper, true}, {"~O:net*", bot, false},
		{"~oper:fhaz*", oper, true}, {"~oper:*", bot, false},
		{"~S:ab*", oper, true}, {"~S:*", bot, false},
		{"~unauthed:*!*@*", bot, true}, {"~unauthed:*!*@*", oper, false},
		{"~bot:*!*@*", bot, true}, {"~bot:*!*@*", oper, false},
		{"~gateway:bee*", bot, true}, {"~gateway:*", oper, false},
		{"~server:leaf.*", bot, true}, {"~server:leaf.*", oper, false},
		{"~realmask:Bot!*@*+a bot", bot, true}, {"~realmask:*+x*", bot, false},
		{"~C:NL", oper, true}, {"~C:N*", oper, true}, {"~C:NL", bot, false}, {"~C:*", bot, false},
		{"~A:64500", oper, true}, {"~A:64501", oper, false}, {"~A:64500", bot, false},
	} {
		if got := MatchUser(c.who, c.u); got != c.want {
			t.Errorf("MatchUser(%q, %s) = %v", c.who, c.u.Nick, got)
		}
	}
}

func TestChannelStops(t *testing.T) {
	u := &User{UID: "1", Nick: "x", Ident: "x", Host: "h", Account: "Bee", Channels: map[string]*Channel{}}
	ch := func(b, e []string) *Channel {
		c := &Channel{Name: "#c", Lists: map[byte][]ListEntry{}}
		for _, m := range b {
			c.Lists['b'] = append(c.Lists['b'], ListEntry{Mask: m})
		}
		for _, m := range e {
			c.Lists['e'] = append(c.Lists['e'], ListEntry{Mask: m})
		}
		return c
	}
	for _, c := range []struct {
		b, e []string
		what Stop
		want bool
	}{
		{[]string{"x!*@*"}, nil, StopJoin, true},
		{[]string{"x!*@*"}, nil, StopNick, true},
		{[]string{"x!*@*"}, []string{"~a:Bee"}, StopTalk, false},
		{[]string{"~j:x!*@*"}, nil, StopJoin, true},
		{[]string{"~j:x!*@*"}, nil, StopTalk, false},
		{[]string{"~n:~a:Bee"}, nil, StopNick, true},
		{[]string{"~n:~a:Bee"}, nil, StopJoin, false},
		{[]string{"~p:x!*@*"}, nil, StopPartMsg, true},
		{[]string{"~q:x!*@*"}, []string{"~q:~a:Bee"}, StopTalk, false},
		{[]string{"x!*@*"}, []string{"~q:~a:Bee"}, StopJoin, true}, // a mute exception doesn't lift a ban
		{[]string{"~noctcp:x!*@*"}, nil, StopCTCP, true},
		{[]string{"~nonotice:x!*@*"}, nil, StopNotice, true},
		{[]string{"~blockcolor:x!*@*"}, nil, StopColor, true},
		{[]string{"~stripcolor:x!*@*"}, nil, StripColor, true},
		{[]string{"~nokick:x!*@*"}, nil, StopKick, true},
		{[]string{"~blockinvite:x!*@*"}, nil, StopInvite, true},
		{[]string{"~t:5:~j:x!*@*"}, nil, StopJoin, true},
		{[]string{"~T:block:*x*"}, nil, StopTalk, false},
	} {
		if got := ch(c.b, c.e).Stops(u, c.what); got != c.want {
			t.Errorf("b=%v e=%v Stops(%d) = %v", c.b, c.e, c.what, got)
		}
	}
	c := ch(nil, []string{"~m:moderated:~a:Bee", "~F:x!*@*"})
	if !c.Bypasses(u, "moderated") || c.Bypasses(u, "external") || !c.FloodExempt(u) {
		t.Error("msgbypass / flood exceptions")
	}
	c = ch([]string{"~T:block:*spam*", "~T:censor:*damn*"}, nil)
	if _, blocked := c.TextBan("buy spam now", "<censored>"); !blocked {
		t.Error("~T:block")
	}
	if out, blocked := c.TextBan("oh damnit ok", "<censored>"); blocked || out != "oh <censored> ok" {
		t.Errorf("~T:censor = %q, %v", out, blocked)
	}
}

func TestInheritBans(t *testing.T) {
	n := NewNetwork("001")
	u := &User{UID: "001A", Nick: "x", Ident: "x", Host: "h", Modes: map[byte]bool{}, Channels: map[string]*Channel{}}
	o := &User{UID: "001B", Nick: "o", Ident: "o", Host: "o", Modes: map[byte]bool{}, Channels: map[string]*Channel{}}
	if err := n.addUser(o); err != nil {
		t.Fatal(err)
	}
	main, _, _ := n.join(o, "#main", 1, RankOp, false)
	side, _, _ := n.join(o, "#side", 1, RankOp, false)
	main.Lists['b'] = []ListEntry{{Mask: "x!*@*"}}
	side.Lists['b'] = []ListEntry{{Mask: "~i:#main"}}
	if !side.IsBanned(u) {
		t.Error("~i:#main must carry #main's bans")
	}
	main.Lists['b'] = []ListEntry{{Mask: "~i:#side"}}
	if side.IsBanned(u) {
		t.Error("inheriting goes one level only")
	}
}
