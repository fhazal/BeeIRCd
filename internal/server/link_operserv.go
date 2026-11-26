package server

// Oper tools for a linked network: OperServ JUPE,
// NOOP and SERVER (the [[link.server]] blocks, L4), STATS UPLINK lines,
// and AdminServ on another server (/as server <name> ...).

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/fhazal/beeircd/internal/config"
	"github.com/fhazal/beeircd/internal/irc"
	"github.com/fhazal/beeircd/internal/link"
	"github.com/fhazal/beeircd/internal/services"
	"github.com/fhazal/beeircd/internal/state"
	"github.com/fhazal/beeircd/internal/version"
)

// ---- people on other servers using server-side commands ----

// standIn is a Client for someone on another server, for the commands
// the server runs itself (AdminServ, OperServ JUPE ...). Nothing is ever
// sent on it: lines it would get are passed back with flushStandIn.
func (s *Server) standIn(uid string) *Client {
	u := s.net.UserByUID(uid)
	if u == nil || s.ours(uid) {
		return nil
	}
	if s.links.stand == nil {
		s.links.stand = map[string]*Client{}
	}
	c := s.links.stand[uid]
	if c == nil || c.user != u {
		c = &Client{s: s, uid: uid, user: u, registered: true, caps: map[string]bool{}}
		s.links.stand[uid] = c
	}
	c.operFlags = host{s}.OperFlags(uid)
	c.operReach = s.reachOf(uid)
	lines := []string{}
	c.capture = &lines
	return c
}

// flushStandIn sends what a stand-in collected to the real person:
// numerics as numerics, notices as notices.
func (s *Server) flushStandIn(c *Client) {
	if c.capture == nil {
		return
	}
	lines := *c.capture
	c.capture = nil
	h := host{s}
	for _, line := range lines {
		m, err := irc.Parse(line)
		if err != nil || len(m.Params) == 0 {
			continue
		}
		switch {
		case len(m.Command) == 3 && m.Command[0] >= '0' && m.Command[0] <= '9':
			h.Numeric(c.uid, m.Command, m.Params[1:]...)
		case m.Command == "NOTICE":
			h.ServerNotice(c.uid, m.Params[len(m.Params)-1])
		}
	}
}

// forgetStandIns drops stand-ins of people who left.
func (s *Server) forgetStandIns() {
	for uid, c := range s.links.stand {
		if s.net.UserByUID(uid) != c.user {
			delete(s.links.stand, uid)
		}
	}
}

// runFor runs f for uid: our client, or a stand-in for someone elsewhere.
func (s *Server) runFor(uid string, f func(c *Client)) {
	if c := s.clients[uid]; c != nil {
		if c.user != nil {
			f(c)
		}
		return
	}
	c := s.standIn(uid)
	if c == nil {
		return
	}
	f(c)
	s.flushStandIn(c)
	s.forgetStandIns()
}

// ---- STATS UPLINK ----

// uplinkLines describes each link for OperServ STATS UPLINK.
func (s *Server) uplinkLines() []string {
	var out []string
	for _, st := range s.linkStatus() {
		switch st.State {
		case "up":
			line := fmt.Sprintf("Link %s (%s): up %s, lag %.0f ms, %s", st.Name, st.SID, durShort(time.Since(st.Since)), st.LagMS, orStr(st.Software, "unknown software"))
			if st.Behind > 0 {
				line += fmt.Sprintf(", %s behind it", plural(st.Behind, "server"))
			}
			out = append(out, line)
		default:
			line := fmt.Sprintf("Link %s: %s", st.Name, st.State)
			if st.Error != "" {
				line += " (" + st.Error + ")"
			}
			out = append(out, line)
		}
	}
	return out
}

func durShort(d time.Duration) string {
	d = d.Round(time.Second)
	switch {
	case d < time.Minute:
		return d.String()
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
	}
	return fmt.Sprintf("%dd", int(d.Hours()/24))
}

// ---- OperServ commands the server runs ----

