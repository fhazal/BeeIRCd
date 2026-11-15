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
	"MARKREAD":     {"MARKREAD <target> [timestamp=...]", "Tells the server how far you have read in a channel or private chat, so your other devices know (IRCv3 read-marker, when the network offers the drafts); your client does this for you."},
	"ISUPPORT":     {"ISUPPORT", "Sends the server's feature list (005) again, also before you have signed in (IRCv3 extended-isupport); your client does this for you."},
	"BATCH":        {"BATCH +<id> draft/multiline <target> ... BATCH -<id>", "Sends several lines as one message (IRCv3 multiline): up to 24 lines or 4096 bytes. Your client does this when you paste a block of text."},
	"REDACT":       {"REDACT <target> <msgid> [reason]", "Takes back a message: your own for 15 minutes; channel ops and IRC operators any message in their channel (IRCv3 message-redaction)."},
	"REGISTER":     {"REGISTER <account|*> <email|*> <password>", "Registers your current nick from your client's sign-up screen (IRCv3 account-registration), with the same rules as /msg NickServ REGISTER."},
	"VERIFY":       {"VERIFY <account> <code>", "Confirms a new account with the code from the registration mail (IRCv3 account-registration), like /msg NickServ CONFIRM."},
	"CHATHISTORY":  {"CHATHISTORY LATEST|BEFORE|AFTER|AROUND|BETWEEN|TARGETS ...", "Fetches the history of a +H channel you are in (IRCv3 chathistory); your client does this for you. At most 100 lines per request."},
	"CHMODES":      chmodesHelp,
	"EXTBANS":      extbansHelp,
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
	"STATS":        {"STATS <l|c|k|f|u> [server]", "l links, c link blocks, k K-lines (opers); f spam filter rules (opers, flag k); u uptime."},
	"GLINE":        {"GLINE [-]<user@host> [duration reason]", "Bans someone from the whole network (= /os akill; opers, flag k).", "Duration like 1d or 2h30m, 0 = until removed; none = OperServ's default. -mask removes it, GLINE alone lists them."},
	"ZLINE":        {"ZLINE [-]<ip> [duration reason]", "Bans an IP or range network-wide, before the connection says anything (= /os zline; opers, flag k).", "Same durations as GLINE. -ip removes it, ZLINE alone lists them."},
	"SHUN":         {"SHUN [-]<user@host> [duration reason]", "Lets someone stay connected but ignores almost everything they send (= /os shun; opers, flag k).", "Same durations as GLINE. -mask removes it, SHUN alone lists them."},
	"QLINE":        {"QLINE [-]<nick mask> [duration reason]", "Forbids nicks or channel names matching a mask (= /os sqline; opers, flag s).", "Same durations as GLINE. -mask removes it, QLINE alone lists them."},
	"RLINE":        {"RLINE [-]<realname mask> [duration reason]", "Bans people whose real name matches (= /os snline; opers, flag k).", "Same durations as GLINE. -mask removes it, RLINE alone lists them."},
	"ELINE":        {"ELINE [-]<user@host> [duration reason]", "Exempts a mask from network bans (= /os exception ban; opers, flag k). Session limits: /os exception.", "Same durations as GLINE. -mask removes it, ELINE alone lists them."},
	"SPAMFILTER": {"SPAMFILTER [add|del|test ...]", "Rules that catch spam as it is sent, on every linked server (opers, flag k). Alone it lists the rules (= STATS F).",
		"add [-simple|-regex] [-force] <targets> <action> <ban-time|-> <reason> <match ...>",
		" targets: c channel, p private, n private notice, N channel notice, P part, q quit, t topic, a away, u nick!user@host:realname",
		" actions: warn block kill tempshun shun gline gzline (kline = gline, zline = gzline). Use _ for spaces in the reason.",
		" A rule that would also catch normal chat is refused unless -force.",
		"del <id> removes a rule. test <targets> <text> shows which rules would catch text."},
	"ADMIN":    {"ADMIN [server]", "Shows who runs a server."},
	"LIST":     {"LIST [#channel|mask]", "Lists channels and their topics."},
	"LUSERS":   {"LUSERS", "Shows how many people and channels there are."},
	"MODE":     {"MODE <#channel|nick> [modes [params]]", "Shows or changes modes. See HELP CHMODES for channel modes."},
	"MONITOR":  {"MONITOR +|-|C|L|S [nick,...]", "Tells you when the nicks you watch come online or leave."},
	"MOTD":     {"MOTD [server]", "Shows the message of the day (of another server too)."},
	"NAMES":    {"NAMES <#channel>", "Shows who is on a channel."},
	"NICK":     {"NICK <newnick>", "Changes your nick."},
	"NOTICE":   {"NOTICE <target[,target]> <text>", "Sends a notice: like PRIVMSG, but never answered automatically."},
	"OPER":     {"OPER <nick> <oper password>", "Makes you an IRC operator. The oper password is separate from your NickServ one."},
	"PART":     {"PART <#channel[,#channel]> [reason]", "Leaves channels."},
	"PASS":     {"PASS <password>", "Sends a server password while connecting."},
	"PING":     {"PING <token>", "Asks the server to answer with PONG."},
	"PONG":     {"PONG <token>", "Answers the server's PING."},
	"PRIVMSG":  {"PRIVMSG <target[,target]> <text>", "Sends a message to a channel or a person.", "Targets like @#channel reach only the ops of #channel."},
	"QUIT":     {"QUIT [reason]", "Disconnects you."},
	"TAGMSG":   {"TAGMSG <target>", "Sends only message tags (typing and reactions); your client does this."},
	"TIME":     {"TIME", "Shows the server's time."},
	"TOPIC":    {"TOPIC <#channel> [topic]", "Shows or sets a channel's topic."},
	"USER":     {"USER <username> 0 * <realname>", "Sends your username and real name while connecting."},
	"USERHOST": {"USERHOST <nick> [nick ...]", "Shows the user@host of these nicks."},
	"VERSION":  {"VERSION", "Shows the server's version and what it supports."},
	"WALLOPS":  {"WALLOPS <text>", "Sends a message to everyone with user mode +w (opers)."},
	"WHO":      {"WHO <#channel|mask> [o][%fields[,token]]", "Lists people on a channel or matching a mask; o shows only opers. With %fields (WHOX: t c u i h s n f d l a o r) each person is one line with just those fields, a for the account."},
	"WHOIS":    {"WHOIS <nick>", "Shows who someone is."},
	"WHOWAS":   {"WHOWAS <nick> [count]", "Shows who used a nick that is gone."},
}

