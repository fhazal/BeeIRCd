package server

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/fhazal/beeircd/internal/cloak"
	"github.com/fhazal/beeircd/internal/irc"
	"github.com/fhazal/beeircd/internal/services"
	"github.com/fhazal/beeircd/internal/state"
	"github.com/fhazal/beeircd/internal/store"
	"github.com/fhazal/beeircd/internal/version"
)

// capabilities are the IRCv3 features BeeIRCd offers.
var capabilities = []string{
	"account-notify", "account-tag", "away-notify", "batch", "cap-notify",
	"chghost", "draft/chathistory", "draft/no-implicit-names", "echo-message", "extended-join", "extended-monitor", "invite-notify",
	"labeled-response", "message-tags", "multi-prefix", "sasl", "server-time",
	"setname", "standard-replies", "userhost-in-names",
}

// draftCaps are the IRCv3 drafts offered while [server] ircv3_drafts is
// on.
var draftCaps = []string{readMarkerCap, preAwayCap, extISupportCap, multilineCap, redactCap, accountRegCap}

// preAwayCap lets AWAY come before registration (IRCv3 draft/pre-away).
const preAwayCap = "draft/pre-away"

// offered are the capabilities clients may ask for now.
func (s *Server) offered() []string {
	if !s.cfg.Server.IRCv3Drafts {
		return capabilities
	}
	out := append([]string(nil), capabilities...)
	for _, d := range draftCaps {
		if d == accountRegCap && s.svc.Off() {
			continue // services run on another server
		}
		out = append(out, d)
	}
	return out
}

// capValue is name as CAP LS 302 shows it, with its value.
func (s *Server) capValue(name string) string {
	switch name {
	case "sasl":
		return "sasl=PLAIN,EXTERNAL"
	case accountRegCap:
		return s.accountRegValue()
	case multilineCap:
		return multilineCap + "=max-bytes=" + strconv.Itoa(mlMaxBytes) + ",max-lines=" + strconv.Itoa(mlMaxLines)
	}
	return name
}

// capLS is the CAP LS list; CAP 302 clients also see values. sts is the
// sts value ("" when not offered); it is never in capabilities, so a REQ
// for it gets NAK as the spec wants.
func (s *Server) capLS(v302 bool, sts string) string {
	names := s.offered()
	out := make([]string, 0, len(names)+1)
	for _, c := range names {
		if v302 {
			c = s.capValue(c)
		}
		out = append(out, c)
	}
	if sts != "" {
		out = append(out, "sts="+sts)
	}
	return strings.Join(out, " ")
}

func (s *Server) supportedCap(name string) bool {
	for _, c := range s.offered() {
		if c == name {
			return true
		}
	}
	return false
}

// draftsChanged tells cap-notify clients the drafts came or went, and
// takes them from clients when they went.
func (s *Server) draftsChanged(on bool) {
	sub := "NEW"
	if !on {
		sub = "DEL"
	}
	for _, c := range s.clients {
		if !on {
			for _, d := range draftCaps {
				delete(c.caps, d)
			}
		}
		if c.caps["cap-notify"] {
			var names []string
			for _, d := range draftCaps {
				if d == accountRegCap && s.svc.Off() {
					continue
				}
				if on {
					d = s.capValue(d)
				}
				names = append(names, d)
			}
			s.capReply(c, sub, strings.Join(names, " "))
		}
	}
}

