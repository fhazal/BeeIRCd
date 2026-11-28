package server

// Live propagation. Every change goes through
// emit; after it is applied here, linkOut writes it for every other link.
// Lines from a link become the same events (emitFrom), so local delivery
// code doesn't change, and they travel on to the other links but never
// back. PRIVMSG/NOTICE/TAGMSG and numerics aren't events: they are routed
// to the server of their target.

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/fhazal/beeircd/internal/irc"
	"github.com/fhazal/beeircd/internal/link"
	"github.com/fhazal/beeircd/internal/state"
)

// linkOrigin is where the next emitted event came from: a link (it is
// not sent back there) and the source on that link's line; quiet events
// go to no link at all (the other servers make the same change
// themselves, like the quits of a split).
type linkOrigin struct {
	l     *linkConn
	src   string
	quiet bool
}

// emitFrom applies an event that arrived over l from src.
func (s *Server) emitFrom(l *linkConn, src string, ev state.Event) error {
	s.origin = &linkOrigin{l: l, src: src}
	return s.emit(ev)
}

// emitQuiet applies an event no link hears about.
func (s *Server) emitQuiet(ev state.Event) error {
	s.origin = &linkOrigin{quiet: true}
	return s.emit(ev)
}

// isUID reports whether x is a UID (9 characters, a SID first).
func isUID(x string) bool { return len(x) == 9 && link.ValidSID(x[:3]) }

// upLinks are the links that are up, except one.
func (s *Server) upLinks(except *linkConn) []*linkConn {
	var out []*linkConn
	for o := range s.links.conns {
		if o != except && o.up && !o.closed {
			out = append(out, o)
		}
	}
	return out
}

// linkOf is the link a user is reached through, nil for ours.
func (s *Server) linkOf(uid string) *linkConn {
	if r := s.links.servers[uidServer(uid)]; r != nil {
		return r.via
	}
	return nil
}

// beeLink reports whether uid is reached through a link to BeeIRCd.
func (s *Server) beeLink(uid string) bool {
	o := s.linkOf(uid)
	return o != nil && o.peer.Bee
}

// ---- out: events to links ----

// linkOut writes ev (already applied) for every link it should reach.
func (s *Server) linkOut(ev state.Event, org *linkOrigin) {
	if len(s.links.conns) == 0 || (org != nil && org.quiet) {
		return
	}
	var from *linkConn
	src := s.cfg.Server.SID
	if org != nil {
		from = org.l
		if isSID(org.src) {
			src = org.src
		}
	}
	if inv, ok := ev.(*state.Invite); ok {
		// Only the target's server needs it.
		if t := s.linkOf(inv.UID); t != nil && t != from && t.up && !t.closed {
			for _, line := range s.linkLines(t, ev, src, from) {
				t.send(line)
			}
		}
		return
	}
	for _, o := range s.upLinks(from) {
		for _, line := range s.linkLines(o, ev, src, from) {
			o.send(line)
		}
	}
}

func isSID(x string) bool { return len(x) == 3 && link.ValidSID(x) }

// ours reports whether uid is a user on this server.
// The services bots count as ours: their pseudo-server hangs off us.
func (s *Server) ours(uid string) bool {
	sid := uidServer(uid)
	if sid == s.cfg.Server.SID {
		return true
	}
	_, ssid, ok := s.cfg.ServicesServer()
	return ok && sid == ssid
}

// servicesInfo is the services pseudo-server, when there is one.
func (s *Server) servicesInfo() (link.ServerInfo, bool) {
	name, sid, ok := s.cfg.ServicesServer()
	// Not "synced" in the burst: its EOS follows the SASL announcement
	// (saslAnnounce), which is when UnrealIRCd looks for a SASL server.
	return link.ServerInfo{Name: name, SID: sid, Description: "BeeIRCd Services", Uplink: s.cfg.Server.SID, Hops: 1}, ok
}

// hopsOf is how far server sid is from us (1 = linked to us).
func (s *Server) hopsOf(sid string) int {
	if r := s.links.servers[sid]; r != nil {
		return r.hops
	}
	return 0
}

// srcOr is uid when it is a user, else the server source.
func srcOr(uid, server string) string {
	if uid != "" {
		return uid
	}
	return server
}