func (s *Server) operServExtras() map[string]services.BotCommand {
	run := func(f func(*Server, *Client, []string)) func(uid string, args []string) {
		return func(uid string, args []string) { s.runFor(uid, func(c *Client) { f(s, c, args) }) }
	}
	return map[string]services.BotCommand{
		"JUPE": {Syntax: "JUPE <server> [reason] | JUPE LIST | JUPE DEL <server>", Short: "block a server name",
			Long: "JUPE <server> [reason]   splits the server if it is linked and puts a fake\n                         one in its place, so it can't link again\nJUPE LIST                the jupes\nJUPE DEL <server>        lift a jupe\nNeeds oper flag c (links).", Run: run(osJupe)},
		"NOOP": {Syntax: "NOOP SET|REVOKE <server>", Short: "no IRC operators on a server",
			Long: "NOOP SET <server>     everyone opered on that server loses it and nobody\n                      can /oper there until NOOP REVOKE\nNOOP REVOKE <server>  lifts it\nNeeds oper flag c (links).", Run: run(osNoop)},
		"SERVER": {Syntax: "SERVER LIST|ADD|SET|DEL|LINK|UNLINK ...", Short: "the servers we link with",
			Long: serverHelp, Run: run(func(s *Server, c *Client, args []string) { s.serverCmd(c, s.svcBot("OperServ"), args) })},
	}
}

// svcBot is a services bot by nick (nil when services are off here).
func (s *Server) svcBot(nick string) *state.User {
	u := s.net.UserByNick(nick)
	if u == nil || !u.Service {
		return nil
	}
	return u
}

// ---- JUPE ----

type jupe struct {
	name, sid, reason string
}

// freeSID finds a SID nobody uses, for a jupe.
func (s *Server) freeSID() string {
	for _, a := range "0123456789" {
		for _, b := range "JABCDEFGHIKLMNOPQRSTUVWXYZ" {
			for _, c := range "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZ" {
				sid := string([]rune{a, b, c})
				if !s.linkKnown(sid) {
					return sid
				}
			}
		}
	}
	return ""
}

func (s *Server) jupeFor(x string) *jupe {
	for _, j := range s.links.jupes {
		if strings.EqualFold(j.name, x) || j.sid == x {
			return j
		}
	}
	return nil
}

func osJupe(s *Server, c *Client, args []string) {
	bot := s.svcBot("OperServ")
	if !s.needFlag(c, bot, 'c') {
		return
	}
	say := func(format string, a ...any) { s.botReply(c, bot, fmt.Sprintf(format, a...)) }
	sub := strings.ToUpper(argAt(args, 0))
	switch {
	case sub == "LIST" || sub == "":
		if len(s.links.jupes) == 0 {
			say("No server is juped.")
			return
		}
		names := make([]string, 0, len(s.links.jupes))
		for _, j := range s.links.jupes {
			names = append(names, j.name+" ("+j.reason+")")
		}
		sort.Strings(names)
		for _, n := range names {
			say("%s", n)
		}
		return
	case sub == "DEL" && len(args) > 1:
		j := s.jupeFor(args[1])
		if j == nil {
			say("%s isn't juped.", args[1])
			return
		}
		delete(s.links.jupes, strings.ToLower(j.name))
		s.linkBroadcast(nil, fmt.Sprintf(":%s SQUIT %s :Jupe removed by %s", s.cfg.Server.SID, j.name, c.user.Nick))
		s.audit(c, bot, "JUPE DEL %s", j.name)
		s.sno('a', "%s lifted the jupe on %s", c.user.Nick, j.name)
		say("The jupe on %s is lifted.", j.name)
		return
	}
	name := args[0]
	reason := orStr(strings.Join(args[1:], " "), "No reason")
	si, svcOK := s.servicesInfo()
	switch {
	case !config.ValidServerName(name) || !strings.Contains(name, "."):
		say("%s isn't a server name.", name)
		return
	case strings.EqualFold(name, s.cfg.Server.Name) || svcOK && strings.EqualFold(name, si.Name):
		say("You can't jupe this server or its services.")
		return
	case s.jupeFor(name) != nil:
		say("%s is juped already.", name)
		return
	}
	if r := s.serverByName(name); r != nil {
		why := "Juped by " + c.user.Nick + ": " + reason
		if r.via != nil && r.sid == r.via.peer.SID {
			s.dropLink(r.via, why)
		} else if r.via != nil {
			r.via.send(fmt.Sprintf(":%s SQUIT %s :%s", s.cfg.Server.SID, r.name, why))
			gone, users := s.splitOff(r.sid, s.links.servers[r.uplink].nameOr(r.uplink), r.name, why)
			s.linkBroadcast(r.via, fmt.Sprintf(":%s SQUIT %s :%s", r.uplink, r.name, why))
			s.remoteSplit(r.name, s.links.servers[r.uplink].nameOr(r.uplink), why, gone, users)
		}
	}
	sid := s.freeSID()
	if sid == "" {
		say("No free server ID for the jupe.")
		return
	}
	if s.links.jupes == nil {
		s.links.jupes = map[string]*jupe{}
	}
	j := &jupe{name: name, sid: sid, reason: "JUPED by " + c.user.Nick + ": " + reason}
	s.links.jupes[strings.ToLower(name)] = j
	s.linkBroadcast(nil, fmt.Sprintf(":%s SID %s 2 %s :%s", s.cfg.Server.SID, j.name, j.sid, j.reason))
	s.linkBroadcast(nil, ":"+j.sid+" EOS")
	s.audit(c, bot, "JUPE %s %s", name, reason)
	s.sno('a', "%s juped %s: %s", c.user.Nick, name, reason)
	say("%s is juped.", name)
}

