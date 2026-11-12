package server

import (
	"runtime"
	"runtime/debug"
	"sort"
	"strings"

	"github.com/fhazal/beeircd/internal/irc"
	"github.com/fhazal/beeircd/internal/version"
)

// cmdInfo answers INFO with what this server is (RPL_INFO, RPL_ENDOFINFO).
// A server name, when given, can only be this one.
func (s *Server) cmdInfo(c *Client, m irc.Message) {
	if t := m.Param(0); t != "" && !irc.Match(t, s.cfg.Server.Name) && s.net.UserByNick(t) == nil {
		s.reply(c, irc.ErrNoSuchServer, t, "No such server")
		return
	}
	for _, l := range s.infoLines(c) {
		s.reply(c, irc.RplInfo, l)
	}
	s.reply(c, irc.RplEndOfInfo, "End of /INFO list")
}

func (s *Server) infoLines(c *Client) []string {
	lines := []string{
		version.String(),
		"BeeIRCd - The Hive of IRC",
		"Where communities gather, conversations thrive, and every connection strengthens the hive.",
		"",
		"Made by BajuBesi Labs. Licence: GNU AGPL v3.",
		"Source: https://github.com/fhazal/BeeIRCd",
		"",
		"Built with " + runtime.Version() + buildTime(),
		"Running since " + s.created.UTC().Format("Mon Jan 2 2006 at 15:04:05 UTC"),
	}
	if c.isOper() && s.cfg.Path != "" {
		lines = append(lines, "Config file: "+s.cfg.Path)
	}
	return lines
}

// buildTime is ", from a commit made <time>" when the binary knows it.
func buildTime() string {
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return ""
	}
	for _, st := range bi.Settings {
		if st.Key == "vcs.time" && st.Value != "" {
			return ", from a commit made " + st.Value
		}
	}
	return ""
}

// helpTopics holds HELP/HELPOP text: the syntax first, then what it does.
var helpTopics = map[string][]string{
	"AWAY":         {"AWAY [message]", "Marks you away with message; without one you are back."},
	"AUTHENTICATE": {"AUTHENTICATE <mechanism|data>", "SASL login (PLAIN or EXTERNAL); your client does this for you."},
	"CAP":          {"CAP LS|REQ|END [...]", "Asks for IRCv3 capabilities; your client does this for you."},
	"CHATHISTORY":  {"CHATHISTORY LATEST|BEFORE|AFTER|AROUND|BETWEEN|TARGETS ...", "Fetches the history of a +H channel you are in (IRCv3 chathistory); your client does this for you. At most 100 lines per request."},
	"CHMODES":      chmodesHelp,
	"UMODES":       umodesHelp,
	"KNOCK":        {"KNOCK <#channel> [reason]", "Asks the ops of a +i, +k or full +l channel to let you in. Not on +K channels."},
	"SILENCE":      {"SILENCE [+|-]<nick|mask>", "Ignores private messages, notices and invites from someone, on the server.", "SILENCE alone lists your entries (at most 32)."},
	"SETNAME":      {"SETNAME <new real name>", "Changes your real name (at most 50 characters)."},
	"CHGNAME":      {"CHGNAME <nick> <real name>", "Changes someone's real name (opers, flag h)."},
	"SETHOST":      {"SETHOST <host>", "Changes your displayed host (opers, flag h)."},
	"SETIDENT":     {"SETIDENT <ident>", "Changes your displayed username (opers, flag h)."},
	"CHGHOST":      {"CHGHOST <nick> <host>", "Changes someone's displayed host (opers, flag h)."},
	"CHGIDENT":     {"CHGIDENT <nick> <ident>", "Changes someone's displayed username (opers, flag h)."},
	"REHASH":       {"REHASH", "Reads beeircd.conf again and applies what is safe while running; says what waits for a restart (opers, flag a)."},
	"SAJOIN":       {"SAJOIN <nick> <#channel[,#channel]>", "Makes someone join channels (opers, flag a)."},
	"SAPART":       {"SAPART <nick> <#channel[,#channel]> [reason]", "Makes someone leave channels (opers, flag a)."},
	"SANICK":       {"SANICK <nick> <newnick>", "Changes someone's nick (opers, flag a)."},
	"SAMODE":       {"SAMODE <#channel|nick> <modes> [params]", "Changes modes without needing channel status (opers, flag a)."},
	"SATOPIC":      {"SATOPIC <#channel> <topic>", "Sets a topic without needing channel status (opers, flag a)."},
	"INFO":         {"INFO", "Shows what this server is, its version and licence."},
	"INVITE":       {"INVITE <nick> <#channel>", "Invites someone to a channel."},
	"ISON":         {"ISON <nick> [nick ...]", "Shows which of these nicks are online."},
	"JOIN":         {"JOIN <#channel[,#channel]> [key[,key]]", "Joins channels. JOIN 0 leaves them all."},
	"KICK":         {"KICK <#channel> <nick[,nick]> [reason]", "Removes people from a channel (halfop and up).", "KICK #a,#b nick1,nick2 kicks nick1 from #a and nick2 from #b."},
	"KILL":         {"KILL <nick> <reason>", "Disconnects someone (opers)."},
	"KLINE":        {"KLINE [+time] <nick|user@host> [reason]", "Bans someone from this server only (opers, flag k). +30m, +12h, +7d; none = until removed."},
	"UNKLINE":      {"UNKLINE <user@host>", "Lifts a K-line (opers, flag k)."},
	"LINKS":        {"LINKS [mask]", "Lists the servers on the network."},
	"MAP":          {"MAP", "Shows the server tree and how many people are on each server."},
	"CONNECT":      {"CONNECT <server>", "Connects to a server in the link blocks now (opers, flag c)."},
	"SQUIT":        {"SQUIT <server> [reason]", "Ends a server link (opers, flag c)."},
	"STATS":        {"STATS <l|c|k|u> [server]", "l links, c link blocks, k K-lines (opers); u uptime."},
	"ADMIN":        {"ADMIN [server]", "Shows who runs a server."},
	"LIST":         {"LIST [#channel|mask]", "Lists channels and their topics."},
	"LUSERS":       {"LUSERS", "Shows how many people and channels there are."},
	"MODE":         {"MODE <#channel|nick> [modes [params]]", "Shows or changes modes. See HELP CHMODES for channel modes."},
	"MONITOR":      {"MONITOR +|-|C|L|S [nick,...]", "Tells you when the nicks you watch come online or leave."},
	"MOTD":         {"MOTD [server]", "Shows the message of the day (of another server too)."},
	"NAMES":        {"NAMES <#channel>", "Shows who is on a channel."},
	"NICK":         {"NICK <newnick>", "Changes your nick."},
	"NOTICE":       {"NOTICE <target[,target]> <text>", "Sends a notice: like PRIVMSG, but never answered automatically."},
	"OPER":         {"OPER <nick> <oper password>", "Makes you an IRC operator. The oper password is separate from your NickServ one."},
	"PART":         {"PART <#channel[,#channel]> [reason]", "Leaves channels."},
	"PASS":         {"PASS <password>", "Sends a server password while connecting."},
	"PING":         {"PING <token>", "Asks the server to answer with PONG."},
	"PONG":         {"PONG <token>", "Answers the server's PING."},
	"PRIVMSG":      {"PRIVMSG <target[,target]> <text>", "Sends a message to a channel or a person.", "Targets like @#channel reach only the ops of #channel."},
	"QUIT":         {"QUIT [reason]", "Disconnects you."},
	"TAGMSG":       {"TAGMSG <target>", "Sends only message tags (typing and reactions); your client does this."},
	"TIME":         {"TIME", "Shows the server's time."},
	"TOPIC":        {"TOPIC <#channel> [topic]", "Shows or sets a channel's topic."},
	"USER":         {"USER <username> 0 * <realname>", "Sends your username and real name while connecting."},
	"USERHOST":     {"USERHOST <nick> [nick ...]", "Shows the user@host of these nicks."},
	"VERSION":      {"VERSION", "Shows the server's version and what it supports."},
	"WALLOPS":      {"WALLOPS <text>", "Sends a message to everyone with user mode +w (opers)."},
	"WHO":          {"WHO <#channel|mask> [o]", "Lists people on a channel or matching a mask; o shows only opers."},
	"WHOIS":        {"WHOIS <nick>", "Shows who someone is."},
	"WHOWAS":       {"WHOWAS <nick> [count]", "Shows who used a nick that is gone."},
}

