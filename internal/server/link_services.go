package server

// Services over links besides SASL
// (link_sasl.go): SVSJOIN/SVSPART both ways.

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/fhazal/beeircd/internal/irc"
	"github.com/fhazal/beeircd/internal/link"
	"github.com/fhazal/beeircd/internal/state"
)

// svcSource is the source for lines our services send: their
// pseudo-server, or this server.
func (s *Server) svcSource() string {
	if si, ok := s.servicesInfo(); ok {
		return si.SID
	}
	return s.cfg.Server.SID
}

// svsJoinRemote asks the server of a user elsewhere to join them.
func (s *Server) svsJoinRemote(uid, channel string) {
	if o := s.linkOf(uid); o != nil {
		o.send(":" + s.svcSource() + " SVSJOIN " + uid + " " + channel)
	}
}

// remoteSvsJoin is SVSJOIN: join our user (no bans or keys checked, as
// on UnrealIRCd), or pass it to the user's server.
func (s *Server) remoteSvsJoin(l *linkConn, m irc.Message, j *link.SvsJoin) {
	u := s.userByUIDOrNick(j.UID)
	if u == nil {
		return
	}
	c := s.clientOf(u)
	if c == nil {
		if o := s.linkOf(u.UID); o != nil && o != l {
			o.send(m.String())
		}
		return
	}
	for _, name := range strings.Split(j.Channel, ",") {
		if !irc.ValidChannel(name, s.cfg.Limits.ChannelLen) {
			continue
		}
		if ch := s.net.Channel(name); ch != nil && ch.Members[u.UID] != nil {
			continue
		}
		if s.emit(&state.Join{UID: u.UID, Channel: name, TS: time.Now().Unix()}) != nil {
			continue
		}
		if ch := s.net.Channel(name); ch != nil {
			if ch.Topic != "" {
				s.sendTopic(c, ch)
			}
			s.joinNames(c, ch)
		}
	}
}

// remoteSvsPart is SVSPART: part our user, or pass it on.
func (s *Server) remoteSvsPart(l *linkConn, m irc.Message, p *link.SvsPart) {
	u := s.userByUIDOrNick(p.UID)
	if u == nil {
		return
	}
	if s.clientOf(u) == nil {
		if o := s.linkOf(u.UID); o != nil && o != l {
			o.send(m.String())
		}
		return
	}
	for _, name := range strings.Split(p.Channel, ",") {
		if ch := s.net.Channel(name); ch != nil && ch.Members[u.UID] != nil {
			s.emit(&state.Part{UID: u.UID, Channel: ch.Name, Reason: p.Reason})
		}
	}
}

// ---- oper status over links ----

// operMD writes uid's oper class (UnrealIRCd MD operclass) and, for
// BeeIRCd, the flags; empty values clear them.
func operMD(o *linkConn, src, uid, class, flags, look string) []string {
	var out []string
	if o.bridge != nil && class != "" { // InspIRCd: the rank for OPERTYPE
		title, _, _ := readOperLook(look)
		out = append(out, mdLine(src, uid, "bee-opertype", o.s.operRank(class, title)))
	}
	out = append(out, mdLine(src, uid, "operclass", class))
	if o.peer.Bee {
		out = append(out, mdLine(src, uid, "bee-operflags", flags), mdLine(src, uid, "bee-operlook", look))
	}
	return out
}

// operLook packs an oper's WHOIS title and extra lines for MD
// bee-operlook, so every BeeIRCd server shows them the same; "" = none.
// tag is the [server] oper_tag of the oper's own server (nil = not known,
// so the server showing the WHOIS uses its own).
func operLook(title string, swhois []string, tag *string) string {
	if title == "" && len(swhois) == 0 && tag == nil {
		return ""
	}
	for {
		b, _ := json.Marshal(operLookData{Title: title, Lines: swhois, Tag: tag})
		if len(b) <= 380 || len(swhois) == 0 {
			return string(b)
		}
		swhois = swhois[:len(swhois)-1] // one line must fit a link line
	}
}

type operLookData struct {
	Title string   `json:"t,omitempty"`
	Lines []string `json:"s,omitempty"`
	Tag   *string  `json:"g,omitempty"` // oper_tag; older servers leave it out
}