// linkLines writes one applied event for link o. src is the server it
// comes from (ours, or the one that sent it); from the link it came in on.
func (s *Server) linkLines(o *linkConn, ev state.Event, src string, from *linkConn) []string {
	cd := o.codecOf()
	switch e := ev.(type) {
	case *state.UserAdd:
		u := e.User
		sid := uidServer(u.UID)
		out := cd.User(s.linkUser(u, s.userHops(sid)), sid)
		if u.Away != "" {
			out = append(out, cd.Away(u.UID, u.Away))
		}
		return out
	case *state.UserQuit:
		if s.ours(e.UID) || from != nil {
			return cd.Encode(&link.Quit{UID: e.UID, Reason: e.Reason})
		}
		// A user elsewhere that we removed (services, a ban): a KILL.
		return []string{cd.Kill(s.cfg.Server.SID, e.UID, e.Reason)}
	case *state.NickChange:
		if !s.ours(e.UID) && from == nil {
			// We renamed someone elsewhere (services, a collision): their
			// server does it (SVSNICK) and tells everyone.
			return cd.Encode(&link.ForceNick{Source: s.cfg.Server.SID, UID: e.UID, Nick: e.Nick, TS: e.TS})
		}
		return cd.Encode(&link.Nick{UID: e.UID, Nick: e.Nick, TS: e.TS})
	case *state.Join:
		ch := s.net.Channel(e.Channel)
		if ch == nil || ch.Members[e.UID] == nil {
			return nil
		}
		lc := link.Channel{Name: ch.Name, TS: ch.TS, Args: map[byte]string{},
			Members: []link.Member{{UID: e.UID, Ranks: rankLetters(ch.Members[e.UID].Ranks)}}}
		if e.Created && !e.Burst {
			lc.Modes, lc.Args = s.channelModeArgs(ch)
		}
		return cd.Channel(lc, src)
	case *state.Part:
		return cd.Encode(&link.Part{UID: e.UID, Channel: e.Channel, Reason: e.Reason})
	case *state.Kick:
		return cd.Encode(&link.Kick{Source: srcOr(e.Source, src), Channel: e.Channel, UID: e.UID, Reason: e.Reason})
	case *state.Topic:
		return []string{cd.Topic(link.Topic{Channel: e.Channel, Text: e.Text, By: orStr(e.By, s.cfg.Server.Name), At: e.At.Unix()}, srcOr(e.Source, src))}
	case *state.ChannelMode:
		var items []link.ModeItem
		for _, c := range e.Changes {
			it := link.ModeItem{Add: c.Add, Mode: c.Mode, Arg: c.Arg}
			switch {
			case c.UID != "":
				it.Arg = c.UID
			case c.Mode == 'k' && !c.Add:
				it.Arg = orStr(c.Arg, "*")
			case !c.Add && c.Mode != 'b' && c.Mode != 'e' && c.Mode != 'I':
				it.Arg = "" // only lists and k need an argument to go off
			}
			items = append(items, it)
		}
		if len(items) == 0 {
			return nil
		}
		return cd.Encode(&link.Mode{Source: srcOr(e.Source, src), Channel: e.Channel, Items: items})
	case *state.UserMode:
		m := modeString(e.Changes)
		if m == "" {
			return nil
		}
		source := s.cfg.Server.SID
		if s.ours(e.UID) || from != nil && uidServer(e.UID) == src {
			source = e.UID // their own change: UMODE2
		} else if from != nil {
			source = src
		}
		return cd.Encode(&link.UMode{Source: source, UID: e.UID, Modes: m})
	case *state.Away:
		return []string{cd.Away(e.UID, e.Message)}
	case *state.Login:
		return cd.Encode(&link.Login{Source: src, UID: e.UID, Account: e.Account})
	case *state.HostChange:
		u := s.net.UserByUID(e.UID)
		if u == nil {
			return nil
		}
		source := src
		if s.ours(e.UID) && from == nil {
			source = e.UID
		}
		out := cd.Encode(&link.Host{Source: source, UID: e.UID, Host: u.Host})
		if e.Ident != "" {
			out = append(out, cd.Encode(&link.Ident{Source: src, UID: e.UID, Ident: u.Ident})...)
		}
		return out
	case *state.RealnameChange:
		source := src
		if s.ours(e.UID) && from == nil {
			source = e.UID
		}
		return cd.Encode(&link.Realname{Source: source, UID: e.UID, Realname: e.Realname})
	case *state.Rename:
		return s.renameLinkLines(o, e, src)
	case *state.Invite:
		if e.Remove {
			if !o.peer.Bee && o.bridge == nil {
				return nil // UnrealIRCd has no server-to-server uninvite (its invite.c only sends INVITE; del_invite is local); InspIRCd's bridge carries ours
			}
			return []string{":" + srcOr(e.Source, src) + " UNINVITE " + e.UID + " " + e.Channel}
		}
		return cd.Encode(&link.Invite{Source: srcOr(e.Source, src), UID: e.UID, Channel: e.Channel})
	}
	return nil // ChannelTS, Reveal: each server works those out itself
}