// cmdHelp answers HELP and HELPOP with the modern help numerics:
// 704 first line, 705 the rest, 706 the end, 524 for an unknown topic.
func (s *Server) cmdHelp(c *Client, m irc.Message) {
	subject := strings.ToUpper(strings.TrimPrefix(m.Param(0), "/"))
	var lines []string
	if subject == "" {
		subject = "*"
		lines = helpIndex()
	} else if lines = helpTopics[subject]; lines == nil {
		s.reply(c, irc.ErrHelpNotFound, subject, "No help available on this topic")
		return
	}
	if subject == "CHMODES" && !s.cfg.FullPrefixes() {
		lines = shortPrefixHelp(lines)
	}
	s.reply(c, irc.RplHelpStart, subject, lines[0])
	for _, l := range lines[1:] {
		s.reply(c, irc.RplHelpTxt, subject, l)
	}
	s.reply(c, irc.RplEndOfHelp, subject, "End of /HELP")
}

// umodesHelp is HELP UMODES.
var umodesHelp = []string{
	"***** User modes *****",
	"Anyone:",
	" i invisible         w wallops            x cloaked host",
	" B bot (WHOIS shows it)                   d deaf: no channel messages",
	" D no private messages (opers still can)  R private messages only from",
	"                                            identified users",
	" T no CTCPs          p hide my channels in WHOIS",
	" Z private messages only with TLS users",
	"Opers:",
	" H hide that I'm an oper                  W see who WHOISes me",
	" q can't be kicked (flag a)               s server notices",
	"Set by the server:",
	" o oper    r identified    z connected with TLS",
}

// helpIndex is the HELP page without a topic.
func helpIndex() []string {
	var names []string
	for n := range helpTopics {
		if n != "CHMODES" && n != "UMODES" {
			names = append(names, n)
		}
	}
	sort.Strings(names)
	lines := []string{"***** BeeIRCd help *****", "HELP <command> explains a command. Commands:"}
	for len(names) > 0 {
		n := min(8, len(names))
		lines = append(lines, " "+strings.Join(names[:n], " "))
		names = names[n:]
	}
	return append(lines,
		"Other topics: CHMODES (channel modes), UMODES (user modes).",
		"Services: /ns help, /cs help, /ms help, /hs help, /os help, /as help")
}

// shortPrefixHelp drops owner/admin/halfop from the CHMODES status lines
// when the server uses only @ and +.
func shortPrefixHelp(in []string) []string {
	out := make([]string, 0, len(in))
	for _, l := range in {
		switch {
		case strings.HasPrefix(l, "~q "):
			continue
		case strings.HasPrefix(l, "%h "):
			l = "@o op (@)          +v voice (@)"
		}
		out = append(out, l)
	}
	return out
}
