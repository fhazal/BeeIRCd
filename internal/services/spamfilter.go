package services

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/fhazal/beeircd/internal/irc"
	"github.com/fhazal/beeircd/internal/spamfilter"
	"github.com/fhazal/beeircd/internal/state"
	"github.com/fhazal/beeircd/internal/store"
)

// Spam filter rules (after 1.0). Services keep the
// list (in the database, like the network bans) and a compiled set the
// server checks text against; every change goes to the links through
// OnSpamChange, rules from links come in through RemoteSpamFilter.

// SpamHits counts what one rule caught (since this server started).
type SpamHits struct {
	Count int
	Last  time.Time
}

type spamState struct {
	list   []spamfilter.Filter
	set    *spamfilter.Set
	bad    map[int64]bool // rules our RE2 can't read (from other servers)
	hits   map[int64]*SpamHits
	recent []time.Time // catch times, oldest first (SpamCaught24h)
	hook   func(f spamfilter.Filter, add bool)
	loaded bool
	due    time.Time // when the next timed rule runs out (zero = none)
}

// OnSpamChange sets what runs after a rule went on (add) or off.
func (s *Services) OnSpamChange(f func(f spamfilter.Filter, add bool)) { s.spam.hook = f }

// loadSpamFilters reads the rules and compiles them.
func (s *Services) loadSpamFilters() error {
	list, err := s.store.SpamFilters()
	if err != nil {
		return err
	}
	set, bad := spamfilter.NewSet(list, time.Now())
	s.spam.list, s.spam.set, s.spam.bad = list, set, map[int64]bool{}
	s.spam.due = time.Time{}
	for _, f := range list {
		if t := time.Unix(f.Expires.Unix(), 0); !f.Expires.IsZero() && t.Unix() > 0 && (s.spam.due.IsZero() || t.Before(s.spam.due)) {
			s.spam.due = t
		}
	}
	for _, f := range bad {
		s.spam.bad[f.ID] = true
		if !s.spam.loaded {
			s.log.Printf("spam filter %d (%s) can't be used here: %s", f.ID, f.Match, compileErr(f))
		}
	}
	if s.spam.hits == nil {
		s.spam.hits = map[int64]*SpamHits{}
	}
	s.spam.loaded = true
	return nil
}

func compileErr(f spamfilter.Filter) string {
	if _, err := spamfilter.Compile(f); err != nil {
		return err.Error()
	}
	return "?"
}

// SpamSet is the compiled rules for the server's checks (nil = none).
func (s *Services) SpamSet() *spamfilter.Set { return s.spam.set }

// SpamFilters is every live rule, oldest first.
func (s *Services) SpamFilters() []spamfilter.Filter {
	now := time.Now()
	var out []spamfilter.Filter
	for _, f := range s.spam.list {
		if f.Live(now) {
			out = append(out, f)
		}
	}
	return out
}

// SpamHitsOf is what rule id caught.
func (s *Services) SpamHitsOf(id int64) SpamHits {
	if h := s.spam.hits[id]; h != nil {
		return *h
	}
	return SpamHits{}
}

// SpamUnusable reports whether rule id can't be used here.
func (s *Services) SpamUnusable(id int64) bool { return s.spam.bad[id] }

// SpamHit counts a catch by rule id.
func (s *Services) SpamHit(id int64, now time.Time) {
	h := s.spam.hits[id]
	if h == nil {
		h = &SpamHits{}
		s.spam.hits[id] = h
	}
	h.Count++
	h.Last = now
	if len(s.spam.recent) >= spamRecentMax {
		s.spam.recent = s.spam.recent[1:]
	}
	s.spam.recent = append(s.spam.recent, now)
}

// ErrSpamDuplicate: the same rule is there already.
var ErrSpamDuplicate = errors.New("that rule is there already")

