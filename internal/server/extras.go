package server

import (
	"math/rand/v2"
	"strings"

	"github.com/fhazal/beeircd/internal/config"
	"github.com/fhazal/beeircd/internal/irc"
)

// Small options: the oper and bot MOTDs, RULES, a quote on connect, a
// hidden server list, modes and channels at /oper, switched-off commands
// and who may make channels.

const (
	errNoOperMotd = "425"
	rplRules      = "232"
	rplRulesStart = "308"
	rplRulesEnd   = "309"
	errNoRules    = "434"
)

func init() {
	commands["OPERMOTD"] = command{0, false, (*Server).cmdOperMotd}
	commands["BOTMOTD"] = command{0, false, (*Server).cmdBotMotd}
	commands["RULES"] = command{0, false, (*Server).cmdRules}
	helpTopics["OPERMOTD"] = []string{"OPERMOTD", "Shows the message of the day for opers (opers)."}
	helpTopics["BOTMOTD"] = []string{"BOTMOTD", "Shows the message of the day for bots."}
	helpTopics["RULES"] = []string{"RULES", "Shows the rules of this network."}
}

// motdLike sends lines the way MOTD is sent, under title.
func (s *Server) motdLike(c *Client, title, cmd string, lines []string) {
	s.reply(c, irc.RplMotdStart, "- "+s.cfg.Server.Name+" "+title+" -")
	for _, l := range lines {
		s.reply(c, irc.RplMotd, "- "+l)
	}
	s.reply(c, irc.RplEndOfMotd, "End of /"+cmd+" command.")
}

func (s *Server) cmdOperMotd(c *Client, m irc.Message) {
	if !s.needOper(c) {
		return
	}
	lines := config.TextLines(s.cfg.MOTD.OperFile, s.cfg.MOTD.OperText)
	if len(lines) == 0 {
		s.reply(c, errNoOperMotd, "OPERMOTD File is missing")
		return
	}
	s.motdLike(c, "IRC Operator Message of the Day", "OPERMOTD", lines)
}

func (s *Server) cmdBotMotd(c *Client, m irc.Message) {
	lines := config.TextLines(s.cfg.MOTD.BotFile, s.cfg.MOTD.BotText)
	if len(lines) == 0 {
		s.notice(c, "BOTMOTD File not found")
		return
	}
	s.motdLike(c, "Bot Message of the Day", "BOTMOTD", lines)
}

func (s *Server) cmdRules(c *Client, m irc.Message) {
	lines := config.TextLines(s.cfg.MOTD.RulesFile, s.cfg.MOTD.RulesText)
	if len(lines) == 0 {
		s.reply(c, errNoRules, "RULES File is missing")
		return
	}
	s.reply(c, rplRulesStart, "- "+s.cfg.Server.Name+" Server Rules -")
	for _, l := range lines {
		s.reply(c, rplRules, "- "+l)
	}
	s.reply(c, rplRulesEnd, "End of RULES command.")
}

// randomQuote sends one line of [motd] quotes_file to someone who just
// connected.
func (s *Server) randomQuote(c *Client) {
	if s.cfg.MOTD.QuotesFile == "" {
		return
	}
	var quotes []string
	for _, l := range config.TextLines(s.cfg.MOTD.QuotesFile, "") {
		if l = strings.TrimSpace(l); l != "" {
			quotes = append(quotes, l)
		}
	}
	if len(quotes) > 0 {
		s.notice(c, quotes[rand.IntN(len(quotes))])
	}
}

// hideServers reports whether c only gets to see its own server.
func (s *Server) hideServers(c *Client) bool {
	return s.cfg.Server.HideServers && !c.isOper()
}

// operExtras runs after a fresh /oper: the oper MOTD, [server] oper_modes
// and oper_join.
func (s *Server) operExtras(c *Client) {
	if lines := config.TextLines(s.cfg.MOTD.OperFile, s.cfg.MOTD.OperText); len(lines) > 0 {
		s.motdLike(c, "IRC Operator Message of the Day", "OPERMOTD", lines)
	}
	if m := s.cfg.Server.OperModes; m != "" {
		s.setUserModes(c, c.uid, m)
	}
	for _, name := range s.cfg.Server.OperJoin {
		if s.clients[c.uid] != c || c.closed {
			return
		}
		host{s}.ForceJoin(c.uid, name)
	}
}

// commandOff reports whether [limits] disabled_commands names cmd.
func (s *Server) commandOff(cmd string) bool {
	for _, d := range s.cfg.Limits.DisabledCommands {
		if strings.EqualFold(d, cmd) {
			return true
		}
	}
	return false
}

// createRefused says why c may not make a new channel ([channels]
// create), or "".
func (s *Server) createRefused(c *Client) string {
	if c.isOper() {
		return ""
	}
	switch s.cfg.Channels.CreateMode() {
	case "opers":
		return "Only IRC operators can create new channels on this network."
	case "registered":
		if c.user.Account == "" {
			return "You need to be logged in to NickServ to create a new channel."
		}
	}
	return ""
}
