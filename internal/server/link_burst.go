package server

// The burst. When a link comes up both sides send
// every user and channel they have; nick collisions are settled (L5) and
// channels merged by timestamp (L6). Users joining through a burst reach
// local clients inside an IRCv3 netjoin batch. A split takes the users
// behind it off the network.
//
// Live changes after the burst (NICK, JOIN, PRIVMSG ... over links) are
// in link_propagate.go.

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/fhazal/beeircd/internal/irc"
	"github.com/fhazal/beeircd/internal/link"
	"github.com/fhazal/beeircd/internal/state"
)

// behind reports whether server sid is reached through l.
func (s *Server) behind(l *linkConn, sid string) bool {
	r := s.links.servers[sid]
	return r != nil && r.via == l
}

// userHops is the hop count sent for a user of server sid.
func (s *Server) userHops(sid string) int {
	if si, ok := s.servicesInfo(); ok && sid == si.SID {
		return 2
	}
	return s.hopsOf(sid) + 1
}

// serverDescription is a server's description: ours, or a linked one's
// (WHOIS 312).
func (s *Server) serverDescription(name string) string {
	if r := s.serverByName(name); r != nil {
		return r.desc
	}
	if si, ok := s.servicesInfo(); ok && strings.EqualFold(si.Name, name) {
		return si.Description
	}
	return s.cfg.Server.Description
}

// uidServer is the server a UID belongs to ("" when it isn't a UID).
func uidServer(uid string) string {
	if len(uid) != 9 {
		return ""
	}
	return uid[:3]
}

// codecOf is l's codec, made once the handshake is done.
func (l *linkConn) codecOf() link.Codec {
	if l.codec == nil {
		l.codec = l.hs.Codec()
	}
	return l.codec
}

// ---- our burst ----

// burstBody is every user and channel not behind l, written for l.
func (s *Server) burstBody(l *linkConn) []string {
	cd := l.codecOf()
	var out []string
	users := s.net.Users()
	sort.Slice(users, func(i, j int) bool { return users[i].UID < users[j].UID })
	for _, u := range users {
		sid := uidServer(u.UID)
		if s.behind(l, sid) {
			continue
		}
		out = append(out, cd.User(s.linkUser(u, s.userHops(sid)), sid)...)
		if u.Away != "" {
			out = append(out, cd.Away(u.UID, u.Away))
		}
		for _, it := range s.metadataBurst(mdTarget{u: u}) {
			out = append(out, cd.Encode(it)...)
		}
	}
	chans := s.net.Channels()
	sort.Slice(chans, func(i, j int) bool { return chans[i].Name < chans[j].Name })
	for _, ch := range chans {
		out = append(out, s.channelBurst(l, ch)...)
	}
	out = append(out, s.readOnlyBurst(l)...)
	return append(out, s.banBurst(l)...)
}

// channelBurst is ch as a burst writes it for l (SJOIN and TOPIC), with
// the members not behind l; nothing when there are none.
func (s *Server) channelBurst(l *linkConn, ch *state.Channel) []string {
	cd := l.codecOf()
	lc := s.linkChannel(ch, func(uid string) bool { return !s.behind(l, uidServer(uid)) })
	if len(lc.Members) == 0 {
		return nil
	}
	out := cd.Channel(lc, s.cfg.Server.SID)
	if ch.Topic != "" {
		out = append(out, cd.Topic(link.Topic{Channel: ch.Name, Text: ch.Topic, By: orStr(ch.TopicBy, s.cfg.Server.Name), At: ch.TopicAt.Unix()}, s.cfg.Server.SID))
	}
	for _, it := range s.metadataBurst(mdTarget{ch: ch}) {
		out = append(out, cd.Encode(it)...)
	}
	return out
}

