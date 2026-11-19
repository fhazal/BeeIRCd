package server

// Oper and info commands for a linked
// network: /LINKS, /MAP, /CONNECT, /SQUIT, /STATS, /ADMIN, remote
// /MOTD /VERSION /ADMIN /TIME <server>, and K-lines (this server only).

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/fhazal/beeircd/internal/config"
	"github.com/fhazal/beeircd/internal/irc"
	"github.com/fhazal/beeircd/internal/store"
	"github.com/fhazal/beeircd/internal/version"
)

// Numerics used here only.
const (
	rplMap         = "006"
	rplMapEnd      = "007"
	rplStatsLinks  = "211"
	rplStatsCLine  = "213"
	rplStatsKLine  = "216"
	rplEndOfStats  = "219"
	rplStatsUptime = "242"
	rplAdminMe     = "256"
	rplAdminLoc1   = "257"
	rplAdminLoc2   = "258"
	rplAdminEmail  = "259"
	rplLinks       = "364"
	rplEndOfLinks  = "365"
	errNoSuchSrv   = "402"
)

// netServer is one server for /LINKS and /MAP.
type netServer struct {
	name, sid, uplink, desc string
	hops                    int
}

// netServers is every server on the network, us first.
func (s *Server) netServers() []netServer {
	out := []netServer{{name: s.cfg.Server.Name, sid: s.cfg.Server.SID, desc: s.cfg.Server.Description}}
	if si, ok := s.servicesInfo(); ok {
		out = append(out, netServer{si.Name, si.SID, s.cfg.Server.SID, si.Description, 1})
	}
	for _, j := range s.jupeInfos() {
		out = append(out, netServer{j.Name, j.SID, s.cfg.Server.SID, j.Description, 1})
	}
	var rest []netServer
	for _, r := range s.links.servers {
		rest = append(rest, netServer{r.name, r.sid, r.uplink, r.desc, r.hops})
	}
	sort.Slice(rest, func(i, k int) bool {
		if rest[i].hops != rest[k].hops {
			return rest[i].hops < rest[k].hops
		}
		return rest[i].name < rest[k].name
	})
	return append(out, rest...)
}

func (s *Server) sidName(sid string) string {
	for _, n := range s.netServers() {
		if n.sid == sid {
			return n.name
		}
	}
	return sid
}

// cmdLinks is LINKS [mask]: every server and which one it hangs off.
func (s *Server) cmdLinks(c *Client, m irc.Message) {
	mask := "*"
	if len(m.Params) > 0 {
		mask = m.Params[len(m.Params)-1]
	}
	for _, n := range s.netServers() {
		if !irc.Match(mask, n.name) || (s.hideServers(c) && n.sid != s.cfg.Server.SID) {
			continue
		}
		up := n.name
		if n.uplink != "" {
			up = s.sidName(n.uplink)
		}
		s.reply(c, rplLinks, n.name, up, strconv.Itoa(n.hops)+" "+n.desc)
	}
	s.reply(c, rplEndOfLinks, mask, "End of /LINKS list.")
}

// cmdMap is MAP: the server tree with people on each.
func (s *Server) cmdMap(c *Client, m irc.Message) {
	if s.hideServers(c) {
		n := 0
		for _, u := range s.net.Users() {
			if !u.Service {
				n++
			}
		}
		s.reply(c, rplMap, fmt.Sprintf("%s %d", s.cfg.Server.Name, n))
		s.reply(c, rplMapEnd, "End of /MAP")
		return
	}
	count := map[string]int{}
	for _, u := range s.net.Users() {
		if !u.Service {
			count[uidServer(u.UID)]++
		}
	}
	kids := map[string][]netServer{}
	all := s.netServers()
	for _, n := range all[1:] {
		kids[n.uplink] = append(kids[n.uplink], n)
	}
	var walk func(n netServer, prefix string, last bool, top bool)
	walk = func(n netServer, prefix string, last bool, top bool) {
		branch, next := "", prefix
		if !top {
			if last {
				branch, next = prefix+"`- ", prefix+"   "
			} else {
				branch, next = prefix+"|- ", prefix+"|  "
			}
		}
		s.reply(c, rplMap, fmt.Sprintf("%s%s (%s) %d", branch, n.name, n.sid, count[n.sid]))
		ks := kids[n.sid]
		sort.Slice(ks, func(i, k int) bool { return ks[i].name < ks[k].name })
		for i, k := range ks {
			walk(k, next, i == len(ks)-1, false)
		}
	}
	walk(all[0], "", true, true)
	s.reply(c, rplMapEnd, "End of /MAP")
}

