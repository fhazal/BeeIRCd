package server

import (
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/fhazal/beeircd/internal/irc"
	"github.com/fhazal/beeircd/internal/link"
	"github.com/fhazal/beeircd/internal/services"
	"github.com/fhazal/beeircd/internal/state"
	"github.com/fhazal/beeircd/internal/store"
	"github.com/fhazal/beeircd/internal/version"
)

// IRCv3 draft/channel-rename: RENAME #old #new [reason] gives a channel a
// new name with everyone, its modes, lists, topic, history, read markers
// and ChanServ registration still in it. Clients with the cap get RENAME;
// the others see themselves leave the old name and join the new one.
// BeeIRCd servers pass it on (BEE RENAME); a rename that someone on another
// IRCd couldn't follow is refused, so the channel never splits in two.

const (
	renameCap      = "draft/channel-rename"
	renameCooldown = 10 * time.Minute // one rename per channel this often
)

// renameMinVersion is the first BeeIRCd release that knows BEE RENAME.
var renameMinVersion = [2]int{1, 5}

// renameRefusal is why a rename can't happen: a numeric when there is one
// for it, else a FAIL code.
type renameRefusal struct {
	numeric string
	code    string // CANNOT_RENAME, CHANNEL_NAME_IN_USE
	text    string
}

func cannotRename(text string) *renameRefusal {
	return &renameRefusal{code: "CANNOT_RENAME", text: text}
}

func (s *Server) renameFail(c *Client, code string, params ...string) {
	c.send(irc.Message{Source: s.cfg.Server.Name, Command: "FAIL", Params: append([]string{"RENAME", code}, params...), Trailing: true}.String())
}

// cmdRename is RENAME <old> <new> [reason].
func (s *Server) cmdRename(c *Client, m irc.Message) {
	if !s.cfg.Server.IRCv3Drafts {
		s.reply(c, irc.ErrUnknownCommand, "RENAME", "Unknown command")
		return
	}
	if len(m.Params) < 2 || m.Params[0] == "" || m.Params[1] == "" {
		s.reply(c, irc.ErrNeedMoreParams, "RENAME", "Not enough parameters")
		return
	}
	oldName, newName := m.Params[0], m.Params[1]
	reason := irc.Truncate(strings.TrimSpace(m.Param(2)), s.cfg.Limits.KickLen)
	ch, no := s.renameCheck(c.user, c.isOper(), oldName, newName)
	if no != nil {
		if no.numeric != "" {
			s.reply(c, no.numeric, orStr(chName(ch), oldName), no.text)
		} else {
			s.renameFail(c, no.code, orStr(chName(ch), oldName), newName, no.text)
		}
		return
	}
	from := ch.Name
	if err := s.emit(&state.Rename{Source: c.user.UID, Old: from, New: newName, Reason: reason}); err != nil {
		s.renameFail(c, "CANNOT_RENAME", from, newName, err.Error())
		return
	}
	if ch.Member(c.user) == nil { // an oper from outside: they see nothing else
		s.notice(c, "Renamed "+from+" to "+ch.Name+".")
	}
	s.log.Printf("RENAME %s to %s by %s", from, ch.Name, c.user.Nick)
}

func chName(ch *state.Channel) string {
	if ch == nil {
		return ""
	}
	return ch.Name
}

