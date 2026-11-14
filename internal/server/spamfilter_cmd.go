package server

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/fhazal/beeircd/internal/irc"
	"github.com/fhazal/beeircd/internal/services"
	"github.com/fhazal/beeircd/internal/spamfilter"
)

// /SPAMFILTER, UnrealIRCd's syntax (oper flag k):
//
//	/SPAMFILTER                                   lists the rules (= STATS F)
//	/SPAMFILTER add [-simple|-regex] [-force] <targets> <action> <ban-time|-> <reason> <match ...>
//	/SPAMFILTER del <id>
//	/SPAMFILTER test <targets> <text ...>         which rules would catch text
//
// The reason uses _ for spaces, as on UnrealIRCd. Without -force a rule
// that would also catch normal chat is refused (the safety net).

const rplStatsSpamF = "229" // UnrealIRCd RPL_STATSSPAMF

func init() {
	commands["SPAMFILTER"] = command{0, false, (*Server).cmdSpamfilter}
}

func (s *Server) cmdSpamfilter(c *Client, m irc.Message) {
	if !s.needFlag(c, nil, 'k') {
		return
	}
	say := func(format string, a ...any) { s.notice(c, "*** Spamfilter: "+fmt.Sprintf(format, a...)) }
	p := m.Params
	if len(p) == 0 {
		s.statsSpam(c)
		s.reply(c, rplEndOfStats, "F", "End of /STATS report")
		return
	}
	switch strings.ToLower(p[0]) {
	case "add", "+":
		f, force, err := parseSpamAdd(p[1:])
		if err != nil {
			say("%v", err)
			say("Syntax: /SPAMFILTER add [-simple|-regex] [-force] <targets> <action> <ban-time|-> <reason> <match>")
			return
		}
		got, err := s.svc.AddSpamFilter(f, c.user.Nick, force)
		var safe *services.SafetyError
		switch {
		case errors.As(err, &safe):
			say("Not added: %v. Narrow the match, or add -force if you are sure.", err)
		case errors.Is(err, services.ErrSpamDuplicate):
			say("Rule %d is the same already.", got.ID)
		case err != nil:
			say("Not added: %v.", err)
		default:
			say("Added rule %d: %s", got.ID, services.Describe(got))
		}
	case "del", "-":
		if len(p) < 2 {
			say("Syntax: /SPAMFILTER del <id> (ids are in /STATS F)")
			return
		}
		id, err := strconv.ParseInt(strings.TrimPrefix(p[1], "#"), 10, 64)
		if err != nil {
			say("%q is not a rule id (ids are in /STATS F).", p[1])
			return
		}
		f, err := s.svc.DelSpamFilter(id, c.user.Nick)
		if err != nil {
			say("%v.", err)
			return
		}
		say("Removed rule %d: %s", f.ID, services.Describe(f))
	case "test":
		if len(p) < 3 {
			say("Syntax: /SPAMFILTER test <targets> <text>")
			return
		}
		targets, err := spamfilter.NormTargets(p[1])
		if err != nil {
			say("%v.", err)
			return
		}
		text := strings.Join(p[2:], " ")
		hits := s.spamTest(targets, text)
		if len(hits) == 0 {
			say("Nothing would catch that.")
			return
		}
		for _, h := range hits {
			say("Rule %d would catch it (%s): %s", h.ID, spamfilter.TargetWords[h.target], services.Describe(h.Filter))
		}
	default:
		say("Use add, del or test. /STATS F lists the rules.")
	}
}

type spamTestHit struct {
	spamfilter.Filter
	target byte
}

// spamTest is every rule that would catch text for any of targets.
func (s *Server) spamTest(targets, text string) []spamTestHit {
	var out []spamTestHit
	seen := map[int64]bool{}
	set := s.svc.SpamSet()
	for i := 0; i < len(targets); i++ {
		for _, r := range set.All(targets[i], text) {
			if !seen[r.ID] {
				seen[r.ID] = true
				out = append(out, spamTestHit{r.Filter, targets[i]})
			}
		}
	}
	return out
}

// parseSpamAdd reads the words after "add".
func parseSpamAdd(p []string) (spamfilter.Filter, bool, error) {
	f := spamfilter.Filter{Type: spamfilter.Simple}
	force := false
	for len(p) > 0 && strings.HasPrefix(p[0], "-") && len(p[0]) > 1 {
		switch strings.ToLower(p[0]) {
		case "-simple":
			f.Type = spamfilter.Simple
		case "-regex":
			f.Type = spamfilter.Regex
		case "-force":
			force = true
		default:
			return f, false, fmt.Errorf("%s is not an option (-simple, -regex, -force)", p[0])
		}
		p = p[1:]
	}
	if len(p) < 5 {
		return f, false, errors.New("not enough words")
	}
	f.Targets, f.Action = p[0], strings.ToLower(p[1])
	if p[2] != "-" && p[2] != "0" {
		d, err := spamDuration(p[2])
		if err != nil {
			return f, false, err
		}
		f.BanTime = d
	}
	f.Reason = spamfilter.DecodeReason(p[3])
	f.Match = strings.Join(p[4:], " ")
	return f, force, nil
}

// spamDuration reads 1d, 2h, 30m or seconds.
func spamDuration(v string) (time.Duration, error) {
	if n, err := strconv.Atoi(v); err == nil && n > 0 {
		return time.Duration(n) * time.Second, nil
	}
	d, err := parseShortDuration(v)
	if err != nil || d < time.Minute {
		return 0, fmt.Errorf("%q is not a ban time (like 30m, 1h, 1d, - for until removed)", v)
	}
	return d, nil
}

// parseShortDuration reads 1w2d3h4m5s.
func parseShortDuration(v string) (time.Duration, error) {
	var total time.Duration
	n := 0
	digits := false
	for i := 0; i < len(v); i++ {
		ch := v[i]
		if ch >= '0' && ch <= '9' {
			n = n*10 + int(ch-'0')
			digits = true
			if n > 1e7 {
				return 0, errors.New("too long")
			}
			continue
		}
		unit := map[byte]time.Duration{'w': 7 * 24 * time.Hour, 'd': 24 * time.Hour, 'h': time.Hour, 'm': time.Minute, 's': time.Second}[ch|0x20]
		if unit == 0 || !digits {
			return 0, errors.New("bad duration")
		}
		total += time.Duration(n) * unit
		n, digits = 0, false
	}
	if digits || total == 0 {
		return 0, errors.New("bad duration")
	}
	return total, nil
}

// statsSpam is STATS F: one 229 line per rule, UnrealIRCd's fields
// (F type targets action setby expire setat ban-time reason :match) with
// BeeIRCd's rule id and hit count before the match.
func (s *Server) statsSpam(c *Client) {
	now := time.Now()
	for _, f := range s.svc.SpamFilters() {
		exp := "0"
		if !f.Expires.IsZero() {
			exp = strconv.FormatInt(int64(f.Expires.Sub(now)/time.Second), 10)
		}
		h := s.svc.SpamHitsOf(f.ID)
		s.reply(c, rplStatsSpamF, "F", f.Type, f.Targets, f.Action, f.SetBy, exp,
			strconv.FormatInt(int64(now.Sub(f.SetAt)/time.Second), 10), strconv.FormatInt(int64(f.BanTime/time.Second), 10),
			spamfilter.EncodeReason(f.Reason), fmt.Sprintf("#%d", f.ID), strconv.Itoa(h.Count), f.Match)
	}
}