// ownLook is operLook for an oper of this server, with our oper_tag.
func (s *Server) ownLook(title string, swhois []string) string {
	tag := s.cfg.Server.OperTag
	return operLook(title, swhois, &tag)
}

// readOperLook is operLook read back from another server, kept short and
// free of control bytes.
func readOperLook(value string) (title string, lines []string, tag *string) {
	var d operLookData
	if value == "" || json.Unmarshal([]byte(value), &d) != nil {
		return "", nil, nil
	}
	clean := func(t string) string {
		t = strings.Map(func(r rune) rune {
			if r < ' ' || r == 0x7f {
				return -1
			}
			return r
		}, t)
		return irc.Truncate(strings.TrimSpace(t), 150)
	}
	for _, l := range d.Lines {
		if l = clean(l); l != "" && len(lines) < 5 {
			lines = append(lines, l)
		}
	}
	if d.Tag != nil {
		t := irc.Truncate(clean(*d.Tag), 30)
		tag = &t
	}
	return clean(d.Title), lines, tag
}

func mdLine(src, uid, key, value string) string {
	if value == "" {
		return ":" + src + " MD client " + uid + " " + key
	}
	return ":" + src + " MD client " + uid + " " + key + " :" + value
}

// operRank is an oper's rank as WHOIS words it, without "is a": their
// own title, else their class's, else "IRC Operator". InspIRCd gets it
// as the oper type ("Network Administrator").
func (s *Server) operRank(class, title string) string {
	if title == "" {
		if oc, ok := s.cfg.Class(class); ok {
			title = oc.Title
		}
	}
	for _, a := range []string{"is a ", "is an ", "is the "} {
		if len(title) > len(a) && strings.EqualFold(title[:len(a)], a) {
			title = title[len(a):]
			break
		}
	}
	if title = strings.TrimSpace(title); title == "" {
		return "IRC Operator"
	}
	return title
}

// linkOperInfo tells every link but except about uid's oper status.
func (s *Server) linkOperInfo(except *linkConn, uid, class, flags, look string) {
	for o := range s.links.conns {
		if o != except && o.up && !o.closed {
			for _, line := range operMD(o, s.cfg.Server.SID, uid, class, flags, look) {
				o.send(line)
			}
		}
	}
}

// resendOperLooks tells the other servers how our opers look again, after
// oper_tag changed.
func (s *Server) resendOperLooks() {
	for c := range s.opers {
		if c.user != nil {
			s.linkOperInfo(nil, c.uid, c.operClass, c.operFlags, s.ownLook(c.operTitle, c.operSwhois))
		}
	}
}

// operBurst is the oper status of everyone not behind l.
func (s *Server) operBurst(l *linkConn) []string {
	var out []string
	for c := range s.opers {
		if c.user != nil {
			out = append(out, operMD(l, s.cfg.Server.SID, c.uid, c.operClass, c.operFlags, s.ownLook(c.operTitle, c.operSwhois))...)
		}
	}
	for _, u := range s.net.Users() {
		if u.OperClass+u.OperFlags != "" && !s.ours(u.UID) && !s.behind(l, uidServer(u.UID)) {
			out = append(out, operMD(l, s.cfg.Server.SID, u.UID, u.OperClass, u.OperFlags, operLook(u.OperTitle, u.OperSwhois, u.OperTag))...)
		}
	}
	return out
}

