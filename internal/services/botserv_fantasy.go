package services

import (
	"sort"
	"strings"

	"github.com/fhazal/beeircd/internal/irc"
	"github.com/fhazal/beeircd/internal/state"
	"github.com/fhazal/beeircd/internal/store"
)

// Fantasy commands (Anope 2.0.20 fantasy.cpp and botserv.example.conf's
// fantasy blocks): "!op Ann" or "Bzz op Ann" in a channel with a bot and
// FANTASY on runs ChanServ's OP for that channel. The bot answers.

// fantasyCmd is one fantasy block: the ChanServ command it runs and
// whether the channel goes in front of the arguments.
type fantasyCmd struct {
	cs      string
	prepend bool
}

// fantasyCmds are Anope's default fantasy blocks.
var fantasyCmds = map[string]fantasyCmd{
	"ACCESS": {"ACCESS", true}, "AKICK": {"AKICK", true}, "AOP": {"AOP", true},
	"BAN": {"BAN", true}, "CLONE": {"CLONE", true}, "DEHALFOP": {"DEHALFOP", true},
	"DEOP": {"DEOP", true}, "DEOWNER": {"DEOWNER", true}, "DEPROTECT": {"DEPROTECT", true},
	"DEVOICE": {"DEVOICE", true}, "DOWN": {"DOWN", true}, "ENFORCE": {"ENFORCE", true},
	"ENTRYMSG": {"ENTRYMSG", true}, "FLAGS": {"FLAGS", true}, "HALFOP": {"HALFOP", true},
	"HOP": {"HOP", true}, "INFO": {"INFO", false}, "INVITE": {"INVITE", true},
	"K": {"KICK", true}, "KB": {"BAN", true}, "KICK": {"KICK", true},
	"LEVELS": {"LEVELS", true}, "LIST": {"LIST", false}, "LOG": {"LOG", true},
	"MODE": {"MODE", true}, "MUTE": {"BAN", true}, "OP": {"OP", true}, "OWNER": {"OWNER", true},
	"PROTECT": {"PROTECT", true}, "QOP": {"QOP", true}, "SEEN": {"SEEN", false},
	"SOP": {"SOP", true}, "STATUS": {"STATUS", true}, "SUSPEND": {"SUSPEND", true},
	"SYNC": {"SYNC", true}, "TOPIC": {"TOPIC", true}, "UNBAN": {"UNBAN", true},
	"UNSUSPEND": {"UNSUSPEND", true}, "UP": {"UP", true}, "VOICE": {"VOICE", true},
	"VOP": {"VOP", true},
}

// fantasyOpen are the commands people who aren't identified may use
// (Anope's AllowUnregistered).
var fantasyOpen = map[string]bool{"INFO": true, "LIST": true, "SEEN": true, "HELP": true}

// fantasyChar is botserv's fantasycharacter.
const fantasyChar = "!"

// bsFantasy runs a fantasy command in text, if there is one.
func (s *Services) bsFantasy(c *Client, ch *state.Channel, reg *store.Channel, bot *service, text string) {
	if text == "" || text[0] == '\x01' {
		return
	}
	fields := strings.Fields(text)
	if len(fields) == 0 {
		return
	}
	p0 := normalizeBuffer(fields[0])
	switch {
	case irc.EqualFold(strings.TrimRight(p0, ":,"), bot.user.Nick):
		// "Bzz op Ann", "Bzz: op Ann". Anope takes any word starting with
		// the nick, so "Beers op me" ran OP for a bot named Bee.
		fields = fields[1:]
	case strings.HasPrefix(p0, fantasyChar):
		fields[0] = fields[0][strings.Index(fields[0], fantasyChar)+1:]
	default:
		return
	}
	if len(fields) == 0 || fields[0] == "" {
		return
	}
	name := strings.ToUpper(normalizeBuffer(fields[0]))
	fc, known := fantasyCmds[name]
	if name != "HELP" {
		if _, ok := s.chanServ.command(fc.cs); !known || !ok {
			return
		}
	}
	if c.accountID == 0 && !fantasyOpen[name] {
		return
	}
	if !s.userHas(reg, c.user, "FANTASIA") && !bsAdmin(c) {
		return
	}
	s.fantasyBot, s.fantasyMute = bot, name == "MUTE" // BAN as a quiet ban (~m:), nobody kicked
	defer func() { s.fantasyBot, s.fantasyMute = nil, false }()
	if name == "HELP" {
		s.fantasyHelp(c)
		return
	}
	args := fields[1:]
	if fc.prepend {
		args = append([]string{ch.Name}, args...)
	}
	s.toService(c, s.chanServ, strings.TrimSpace(fc.cs+" "+strings.Join(args, " ")))
}

// fantasyHelp lists the fantasy commands this server has.
func (s *Services) fantasyHelp(c *Client) {
	var names []string
	for n, fc := range fantasyCmds {
		if _, ok := s.chanServ.command(fc.cs); ok {
			names = append(names, n)
		}
	}
	sort.Strings(names)
	s.svcReply(c, s.chanServ, "Fantasy commands (type them in the channel with "+fantasyChar+" in front, like "+fantasyChar+"op):")
	for i := 0; i < len(names); i += 10 {
		s.svcReply(c, s.chanServ, "  "+strings.Join(names[i:min(i+10, len(names))], " "))
	}
	s.svcReply(c, s.chanServ, "They work like the ChanServ commands of the same name, for this channel.")
}

// whoSends is the service that acts in ch for ChanServ: the channel's bot
// when it is in there (Anope's ChannelInfo::WhoSends), else ChanServ.
func (s *Services) whoSends(ch *state.Channel) *service {
	reg, err := s.store.ChannelByName(ch.Name)
	if err != nil || reg == nil {
		return s.chanServ
	}
	if bot, _ := s.chanBot(reg); bot != nil && ch.Member(bot.user) != nil {
		return bot
	}
	return s.chanServ
}
