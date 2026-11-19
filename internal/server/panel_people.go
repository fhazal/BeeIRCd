package server

// The Hive Panel's People page. Everyone on the network (every
// server), searchable, one page at a time; one person's details; a notice,
// a disconnect (flag k) or a network ban (flag k, like OperServ AKILL).
// Real hosts and addresses are only shown to opers with flag h, and only
// they can search by them.

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/fhazal/beeircd/internal/config"
	"github.com/fhazal/beeircd/internal/control"
	"github.com/fhazal/beeircd/internal/irc"
	"github.com/fhazal/beeircd/internal/state"
)

const (
	peoplePage    = 50
	peopleMaxPage = 200
	noticeMax     = 400
)

func (s *Server) isOperUser(u *state.User) bool { return u.Modes['o'] }

// brief is u's row; geo adds the country and network (flag h only).
func brief(u *state.User, geo bool) control.PersonBrief {
	b := control.PersonBrief{UID: u.UID, Nick: u.Nick, Host: u.Host, Server: u.Server, Account: u.Account,
		Oper: u.Modes['o'], Channels: len(u.Channels), SignOn: u.SignOn}
	if geo {
		b.Country, b.ASN, b.ASOrg = u.Country, u.ASN, u.ASOrg
	}
	return b
}

// geoOn reports whether GeoIP has a database loaded.
func (s *Server) geoOn() bool {
	return s.cfg.GeoIP.On() && (s.geo.set.Country != nil || s.geo.set.ASN != nil)
}

// geoHit reports whether q (lower case) is u's country code or name, or
// network number ("as4788", "4788") or owner.
func (s *Server) geoHit(u *state.User, q string, names map[string]string) bool {
	if u.Country != "" {
		if q == strings.ToLower(u.Country) {
			return true
		}
		name, ok := names[u.Country]
		if !ok {
			name = strings.ToLower(s.geo.set.Lookup(u.IP).Name)
			names[u.Country] = name
		}
		if len(q) >= 3 && strings.Contains(name, q) {
			return true
		}
	}
	if u.ASN != 0 {
		n := strings.TrimPrefix(q, "as")
		if n == strconv.FormatUint(uint64(u.ASN), 10) {
			return true
		}
		if len(q) >= 3 && strings.Contains(strings.ToLower(u.ASOrg), q) {
			return true
		}
	}
	return false
}

// panelPeople is one page of the People list for po.
func (s *Server) panelPeople(po *control.PanelOper, req control.Request) *control.People {
	seeHosts := strings.IndexByte(po.Flags, 'h') >= 0
	q := strings.ToLower(strings.TrimSpace(req.Query))
	if len(q) > 100 {
		q = q[:100]
	}
	geo := seeHosts && s.geoOn()
	out := &control.People{Geo: geo}
	names := map[string]string{}
	var match []*state.User
	for _, u := range s.net.Users() {
		if u.Service {
			continue
		}
		out.Online++
		oper := s.isOperUser(u)
		if u.Account != "" {
			out.LoggedIn++
		} else {
			out.Guests++
		}
		if oper {
			out.Opers++
		}
		switch req.Filter {
		case "in":
			if u.Account == "" {
				continue
			}
		case "guests":
			if u.Account != "" {
				continue
			}
		case "opers":
			if !oper {
				continue
			}
		}
		if q != "" {
			hit := strings.Contains(strings.ToLower(u.Nick), q) || strings.Contains(strings.ToLower(u.Host), q) ||
				strings.Contains(strings.ToLower(u.Account), q) || strings.Contains(strings.ToLower(u.Ident+"@"+u.Host), q)
			if !hit && seeHosts {
				hit = strings.Contains(strings.ToLower(u.RealHost), q) || strings.Contains(u.IP, q)
			}
			if !hit && geo {
				hit = s.geoHit(u, q, names)
			}
			if !hit {
				continue
			}
		}
		match = append(match, u)
	}
	sort.Slice(match, func(i, j int) bool { return irc.Fold(match[i].Nick) < irc.Fold(match[j].Nick) })
	out.Matched = len(match)
	limit := req.Limit
	if limit <= 0 {
		limit = peoplePage
	}
	limit = min(limit, peopleMaxPage)
	off := max(0, min(req.Offset, len(match)))
	out.Offset = off
	end := min(off+limit, len(match))
	out.Rows = make([]control.PersonBrief, 0, end-off)
	for _, u := range match[off:end] {
		out.Rows = append(out.Rows, brief(u, geo))
	}
	return out
}