// renameCheck says whether u (nil: the Hive Panel; oper: an IRC operator)
// may rename channel oldName to newName, and returns the channel.
func (s *Server) renameCheck(u *state.User, oper bool, oldName, newName string) (*state.Channel, *renameRefusal) {
	ch := s.net.Channel(oldName)
	if ch == nil {
		return nil, &renameRefusal{numeric: irc.ErrNoSuchChannel, text: "No such channel"}
	}
	switch {
	case !irc.ValidChannel(newName, s.cfg.Limits.ChannelLen):
		return ch, cannotRename(newName + " is not a valid channel name")
	case newName[0] != ch.Name[0]:
		return ch, cannotRename("A rename keeps the channel type: the new name must start with " + ch.Name[:1])
	case newName == ch.Name:
		return ch, cannotRename("That is already the channel's name")
	}
	registered := s.renameRegistered(ch)
	if registered && s.svc.Off() {
		return ch, cannotRename(ch.Name + " is registered and services run on another server: rename it from there")
	}
	if u != nil && !oper {
		if registered {
			if _, ok, why := s.svc.RenameRights(u.UID, ch.Name); !ok {
				return ch, &renameRefusal{numeric: irc.ErrChanOPrivsNeeded, text: why}
			}
		} else if mem := ch.Member(u); mem == nil {
			return ch, &renameRefusal{numeric: irc.ErrNotOnChannel, text: "You're not on that channel"}
		} else if !mem.AtLeast(state.RankOp) {
			return ch, &renameRefusal{numeric: irc.ErrChanOPrivsNeeded, text: "You're not channel operator"}
		}
	}
	if s.readOnly() { // a rename moves stored data, registered or not
		return ch, cannotRename(services.ReadonlyRefusal)
	}
	if irc.Fold(newName) != irc.Fold(ch.Name) { // not a change of case only
		if s.net.Channel(newName) != nil {
			return ch, &renameRefusal{code: "CHANNEL_NAME_IN_USE", text: newName + " is already in use"}
		}
		if !s.svc.Off() && s.svc.Registered(newName) {
			return ch, &renameRefusal{code: "CHANNEL_NAME_IN_USE", text: newName + " is registered to another channel"}
		}
		if f, ok := s.svc.Forbidden(store.ForbidChan, newName); ok {
			return ch, cannotRename(newName + " is forbidden by the network: " + orStr(f.Reason, "no reason given"))
		}
	}
	if at, ok := s.renamedAt[irc.Fold(ch.Name)]; ok {
		if left := renameCooldown - time.Since(at); left > 0 {
			return ch, cannotRename(ch.Name + " was renamed a moment ago; it can be renamed again in " + minutesText(left))
		}
	}
	for uid := range ch.Members {
		if s.ours(uid) {
			continue
		}
		if server, sw := s.renameRoute(uidServer(uid)); server != "" {
			nick := uid
			if mu := s.net.UserByUID(uid); mu != nil {
				nick = mu.Nick
			}
			return ch, cannotRename(nick + " is on " + server + ", which runs " + sw +
				" and can't rename channels; renaming now would split " + ch.Name + " in two")
		}
	}
	return ch, nil
}

func minutesText(d time.Duration) string {
	n := int((d + time.Minute - 1) / time.Minute)
	if n <= 1 {
		return "1 minute"
	}
	return strconv.Itoa(n) + " minutes"
}

// renameRegistered reports whether ch is registered with ChanServ (+r when
// services run on another server).
func (s *Server) renameRegistered(ch *state.Channel) bool {
	if s.svc.Off() {
		return ch.HasMode('r')
	}
	return s.svc.Registered(ch.Name)
}

// renameRoute is the server (and what it runs) on the way to server sid
// that can't follow a rename, or "" when every server there can: each one
// must be a BeeIRCd that knows RENAME, linked the BeeIRCd way.
func (s *Server) renameRoute(sid string) (server, software string) {
	for hops := 0; sid != "" && sid != s.cfg.Server.SID && hops < 64; hops++ {
		r := s.links.servers[sid]
		if r == nil {
			return sid, "unknown software"
		}
		if r.via == nil {
			return r.name, orStr(r.software, "unknown software")
		}
		if !renameLink(r.via) {
			return r.via.peer.Name, orStr(r.via.peer.Software, "unknown software")
		}
		if !renameAware(r.software) {
			return r.name, orStr(r.software, "unknown software")
		}
		sid = r.uplink
	}
	return "", ""
}

// renameLink reports whether link o gets BEE RENAME: a direct BeeIRCd
// link (not a bridge) running a release that knows it. Other links get
// the rename spelled out as parts and a new channel.
func renameLink(o *linkConn) bool {
	return o.peer.Bee && o.bridge == nil && renameAware(o.peer.Software)
}

// renameAware reports whether software, as a handshake ("BeeIRCd-1.5.0")
// or SINFO ("BeeIRCd 1.5.0 (abc1234)") gives it, is a BeeIRCd that knows
// RENAME. Development builds (0.0.0) count as current.
func renameAware(software string) bool {
	rest, ok := strings.CutPrefix(software, version.Name)
	if !ok {
		return false
	}
	rest = strings.TrimLeft(rest, " -")
	if i := strings.IndexAny(rest, " (-+"); i >= 0 {
		rest = rest[:i]
	}
	parts := strings.Split(rest, ".")
	if len(parts) < 2 {
		return false
	}
	major, err1 := strconv.Atoi(parts[0])
	minor, err2 := strconv.Atoi(parts[1])
	if err1 != nil || err2 != nil {
		return false
	}
	if major == 0 && minor == 0 {
		return true // a development build
	}
	return major > renameMinVersion[0] || major == renameMinVersion[0] && minor >= renameMinVersion[1]
}

