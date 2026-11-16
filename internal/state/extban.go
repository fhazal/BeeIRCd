package state

import (
	"strconv"
	"strings"

	"github.com/fhazal/beeircd/internal/irc"
)

// Stop is something a +b entry stops a matching user doing (and a +e
// entry lets them do again).
type Stop uint16

const (
	StopJoin Stop = 1 << iota
	StopTalk
	StopNick
	StopPartMsg
	StopCTCP
	StopNotice
	StopColor
	StripColor
	StopKick
	StopInvite
)

// A plain ban (and a matching extban) stops joining, talking and nick
// changes, like UnrealIRCd and InspIRCd.
const stopBan = StopJoin | StopTalk | StopNick

var actStops = map[string]Stop{
	"quiet": StopTalk, "join": StopJoin, "nickchange": StopNick, "partmsg": StopPartMsg,
	"noctcp": StopCTCP, "nonotice": StopNotice, "blockcolor": StopColor, "stripcolor": StripColor,
	"nokick": StopKick, "blockinvite": StopInvite,
}

// entry reads a list entry: what it stops and the mask that says who.
// ~t is looked through. Special extbans (~T ~F ~i and the bypass ~m)
// stop nothing here: they have their own checks.
func entry(mask string) (Stop, string) {
	e, arg := irc.SplitExtban(mask)
	switch {
	case e == nil:
		return stopBan, mask
	case e.Letter == 't':
		_, in, _ := strings.Cut(arg, ":")
		return entry(in)
	case e.Letter == 'f':
		_, in, _ := strings.Cut(arg, ":")
		return stopBan, in
	case e.Kind == irc.ExtMatch:
		return stopBan, mask
	case e.Kind == irc.ExtAct:
		return actStops[e.Name], arg
	}
	return 0, ""
}

// MatchUser reports whether who (a nick!user@host mask or a matching
// extban, as in list entries) covers u.
func MatchUser(who string, u *User) bool {
	e, arg := irc.SplitExtban(who)
	if e == nil {
		for _, m := range u.Masks() {
			if irc.Match(who, m) {
				return true
			}
		}
		return false
	}
	switch e.Name {
	case "account":
		if arg == "0" {
			return u.Account == ""
		}
		return u.Account != "" && irc.Match(arg, u.Account)
	case "unauthed":
		return u.Account == "" && MatchUser(arg, u)
	case "realname":
		return irc.Match(arg, u.Realname)
	case "channel":
		name := strings.TrimLeft(arg, "~&@%+")
		ch := u.Channels[irc.Fold(name)]
		if ch == nil {
			return false
		}
		if len(arg) == len(name) {
			return true
		}
		mem := ch.Member(u)
		return mem != nil && mem.AtLeast(rankOfPrefix(arg[0]))
	case "operclass":
		return u.HasMode('o') && u.OperClass != "" && irc.Match(arg, u.OperClass)
	case "oper":
		return u.HasMode('o') && u.OperName != "" && irc.Match(arg, u.OperName)
	case "certfp":
		return u.CertFP != "" && irc.Match(arg, u.CertFP)
	case "country":
		return u.Country != "" && irc.Match(arg, u.Country)
	case "asn":
		return u.ASN != 0 && arg == strconv.FormatUint(uint64(u.ASN), 10)
	case "security-group":
		for _, g := range u.Groups {
			if strings.EqualFold(g, arg) {
				return true
			}
		}
		return false
	case "bot":
		return u.HasMode('B') && MatchUser(arg, u)
	case "gateway":
		return u.Gateway != "" && irc.Match(arg, u.Gateway)
	case "server":
		return irc.Match(arg, u.Server)
	case "realmask":
		for _, m := range u.Masks() {
			if irc.Match(arg, m+"+"+u.Realname) {
				return true
			}
		}
	}
	return false
}

func rankOfPrefix(p byte) Rank {
	for _, r := range Ranks {
		if r.Prefix == p {
			return r.Rank
		}
	}
	return RankVoice
}

// MatchBan matches a channel list mask (with extbans, see irc.Extbans)
// against u: whether it covers u, and whether it only stops talking
// (a mute: ~m, ~q).
func MatchBan(mask string, u *User) (match, mute bool) {
	stop, who := entry(mask)
	if stop == 0 || !MatchUser(who, u) {
		return false, false
	}
	return true, stop == StopTalk
}

