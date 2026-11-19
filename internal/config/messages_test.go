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

// The newer [messages] keys keep today's texts when left out, quit_prefix
// keeps its trailing space and "none", and bad values are refused.
func TestMessagesTextsConfig(t *testing.T) {
	c, err := Load(write(t, good))
	if err != nil {
		t.Fatal(err)
	}
	m := c.Messages
	if m.QuitPrefixText() != "Quit: " || m.FullText() != "This server is full. Please try again later." ||
		m.TooManyText() != "Too many connections from your IP" || m.PasswordText() != "Password incorrect" {
		t.Errorf("defaults changed: %q %q %q %q", m.QuitPrefixText(), m.FullText(), m.TooManyText(), m.PasswordText())
	}
	if m.BanNotice != "" || m.HideKilledBy != "" || m.BanQuitText("Banned", "spam") != "Banned: spam" || m.BanQuitText("K-Lined", "x") != "K-Lined: x" {
		t.Errorf("ban texts not off by default: %+v", m)
	}

	c, err = Load(write(t, good+"\n[messages]\nquit_prefix = \"Exit: \"\nreject_full = \" Full up \"\nreject_too_many = \"Too many\"\nreject_password = \"Nope\"\nban_notice = \"Read the rules\"\nban_quit = \"yes\"\nhide_killed_by = \" OperServ \"\n"))
	if err != nil {
		t.Fatal(err)
	}
	m = c.Messages
	if m.QuitPrefix != "Exit: " || m.QuitPrefixText() != "Exit: " {
		t.Errorf("quit_prefix lost its space: %q", m.QuitPrefix)
	}
	if m.FullText() != "Full up" || m.TooManyText() != "Too many" || m.PasswordText() != "Nope" || m.BanNotice != "Read the rules" || m.HideKilledBy != "OperServ" {
		t.Errorf("read %+v", m)
	}
	if m.BanQuitText("Banned", "spam") != "Banned" || m.BanQuitText("K-Lined", "spam") != "K-Lined" {
		t.Error("ban_quit yes kept the reason")
	}
	m.BanQuit = "Gone"
	if m.BanQuitText("Banned", "spam") != "Gone" {
		t.Error("ban_quit text not used")
	}
	m.BanQuit = "No"
	if m.BanQuitText("Banned", "spam") != "Banned: spam" {
		t.Error("ban_quit no changed the reason")
	}
	for _, none := range []string{"none", "NONE"} {
		m.QuitPrefix = none
		if m.QuitPrefixText() != "" {
			t.Errorf("quit_prefix %q = %q", none, m.QuitPrefixText())
		}
	}

	for _, bad := range []string{
		`quit_prefix = "a\nb"`,
		"reject_full = \"" + strings.Repeat("x", 301) + "\"",
		`reject_too_many = "a\rb"`,
		"reject_password = \"" + strings.Repeat("x", 301) + "\"",
		`ban_notice = "a\nb"`,
		"ban_quit = \"" + strings.Repeat("x", 301) + "\"",
		`hide_killed_by = "a\nb"`,
	} {
		if _, err := Load(write(t, good+"\n[messages]\n"+bad+"\n")); err == nil || !strings.Contains(err.Error(), "[messages]") {
			t.Errorf("%s: %v", bad, err)
		}
	}
}