// jupeInfos are our jupes for a burst.
func (s *Server) jupeInfos() []link.ServerInfo {
	var out []link.ServerInfo
	for _, j := range s.links.jupes {
		out = append(out, link.ServerInfo{Name: j.name, SID: j.sid, Description: j.reason, Uplink: s.cfg.Server.SID, Hops: 1, Synced: true})
	}
	sort.Slice(out, func(i, k int) bool { return out[i].SID < out[k].SID })
	return out
}

// squitJupe is SQUIT for one of our jupes (an oper elsewhere removed it).
func (s *Server) squitJupe(l *linkConn, target string) bool {
	j := s.jupeFor(target)
	if j == nil {
		return false
	}
	delete(s.links.jupes, strings.ToLower(j.name))
	s.linkBroadcast(l, fmt.Sprintf(":%s SQUIT %s :Jupe removed", s.cfg.Server.SID, j.name))
	s.sno('a', "The jupe on %s was removed from %s", j.name, l.peer.Name)
	return true
}

// ---- NOOP ----

func osNoop(s *Server, c *Client, args []string) {
	bot := s.svcBot("OperServ")
	if !s.needFlag(c, bot, 'c') {
		return
	}
	say := func(format string, a ...any) { s.botReply(c, bot, fmt.Sprintf(format, a...)) }
	sub := strings.ToUpper(argAt(args, 0))
	if (sub != "SET" && sub != "REVOKE") || len(args) < 2 {
		say("Syntax: NOOP SET|REVOKE <server>")
		return
	}
	on := sub == "SET"
	name := args[1]
	switch {
	case strings.EqualFold(name, s.cfg.Server.Name):
		s.setNoop(on, c.user.Nick)
	default:
		r := s.serverByName(name)
		if r == nil || r.via == nil {
			say("%s isn't linked.", name)
			return
		}
		sign := "-"
		if on {
			sign = "+"
		}
		r.via.send(fmt.Sprintf(":%s SVSNOOP %s %s", s.svcSource(), r.name, sign))
		name = r.name
	}
	s.audit(c, bot, "NOOP %s %s", sub, name)
	if on {
		say("Nobody can be an IRC operator on %s now.", name)
	} else {
		say("IRC operators are back on %s.", name)
	}
}

