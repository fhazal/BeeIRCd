package server

import (
	"fmt"
	"net"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/fhazal/beeircd/internal/irc"
	"github.com/fhazal/beeircd/internal/state"
	"github.com/fhazal/beeircd/internal/store"
)

// Reputation: a score per IP that grows by one every five minutes while
// someone from it sits in a channel of three or more (two when they are
// logged in), like UnrealIRCd's reputation module. Servers share scores
// with REPUTATION and keep the higher one, so a score earned anywhere on
// the network counts everywhere.

const (
	repCap       = 10000
	repBumpEvery = 5 * time.Minute
	repSaveEvery = 15 * time.Minute
)

type repEntry struct {
	score int
	seen  int64 // unix seconds
	mark  uint32
}

type repState struct {
	scores    map[string]*repEntry
	round     uint32
	lastBump  time.Time
	lastSave  time.Time
	written   time.Time // last good save
	since     time.Time // gathering started
	dirty     bool
	bumpEvery time.Duration // tests: short
}

func (s *Server) repOn() bool { return s.cfg.Reputation.On() }

// loadReputation fills the scores from the database.
func (s *Server) loadReputation(now time.Time) {
	r := &s.rep
	r.scores = map[string]*repEntry{}
	r.lastBump, r.lastSave = now, now
	if r.bumpEvery == 0 {
		r.bumpEvery = repBumpEvery
	}
	if !s.repOn() {
		return
	}
	all, err := s.store.Reputations()
	if err != nil {
		s.log.Printf("services database: reputation: %v", err)
	}
	for _, e := range all {
		r.scores[e.IP] = &repEntry{score: e.Score, seen: e.LastSeen.Unix()}
	}
	r.since = s.store.ReputationSince()
	if r.since.IsZero() {
		r.since = now
		if err := s.store.SetReputationSince(now); err != nil {
			s.log.Printf("services database: reputation: %v", err)
		}
	}
}

// repScore is ip's score (0 when none).
func (s *Server) repScore(ip string) int {
	if e := s.rep.scores[ip]; e != nil {
		return e.score
	}
	return 0
}

// gathering reports whether scores have been gathered for less than d.
func (s *Server) gathering(d time.Duration, now time.Time) bool {
	return !s.repOn() || now.Sub(s.rep.since) < d
}

func (s *Server) repTick(now time.Time) {
	r := &s.rep
	if !s.repOn() {
		return
	}
	if now.Sub(r.lastBump) >= r.bumpEvery {
		r.lastBump = now
		s.repBump(now)
		s.repExpire(now)
	}
	if now.Sub(r.lastSave) >= repSaveEvery {
		s.saveReputation(now)
	}
}

// biggestChannel is how many members u's biggest channel has.
func biggestChannel(u *state.User) int {
	n := 0
	for _, ch := range u.Channels {
		n = max(n, len(ch.Members))
	}
	return n
}

// repBump gives every IP with someone chatting its points: one per IP
// per round, two when one of them is logged in.
func (s *Server) repBump(now time.Time) {
	r := &s.rep
	r.round += 2
	plain, loggedIn := r.round, r.round+1
	need := s.cfg.Reputation.Members()
	for _, u := range s.net.Users() {
		if u.Service || u.IP == "" || u.IP == "0" {
			continue
		}
		if need > 0 && biggestChannel(u) < need {
			continue
		}
		e := r.scores[u.IP]
		if e == nil {
			e = &repEntry{}
			r.scores[u.IP] = e
		}
		switch {
		case e.mark != plain && e.mark != loggedIn:
			e.mark = plain
			if e.score < repCap {
				e.score++
				if u.Account != "" && e.score < repCap {
					e.score++
					e.mark = loggedIn
				}
			}
		case e.mark == plain && u.Account != "" && e.score < repCap:
			e.score++
			e.mark = loggedIn
		}
		e.seen = now.Unix()
		r.dirty = true
	}
}

