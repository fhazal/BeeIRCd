package server

import (
	"testing"
	"time"

	"github.com/fhazal/beeircd/internal/config"
	"github.com/fhazal/beeircd/internal/store"
)

func draftsOn(c *config.Config) { c.Server.IRCv3Drafts = true }

func TestReadMarker(t *testing.T) {
	s := testServer(t, draftsOn)
	pc := capLogin(t, s, "Bee", readMarkerCap)
	pc.send("NS REGISTER " + pw)
	pc.expect(":irc.test.local MODE Bee +r")
	phone := capLogin(t, s, "Bee_", readMarkerCap)
	phone.send("NS IDENTIFY Bee " + pw)
	phone.expect(" 900 Bee_ ")

	pc.send("JOIN #hive")
	pc.expect("MARKREAD #hive *")
	pc.expect(" 366 Bee #hive ")

	phone.send("MARKREAD #hive timestamp=2026-11-16T01:02:03.456Z")
	phone.expect(":irc.test.local MARKREAD #hive timestamp=2026-11-16T01:02:03.456Z")
	pc.expect(":irc.test.local MARKREAD #hive timestamp=2026-11-16T01:02:03.456Z") // the other device follows

	phone.send("MARKREAD #hive timestamp=2026-11-15T00:00:00.000Z") // older: the stored one comes back
	phone.expect("MARKREAD #hive timestamp=2026-11-16T01:02:03.456Z")
	pc.send("MARKREAD #hive")
	pc.expect("MARKREAD #hive timestamp=2026-11-16T01:02:03.456Z")
	phone.send("JOIN #hive")
	phone.expect("MARKREAD #hive timestamp=2026-11-16T01:02:03.456Z")

	pc.send("MARKREAD")
	pc.expect("FAIL MARKREAD NEED_MORE_PARAMS :Missing parameters")
	pc.send("MARKREAD #hive yesterday")
	pc.expect("FAIL MARKREAD INVALID_PARAMS yesterday :Invalid timestamp")

	// Without an account the marker stays with the connection.
	guest := capLogin(t, s, "guest", readMarkerCap)
	guest.send("MARKREAD nova timestamp=2026-11-16T05:00:00Z")
	guest.expect("MARKREAD nova timestamp=2026-11-16T05:00:00.000Z")

	// Kept in the database for the next start.
	var ms []store.ReadMarker
	var err error
	for end := time.Now().Add(2 * time.Second); time.Now().Before(end) && len(ms) == 0; time.Sleep(10 * time.Millisecond) {
		ms, err = s.store.ReadMarkers() // written off the loop
	}
	if err != nil || len(ms) != 1 || ms[0].Account != "bee" || ms[0].Target != "#hive" {
		t.Errorf("stored markers %+v, %v", ms, err)
	}
}

func TestDraftsSwitch(t *testing.T) {
	s := testServer(t, nil)
	c := raw(t, s)
	c.send("CAP LS 302")
	if l := c.expect("CAP * LS :"); contains(l, readMarkerCap) {
		t.Errorf("drafts offered while off: %s", l)
	}
	c.send("CAP REQ " + readMarkerCap)
	c.expect("CAP * NAK :" + readMarkerCap)

	on := capLogin(t, s, "Bee", "cap-notify")
	s.do(func() { s.cfg.Server.IRCv3Drafts = true; s.draftsChanged(true) })
	on.expect("CAP Bee NEW :" + readMarkerCap)
	on.send("CAP REQ " + readMarkerCap)
	on.expect("CAP Bee ACK :" + readMarkerCap)
	s.do(func() { s.cfg.Server.IRCv3Drafts = false; s.draftsChanged(false) })
	on.expect("CAP Bee DEL :" + readMarkerCap)
	on.send("MARKREAD #a")
	on.expect(" 421 Bee MARKREAD ")
}

// do runs f on s's loop and waits for it.
func (s *Server) do(f func()) {
	done := make(chan struct{})
	s.post(func() { f(); close(done) })
	<-done
}

// A marker set on the leaf reaches the account's connection on the hub.
func TestLinkReadMarker(t *testing.T) {
	hub, leaf := linkedPair(t)
	for _, n := range []*linkNode{hub, leaf} {
		n.s.do(func() { n.s.cfg.Server.IRCv3Drafts = true })
	}
	pc := capLogin(t, hub.s, "Bee", readMarkerCap)
	pc.send("NS REGISTER " + pw)
	pc.expect(" MODE Bee +r")
	phone := capLogin(t, leaf.s, "Bee_", readMarkerCap)
	hub.seen(t, "Bee_")
	phone.send("NS IDENTIFY Bee " + pw)
	phone.expect(" 900 Bee_ ")
	waitLink(t, "Bee_ identified on the leaf", func() bool { u := leaf.user("Bee_"); return u != nil && u.Account == "Bee" })

	phone.send("MARKREAD Nova timestamp=2026-11-16T07:00:00.000Z")
	phone.expect("MARKREAD Nova timestamp=2026-11-16T07:00:00.000Z")
	pc.expect("MARKREAD Nova timestamp=2026-11-16T07:00:00.000Z")
	pc.send("MARKREAD Nova timestamp=2026-11-16T08:00:00.000Z")
	phone.expect("MARKREAD Nova timestamp=2026-11-16T08:00:00.000Z")
}