// cmdConnect is CONNECT <server>: connect to a [[link.server]] block now
// (oper flag c).
func (s *Server) cmdConnect(c *Client, m irc.Message) {
	if !s.needFlag(c, nil, 'c') {
		return
	}
	if len(m.Params) < 1 {
		s.reply(c, irc.ErrNeedMoreParams, "CONNECT", "Not enough parameters")
		return
	}
	s.notice(c, "*** "+s.linkNow(c, m.Params[0]))
}

// linkNow connects to block name now; the result is a sentence for opers.
func (s *Server) linkNow(c *Client, name string) string { return s.linkNowBy(c.user.Nick, name) }

// linkNowBy is linkNow asked by who (a nick, or an oper in the Hive Panel).
func (s *Server) linkNowBy(who, name string) string {
	b := s.cfg.Link.Server(name)
	switch {
	case !s.cfg.Link.Linked():
		return "This server doesn't link ([link] role = standalone)."
	case b == nil:
		return "There is no link block for " + name + "."
	case b.Host == "":
		return b.Name + " connects to us; it has no host to connect to."
	case s.linkKnown(b.Name) || s.linkPending(b.Name):
		return b.Name + " is linked or linking already."
	}
	key := strings.ToLower(b.Name)
	r := s.links.retry[key]
	if r == nil {
		r = &linkRetry{}
		s.links.retry[key] = r
	}
	if r.dialing {
		return "Already connecting to " + b.Name + "."
	}
	r.dialing, r.next = true, time.Now().Add(linkRetryMin)
	s.wg.Add(1)
	go s.dialLink(*b)
	s.lnotice("CONNECT: Connecting to server \x02%s\x02 (%s:%d), asked by %s", b.Name, b.Host, b.Port, who)
	return fmt.Sprintf("Connecting to %s (%s:%d). Opers with +s see how it goes.", b.Name, b.Host, b.Port)
}

// cmdSquit is SQUIT <server> [reason]: end a link here, or split a server
// further away (oper flag c).
func (s *Server) cmdSquit(c *Client, m irc.Message) {
	if !s.needFlag(c, nil, 'c') {
		return
	}
	if len(m.Params) < 1 {
		s.reply(c, irc.ErrNeedMoreParams, "SQUIT", "Not enough parameters")
		return
	}
	name := m.Params[0]
	reason := "SQUIT by " + c.user.Nick
	if len(m.Params) > 1 && m.Params[len(m.Params)-1] != "" {
		reason = m.Params[len(m.Params)-1]
	}
	switch text, ok := s.squitBy(c.user.Nick, c.uid, name, reason); {
	case !ok:
		s.reply(c, errNoSuchSrv, name, "No such server")
	case text != "":
		s.notice(c, "*** "+text)
	}
}

