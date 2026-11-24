package server

import (
	"cmp"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/fhazal/beeircd/internal/config"
	"github.com/fhazal/beeircd/internal/irc"
	"github.com/fhazal/beeircd/internal/state"
)

// Connect classes ([[class]] blocks). Someone's class is worked out once,
// when they register (after SASL, before the welcome), and again for
// everyone on a rehash. Logging in or getting a mode later doesn't move
// them: the limits they connected with stay until the next rehash.

// Numerics of STATS Y and STATS G.
const (
	rplStatsYLine   = "218" // RPL_STATSYLINE (UnrealIRCd: one line per class)
	rplStatsCountry = "801" // RPL_STATSCOUNTRY (InspIRCd's geoclass)
)

// floodPace is the flood burst and rate of a class, for readLoop.
type floodPace struct{ burst, rate int }

// classFor is the class u is in: the first [[class]] in the file that
// takes them in, or nil for the default class ([limits]). The class is a
// copy, so a rehash can't change it under the person.
func (s *Server) classFor(u *state.User, now time.Time) *config.ConnClass {
	if u == nil || u.Service {
		return nil
	}
	for i := range s.cfg.ConnClasses {
		if k := &s.cfg.ConnClasses[i]; s.inAnyGroup(u, k.SecurityGroup, now) {
			copied := *k
			return &copied
		}
	}
	return nil
}

// limitsOf is what c gets from its class: the one place sendq, pings,
// flood and max_channels are read from.
func (s *Server) limitsOf(c *Client) config.ClassLimits { return s.cfg.LimitsOf(c.class) }

// className is the name of c's class, "default" for the default class.
func className(k *config.ConnClass) string {
	if k == nil {
		return config.DefaultClass
	}
	return k.Name
}

// setClass puts c in class k and tells its reader the class's flood pace.
// Nothing is stored for the default class when c was in it already, so a
// server without classes reads flood limits as it always has.
func (s *Server) setClass(c *Client, k *config.ConnClass) {
	was := c.class
	c.class = k
	if k == nil && was == nil {
		return
	}
	l := s.limitsOf(c)
	c.floodClass.Store(&floodPace{burst: l.FloodBurst, rate: l.FloodRate})
}

// inClass counts the local people in the class named name.
func (s *Server) inClass(name string) int {
	n := 0
	for _, c := range s.clients {
		if c.registered && !c.closed && strings.EqualFold(className(c.class), name) {
			n++
		}
	}
	return n
}

// classAdmit works out c's class at registration and refuses c when the
// class says so: deny, max_users, or its session_limit. False means c was
// refused (and is on its way out).
func (s *Server) classAdmit(c *Client, u *state.User, now time.Time) bool {
	if len(s.cfg.ConnClasses) == 0 {
		return true // no classes: [limits] as it always was
	}
	k := s.classFor(u, now)
	name := className(k)
	if k != nil && k.Deny {
		reason := "Your connection class (" + k.Name + ") may not connect here"
		s.reply(c, irc.ErrYoureBannedCreep, s.cfg.Messages.BanText("You are banned from this network: "+reason))
		s.sno('x', "Class %s stopped %s (%s@%s)", k.Name, c.nick, c.ident, c.host)
		s.banQuit(c, "Banned", reason)
		return false
	}
	l := s.cfg.LimitsOf(k)
	if k != nil && k.MaxUsers > 0 && s.inClass(name) >= k.MaxUsers {
		s.sno('a', "Client %s with IP %s rejected: class %s is full (%d)", c.nick, c.ip, name, k.MaxUsers)
		s.quit(c, "Too many connections in your class ("+name+")")
		return false
	}
	// The session limit of the class (OperServ exceptions still win;
	// gateway IPs are left alone, as at connect).
	if !s.webircGateway(c.ip) {
		limit := l.SessionLimit
		if n, ok := s.svc.SessionExcept(c.ip, c.host); ok {
			limit = n
		}
		if n := s.sessions(c.ip); n > limit {
			s.svc.LogSessionLimit(c.ip, limit, c.logMask(c.nick))
			s.sno('a', "Client %s with IP %s rejected: maxperip limit exceeded (%d local, max %d, class %s)", c.nick, c.ip, n, limit, name)
			s.quit(c, s.cfg.Messages.TooManyText())
			return false
		}
	}
	s.setClass(c, k)
	return true
}

// sessionMax is how many connections ip may have before its class is
// known: its OperServ exception, else the loosest of [limits] and the
// classes' session limits (the class's own limit is checked when the
// person registers). Without classes it is [limits] session_limit.
func (s *Server) sessionMax(ip string) int {
	if n, ok := s.svc.SessionExcept(ip, ""); ok {
		return n
	}
	most := s.cfg.Limits.SessionLimit
	for _, k := range s.cfg.ConnClasses {
		most = max(most, k.SessionLimit)
	}
	return most
}

// reclass works out every local person's class again after a rehash or
// a hand-over. Nobody is disconnected: a full class or a deny only stops
// new connections.
func (s *Server) reclass() {
	now := time.Now()
	for _, c := range s.clients {
		if c.registered && !c.closed && c.user != nil {
			s.setClass(c, s.classFor(c.user, now))
		}
	}
}

// statsClasses is STATS Y: one line per class with its people and limits,
// the default class last.
func (s *Server) statsClasses(c *Client) {
	line := func(k *config.ConnClass) {
		name := className(k)
		l := s.cfg.LimitsOf(k)
		text := fmt.Sprintf("%d in class; session_limit %d, max_users %d, sendq %d, ping %s/%s, flood %d/%d, max_channels %d",
			s.inClass(name), l.SessionLimit, l.MaxUsers, l.SendQ, l.PingInterval, l.PingTimeout, l.FloodBurst, l.FloodRate, l.MaxChannels)
		if k != nil {
			text += "; groups " + strings.Join(k.SecurityGroup, ",")
			if k.Deny {
				text += "; deny"
			}
		}
		s.reply(c, rplStatsYLine, "Y", name, text)
	}
	for i := range s.cfg.ConnClasses {
		line(&s.cfg.ConnClasses[i])
	}
	line(nil)
}

// statsCountries is STATS G: how many local people come from each
// country, most first ("--" when GeoIP doesn't know).
func (s *Server) statsCountries(c *Client) {
	type place struct {
		code, name string
		n          int
	}
	by := map[string]*place{}
	for _, cl := range s.clients {
		u := cl.user
		if !cl.registered || cl.closed || u == nil || u.Service {
			continue
		}
		code := orStr(u.Country, "--")
		p := by[code]
		if p == nil {
			p = &place{code: code, name: "Unknown"}
			if u.Country != "" {
				p.name = orStr(s.geo.set.Lookup(u.IP).Name, u.Country)
			}
			by[code] = p
		}
		p.n++
	}
	list := make([]*place, 0, len(by))
	for _, p := range by {
		list = append(list, p)
	}
	slices.SortFunc(list, func(a, b *place) int {
		return cmp.Or(cmp.Compare(b.n, a.n), cmp.Compare(a.code, b.code))
	})
	for _, p := range list {
		s.reply(c, rplStatsCountry, strconv.Itoa(p.n), p.code, p.name)
	}
}