func (s *Server) cmdCap(c *Client, m irc.Message) {
	switch strings.ToUpper(m.Params[0]) {
	case "LS":
		if !c.registered {
			c.capBusy = true
		}
		v, _ := strconv.Atoi(m.Param(1))
		if v >= 302 {
			c.caps["cap-notify"] = true // implied by CAP LS 302
		}
		s.capReply(c, "LS", s.capLS(v >= 302, s.stsValue(c, v >= 302)))
	case "LIST":
		var on []string
		for _, name := range s.offered() {
			if c.caps[name] {
				on = append(on, name)
			}
		}
		s.capReply(c, "LIST", strings.Join(on, " "))
	case "REQ":
		if !c.registered {
			c.capBusy = true
		}
		req := m.Param(1)
		wanted := strings.Fields(req)
		ok := len(wanted) > 0
		for _, w := range wanted {
			if !s.supportedCap(strings.TrimPrefix(w, "-")) {
				ok = false
			}
		}
		if !ok {
			s.capReply(c, "NAK", req)
			return
		}
		for _, w := range wanted {
			if name, off := strings.CutPrefix(w, "-"); off {
				delete(c.caps, name)
			} else {
				c.caps[w] = true
			}
		}
		s.capReply(c, "ACK", req)
	case "END":
		c.capBusy = false
		s.tryRegister(c)
	default:
		s.reply(c, "410", m.Params[0], "Invalid CAP command")
	}
}

func (s *Server) capReply(c *Client, sub, text string) {
	c.send(irc.Message{Source: s.cfg.Server.Name, Command: "CAP", Params: []string{c.target(), sub, text}, Trailing: true}.String())
}

func (s *Server) cmdPass(c *Client, m irc.Message) {
	if c.registered {
		s.reply(c, irc.ErrAlreadyRegistered, "You may not reregister")
	}
	// No server password; accounts are SASL and NickServ.
}

func (s *Server) cmdNick(c *Client, m irc.Message) {
	nick := m.Param(0)
	if nick == "" {
		s.reply(c, irc.ErrNoNicknameGiven, "No nickname given")
		return
	}
	if !irc.ValidNick(nick, s.cfg.Limits.NickLen) {
		s.reply(c, irc.ErrErroneusNickname, nick, "Erroneous nickname")
		return
	}
	if services.IsReserved(nick) {
		s.reply(c, irc.ErrErroneusNickname, nick, "Reserved for services")
		return
	}
	if f, ok := s.svc.Forbidden(store.ForbidNick, nick); ok && !c.isOper() {
		s.reply(c, irc.ErrErroneusNickname, nick, "Forbidden by the network: "+f.Reason)
		return
	}
	if q, ok := s.qlineFor(nick); ok && !c.isOper() {
		s.reply(c, irc.ErrErroneusNickname, nick, "Forbidden by the network: "+q.reason)
		return
	}
	if !c.registered {
		if s.net.NickInUse(nick) {
			s.reply(c, irc.ErrNicknameInUse, nick, "Nickname is already in use")
			return
		}
		c.nick = nick
		s.tryRegister(c)
		return
	}
	u := c.user
	if nick == u.Nick {
		return
	}
	if c.nickLocked {
		s.reply(c, irc.ErrNoNickChange, "You may not change your nickname: an IRC operator locked it")
		return
	}
	if !c.isOper() && !irc.EqualFold(nick, u.Nick) {
		for _, ch := range u.Channels {
			mem := ch.Member(u)
			if ch.HasMode('N') && !mem.AtLeast(state.RankOp) {
				s.reply(c, irc.ErrNoNickChange, "Can not change nickname while on "+ch.Name+" (+N)")
				return
			}
			if !mem.AtLeast(state.RankVoice) && ch.Stops(u, state.StopNick) {
				s.reply(c, irc.ErrBanNickChange, ch.Name, "Cannot change nickname while banned on channel")
				return
			}
		}
	}
	if !s.spamUser(c, nick) {
		return
	}
	if err := s.emit(&state.NickChange{UID: u.UID, Nick: nick, TS: time.Now().Unix()}); err != nil {
		s.reply(c, irc.ErrNicknameInUse, nick, "Nickname is already in use")
		return
	}
	for _, ch := range u.Channels {
		if mem := ch.Member(u); mem != nil && !mem.Hidden {
			s.floodEvent(c, ch, 'n')
		}
	}
}

