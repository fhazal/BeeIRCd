package server

import (
	"strings"
	"time"
	"unicode"

	"github.com/fhazal/beeircd/internal/irc"
	"github.com/fhazal/beeircd/internal/state"
)

// More protection against spam, each off until switched on:
// [antirandom] (random-looking nick, username and real name),
// [antimixedutf8] (alphabets mixed inside words), [targetfloodprot] (too
// many messages to one channel or person) from UnrealIRCd, and
// [blockamsg] (one text to many channels) from InspIRCd.

// ---- antirandom ----

// rarePairs are letter pairs that hardly ever occur in names and words.
var rarePairs = func() map[[2]byte]bool {
	m := map[[2]byte]bool{}
	for _, p := range strings.Fields("bx cj cv cx dx fq fx gq gx hx jc jf jg jq js jv jw jx jz kq kx mx px pz " +
		"qb qc qd qf qg qh qj qk ql qm qn qp qr qs qt qv qw qx qy qz sx vb vf vh vj vm vp vq vt vw vx wx xj xx zj zq zx") {
		m[[2]byte{p[0], p[1]}] = true
	}
	return m
}()

// randomScore says how random text looks: long runs of digits, vowels or
// consonants and letter pairs that don't occur in real names add up.
func randomScore(text string) int {
	score := 0
	digits, vowels, cons := 0, 0, 0
	maxD, maxV, maxC := 0, 0, 0
	prev := byte(0)
	for i := 0; i < len(text); i++ {
		ch := text[i]
		if ch >= 'A' && ch <= 'Z' {
			ch += 'a' - 'A'
		}
		letter := ch >= 'a' && ch <= 'z'
		switch {
		case ch >= '0' && ch <= '9':
			digits, vowels, cons = digits+1, 0, 0
		case strings.IndexByte("aeiouy", ch) >= 0:
			digits, vowels, cons = 0, vowels+1, 0
		case letter:
			digits, vowels, cons = 0, 0, cons+1
		default:
			digits, vowels, cons = 0, 0, 0
		}
		maxD, maxV, maxC = max(maxD, digits), max(maxV, vowels), max(maxC, cons)
		if letter && rarePairs[[2]byte{prev, ch}] {
			score++
		}
		prev = ch
	}
	if maxD >= 5 {
		score += 5 + maxD - 5
	}
	if maxV >= 4 {
		score += 4 + maxV - 4
	}
	if maxC >= 5 {
		score += 4 + maxC - 5
	}
	return score
}

// antiRandom checks someone who is about to finish connecting; the
// answer is why they are refused, "" when they may come in.
func (s *Server) antiRandom(u *state.User, now time.Time) string {
	a := s.cfg.AntiRandom
	if !a.Enabled || s.inAnyGroup(u, a.ExceptGroups(), now) {
		return ""
	}
	score := randomScore(u.Nick) + randomScore(strings.TrimPrefix(u.TrueIdent(), "~")) + randomScore(u.Realname)
	if score < a.Limit() {
		return ""
	}
	if a.Warn() {
		s.snotice("[antirandom] would have refused %s (%s@%s) [%s], real name '%s': score %d", u.Nick, u.TrueIdent(), u.RealHost, u.IP, u.Realname, score)
		return ""
	}
	s.snotice("[antirandom] refused %s (%s@%s) [%s], real name '%s': score %d", u.Nick, u.TrueIdent(), u.RealHost, u.IP, u.Realname, score)
	return a.Message()
}

// ---- antimixedutf8 ----

// scriptOf is the alphabet r belongs to, 0 for digits, spaces, signs and
// emoji. Chinese, Japanese and Korean writing count as one: they mix.
func scriptOf(r rune) int {
	switch {
	case r < 0x80:
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' {
			return 1
		}
		return 0
	case unicode.Is(unicode.Latin, r):
		return 1
	case unicode.Is(unicode.Cyrillic, r):
		return 2
	case unicode.Is(unicode.Greek, r):
		return 3
	case unicode.Is(unicode.Arabic, r):
		return 4
	case unicode.Is(unicode.Hebrew, r):
		return 5
	case unicode.In(r, unicode.Han, unicode.Hiragana, unicode.Katakana, unicode.Hangul):
		return 6
	case unicode.Is(unicode.Thai, r):
		return 7
	case unicode.Is(unicode.Devanagari, r):
		return 8
	case unicode.IsLetter(r):
		return 9
	}
	return 0
}