// ---- after the rename ----

// renamePrepare runs before a Rename is applied and returns what finishes
// it afterwards: moving the server's own per-channel state, the stored
// data and the history, then telling the members.
func (s *Server) renamePrepare(e *state.Rename) func() {
	ch := s.net.Channel(e.Old)
	if ch == nil {
		return nil
	}
	oldName := ch.Name
	if dst := s.net.Channel(e.New); e.Merge && dst != nil && dst != ch {
		before := make(map[string]bool, len(dst.Members))
		for uid := range dst.Members {
			before[uid] = true
		}
		return func() {
			s.renameMove(oldName, dst.Name)
			s.renameMergeDeliver(e, ch, dst, before)
		}
	}
	return func() {
		newName := ch.Name
		s.renameMove(oldName, newName)
		s.renameDeliver(e, ch, oldName)
	}
}

// renameMove moves everything kept under channel oldName to newName.
func (s *Server) renameMove(oldName, newName string) {
	oldKey, newKey := irc.Fold(oldName), irc.Fold(newName)
	if err := s.store.RenameChannel(oldName, newName); err != nil {
		s.log.Printf("services database: rename %s to %s: %v", oldName, newName, err)
	}
	if s.renamedAt == nil {
		s.renamedAt = map[string]time.Time{}
	}
	now := time.Now()
	for k, at := range s.renamedAt {
		if now.Sub(at) >= renameCooldown {
			delete(s.renamedAt, k)
		}
	}
	delete(s.renamedAt, oldKey)
	s.renamedAt[newKey] = now
	if s.hist.store != nil {
		s.hist.store.Rename(oldName, newName)
		s.hist.dirty = true
	}
	for i := range s.floodUnsets {
		if irc.Fold(s.floodUnsets[i].channel) == oldKey {
			s.floodUnsets[i].channel = newName
		}
	}
	for i := range s.timedBans {
		if irc.Fold(s.timedBans[i].channel) == oldKey {
			s.timedBans[i].channel = newName
		}
	}
	if oldKey == newKey {
		return // the rest is kept by the folded name
	}
	if w, ok := s.targetFlood[oldKey]; ok {
		delete(s.targetFlood, oldKey)
		s.targetFlood[newKey] = w
	}
	if f, ok := s.floods[oldKey]; ok {
		delete(s.floods, oldKey)
		s.floods[newKey] = f
	}
	for k, until := range s.rejoinBlock {
		if uid, ok := strings.CutPrefix(k, oldKey+" "); ok {
			delete(s.rejoinBlock, k)
			s.rejoinBlock[newKey+" "+uid] = until
		}
	}
	for k, at := range s.marks {
		if k.target == oldKey {
			delete(s.marks, k)
			nk := markKey{k.account, newKey}
			if at.After(s.marks[nk]) {
				s.marks[nk] = at
			}
		}
	}
	for _, c := range s.clients {
		if at, ok := c.marks[oldKey]; ok {
			delete(c.marks, oldKey)
			if at.After(c.marks[newKey]) {
				c.marks[newKey] = at
			}
		}
	}
	for id, r := range s.recent.by {
		if r.target == oldKey {
			r.target = newKey
			s.recent.by[id] = r
		}
	}
}