// channelModeArgs are ch's non-list modes and their arguments, for SJOIN.
func (s *Server) channelModeArgs(ch *state.Channel) (string, map[byte]string) {
	args := map[byte]string{}
	modes := link.SortedModes(ch.Modes)
	for i := 0; i < len(modes); i++ {
		switch m := modes[i]; {
		case m == 'k':
			args[m] = ch.Key
		case m == 'l':
			args[m] = strconv.Itoa(ch.Limit)
		case state.IsParamMode(m):
			args[m] = ch.Param(m)
		}
	}
	return modes, args
}

// modeString writes user mode changes as "+x-i".
func modeString(chg []state.ModeChange) string {
	var b strings.Builder
	sign := byte(0)
	for _, c := range chg {
		s := byte('-')
		if c.Add {
			s = '+'
		}
		if s != sign {
			b.WriteByte(s)
			sign = s
		}
		b.WriteByte(c.Mode)
	}
	return b.String()
}

// ---- in: live lines from a link ----

// userBehind is the user uid (or nick) when it is reached through l.
func (s *Server) userBehind(l *linkConn, x string) *state.User {
	u := s.userByUIDOrNick(x)
	if u == nil || s.linkOf(u.UID) != l {
		return nil
	}
	return u
}

// userByUIDOrNick finds a user by UID, else by nick.
func (s *Server) userByUIDOrNick(x string) *state.User {
	if isUID(x) {
		if u := s.net.UserByUID(x); u != nil {
			return u
		}
	}
	return s.net.UserByNick(x)
}

// sourceName is how a link source (SID or UID, or a server name or nick, as
// UnrealIRCd names them on numerics and notices) shows to people: a server
// name or a nick.
func (s *Server) sourceName(src string) string {
	if r := s.links.servers[src]; r != nil {
		return r.name
	}
	if u := s.net.UserByUID(src); u != nil {
		return u.Nick
	}
	if r := s.serverByName(src); r != nil {
		return r.name
	}
	if u := s.net.UserByNick(src); u != nil {
		return u.Nick
	}
	return s.cfg.Server.Name
}

// linkLive handles a live change from l; false when it isn't one.
func (s *Server) linkLive(l *linkConn, m irc.Message, item any) bool {
	switch it := item.(type) {
	case *link.Nick:
		s.remoteNick(l, m.Source, it)
	case *link.Quit:
		if u := s.userBehind(l, it.UID); u != nil {
			s.snoRemoteQuit(u, it.Reason)
			s.emitFrom(l, m.Source, &state.UserQuit{UID: u.UID, Reason: it.Reason})
		}
	case *link.Part:
		if u := s.userBehind(l, it.UID); u != nil {
			for _, name := range strings.Split(it.Channel, ",") {
				if ch := s.net.Channel(name); ch != nil && ch.Members[u.UID] != nil {
					s.emitFrom(l, m.Source, &state.Part{UID: u.UID, Channel: ch.Name, Reason: it.Reason})
				}
			}
		}
	case *link.Kick:
		t, ch := s.userByUIDOrNick(it.UID), s.net.Channel(it.Channel)
		if t != nil && ch != nil && ch.Members[t.UID] != nil {
			s.emitFrom(l, m.Source, &state.Kick{Source: s.userSource(it.Source), UID: t.UID, Channel: ch.Name, Reason: it.Reason})
		}
	case *link.Mode:
		s.remoteMode(l, it)
	case *link.UMode:
		s.remoteUMode(l, it)
	case *link.Login:
		if u := s.userByUIDOrNick(it.UID); u == nil {
			s.remoteLogin(l, m.String(), it.Target, it.UID, it.Account)
		} else if u.Account != it.Account && s.linkLoginOK(l, u.UID, u.Nick, it.Account, u.Account) {
			s.emitFrom(l, m.Source, &state.Login{UID: u.UID, Account: it.Account})
		}
	case *link.Host:
		if u := s.userByUIDOrNick(it.UID); u != nil && it.Host != "" && u.Host != it.Host {
			if !validLinkHost(it.Host) {
				s.linkNoticeOnce(l, "badhost", "Ignored a bad host %q for %s from %s", it.Host, u.Nick, l.peer.Name)
				break
			}
			s.emitFrom(l, m.Source, &state.HostChange{UID: u.UID, Host: it.Host})
		}
	case *link.Ident:
		if u := s.userByUIDOrNick(it.UID); u != nil && it.Ident != "" && u.Ident != it.Ident {
			if !validLinkIdent(it.Ident) {
				s.linkNoticeOnce(l, "badident", "Ignored a bad ident %q for %s from %s", it.Ident, u.Nick, l.peer.Name)
				break
			}
			s.emitFrom(l, m.Source, &state.HostChange{UID: u.UID, Host: u.Host, Ident: it.Ident})
		}
	case *link.Realname:
		if u := s.userByUIDOrNick(it.UID); u != nil && u.Realname != it.Realname {
			s.emitFrom(l, m.Source, &state.RealnameChange{UID: u.UID, Realname: it.Realname})
		}
	case *link.Invite:
		t := s.userByUIDOrNick(it.UID)
		switch {
		case t == nil:
		case s.ours(t.UID):
			s.emitFrom(l, m.Source, &state.Invite{Source: s.userSource(it.Source), UID: t.UID, Channel: it.Channel})
		default:
			if o := s.linkOf(t.UID); o != nil && o != l {
				o.send(m.String()) // on towards the target
			}
		}
	case *link.ForceNick:
		s.remoteForceNick(l, m, it)
	case *link.Message:
		s.remoteMessage(l, m, it)
	case *link.Numeric:
		s.remoteNumeric(l, m, it)
	case *link.Ban:
		s.remoteBan(l, m, it)
	case *link.SpamFilter:
		s.remoteSpam(l, m, it)
	case *link.Bee:
		s.remoteBee(l, it)
	default:
		return false
	}
	return true
}

