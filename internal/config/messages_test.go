package config

import (
	"strings"
	"testing"
	"time"
)

// [messages] is off when left out, trims its texts and refuses bad values.
func TestMessagesConfig(t *testing.T) {
	c, err := Load(write(t, good))
	if err != nil {
		t.Fatal(err)
	}
	m := c.Messages
	if m.StaticQuit != "" || m.StaticPart != "" || m.BanContact != "" || m.QuitMessageMinTime.Duration != 0 {
		t.Errorf("not off by default: %+v", m)
	}
	if m.PartText("bye") != "bye" || m.BanText("You are banned") != "You are banned" {
		t.Error("off still changes texts")
	}

	c, err = Load(write(t, good+"\n[messages]\nstatic_quit = \" Client exited \"\nstatic_part = \"yes\"\nquit_message_min_time = \"5m\"\nban_contact = \"kline@example.net\"\n"))
	if err != nil {
		t.Fatal(err)
	}
	m = c.Messages
	if m.StaticQuit != "Client exited" || m.QuitMessageMinTime.Duration != 5*time.Minute {
		t.Errorf("read %+v", m)
	}
	if m.PartText("bye") != "" {
		t.Error("static_part yes kept the reason")
	}
	m.StaticPart = "Left"
	if m.PartText("") != "Left" || m.PartText("bye") != "Left" {
		t.Error("static_part text not used")
	}
	m.StaticPart = "No"
	if m.PartText("bye") != "bye" {
		t.Error("static_part no changed the reason")
	}
	if got := m.BanText("You are banned: spam"); got != "You are banned: spam. Email kline@example.net for more information." {
		t.Errorf("BanText = %q", got)
	}
	if got := m.BanText("You are banned: spam?"); got != "You are banned: spam? Email kline@example.net for more information." {
		t.Errorf("BanText = %q", got)
	}

	for _, bad := range []string{
		"static_quit = \"" + strings.Repeat("x", 301) + "\"",
		"static_part = \"a\\nb\"",
		"ban_contact = \"" + strings.Repeat("x", 301) + "\"",
		"quit_message_min_time = \"2h\"",
		"quit_message_min_time = \"-1s\"",
	} {
		if _, err := Load(write(t, good+"\n[messages]\n"+bad+"\n")); err == nil || !strings.Contains(err.Error(), "[messages]") {
			t.Errorf("%s: %v", bad, err)
		}
	}
}