// panelPerson is one person's details for po; nil when they're gone.
func (s *Server) panelPerson(po *control.PanelOper, uid string) *control.Person {
	u := s.net.UserByUID(uid)
	if u == nil || u.Service {
		return nil
	}
	seeHosts := strings.IndexByte(po.Flags, 'h') >= 0
	geo := seeHosts && s.geoOn()
	p := &control.Person{PersonBrief: brief(u, geo), Ident: u.Ident, Realname: u.Realname, TLS: u.TLS, Away: u.Away, Modes: u.ModeString()}
	if seeHosts {
		p.RealHost, p.IP = u.RealHost, irc.DisplayIP(u.IP)
	}
	if geo && u.Country != "" {
		if p.CountryName = s.geo.set.Lookup(u.IP).Name; p.CountryName == "" {
			p.CountryName = u.Country
		}
	}
	for _, ch := range u.Channels {
		pre := ""
		if m := ch.Members[u.UID]; m != nil {
			pre = m.Prefix()
		}
		p.Chans = append(p.Chans, pre+ch.Name)
	}
	sort.Slice(p.Chans, func(i, j int) bool {
		return irc.Fold(strings.TrimLeft(p.Chans[i], "~&@%+")) < irc.Fold(strings.TrimLeft(p.Chans[j], "~&@%+"))
	})
	if c := s.clients[u.UID]; c != nil {
		p.Local = true
		p.Idle = c.lastMsg
		if c.isOper() {
			p.OperAs = orNone(c.operClass) + " · " + c.operFlags
		}
	} else if u.Modes['o'] {
		p.OperAs = orNone(u.OperClass) + " · " + u.OperFlags
	}
	return p
}

// panelTarget finds the person an action is for; problem when gone.
func (s *Server) panelTarget(uid string) (*state.User, string) {
	u := s.net.UserByUID(uid)
	switch {
	case u == nil:
		return nil, "They have left."
	case u.Service:
		return nil, "That is a service."
	}
	return u, ""
}

func (s *Server) panelNotice(po *control.PanelOper, ip, uid, text string) control.Response {
	u, problem := s.panelTarget(uid)
	if problem != "" {
		return control.Response{Error: problem}
	}
	text = strings.Join(strings.Fields(text), " ")
	if text == "" {
		return control.Response{Error: "Type a message first."}
	}
	if utf8.RuneCountInString(text) > noticeMax {
		return control.Response{Error: fmt.Sprintf("That is too long (%d characters at most).", noticeMax)}
	}
	host{s}.ServerNotice(u.UID, "*** Notice from "+po.Account+": "+text)
	s.panelEvent(po.Account, ip, "sent a notice to "+u.Nick)
	return control.Response{Done: &control.Done{Text: "Notice sent to " + u.Nick + "."}}
}

func (s *Server) panelKill(po *control.PanelOper, ip, uid, reason string) control.Response {
	u, problem := s.panelTarget(uid)
	if problem != "" {
		return control.Response{Error: problem}
	}
	reason = strings.Join(strings.Fields(reason), " ")
	if reason == "" {
		reason = "Disconnected by an operator"
	}
	nick := u.Nick
	s.killUser(u, po.Account, reason)
	s.panelEvent(po.Account, ip, "disconnected "+nick+" ("+reason+")")
	return control.Response{Done: &control.Done{Text: nick + " was disconnected."}}
}

// panelLife reads a ban length: "" the default, "0" until removed.
func panelLife(v string) (time.Duration, bool) {
	switch v = strings.TrimSpace(strings.ToLower(v)); v {
	case "":
		return -1, true
	case "0":
		return 0, true
	}
	d, err := config.ParseBanDuration(v)
	if err != nil || d < time.Minute || d > 3650*24*time.Hour {
		return 0, false
	}
	return d, true
}

func (s *Server) panelBan(po *control.PanelOper, ip, uid, reason, life string) control.Response {
	u, problem := s.panelTarget(uid)
	if problem != "" {
		return control.Response{Error: problem}
	}
	d, ok := panelLife(life)
	if !ok {
		return control.Response{Error: "That ban length isn't one I know. Use 1h, 1d, 7d, 30d or 0 for until removed."}
	}
	if u.IP == "" {
		return control.Response{Error: "No address known for " + u.Nick + "."}
	}
	nick := u.Nick
	mask, removed, problem := s.svc.PanelAkill(po.Account, "*@"+irc.DisplayIP(u.IP), reason, d)
	if problem != "" {
		return control.Response{Error: problem}
	}
	how := "until removed"
	if d < 0 {
		d = s.cfg.Services.BanExpiry.Duration
	}
	if d > 0 {
		how = "for " + lengthWords(d)
	}
	shown := mask
	if strings.IndexByte(po.Flags, 'h') < 0 {
		shown = "their address" // no real addresses for opers without h
	}
	// What happened is for every oper: no address in it.
	s.panelEvent(po.Account, ip, fmt.Sprintf("banned %s from the network %s", nick, how))
	return control.Response{Done: &control.Done{Text: fmt.Sprintf("%s is banned (%s, %s). %d removed.", nick, shown, how, removed)}}
}