// setNoop turns NOOP on or off here: opers lose it and /oper is refused.
func (s *Server) setNoop(on bool, by string) {
	if s.links.noop == on {
		return
	}
	s.links.noop = on
	if !on {
		s.sno('a', "%s lifted NOOP: /oper works again on %s", by, s.cfg.Server.Name)
		return
	}
	for c := range s.opers {
		s.deoper(c, "NOOP: nobody may be an IRC operator on this server for now.")
	}
	s.sno('a', "%s set NOOP: nobody may be an IRC operator on %s", by, s.cfg.Server.Name)
}

// remoteNoop is SVSNOOP <server> +|-.
func (s *Server) remoteNoop(l *linkConn, m irc.Message) {
	if len(m.Params) < 2 {
		return
	}
	if strings.EqualFold(m.Params[0], s.cfg.Server.Name) || m.Params[0] == s.cfg.Server.SID {
		by := m.Source
		if r := s.links.servers[by]; r != nil {
			by = r.name
		}
		s.setNoop(m.Params[1] == "+", by)
		return
	}
	if r := s.serverByName(m.Params[0]); r != nil && r.via != l && r.via != nil {
		r.via.send(m.String())
	}
}

// ---- SERVER: link blocks (L4) ----

const serverHelp = "SERVER LIST                       the link blocks and how each link is\n" +
	"SERVER ADD <name> <fingerprint> [host [port]]   a new [[link.server]] block\n" +
	"SERVER SET <name> HOST|PORT|FINGERPRINT|PASSWORD|AUTOCONNECT|SENDQ <value>\n" +
	"SERVER DEL <name>                 remove the block (drops the link)\n" +
	"SERVER LINK <name>                connect now\n" +
	"SERVER UNLINK <name> [reason]     end the link now (autoconnect tries again)\n" +
	"AdminServ also has: SERVER <name> STATUS and SERVER <name> <command>.\n" +
	"LIST, LINK and UNLINK need oper flag c, the rest flag a. Changes are saved\n" +
	"to beeircd.conf."

// serverCmd is OperServ SERVER (and AdminServ SERVER) for c; bot answers.
func (s *Server) serverCmd(c *Client, bot *state.User, args []string) {
	say := func(format string, a ...any) { s.botReply(c, bot, fmt.Sprintf(format, a...)) }
	if !s.needFlag(c, bot, 'c') {
		return
	}
	if !s.cfg.Link.Linked() {
		say("This server doesn't link ([link] role = standalone); set role hub or leaf and restart first.")
		return
	}
	sub := strings.ToUpper(argAt(args, 0))
	switch sub {
	case "", "LIST":
		st := s.linkStatus()
		if len(st) == 0 {
			say("No link blocks yet: SERVER ADD <name> <fingerprint> [host [port]]")
		}
		for _, l := range st {
			b := s.cfg.Link.Server(l.Name)
			where := "they connect to us"
			if b != nil && b.Host != "" {
				where = b.Host + ":" + strconv.Itoa(b.Port)
				if b.Autoconnect {
					where += ", autoconnect"
				}
			}
			if b == nil {
				where = "no block"
			}
			line := fmt.Sprintf("%s: %s (%s)", l.Name, l.State, where)
			if l.State == "up" {
				line = fmt.Sprintf("%s: up %s, lag %.0f ms (%s)", l.Name, durShort(time.Since(l.Since)), l.LagMS, where)
			}
			say("%s", line)
		}
		for _, j := range s.jupeInfos() {
			say("%s: juped (%s)", j.Name, j.Description)
		}
		return
	case "LINK":
		b := s.cfg.Link.Server(argAt(args, 1))
		switch {
		case b == nil:
			say("There is no link block for %s (SERVER LIST).", argAt(args, 1))
		case b.Host == "":
			say("%s connects to us; it has no host to connect to.", b.Name)
		case s.linkKnown(b.Name) || s.linkPending(b.Name):
			say("%s is linked or linking already.", b.Name)
		default:
			key := strings.ToLower(b.Name)
			r := s.links.retry[key]
			if r == nil {
				r = &linkRetry{}
				s.links.retry[key] = r
			}
			if r.dialing {
				say("Already connecting to %s.", b.Name)
				return
			}
			r.dialing, r.next = true, time.Now().Add(linkRetryMin)
			s.wg.Add(1)
			go s.dialLink(*b)
			s.audit(c, bot, "SERVER LINK %s", b.Name)
			say("Connecting to %s (%s:%d). Opers with +s see how it goes.", b.Name, b.Host, b.Port)
		}
		return
	case "UNLINK":
		name := argAt(args, 1)
		for l := range s.links.conns {
			if strings.EqualFold(l.name(), name) {
				why := orStr(strings.Join(args[min(2, len(args)):], " "), "unlinked by "+c.user.Nick)
				s.dropLink(l, why)
				s.audit(c, bot, "SERVER UNLINK %s %s", name, why)
				say("The link with %s is ended.", l.name())
				return
			}
		}
		say("%s isn't linked to this server.", name)
		return
	case "ADD", "SET", "DEL":
		if !s.needFlag(c, bot, 'a') {
			return
		}
		s.serverEdit(c, bot, sub, args[1:])
		return
	}
	say("Syntax: SERVER LIST|ADD|SET|DEL|LINK|UNLINK ... (/msg %s HELP SERVER)", orStr(nickOf(bot), "OperServ"))
}

