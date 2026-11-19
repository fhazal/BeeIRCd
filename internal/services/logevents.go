package services

import (
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/fhazal/beeircd/internal/irc"
	"github.com/fhazal/beeircd/internal/state"
	"github.com/fhazal/beeircd/internal/store"
)

// Event lines in the log channel, like Anope 2.0's log block: opers,
// kills, servers, ban hits and (when asked for) mode, host and channel
// changes. [services] log_events picks which ones show.

// logEventOn reports whether lines of event ev go to the log channel.
func (s *Services) logEventOn(ev string) bool {
	return !s.off && s.logChannel() != "" && s.cfg.Services.LogEventOn(ev)
}

// Flood cap for ban and session hits: at most hitMax lines per ban mask
// (or IP) in hitWindow, then one "... and N more hits" line.
const (
	hitWindow = 10 * time.Second
	hitMax    = 5
)

// hitCap counts the lines of one ban mask in the current window.
type hitCap struct {
	start   time.Time
	n       int
	dropped int
	sv      *service
	label   string // the mask or IP the summary names
}

// capped posts text from sv unless key already had hitMax lines in this
// window; those it counts for the summary line.
func (s *Services) capped(key, label string, sv *service, text string) {
	now := s.now()
	h := s.hitCaps[key]
	if h != nil && now.Sub(h.start) >= hitWindow {
		s.hitSummary(h)
		h = nil
	}
	if h == nil {
		h = &hitCap{start: now, sv: sv, label: label}
		s.hitCaps[key] = h
	}
	h.n++
	if h.n > hitMax {
		h.dropped++
		return
	}
	s.logTo(sv, text)
}

// hitSummary posts how many lines a window held back.
func (s *Services) hitSummary(h *hitCap) {
	if h.dropped > 0 {
		s.logTo(h.sv, "... and "+strconv.Itoa(h.dropped)+" more hits on "+h.label)
	}
}

// flushHitCaps closes the windows that are over (from Tick).
func (s *Services) flushHitCaps(now time.Time) {
	if len(s.hitCaps) == 0 {
		return
	}
	keys := make([]string, 0, len(s.hitCaps))
	for k := range s.hitCaps {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if h := s.hitCaps[k]; now.Sub(h.start) >= hitWindow {
			s.hitSummary(h)
			delete(s.hitCaps, k)
		}
	}
}

// LogBanHit posts that a ban caught someone, from OperServ: "AKILL: mask
// hit nick!ident@host (set by setter: reason)". kind is the ban kind
// (akill, zline, sqline, spamfilter...).
func (s *Services) LogBanHit(kind, mask, who, setBy, reason string) {
	if !s.logEventOn("bans") {
		return
	}
	text := strings.ToUpper(kind) + ": " + mask + " hit " + who
	switch {
	case setBy != "":
		text += " (set by " + setBy + ": " + reason + ")"
	case reason != "":
		text += " (" + reason + ")"
	}
	s.capped(strings.ToLower(kind)+" "+mask, mask, s.operServ, text)
}

// logNetBanHit is LogBanHit for a stored network ban.
func (s *Services) logNetBanHit(b *store.NetBan, u *state.User) {
	if b != nil && u != nil {
		s.LogBanHit(b.Kind, b.Mask, anopeMask(u), b.SetBy, b.Reason)
	}
}

// ShunBan returns the SHUN covering u, or nil.
func (s *Services) ShunBan(u *state.User) *store.NetBan {
	if len(s.netbans) == 0 || s.exempt(u.TrueIdent(), u.RealHost, u.IP) {
		return nil
	}
	return s.firstBan(store.BanShun, u.TrueIdent(), u.RealHost, u.IP, u.Realname)
}

// LogSessionLimit posts that ip hit its session limit, from OperServ.
// who is the refused nick!ident@host, "" when not known yet.
func (s *Services) LogSessionLimit(ip string, limit int, who string) {
	if !s.logEventOn("other") {
		return
	}
	ip = irc.DisplayIP(ip)
	text := "SESSION: limit of " + strconv.Itoa(limit) + " reached for " + ip
	if who != "" {
		text += " (" + who + ")"
	}
	s.capped("session "+ip, ip, s.operServ, text)
}

// LogKill posts that u was killed, from Global. by is a nick or a server
// name.
func (s *Services) LogKill(u *state.User, by, reason string) {
	if u == nil || u.Service || !s.logEventOn("kill") {
		return
	}
	s.logTo(s.global, "USERS: "+anopeMask(u)+" was killed by "+by+" (Reason: "+reason+")")
}

// LogServerJoin posts that a server joined the network, from Global.
func (s *Services) LogServerJoin(name, desc, uplink string) {
	if !s.logEventOn("server") {
		return
	}
	s.logTo(s.global, "SERVER: "+name+" ("+desc+") has connected to the network (uplinked to "+uplink+")")
}