// squitBy splits server name off, asked by who (from source: a UID or our
// SID). ok is false when there is no such server; text is for the asker.
func (s *Server) squitBy(who, source, name, reason string) (text string, ok bool) {
	if j := s.jupeFor(name); j != nil {
		delete(s.links.jupes, strings.ToLower(j.name))
		s.linkBroadcast(nil, fmt.Sprintf(":%s SQUIT %s :%s", s.cfg.Server.SID, j.name, reason))
		return "The jupe on " + j.name + " is lifted.", true
	}
	r := s.serverByName(name)
	if r == nil || r.via == nil {
		return "", false
	}
	s.lnotice("SQUIT: Server \x02%s\x02 removed from network by %s", r.name, who)
	if r.sid == r.via.peer.SID {
		s.dropLink(r.via, reason)
		return "", true
	}
	r.via.send(fmt.Sprintf(":%s SQUIT %s :%s", source, r.name, reason))
	gone, users := s.splitOff(r.sid, s.links.servers[r.uplink].nameOr(r.uplink), r.name)
	s.linkBroadcast(r.via, fmt.Sprintf(":%s SQUIT %s :%s", r.uplink, r.name, reason))
	s.remoteSplit(r.name, s.links.servers[r.uplink].nameOr(r.uplink), reason, gone, users)
	return "", true
}

// cmdStats is STATS <letter> [server]: l links, c link blocks, k
// K-lines, u uptime (l, c and k for opers).
func (s *Server) cmdStats(c *Client, m irc.Message) {
	letter := "*"
	if len(m.Params) > 0 && m.Params[0] != "" {
		letter = m.Params[0][:1]
	}
	if len(m.Params) > 1 && s.queryElsewhere(c, "STATS", m.Params[1], letter) {
		return
	}
	s.statsHere(c, letter)
}

func (s *Server) statsHere(c *Client, letter string) {
	if u := c.user; u != nil {
		s.snoOnly('t', "Stats '"+letter+"' requested by "+u.Nick+" ("+u.TrueIdent()+"@"+u.RealHost+")")
	}
	switch letter {
	case "l", "L":
		if !c.isOper() {
			break
		}
		for _, st := range s.linkStatus() {
			if st.State != "up" {
				continue
			}
			// sendq sent recv_lines ... (as UnrealIRCd shows): we give
			// the name, state and seconds up.
			s.reply(c, rplStatsLinks, st.Name, "0", "0", "0", "0", "0", strconv.Itoa(int(time.Since(st.Since).Seconds())))
		}
	case "c", "C":
		if !c.isOper() {
			break
		}
		for _, b := range s.cfg.Link.Servers {
			s.reply(c, rplStatsCLine, "C", orStr(b.Host, "*"), "*", b.Name, strconv.Itoa(b.Port), "servers")
		}
	case "k", "K":
		if !c.isOper() {
			break
		}
		for _, b := range s.svc.Klines() {
			user, host, _ := strings.Cut(b.Mask, "@")
			s.reply(c, rplStatsKLine, "K", host, "*", user, b.Reason)
		}
	case "f", "F":
		if c.can('k') {
			s.statsSpam(c)
		}
	case "u", "U":
		up := time.Since(s.created)
		days := int(up.Hours()) / 24
		s.reply(c, rplStatsUptime, fmt.Sprintf("Server Up %d days, %d:%02d:%02d", days, int(up.Hours())%24, int(up.Minutes())%60, int(up.Seconds())%60))
	}
	s.reply(c, rplEndOfStats, letter, "End of /STATS report")
}

// cmdAdmin is ADMIN [server]: who runs the server.
func (s *Server) cmdAdmin(c *Client, m irc.Message) {
	if len(m.Params) > 0 && s.queryElsewhere(c, "ADMIN", m.Params[0], "") {
		return
	}
	s.adminHere(c)
}

func (s *Server) adminHere(c *Client) {
	s.reply(c, rplAdminMe, s.cfg.Server.Name, "Administrative info")
	s.reply(c, rplAdminLoc1, s.cfg.Server.Description)
	s.reply(c, rplAdminLoc2, s.cfg.Server.Network)
	s.reply(c, rplAdminEmail, "Admin: "+orStr(s.cfg.Services.Admin, "not set"))
}