// userSource is src when it is a user on the network, else "" (a server).
func (s *Server) userSource(src string) string {
	if s.net.UserByUID(src) != nil {
		return src
	}
	return ""
}

// remoteNick is a nick change of a user behind l. A clash with someone
// else is settled like a burst collision (L5 or UnrealIRCd's rule).
func (s *Server) remoteNick(l *linkConn, src string, n *link.Nick) {
	u := s.userBehind(l, n.UID)
	if u == nil || u.Nick == n.Nick {
		return
	}
	nick := n.Nick
	if !irc.ValidNick(nick, 64) && nick != u.UID {
		l.send(l.codecOf().Kill(s.cfg.Server.SID, u.UID, "Bad nickname"))
		s.emitQuiet(&state.UserQuit{UID: u.UID, Reason: "Killed (" + s.cfg.Server.Name + " (Bad nickname))"})
		s.linkBroadcastKill(l, s.cfg.Server.SID, u.UID, "Bad nickname")
		return
	}
	if ex := s.net.UserByNick(nick); ex != nil && ex != u && ex.Service && s.ours(ex.UID) && !u.Service {
		s.killServiceNick(l, u.UID, nick, u.Server)
		s.emitQuiet(&state.UserQuit{UID: u.UID, Reason: "Killed (" + s.cfg.Server.Name + " (Nick collision with services))"})
		s.linkBroadcastKill(l, s.cfg.Server.SID, u.UID, "Nick collision with services ("+nick+")")
		return
	}
	if ex := s.net.UserByNick(nick); ex != nil && ex != u {
		in := &link.User{UID: u.UID, Nick: nick, NickTS: n.TS, Ident: u.TrueIdent(), RealHost: u.RealHost, Account: u.Account}
		keep, ok := s.collide(l, ex, in, u.Server)
		if !ok {
			// They lost under UnrealIRCd's rule: l got a KILL; the rest of
			// the network hears it too.
			s.emitQuiet(&state.UserQuit{UID: u.UID, Reason: "Killed (" + s.cfg.Server.Name + " (Nick collision))"})
			s.linkBroadcastKill(l, s.cfg.Server.SID, u.UID, "Nick collision")
			return
		}
		if !keep {
			nick = u.UID
		}
	}
	ts := n.TS
	if ts == 0 {
		ts = time.Now().Unix()
	}
	s.emitFrom(l, src, &state.NickChange{UID: u.UID, Nick: nick, TS: ts})
}