// linkChannel is ch as an SJOIN carries it: age, modes, lists and the
// members keep says (sorted by UID).
func (s *Server) linkChannel(ch *state.Channel, keep func(uid string) bool) link.Channel {
	lc := link.Channel{Name: ch.Name, TS: ch.TS, Args: map[byte]string{}}
	for uid, m := range ch.Members {
		if keep(uid) {
			lc.Members = append(lc.Members, link.Member{UID: uid, Ranks: rankLetters(m.Ranks)})
		}
	}
	sort.Slice(lc.Members, func(i, j int) bool { return lc.Members[i].UID < lc.Members[j].UID })
	lc.Modes = link.SortedModes(ch.Modes)
	for i := 0; i < len(lc.Modes); i++ {
		switch m := lc.Modes[i]; {
		case m == 'k':
			lc.Args[m] = ch.Key
		case m == 'l':
			lc.Args[m] = strconv.Itoa(ch.Limit)
		case state.IsParamMode(m):
			lc.Args[m] = ch.Param(m)
		}
	}
	for _, mode := range []byte{'b', 'e', 'I'} {
		for _, e := range ch.Lists[mode] {
			lc.Lists = append(lc.Lists, link.ListEntry{Mode: mode, Mask: e.Mask, SetBy: orStr(e.SetBy, s.cfg.Server.Name), SetAt: e.SetAt.Unix()})
		}
	}
	return lc
}

func (s *Server) linkUser(u *state.User, hops int) link.User {
	modes := link.SortedModes(u.Modes)
	if u.Service {
		// +S marks a service bot, as in UnrealIRCd: the other side keeps
		// it out of the people counts and lists.
		modes = link.SortedModes(withMode(u.Modes, 'S'))
	}
	return link.User{
		UID: u.UID, Nick: u.Nick, NickTS: u.NickTS, Hops: hops,
		Ident: u.Ident, RealHost: u.RealHost, Host: u.Host, Cloak: s.userCloak(u), IP: u.IP,
		Account: u.Account, Modes: modes, Realname: u.Realname,
	}
}

func withMode(m map[byte]bool, c byte) map[byte]bool {
	out := make(map[byte]bool, len(m)+1)
	for k, v := range m {
		out[k] = v
	}
	out[c] = true
	return out
}

// userCloak is the host +x gives u: ours made here, others' from their
// server; "" when cloaks are off (or a service).
func (s *Server) userCloak(u *state.User) string {
	switch {
	case u.Cloak != "":
		return u.Cloak
	case !s.ours(u.UID) || u.Service || !s.cfg.CloakEnabled():
		return ""
	}
	return s.cloakFor(u)
}

// rankLetters writes ranks as mode letters, highest first ("ov").
func rankLetters(r state.Rank) string {
	var b []byte
	for _, ri := range state.Ranks {
		if r&ri.Rank != 0 {
			b = append(b, ri.Mode)
		}
	}
	return string(b)
}

// ---- their burst ----

// linkItem handles a decoded burst line from l; false when m wasn't one.
func (s *Server) linkItem(l *linkConn, m irc.Message) bool {
	switch it := l.codecOf().Decode(m).(type) {
	case *link.User:
		s.remoteUser(l, m.Source, it)
	case *link.Channel:
		s.remoteSJoin(l, m.Source, it)
	case *link.Topic:
		s.remoteTopic(l, m.Source, it)
	case *link.Away:
		if u := s.net.UserByUID(it.UID); u != nil && s.behind(l, uidServer(u.UID)) && u.Away != it.Message {
			s.emitFrom(l, m.Source, &state.Away{UID: u.UID, Message: it.Message})
		}
	case *link.Kill:
		s.remoteKill(l, it)
	case *link.SvsKill:
		s.remoteSvsKill(l, it)
	case *link.SvsJoin:
		s.remoteSvsJoin(l, m, it)
	case *link.SvsPart:
		s.remoteSvsPart(l, m, it)
	default:
		return s.linkLive(l, m, it)
	}
	return true
}

// ignoreOnce logs something we skip, once per link and kind.
func (s *Server) ignoreOnce(l *linkConn, kind, format string, a ...any) {
	if l.ignored[kind] {
		return
	}
	l.ignored[kind] = true
	s.log.Printf("link %s: "+format, append([]any{l.peer.Name}, a...)...)
}

