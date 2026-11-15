package state

import (
	"strings"

	"github.com/fhazal/beeircd/internal/irc"
)

// MatchBan matches a channel list mask (with extbans, see
// irc.ExtbanLetters) against u. mute reports a ~m/~q entry, which stops
// talking but not joining.
func MatchBan(mask string, u *User) (match, mute bool) {
	if strings.HasPrefix(mask, "~t:") {
		if _, inner, ok := strings.Cut(mask[3:], ":"); ok {
			mask = inner
		}
	}
	if len(mask) >= 3 && mask[0] == '~' && mask[2] == ':' {
		arg := mask[3:]
		switch mask[1] {
		case 'a':
			if arg == "0" {
				return u.Account == "", false
			}
			return u.Account != "" && irc.Match(arg, u.Account), false
		case 'r':
			return irc.Match(arg, u.Realname), false
		case 'f':
			_, inner, _ := strings.Cut(arg, ":")
			m, _ := MatchBan(inner, u)
			return m, false
		case 'm', 'q':
			m, _ := MatchBan(arg, u)
			return m, true
		}
		return false, false
	}
	for _, m := range u.Masks() {
		if irc.Match(mask, m) {
			return true, false
		}
	}
	return false, false
}

// BanForward is the channel a matching ~f:#chan:mask ban sends u to, or
// "" when no forward ban matches (timed ~t: forwards count too).
func (c *Channel) BanForward(u *User) string {
	for _, e := range c.Lists['b'] {
		mask := e.Mask
		if strings.HasPrefix(mask, "~t:") {
			if _, inner, ok := strings.Cut(mask[3:], ":"); ok {
				mask = inner
			}
		}
		if !strings.HasPrefix(mask, "~f:") {
			continue
		}
		if m, _ := MatchBan(mask, u); m {
			to, _, _ := strings.Cut(mask[3:], ":")
			return to
		}
	}
	return ""
}