// queryElsewhere sends "CMD [arg] :server" to another server named by
// where (a server name, or the nick of someone on it); false = answer
// here. Unknown names get 402.
func (s *Server) queryElsewhere(c *Client, cmd, where, arg string) bool {
	if strings.EqualFold(where, s.cfg.Server.Name) || irc.Match(where, s.cfg.Server.Name) && !strings.ContainsAny(where, "*?") {
		return false
	}
	var r *remoteServer
	if r = s.serverByName(where); r == nil {
		if u := s.net.UserByNick(where); u != nil {
			if s.ours(u.UID) {
				return false
			}
			r = s.links.servers[uidServer(u.UID)]
		}
	}
	if r == nil {
		if si, ok := s.servicesInfo(); ok && strings.EqualFold(where, si.Name) {
			return false // our services: we answer
		}
		for _, x := range s.links.servers {
			if irc.Match(where, x.name) {
				r = x
				break
			}
		}
	}
	if r == nil || r.via == nil {
		s.reply(c, errNoSuchSrv, where, "No such server")
		return true
	}
	if arg != "" {
		r.via.send(":" + c.uid + " " + cmd + " " + arg + " :" + r.name)
	} else {
		r.via.send(":" + c.uid + " " + cmd + " :" + r.name)
	}
	return true
}

// remoteQuery is MOTD/VERSION/ADMIN/TIME/STATS <server> from someone on
// another server: answered here when it names us, else passed on.
func (s *Server) remoteQuery(l *linkConn, m irc.Message) {
	asker := s.net.UserByUID(m.Source)
	if asker == nil || !s.behind(l, uidServer(asker.UID)) || len(m.Params) == 0 {
		return
	}
	target := m.Params[len(m.Params)-1]
	if !strings.EqualFold(target, s.cfg.Server.Name) && target != s.cfg.Server.SID {
		r := s.serverByName(target)
		if r == nil {
			r = s.links.servers[target]
		}
		if r != nil && r.via != nil && r.via != l {
			r.via.send(m.String())
		}
		return
	}
	s.runFor(asker.UID, func(c *Client) {
		switch m.Command {
		case "MOTD":
			s.motd(c)
		case "VERSION":
			s.reply(c, irc.RplVersion, version.Name+"-"+version.Version+".", s.cfg.Server.Name, version.String())
		case "ADMIN":
			s.adminHere(c)
		case "TIME":
			s.reply(c, irc.RplTime, s.cfg.Server.Name, time.Now().Format("Monday January 2 2006 -- 15:04:05 -07:00"))
		case "TRACE":
			s.traceHere(c)
		case "STATS":
			letter := "*"
			if len(m.Params) > 1 && m.Params[0] != "" {
				letter = m.Params[0][:1]
			}
			s.statsHere(c, letter)
		}
	})
}

// ---- K-lines ----