func (s *Server) remoteUser(l *linkConn, source string, in *link.User) {
	src := s.sourceServer(l, source)
	home := s.links.servers[uidServer(in.UID)]
	switch {
	case src == nil || home == nil || home.via != l:
		s.dropLink(l, fmt.Sprintf("UID %s (%s) from a server that isn't behind this link", in.UID, in.Nick))
		return
	case s.net.UserByUID(in.UID) != nil:
		s.dropLink(l, "UID collision: "+in.UID+" is on the network already")
		return
	}
	cd := l.codecOf()
	if !irc.ValidNick(in.Nick, 64) || strings.EqualFold(in.Nick, in.UID) {
		if !cd.Rename() {
			l.send(cd.Kill(s.cfg.Server.SID, in.UID, "Bad nickname"))
			s.ignoreOnce(l, "badnick:"+in.Nick, "killed %s: nick %q is not valid here", in.UID, in.Nick)
			return
		}
		in.Nick = in.UID
	}
	if ex := s.net.UserByNick(in.Nick); ex != nil && !s.ours(ex.UID) && uidServer(ex.UID) == uidServer(in.UID) {
		// One server never has two users with one nick, so the one we
		// hold is a ghost: we missed how it left. Drop it here; killing
		// the real user over it would be wrong.
		s.sno('l', "Dropped stale %s (%s): %s introduced the nick again as %s", ex.Nick, ex.UID, home.name, in.UID)
		s.emitQuiet(&state.UserQuit{UID: ex.UID, Reason: "Gone (" + home.name + " has the nick again)"})
	}
	if ex := s.net.UserByNick(in.Nick); ex != nil {
		if ex.Service && s.ours(ex.UID) {
			// A services server behind the link (Anope on UnrealIRCd):
			// one network, one set of services.
			s.dropLink(l, fmt.Sprintf("the network behind %s has its own services (%s on %s); only one server may run them, set [services] enabled = false here", l.peer.Name, in.Nick, home.name))
			return
		}
		keep, ok := s.collide(l, ex, in, home.name)
		if !ok {
			return // the newcomer was killed
		}
		if !keep {
			in.Nick = in.UID
		}
	}
	modes := map[byte]bool{}
	for i := 0; i < len(in.Modes); i++ {
		if strings.IndexByte(userModes, in.Modes[i]) >= 0 {
			modes[in.Modes[i]] = true
		}
	}
	u := &state.User{
		UID: in.UID, Nick: in.Nick, Ident: in.Ident, Host: in.Host, RealHost: in.RealHost,
		IP: in.IP, Realname: in.Realname, Server: home.name, Account: in.Account,
		SignOn: time.Unix(in.NickTS, 0), NickTS: in.NickTS, Modes: modes, TLS: modes['z'], Cloak: in.Cloak,
		Channels: map[string]*state.Channel{}, Service: strings.IndexByte(in.Modes, 'S') >= 0,
	}
	s.geoFill(u) // every server looks them up itself: nothing extra on the link
	if err := s.emitFrom(l, source, &state.UserAdd{User: u}); err != nil {
		s.dropLink(l, fmt.Sprintf("could not add %s (%s): %v", in.Nick, in.UID, err))
		return
	}
	if !u.Service && s.serverSynced(uidServer(u.UID)) {
		s.snoOnly('C', "Client connecting at "+u.Server+": "+snoMaskIP(u)+" ["+u.Realname+"\x0F]")
	}
}

// renames reports whether u's side settles collisions by renaming: a user
// here, or one behind a BeeIRCd link.
func (s *Server) renames(u *state.User) bool {
	sid := uidServer(u.UID)
	if sid == s.cfg.Server.SID {
		return true
	}
	r := s.links.servers[sid]
	return r != nil && r.via.codec != nil && r.via.codec.Rename()
}

