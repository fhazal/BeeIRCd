package services

import (
	"strings"

	"github.com/fhazal/beeircd/internal/irc"
	"github.com/fhazal/beeircd/internal/store"
)

// Extra INFO fields (Anope's ns_set_misc / cs_set_misc): [services]
// nick_fields and chan_fields name them, NickServ SET <field> [text] and
// ChanServ SET #chan <field> [text] fill them in, INFO shows them. The
// store keeps them by lower-case name.

// maxFieldLen is how long a field's text may be.
const maxFieldLen = 200

// fieldName is the name in list that opt is (any case), as the config
// spells it, or "".
func fieldName(list []string, opt string) string {
	for _, n := range list {
		if strings.EqualFold(n, opt) {
			return n
		}
	}
	return ""
}

// cleanField is text without control characters (colours, bold, ...),
// cut to maxFieldLen.
func cleanField(text string) string {
	text = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, text)
	return irc.Truncate(strings.TrimSpace(text), maxFieldLen)
}

// infoFields shows vals in INFO, in the order list names them; values of
// names not in list stay hidden.
func infoFields(list []string, vals map[string]string, say func(format string, a ...any)) {
	for _, n := range list {
		if v := vals[strings.ToLower(n)]; v != "" {
			say("  %-13s %s", n, v)
		}
	}
}

// nsSetField is NickServ SET (or SASET) <field> [text] on account a.
func (s *Services) nsSetField(c *Client, a *store.Account, name string, args []string) bool {
	text := cleanField(strings.Join(args, " "))
	if err := s.store.SetAccountField(a.ID, strings.ToLower(name), text); err != nil {
		s.dbErr(c, s.nickServ, err)
		return false
	}
	if text == "" {
		s.nsLog(c, a.ID, "to unset %s of %s", strings.ToUpper(name), a.Name)
		s.ns(c, "%s for %s unset.", name, a.Name)
	} else {
		s.nsLog(c, a.ID, "to change %s of %s to %s", strings.ToUpper(name), a.Name, text)
		s.ns(c, "%s for %s set to %s.", name, a.Name, text)
	}
	return true
}

// csSetField is ChanServ SET #chan <field> [text]; override is an oper
// acting without access.
func (s *Services) csSetField(c *Client, reg *store.Channel, override bool, name, val string) {
	text := cleanField(val)
	if err := s.store.SetChannelField(reg.ID, strings.ToLower(name), text); err != nil {
		s.dbErr(c, s.chanServ, err)
		return
	}
	s.csUsed(c, reg)
	if text == "" {
		s.csLog(c, reg.Name, override, "to unset %s", strings.ToUpper(name))
		s.cs(c, "%s of %s unset.", name, reg.Name)
	} else {
		s.csLog(c, reg.Name, override, "to change %s to %s", strings.ToUpper(name), text)
		s.cs(c, "%s of %s is now: %s", name, reg.Name, text)
	}
}