// LogServerSplit posts that a server left the network, from Global.
func (s *Services) LogServerSplit(name, desc, uplink, reason string) {
	if !s.logEventOn("server") {
		return
	}
	s.logTo(s.global, "SERVER: "+name+" ("+desc+") quit from "+uplink+" ("+reason+")")
}

// logUserMode posts oper up/down (event oper) and the mode change itself
// (event umode).
func (s *Services) logUserMode(e *state.UserMode) {
	if len(e.Changes) == 0 || s.logChannel() == "" {
		return
	}
	u := s.net.UserByUID(e.UID)
	if u == nil || u.Service {
		return
	}
	if s.logEventOn("oper") {
		for _, m := range e.Changes {
			if m.Mode != 'o' {
				continue
			}
			if m.Add {
				s.logTo(s.global, "USERS: "+anopeMask(u)+" ("+u.Realname+") is now an IRC operator"+s.operDetail(u))
			} else {
				s.logTo(s.global, "USERS: "+anopeMask(u)+" ("+u.Realname+") is no longer an IRC operator")
			}
		}
	}
	if s.logEventOn("umode") {
		s.logTo(s.global, "USERS: "+anopeMask(u)+" changes modes to "+modeString(e.Changes))
	}
}

// operDetail is " (rank, oper account name)" for an oper line, leaving out
// what isn't known.
func (s *Services) operDetail(u *state.User) string {
	var parts []string
	if r := s.operRank(u); r != "" {
		parts = append(parts, r)
	}
	if u.OperName != "" {
		parts = append(parts, "oper account "+u.OperName)
	}
	if len(parts) == 0 {
		return ""
	}
	return " (" + strings.Join(parts, ", ") + ")"
}

// operRank is an oper's rank: "Network Administrator" from the title
// "is a Network Administrator", else the class name.
func (s *Services) operRank(u *state.User) string {
	title := u.OperTitle
	if title == "" && u.OperClass != "" {
		oc, ok := s.cfg.Class(u.OperClass)
		if !ok {
			oc, ok = s.cfg.Class(strings.TrimSuffix(u.OperClass, "-with-override"))
		}
		if ok {
			title = oc.Title
		}
	}
	for _, p := range []string{"is a ", "is an ", "is the "} {
		if len(title) > len(p) && strings.EqualFold(title[:len(p)], p) {
			title = title[len(p):]
			break
		}
	}
	if title == "" {
		return u.OperClass
	}
	return title
}

// modeString is "+ix-w" for a list of user mode changes.
func modeString(ms []state.ModeChange) string {
	var b strings.Builder
	sign := byte(0)
	for _, m := range ms {
		want := byte('-')
		if m.Add {
			want = '+'
		}
		if want != sign {
			b.WriteByte(want)
			sign = want
		}
		b.WriteByte(m.Mode)
	}
	return b.String()
}

// logHost posts a host change (event host).
func (s *Services) logHost(e *state.HostChange) {
	if !s.logEventOn("host") {
		return
	}
	u := s.net.UserByUID(e.UID)
	if u == nil || u.Service {
		return
	}
	host := u.Host
	if e.Ident != "" {
		host = u.Ident + "@" + u.Host
	}
	s.logTo(s.global, "USERS: "+anopeMask(u)+" changed host to "+host)
}

// logJoin posts a join and a new channel (event channel). Joins from a
// server link's sync are left out, and the log channel itself too.
func (s *Services) logJoin(e *state.Join) {
	if e.Burst || !s.logEventOn("channel") || s.IsLogChannel(e.Channel) {
		return
	}
	u := s.net.UserByUID(e.UID)
	if u == nil || u.Service {
		return
	}
	name := e.Channel
	if ch := s.net.Channel(e.Channel); ch != nil {
		name = ch.Name
	}
	if e.Created {
		s.logTo(s.global, "CHANNEL: "+name+" created by "+u.Nick)
	}
	s.logTo(s.global, "CHANNEL: "+anopeMask(u)+" joined "+name)
}

// logPart posts a part, and the channel going empty after a part or kick
// (event channel).
func (s *Services) logPart(uid, channel string, parted bool) {
	if !s.logEventOn("channel") || s.IsLogChannel(channel) {
		return
	}
	u := s.net.UserByUID(uid)
	if u == nil || u.Service {
		return
	}
	if parted {
		s.logTo(s.global, "CHANNEL: "+anopeMask(u)+" parted "+channel)
	}
	if s.net.Channel(channel) == nil {
		s.logTo(s.global, "CHANNEL: "+channel+" is now empty")
	}
}