// renameDeliver tells ch's members here: RENAME for clients with the cap;
// for the others, a PART of the old name and the JOIN, topic and names of
// the new one (nothing when only the case changed: the name still works).
func (s *Server) renameDeliver(e *state.Rename, ch *state.Channel, oldName string) {
	o := s.outFrom(e.Source, "RENAME", oldName, ch.Name, e.Reason).text()
	caseOnly := irc.Fold(oldName) == irc.Fold(ch.Name)
	for _, m := range ch.Members {
		c := s.clients[m.User.UID]
		if c == nil || !c.registered {
			continue
		}
		if c.caps[renameCap] {
			c.deliver(o)
			continue
		}
		if caseOnly {
			continue
		}
		u := m.User
		params := []string{oldName}
		if e.Reason != "" {
			params = append(params, e.Reason)
		}
		part := s.out(u, "PART", params...)
		part.msg.Trailing = e.Reason != ""
		c.deliver(part)
		join := s.out(u, "JOIN", ch.Name)
		join.extParams = []string{ch.Name, accountOrStar(u), u.Realname}
		c.deliver(join)
		if ch.Topic != "" {
			s.sendTopic(c, ch)
		}
		s.joinNames(c, ch)
	}
}

// renameMergeDeliver tells the members here of a rename that merged ch
// into dst (before: dst's members until then). ch's members hear of the
// rename as usual, the clients with the cap then of dst's members as
// joins; dst's members see ch's come in; someone in both only leaves the
// old name.
func (s *Server) renameMergeDeliver(e *state.Rename, ch, dst *state.Channel, before map[string]bool) {
	oldName := ch.Name
	var came []*state.User // ch's members new to dst
	for uid, m := range ch.Members {
		if !before[uid] {
			came = append(came, m.User)
		}
	}
	sort.Slice(came, func(i, j int) bool { return came[i].UID < came[j].UID })
	for _, u := range came {
		join, away := s.joinLines(u, dst.Name)
		for _, o := range seers(dst, u) {
			if before[o.UID] {
				s.deliverTo(o, join)
				if away != nil {
					s.deliverTo(o, away)
				}
			}
		}
	}
	var stay []*state.Member // dst's members from before
	for uid := range before {
		if m := dst.Members[uid]; m != nil {
			stay = append(stay, m)
		}
	}
	sort.Slice(stay, func(i, j int) bool { return stay[i].User.UID < stay[j].User.UID })
	o := s.outFrom(e.Source, "RENAME", oldName, dst.Name, e.Reason).text()
	for uid, m := range ch.Members {
		c := s.clients[uid]
		if c == nil || !c.registered {
			continue
		}
		u := m.User
		params := []string{oldName}
		if e.Reason != "" {
			params = append(params, e.Reason)
		}
		part := s.out(u, "PART", params...)
		part.msg.Trailing = e.Reason != ""
		switch {
		case before[uid]:
			c.deliver(part)
		case c.caps[renameCap]:
			c.deliver(o)
			for _, sm := range stay {
				if dst.Sees(u, sm) {
					join, away := s.joinLines(sm.User, dst.Name)
					c.deliver(join)
					if away != nil {
						c.deliver(away)
					}
				}
			}
		default:
			c.deliver(part)
			join := s.out(u, "JOIN", dst.Name)
			join.extParams = []string{dst.Name, accountOrStar(u), u.Realname}
			c.deliver(join)
			if dst.Topic != "" {
				s.sendTopic(c, dst)
			}
			s.joinNames(c, dst)
		}
	}
}

// joinLines are u's JOIN of channel name as others see it, and its AWAY
// for away-notify (nil when u isn't away).
func (s *Server) joinLines(u *state.User, name string) (join, away *outMsg) {
	join = s.out(u, "JOIN", name)
	join.extParams = []string{name, accountOrStar(u), u.Realname}
	if u.Away != "" {
		away = s.out(u, "AWAY", u.Away).text()
		away.needCap = "away-notify"
	}
	return join, away
}