// cmdKline is KLINE [+time] <nick|user@host> [duration] [reason] (oper
// flag k): a ban on this server only; whoever it covers here is
// disconnected. Like InspIRCd, KLINE <mask> alone (or KLINE -<mask>)
// removes the K-line, and a duration after the mask (0 = until removed)
// sets how long it lasts.
func (s *Server) cmdKline(c *Client, m irc.Message) {
	if !s.needFlag(c, nil, 'k') {
		return
	}
	args := append([]string{}, m.Params...)
	var life time.Duration
	timed := false
	if len(args) > 0 && strings.HasPrefix(args[0], "+") {
		d, err := parseLife(args[0][1:])
		if err != nil {
			s.stdReply(c, "FAIL", "KLINE", "INVALID_DURATION", args[0]+" isn't a time. Use +30m, +12h, +7d, or +0 for until removed.", "", args[0])
			return
		}
		life, args, timed = d, args[1:], true
	}
	if len(args) == 0 {
		s.reply(c, irc.ErrNeedMoreParams, "KLINE", "Not enough parameters")
		return
	}
	if rest, ok := strings.CutPrefix(args[0], "-"); ok || len(args) == 1 && !timed {
		if !ok {
			rest = args[0]
		}
		if rest == "" {
			s.reply(c, irc.ErrNeedMoreParams, "KLINE", "Not enough parameters")
			return
		}
		s.removeKline(c, "KLINE", rest)
		return
	}
	if !timed && len(args) > 1 {
		if d, err := parseLife(args[1]); err == nil {
			life, timed = d, true
			args = append(args[:1:1], args[2:]...)
		}
	}
	mask := args[0]
	// A % before the mask makes a soft K-line: logged-in people pass.
	soft := strings.HasPrefix(mask, "%")
	mask = strings.TrimPrefix(mask, "%")
	if !strings.Contains(mask, "@") {
		u := s.net.UserByNick(mask)
		if u == nil {
			s.reply(c, irc.ErrNoSuchNick, mask, "No such nick")
			return
		}
		mask = "*@" + u.RealHost
	}
	if user, host, _ := strings.Cut(mask, "@"); user == "" || user == "%" || host == "" || strings.Trim(host, "*?.") == "" {
		s.stdReply(c, "FAIL", "KLINE", "MASK_TOO_WIDE", "That mask would ban everyone; give a user@host with a real host part.", "", mask)
		return
	}
	reason := orStr(strings.Join(args[1:], " "), "No reason")
	match := mask
	if soft {
		mask = "%" + mask
	}
	if err := s.svc.AddKline(mask, reason, c.user.Nick, life); err != nil {
		s.log.Printf("services database: %v", err)
		s.stdReply(c, "FAIL", "KLINE", "INTERNAL_ERROR", "The K-line couldn't be saved.", "")
		return
	}
	hit := 0
	for _, o := range s.clients {
		if o.user == nil || o.closed || o.user.Service || o.isOper() || soft && o.user.Account != "" {
			continue
		}
		u := o.user
		if irc.MatchUserHost(strings.ToLower(match), u.TrueIdent(), u.RealHost, u.IP) {
			s.reply(o, irc.ErrYoureBannedCreep, "You are banned from this server: "+reason)
			s.quit(o, "K-Lined: "+reason)
			hit++
		}
	}
	until := "until removed"
	if life > 0 {
		until = "for " + config.DurationWords(life)
	}
	now := time.Now()
	var expires time.Time
	if life > 0 {
		expires = now.Add(life)
	}
	s.snoBanAdded('x', c.user.Nick, store.BanKline, strings.ToLower(mask), reason, now, expires)
	s.notice(c, fmt.Sprintf("*** K-line on %s added %s.", strings.ToLower(mask), until))
}

// cmdUnkline is UNKLINE <user@host> (oper flag k).
func (s *Server) cmdUnkline(c *Client, m irc.Message) {
	if !s.needFlag(c, nil, 'k') {
		return
	}
	if len(m.Params) < 1 {
		s.reply(c, irc.ErrNeedMoreParams, "UNKLINE", "Not enough parameters")
		return
	}
	s.removeKline(c, "UNKLINE", m.Params[0])
}

// removeKline takes the K-line on mask away for UNKLINE, KLINE -mask and
// KLINE mask.
func (s *Server) removeKline(c *Client, cmd, mask string) {
	why := ""
	for _, b := range s.svc.Klines() {
		if b.Mask == strings.ToLower(mask) {
			why = b.Reason
		}
	}
	gone, err := s.svc.DelKline(mask)
	switch {
	case err != nil:
		s.log.Printf("services database: %v", err)
		s.stdReply(c, "FAIL", cmd, "INTERNAL_ERROR", "The K-line couldn't be removed.", "")
	case !gone && cmd == "KLINE":
		s.notice(c, "*** No such K-line: "+strings.ToLower(mask)+".")
	case !gone:
		s.stdReply(c, "FAIL", cmd, "NO_SUCH_KLINE", "There is no K-line on "+mask+".", "", mask)
	default:
		s.snoBanRemoved('x', c.user.Nick, store.BanKline, strings.ToLower(mask), why)
		s.notice(c, "*** K-line on "+strings.ToLower(mask)+" removed.")
	}
}

// parseLife reads a ban time: 30m, 12h, 7d, 2m30s, 90 (seconds), 0.
func parseLife(v string) (time.Duration, error) {
	return config.ParseBanDuration(v)
}