// remoteGlobops is oper chat from elsewhere (":uid GLOBOPS :text" from an
// oper, or from a server with the whole line "From nick: text"): the opers
// here with the G letter get "*** GLOBOPS: From <nick>: <text>", and the
// other links get it on.
func (s *Server) remoteGlobops(l *linkConn, m irc.Message) {
	if len(m.Params) == 0 || m.Params[len(m.Params)-1] == "" {
		return
	}
	text := m.Params[len(m.Params)-1]
	_, u, ok := s.linkSource(l, m.Source)
	if !ok {
		return
	}
	if u != nil {
		if !u.Modes['o'] {
			return
		}
		text = "From " + u.Nick + ": " + text
	}
	s.sno('G', "%s", text)
	for _, o := range s.upLinks(l) {
		if u == nil && !o.peer.Bee && o.bridge == nil {
			o.send(s.unrealGlobops(m.Source, text))
			continue
		}
		o.send(m.String())
	}
}

// remoteWallops is a WALLOPS from elsewhere (":uid WALLOPS :text", or
// from a server): from an oper or a server it shows to everyone here with
// +w and goes on to the other links; from anyone else it is dropped.
func (s *Server) remoteWallops(l *linkConn, m irc.Message) {
	if len(m.Params) == 0 || m.Params[len(m.Params)-1] == "" {
		return
	}
	text := m.Params[len(m.Params)-1]
	srv, u, ok := s.linkSource(l, m.Source)
	if !ok {
		return
	}
	src := ""
	switch {
	case u != nil:
		if !u.Modes['o'] {
			return
		}
		src = u.Prefix()
	case srv != nil:
		src = srv.name
	default:
		return
	}
	s.localWallops(src, text)
	line := irc.Message{Source: m.Source, Command: "WALLOPS", Params: []string{text}, Trailing: true}.String()
	for _, o := range s.upLinks(l) {
		o.send(line)
	}
}

// unrealGlobops is a server's GLOBOPS "From nick: text" the way UnrealIRCd
// sends one: from the person (":uid GLOBOPS :text", shown there as "from
// nick: text") when nick is someone known, else ":sid SENDUMODE o :from
// nick: text"; a line without a nick goes as it is.
func (s *Server) unrealGlobops(src, text string) string {
	rest, ok := strings.CutPrefix(text, "From ")
	nick, msg, ok2 := strings.Cut(rest, ": ")
	if !ok || !ok2 || nick == "" || strings.ContainsAny(nick, " ") {
		return irc.Message{Source: src, Command: "GLOBOPS", Params: []string{text}, Trailing: true}.String()
	}
	if u := s.net.UserByNick(nick); u != nil {
		return irc.Message{Source: u.UID, Command: "GLOBOPS", Params: []string{msg}, Trailing: true}.String()
	}
	return irc.Message{Source: src, Command: "SENDUMODE", Params: []string{"o", "from " + nick + ": " + msg}, Trailing: true}.String()
}

// remoteSnotice is a server notice from a server behind an InspIRCd link
// (":sid SENDSNO <letter> :text", the bridge's form of InspIRCd 4's
// SNONOTICE with the letter already ours): our opers with that letter
// get it as "From <server>: text", as InspIRCd shows them. It goes no
// further. UnrealIRCd's SENDSNO uses its own letters, so it is ignored.
func (s *Server) remoteSnotice(l *linkConn, m irc.Message) {
	if l.bridge == nil || len(m.Params) != 2 || len(m.Params[0]) != 1 || m.Params[1] == "" ||
		strings.IndexByte(snomaskLetters, m.Params[0][0]) < 0 {
		return
	}
	r := s.links.servers[m.Source]
	if r == nil || r.via != l {
		return
	}
	s.sno(m.Params[0][0], "From %s: %s", r.name, m.Params[1])
}

// remoteSendUmode is UnrealIRCd's ":sid SENDUMODE o :from nick: text", how
// a GLOBOPS crosses its links. Only that form, from an oper (or a server or
// services bot) behind l, is oper chat; every other SENDUMODE is left alone.
func (s *Server) remoteSendUmode(l *linkConn, m irc.Message) {
	if l.peer.Bee || l.bridge != nil || len(m.Params) != 2 || strings.TrimPrefix(m.Params[0], "+") != "o" {
		return
	}
	rest, ok := strings.CutPrefix(m.Params[1], "from ")
	if !ok {
		return
	}
	name, text, ok := strings.Cut(rest, ": ")
	if !ok || name == "" || text == "" || strings.ContainsAny(name, " ") {
		return
	}
	if srv, u, ok := s.linkSource(l, name); ok && (srv != nil || u != nil) {
		if u != nil && u.Modes['o'] {
			s.remoteGlobops(l, irc.Message{Source: u.UID, Command: "GLOBOPS", Params: []string{text}, Trailing: true})
			return
		}
		if u == nil || u.Service {
			s.remoteGlobops(l, irc.Message{Source: uidServer(m.Source), Command: "GLOBOPS", Params: []string{"From " + name + ": " + text}, Trailing: true})
		}
	}
}