// mixedScore counts changes of alphabet in text: 1 each, and 1 more when
// it happens inside a word.
func mixedScore(text string) int {
	score, last, inWord := 0, 0, false
	for _, r := range text {
		sc := scriptOf(r)
		if sc == 0 {
			inWord = false
			continue
		}
		if last != 0 && sc != last {
			score++
			if inWord {
				score++
			}
		}
		last, inWord = sc, true
	}
	return score
}

// ---- targetfloodprot ----

// targetWin counts the messages to one target in the running period.
type targetWin struct {
	start time.Time
	n     int
}

// targetFlooded counts one more message to key and reports whether that
// is over the limit for its kind.
func (s *Server) targetFlooded(key string, toChannel bool, verb string, now time.Time) bool {
	limit, per := s.cfg.TargetFlood.Limit(toChannel, verb)
	if limit == 0 {
		return false
	}
	if s.targetFlood == nil {
		s.targetFlood = map[string]*[3]targetWin{}
	}
	w := s.targetFlood[key]
	if w == nil {
		w = &[3]targetWin{}
		s.targetFlood[key] = w
	}
	i := 2
	switch verb {
	case "PRIVMSG":
		i = 0
	case "NOTICE":
		i = 1
	}
	if now.Sub(w[i].start) >= per {
		w[i] = targetWin{start: now}
	}
	w[i].n++
	return w[i].n > limit
}

// antiSpamTick forgets targets nobody wrote to for a while.
func (s *Server) antiSpamTick(now time.Time) {
	if len(s.targetFlood) == 0 || now.Sub(s.targetSwept) < time.Minute {
		return
	}
	s.targetSwept = now
	for key, w := range s.targetFlood {
		if now.Sub(w[0].start) > time.Hour && now.Sub(w[1].start) > time.Hour && now.Sub(w[2].start) > time.Hour {
			delete(s.targetFlood, key)
		}
	}
}

// ---- all of them, for one message ----

// antiSpam says why c's message to the channel ch (or, when ch is nil, to
// the person t) is not delivered, "" when it is fine. Opers and messages
// to services are never stopped.
func (s *Server) antiSpam(c *Client, ch *state.Channel, t *state.User, verb, text string) string {
	if c.isOper() || t != nil && (t.Service || t == c.user) {
		return ""
	}
	now := time.Now()
	hasText := verb != "TAGMSG"
	if b := s.cfg.BlockAmsg; b.Enabled && ch != nil && hasText {
		key := irc.Fold(ch.Name)
		amsg := c.amsgText == text && c.amsgTarget != key && now.Sub(c.amsgAt) < b.Within()
		c.amsgText, c.amsgTarget, c.amsgAt = text, key, now
		if amsg {
			return "Cannot send to channel (the same message to several channels at once, like /amsg, is not allowed)"
		}
	}
	if m := s.cfg.AntiMixedUTF8; m.Enabled && hasText && !s.inAnyGroup(c.user, m.Except, now) {
		if score := mixedScore(text); score >= m.Limit() {
			u := c.user
			s.snotice("[antimixedutf8] stopped a message of %s (%s@%s): alphabets mixed, score %d", u.Nick, u.TrueIdent(), u.RealHost, score)
			return m.Message()
		}
	}
	if s.cfg.TargetFlood.Enabled {
		if ch != nil && s.targetFlooded(irc.Fold(ch.Name), true, verb, now) {
			return "Channel is being flooded. Message not delivered."
		}
		if ch == nil && s.targetFlooded(t.UID, false, verb, now) {
			return "User is being flooded. Message not delivered."
		}
	}
	return ""
}