func (s *Server) cmdUser(c *Client, m irc.Message) {
	if c.registered {
		s.reply(c, irc.ErrAlreadyRegistered, "You may not reregister")
		return
	}
	if m.Params[3] == "" {
		s.reply(c, irc.ErrNeedMoreParams, "USER", "Not enough parameters")
		return
	}
	c.ident = cleanIdent(m.Params[0])
	c.realname = irc.Truncate(m.Params[3], 50)
	s.tryRegister(c)
}

// cleanIdent keeps letters, digits and . _ - and marks it with ~ because
// BeeIRCd does no ident lookups.
func cleanIdent(raw string) string {
	var b strings.Builder
	for i := 0; i < len(raw) && b.Len() < 9; i++ {
		ch := raw[i]
		if ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || ch >= '0' && ch <= '9' || ch == '.' || ch == '_' || ch == '-' {
			b.WriteByte(ch)
		}
	}
	if b.Len() == 0 {
		return "~user"
	}
	return "~" + b.String()
}

func (s *Server) cmdPing(c *Client, m irc.Message) {
	if len(m.Params) == 0 {
		s.reply(c, irc.ErrNoOrigin, "No origin specified")
		return
	}
	c.send(irc.Message{Source: s.cfg.Server.Name, Command: "PONG", Params: []string{s.cfg.Server.Name, m.Params[0]}, Trailing: true}.String())
}

func (s *Server) cmdPong(c *Client, m irc.Message) {} // handleLine already reset the ping timer

func (s *Server) cmdQuit(c *Client, m irc.Message) {
	reason := "Quit"
	if r := m.Param(0); r != "" {
		reason = "Quit: " + irc.Truncate(r, s.cfg.Limits.KickLen)
	}
	s.quit(c, reason)
}

// tryRegister finishes registration once NICK, USER, CAP and the host
// lookup are all done.
func (s *Server) tryRegister(c *Client) {
	if c.registered || c.closed || c.nick == "" || c.ident == "" || !c.hostDone || c.dnsblBusy || c.capBusy || c.saslBusy || c.webircBusy || c.identBusy {
		return
	}
	if c.identAnswer != "" {
		c.ident = c.identAnswer // their ident server answered: no ~
	}
	if r := c.dnsblListed; r != nil {
		c.dnsblListed = nil
		if s.dnsblHit(c, *r) {
			return
		}
	}
	if c.sessionLate {
		// Over its IP's limit at connect; now the hostname is known.
		c.sessionLate = false
		if n, max := s.sessions(c.ip), s.svc.SessionLimitFor(c.ip, c.host); n > max {
			s.snotice("Too many connections from %s (%d, the limit is %d)", c.ip, n, max)
			s.quit(c, fmt.Sprintf("Too many connections from your IP (the limit is %d)", max))
			return
		}
	}
	now := time.Now()
	u := &state.User{
		UID:      c.uid,
		Nick:     c.nick,
		Ident:    c.ident,
		Host:     c.host,
		RealHost: c.host,
		IP:       c.ip,
		Realname: c.realname,
		Server:   s.cfg.Server.Name,
		TLS:      c.tls,
		CertFP:   c.certfp,
		Gateway:  c.webirc,
		SignOn:   now,
		NickTS:   now.Unix(),
		Modes:    map[byte]bool{'i': true},
	}
	s.geoFill(u)
	if s.cfg.CloakEnabled() {
		u.Modes['x'] = true
		u.Host = s.cloakFor(u)
	}
	if c.tls {
		u.Modes['z'] = true // connected with TLS (UnrealIRCd +z)
	}
	a := c.saslAccount
	c.saslAccount = nil
	if a != nil {
		u.Account = a.Name
		u.Modes['r'] = true
	}
	if s.localUsers() >= s.cfg.Limits.MaxUsers {
		s.quit(c, "This server is full. Please try again later.")
		return
	}
	if s.jump != nil {
		s.jumpRedirect(c) // JUMPSERVER: go there instead
		return
	}
	if k := s.svc.ConnectBan(c.ident, c.host, c.ip, c.realname); k != nil {
		s.reply(c, irc.ErrYoureBannedCreep, "You are banned from this network: "+k.Reason)
		s.snotice("%s %s stopped %s (%s@%s)", strings.ToUpper(k.Kind), k.Mask, c.nick, c.ident, c.host)
		s.quit(c, "Banned: "+k.Reason)
		return
	}
	if !s.spamUser(c, c.nick) {
		return
	}
	u.Groups = s.groupsOf(u, now)
	if why := s.throttleNew(u, now); why != "" {
		s.quit(c, why)
		return
	}
	s.repAnnounce(c.ip)
	if err := s.emit(&state.UserAdd{User: u}); err != nil {
		s.reply(c, irc.ErrNicknameInUse, c.nick, "Nickname is already in use")
		c.nick = ""
		return
	}
	c.user = u
	c.registered = true
	s.doneUnregistered(c)
	s.local++
	n := s.localUsers()
	if n > s.maxLocal {
		s.maxLocal = n
	}
	s.notePeak(n)
	s.notePeakGlobal()
	s.log.Printf("connect %s (%s@%s) %s", u.Nick, u.Ident, u.RealHost, c.ip)
	connect := fmt.Sprintf("Connect: %s (%s@%s) [%s]", u.Nick, u.Ident, u.RealHost, c.ip)
	if c.webirc != "" {
		connect += " (WEBIRC " + c.webirc + ")"
	}
	s.snoticeOnly(connect)
	s.svc.LogUserConnect(u)
	s.welcome(c)
	if c.preAway != "" {
		msg := c.preAway
		c.preAway = ""
		s.emit(&state.Away{UID: u.UID, Message: msg})
		s.reply(c, irc.RplNowAway, "You have been marked as being away")
	}
	s.svc.SendNews(c.uid)
	// Services take it from here (SASL account, nick check) once this
	// command is done.
}

