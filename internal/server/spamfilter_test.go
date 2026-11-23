package server

import (
	"strings"
	"testing"
)

// spamRule adds a rule as Boss and checks it went on.
func spamRule(t *testing.T, b *tc, args string) {
	t.Helper()
	b.send("SPAMFILTER add " + args)
	b.expect("*** Spamfilter: Added rule ")
}

func TestSpamfilterBlocksChannelText(t *testing.T) {
	s, _, _ := fileServer(t, "")
	b := boss(t, s)
	spamRule(t, b, "cp block - No_nitro_spam *free nitro*")
	a, v := login(t, s, "Ann"), login(t, s, "Vic")
	a.send("JOIN #lobby")
	a.expect(" 366 Ann #lobby ")
	v.send("JOIN #lobby")
	v.expect(" 366 Vic #lobby ")
	a.sync()

	v.send("PRIVMSG #lobby :get FREE \x02nitro\x02 here")
	v.expect("*** Your message to #lobby was blocked: No nitro spam")
	v.send("PRIVMSG Ann :free nitro for you")
	v.expect("*** Your message to Ann was blocked: No nitro spam")
	v.send("PRIVMSG #lobby :hello all")
	a.expect("PRIVMSG #lobby :hello all")
	a.quiet("nitro")
	b.expect("FILTER: Vic had their message to #lobby filtered as it matched *free nitro* (No nitro spam)")

	// Several targets: only the caught one is dropped.
	spamRule(t, b, "p block - No_dms_with_links *bit.ly/*")
	v.send("PRIVMSG #lobby,Ann :see bit.ly/x")
	v.expect("*** Your message to Ann was blocked: No dms with links")
	a.expect("PRIVMSG #lobby :see bit.ly/x")
	a.quiet("PRIVMSG Ann")
}

func TestSpamfilterWarnLetsThrough(t *testing.T) {
	s, _, _ := fileServer(t, "")
	b := boss(t, s)
	spamRule(t, b, "c warn - Trying_out *crypto doubler*")
	a, v := login(t, s, "Ann"), login(t, s, "Vic")
	for _, c := range []*tc{a, v} {
		c.send("JOIN #lobby")
		c.expect(" 366 " + c.nick + " #lobby ")
	}
	v.send("PRIVMSG #lobby :best crypto doubler ever")
	a.expect("PRIVMSG #lobby :best crypto doubler ever")
	b.expect("FILTER: WARNING: Vic's message to #lobby matched *crypto doubler* (Trying out)")
	v.quiet("blocked")
}

func TestSpamfilterKillAndAkill(t *testing.T) {
	s, _, _ := fileServer(t, "")
	b := boss(t, s)
	spamRule(t, b, "p kill - Bye *kill me now*")
	v := login(t, s, "Vic")
	v.send("PRIVMSG Boss :kill me now please")
	v.expect("ERROR :Closing Link: 127.0.0.1 (Spam filter: Bye)")

	spamRule(t, b, "c gline 1h Spammers_go *buy followers*")
	w := login(t, s, "Wes")
	w.send("JOIN #x")
	w.expect(" 366 Wes #x ")
	w.send("PRIVMSG #x :buy followers cheap")
	w.expect("(Banned: Spammers go)")
	b.send("PRIVMSG OperServ :AKILL LIST")
	b.expect("*@127.0.0.1")
	// The AKILL keeps them out: a new connection from that IP is refused.
	c := raw(t, s)
	c.send("NICK Wes2")
	c.send("USER wes 0 * :Wes")
	c.expect("You are banned from this network: Spammers go")
}

func TestSpamfilterOpersAndExceptChannelsSkipped(t *testing.T) {
	s, _, _ := fileServer(t, "\n[spamfilter]\nexcept = [\"#help\"]\n")
	b := boss(t, s)
	spamRule(t, b, "c block - No *free nitro*")
	a, v := login(t, s, "Ann"), login(t, s, "Vic")
	for _, c := range []*tc{a, v, b} {
		c.send("JOIN #help,#lobby")
		c.expect(" 366 " + c.nick + " #lobby ")
	}
	v.send("PRIVMSG #help :someone said free nitro, is it real?")
	a.expect("PRIVMSG #help :someone said free nitro, is it real?")
	b.send("PRIVMSG #lobby :warning: free nitro links are scams")
	a.expect("PRIVMSG #lobby :warning: free nitro links are scams")
	v.send("PRIVMSG #lobby :free nitro")
	v.expect("was blocked")
}