// collide settles a nick collision between ex (on the network) and in
// (arriving from l). keep: in keeps its nick; ok false: in was killed
// and must not be added.
//
// Between BeeIRCd servers (L5) the loser is renamed to its UID: an
// identified user beats one who isn't, then the older nick wins, equal
// times rename both. Otherwise UnrealIRCd's rule (src/modules/nick.c):
// equal times kill both; with a different user@host the older wins, with
// the same user@host (a reconnect) the newer wins; the loser is killed.
func (s *Server) collide(l *linkConn, ex *state.User, in *link.User, inServer string) (keep, ok bool) {
	exServer := ex.Server
	rename := l.codecOf().Rename() && s.renames(ex)
	exWins, inWins := collisionWinner(rename,
		collider{ts: ex.NickTS, account: ex.Account, ident: ex.TrueIdent(), host: ex.RealHost},
		collider{ts: in.NickTS, account: in.Account, ident: in.Ident, host: in.RealHost})
	how := "renamed"
	if !rename {
		how = "killed"
	}
	switch {
	case exWins:
		s.sno('l', "Nick collision on %s: %s (%s) keeps it, %s (%s) %s", in.Nick, ex.UID, exServer, in.UID, inServer, how)
	case inWins:
		s.sno('l', "Nick collision on %s: %s (%s) keeps it, %s (%s) %s", in.Nick, in.UID, inServer, ex.UID, exServer, how)
	default:
		s.sno('l', "Nick collision on %s: %s (%s) and %s (%s) both %s (same time)", in.Nick, ex.UID, exServer, in.UID, inServer, how)
	}
	if rename {
		if !exWins {
			s.renameToUID(ex)
		}
		if t, ok := l.codecOf().(interface{ TellsRenames() bool }); ok && t.TellsRenames() && !inWins {
			// InspIRCd decides by its own rule; tell it that its user
			// lost here (its SAVE), so both sides agree on the nick.
			for _, line := range l.codecOf().Encode(&link.ForceNick{Source: s.cfg.Server.SID, UID: in.UID, Nick: in.UID, TS: in.NickTS}) {
				l.send(line)
			}
		}
		return inWins, true
	}
	comment := fmt.Sprintf("Nick collision: %s <- %s", exServer, inServer)
	if !exWins {
		s.killForCollision(ex, comment)
	}
	if !inWins {
		l.send(l.codecOf().Kill(s.cfg.Server.SID, in.UID, fmt.Sprintf("Nick collision: %s <- %s", inServer, exServer)))
		return false, false
	}
	return true, true
}

// collider is what a nick collision looks at.
type collider struct {
	ts          int64
	account     string
	ident, host string
}

// collisionWinner says who keeps the nick: rename is the BeeIRCd rule
// (L5), otherwise UnrealIRCd's (see collide). Both false: both lose.
func collisionWinner(rename bool, ex, in collider) (exWins, inWins bool) {
	if rename {
		exAcc, inAcc := ex.account != "", in.account != ""
		switch {
		case exAcc != inAcc:
			return exAcc, inAcc
		case ex.ts < in.ts:
			return true, false
		case ex.ts > in.ts:
			return false, true
		}
		return false, false
	}
	differ := !strings.EqualFold(ex.ident, in.ident) || !strings.EqualFold(ex.host, in.host)
	switch {
	case ex.ts == in.ts:
		return false, false
	case differ && ex.ts > in.ts, !differ && ex.ts < in.ts:
		return false, true
	}
	return true, false
}

// renameToUID gives u its UID as nick (collision loser, L5).
func (s *Server) renameToUID(u *state.User) {
	if c := s.clientOf(u); c != nil {
		s.stdReply(c, "NOTE", "*", "NICK_COLLISION", "Your nick "+u.Nick+" collided with someone on another server who had it first; you are now "+u.UID, "", u.Nick)
	}
	s.emit(&state.NickChange{UID: u.UID, Nick: u.UID, TS: time.Now().Unix()})
}

// killForCollision removes u (UnrealIRCd's rule) here and tells every link.
func (s *Server) killForCollision(u *state.User, comment string) {
	comment = unwrapKill(comment)
	s.svc.LogKill(u, s.cfg.Server.Name, comment)
	s.linkBroadcastKill(nil, s.cfg.Server.SID, u.UID, comment)
	if c := s.clientOf(u); c != nil {
		c.send(irc.Message{Source: s.cfg.Server.Name, Command: "KILL", Params: []string{u.Nick, comment}, Trailing: true}.String())
		s.origin = &linkOrigin{quiet: true} // every link has the KILL
		s.quit(c, "Killed ("+s.cfg.Server.Name+" ("+comment+"))")
		return
	}
	s.emitQuiet(&state.UserQuit{UID: u.UID, Reason: "Killed (" + s.cfg.Server.Name + " (" + comment + "))"})
}

// remoteKill is a KILL from l for a user anywhere.
func (s *Server) remoteKill(l *linkConn, k *link.Kill) {
	u := s.net.UserByUID(k.UID)
	if u == nil {
		return // gone already (both sides killed it in a collision)
	}
	by, shown, src := k.Source, k.Source, k.Source
	if r := s.links.servers[by]; r != nil {
		by, shown, src = r.name, r.name, r.name // a server's kill keeps its name
	} else if v := s.net.UserByUID(by); v != nil {
		by, shown, src = v.Nick, s.killedBy(v.Nick), v.Nick
		if shown != by {
			src = s.cfg.Server.Name // [messages] hide_killed_by: not the oper
		}
	}
	why := unwrapKill(k.Reason)
	reason := "Killed (" + shown + " (" + why + "))"
	s.svc.LogKill(u, by, why)
	s.sno('K', "Remote kill by %s: %s (%s)", by, snoRealMask(u), why)
	s.linkBroadcastKill(l, k.Source, u.UID, why) // on to the rest of the network
	if c := s.clientOf(u); c != nil {
		c.send(irc.Message{Source: src, Command: "KILL", Params: []string{u.Nick, why}, Trailing: true}.String())
		s.origin = &linkOrigin{quiet: true}
		s.quit(c, reason)
		return
	}
	s.snoRemoteQuit(u, reason)
	s.emitQuiet(&state.UserQuit{UID: u.UID, Reason: reason})
}