func (s *Server) welcome(c *Client) {
	cfg := s.cfg.Server
	u := c.user
	s.reply(c, irc.RplWelcome, fmt.Sprintf("Welcome to %s, %s", cfg.Network, u.Nick))
	s.reply(c, irc.RplYourHost, fmt.Sprintf("Your host is %s, running %s", cfg.Name, version.String()))
	s.reply(c, irc.RplCreated, "This server was created "+s.created.UTC().Format("Mon Jan 2 2006 at 15:04:05 UTC"))
	s.replyBare(c, irc.RplMyInfo, cfg.Name, version.Name+"-"+version.Version, userModes, s.channelModes(), s.channelModesWithParam())
	s.sendISupport(c)
	s.lusers(c)
	s.motd(c)
	c.send(irc.Message{Source: u.Nick, Command: "MODE", Params: []string{u.Nick, u.ModeString()}}.String())
	if u.Host != u.RealHost {
		s.reply(c, irc.RplHostHidden, u.Host, "is now your displayed host")
	}
}

// cloakFor is u's cloaked host.
func (s *Server) cloakFor(u *state.User) string {
	if s.cfg.KeyedCloak() {
		k := s.cfg.Cloak.Keys
		keys := [3]string{k[0], k[1], k[2]}
		if s.cfg.InspCloak() {
			return cloak.InspHost(keys, u.RealHost, u.IP)
		}
		return cloak.UnrealHost(keys, s.cfg.Cloak.Prefix, u.RealHost)
	}
	return cloak.Host(s.cfg.Cloak.Secret, s.cfg.Server.Network, u.RealHost, u.IP, u.RealHost == irc.DisplayIP(u.IP))
}

// setHost changes the host others see and tells chghost clients.
func (s *Server) setHost(c *Client, host string) {
	u := c.user
	if host == u.Host {
		return
	}
	s.emit(&state.HostChange{UID: u.UID, Host: host})
	s.reply(c, irc.RplHostHidden, host, "is now your displayed host")
}