func TestSpamfilterSafetyNet(t *testing.T) {
	s, _, _ := fileServer(t, "")
	b := boss(t, s)
	b.send("SPAMFILTER add c block - No *free*")
	b.expect(`Not added: it would also catch normal chat, like "free tonight?" and "feel free to ask"`)
	b.send("SPAMFILTER add -force c block - No *free*")
	b.expect("*** Spamfilter: Added rule 1")
	b.send("SPAMFILTER add c block - No *a*")
	b.expect("a simple match needs at least 4 normal characters")
	b.send("SPAMFILTER add -regex c block - No (unclosed")
	b.expect("the regex doesn't work")
	b.send("SPAMFILTER add cx block - No *free nitro*")
	b.expect("is not a target")
	b.send("SPAMFILTER add c explode - No *free nitro*")
	b.expect(`"explode" is not an action`)

	v := login(t, s, "Vic")
	v.send("SPAMFILTER add c block - No *free nitro*")
	v.expect(" 481 Vic ") // opers with k only
}

func TestSpamfilterListTestDel(t *testing.T) {
	s, _, _ := fileServer(t, "")
	b := boss(t, s)
	spamRule(t, b, "cp gline 1d Spam_is_not_allowed *free nitro*")
	spamRule(t, b, "-regex u kill - Bot_nicks ^[a-z]{3}[0-9]{5}!")
	b.send("STATS F")
	if l := b.expect(" 229 Boss F simple cp gline Boss 0 "); !strings.HasSuffix(strings.TrimSpace(l), " 86400 Spam_is_not_allowed #1 0 :*free nitro*") {
		t.Fatal(l)
	}
	b.expect(" 229 Boss F regex u kill Boss 0 ")
	b.expect(" 219 Boss F ")
	b.send("SPAMFILTER test cpu get free nitro now")
	b.expect("Rule 1 would catch it (chan)")
	b.send("SPAMFILTER test u abc12345!~x@h:x")
	b.expect("Rule 2 would catch it (nick)")
	b.send("SPAMFILTER test c hello")
	b.expect("Nothing would catch that.")
	b.send("SPAMFILTER del 1")
	b.expect("*** Spamfilter: Removed rule 1: *free nitro* on chan, private -> AKILL 1d (Spam is not allowed)")
	b.send("SPAMFILTER del 1")
	b.expect("there is no spam filter 1")
	b.send("STATS F")
	b.quiet("*free nitro*")
}

func TestSpamfilterNickAtConnectAndChange(t *testing.T) {
	s, _, _ := fileServer(t, "")
	b := boss(t, s)
	spamRule(t, b, "-regex u kill - Bot_nicks ^[a-z]{3}[0-9]{5}!")
	spamRule(t, b, "u block - No_such_nicks *spammer*!*")
	c := raw(t, s)
	c.send("NICK abc12345")
	c.send("USER x 0 * :x")
	c.expect("(Spam filter: Bot nicks)")
	c2 := raw(t, s)
	c2.send("NICK bigspammer")
	c2.send("USER x 0 * :x")
	c2.expect("(Spam filter: No such nicks)")

	v := login(t, s, "Vic")
	v.send("NICK spammerboy")
	v.expect(" 432 Vic spammerboy :Nick refused by the spam filter: No such nicks")
	v.send("NICK Victor")
	v.expect(":Vic!~vic@127.0.0.1 NICK Victor")
}

