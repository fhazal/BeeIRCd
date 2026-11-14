package server

import (
	"strings"
	"testing"
	"time"

	"github.com/fhazal/beeircd/internal/control"
)

// Hive Chat h2: the Chat tab's one-time SASL key.

const chatOn = "\n[web]\nmembers = true\nchat_url = \"https://chat.example.org\"\n"

// saslWith logs a new connection in with SASL PLAIN and reports the
// numeric that came back: 903 (success) or 904 (failure).
func saslWith(t *testing.T, s *Server, nick, account, password string) string {
	t.Helper()
	c := capLogin(t, s, nick, "sasl")
	c.send("AUTHENTICATE PLAIN")
	c.expect("AUTHENTICATE +")
	c.send("AUTHENTICATE " + saslPlain(account, password))
	for {
		l := c.next()
		switch {
		case strings.Contains(l, " 903 "):
			return "903"
		case strings.Contains(l, " 904 "):
			return "904"
		}
	}
}

func chatKeyFor(t *testing.T, s *Server, sess control.Request) *control.ChatKey {
	t.Helper()
	r := panelAsk(t, s, as(sess, control.CmdChatKey))
	if r.Chat == nil {
		t.Fatalf("chat-key: %+v", r)
	}
	return r.Chat
}

func TestChatKeySignsInOnce(t *testing.T) {
	s, _, _ := fileServer(t, chatOn)
	registered(t, s, "ember")
	registered(t, s, "other")
	m := memberSession(t, s, "ember", pw)
	if who := panelAsk(t, s, as(m, control.CmdPanelWho)); who.Oper == nil || who.Oper.Chat != "https://chat.example.org" {
		t.Fatalf("panel-who should carry the Chat address: %+v", who)
	}
	k := chatKeyFor(t, s, m)
	if k.URL != "https://chat.example.org" || k.Account != "ember" || !strings.HasPrefix(k.Key, chatKeyPrefix) {
		t.Fatalf("key: %+v", k)
	}
	if got := saslWith(t, s, "web1", "ember", k.Key); got != "903" {
		t.Fatalf("first use: %s", got)
	}
	if got := saslWith(t, s, "web2", "ember", k.Key); got != "904" {
		t.Errorf("second use: %s, want 904 (once only)", got)
	}

	// A key is for its own account only, and is spent by a wrong try.
	k2 := chatKeyFor(t, s, m)
	if got := saslWith(t, s, "web3", "other", k2.Key); got != "904" {
		t.Errorf("ember's key for other: %s", got)
	}
	if got := saslWith(t, s, "web4", "ember", k2.Key); got != "904" {
		t.Errorf("key after a wrong try: %s", got)
	}

	// Not after a minute.
	k3 := chatKeyFor(t, s, m)
	s.onLoop(func() {
		ck := s.chatKeys[k3.Key]
		ck.until = time.Now().Add(-time.Second)
		s.chatKeys[k3.Key] = ck
	})
	if got := saslWith(t, s, "web5", "ember", k3.Key); got != "904" {
		t.Errorf("old key: %s", got)
	}

	// The password still works as before.
	if got := saslWith(t, s, "web6", "ember", pw); got != "903" {
		t.Errorf("password: %s", got)
	}
}

func TestChatKeyNeedsChatURL(t *testing.T) {
	s, _, _ := fileServer(t, membersOn)
	registered(t, s, "ember")
	m := memberSession(t, s, "ember", pw)
	if who := panelAsk(t, s, as(m, control.CmdPanelWho)); who.Oper == nil || who.Oper.Chat != "" {
		t.Errorf("no chat_url, still a Chat address: %+v", who)
	}
	if r := panelAsk(t, s, as(m, control.CmdChatKey)); r.Chat != nil || !strings.Contains(r.Error, "chat_url") {
		t.Errorf("chat-key without chat_url: %+v", r)
	}
	// Without a panel session there is no key at all.
	if r := panelAsk(t, s, control.Request{Cmd: control.CmdChatKey, As: "ember", IP: "127.0.0.1", Seal: "made-up"}); r.Chat != nil {
		t.Errorf("chat-key with a made-up seal: %+v", r)
	}
}