// lists are the entries of list, with the ones ~i:#chan inherits from
// #chan's same list (one level).
func (c *Channel) lists(list byte) []ListEntry {
	own := c.Lists[list]
	var out []ListEntry
	for _, e := range own {
		if !strings.HasPrefix(e.Mask, "~i:") {
			continue
		}
		if out == nil {
			out = append([]ListEntry(nil), own...)
		}
		if c.net == nil {
			continue
		}
		if from := c.net.Channel(e.Mask[3:]); from != nil && from != c {
			for _, f := range from.Lists[list] {
				if !strings.HasPrefix(f.Mask, "~i:") {
					out = append(out, f)
				}
			}
		}
	}
	if out == nil {
		return own
	}
	return out
}

// Stops reports whether a +b entry stops u doing what, and no +e entry
// lets them (a plain or matching exception lets them do everything; an
// acting one, like +e ~q:mask, only that).
func (c *Channel) Stops(u *User, what Stop) bool {
	for _, e := range c.lists('b') {
		if stop, who := entry(e.Mask); stop&what != 0 && MatchUser(who, u) {
			return !c.excepted(u, what)
		}
	}
	return false
}

func (c *Channel) excepted(u *User, what Stop) bool {
	for _, e := range c.Lists['e'] {
		if stop, who := entry(e.Mask); stop&what != 0 && MatchUser(who, u) {
			return true
		}
	}
	return false
}

// Matches reports whether any plain or matching entry of list covers u
// (for +I, invite exceptions; ~i:#chan entries count #chan's too).
func (c *Channel) Matches(list byte, u *User) bool {
	for _, e := range c.lists(list) {
		if stop, who := entry(e.Mask); stop == stopBan && MatchUser(who, u) {
			return true
		}
	}
	return false
}

// IsBanned reports whether u is banned from joining and not excepted.
func (c *Channel) IsBanned(u *User) bool { return c.Stops(u, StopJoin) }

// IsMuted reports whether a ban or a mute stops u talking, and no
// exception lets them.
func (c *Channel) IsMuted(u *User) bool { return c.Stops(u, StopTalk) }

// Bypasses reports whether a +e ~m:typ:mask (UnrealIRCd msgbypass) lets
// u skip typ: external, censor, moderated, color or notice.
func (c *Channel) Bypasses(u *User, typ string) bool {
	for _, e := range c.Lists['e'] {
		x, arg := irc.SplitExtban(e.Mask)
		if x == nil || x.Name != "msgbypass" {
			continue
		}
		t, who, _ := strings.Cut(arg, ":")
		if t == typ && MatchUser(who, u) {
			return true
		}
	}
	return false
}

// FloodExempt reports whether a +e ~F:mask lets u skip the +f flood
// limits.
func (c *Channel) FloodExempt(u *User) bool {
	for _, e := range c.Lists['e'] {
		if x, who := irc.SplitExtban(e.Mask); x != nil && x.Name == "flood" && MatchUser(who, u) {
			return true
		}
	}
	return false
}

// TextBan checks text against the channel's ~T:block:pattern and
// ~T:censor:pattern entries: blocked, or the text with censored words
// replaced.
func (c *Channel) TextBan(text, replace string) (string, bool) {
	for _, e := range c.Lists['b'] {
		x, arg := irc.SplitExtban(e.Mask)
		if x == nil || x.Name != "text" {
			continue
		}
		act, pat, _ := strings.Cut(arg, ":")
		switch act {
		case "block":
			if irc.Match(pat, text) {
				return "", true
			}
		case "censor":
			text = censorWord(text, pat, replace)
		}
	}
	return text, false
}

// censorWord replaces each word of text that matches pat (a glob) with
// replace.
func censorWord(text, pat, replace string) string {
	words := strings.Split(text, " ")
	changed := false
	for i, w := range words {
		if w != "" && irc.Match(pat, w) {
			words[i], changed = replace, true
		}
	}
	if !changed {
		return text
	}
	return strings.Join(words, " ")
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
		to, who, _ := strings.Cut(mask[3:], ":")
		if MatchUser(who, u) {
			return to
		}
	}
	return ""
}
