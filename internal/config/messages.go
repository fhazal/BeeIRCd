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
	}{{"static_quit", &m.StaticQuit}, {"static_part", &m.StaticPart}, {"ban_contact", &m.BanContact}} {
		*f.v = strings.TrimSpace(*f.v)
		if len(*f.v) > maxMessageText || strings.ContainsAny(*f.v, "\r\n\x00") {
			return fmt.Errorf("[messages] %s: one line of at most %d characters", f.key, maxMessageText)
		}
	}
	if d := m.QuitMessageMinTime.Duration; d < 0 || d > time.Hour {
		return fmt.Errorf("[messages] quit_message_min_time must be between 0 and 1h")
	}
	return nil
}