// AddSpamFilter adds rule f for oper by (checked first: spamfilter.Check
// and, unless force, the safety net). It returns the stored rule.
func (s *Services) AddSpamFilter(f spamfilter.Filter, by string, force bool) (spamfilter.Filter, error) {
	t, err := spamfilter.NormTargets(f.Targets)
	if err != nil {
		return f, err
	}
	f.Targets, f.SetBy, f.SetAt = t, by, time.Now()
	if f.Reason == "" {
		f.Reason = "Spam is not allowed here"
	}
	if err := spamfilter.Check(f); err != nil {
		return f, err
	}
	if !force {
		if hit := spamfilter.SafetyHits(f, 2); len(hit) > 0 {
			return f, &SafetyError{Lines: hit}
		}
	}
	live := s.SpamFilters()
	if len(live) >= spamfilter.MaxFilters {
		return f, fmt.Errorf("there are %d rules already, the most this server keeps", spamfilter.MaxFilters)
	}
	for _, have := range live {
		if have.Key() == f.Key() && have.Reason == f.Reason && have.BanTime == f.BanTime && have.Expires.Equal(f.Expires) {
			return have, ErrSpamDuplicate
		}
	}
	id, err := s.store.AddSpamFilter(f)
	if err != nil {
		s.log.Printf("services database: %v", err)
		return f, errors.New("something went wrong on the server")
	}
	f.ID = id
	if err := s.loadSpamFilters(); err != nil {
		s.log.Printf("services database: %v", err)
	}
	if s.spam.hook != nil {
		s.spam.hook(f, true)
	}
	s.snotice('f', "%s added filter %s", by, FilterNotice(f))
	s.logTo(s.operServ, fmt.Sprintf("%s added spam filter %s", by, Describe(f)))
	return f, nil
}

// SafetyError: the rule would catch normal chat lines.
type SafetyError struct{ Lines []string }

func (e *SafetyError) Error() string {
	q := make([]string, len(e.Lines))
	for i, l := range e.Lines {
		q[i] = fmt.Sprintf("%q", l)
	}
	return "it would also catch normal chat, like " + strings.Join(q, " and ")
}

// DelSpamFilter removes rule id for oper by.
func (s *Services) DelSpamFilter(id int64, by string) (spamfilter.Filter, error) {
	var f spamfilter.Filter
	found := false
	for _, have := range s.SpamFilters() {
		if have.ID == id {
			f, found = have, true
		}
	}
	if !found {
		return f, fmt.Errorf("there is no spam filter %d", id)
	}
	if _, err := s.store.DelSpamFilter(id); err != nil {
		s.log.Printf("services database: %v", err)
		return f, errors.New("something went wrong on the server")
	}
	delete(s.spam.hits, id)
	if err := s.loadSpamFilters(); err != nil {
		s.log.Printf("services database: %v", err)
	}
	if s.spam.hook != nil {
		s.spam.hook(f, false)
	}
	s.snotice('f', "%s removed filter '%s': %s", by, f.Match, f.Reason)
	s.logTo(s.operServ, fmt.Sprintf("%s removed spam filter %s", by, Describe(f)))
	return f, nil
}

// RemoteSpamFilter applies a rule change from another server and reports
// whether anything changed. Rules from links are checked only for size:
// the oper who set them was checked on their own server.
func (s *Services) RemoteSpamFilter(f spamfilter.Filter, add bool) bool {
	var have *spamfilter.Filter
	for i := range s.spam.list {
		if s.spam.list[i].Key() == f.Key() {
			have = &s.spam.list[i]
		}
	}
	if !add {
		if have == nil {
			return false
		}
		if _, err := s.store.DelSpamFilter(have.ID); err != nil {
			s.log.Printf("services database: %v", err)
			return false
		}
		delete(s.spam.hits, have.ID)
	} else {
		if !f.Live(time.Now()) || f.Match == "" || len(f.Match) > spamfilter.MaxMatch || len(f.Reason) > 4*spamfilter.MaxReason {
			return false
		}
		if have != nil && (have.SetAt.After(f.SetAt) || have.Reason == f.Reason && have.BanTime == f.BanTime && have.Expires.Equal(f.Expires)) {
			return false // ours is newer, or the same (after a split both sides send theirs)
		}
		if len(s.SpamFilters()) >= spamfilter.MaxFilters && have == nil {
			s.log.Printf("refused spam filter %q from another server: %d rules already", f.Match, spamfilter.MaxFilters)
			return false
		}
		if _, err := s.store.AddSpamFilter(f); err != nil {
			s.log.Printf("services database: %v", err)
			return false
		}
	}
	if err := s.loadSpamFilters(); err != nil {
		s.log.Printf("services database: %v", err)
	}
	return true
}

// expireSpamFilters drops rules that ran out (every server does at the
// same time, so nothing is sent over links).
func (s *Services) expireSpamFilters(now time.Time) {
	gone, err := s.store.PurgeSpamFilters(now)
	if err != nil {
		s.log.Printf("services database: %v", err)
		return
	}
	if len(gone) == 0 {
		return
	}
	for _, f := range gone {
		delete(s.spam.hits, f.ID)
		s.snotice('f', "%s", expiryLine("filter", "'"+f.Match+"'", f.SetBy, f.SetAt, f.Reason, now))
	}
	if err := s.loadSpamFilters(); err != nil {
		s.log.Printf("services database: %v", err)
	}
}