// linkBroadcastKill tells every link but one that uid was killed.
func (s *Server) linkBroadcastKill(except *linkConn, src, uid, reason string) {
	for _, o := range s.upLinks(except) {
		o.send(o.codecOf().Kill(src, uid, reason))
	}
}

// remoteForceNick is SVSNICK: services rename one of our users.
func (s *Server) remoteForceNick(l *linkConn, m irc.Message, f *link.ForceNick) {
	u := s.userByUIDOrNick(f.UID)
	switch {
	case u == nil || u.Nick == f.Nick:
	case !s.ours(u.UID):
		if o := s.linkOf(u.UID); o != nil && o != l {
			o.send(m.String())
		}
	case !irc.ValidNick(f.Nick, s.cfg.Limits.NickLen) && f.Nick != u.UID:
	case s.net.UserByNick(f.Nick) != nil && !irc.EqualFold(f.Nick, u.Nick):
	default:
		s.emit(&state.NickChange{UID: u.UID, Nick: f.Nick, TS: time.Now().Unix()})
	}
}

// remoteMode is a channel MODE from l.
func (s *Server) remoteMode(l *linkConn, m *link.Mode) {
	ch := s.net.Channel(m.Channel)
	if ch == nil || len(m.Items) == 0 {
		return
	}
	var chg []state.ModeChange
	for _, it := range m.Items {
		c := state.ModeChange{Add: it.Add, Mode: it.Mode, Arg: it.Arg}
		switch {
		case state.RankForMode(it.Mode) != 0 || strings.IndexByte("qaohv", it.Mode) >= 0:
			t := s.userByUIDOrNick(it.Arg)
			if t == nil || ch.Members[t.UID] == nil {
				continue
			}
			r := s.supportedRank(it.Mode)
			mode := byte(0)
			for _, ri := range state.Ranks {
				if ri.Rank == r {
					mode = ri.Mode
				}
			}
			if mode == 0 {
				continue
			}
			c.Mode, c.UID, c.Arg = mode, t.UID, t.Nick
		case it.Mode == 'b' || it.Mode == 'e' || it.Mode == 'I':
			if it.Arg == "" {
				continue
			}
		case !chanModeKnown(it.Mode):
			s.ignoreOnce(l, "cmode:"+string(it.Mode), "channel mode %c from %s is not one we have; skipped", it.Mode, l.peer.Name)
			continue
		case it.Mode == 'k' && !it.Add:
			c.Arg = ch.Key
		case !it.Add:
			c.Arg = ""
		}
		chg = append(chg, c)
	}
	if len(chg) == 0 {
		return
	}
	by := s.sourceName(m.Source)
	if u := s.net.UserByUID(m.Source); u != nil {
		by = u.Prefix()
	}
	s.emitFrom(l, m.Source, &state.ChannelMode{Source: s.userSource(m.Source), Channel: ch.Name, By: by, At: time.Now(), Changes: chg})
}

// remoteUMode is UMODE2/SVS2MODE/MODE on a user. A user's +x shows the
// cloak their server gave them (the UID line), -x their real host.
func (s *Server) remoteUMode(l *linkConn, m *link.UMode) {
	u := s.userByUIDOrNick(m.UID)
	if u == nil {
		return
	}
	if m.Login != nil && u.Account != m.Login.Account && s.linkLoginOK(l, u.UID, u.Nick, m.Login.Account, u.Account) {
		s.emitFrom(l, m.Source, &state.Login{UID: u.UID, Account: m.Login.Account})
	}
	if strings.Trim(m.Modes, "+-") == "" {
		return
	}
	if s.ours(u.UID) {
		// Services elsewhere change one of ours (SVS2MODE +r, -o ...).
		s.svsMode(u, m.Modes)
		return
	}
	if s.linkOf(u.UID) != l && !isSID(m.Source) {
		return
	}
	var chg []state.ModeChange
	add := true
	for i := 0; i < len(m.Modes); i++ {
		switch c := m.Modes[i]; {
		case c == '+':
			add = true
		case c == '-':
			add = false
		case strings.IndexByte(userModes, c) >= 0:
			chg = append(chg, state.ModeChange{Add: add, Mode: c})
		}
	}
	if len(chg) == 0 {
		return
	}
	hadX := u.Modes['x']
	s.emitFrom(l, m.Source, &state.UserMode{UID: u.UID, Changes: chg})
	switch {
	case !hadX && u.Modes['x'] && u.Cloak != "" && u.Host == u.RealHost:
		s.emitQuiet(&state.HostChange{UID: u.UID, Host: u.Cloak})
	case hadX && !u.Modes['x'] && u.Host == u.Cloak:
		s.emitQuiet(&state.HostChange{UID: u.UID, Host: u.RealHost})
	}
}