// remoteSvsKill is SVSKILL: services remove a user anywhere. UnrealIRCd
// sends it to every server and each drops the user with the reason as
// its quit message (src/modules/svskill.c).
func (s *Server) remoteSvsKill(l *linkConn, k *link.SvsKill) {
	u := s.userByUIDOrNick(k.UID)
	if u == nil || u.Service && s.ours(u.UID) {
		return
	}
	for _, o := range s.upLinks(l) {
		o.send(":" + k.Source + " SVSKILL " + u.UID + " :" + k.Reason)
	}
	reason := s.hideKillerIn(k.Reason)
	if c := s.clientOf(u); c != nil {
		s.origin = &linkOrigin{quiet: true} // every link has the SVSKILL
		s.quit(c, reason)
		return
	}
	s.snoRemoteQuit(u, reason)
	s.emitQuiet(&state.UserQuit{UID: u.UID, Reason: reason})
}

// hideKillerIn applies [messages] hide_killed_by to a quit reason shaped
// like a kill, "Killed (who (why))", as services send in SVSKILL; any
// other text stays as it is.
func (s *Server) hideKillerIn(reason string) string {
	if s.cfg.Messages.HideKilledBy == "" || !strings.HasPrefix(reason, "Killed (") || !strings.HasSuffix(reason, "))") {
		return reason
	}
	rest := reason[len("Killed ("):]
	i := strings.Index(rest, " (")
	if i <= 0 || strings.ContainsAny(rest[:i], " ()") {
		return reason
	}
	return "Killed (" + s.killedBy(rest[:i]) + rest[i:]
}

// supportedRank turns a rank letter from another server into one this
// server shows: with only @ and +, owner and admin become op, halfop
// voice.
func (s *Server) supportedRank(m byte) state.Rank {
	if strings.IndexByte(s.rankModes(), m) < 0 {
		switch m {
		case 'q', 'a':
			m = 'o'
		case 'h':
			m = 'v'
		}
	}
	return state.RankForMode(m)
}

// chanModeKnown reports whether m is a channel mode this server has
// (lists and ranks aside).
func chanModeKnown(m byte) bool {
	return strings.IndexByte(chanModesFlags+chanModesParam+chanModesKey, m) >= 0
}

// remoteSJoin merges a channel from l's burst (UnrealIRCd's SJOIN rule):
// an older channel wins its modes, lists and ranks; the same age merges;
// a younger one only adds members.
func (s *Server) remoteSJoin(l *linkConn, source string, c *link.Channel) {
	src := s.sourceServer(l, source)
	if src == nil || !irc.ValidChannel(c.Name, 200) {
		s.ignoreOnce(l, "sjoin:"+c.Name, "SJOIN for %q from %s skipped", c.Name, source)
		return
	}
	ch := s.net.Channel(c.Name)
	removeTheirs, merge := false, false
	if ch != nil {
		switch {
		case c.TS < ch.TS:
			s.sno('l', "Channel %s: %s has it from %s, older than ours; our modes and ranks are dropped", ch.Name, src.name, time.Unix(c.TS, 0).UTC().Format(time.DateTime))
			// The other links lose them too; l never had them.
			s.clearChannel(func(ev state.Event) error { return s.emitFrom(l, "", ev) }, ch, src.name)
			s.emitFrom(l, src.sid, &state.ChannelTS{Channel: ch.Name, TS: c.TS})
		case c.TS == ch.TS:
			merge = true
		default:
			removeTheirs = true
		}
	}
	for _, m := range c.Members {
		u := s.net.UserByUID(m.UID)
		if u == nil || !s.behind(l, uidServer(m.UID)) {
			s.ignoreOnce(l, "member:"+m.UID, "SJOIN %s: member %s unknown or not behind this link", c.Name, m.UID)
			continue
		}
		var ranks state.Rank
		if !removeTheirs {
			for i := 0; i < len(m.Ranks); i++ {
				ranks |= s.supportedRank(m.Ranks[i])
			}
		}
		if ch = s.net.Channel(c.Name); ch != nil && ch.Members[u.UID] != nil {
			if ranks != 0 {
				var chg []state.ModeChange
				for _, ri := range state.Ranks {
					if ranks&ri.Rank != 0 {
						chg = append(chg, state.ModeChange{Add: true, Mode: ri.Mode, UID: u.UID, Arg: u.Nick})
					}
				}
				s.emitFrom(l, src.sid, &state.ChannelMode{Channel: ch.Name, By: src.name, At: time.Now(), Changes: chg})
			}
			continue
		}
		s.emitFrom(l, src.sid, &state.Join{UID: u.UID, Channel: c.Name, TS: c.TS, Ranks: ranks, Burst: true})
	}
	ch = s.net.Channel(c.Name)
	if ch == nil || removeTheirs {
		return
	}
	s.sjoinModes(func(ev state.Event) error { return s.emitFrom(l, src.sid, ev) }, ch, c, src.name, merge, func(m byte) {
		s.ignoreOnce(l, "cmode:"+string(m), "channel mode %c from %s is not one we have; skipped", m, src.name)
	})
}