// remoteOperMD is MD operclass / bee-operflags for a user behind l.
// UnrealIRCd sends only the class: its usual names get the matching
// flags here (netadmin, admin, globop, locop; "-with-override" too).
func (s *Server) remoteOperMD(l *linkConn, m irc.Message) {
	uid, key := m.Params[1], m.Params[2]
	u := s.net.UserByUID(uid)
	if u == nil || !s.behind(l, uidServer(uid)) {
		return
	}
	value := ""
	if len(m.Params) > 3 {
		value = m.Params[3]
	}
	wasOper := u.OperClass+u.OperFlags != ""
	switch key {
	case "operclass":
		u.OperClass = value
		if !l.peer.Bee {
			u.OperFlags = s.flagsForClass(value)
		}
	case "bee-operflags":
		u.OperFlags, _ = cleanFlags(value)
	case "bee-operlook":
		u.OperTitle, u.OperSwhois, u.OperTag = readOperLook(value)
	}
	if !wasOper && u.OperClass+u.OperFlags != "" && !u.Service && s.serverSynced(uidServer(uid)) {
		s.sno('O', "From %s: %s is now a server operator of type %s", u.Server, snoWho(u), operType(u.OperClass, u.OperFlags))
	}
	for o := range s.links.conns {
		if o != l && o.up && !o.closed && (key == "operclass" || o.peer.Bee) {
			if o.bridge != nil && key == "operclass" && value != "" {
				o.send(mdLine(s.cfg.Server.SID, uid, "bee-opertype", s.operRank(value, u.OperTitle)))
			}
			o.send(m.String())
		}
	}
}

// flagsForClass is what an oper class from another server may do here:
// ours when we have that class, else UnrealIRCd's default names.
func (s *Server) flagsForClass(name string) string {
	if name == "" {
		return ""
	}
	base := strings.TrimSuffix(strings.ToLower(name), "-with-override")
	if base == "locop" {
		base = "helper"
	}
	if oc, ok := s.cfg.Class(base); ok {
		f, _ := cleanFlags(oc.Flags)
		return f
	}
	return ""
}

// ---- remote WHOIS ----

// whoisElsewhere sends "WHOIS server nick" (or "WHOIS nick nick") to the
// server named, when that is another one; false = answer here.
func (s *Server) whoisElsewhere(c *Client, where, nick string) bool {
	var sid, target string
	if r := s.serverByName(where); r != nil {
		sid, target = r.sid, r.sid
	} else if u := s.net.UserByNick(where); u != nil && !s.ours(u.UID) {
		sid, target = uidServer(u.UID), u.UID
	}
	r := s.links.servers[sid]
	if r == nil || r.via == nil {
		return false
	}
	r.via.send(":" + c.uid + " WHOIS " + target + " :" + nick)
	return true
}

// remoteWhois is WHOIS from someone on another server: answer when it is
// for us, else pass it on towards the server asked.
func (s *Server) remoteWhois(l *linkConn, m irc.Message) {
	if len(m.Params) < 2 {
		return
	}
	asker := s.net.UserByUID(m.Source)
	if asker == nil || !s.behind(l, uidServer(asker.UID)) {
		return
	}
	target, nick := m.Params[0], strings.SplitN(m.Params[1], ",", 2)[0]
	sid := target
	if len(target) == 9 {
		sid = uidServer(target)
	} else if u := s.net.UserByNick(target); u != nil && !strings.Contains(target, ".") {
		sid = uidServer(u.UID) // UnrealIRCd names the target by nick
		if s.ours(u.UID) {
			sid = s.cfg.Server.SID
		}
	} else if r := s.serverByName(target); r != nil {
		sid = r.sid
	} else if strings.EqualFold(target, s.cfg.Server.Name) {
		sid = s.cfg.Server.SID
	}
	if si, ok := s.servicesInfo(); ok && (sid == si.SID || strings.EqualFold(target, si.Name)) {
		sid = s.cfg.Server.SID // our services server: we answer
	}
	if sid != s.cfg.Server.SID {
		if r := s.links.servers[sid]; r != nil && r.via != l {
			r.via.send(m.String())
		}
		return
	}
	h := host{s}
	reply := func(code string, p ...string) { h.Numeric(asker.UID, code, p...) }
	u := s.net.UserByNick(nick)
	if u == nil {
		reply(irc.ErrNoSuchNick, nick, "No such nick/channel")
		reply(irc.RplEndOfWhois, nick, "End of /WHOIS list.")
		return
	}
	// A stand-in for the asker: what they may see is what their own
	// server says (oper flags), nothing is ever sent to it.
	stand := &Client{uid: asker.UID, user: asker, caps: map[string]bool{"multi-prefix": true}}
	if asker.Modes['o'] {
		stand.operFlags = asker.OperFlags
		if stand.operFlags == "" {
			stand.operFlags = "o"
		}
	}
	s.whoisOne(stand, u, reply)
}