// svsMode applies a mode change another server's services make to one
// of our users; the result travels on as UMODE2 from the user.
func (s *Server) svsMode(u *state.User, modes string) {
	if chg := svsChanges(modes); len(chg) > 0 {
		s.emit(&state.UserMode{UID: u.UID, Changes: chg})
	}
}

// svsChanges are the user modes in modes that one server may set on
// another's user: known ones, no +o, no z.
func svsChanges(modes string) []state.ModeChange {
	var chg []state.ModeChange
	add := true
	for i := 0; i < len(modes); i++ {
		switch c := modes[i]; {
		case c == '+':
			add = true
		case c == '-':
			add = false
		case c == 'o' && add, c == 'z':
			// Nobody becomes an oper here from elsewhere; +z is TLS.
		case strings.IndexByte(userModes, c) >= 0:
			chg = append(chg, state.ModeChange{Add: add, Mode: c})
		}
	}
	return chg
}

// ---- messages ----

// maskHere reports whether a "$mask" target names this server.
func (s *Server) maskHere(target string) bool {
	return irc.Match(target[1:], s.cfg.Server.Name)
}

// deliverMask gives a "$mask" message to everyone here except skip.
func (s *Server) deliverMask(o *outMsg, skip *Client) {
	for _, c := range s.clients {
		if c.user != nil && c != skip {
			c.deliver(o)
		}
	}
}

// routeChannel sends a channel message on to every link with members of
// ch, except one. status is a prefix like "@" or "".
func (s *Server) routeChannel(except *linkConn, src string, ch *state.Channel, status, verb, text string, tags map[string]string) {
	if len(s.links.conns) == 0 {
		return
	}
	want := map[*linkConn]bool{}
	for uid := range ch.Members {
		if o := s.linkOf(uid); o != nil && o != except && o.up && !o.closed {
			want[o] = true
		}
	}
	for o := range want {
		for _, line := range o.codecOf().Encode(&link.Message{Source: src, Verb: verb, Target: status + ch.Name, Text: text, Tags: tags}) {
			o.send(line)
		}
	}
}

// routeUser sends a private message to t on another server.
func (s *Server) routeUser(src string, t *state.User, verb, text string, tags map[string]string) bool {
	o := s.linkOf(t.UID)
	if o == nil || !o.up || o.closed {
		return false
	}
	for _, line := range o.codecOf().Encode(&link.Message{Source: src, Verb: verb, Target: t.UID, Text: text, Tags: tags}) {
		o.send(line)
	}
	return true
}

// linkTags are the tags a message carries over a link: the client-only
// ones and its msgid.
func linkTags(tags map[string]string, msgid string) map[string]string {
	out := map[string]string{}
	for k, v := range tags {
		out[k] = v
	}
	if msgid != "" {
		out["msgid"] = msgid
	}
	return out
}