// sjoinModes gives ch the modes and lists of c, an SJOIN's copy of it that
// won or tied on age (merge: the larger key, limit and parameter win).
// Lists keep who set them and when; unknown is told of modes we lack.
func (s *Server) sjoinModes(emit func(state.Event) error, ch *state.Channel, c *link.Channel, by string, merge bool, unknown func(m byte)) {
	var chg []state.ModeChange
	for i := 0; i < len(c.Modes); i++ {
		m := c.Modes[i]
		arg := c.Args[m]
		switch {
		case !chanModeKnown(m):
			unknown(m)
			continue
		case m == 'k' && merge && ch.Modes['k'] && ch.Key >= arg:
			continue // the same age: the larger key wins on both sides
		case m == 'l' && merge && ch.Modes['l']:
			if n, _ := strconv.Atoi(arg); n <= ch.Limit {
				continue // the larger limit wins
			}
		case state.IsParamMode(m) && merge && ch.Modes[m] && ch.Param(m) >= arg:
			continue
		}
		chg = append(chg, state.ModeChange{Add: true, Mode: m, Arg: arg})
	}
	if len(chg) > 0 {
		emit(&state.ChannelMode{Channel: ch.Name, By: by, At: time.Now(), Changes: chg})
	}
	// Lists keep who set them and when: one event per setter and time.
	for i := 0; i < len(c.Lists); {
		j := i
		for j < len(c.Lists) && c.Lists[j].SetBy == c.Lists[i].SetBy && c.Lists[j].SetAt == c.Lists[i].SetAt {
			j++
		}
		var lc []state.ModeChange
		for _, e := range c.Lists[i:j] {
			lc = append(lc, state.ModeChange{Add: true, Mode: e.Mode, Arg: e.Mask})
		}
		at := time.Unix(c.Lists[i].SetAt, 0)
		if c.Lists[i].SetAt == 0 {
			at = time.Now()
		}
		emit(&state.ChannelMode{Channel: ch.Name, By: orStr(c.Lists[i].SetBy, by), At: at, Changes: lc})
		i = j
	}
}

// clearChannel takes away every mode, list entry and rank of ch, through
// emit: an older copy of it came over a link.
func (s *Server) clearChannel(emit func(state.Event) error, ch *state.Channel, by string) {
	var chg []state.ModeChange
	for _, m := range link.SortedModes(ch.Modes) {
		switch b := byte(m); {
		case b == 'k':
			chg = append(chg, state.ModeChange{Mode: b, Arg: ch.Key})
		default:
			chg = append(chg, state.ModeChange{Mode: b})
		}
	}
	for _, mode := range []byte{'b', 'e', 'I'} {
		for _, e := range ch.Lists[mode] {
			chg = append(chg, state.ModeChange{Mode: mode, Arg: e.Mask})
		}
	}
	uids := make([]string, 0, len(ch.Members))
	for uid := range ch.Members {
		uids = append(uids, uid)
	}
	sort.Strings(uids)
	for _, uid := range uids {
		mem := ch.Members[uid]
		for _, ri := range state.Ranks {
			if mem.Has(ri.Rank) {
				chg = append(chg, state.ModeChange{Mode: ri.Mode, UID: uid, Arg: mem.User.Nick})
			}
		}
	}
	if len(chg) > 0 {
		emit(&state.ChannelMode{Channel: ch.Name, By: by, At: time.Now(), Changes: chg})
	}
}