func nickOf(u *state.User) string {
	if u == nil {
		return ""
	}
	return u.Nick
}

// serverEdit is SERVER ADD/SET/DEL: it rewrites the block in beeircd.conf
// and applies it like a REHASH would.
func (s *Server) serverEdit(c *Client, bot *state.User, sub string, args []string) {
	say := func(format string, a ...any) { s.botReply(c, bot, fmt.Sprintf(format, a...)) }
	if s.cfg.Path == "" {
		say("This server wasn't started from a config file, so link blocks can't be saved.")
		return
	}
	if len(args) == 0 {
		say("Syntax: SERVER %s <name> ...", sub)
		return
	}
	name := args[0]
	old := s.cfg.Link.Server(name)
	var b config.LinkServer
	switch sub {
	case "ADD":
		if old != nil {
			say("%s has a block already: SERVER SET %s ...", old.Name, old.Name)
			return
		}
		if len(args) < 2 {
			say("Syntax: SERVER ADD <name> <fingerprint> [host [port]]   (the fingerprint: beeircd link-info there, or ./unrealircd spkifp)")
			return
		}
		b = config.LinkServer{Name: name, Fingerprint: args[1], SendQMB: 20}
		if len(args) > 2 {
			b.Host, b.Port, b.Autoconnect = args[2], config.LinkPort, true
		}
		if len(args) > 3 {
			p, err := strconv.Atoi(args[3])
			if err != nil {
				say("%s isn't a port.", args[3])
				return
			}
			b.Port = p
		}
	case "SET":
		if old == nil {
			say("There is no link block for %s (SERVER LIST).", name)
			return
		}
		if len(args) < 3 {
			say("Syntax: SERVER SET <name> HOST|PORT|FINGERPRINT|PASSWORD|AUTOCONNECT|SENDQ <value>")
			return
		}
		b = *old
		value := strings.Join(args[2:], " ")
		switch strings.ToUpper(args[1]) {
		case "HOST":
			b.Host = value
			if strings.EqualFold(value, "OFF") {
				b.Host, b.Autoconnect = "", false
			} else if b.Port == 0 {
				b.Port = config.LinkPort
			}
		case "PORT", "SENDQ":
			n, err := strconv.Atoi(value)
			if err != nil || n <= 0 {
				say("%s isn't a number.", value)
				return
			}
			if strings.EqualFold(args[1], "PORT") {
				b.Port = n
			} else {
				b.SendQMB = n
			}
		case "FINGERPRINT":
			b.Fingerprint = value
		case "PASSWORD":
			b.Password = value
			if strings.EqualFold(value, "OFF") {
				b.Password = ""
			}
		case "AUTOCONNECT":
			on, ok := onOff(value)
			if !ok {
				say("AUTOCONNECT is ON or OFF.")
				return
			}
			b.Autoconnect = on
		default:
			say("Syntax: SERVER SET <name> HOST|PORT|FINGERPRINT|PASSWORD|AUTOCONNECT|SENDQ <value>")
			return
		}
	case "DEL":
		if old == nil {
			say("There is no link block for %s (SERVER LIST).", name)
			return
		}
		b = *old
	}
	if sub != "DEL" {
		if err := b.Check(s.cfg.Server.Name); err != nil {
			say("Not saved: %v", err)
			return
		}
	}
	fresh, err := config.Edit(s.cfg.Path, func(d *config.Doc) error {
		if sub == "DEL" {
			d.DelBlock("link.server", "name", b.Name)
			return nil
		}
		d.SetBlock("link.server", linkBlockKV(b))
		return nil
	})
	if err != nil {
		say("Not saved: %v", err)
		return
	}
	s.cfg.Link.Servers = fresh.Link.Servers
	s.linkRehash()
	what := sub + " " + b.Name
	if sub == "SET" {
		what += " " + strings.ToUpper(args[1])
	}
	s.audit(c, bot, "SERVER %s", what)
	s.sno('l', "%s changed the link blocks: SERVER %s", c.user.Nick, what)
	switch sub {
	case "ADD":
		say("Added the link block for %s. Saved to beeircd.conf.", b.Name)
	case "SET":
		say("Changed %s for %s. Saved to beeircd.conf.", strings.ToUpper(args[1]), b.Name)
	default:
		say("Removed the link block for %s. Saved to beeircd.conf.", b.Name)
	}
}

