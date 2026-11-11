package config

import (
	"strings"
	"testing"
)

func TestLinkConfig(t *testing.T) {
	c, err := Load(write(t, good))
	if err != nil {
		t.Fatal(err)
	}
	if c.Link.Role != "standalone" || c.Link.Port != 0 || c.Link.Linked() {
		t.Errorf("default link %+v", c.Link)
	}
	hub, err := Load(write(t, good+"\n[link]\nrole = \"hub\"\n"))
	if err != nil {
		t.Fatal(err)
	}
	if hub.Link.Port != 7000 || !hub.Link.Linked() {
		t.Errorf("hub %+v", hub.Link)
	}
	for want, body := range map[string]string{
		"must be standalone, hub or leaf": good + "\n[link]\nrole = \"server\"\n",
		"already a [[listen]] port":       good + "\n[link]\nrole = \"leaf\"\nport = 6697\n",
		"must be a port number":           good + "\n[link]\nrole = \"hub\"\nport = 70000\n",
	} {
		if _, err := Load(write(t, body)); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("want %q, got %v", want, err)
		}
	}
}

const fpA = "7Q2bJkp3cLbyfLRQYn+Y5Nn0XyWrr+0oXlzR8Cj3KeY="

func TestLinkServers(t *testing.T) {
	hub := good + "\n[link]\nrole = \"hub\"\n"
	block := func(name, extra string) string {
		return "\n[[link.server]]\nname = \"" + name + "\"\nfingerprint = \"" + fpA + "\"\n" + extra
	}
	c, err := Load(write(t, hub+block("leaf.example.net", "host = \"10.0.0.2\"\nautoconnect = true\n")))
	if err != nil {
		t.Fatal(err)
	}
	s := c.Link.Server("LEAF.example.net")
	if s == nil || s.Port != 7000 || s.SendQMB != 20 || !s.Autoconnect || s.Host != "10.0.0.2" {
		t.Fatalf("server %+v", s)
	}
	if !strings.HasSuffix(c.Link.Cert, "link-cert.pem") || !strings.HasSuffix(c.Link.Key, "link-key.pem") {
		t.Errorf("cert %q key %q", c.Link.Cert, c.Link.Key)
	}
	leaf := "leaf.example.net"
	for want, body := range map[string]string{
		"need [link] role hub or leaf": good + block(leaf, ""),
		"fingerprint must be":          hub + "\n[[link.server]]\nname = \"leaf.example.net\"\nfingerprint = \"abc\"\n",
		"autoconnect needs a host":     hub + block(leaf, "autoconnect = true\n"),
		"own name":                     hub + block("irc.beenet.org", ""),
		"there twice":                  hub + block(leaf, "") + block("LEAF.example.net", ""),
		"may not start with :":         hub + block(leaf, "password = \":x\"\n"),
		"server name like":             hub + block("leaf", ""),
		"sendq_mb must be":             hub + block(leaf, "sendq_mb = 5000\n"),
		"address must be an IP":        good + "\n[link]\nrole = \"hub\"\naddress = \"localhost\"\n",
	} {
		if _, err := Load(write(t, body)); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("want %q, got %v", want, err)
		}
	}
}