// repExpire forgets IPs not seen for a while: low scores sooner.
func (s *Server) repExpire(now time.Time) {
	for ip, e := range s.rep.scores {
		age := now.Unix() - e.seen
		if (e.score <= 2 && age > 3600) || (e.score <= 6 && age > 7*86400) ||
			(e.score <= 12 && age > 30*86400) || age > 90*86400 {
			delete(s.rep.scores, ip)
			s.rep.dirty = true
		}
	}
}

func (s *Server) saveReputation(now time.Time) {
	r := &s.rep
	r.lastSave = now
	if !r.dirty || !s.repOn() {
		return
	}
	all := make([]store.Reputation, 0, len(r.scores))
	for ip, e := range r.scores {
		all = append(all, store.Reputation{IP: ip, Score: e.score, LastSeen: time.Unix(e.seen, 0)})
	}
	if err := s.store.SaveReputations(all); err != nil {
		s.log.Printf("services database: reputation: %v", err)
		return
	}
	r.dirty = false
	r.written = now
}

// repSet changes ip's score and puts its people in the right groups.
func (s *Server) repSet(ip string, score int, now time.Time) {
	e := s.rep.scores[ip]
	if e == nil {
		e = &repEntry{seen: now.Unix()}
		s.rep.scores[ip] = e
	}
	e.score = score
	s.rep.dirty = true
	for _, u := range s.net.Users() {
		if u.IP == ip {
			s.refreshGroups(u)
		}
	}
}

// repAnnounce tells the other servers our score for someone connecting.
func (s *Server) repAnnounce(ip string) {
	if !s.repOn() || ip == "" {
		return
	}
	s.linkBroadcast(nil, ":"+s.cfg.Server.SID+" REPUTATION "+ip+" "+strconv.Itoa(s.repScore(ip)))
}

// remoteReputation is ":server REPUTATION <ip> <score>" from a link; a
// "*" first means "don't answer", one last too means "set it, even
// lower" (an oper changed it).
func (s *Server) remoteReputation(l *linkConn, m irc.Message) {
	if len(m.Params) < 2 || m.Params[1] == "" || !s.repOn() {
		return
	}
	ip, raw := m.Params[0], m.Params[1]
	if net.ParseIP(ip) == nil {
		return
	}
	reply, forced := true, false
	if strings.HasPrefix(raw, "*") {
		reply = false
		raw = raw[1:]
		if strings.HasSuffix(raw, "*") && raw != "" {
			forced = true
			raw = strings.TrimSuffix(raw, "*")
		}
	}
	score, err := strconv.Atoi(raw)
	if err != nil {
		return
	}
	score = min(max(score, 0), repCap)
	now := time.Now()
	e := s.rep.scores[ip]
	if reply && e != nil && e.score > score+1 {
		l.send(":" + s.cfg.Server.SID + " REPUTATION " + ip + " *" + strconv.Itoa(e.score))
		score = e.score
	}
	switch {
	case e != nil && score > e.score, e != nil && forced && score != e.score:
		s.repSet(ip, score, now)
	case e == nil && score > 0:
		s.repSet(ip, score, now)
	}
	out := strconv.Itoa(score)
	if !reply {
		out = "*" + out
	}
	if forced {
		out += "*"
	}
	src := m.Source
	if src == "" {
		src = l.peer.SID
	}
	s.linkBroadcast(l, ":"+src+" REPUTATION "+ip+" "+out)
}