const userModes = "BDHRTWZdghiopqrswxz"

// operOnlyModes go when someone stops being an oper.
const operOnlyModes = "HWq"

// rankEnabled reports whether this server uses rank r (owner, admin and
// halfop exist only with prefixes "~&@%+").
func (s *Server) rankEnabled(r state.Rank) bool {
	if s.cfg.FullPrefixes() {
		return true
	}
	return r == state.RankOp || r == state.RankVoice
}

// rankModes and rankPrefixes are the enabled ranks, highest first:
// "ov" and "@+", or "qaohv" and "~&@%+".
func (s *Server) rankModes() string {
	var b []byte
	for _, r := range state.Ranks {
		if s.rankEnabled(r.Rank) {
			b = append(b, r.Mode)
		}
	}
	return string(b)
}

func (s *Server) rankPrefixes() string {
	var b []byte
	for _, r := range state.Ranks {
		if s.rankEnabled(r.Rank) {
			b = append(b, r.Prefix)
		}
	}
	return string(b)
}

func (s *Server) channelModes() string {
	all := chanModesList + chanModesKey + chanModesParam + chanModesFlags + "ov"
	if s.cfg.FullPrefixes() {
		all += "qah"
	}
	return sortedLetters(all)
}

// sortedLetters sorts mode letters (capitals first, like the 004 lists).
func sortedLetters(s string) string {
	b := []byte(s)
	sort.Slice(b, func(i, j int) bool { return b[i] < b[j] })
	return string(b)
}

func (s *Server) channelModesWithParam() string {
	all := chanModesList + chanModesKey + chanModesParam + "ov"
	if s.cfg.FullPrefixes() {
		all += "qah"
	}
	return sortedLetters(all)
}

func (s *Server) isupport() []string {
	l := s.cfg.Limits
	out := []string{
		"ACCEPT=" + strconv.Itoa(acceptMax),
		"ACCOUNTEXTBAN=a",
		"AWAYLEN=" + strconv.Itoa(l.AwayLen),
		"BOT=B",
		"CALLERID=g",
		"CASEMAPPING=" + irc.Casemapping(),
		"CHANLIMIT=#:" + strconv.Itoa(l.MaxChannels),
		"CHANMODES=" + chanModesList + "," + chanModesKey + "," + chanModesParam + "," + chanModesFlags,
		"CHANNELLEN=" + strconv.Itoa(l.ChannelLen),
		"CHANTYPES=#",
		"CHATHISTORY=" + strconv.Itoa(chathistoryMax),
		"EXTBAN=~," + irc.ExtbanLetters,
		"EXCEPTS=e",
		"HOSTLEN=63",
		"INVEX=I",
		"KICKLEN=" + strconv.Itoa(l.KickLen),
		"MAXLIST=beI:" + strconv.Itoa(maxListEntries),
		"MAXTARGETS=" + strconv.Itoa(maxTargets),
		"MONITOR=" + strconv.Itoa(monitorMax),
		"MODES=" + strconv.Itoa(maxModeParams),
		"MSGREFTYPES=msgid,timestamp",
		"NETWORK=" + s.cfg.Server.Network,
		"NICKLEN=" + strconv.Itoa(l.NickLen),
		"PREFIX=(" + s.rankModes() + ")" + s.rankPrefixes(),
		"SILENCE=" + strconv.Itoa(maxSilence),
		"STATUSMSG=" + s.rankPrefixes(),
		"TARGMAX=JOIN:,KICK:4,NAMES:1,NOTICE:4,PART:,PRIVMSG:4,WHOIS:1",
		"TOPICLEN=" + strconv.Itoa(l.TopicLen),
		"USERLEN=10",
		"WATCH=" + strconv.Itoa(watchMax),
		"WHOX",
	}
	if l.UTF8Only {
		out = append(out, "UTF8ONLY")
	}
	return out
}

