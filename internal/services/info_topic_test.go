package services

import (
	"strings"
	"testing"
	"time"

	"github.com/fhazal/beeircd/internal/state"
)

// ChanServ INFO shows the kept topic and who set it; on a secret channel
// only people with the INFO privilege and opers see it.
func TestChanServInfoTopic(t *testing.T) {
	svc, h := newServices(t)
	ns, cs := h.net.UserByNick("NickServ"), h.net.UserByNick("ChanServ")
	bee := helpUser(t, h, "1LFAAAAAA", "Bee")
	svc.Message(bee.UID, ns, "REGISTER honeypot42")
	h.waitFor("Bee", "registered")
	h.Emit(&state.Join{UID: bee.UID, Channel: "#hive", TS: 1})
	svc.Flush()
	svc.Message(bee.UID, cs, "REGISTER #hive the hive")
	h.waitFor("Bee", "You are its Founder")
	noticeTo(h, "Bee")

	guest := helpUser(t, h, "1LFAAAAAB", "Kumbang")
	svc.Message(guest.UID, cs, "INFO #hive")
	if got := noticeTo(h, "Kumbang"); strings.Contains(got, "Topic") {
		t.Errorf("no topic kept, INFO still has a topic line: %q", got)
	}

	reg, _ := svc.store.ChannelByName("#hive")
	reg.Topic, reg.TopicBy, reg.TopicAt = "Welcome to #hive", "Bee", time.Date(2026, 11, 18, 14, 5, 0, 0, time.UTC)
	if err := svc.store.SaveChannel(reg); err != nil {
		t.Fatal(err)
	}
	svc.Message(guest.UID, cs, "INFO #hive")
	got := noticeTo(h, "Kumbang")
	if !strings.Contains(got, "  Topic         Welcome to #hive") || !strings.Contains(got, "  Topic by      Bee, 18 Nov 2026 14:05 UTC") {
		t.Errorf("INFO for everyone: %q", got)
	}

	h.net.Channel("#hive").Modes['s'] = true
	svc.Message(guest.UID, cs, "INFO #hive")
	if got := noticeTo(h, "Kumbang"); strings.Contains(got, "Welcome") {
		t.Errorf("secret channel, guest sees the topic: %q", got)
	}
	svc.Message(bee.UID, cs, "INFO #hive")
	if got := noticeTo(h, "Bee"); !strings.Contains(got, "Welcome to #hive") {
		t.Errorf("secret channel, founder misses the topic: %q", got)
	}

	svc.Message(bee.UID, ns, "SET LANGUAGE ms")
	noticeTo(h, "Bee")
	svc.Message(bee.UID, cs, "INFO #hive")
	if got := noticeTo(h, "Bee"); !strings.Contains(got, "  Topik         Welcome to #hive") || !strings.Contains(got, "  Topik oleh    Bee, ") {
		t.Errorf("INFO in Malay: %q", got)
	}
}

// MemoServ LIST says its times are UTC.
func TestMemoListSaysUTC(t *testing.T) {
	svc, h := newServices(t)
	ns, ms := h.net.UserByNick("NickServ"), h.net.UserByNick("MemoServ")
	bee := helpUser(t, h, "1LFAAAAAA", "Bee")
	svc.Message(bee.UID, ns, "REGISTER honeypot42")
	h.waitFor("Bee", "registered")
	lebah := helpUser(t, h, "1LFAAAAAB", "Lebah")
	svc.Message(lebah.UID, ns, "REGISTER honeypot42")
	h.waitFor("Lebah", "registered")
	svc.Message(lebah.UID, ms, "SEND Bee hello there")
	svc.Flush()
	noticeTo(h, "Bee")
	svc.Message(bee.UID, ms, "LIST")
	if got := noticeTo(h, "Bee"); !strings.Contains(got, "Sent (UTC)") || !strings.Contains(got, "Lebah") {
		t.Errorf("MS LIST: %q", got)
	}
	svc.Message(bee.UID, ns, "SET LANGUAGE ms")
	noticeTo(h, "Bee")
	svc.Message(bee.UID, ms, "LIST")
	if got := noticeTo(h, "Bee"); !strings.Contains(got, "Dihantar (UTC)") {
		t.Errorf("MS LIST in Malay: %q", got)
	}
}

// A ban type outside 0-3 (from an old Anope import) made INFO panic; it
// shows what such a channel bans with: *!*@host, as type 2.
func TestChanServInfoBadBanType(t *testing.T) {
	svc, h := newServices(t)
	cs := h.net.UserByNick("ChanServ")
	bee := regChannel(t, svc, h, "1LFAAAAAA", "Bee")
	noticeTo(h, "Bee")
	reg, _ := svc.store.ChannelByName("#hive")
	reg.BanType = 7
	if err := svc.store.SaveChannel(reg); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("INFO panicked: %v", r)
		}
	}()
	svc.Message(bee.UID, cs, "INFO #hive")
	if got := noticeTo(h, "Bee"); !strings.Contains(got, "  Ban type      7 (*!*@host)") {
		t.Errorf("INFO: %q", got)
	}
}