// cmdReputation is the oper REPUTATION command (UnrealIRCd's): no
// argument for the numbers, a nick or IP for one score (and a new value
// to set it), #channel for its people, <N for everyone below N.
func (s *Server) cmdReputation(c *Client, m irc.Message) {
	if !s.needFlag(c, nil, 'h') {
		return
	}
	note := func(f string, a ...any) { s.notice(c, fmt.Sprintf(f, a...)) }
	if !s.repOn() {
		note("Reputation is off on this server ([reputation] enabled = false).")
		return
	}
	now := time.Now()
	if len(m.Params) == 0 || m.Params[0] == "" {
		r := &s.rep
		note("Reputation module statistics:")
		note("Recording for: %d seconds (since unixtime %d)", int64(now.Sub(r.since)/time.Second), r.since.Unix())
		if r.written.IsZero() {
			note("Last successful db write: never")
		} else {
			note("Last successful db write: %d seconds ago (unixtime %d)", int64(now.Sub(r.written)/time.Second), r.written.Unix())
		}
		note("Current number of records (IP's): %d", len(r.scores))
		note("-")
		note("Available commands:")
		note("/REPUTATION <nick>          Show reputation info about nick name")
		note("/REPUTATION <nick> <value>  Adjust the reputation score of the IP address of nick to <value>")
		note("/REPUTATION <ip>            Show reputation info about IP address")
		note("/REPUTATION <ip> <value>    Adjust the reputation score of the IP address to <value>")
		note("/REPUTATION <channel>       List users in channel along with their reputation score")
		note("/REPUTATION <NN             List users with reputation score below value NN")
		return
	}
	arg := m.Params[0]
	var ip string
	switch {
	case strings.ContainsAny(arg, ".:"):
		if net.ParseIP(arg) == nil {
			note("REPUTATION: %s is not an IP address", arg)
			return
		}
		ip = arg
	case irc.IsChannel(arg):
		ch := s.net.Channel(arg)
		if ch == nil {
			s.reply(c, irc.ErrNoSuchChannel, arg, "No such channel")
			return
		}
		note("Users and reputation scores for %s:", ch.Name)
		for _, mem := range ch.SortedMembers() {
			if !mem.User.Service {
				note("%s!%s@%s [%s] score: %d", mem.User.Nick, mem.User.Ident, mem.User.RealHost, mem.User.IP, s.repScore(mem.User.IP))
			}
		}
		note("End of list.")
		return
	case strings.HasPrefix(arg, "<"):
		below, err := strconv.Atoi(arg[1:])
		if err != nil || below < 1 {
			note("REPUTATION: Invalid search value specified. Use for example '/REPUTATION <5' to search on less-than-five")
			return
		}
		users := s.net.Users()
		sort.Slice(users, func(i, j int) bool { return irc.Fold(users[i].Nick) < irc.Fold(users[j].Nick) })
		note("Users and reputation scores <%d:", below)
		for _, u := range users {
			if !u.Service && s.repScore(u.IP) < below {
				note("%s!%s@%s [%s] score: %d", u.Nick, u.Ident, u.RealHost, u.IP, s.repScore(u.IP))
			}
		}
		note("End of list.")
		return
	default:
		u := s.net.UserByNick(arg)
		if u == nil {
			s.reply(c, irc.ErrNoSuchNick, arg, "No such nick/channel")
			return
		}
		if u.Service || u.IP == "" {
			note("No IP address information available for user '%s'.", arg)
			return
		}
		ip = u.IP
	}
	setting := len(m.Params) > 1 && m.Params[1] != ""
	if setting && !s.needFlag(c, nil, 'g') {
		return
	}
	e := s.rep.scores[ip]
	if e == nil {
		note("No reputation record found for IP %s", ip)
		return
	}
	if setting {
		v, err := strconv.Atoi(m.Params[1])
		if err != nil {
			note("REPUTATION: the new score must be a number from 0 to %d", repCap)
			return
		}
		v = min(max(v, 0), repCap)
		s.repSet(ip, v, now)
		s.linkBroadcast(nil, ":"+s.cfg.Server.SID+" REPUTATION "+ip+" *"+strconv.Itoa(v)+"*")
		note("Reputation of IP %s set to %d", ip, v)
		return
	}
	note("****************************************************")
	note("Reputation record for IP %s:", ip)
	note("    Score: %d", e.score)
	note("Last seen: %d seconds ago (unixtime: %d)", now.Unix()-e.seen, e.seen)
	note("****************************************************")
}