// Describe is one rule in a line: [regex] match on chan, private -> AKILL 1d (reason).
func Describe(f spamfilter.Filter) string {
	m := f.Match
	if f.Type == spamfilter.Regex {
		m = "regex " + m
	}
	return fmt.Sprintf("%s on %s -> %s (%s)", m, strings.Join(f.Words(), ", "), ActionWords(f), f.Reason)
}

// FilterNotice is f the way InspIRCd 4's m_filter words a new filter in
// its "added filter" notice: "'match', type 'action', duration '1 hour',
// flags 'targets', reason: why", the duration only for a timed ban. The
// type and flags are our (UnrealIRCd) action name and target letters.
func FilterNotice(f spamfilter.Filter) string {
	dur := ""
	if spamfilter.Bans(f.Action) && f.BanTime > 0 {
		dur = ", duration '" + longDuration(f.BanTime) + "'"
	}
	return fmt.Sprintf("'%s', type '%s'%s, flags '%s', reason: %s", f.Match, f.Action, dur, f.Targets, f.Reason)
}

// ActionWords is f's action in plain words: "Block", "AKILL 1d" ...
func ActionWords(f spamfilter.Filter) string {
	length := "until removed"
	if f.BanTime > 0 {
		length = shortDur(f.BanTime)
	}
	switch f.Action {
	case spamfilter.Warn:
		return "Warn opers"
	case spamfilter.Block:
		return "Block"
	case spamfilter.Kill:
		return "Kill"
	case spamfilter.TempShun:
		return "Shun until reconnect"
	case spamfilter.Shun:
		return "Shun " + length
	case spamfilter.Gline, spamfilter.Kline:
		return "AKILL " + length
	case spamfilter.Gzline, spamfilter.Zline:
		return "ZLINE " + length
	}
	return f.Action + " (not used here)"
}

// shortDur writes 90m as 1h30m, 86400s as 1d.
func shortDur(d time.Duration) string {
	if d%(24*time.Hour) == 0 {
		return fmt.Sprintf("%dd", d/(24*time.Hour))
	}
	if d%time.Hour == 0 {
		return fmt.Sprintf("%dh", d/time.Hour)
	}
	return fmt.Sprintf("%dm", d/time.Minute)
}

// SpamBan sets the network ban a rule's action asks for: kind is
// store.BanShun, BanAkill or BanZline, on ip; life 0 = until removed.
func (s *Services) SpamBan(kind, ip, reason string, life time.Duration) {
	mask := ip
	if kind != store.BanZline {
		mask = "*@" + ip
	}
	mask, problem := s.netbanMask(kind, mask)
	if problem != "" {
		return
	}
	for _, b := range s.liveBans(kind) {
		if strings.EqualFold(b.Mask, mask) {
			return
		}
	}
	now := time.Now()
	b := store.NetBan{Kind: kind, Mask: mask, Reason: irc.Truncate(reason, 200), SetBy: "Spam filter", SetAt: now}
	if life > 0 {
		b.Expires = now.Add(life)
	}
	if err := s.store.AddNetBan(b); err != nil {
		s.log.Printf("services database: %v", err)
		return
	}
	s.reloadOperData()
}

// SpamExempt reports whether u is never filtered: an Exception (ELINE).
// Opers are let through by the server.
func (s *Services) SpamExempt(u *state.User) bool { return s.exempt(u.TrueIdent(), u.RealHost, u.IP) }

// SpamExemptMask is SpamExempt before the person is registered.
func (s *Services) SpamExemptMask(ident, host, ip string) bool { return s.exempt(ident, host, ip) }

// spamRecentMax caps the hit times kept for the 24-hour count.
const spamRecentMax = 20000

// SpamCaught24h is how many catches the last 24 hours had (since start).
func (s *Services) SpamCaught24h(now time.Time) int {
	cut := now.Add(-24 * time.Hour)
	i := 0
	for i < len(s.spam.recent) && s.spam.recent[i].Before(cut) {
		i++
	}
	s.spam.recent = s.spam.recent[i:]
	return len(s.spam.recent)
}

// CheckSpamFilter says what adding f would do, without adding it: a
// problem (it can't be added), or the normal lines it would catch.
func (s *Services) CheckSpamFilter(f spamfilter.Filter) (problem string, unsafe []string, lines int) {
	t, err := spamfilter.NormTargets(f.Targets)
	if err != nil {
		return err.Error(), nil, 0
	}
	f.Targets = t
	if err := spamfilter.Check(f); err != nil {
		return err.Error(), nil, 0
	}
	return "", spamfilter.SafetyHits(f, 2), spamfilter.SafetyCount(f)
}
