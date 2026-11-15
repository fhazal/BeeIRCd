package server

import (
	"strings"
	"testing"
)

func TestPreAway(t *testing.T) {
	s := testServer(t, draftsOn)
	c := raw(t, s)
	c.nick = "Bee"
	c.send("CAP LS 302")
	c.expect("CAP * LS")
	c.send("AWAY :too early")
	c.expect(" 451 ")
	c.send("CAP REQ " + preAwayCap)
	c.expect("CAP * ACK :" + preAwayCap)
	c.send("AWAY :on my phone")
	c.send("NICK Bee")
	c.send("USER bee 0 * :Bee")
	c.send("CAP END")
	c.expect(" 001 Bee ")
	c.expect(" 306 Bee ")

	nova := login(t, s, "nova")
	nova.send("WHOIS Bee")
	nova.expect(" 301 nova Bee :on my phone")
}

func TestExtendedISupport(t *testing.T) {
	s := testServer(t, draftsOn)
	c := raw(t, s)
	c.nick = "Bee"
	c.send("CAP LS 302")
	c.expect("CAP * LS")
	c.send("ISUPPORT")
	c.expect(" 451 ")
	c.send("CAP REQ :batch " + extISupportCap)
	c.expect("CAP * ACK")
	c.send("ISUPPORT")
	open := c.expect(" BATCH +")
	if !contains(open, " draft/isupport") {
		t.Fatalf("batch: %s", open)
	}
	id := open[strings.Index(open, "+")+1 : strings.Index(open, " draft/")]
	l := c.expect(" 005 * ")
	if !strings.HasPrefix(l, "@batch="+id+" ") || !contains(l, "NETWORK=") && !contains(l, "CASEMAPPING=") {
		t.Errorf("005 before registration: %s", l)
	}
	c.expect(" BATCH -" + id)
	c.send("NICK Bee")
	c.send("USER bee 0 * :Bee")
	c.send("CAP END")
	c.expect(" 001 Bee ")
	c.expect(" draft/isupport")
	c.expect(" 005 Bee ")
}