// remoteTopic: from a server (a burst) the newer topic wins (UnrealIRCd's
// rule); a person changing it always does.
func (s *Server) remoteTopic(l *linkConn, src string, t *link.Topic) {
	ch := s.net.Channel(t.Channel)
	if ch == nil {
		return
	}
	user := s.net.UserByUID(src)
	if user == nil && ch.Topic != "" && t.At <= ch.TopicAt.Unix() {
		return
	}
	if ch.Topic == t.Text && ch.TopicBy == t.By {
		return
	}
	source := ""
	if user != nil {
		source = user.UID
	}
	s.emitFrom(l, src, &state.Topic{Source: source, Channel: ch.Name, Text: t.Text, By: t.By, At: time.Unix(t.At, 0)})
}

// ---- splits ----

// quitSplit takes every user on the servers in gone off the network, with
// the classic "near far" quit reason (L8: real names).
func (s *Server) quitSplit(gone map[string]bool, near, far string) int {
	var uids []string
	for _, u := range s.net.Users() {
		if gone[uidServer(u.UID)] {
			uids = append(uids, u.UID)
		}
	}
	sort.Strings(uids)
	if len(uids) == 0 {
		return 0
	}
	// The QUITs reach clients with the batch capability inside an IRCv3
	// netsplit batch; with [link] hide_split non-opers see
	// "*.net *.split" instead of the server names (L8).
	s.batchSeq++
	b := &netjoinBatch{id: "ns" + strconv.FormatUint(s.batchSeq, 36), kind: "netsplit", near: near, far: far, started: map[*Client]bool{}}
	s.netsplit = b
	for _, uid := range uids {
		s.emitQuiet(&state.UserQuit{UID: uid, Reason: near + " " + far}) // every server sees the SQUIT
	}
	s.netsplit = nil
	b.end()
	return len(uids)
}

// ---- netjoin batch ----

// netjoinBatch wraps the JOINs a burst brings in an IRCv3 netjoin batch
// for clients with the batch capability.
type netjoinBatch struct {
	id       string
	kind     string // "netjoin" ("" too) or "netsplit"
	near     string
	far      string
	started  map[*Client]bool
	finished bool
}

// netjoinFor is l's batch while its burst is coming in.
func (s *Server) netjoinFor(l *linkConn) *netjoinBatch {
	if l.netjoin == nil {
		s.batchSeq++
		l.netjoin = &netjoinBatch{id: "nj" + strconv.FormatUint(s.batchSeq, 36), near: s.cfg.Server.Name, far: l.peer.Name, started: map[*Client]bool{}}
	}
	return l.netjoin
}

// tag puts line (a JOIN for c) in the batch, opening it for c first.
func (b *netjoinBatch) tag(c *Client, line string) string {
	if !b.started[c] {
		b.started[c] = true
		kind := orStr(b.kind, "netjoin")
		near, far := b.near, b.far
		if b.hidden(c) {
			near, far = "*.net", "*.split"
		}
		c.queueLine(":" + c.s.cfg.Server.Name + " BATCH +" + b.id + " " + kind + " " + near + " " + far + crlf)
	}
	return addTag(line, "batch", b.id)
}

// hidden reports whether c sees "*.net *.split" ([link] hide_split, not
// an oper).
func (b *netjoinBatch) hidden(c *Client) bool {
	return c.s.cfg.Link.HideSplit && !c.isOper()
}

// end closes the batch for everyone it was opened for.
func (b *netjoinBatch) end() {
	if b == nil || b.finished {
		return
	}
	b.finished = true
	cs := make([]*Client, 0, len(b.started))
	for c := range b.started {
		cs = append(cs, c)
	}
	sort.Slice(cs, func(i, j int) bool { return cs[i].uid < cs[j].uid })
	for _, c := range cs {
		if !c.closed {
			c.queueLine(":" + c.s.cfg.Server.Name + " BATCH -" + b.id + crlf)
		}
	}
}
