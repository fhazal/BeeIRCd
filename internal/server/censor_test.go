package server

import (
	"testing"

	"github.com/fhazal/beeircd/internal/config"
)

func TestCensorWords(t *testing.T) {
	words := []string{"darn", "*spam*", "heck"}
	for in, want := range map[string]string{
		"darn it":               "<c> it",
		"Darn, DARN!":           "<c>, <c>!",
		"no spammers here":      "no <c> here",
		"darning socks":         "darning socks", // whole words only
		"what the \x02heck\x02": "what the \x02<c>\x02",
		"heck's sake":           "<c>'s sake",
		"nothing to see":        "nothing to see",
		"\xff darn":             "\xff darn", // not UTF-8: left alone
		"spamé café darn":       "<c> café <c>",
	} {
		if got := censor(in, words, "<c>"); got != want {
			t.Errorf("censor(%q) = %q, want %q", in, got, want)
		}
	}
	if got := censor("darn", nil, "<c>"); got != "darn" {
		t.Errorf("no words: %q", got)
	}
}

func TestCensorMode(t *testing.T) {
	s := testServer(t, func(c *config.Config) { c.Censor.Words = []string{"darn"} })
	bee, ember := login(t, s, "Bee"), login(t, s, "ember")
	bee.send("JOIN #a")
	bee.expect(" 366 Bee #a ")
	ember.send("JOIN #a")
	ember.expect(" 366 ember #a ")
	ember.send("PRIVMSG #a :darn it")
	bee.expect("PRIVMSG #a :darn it")
	bee.send("MODE #a +G")
	bee.expect("MODE #a +G")
	ember.send("PRIVMSG #a :darn it")
	bee.expect("PRIVMSG #a :<censored> it")
	bee.send("PRIVMSG #a :darn, ops are not censored")
	ember.expect("PRIVMSG #a :darn, ops are not censored")
}