// sendISupport sends the 005 lines; in a draft/isupport batch for
// clients with draft/extended-isupport and batch.
func (s *Server) sendISupport(c *Client) {
	tokens := s.isupport()
	tag := ""
	if c.caps[extISupportCap] && c.caps["batch"] {
		s.batchSeq++
		id := "i" + strconv.FormatUint(s.batchSeq, 36)
		c.send(":" + s.cfg.Server.Name + " BATCH +" + id + " draft/isupport")
		tag = "@batch=" + id + " "
		defer c.send(":" + s.cfg.Server.Name + " BATCH -" + id)
	}
	for len(tokens) > 0 {
		n := min(13, len(tokens))
		p := append([]string{c.target()}, append(tokens[:n:n], "are supported by this server")...)
		c.send(tag + irc.Message{Source: s.cfg.Server.Name, Command: irc.RplISupport, Params: p, Trailing: true}.String())
		tokens = tokens[n:]
	}
}

// extISupportCap: 005 before registration and in a batch (IRCv3
// draft/extended-isupport).
const extISupportCap = "draft/extended-isupport"

// cmdISupport is ISUPPORT: the 005 lines again, also before registration.
func (s *Server) cmdISupport(c *Client, m irc.Message) {
	if !c.caps[extISupportCap] {
		if c.registered {
			s.reply(c, irc.ErrUnknownCommand, "ISUPPORT", "Unknown command")
		} else {
			s.reply(c, irc.ErrNotRegistered, "You have not registered")
		}
		return
	}
	s.sendISupport(c)
}

func (s *Server) localUsers() int { return s.local }

// notePeakGlobal keeps the most people on the whole network at once.
func (s *Server) notePeakGlobal() {
	if n, _, _ := s.net.PeopleCounts(); n > s.maxGlobal {
		s.maxGlobal = n
	}
}

func (s *Server) lusers(c *Client) {
	total, invisible, opers := s.net.PeopleCounts() // services aren't people
	local := s.localUsers()
	unknown := len(s.clients) - local
	s.notePeakGlobal()
	links := 0 // servers linked to this one directly
	for l := range s.links.conns {
		if l.up {
			links++
		}
	}
	s.reply(c, irc.RplLuserClient, fmt.Sprintf("There are %d users and %d invisible on %d servers", total-invisible, invisible, len(s.netServers())))
	if opers > 0 {
		s.reply(c, irc.RplLuserOp, strconv.Itoa(opers), "operator(s) online")
	}
	if unknown > 0 {
		s.reply(c, irc.RplLuserUnknown, strconv.Itoa(unknown), "unknown connection(s)")
	}
	s.reply(c, irc.RplLuserChannels, strconv.Itoa(s.net.ChannelCount()), "channels formed")
	s.reply(c, irc.RplLuserMe, fmt.Sprintf("I have %d clients and %d servers", local, links))
	s.reply(c, irc.RplLocalUsers, strconv.Itoa(local), strconv.Itoa(s.maxLocal), fmt.Sprintf("Current local users %d, max %d", local, s.maxLocal))
	s.reply(c, irc.RplGlobalUsers, strconv.Itoa(total), strconv.Itoa(s.maxGlobal), fmt.Sprintf("Current global users %d, max %d", total, s.maxGlobal))
}

func (s *Server) motd(c *Client) {
	lines := s.cfg.MOTDLines()
	if len(lines) == 0 {
		s.reply(c, irc.ErrNoMotd, "MOTD File is missing")
		return
	}
	s.reply(c, irc.RplMotdStart, "- "+s.cfg.Server.Name+" Message of the Day -")
	for _, l := range lines {
		s.reply(c, irc.RplMotd, "- "+l)
	}
	s.reply(c, irc.RplEndOfMotd, "End of /MOTD command.")
}
