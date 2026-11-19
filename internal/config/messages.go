package config

import (
	"fmt"
	"strings"
	"time"
)

// Messages is [messages]: quit, part and ban texts the owner sets, like
// UnrealIRCd's static-quit, static-part, anti-spam-quit-message-time and
// kline-address. Everything is off by default; opers are never changed.
type Messages struct {
	// StaticQuit is shown as the quit reason of everyone who quits
	// themself, word for word. Empty: their own reason.
	StaticQuit string `toml:"static_quit"`
	// StaticPart: "yes" drops part reasons, other text replaces them.
	// Empty or "no": their own reason.
	StaticPart string `toml:"static_part"`
	// QuitMessageMinTime: someone connected for less than this quits
	// with plain "Quit". 0: off.
	QuitMessageMinTime Duration `toml:"quit_message_min_time"`
	// BanContact is added to "You are banned" so people know whom to ask.
	BanContact string `toml:"ban_contact"`
	// QuitPrefix starts the quit reason people give, kept as written
	// (a trailing space too). Empty: "Quit: "; "none": no prefix.
	QuitPrefix string `toml:"quit_prefix"`
	// RejectFull, RejectTooMany and RejectPassword replace the texts of
	// "server full", "too many connections from your IP" and a wrong
	// /OPER password. Empty: the built-in ones.
	RejectFull     string `toml:"reject_full"`
	RejectTooMany  string `toml:"reject_too_many"`
	RejectPassword string `toml:"reject_password"`
	// BanNotice is sent as a server notice to someone a ban disconnects,
	// like InspIRCd's moronbanner. Empty: none.
	BanNotice string `toml:"ban_notice"`
	// BanQuit is the quit reason others see when a ban disconnects
	// someone: empty or "no" the real one ("Banned: spam"), "yes" just
	// "Banned", any other text that text.
	BanQuit string `toml:"ban_quit"`
	// HideKilledBy shows this name instead of the oper in "Killed (<by>
	// (reason))", like InspIRCd's hidekills. Empty: the real killer.
	HideKilledBy string `toml:"hide_killed_by"`
}

// Built-in texts the reject_* settings replace.
const (
	DefaultQuitPrefix     = "Quit: "
	DefaultRejectFull     = "This server is full. Please try again later."
	DefaultRejectTooMany  = "Too many connections from your IP"
	DefaultRejectPassword = "Password incorrect"
)

// QuitPrefixText is what goes before a quit reason people give.
func (m *Messages) QuitPrefixText() string {
	switch {
	case m.QuitPrefix == "":
		return DefaultQuitPrefix
	case strings.EqualFold(strings.TrimSpace(m.QuitPrefix), "none"):
		return ""
	}
	return m.QuitPrefix
}

// FullText is the quit reason of someone refused because the server is full.
func (m *Messages) FullText() string { return orDefault(m.RejectFull, DefaultRejectFull) }

// TooManyText is the quit reason of someone over their IP's limit.
func (m *Messages) TooManyText() string { return orDefault(m.RejectTooMany, DefaultRejectTooMany) }

// PasswordText is the text of ERR_PASSWDMISMATCH for a wrong password.
func (m *Messages) PasswordText() string {
	return orDefault(m.RejectPassword, DefaultRejectPassword)
}

// BanQuitText is the quit reason others see for someone a ban removes:
// what is "Banned" or "K-Lined", reason the ban's reason.
func (m *Messages) BanQuitText(what, reason string) string {
	switch strings.ToLower(m.BanQuit) {
	case "", "no":
		return what + ": " + reason
	case "yes":
		return what
	}
	return m.BanQuit
}

// maxMessageText is the longest text a [messages] setting takes.
const maxMessageText = 300

// PartText is what a part reason becomes: reason itself when static_part
// is off, "" for "yes", else the owner's text.
func (m *Messages) PartText(reason string) string {
	switch strings.ToLower(m.StaticPart) {
	case "", "no":
		return reason
	case "yes":
		return ""
	}
	return m.StaticPart
}

// BanText adds the ban contact to a "You are banned" text, like
// UnrealIRCd's kline-address: "...: spam. Email x for more information."
func (m *Messages) BanText(text string) string {
	if m.BanContact == "" {
		return text
	}
	if !strings.HasSuffix(text, ".") && !strings.HasSuffix(text, "!") && !strings.HasSuffix(text, "?") {
		text += "."
	}
	return text + " Email " + m.BanContact + " for more information."
}

func (c *Config) checkMessages() error {
	m := &c.Messages
	for _, f := range []struct {
		key string
		v   *string
	}{{"static_quit", &m.StaticQuit}, {"static_part", &m.StaticPart}, {"ban_contact", &m.BanContact},
		{"quit_prefix", &m.QuitPrefix}, {"reject_full", &m.RejectFull}, {"reject_too_many", &m.RejectTooMany},
		{"reject_password", &m.RejectPassword}, {"ban_notice", &m.BanNotice}, {"ban_quit", &m.BanQuit},
		{"hide_killed_by", &m.HideKilledBy}} {
		if f.key != "quit_prefix" { // "Exit: " keeps its space
			*f.v = strings.TrimSpace(*f.v)
		}
		if len(*f.v) > maxMessageText || strings.ContainsAny(*f.v, "\r\n\x00") {
			return fmt.Errorf("[messages] %s: one line of at most %d characters", f.key, maxMessageText)
		}
	}
	if d := m.QuitMessageMinTime.Duration; d < 0 || d > time.Hour {
		return fmt.Errorf("[messages] quit_message_min_time must be between 0 and 1h")
	}
	return nil
}