// cmdHelp answers HELP and HELPOP with the modern help numerics:
// 704 first line, 705 the rest, 706 the end, 524 for an unknown topic.
func (s *Server) cmdHelp(c *Client, m irc.Message) {
	subject := strings.ToUpper(strings.TrimPrefix(m.Param(0), "/"))
	if subject == "" {
		subject = "*"
	}
	lines := s.helpLines(subject)
	if lines == nil {
		s.reply(c, irc.ErrHelpNotFound, subject, "No help available on this topic")
		return
	}
	s.reply(c, irc.RplHelpStart, subject, lines[0])
	for _, l := range lines[1:] {
		s.reply(c, irc.RplHelpTxt, subject, l)
	}
	s.reply(c, irc.RplEndOfHelp, subject, "End of /HELP")
}

// helpLines is the HELP text of a topic ("*" the index), nil when there
// is none. HelpServ answers from it too.
func (s *Server) helpLines(subject string) []string {
	if subject == "*" {
		return helpIndex()
	}
	lines := helpTopics[subject]
	if subject == "CHMODES" && lines != nil && !s.cfg.FullPrefixes() {
		lines = shortPrefixHelp(lines)
	}
	return lines
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
	"Opers and HelpServ helpers:",
	" h available for help: HelpServ sends you people's questions",
	"Set by the server:",
	" o oper    r identified    z connected with TLS",
}

// helpIndex is the HELP page without a topic.
func helpIndex() []string {
	var names []string
	for n := range helpTopics {
		if n != "CHMODES" && n != "UMODES" && n != "EXTBANS" {
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
		"Other topics: CHMODES (channel modes), EXTBANS (special bans), UMODES (user modes).",
		"Services: /ns help, /cs help, /ms help, /hs help, /bs help, /os help, /as help",
		"Ask a person: /msg HelpServ <your question>")
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