func TestSpamfilterPartQuitTopicAway(t *testing.T) {
	s, _, _ := fileServer(t, "")
	b := boss(t, s)
	spamRule(t, b, "Pqta block - No_ads *visit my shop*")
	a, v := login(t, s, "Ann"), login(t, s, "Vic")
	for _, c := range []*tc{a, v} {
		c.send("JOIN #lobby")
		c.expect(" 366 " + c.nick + " #lobby ")
	}
	v.send("TOPIC #lobby :visit my shop at x")
	v.expect("*** Your message to #lobby was blocked: No ads")
	v.send("AWAY :visit my shop")
	v.expect("*** Your message to the server was blocked: No ads")
	v.send("PART #lobby :visit my shop today")
	if line := a.expect(":Vic!~vic@127.0.0.1 PART #lobby"); strings.Contains(line, "shop") {
		t.Fatal(line)
	}
	v.send("JOIN #lobby")
	a.expect(":Vic!~vic@127.0.0.1 JOIN #lobby")
	v.send("QUIT :visit my shop bye")
	if line := a.expect(":Vic!~vic@127.0.0.1 QUIT"); strings.Contains(line, "shop") {
		t.Fatal(line)
	}
}

func TestSpamfilterTempShun(t *testing.T) {
	s, _, _ := fileServer(t, "")
	b := boss(t, s)
	spamRule(t, b, "c tempshun - Quiet *join my server*")
	a, v := login(t, s, "Ann"), login(t, s, "Vic")
	for _, c := range []*tc{a, v} {
		c.send("JOIN #lobby")
		c.expect(" 366 " + c.nick + " #lobby ")
	}
	v.send("PRIVMSG #lobby :join my server now")
	v.send("PRIVMSG #lobby :and hello")
	v.sync()
	a.quiet("PRIVMSG #lobby")
	// A new connection is not shunned.
	w := login(t, s, "Vic2")
	w.send("JOIN #lobby")
	w.expect(" 366 Vic2 #lobby ")
	w.send("PRIVMSG #lobby :hi again")
	a.expect("PRIVMSG #lobby :hi again")
}

// SPAMFILTER del takes the id or the words of add (with or without the
// type), and says when no rule is like that.
func TestSpamfilterDelBySpec(t *testing.T) {
	s, _, _ := fileServer(t, "")
	b := boss(t, s)
	spamRule(t, b, "p block - No_dms_with_links *bit.ly/*")
	spamRule(t, b, "-regex u kill - Bot_nicks ^[a-z]{3}[0-9]{5}!")
	b.send("SPAMFILTER del -simple p block - No_dms_with_links *other*")
	b.expect("there is no spam filter like that")
	b.send("SPAMFILTER del -simple p block - No_dms_with_links *bit.ly/*")
	b.expect("*** Spamfilter: Removed rule 1: *bit.ly/* on private")
	b.send("SPAMFILTER del -regex u kill - Bot_nicks ^[a-z]{3}[0-9]{5}!")
	b.expect("*** Spamfilter: Removed rule 2: ")
	b.send("SPAMFILTER del -simple p block - No_dms_with_links *bit.ly/*")
	b.expect("there is no spam filter like that")
}

// Adding and removing a rule tells opers in InspIRCd 4's m_filter words.
func TestSpamfilterNoticeWords(t *testing.T) {
	s, _, _ := fileServer(t, "")
	b := boss(t, s)
	b.send("SPAMFILTER add c gline 1h Spammers_go *buy followers*")
	b.expect("*** FILTER: Boss added filter '*buy followers*', type 'gline', duration '1 hour', flags 'c', reason: Spammers go")
	b.send("SPAMFILTER add cp block - No_nitro_spam *free nitro*")
	b.expect("*** FILTER: Boss added filter '*free nitro*', type 'block', flags 'cp', reason: No nitro spam")
	b.send("SPAMFILTER del cp block - No_nitro_spam *free nitro*")
	b.expect("*** FILTER: Boss removed filter '*free nitro*': No nitro spam")
}

// A metadata value the spam filter blocks says it was the spam filter.
func TestSpamfilterBlocksMetadataValue(t *testing.T) {
	s, _, _ := fileServer(t, "")
	s.do(func() { s.cfg.Server.IRCv3Drafts = true })
	b := boss(t, s)
	spamRule(t, b, "a block - No_ads *visit my shop*")
	v := mdClient(t, s, "Vic")
	v.send("METADATA * SET status :visit my shop")
	v.expect("Vic was refused by the spam filter: No ads")
	v.send("METADATA * SET status :hello")
	v.expect(" 761 Vic Vic status * :hello")
}