// ---- nick bans (SQLINE) and realname bans (SNLINE) over links ----

// qline is a nick ban from another server (UnrealIRCd TKL Q). Our own
// SQLINEs are the services' FORBID NICK list.
type qline struct {
	mask, reason, setBy string
	setAt, expires      int64
	via                 *linkConn
}

// qlineFor finds a nick ban from another server that matches nick.
func (s *Server) qlineFor(nick string) (qline, bool) {
	now := time.Now().Unix()
	for _, q := range s.links.qlines {
		if (q.expires == 0 || q.expires > now) && irc.Match(q.mask, nick) {
			return q, true
		}
	}
	return qline{}, false
}

func qlineBan(src string, add bool, mask, setBy, reason string, setAt, expires int64) *link.Ban {
	return &link.Ban{Source: src, Add: add, Type: link.BanQline, User: "*", Host: mask, SetBy: orStr(setBy, "services"),
		SetAt: setAt, Expires: expires, Reason: orStr(reason, "No reason")}
}

// ourQlines are the services' nick forbids as Q-lines, by mask.
func (s *Server) ourQlines() map[string]*link.Ban {
	out := map[string]*link.Ban{}
	if s.svc.Off() {
		return out
	}
	for _, f := range s.svc.NickForbids() {
		var exp int64
		if !f.Expires.IsZero() {
			exp = f.Expires.Unix()
		}
		out[irc.Fold(f.Name)] = qlineBan(s.svcSource(), true, f.Name, f.SetBy, f.Reason, f.SetAt.Unix(), exp)
	}
	return out
}

// syncQlines sends what changed in our nick forbids to every link.
func (s *Server) syncQlines() {
	now := s.ourQlines()
	var lines []*link.Ban
	for k, b := range now {
		if _, sent := s.links.qlineSent[k]; !sent {
			lines = append(lines, b)
		}
	}
	for k, b := range s.links.qlineSent {
		if _, still := now[k]; !still {
			off := *b
			off.Add = false
			lines = append(lines, &off)
		}
	}
	s.links.qlineSent = now
	for _, b := range lines {
		for o := range s.links.conns {
			if o.up && !o.closed {
				for _, line := range o.codecOf().Encode(b) {
					o.send(line)
				}
			}
		}
	}
}

// qlineBurst is every nick ban for a new link: ours and those from links
// other than l.
func (s *Server) qlineBurst(l *linkConn) []string {
	cd := l.codecOf()
	var out []string
	if s.links.qlineSent == nil {
		s.links.qlineSent = s.ourQlines()
	}
	for _, b := range s.links.qlineSent {
		out = append(out, cd.Encode(b)...)
	}
	for _, q := range s.links.qlines {
		if q.via != l {
			out = append(out, cd.Encode(qlineBan(s.cfg.Server.SID, true, q.mask, q.setBy, q.reason, q.setAt, q.expires))...)
		}
	}
	return out
}

// remoteQline is TKL Q from l: kept (and enforced at NICK) and passed on.
func (s *Server) remoteQline(l *linkConn, raw irc.Message, b *link.Ban) {
	if s.links.qlines == nil {
		s.links.qlines = map[string]qline{}
	}
	key := irc.Fold(b.Host)
	if b.Add && strings.Trim(b.Host, "*?") == "" {
		s.ignoreOnce(l, "qline-everyone", "refused a nick ban (Q-line) that matches every nick: %q", b.Host)
		return
	}
	if b.Add {
		s.links.qlines[key] = qline{mask: b.Host, reason: b.Reason, setBy: b.SetBy, setAt: b.SetAt, expires: b.Expires, via: l}
	} else {
		delete(s.links.qlines, key)
	}
	for _, o := range s.upLinks(l) {
		o.send(raw.String())
	}
}

// dropQlinesVia forgets nick bans that came over l (it split).
func (s *Server) dropQlinesVia(l *linkConn) {
	for k, q := range s.links.qlines {
		if q.via == l {
			delete(s.links.qlines, k)
		}
	}
}
