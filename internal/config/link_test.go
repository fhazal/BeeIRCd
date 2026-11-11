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