// renameSettle readies a rename from another server that met dst, a
// channel just made here under the new name, as a netjoin of the two (the
// SJOIN rules): the older one keeps its age, modes, lists and ranks and
// the other takes them; the same age merges both ways; the newer topic
// wins. Each server that meets the clash settles it the same way itself,
// so none of this goes over a link.
func (s *Server) renameSettle(old, dst *state.Channel) {
	quiet, by := s.emitQuiet, s.cfg.Server.Name
	noMembers := func(string) bool { return false }
	skip := func(byte) {}
	lo, ld := s.linkChannel(old, noMembers), s.linkChannel(dst, noMembers)
	switch {
	case old.TS < dst.TS:
		s.clearChannel(quiet, dst, by)
		quiet(&state.ChannelTS{Channel: dst.Name, TS: old.TS})
		s.sjoinModes(quiet, dst, &lo, by, false, skip)
	case old.TS > dst.TS:
		s.clearChannel(quiet, old, by)
		s.sjoinModes(quiet, old, &ld, by, false, skip)
	default:
		s.sjoinModes(quiet, dst, &lo, by, true, skip)
		s.sjoinModes(quiet, old, &ld, by, true, skip)
	}
	// remoteTopic's rule, from old's side: its topic wins when dst has none
	// or it is newer. The losing channel's members are told the winner.
	switch {
	case old.Topic != "" && (dst.Topic == "" || old.TopicAt.Unix() > dst.TopicAt.Unix()):
		if dst.Topic != old.Topic || dst.TopicBy != old.TopicBy {
			quiet(&state.Topic{Channel: dst.Name, Text: old.Topic, By: old.TopicBy, At: old.TopicAt})
		}
	case dst.Topic != "" && (dst.Topic != old.Topic || dst.TopicBy != old.TopicBy):
		quiet(&state.Topic{Channel: old.Name, Text: dst.Topic, By: dst.TopicBy, At: dst.TopicAt})
	}
}

// ---- links ----

// renameLinkLines is a Rename for link o: BEE RENAME for BeeIRCd servers
// that know it; for the others (whose own people aren't in the channel,
// or it was refused) the members leave the old name and the channel comes
// back under the new one, as in a burst.
func (s *Server) renameLinkLines(o *linkConn, e *state.Rename, src string) []string {
	cd := o.codecOf()
	if renameLink(o) {
		return cd.Encode(&link.Bee{Source: srcOr(e.Source, src), Sub: "RENAME", Args: []string{e.Old, e.New, e.Reason}})
	}
	ch := s.net.Channel(e.New)
	if ch == nil || irc.Fold(e.Old) == irc.Fold(e.New) {
		return nil
	}
	var out []string
	for uid := range ch.Members {
		if !s.behind(o, uidServer(uid)) {
			out = append(out, cd.Encode(&link.Part{UID: uid, Channel: e.Old, Reason: e.Reason})...)
		}
	}
	if ch.HasMode('P') { // a permanent channel stays after the last part
		out = append(out, cd.Encode(&link.Mode{Source: src, Channel: e.Old, Items: []link.ModeItem{{Mode: 'P'}}})...)
	}
	return append(out, s.channelBurst(o, ch)...)
}

// remoteRename is BEE RENAME <old> <new> :reason from l.
func (s *Server) remoteRename(l *linkConn, it *link.Bee) {
	if len(it.Args) < 2 || !irc.IsChannel(it.Args[0]) || !irc.IsChannel(it.Args[1]) {
		return
	}
	reason := ""
	if len(it.Args) > 2 {
		reason = it.Args[2]
	}
	uid := ""
	if isUID(it.Source) {
		uid = it.Source
	}
	e := &state.Rename{Source: uid, Old: it.Args[0], New: it.Args[1], Reason: reason}
	// Someone here made the new name at the same moment: the two channels
	// merge, as in a netjoin, so every server ends with the same one.
	if old, dst := s.net.Channel(e.Old), s.net.Channel(e.New); old != nil && dst != nil && old != dst {
		s.sno('l', "Channel %s: renamed to %s on %s while it was just made here; the two are merged", old.Name, dst.Name, l.peer.Name)
		s.renameSettle(old, dst)
		e.Merge = true
	}
	if err := s.emitFrom(l, it.Source, e); err != nil {
		s.log.Printf("link %s: RENAME %s to %s: %v", l.peer.Name, it.Args[0], it.Args[1], err)
	}
}

// ---- Hive Panel ----

// renameFromPanel renames channel oldName for an oper on the Hive Panel,
// with the same checks as RENAME. It returns what to tell them.
func (s *Server) renameFromPanel(oldName, newName, reason string) (string, error) {
	reason = irc.Truncate(strings.TrimSpace(reason), s.cfg.Limits.KickLen)
	newName = strings.TrimSpace(newName)
	ch, no := s.renameCheck(nil, true, oldName, newName)
	if no != nil {
		return "", errText(no.text)
	}
	from := ch.Name
	if err := s.emit(&state.Rename{Old: from, New: newName, Reason: reason}); err != nil {
		return "", err
	}
	return from + " is now " + ch.Name + ".", nil
}

type errText string

func (e errText) Error() string { return string(e) }
