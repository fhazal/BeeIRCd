package server

import (
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/fhazal/beeircd/internal/config"
)

// [antirandom], [antimixedutf8], [targetfloodprot] and [blockamsg].

func TestRandomScore(t *testing.T) {
	for name, random := range map[string]bool{
		"Bee": false, "Belon": false, "Fhazal": false, "J|wang": false, "Psycho": false, "Lightning": false,
		"strengths": false, "Guest48213": false, "Khairul_Anuar": false, "Zulkifli": false, "The Bee": false,
		"xkqzvbnw": true, "qwrtpsdf": true, "hgfdsjklm": true, "a83749201z": true, "zxqjvkpw": true,
	} {
		if got := randomScore(name) >= 7; got != random {
			t.Errorf("randomScore(%q) = %d, random = %v, want %v", name, randomScore(name), got, random)
		}
	}
}

func TestAntiRandom(t *testing.T) {
	s := testServer(t, func(c *config.Config) {
		c.AntiRandom = config.AntiRandom{Enabled: true, Except: []string{}}
	})
	addr := s.Addrs()[0].String()
	login(t, s, "Bee")
	bot := raw(t, s)
	bot.nick = "xkqzvbnw"
	bot.send("NICK xkqzvbnw")
	bot.send("USER zxqjvk 0 * :qwrtpsdf hgfdsj")
	bot.expect("ERROR :Closing Link: 127.0.0.1 (You look like a bot. Be sure to fill in your nick/ident/realname properly.)")
	_ = addr

	// warn: let in, opers told; known people are never checked.
	w := testServer(t, func(c *config.Config) {
		c.AntiRandom = config.AntiRandom{Enabled: true, Action: "warn", Except: []string{}}
	})
	login(t, w, "xkqzvbnw")
	k := testServer(t, func(c *config.Config) {
		c.AntiRandom = config.AntiRandom{Enabled: true, Except: []string{"tls-users"}}
	})
	loginTLS(t, k, "xkqzvbnw")
}

func TestMixedScore(t *testing.T) {
	for text, want := range map[string]int{
		"hello world":          0,
		"привет мир":           0,
		"привет hello мир":     2,
		"日本語のテキスト and English": 1,
		"pаypаl":               8,  // Cyrillic а inside a Latin word, twice
		"frее bіtсоіn hеrе":    17, // look-alike letters all over
		"12345 !!! :)":         0,
	} {
		if got := mixedScore(text); got != want {
			t.Errorf("mixedScore(%q) = %d, want %d", text, got, want)
		}
	}
}

func TestAntiMixedUTF8(t *testing.T) {
	s := testServer(t, func(c *config.Config) { c.AntiMixedUTF8 = config.AntiMixedUTF8{Enabled: true} })
	bee, ember := login(t, s, "Bee"), login(t, s, "ember")
	bee.send("JOIN #x")
	bee.expect(" 366 ")
	ember.send("JOIN #x")
	ember.expect(" 366 ")
	ember.send("PRIVMSG #x :frее bіtсоіn hеrе")
	ember.expect(" 404 ember #x :Possible mixed character spam")
	ember.send("PRIVMSG Bee :frее bіtсоіn hеrе")
	ember.expect(" 531 ember Bee :Possible mixed character spam")
	ember.send("PRIVMSG #x :привет hello мир")
	bee.expect("PRIVMSG #x :привет hello мир")
	ember.send("PRIVMSG NickServ :frее bіtсоіn hеrе")
	ember.expect("NickServ!")
}

func TestTargetFlood(t *testing.T) {
	s := testServer(t, func(c *config.Config) {
		c.TargetFlood = config.TargetFlood{Enabled: true, ChannelPrivmsg: "3:60", PrivatePrivmsg: "2:60"}
	})
	bee, ember, nova := login(t, s, "Bee"), login(t, s, "ember"), login(t, s, "nova")
	for _, c := range []*tc{bee, ember, nova} {
		c.send("JOIN #x")
		c.expect(" 366 ")
	}
	// Three messages from two people fill the channel's allowance.
	ember.send("PRIVMSG #x :one")
	nova.send("PRIVMSG #x :two")
	bee.expect("PRIVMSG #x :one")
	bee.expect("PRIVMSG #x :two")
	ember.send("PRIVMSG #x :three")
	bee.expect("PRIVMSG #x :three")
	nova.send("PRIVMSG #x :four")
	nova.expect(" 404 nova #x :Channel is being flooded. Message not delivered.")
	nova.send("NOTICE #x :a notice has its own count")
	bee.expect("NOTICE #x :a notice has its own count")

	ember.send("PRIVMSG Bee :1")
	nova.send("PRIVMSG Bee :2")
	bee.expect("PRIVMSG Bee :2")
	ember.send("PRIVMSG Bee :3")
	ember.expect(" 531 ember Bee :User is being flooded. Message not delivered.")

	// A new period starts clean.
	s.onLoop(func() {
		for _, w := range s.targetFlood {
			for i := range w {
				w[i].start = w[i].start.Add(-2 * time.Minute)
			}
		}
	})
	nova.send("PRIVMSG #x :five")
	bee.expect("PRIVMSG #x :five")
	if n, _ := (config.TargetFlood{}).Limit(true, "PRIVMSG"); n != 45 {
		t.Errorf("default channel limit %d", n)
	}
}

func TestBlockAmsg(t *testing.T) {
	s := testServer(t, func(c *config.Config) {
		c.BlockAmsg = config.BlockAmsg{Enabled: true, Delay: config.Duration{Duration: 5 * time.Second}}
	})
	bee, ember := login(t, s, "Bee"), login(t, s, "ember")
	for i := 0; i < 3; i++ {
		ch := "#c" + strconv.Itoa(i)
		bee.send("JOIN " + ch)
		bee.expect(" 366 ")
		ember.send("JOIN " + ch)
		ember.expect(" 366 ")
	}
	ember.send("PRIVMSG #c0 :hello everyone")
	bee.expect("PRIVMSG #c0 :hello everyone")
	ember.send("PRIVMSG #c1 :hello everyone")
	if l := ember.expect(" 404 ember #c1 "); !strings.Contains(l, "/amsg") {
		t.Errorf("amsg answer %q", l)
	}
	ember.send("PRIVMSG #c2 :hello everyone")
	ember.expect(" 404 ember #c2 ")
	ember.send("PRIVMSG #c2 :something else")
	bee.expect("PRIVMSG #c2 :something else")
	ember.send("PRIVMSG #c2 :something else") // the same channel twice is no amsg
	bee.expect("PRIVMSG #c2 :something else")
	ember.send("PRIVMSG #c0,#c1 :to two at once")
	bee.expect("PRIVMSG #c0 :to two at once")
	ember.expect(" 404 ember #c1 ")
}