func linkBlockKV(b config.LinkServer) []config.KV {
	kv := []config.KV{{Key: "name", Value: config.Quote(b.Name)}}
	if b.Host != "" {
		kv = append(kv, config.KV{Key: "host", Value: config.Quote(b.Host)}, config.KV{Key: "port", Value: strconv.Itoa(b.Port)})
	}
	kv = append(kv, config.KV{Key: "fingerprint", Value: config.Quote(b.Fingerprint)})
	if b.Password != "" {
		kv = append(kv, config.KV{Key: "password", Value: config.Quote(b.Password)})
	}
	if b.Autoconnect {
		kv = append(kv, config.KV{Key: "autoconnect", Value: "true"})
	}
	if b.SendQMB != 0 && b.SendQMB != 20 {
		kv = append(kv, config.KV{Key: "sendq_mb", Value: strconv.Itoa(b.SendQMB)})
	}
	// SET rewrites the whole block: keep the keys it has no option for.
	if b.Protocol != "" {
		kv = append(kv, config.KV{Key: "protocol", Value: config.Quote(b.Protocol)})
	}
	if b.Rule != "" {
		kv = append(kv, config.KV{Key: "rule", Value: config.Quote(b.Rule)})
		if b.RuleForAll() {
			kv = append(kv, config.KV{Key: "rule_for", Value: config.Quote(config.RuleForAll)})
		}
	}
	return kv
}

// ---- AdminServ on another server ----

// remoteAdminOK are the AdminServ commands /as server <name> may run
// elsewhere: live settings, RESTART and DIE (ports and link blocks need
// the shell there). A BeeIRCd from before RESTART and DIE were here
// answers that they can't be run from another server.
var remoteAdminOK = map[string]bool{"SHOW": true, "SET": true, "MOTD": true, "OPER": true, "CLASS": true,
	"HISTORY": true, "DIFF": true, "UNDO": true, "DNSBL": true, "STATUS": true, "RESTART": true, "DIE": true}