// remoteMessage delivers PRIVMSG/NOTICE/TAGMSG from l to people here and
// sends it on to other links that need it.
func (s *Server) remoteMessage(l *linkConn, raw irc.Message, m *link.Message) {
	from := s.net.UserByUID(m.Source)
	if from == nil && s.links.servers[m.Source] == nil {
		if u := s.net.UserByNick(m.Source); u != nil {
			from = u
		}
	}
	if from == nil && !isSID(m.Source) && s.serverByName(m.Source) == nil {
		return
	}
	if from != nil && m.Source != from.UID {
		// UnrealIRCd may name the sender by nick on channel messages; links
		// further on (InspIRCd) only take a UID, so pass it on as one.
		m.Source, raw.Source = from.UID, from.UID
	}
	msgid := raw.Tags["msgid"]
	if msgid == "" {
		msgid = s.newMsgID()
	}
	tags := clientTags(raw.Tags)
	build := func(to string) *outMsg {
		params := []string{to}
		if m.Verb != "TAGMSG" {
			params = append(params, m.Text)
		}
		var o *outMsg
		if from != nil {
			o = s.out(from, m.Verb, params...)
		} else {
			o = s.outFrom("", m.Verb, params...)
			o.msg.Source = s.sourceName(m.Source)
		}
		o.msg.Trailing = m.Verb != "TAGMSG"
		o.tags, o.msgid = tags, msgid
		if m.Verb == "TAGMSG" {
			o.needCap = "message-tags"
		}
		return o
	}
	target := m.Target
	switch {
	case strings.HasPrefix(target, "$"):
		// $mask: a global notice to everyone on matching servers.
		if s.maskHere(target) {
			s.deliverMask(build(target), nil)
		}
		for _, o := range s.upLinks(l) {
			o.send(raw.String())
		}
		return
	}
	status, name := "", target
	if len(name) > 1 && name[0] != '#' {
		if i := strings.IndexByte(name, '#'); i > 0 {
			status, name = name[:i], name[i:]
		}
	}
	if irc.IsChannel(name) {
		ch := s.net.Channel(name)
		if ch == nil {
			return
		}
		var min state.Rank
		if status != "" {
			min = state.RankForPrefix(status[len(status)-1])
		}
		o := build(status + ch.Name)
		for _, mem := range ch.Members {
			if !s.ours(mem.User.UID) || mem.User == from || mem.User.HasMode('d') {
				continue
			}
			if min != 0 && !mem.AtLeast(min) {
				continue
			}
			s.deliverTo(mem.User, o)
		}
		if status == "" && m.Verb != "TAGMSG" {
			s.recordHistory(ch, o)
			s.noteMsg(msgid, from, ch.Name)
		}
		if status == "" && m.Verb == "PRIVMSG" && from != nil {
			s.noteTalk(ch, from.UID)                // Hive Panel Spotlight
			s.svc.ChannelText(from.UID, ch, m.Text) // BotServ kickers for people on other servers
		}
		s.routeChannel(l, m.Source, ch, status, m.Verb, m.Text, linkTags(tags, msgid))
		return
	}
	t := s.userByUIDOrNick(name)
	if t == nil {
		return
	}
	if !s.ours(t.UID) {
		if o := s.linkOf(t.UID); o != nil && o != l && !s.farCallerBlocked(t, from, m.Verb == "PRIVMSG") {
			s.routeUser(m.Source, t, m.Verb, m.Text, linkTags(tags, msgid))
		}
		return
	}
	if from != nil && s.silenced(t, from) {
		return
	}
	if s.callerBlocked(t, from, m.Verb == "PRIVMSG") || s.farCommonChansBlocked(t, from, m.Verb == "PRIVMSG") {
		return
	}
	if file, ok := dccFile(m.Text); ok && m.Verb != "TAGMSG" && s.dccSoftBlocked(from, t, file) {
		return
	}
	if s.svc.IsBot(t) {
		if m.Verb == "PRIVMSG" && from != nil {
			s.svc.Message(from.UID, t, m.Text)
		}
		return
	}
	if m.Verb != "TAGMSG" {
		s.noteMsg(msgid, from, t.Nick)
	}
	s.deliverTo(t, build(t.Nick))
}

// remoteNumeric is a numeric reply for someone here (or further on).
func (s *Server) remoteNumeric(l *linkConn, raw irc.Message, n *link.Numeric) {
	t := s.userByUIDOrNick(n.Params[0])
	if t == nil {
		return
	}
	if !s.ours(t.UID) {
		if o := s.linkOf(t.UID); o != nil && o != l {
			o.send(raw.String())
		}
		return
	}
	c := s.clientOf(t)
	if c == nil {
		return
	}
	p := append([]string{c.target()}, n.Params[1:]...)
	c.send(irc.Message{Source: s.sourceName(n.Source), Command: n.Code, Params: p, Trailing: len(p) > 1}.String())
}

// ---- to users elsewhere (services on the hub, KILL) ----

// sendRemote writes item for the link behind uid; false when uid is here
// or nowhere.
func (s *Server) sendRemote(uid string, item any) bool {
	o := s.linkOf(uid)
	if o == nil || !o.up || o.closed {
		return false
	}
	for _, line := range o.codecOf().Encode(item) {
		o.send(line)
	}
	return true
}

// killRemote removes a user on another server: KILL to every link.
func (s *Server) killRemote(u *state.User, by, reason string) {
	reason = unwrapKill(reason)
	src := s.cfg.Server.SID
	if v := s.net.UserByNick(by); v != nil {
		src = v.UID
	}
	s.sno('K', "Remote kill by %s: %s (%s)", by, snoRealMask(u), reason)
	s.linkBroadcastKill(nil, src, u.UID, reason)
	shown := fmt.Sprintf("Killed (%s (%s))", s.killedBy(by), reason)
	s.snoRemoteQuit(u, shown)
	s.emitQuiet(&state.UserQuit{UID: u.UID, Reason: shown})
}
