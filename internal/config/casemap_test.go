package config

import (
	"strings"
	"testing"
)

func TestCasemapping(t *testing.T) {
	c, err := Load(write(t, good))
	if err != nil || c.Server.Casemapping != "ascii" {
		t.Fatalf("default: %v %q", err, c.Server.Casemapping)
	}
	c, err = Load(write(t, strings.Replace(good, `sid = "0BE"`, "sid = \"0BE\"\ncasemapping = \"rfc1459\"", 1)))
	if err != nil || c.Server.Casemapping != "rfc1459" {
		t.Fatalf("rfc1459: %v", err)
	}
	if _, err := Load(write(t, strings.Replace(good, `sid = "0BE"`, "sid = \"0BE\"\ncasemapping = \"strict\"", 1))); err == nil || !strings.Contains(err.Error(), "casemapping") {
		t.Errorf("bad value: %v", err)
	}
}