// asServerOn is "/as server <name> <command ...>": run it on server name.
func (s *Server) asServerOn(c *Client, name string, args []string) {
	cmd := strings.ToUpper(argAt(args, 0))
	if cmd == "" {
		s.as(c, "Syntax: SERVER <name> STATUS | SERVER <name> <AdminServ command>")
		return
	}
	if !remoteAdminOK[cmd] {
		s.as(c, "%s can't be run on another server: ports and link blocks are changed there, in its shell.", cmd)
		return
	}
	if strings.EqualFold(name, s.cfg.Server.Name) {
		if cmd == "STATUS" {
			for _, line := range s.adminStatus() {
				s.as(c, "%s", line)
			}
			return
		}
		ac, ok := adminCommands()[cmd]
		if !ok {
			return
		}
		if !s.ours(c.uid) {
			// Someone on another server naming this one: the same rules
			// as their plain /as commands here.
			if why := s.outsideRefusal(c); why != "" {
				s.as(c, "%s", why)
				return
			}
			s.outsideNote(c, strings.Join(args, " "))
		}
		ac.run(s, c, args[1:])
		return
	}
	r := s.serverByName(name)
	switch {
	case r == nil || r.via == nil:
		s.as(c, "%s isn't linked.", name)
		return
	case !r.via.peer.Bee:
		s.as(c, "%s isn't a BeeIRCd server, so it has no AdminServ.", r.name)
		return
	}
	if cmd != "STATUS" && !c.can('a') {
		s.as(c, "You need oper flag a (admin) for that.")
		return
	}
	// Our line goes first, so it comes before the other server's answer.
	s.as(c, "Sent to %s; its answer follows from there.", r.name)
	s.audit(c, s.adminServ, "SERVER %s %s", r.name, strings.Join(args, " "))
	r.via.send(fmt.Sprintf(":%s BEEADMIN %s :%s", c.uid, r.sid, strings.Join(args, " ")))
}

// stopTheirServer is RESTART or DIE (cmd) typed by c, an admin on another
// server: it restarts or stops the server they are on, never this one.
// It goes there as BEEADMIN, the way /as server <theirs> restart does;
// that server checks the confirm code and their flags itself.
func (s *Server) stopTheirServer(c *Client, cmd string, args []string) {
	if !s.needFlag(c, s.adminServ, 'a') {
		return
	}
	verb := "restart"
	if cmd == "DIE" {
		verb = "stop"
	}
	r := s.links.servers[uidServer(c.uid)]
	switch {
	case r == nil || r.via == nil:
		s.as(c, "Your server isn't linked here, so AdminServ can't %s it: %s it from its own shell.", verb, verb)
		return
	case !r.via.peer.Bee || r.software != "" && !strings.HasPrefix(r.software, version.Name):
		s.as(c, "%s isn't a BeeIRCd server, so AdminServ can't %s it: %s it from its own shell.", r.name, verb, verb)
		return
	}
	if len(args) == 0 { // before the send, so it comes before the answer
		s.as(c, "Sent to %s, the server you are on; its answer follows from there. If it says it can't, it runs an older BeeIRCd: %s it from its own shell.", r.name, verb)
	}
	r.via.send(fmt.Sprintf(":%s BEEADMIN %s :%s", c.uid, r.sid, strings.TrimSpace(cmd+" "+strings.Join(args, " "))))
}

// setOnTheirServer is SET typed by c, an admin on another server. A local
// setting (see settingScopes) belongs to the server they are on, so it
// goes there as BEEADMIN, as RESTART does, and that server checks their
// flags itself. A network setting is changed here, on the hub, by those
// outsideRefusal lets in; others are told it is a network setting. It
// returns false when the hub should run the SET as before.
func (s *Server) setOnTheirServer(c *Client, args []string) bool {
	if len(args) < 2 {
		s.as(c, "Syntax: SET <setting> <value>. See them all: /as show")
		return true
	}
	st, ok := findSetting(args[0])
	if !ok {
		s.as(c, "There is no setting %s. See them all: /as show", args[0])
		return true
	}
	if st.network() {
		if why := s.outsideRefusal(c); why != "" {
			s.as(c, "%s %s", networkSettingRefusal(st, s.cfg.Server.Name), why)
			return true
		}
		return false
	}
	r := s.links.servers[uidServer(c.uid)]
	switch {
	case r == nil || r.via == nil:
		s.as(c, "Your server isn't linked here, so AdminServ can't change its %s: change it in its beeircd.conf.", st.name)
		return true
	case !r.via.peer.Bee || r.software != "" && !strings.HasPrefix(r.software, version.Name):
		s.as(c, "%s isn't a BeeIRCd server, so AdminServ can't change its %s: change it in its own config.", r.name, st.name)
		return true
	}
	r.via.send(fmt.Sprintf(":%s BEEADMIN %s :%s", c.uid, r.sid, "SET "+strings.Join(args, " ")))
	return true
}

// hubName names the hub, where the services and the network settings
// are: this server when it runs the services, else the server the
// services pseudo-server hangs off.
func (s *Server) hubName() string {
	if !s.svc.Off() {
		return s.cfg.Server.Name
	}
	t := s.saslTarget()
	if t == "" {
		return "the services server"
	}
	if r := s.serverByName(t); r != nil {
		if up := s.links.servers[r.uplink]; up != nil {
			return up.name
		}
	}
	return t
}

// adminStatus is SERVER <name> STATUS here.
func (s *Server) adminStatus() []string {
	total, _, _ := s.net.PeopleCounts()
	lines := []string{
		fmt.Sprintf("%s: BeeIRCd %s, up %s, %d here, %d on the network", s.cfg.Server.Name, version.String(), durShort(time.Since(s.created)), s.localUsers(), total),
	}
	lines = append(lines, fmt.Sprintf("Role %s, outside admins %s, services %s", s.cfg.Link.Role, s.cfg.Link.OutsideAdmins, map[bool]string{true: "on", false: "off"}[s.cfg.Services.On()]))
	return append(lines, s.uplinkLines()...)
}

// remoteAdmin is BEEADMIN <sid> :<command> from an admin elsewhere.
func (s *Server) remoteAdmin(l *linkConn, m irc.Message) {
	if len(m.Params) < 2 || !l.peer.Bee {
		return
	}
	target, line := m.Params[0], m.Params[1]
	if target != s.cfg.Server.SID {
		if r := s.links.servers[target]; r != nil && r.via != l && r.via != nil && r.via.peer.Bee {
			r.via.send(m.String())
		}
		return
	}
	u := s.net.UserByUID(m.Source)
	if u == nil || !s.behind(l, uidServer(u.UID)) && !s.ours(u.UID) {
		return
	}
	local := s.ours(u.UID) // our own admin, sent round through the hub
	h := host{s}
	say := func(text string) { h.ServerNotice(u.UID, "["+s.cfg.Server.Name+" AdminServ] "+text) }
	args := strings.Fields(line)
	cmd := strings.ToUpper(argAt(args, 0))
	flags := h.OperFlags(u.UID)
	switch {
	case !remoteAdminOK[cmd]:
		say(cmd + " can't be run from another server.")
		return
	case strings.IndexByte(flags, 'a') < 0 && cmd != "STATUS":
		say("You need oper flag a (admin) for that.")
		return
	case strings.IndexByte(flags, 'a') < 0:
		say("STATUS is for admins (oper flag a).")
		return
	}
	if cmd == "STATUS" {
		for _, text := range s.adminStatus() {
			say(text)
		}
		return
	}
	if local {
		if c := s.clients[u.UID]; c != nil && c.user != nil {
			c.adminFrom = s.cfg.Server.Name
			s.log.Printf("AdminServ (via the hub) from %s: %s", u.Nick, line)
			adminCommands()[cmd].run(s, c, args[1:])
			c.adminFrom = ""
		}
		return
	}
	c := s.standIn(u.UID)
	if c == nil {
		return
	}
	if why := s.outsideRefusal(c); why != "" {
		c.capture = nil
		say(why)
		s.sno('a', "Refused outside AdminServ from %s (%s on %s): %s", u.Nick, orStr(u.Account, "no account"), u.Server, line)
		return
	}
	c.adminFrom = u.Server
	s.outsideNote(c, line)
	adminCommands()[cmd].run(s, c, args[1:])
	c.adminFrom = ""
	s.flushStandIn(c)
}
