package services

import (
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/fhazal/beeircd/internal/irc"
	"github.com/fhazal/beeircd/internal/state"
	"github.com/fhazal/beeircd/internal/store"
)

const (
	maxAkicks         = 100
	maxChannelsPerAcc = 20
	maxEntryMsgs      = 5
	mlockFlags        = "CDKMNQRSTVcimnpstz"
)

// chanServAliases are old or other names HELP doesn't show.
var chanServAliases = map[string]string{"WHY": "STATUS"}

func chanServCommands() map[string]svcCmd {
	level := func(l int) svcCmd {
		n := store.LevelName(l)
		extra := ""
		if l == store.LevelQOP || l == store.LevelHOP {
			extra = "\nOnly on servers with ~&@%+ prefixes."
		}
		return svcCmd{n + " #chan ADD|DEL|LIST|CLEAR [nick]", fmt.Sprintf("edit the %s list (level %d)", n, l),
			fmt.Sprintf("%[1]s #chan ADD <nick> [description]   adding someone on another level moves them\n%[1]s #chan DEL {<nick> | <num> | <list>}   numbers from LIST, like 2 or 1-3,5\n%[1]s #chan LIST [<pattern> | <list>]\n%[1]s #chan CLEAR   (founder)\n%[1]s is level %[2]d on the access list. Changing the list needs ACCESS_CHANGE (SOP by\ndefault), and you can only give levels below your own; seeing it needs ACCESS_LIST.%[3]s\nExample: /cs %[4]s #bchat add ember helps with the wiki", n, l, extra, strings.ToLower(n)),
			csLevelList(l)}
	}
	return map[string]svcCmd{
		"REGISTER": {"REGISTER #chan [description]", "register a channel you op in",
			"Registers a channel to your account; you become its Founder. You need to be\nidentified and a channel operator in it.\nExample: /cs register #bchat Be kind, stay curious", csRegister},
		"DROP": {"DROP #chan #chan", "unregister a channel (founder)",
			"Removes the registration with its access list, AKICKs and memos. Type the\nchannel name twice, so it can't happen by accident. Opers with flag s may\ndrop any channel.\nExample: /cs drop #bchat #bchat", csDrop},
		"INFO": {"INFO #chan", "who owns a channel and its settings",
			"Shows the founder, successor and description. With the INFO privilege (QOP\nand the founder by default) also the mode lock and options.", csInfo},
		"QOP": level(store.LevelQOP),
		"SOP": level(store.LevelSOP),
		"HOP": level(store.LevelHOP),
		"AOP": level(store.LevelAOP),
		"VOP": level(store.LevelVOP),
		"UOP": level(store.LevelUOP),
		"ACCESS": {"ACCESS #chan ADD|DEL|LIST|VIEW|CLEAR ...", "the access list with levels",
			"ACCESS #chan ADD <nick> <level> [description]   level 1-10000, or VOP, AOP, SOP...\nACCESS #chan DEL {<nick> | <num> | <list>}\nACCESS #chan LIST [<pattern> | <list>]\nACCESS #chan VIEW [<pattern> | <list>]   with who added them and when\nACCESS #chan CLEAR   (founder)\nLevels: UOP 1, VOP 3, HOP 4, AOP 5, SOP 10, QOP 10000. What each level may do is\nin LEVELS. ADD and DEL need ACCESS_CHANGE, LIST and VIEW need ACCESS_LIST. You can\nonly give levels below your own.\nExample: /cs access #bchat add ember 5", csAccess},
		"LEVELS": {"LEVELS #chan SET|DISABLE|LIST|RESET ...", "what each level may do",
			"LEVELS #chan LIST   every privilege and the level it needs\nLEVELS #chan SET <privilege> {<level> | FOUNDER}   0 = everyone in the channel\nLEVELS #chan DISABLE <privilege>   nobody, not even the founder\nLEVELS #chan RESET   back to the defaults\nSET, DISABLE and RESET are for the founder; LIST needs ACCESS_LIST.\nExample: /cs levels #bchat set autovoice 0   (voice everyone who joins)", csLevels},
		"STATUS": {"STATUS #chan [nick]", "someone's access and why",
			"Shows the level you (or nick) have on a channel and the privileges it gives.\nAsking about others needs ACCESS_LIST. WHY does the same.", csStatus},
		"AKICK": {"AKICK #chan ADD|DEL|LIST|VIEW|ENFORCE|CLEAR ...", "keep someone out for good",
			"AKICK #chan ADD {<nick> | <mask>} [reason]\nAKICK #chan DEL {<mask> | <num> | <list>}   numbers from LIST, like 2 or 1-3,5\nAKICK #chan LIST [<mask> | <list>]\nAKICK #chan VIEW [<mask> | <list>]        with who added it and when\nAKICK #chan ENFORCE   kick everyone in the channel who matches now\nAKICK #chan CLEAR\nAnyone matching an AKICK is banned and kicked when they join, unless they are\non the access list. Needs the AKICK privilege (SOP by default).\nExample: /cs akick #bchat add *!*@*.badhost.example spamming", csAkick},
		"OP":        {"OP #chan [nick]", "op someone (or yourself)", "Needs OP for others, OPME for yourself (AOP by default). With SECUREOPS on,\nonly people allowed @ can be opped.", csModeCmd('o', true)},
		"DEOP":      {"DEOP #chan [nick]", "deop someone (or yourself)", "Needs OP for others; anyone can deop themself.", csModeCmd('o', false)},
		"VOICE":     {"VOICE #chan [nick]", "voice someone (or yourself)", "Needs VOICE for others (HOP by default), VOICEME for yourself (VOP).", csModeCmd('v', true)},
		"DEVOICE":   {"DEVOICE #chan [nick]", "devoice someone (or yourself)", "Needs VOICE for others; anyone can devoice themself.", csModeCmd('v', false)},
		"HALFOP":    {"HALFOP #chan [nick]", "halfop someone (or yourself)", "Needs HALFOP for others (AOP by default), HALFOPME for yourself (HOP).\nOnly with ~&@%+ prefixes.", csModeCmd('h', true)},
		"DEHALFOP":  {"DEHALFOP #chan [nick]", "take halfop away", "Needs HALFOP for others; anyone can dehalfop themself.", csModeCmd('h', false)},
		"PROTECT":   {"PROTECT #chan [nick]", "give admin (&) status", "Needs PROTECT for others (QOP by default), PROTECTME for yourself (SOP).\nOnly with ~&@%+ prefixes.", csModeCmd('a', true)},
		"DEPROTECT": {"DEPROTECT #chan [nick]", "take admin (&) status away", "Needs PROTECT for others; anyone can take it from themself.", csModeCmd('a', false)},
		"OWNER":     {"OWNER #chan [nick]", "give owner (~) status", "Needs OWNER for others (the founder by default), OWNERME for yourself (QOP).\nOnly with ~&@%+ prefixes.", csModeCmd('q', true)},
		"DEOWNER":   {"DEOWNER #chan [nick]", "take owner (~) status away", "Needs OWNER for others; anyone can take it from themself.", csModeCmd('q', false)},
		"UP":        {"UP #chan", "give yourself every status you earn", "Gives you the modes your level earns, like when you join.", csUp},
		"DOWN":      {"DOWN #chan", "drop all your channel status", "Takes every status mode you have in the channel.", csDown},
		"SYNC":      {"SYNC #chan", "give everyone the status they earn", "Gives everyone in the channel the modes their level earns and, with SECUREOPS\non, takes status from those not allowed it. Needs ACCESS_CHANGE.", csSyncCmd},
		"KICK":      {"KICK #chan <nick> [reason]", "kick through ChanServ", "Needs KICK (HOP by default). With PEACE on, nobody can kick someone on the\nsame level or above, except the founder.", csKick},
		"BAN":       {"BAN #chan [+expiry] <nick|mask> [reason]", "ban and kick", "Needs BAN (HOP by default). A nick bans that person's host, the way BANTYPE\nsays. With +expiry (+30m, +2h, +7d) the ban lifts itself (a ~t: ban).\nExtbans work too: ~a:account, ~r:realname, ~m:mask (mute).", csBan},
		"UNBAN":     {"UNBAN [#chan [nick|mask]]", "remove bans", "Needs UNBAN (HOP by default). UNBAN #chan removes every ban on you there;\nwith a nick, the bans on them. UNBAN alone unbans you in every channel where\nyou have UNBAN.", csUnban},
		"INVITE":    {"INVITE #chan [nick]", "invite yourself or someone", "Needs INVITE (AOP by default). Without a nick it invites you.", csInvite},
		"GETKEY":    {"GETKEY #chan", "tell you the channel key", "Needs GETKEY (AOP by default).", csGetKey},
		"TOPIC": {"TOPIC #chan [SET|APPEND|PREPEND|LOCK|UNLOCK] [text]", "set or lock the topic",
			"TOPIC #chan <text>           set the topic (SET may come first)\nTOPIC #chan SET              with no text: clear the topic\nTOPIC #chan APPEND <text>    add to the end\nTOPIC #chan PREPEND <text>   add to the front\nTOPIC #chan LOCK|UNLOCK      only those with TOPIC may change it (the old TOPICLOCK)\nNeeds TOPIC (AOP by default); LOCK and UNLOCK need SET.", csTopic},
		"MODE": {"MODE #chan LOCK|SET|CLEAR ...", "channel modes and the mode lock",
			"MODE #chan LOCK ADD <modes>    like +nt-s or +k secret; joins the lock\nMODE #chan LOCK DEL <modes>    take letters out of the lock\nMODE #chan LOCK SET <modes>    replace the whole lock\nMODE #chan LOCK LIST\nMODE #chan SET <modes>         set modes now (the lock still wins)\nMODE #chan CLEAR [what]        BANS, EXCEPTS, INVITES, OPS, HALFOPS, VOICES,\n                               PROTECTS, OWNERS, MODES or ALL (the default)\nThe lock can hold the on/off modes C D K M N Q R S T V c i m n p s t z,\nk and l, and f F L E B J with their value (see /helpop chmodes).\nNeeds MODE (QOP by default).", csModeCommand},
		"CLEAR": {"CLEAR #chan <what>", "same as MODE CLEAR", "Kept for ConferenceRoom habits: /cs clear #chan bans is /cs mode #chan clear bans.", csClearOld},
		"ENTRYMSG": {"ENTRYMSG #chan ADD|DEL|LIST|CLEAR ...", "notices people get when they join",
			"ENTRYMSG #chan ADD <text>\nENTRYMSG #chan DEL <num>\nENTRYMSG #chan LIST\nENTRYMSG #chan CLEAR\nUp to 5 lines, sent to everyone who joins. Needs SET.", csEntryMsg},
		"ENFORCE": {"ENFORCE #chan <what>", "apply a rule to everyone in the channel now",
			"ENFORCE #chan SECUREOPS    take status from those not allowed it\nENFORCE #chan RESTRICTED   kick and ban everyone not on the access list\nENFORCE #chan REGONLY      kick everyone who isn't identified\nENFORCE #chan BANS         kick everyone a ban matches\nENFORCE #chan LIMIT        kick the newest people over the +l limit\nNeeds AKICK (SOP by default).", csEnforce},
		"LOG": {"LOG #chan [<command> <method> [status]]", "tell the channel when ChanServ is used",
			"LOG #chan                       show the log settings\nLOG #chan <command> <method>    method is MESSAGE, NOTICE or MEMO\nLOG #chan <command> <method> @  only to those with that status or higher\nLOG #chan <command> NONE        stop logging a command\nExample: /cs log #bchat kick notice @   tells ops whenever someone uses KICK.\nNeeds SET.", csLog},
		"SUSPEND": {"SUSPEND #chan [+expiry] [reason]", "close a channel, keeping its data (opers)",
			"Nobody can use a suspended channel: everyone is kicked and kept out, and\nChanServ commands on it are refused. Expiry like +7d; without it the channel\nstays suspended until UNSUSPEND. Opers with flag s.", csSuspend},
		"UNSUSPEND": {"UNSUSPEND #chan", "open a suspended channel again (opers)", "Opers with flag s.", csUnsuspend},
		"SET": {"SET <option> #chan <value>", "change channel settings",
			"SET DESCRIPTION #chan <text>\nSET FOUNDER #chan <nick>        (founder) Opers with flag s too.\nSET SUCCESSOR #chan <nick>|OFF  (founder) gets the channel if the founder's\n                                account goes\nSET SECUREFOUNDER #chan ON|OFF  (founder) only the real founder has founder\n                                rights, not the FOUNDER privilege (on)\nSET KEEPTOPIC #chan ON|OFF      the topic comes back when the channel reopens (on)\nSET KEEPMODES #chan ON|OFF      the modes come back when the channel reopens\nSET PEACE #chan ON|OFF          nobody can kick, ban or deop someone at their\n                                level or above (on)\nSET RESTRICTED #chan ON|OFF     only people on the access list may stay\nSET SECURE #chan ON|OFF         access only for people who identified, not for\n                                ones NickServ ACCESS recognises (on)\nSET SECUREOPS #chan ON|OFF      only those allowed a status may hold it\nSET SIGNKICK #chan ON|OFF       kicks show who asked for them (on)\nSET AUTOOP #chan ON|OFF         ChanServ gives status on join (on)\nSET PERSIST #chan ON|OFF        ChanServ keeps the channel open when empty\nSET OVERFLOW #chan <#other>|OFF people who can't get in (full, +i, +k,\n                                banned) go to #other: mode lock +L\nSET PRIVATE #chan ON|OFF        hide from /cs list and /cs info for outsiders\nSET BANTYPE #chan 0-3           0 *!user@host, 1 *!*user@host, 2 *!*@host (default),\n                                3 *!*user@*.domain\nSET URL #chan <url>|OFF\nSET NOEXPIRE #chan ON|OFF       (opers with flag s) the channel never expires\nEverything else needs the SET privilege. The mode lock is in MODE, entry\nmessages in ENTRYMSG, the topic lock in TOPIC.\nConferenceRoom shortcuts: MEMOLEVEL <level>, AUTOVOP ON|OFF, LEAVEOPS ON|OFF.", csSet},
		"SEEN": {"SEEN <nick>", "when someone was last seen, and doing what",
			"Tells you the last time a nick was seen connecting, joining, leaving,\nchanging nick or quitting, and where (secret channels stay secret).", csSeen},
		"LIST": {"LIST <pattern>", "search registered channels", "Lists registered channels matching a pattern, like #bee*. Private ones are\nleft out unless you are on their access list.", csList},
	}
}

func (s *Services) cs(c *Client, format string, a ...any) {
	s.svcReply(c, s.chanServ, fmt.Sprintf(format, a...))
}

// csLookup loads a registered channel and c's level on it (founder rights
// included), without any checks.
func (s *Services) csLookup(c *Client, name string) (*store.Channel, int, bool) {
	if !irc.IsChannel(name) {
		s.cs(c, "Give a channel, like #bchat.")
		return nil, 0, false
	}
	reg, err := s.store.ChannelByName(name)
	if err != nil {
		s.dbErr(c, s.chanServ, err)
		return nil, 0, false
	}
	if reg == nil {
		s.cs(c, "%s isn't registered.", name)
		return nil, 0, false
	}
	return reg, s.accountLevel(reg, s.chanAccount(c, reg)), true
}

// csChan is csLookup for commands: a suspended channel is refused (except
// to opers with flag s), and with need above zero c must be identified and
// at least that level.
func (s *Services) csChan(c *Client, name string, need int) (*store.Channel, int, bool) {
	reg, lvl, ok := s.csLookup(c, name)
	if !ok {
		return nil, 0, false
	}
	if reg.Suspended(time.Now()) && !c.can('s') {
		s.cs(c, "%s is suspended: %s", reg.Name, reg.SuspendReason)
		return nil, 0, false
	}
	if need > 0 {
		switch {
		case s.chanAccount(c, reg) == 0:
			s.cs(c, "Identify first: /ns identify <password>")
			s.logDeniedUnreg(c, s.chanServ)
			return nil, 0, false
		case lvl < need && need == store.LevelFounder:
			s.cs(c, "Only the founder of %s can do that.", reg.Name)
			return nil, 0, false
		case lvl < need:
			s.cs(c, "You need to be %s on %s for that.", needWords(need), reg.Name)
			return nil, 0, false
		}
	}
	return reg, lvl, true
}

// csChanOper is csChan for commands opers with flag s may use on any
// channel (DROP, SET FOUNDER). asOper reports that the oper right was used.
func (s *Services) csChanOper(c *Client, name string) (reg *store.Channel, asOper, ok bool) {
	if c.can('s') {
		reg, lvl, ok := s.csChan(c, name, 0)
		if !ok {
			return nil, false, false
		}
		return reg, lvl < store.LevelFounder, true
	}
	reg, _, ok = s.csChan(c, name, store.LevelFounder)
	return reg, false, ok
}

func csRegister(s *Services, c *Client, args []string) {
	u := c.user
	name := argAt(args, 0)
	if c.accountID == 0 {
		s.cs(c, "Identify first: /ns identify <password>")
		s.logDeniedUnreg(c, s.chanServ)
		return
	}
	if !irc.IsChannel(name) {
		s.cs(c, "Syntax: REGISTER #chan [description]")
		return
	}
	if why := s.registerForbidden(name); why != "" {
		s.cs(c, "%s", why)
		return
	}
	desc := strings.Join(args[1:], " ")
	if reg, err := s.store.ChannelByName(name); err != nil {
		s.dbErr(c, s.chanServ, err)
		return
	} else if reg != nil {
		s.cs(c, "%s is registered already. Its founder is %s.", reg.Name, reg.Founder)
		return
	}
	live := s.net.Channel(name)
	if live == nil || live.Member(u) == nil || !live.Member(u).AtLeast(state.RankOp) {
		s.cs(c, "You need to be a channel operator in %s to register it.", name)
		return
	}
	a, ok := s.identified(c)
	if !ok {
		return
	}
	all, err := s.store.Channels()
	if err != nil {
		s.dbErr(c, s.chanServ, err)
		return
	}
	owned := 0
	for _, ch := range all {
		if ch.FounderID == a.ID {
			owned++
		}
	}
	if owned >= maxChannelsPerAcc {
		s.cs(c, "You found %d channels already, the most one account can.", owned)
		return
	}
	reg, err := s.store.CreateChannel(live.Name, a, irc.Truncate(desc, 200), time.Now())
	if err != nil {
		s.dbErr(c, s.chanServ, err)
		return
	}
	if live.Topic != "" {
		reg.Topic, reg.TopicBy, reg.TopicAt = live.Topic, live.TopicBy, live.TopicAt
		s.store.SaveChannel(reg)
	}
	s.log.Printf("registered channel %s to %s", reg.Name, a.Name)
	s.cs(c, "%s is registered to %s. You are its Founder.", reg.Name, a.Name)
	s.csLog(c, reg.Name, false, "")
	s.csMode(live, mlockChanges(reg, live))
	s.csAutoModes(reg, live, u)
}

func csDrop(s *Services, c *Client, args []string) {
	reg, asOper, ok := s.csChanOper(c, argAt(args, 0))
	if !ok {
		return
	}
	if again := argAt(args, 1); !irc.EqualFold(again, reg.Name) {
		s.cs(c, "Type the channel name twice to drop it, as a confirmation: /cs drop %s %s", reg.Name, reg.Name)
		return
	}
	if err := s.store.DeleteChannel(reg.ID); err != nil {
		s.dbErr(c, s.chanServ, err)
		return
	}
	s.channelGone(reg.Name)
	s.csLog(c, reg.Name, asOper, "(founder was: %s)", orNone(reg.Founder))
	s.log.Printf("dropped channel %s (by %s)", reg.Name, c.user.Nick)
	if asOper {
		s.audit(c, s.chanServ, "DROP %s (founder %s)", reg.Name, reg.Founder)
	}
	s.cs(c, "%s is no longer registered.", reg.Name)
}

// channelGone tidies a live channel whose registration went: -r, and
// ChanServ leaves if it was keeping it open.
func (s *Services) channelGone(name string) {
	live := s.net.Channel(name)
	if live == nil {
		return
	}
	s.csMode(live, []modeChange{{false, 'r', ""}})
	if live.Member(s.chanServ.user) != nil {
		s.emit(&state.Part{UID: s.chanServ.user.UID, Channel: live.Name})
	}
	for _, bot := range s.bots { // a BotServ bot leaves with the registration
		s.botPart(name, bot, "")
	}
	if s.bsWatch[irc.Fold(name)] {
		s.bsWatchAll()
	}
}

func csInfo(s *Services, c *Client, args []string) {
	reg, lvl, ok := s.csLookup(c, argAt(args, 0))
	if !ok {
		return
	}
	if reg.Private && lvl == store.LevelNone && !c.isOper() {
		s.cs(c, "%s is private.", reg.Name)
		return
	}
	s.cs(c, "%s · registered %s (%s)", reg.Name, showTime(reg.Registered), ago(reg.Registered))
	s.cs(c, "  Founder       %s", reg.Founder)
	if reg.Successor != "" {
		s.cs(c, "  Successor     %s", reg.Successor)
	}
	s.cs(c, "  Description   %s", reg.Description)
	if reg.URL != "" {
		s.cs(c, "  URL           %s", reg.URL)
	}
	if s.channelInUse(reg) {
		s.cs(c, "  Last used     in use now")
	} else {
		s.cs(c, "  Last used     %s (%s)", showTime(reg.LastUsed), ago(reg.LastUsed))
	}
	if reg.Suspended(time.Now()) {
		until := "until UNSUSPEND"
		if reg.SuspendUntil.Unix() != 0 {
			until = "until " + showTime(reg.SuspendUntil)
		}
		s.cs(c, "  Suspended     by %s, %s, %s: %s", reg.SuspendedBy, showTime(reg.SuspendedAt), until, reg.SuspendReason)
	}
	if !s.levelHas(reg, lvl, "INFO") && !c.isOper() {
		return
	}
	s.cs(c, "  Mode lock     %s", mlockString(reg))
	var opts []string
	for _, o := range []struct {
		on   bool
		name string
	}{{reg.KeepTopic, "keep topic"}, {reg.TopicLock, "topic lock"}, {reg.KeepModes, "keep modes"}, {reg.Peace, "peace"},
		{reg.Restricted, "restricted"}, {reg.Secure, "security"}, {reg.SecureOps, "secure ops"}, {reg.SecureFounder, "secure founder"},
		{reg.SignKick, "sign kick"}, {!reg.AutoOp, "no auto-op"}, {reg.Persist, "persist"}, {reg.Private, "private"},
		{reg.NoExpire, "no expire"}} {
		if o.on {
			opts = append(opts, o.name)
		}
	}
	if len(opts) == 0 {
		opts = []string{"none"}
	}
	s.cs(c, "  Options       %s", strings.Join(opts, ", "))
	s.cs(c, "  Ban type      %d (%s)", reg.BanType, banTypes[reg.BanType])
	for i, e := range s.store.EntryMsgs(reg) {
		label := ""
		if i == 0 {
			label = "Entry message"
		}
		s.cs(c, "  %-13s %s", label, e.Text)
	}
	s.showOperInfo(c, s.chanServ, true, reg.ID)
}

// csLevelList handles QOP, SOP, AOP, HOP, VOP and UOP.
func csLevelList(level int) func(*Services, *Client, []string) {
	n := store.LevelName(level)
	return func(s *Services, c *Client, args []string) {
		name, sub, arg := argAt(args, 0), strings.ToUpper(argAt(args, 1)), argAt(args, 2)
		if (level == store.LevelQOP || level == store.LevelHOP) && !s.cfg.FullPrefixes() {
			s.cs(c, "%s lists are only used on servers with ~&@%%+ prefixes; this one uses @+.", n)
			return
		}
		var reg *store.Channel
		var lvl int
		var ok bool
		switch sub {
		case "":
			s.cs(c, "Syntax: %s #chan ADD|DEL|LIST|CLEAR [nick]", n)
			return
		case "LIST":
			reg, lvl, ok = s.csPriv(c, name, "ACCESS_LIST")
		case "CLEAR":
			reg, lvl, ok = s.csChan(c, name, store.LevelFounder)
		default:
			reg, lvl, ok = s.csPriv(c, name, "ACCESS_CHANGE")
		}
		if !ok {
			return
		}
		switch sub {
		case "ADD":
			if arg == "" {
				s.cs(c, "Syntax: %s %s ADD <nick> [description]", n, reg.Name)
				return
			}
			target, ok := s.csAccount(c, reg, arg)
			if !ok {
				return
			}
			old, _ := s.store.Level(reg, target.ID)
			if old == level {
				s.cs(c, "%s is on the %s list of %s already.", target.Name, n, reg.Name)
				return
			}
			if !s.accessChange(c, reg, lvl, old, level, target.Name) {
				return
			}
			note := irc.Truncate(strings.Join(args[3:], " "), 100)
			if _, err := s.store.SetLevel(reg, target, level, c.user.Account, note, time.Now()); err != nil {
				s.dbErr(c, s.chanServ, err)
				return
			}
			s.csLog(c, reg.Name, false, "to add %s", target.Name)
			if old == store.LevelNone {
				s.csUsed(c, reg)
				s.cs(c, "%s added to the %s list of %s.", target.Name, n, reg.Name)
			} else {
				s.csUsed(c, reg)
				s.cs(c, "%s moved from %s to %s on %s.", target.Name, store.LevelName(old), n, reg.Name)
			}
			s.afterAccessChange(reg, target.ID)
		case "DEL":
			s.accessDel(c, reg, lvl, level, arg, n)
		case "LIST":
			entries, err := s.store.AccessList(reg, level)
			if err != nil {
				s.dbErr(c, s.chanServ, err)
				return
			}
			if len(entries) == 0 {
				s.cs(c, "The %s list of %s is empty.", n, reg.Name)
				return
			}
			show := pickEntries(arg, len(entries), func(i int) string { return entries[i].Account })
			if len(show) == 0 {
				s.cs(c, "No entry on the %s list of %s matches %s.", n, reg.Name, arg)
				return
			}
			width := 4
			for _, e := range entries {
				width = max(width, len(e.Account))
			}
			s.cs(c, "%s list of %s:", n, reg.Name)
			for _, i := range show {
				e := entries[i]
				line := fmt.Sprintf("  %-3d %-*s  added by %s, %s", i+1, width, e.Account, e.AddedBy, e.AddedAt.UTC().Format("2 Jan 2006"))
				if e.Note != "" {
					line += " · " + e.Note
				}
				s.cs(c, "%s", line)
			}
			s.cs(c, "%s.", plural(len(show), "entry"))
		case "CLEAR":
			removed, err := s.store.ClearLevel(reg, level)
			if err != nil {
				s.dbErr(c, s.chanServ, err)
				return
			}
			s.csUsed(c, reg)
			s.csLog(c, reg.Name, false, "to clear the access list")
			s.cs(c, "The %s list of %s is now empty (%d removed).", n, reg.Name, removed)
		default:
			s.cs(c, "Syntax: %s #chan ADD|DEL|LIST|CLEAR [nick]", n)
		}
	}
}

// pickEntries returns the indexes (0-based) a LIST shows: all for "", the
// numbers for a number list, else those whose name matches the pattern.
func pickEntries(spec string, n int, name func(int) string) []int {
	var out []int
	if nums, isList := numList(spec, n); isList {
		for _, i := range nums {
			out = append(out, i-1)
		}
		return out
	}
	for i := 0; i < n; i++ {
		if spec == "" || irc.Match(spec, name(i)) {
			out = append(out, i)
		}
	}
	return out
}

func csAkick(s *Services, c *Client, args []string) {
	reg, _, ok := s.csPriv(c, argAt(args, 0), "AKICK")
	if !ok {
		return
	}
	sub, arg := strings.ToUpper(argAt(args, 1)), argAt(args, 2)
	list, err := s.store.Akicks(reg)
	if err != nil {
		s.dbErr(c, s.chanServ, err)
		return
	}
	switch {
	case sub == "LIST" || sub == "VIEW":
		if len(list) == 0 {
			s.cs(c, "The AKICK list of %s is empty.", reg.Name)
			return
		}
		show := pickEntries(arg, len(list), func(i int) string { return list[i].Mask })
		if len(show) == 0 {
			s.cs(c, "No AKICK on %s matches %s.", reg.Name, arg)
			return
		}
		s.cs(c, "AKICK list of %s:", reg.Name)
		for _, i := range show {
			k := list[i]
			if sub == "VIEW" {
				s.cs(c, "  %-3d %s  added by %s, %s · %s", i+1, k.Mask, k.AddedBy, k.AddedAt.UTC().Format("2 Jan 2006 15:04 UTC"), k.Reason)
			} else {
				s.cs(c, "  %-3d %s  %s", i+1, k.Mask, k.Reason)
			}
		}
		s.cs(c, "%s.", plural(len(show), "entry"))
	case (sub == "ADD" || sub == "DEL") && arg == "":
		s.cs(c, "Syntax: AKICK %s %s <mask>", reg.Name, sub)
	case sub == "ADD":
		if len(list) >= maxAkicks {
			s.cs(c, "The AKICK list of %s is full (%d).", reg.Name, maxAkicks)
			return
		}
		mask, valid := irc.NormalizeBan(arg) // a nick becomes nick!*@*
		if !valid {
			s.cs(c, "%s isn't a mask or extban (~a:account, ~r:realname).", arg)
			return
		}
		reason := strings.Join(args[3:], " ")
		if reason == "" {
			reason = "You are not welcome in " + reg.Name + "."
		}
		added, err := s.store.AddAkick(reg, mask, irc.Truncate(reason, 200), c.user.Account, time.Now())
		if err != nil {
			s.dbErr(c, s.chanServ, err)
			return
		}
		if !added {
			s.cs(c, "%s is on the AKICK list of %s already.", mask, reg.Name)
			return
		}
		s.csUsed(c, reg)
		if given := strings.Join(args[3:], " "); given != "" {
			s.csLog(c, reg.Name, false, "to add %s: %s", mask, given)
		} else {
			s.csLog(c, reg.Name, false, "to add %s", mask)
		}
		s.cs(c, "Added %s to the AKICK list of %s.", mask, reg.Name)
		s.akickEnforce(reg, []store.Akick{{Mask: mask, Reason: reason}})
	case sub == "DEL":
		var masks []string
		if nums, isList := numList(arg, len(list)); isList {
			for _, i := range nums {
				masks = append(masks, list[i-1].Mask)
			}
		} else {
			m, _ := irc.NormalizeBan(arg)
			masks = []string{m}
		}
		var removed []string
		for _, m := range masks {
			ok, err := s.store.DelAkick(reg, m)
			if err != nil {
				s.dbErr(c, s.chanServ, err)
				return
			}
			if ok {
				removed = append(removed, m)
			}
		}
		if len(removed) == 0 {
			s.cs(c, "%s isn't on the AKICK list of %s.", arg, reg.Name)
			return
		}
		s.csUsed(c, reg)
		for _, m := range removed {
			s.csLog(c, reg.Name, false, "to delete %s", m)
		}
		s.cs(c, "Removed %s from the AKICK list of %s.", strings.Join(removed, ", "), reg.Name)
	case sub == "ENFORCE":
		n := s.akickEnforce(reg, list)
		s.csUsed(c, reg)
		s.csLog(c, reg.Name, false, "ENFORCE, affects %d users", n)
		s.cs(c, "AKICK enforced on %s: %s.", reg.Name, plural(n, "person")+" kicked")
	case sub == "CLEAR":
		for _, k := range list {
			if _, err := s.store.DelAkick(reg, k.Mask); err != nil {
				s.dbErr(c, s.chanServ, err)
				return
			}
		}
		s.csUsed(c, reg)
		s.csLog(c, reg.Name, false, "to clear the akick list")
		s.cs(c, "The AKICK list of %s is now empty (%d removed).", reg.Name, len(list))
	default:
		s.cs(c, "Syntax: AKICK #chan ADD|DEL|LIST|VIEW|ENFORCE|CLEAR ...")
	}
}

// akickEnforce bans and kicks everyone in reg who matches one of kicks and
// isn't on the access list, and returns how many it kicked.
func (s *Services) akickEnforce(reg *store.Channel, kicks []store.Akick) int {
	live := s.net.Channel(reg.Name)
	if live == nil {
		return 0
	}
	n := 0
	for _, m := range live.SortedMembers() {
		if m.User.Service || s.userLevel(reg, m.User) != store.LevelNone {
			continue
		}
		for _, k := range kicks {
			if matchesMask(k.Mask, m.User) {
				s.csMode(live, []modeChange{{true, 'b', k.Mask}})
				s.csKickUser(live, m.User, k.Reason)
				n++
				break
			}
		}
		if s.net.Channel(reg.Name) == nil {
			break // everyone left
		}
	}
	return n
}

// matchesMask reports whether a ban mask (extbans too, see state.MatchBan)
// covers u.
func matchesMask(mask string, u *state.User) bool {
	m, _ := state.MatchBan(mask, u)
	return m
}

// liveTarget finds the channel as it is now and a member in it.
func (s *Services) liveTarget(c *Client, reg *store.Channel, nick string) (*state.Channel, *state.User, bool) {
	live := s.net.Channel(reg.Name)
	if live == nil {
		s.cs(c, "%s is empty right now.", reg.Name)
		return nil, nil, false
	}
	t := s.net.UserByNick(nick)
	if t == nil || live.Member(t) == nil {
		s.cs(c, "%s isn't in %s.", nick, reg.Name)
		return nil, nil, false
	}
	return live, t, true
}

// modePrivs are the privileges for giving a status mode to others and to
// yourself.
var modePrivs = map[byte][2]string{
	'q': {"OWNER", "OWNERME"},
	'a': {"PROTECT", "PROTECTME"},
	'o': {"OP", "OPME"},
	'h': {"HALFOP", "HALFOPME"},
	'v': {"VOICE", "VOICEME"},
}

// autoPrivs are the privileges that give a status mode on join.
var autoPrivs = map[byte]string{'q': "AUTOOWNER", 'a': "AUTOPROTECT", 'o': "AUTOOP", 'h': "AUTOHALFOP", 'v': "AUTOVOICE"}

func csModeCmd(mode byte, add bool) func(*Services, *Client, []string) {
	return func(s *Services, c *Client, args []string) {
		name := argAt(args, 0)
		nick := argAt(args, 1)
		if nick == "" {
			nick = c.user.Nick
		}
		if !s.rankEnabled(state.RankForMode(mode)) {
			s.cs(c, "This server uses only @ and + prefixes, so +%c doesn't exist here.", mode)
			return
		}
		self := irc.EqualFold(nick, c.user.Nick)
		var reg *store.Channel
		var lvl int
		var ok bool
		switch {
		case self && !add:
			reg, lvl, ok = s.csChan(c, name, 0)
		case self:
			reg, lvl, ok = s.csPriv(c, name, modePrivs[mode][1])
		default:
			reg, lvl, ok = s.csPriv(c, name, modePrivs[mode][0])
		}
		if !ok {
			return
		}
		live, t, ok := s.liveTarget(c, reg, nick)
		if !ok {
			return
		}
		if t.Service {
			s.cs(c, "%s is a network service.", t.Nick)
			return
		}
		tl := s.userLevel(reg, t)
		if add && reg.SecureOps && !s.secureAllows(reg, tl, mode) {
			s.cs(c, "%s may not hold +%c on %s: SECUREOPS is on.", t.Nick, mode, reg.Name)
			return
		}
		if !add && !self && reg.Peace && tl >= lvl && lvl != store.LevelFounder {
			s.cs(c, "You can't do that to %s: their level is the same as or above yours.", t.Nick)
			return
		}
		s.csUsed(c, reg)
		s.csLog(c, reg.Name, false, "on %s", t.Nick)
		s.csMode(live, []modeChange{{add, mode, t.Nick}})
	}
}

func csKick(s *Services, c *Client, args []string) {
	reg, lvl, ok := s.csPriv(c, argAt(args, 0), "KICK")
	if !ok {
		return
	}
	if argAt(args, 1) == "" {
		s.cs(c, "Syntax: KICK %s <nick> [reason]", reg.Name)
		return
	}
	live, t, ok := s.liveTarget(c, reg, args[1])
	if !ok {
		return
	}
	if t.Service {
		s.cs(c, "%s is a network service.", t.Nick)
		return
	}
	if reg.Peace && s.userLevel(reg, t) >= lvl && lvl != store.LevelFounder {
		s.cs(c, "You can't kick %s: their level is the same as or above yours.", t.Nick)
		return
	}
	reason := strings.Join(args[2:], " ")
	if reason == "" {
		reason = "Requested"
	}
	s.csUsed(c, reg)
	s.csLog(c, reg.Name, false, "for %s", t.Nick)
	s.csKickUser(live, t, s.signed(reg, c, lvl, reason))
}

// signed puts "(nick) " in front of a kick reason when SIGNKICK says so.
func (s *Services) signed(reg *store.Channel, c *Client, lvl int, reason string) string {
	if reg.SignKick && !s.levelHas(reg, lvl, "SIGNKICK") {
		return "(" + c.user.Nick + ") " + reason
	}
	return reason
}

// csKickUser kicks u from ch as ChanServ (or the channel's bot).
func (s *Services) csKickUser(ch *state.Channel, u *state.User, reason string) {
	s.svcKick(s.whoSends(ch), ch, u, reason)
}

// svcKick kicks u from ch as a service.
func (s *Services) svcKick(sv *service, ch *state.Channel, u *state.User, reason string) {
	s.emit(&state.Kick{Source: sv.user.UID, UID: u.UID, Channel: ch.Name, Reason: irc.Truncate(reason, s.cfg.Limits.KickLen)})
}

// banTypes say what each BANTYPE bans.
var banTypes = []string{"*!user@host", "*!*user@host", "*!*@host", "*!*user@*.domain"}

// banMask is the ban BANTYPE gives for u.
func banMask(reg *store.Channel, u *state.User) string {
	user := strings.TrimPrefix(u.Ident, "~")
	host := u.Host
	switch reg.BanType {
	case 0:
		return "*!" + u.Ident + "@" + host
	case 1:
		return "*!*" + user + "@" + host
	case 3:
		return "*!*" + user + "@" + wideHost(host)
	}
	return "*!*@" + host
}

// wideHost is host with its first part as *: *.example.org, 1.2.3.*.
func wideHost(host string) string {
	if ip := net.ParseIP(host); ip != nil {
		if v4 := ip.To4(); v4 != nil {
			return fmt.Sprintf("%d.%d.%d.*", v4[0], v4[1], v4[2])
		}
		return host
	}
	if i := strings.IndexByte(host, '.'); i > 0 && strings.Count(host, ".") >= 2 {
		return "*" + host[i:]
	}
	return host
}

func csBan(s *Services, c *Client, args []string) {
	reg, lvl, ok := s.csPriv(c, argAt(args, 0), "BAN")
	if !ok {
		return
	}
	// BAN #chan [+expiry] nick|mask [reason] (Anope); the expiry becomes a
	// ~t: ban that lifts itself.
	var minutes int
	if e := argAt(args, 1); strings.HasPrefix(e, "+") {
		d, ok := parseExpiry(e)
		if !ok || d == 0 {
			s.cs(c, "%s isn't a time. Use +30m, +12h, +7d.", e)
			return
		}
		minutes = min(int((d+time.Minute-1)/time.Minute), irc.MaxTimedBan)
		args = append(args[:1:1], args[2:]...)
	}
	arg := argAt(args, 1)
	if arg == "" {
		s.cs(c, "Syntax: BAN %s [+expiry] <nick|mask> [reason]", reg.Name)
		return
	}
	live := s.net.Channel(reg.Name)
	if live == nil {
		s.cs(c, "%s is empty right now.", reg.Name)
		return
	}
	mask, valid := irc.NormalizeBan(arg)
	if !valid {
		s.cs(c, "%s isn't a mask or extban.", arg)
		return
	}
	t := s.net.UserByNick(arg)
	if t != nil && !t.Service {
		if reg.Peace && s.userLevel(reg, t) >= lvl && lvl != store.LevelFounder {
			s.cs(c, "You can't ban %s: their level is the same as or above yours.", t.Nick)
			return
		}
		mask = banMask(reg, t)
	}
	if minutes > 0 {
		mask = "~t:" + strconv.Itoa(minutes) + ":" + mask
	}
	reason := "Banned by " + c.user.Nick
	if len(args) > 2 {
		reason = s.signed(reg, c, lvl, strings.Join(args[2:], " "))
	}
	s.csUsed(c, reg)
	s.csLog(c, reg.Name, false, "for %s", mask)
	s.csMode(live, []modeChange{{true, 'b', mask}})
	for _, m := range live.SortedMembers() {
		if !m.User.Service && matchesMask(mask, m.User) && s.userLevel(reg, m.User) < lvl {
			s.csKickUser(live, m.User, reason)
		}
	}
}

func csUnban(s *Services, c *Client, args []string) {
	if len(args) == 0 {
		csUnbanEverywhere(s, c)
		return
	}
	arg := argAt(args, 1)
	self := arg == "" || irc.EqualFold(arg, c.user.Nick)
	reg, _, ok := s.csPriv(c, argAt(args, 0), "UNBAN")
	if !ok {
		return
	}
	live := s.net.Channel(reg.Name)
	if live == nil {
		s.cs(c, "%s is empty, so it has no bans.", reg.Name)
		return
	}
	var who *state.User
	if self {
		who = c.user
	} else {
		who = s.net.UserByNick(arg)
	}
	lift := bansOn(live, who, arg)
	if len(lift) == 0 {
		if self {
			s.cs(c, "You aren't banned in %s.", reg.Name)
		} else {
			s.cs(c, "No ban in %s matches %s.", reg.Name, arg)
		}
		return
	}
	s.csUsed(c, reg)
	if who != nil {
		s.csLog(c, reg.Name, false, "to unban %s", who.Nick)
	} else {
		s.csLog(c, reg.Name, false, "to unban %s", arg)
	}
	s.csMode(live, lift)
	s.cs(c, "%s removed in %s.", plural(len(lift), "ban"), reg.Name)
}

// bansOn are the bans in live that hit who (or, with who nil, equal mask).
func bansOn(live *state.Channel, who *state.User, mask string) []modeChange {
	var lift []modeChange
	for _, e := range live.Lists['b'] {
		if who != nil && matchesMask(e.Mask, who) || who == nil && irc.EqualFold(e.Mask, irc.NormalizeMask(mask)) || irc.EqualFold(e.Mask, mask) {
			lift = append(lift, modeChange{false, 'b', e.Mask})
		}
	}
	return lift
}

// csUnbanEverywhere is UNBAN with no channel (Anope 2.0.20): lift the bans
// on c in every open registered channel where c has UNBAN.
func csUnbanEverywhere(s *Services, c *Client) {
	if c.accountID == 0 {
		s.cs(c, "Identify first: /ns identify <password>")
		s.logDeniedUnreg(c, s.chanServ)
		return
	}
	all, err := s.store.Channels()
	if err != nil {
		s.dbErr(c, s.chanServ, err)
		return
	}
	n := 0
	for _, reg := range all {
		live := s.net.Channel(reg.Name)
		if live == nil || reg.Suspended(time.Now()) || !s.userHas(reg, c.user, "UNBAN") {
			continue
		}
		if lift := bansOn(live, c.user, ""); len(lift) > 0 {
			s.csMode(live, lift)
			n++
		}
	}
	s.csLog(c, "", false, "on all channels")
	s.cs(c, "You have been unbanned from %s.", plural(n, "channel"))
}

func csInvite(s *Services, c *Client, args []string) {
	nick := argAt(args, 1)
	reg, _, ok := s.csPriv(c, argAt(args, 0), "INVITE")
	if !ok {
		return
	}
	who := c.user
	if nick != "" && !irc.EqualFold(nick, c.user.Nick) {
		if who = s.net.UserByNick(nick); who == nil || who.Service {
			s.cs(c, "%s isn't online.", nick)
			return
		}
	}
	live := s.net.Channel(reg.Name)
	switch {
	case live == nil:
		s.cs(c, "%s is empty; just join it.", reg.Name)
	case live.Member(who) != nil && who == c.user:
		s.cs(c, "You are in %s already.", reg.Name)
	case live.Member(who) != nil:
		s.cs(c, "%s is in %s already.", who.Nick, reg.Name)
	default:
		s.csUsed(c, reg)
		if who != c.user {
			s.csLog(c, reg.Name, false, "for %s", who.Nick)
		} else {
			s.csLog(c, reg.Name, false, "")
		}
		s.emit(&state.Invite{Source: s.chanServ.user.UID, UID: who.UID, Channel: live.Name})
		if who != c.user {
			s.cs(c, "%s is invited to %s.", who.Nick, reg.Name)
		}
	}
}

func csGetKey(s *Services, c *Client, args []string) {
	reg, _, ok := s.csPriv(c, argAt(args, 0), "GETKEY")
	if !ok {
		return
	}
	s.csLog(c, reg.Name, false, "")
	if live := s.net.Channel(reg.Name); live != nil && live.HasMode('k') {
		s.csUsed(c, reg)
		s.cs(c, "The key of %s is %s", reg.Name, live.Key)
		return
	}
	s.cs(c, "%s has no key.", reg.Name)
}

func csSet(s *Services, c *Client, args []string) {
	// Anope's order is SET <option> #chan; ours used to be SET #chan <option>.
	// Both work: the word starting with # is the channel.
	if len(args) >= 2 && !irc.IsChannel(args[0]) && irc.IsChannel(args[1]) {
		args = append([]string{args[1], args[0]}, args[2:]...)
	}
	what := strings.ToUpper(argAt(args, 1))
	switch what {
	case "DESC":
		what = "DESCRIPTION"
	}
	var reg *store.Channel
	var ok, asOper bool
	switch what {
	case "FOUNDER":
		reg, asOper, ok = s.csChanOper(c, argAt(args, 0))
	case "SUCCESSOR", "SECUREFOUNDER", "MEMOLEVEL", "AUTOVOP":
		reg, _, ok = s.csChan(c, argAt(args, 0), store.LevelFounder)
	case "NOEXPIRE":
		if !c.can('s') {
			s.cs(c, "SET NOEXPIRE is for IRC operators with flag s.")
			return
		}
		reg, _, ok = s.csChan(c, argAt(args, 0), 0)
	case "":
		s.cs(c, "Syntax: SET <option> #chan <value> (see /cs help set)")
		return
	default:
		reg, _, ok = s.csPriv(c, argAt(args, 0), "SET")
	}
	if !ok {
		return
	}
	val := strings.Join(args[min(2, len(args)):], " ")
	// logText is Anope's log text for the change save makes (cs_set.cpp).
	logText := ""
	save := func(format string, a ...any) bool {
		if err := s.store.SaveChannel(reg); err != nil {
			s.dbErr(c, s.chanServ, err)
			return false
		}
		s.csUsed(c, reg)
		if what == "NOEXPIRE" {
			s.csLogAdmin(c, reg.Name, "%s", logText)
		} else {
			s.csLog(c, reg.Name, asOper, "%s", logText)
		}
		s.cs(c, format, a...)
		return true
	}
	// toggle sets an ON|OFF option; anope is its name in Anope's log text.
	toggle := func(field *bool, label string, invert bool, anope string) bool {
		on, ok := onOff(val)
		if !ok {
			s.cs(c, "Syntax: SET %s %s ON|OFF", what, reg.Name)
			return false
		}
		*field = on != invert
		word := "off"
		logText = "to disable " + anope
		if *field {
			word = "on"
			logText = "to enable " + anope
		}
		return save("%s is now %s for %s.", label, word, reg.Name)
	}
	live := s.net.Channel(reg.Name)
	switch what {
	case "DESCRIPTION":
		if val == "" {
			s.cs(c, "Syntax: SET DESCRIPTION %s <text>", reg.Name)
			return
		}
		reg.Description = irc.Truncate(val, 200)
		logText = "to change the description to " + reg.Description
		save("The description of %s is now: %s", reg.Name, reg.Description)
	case "FOUNDER", "SUCCESSOR":
		if what == "SUCCESSOR" && strings.EqualFold(val, "OFF") {
			logText = "to change the successor from " + orNoneP(reg.Successor) + " to (none)"
			reg.SuccessorID, reg.Successor = 0, ""
			save("%s has no successor now.", reg.Name)
			return
		}
		target, err := s.store.AccountByNick(argAt(args, 2))
		if err != nil {
			s.dbErr(c, s.chanServ, err)
			return
		}
		if target == nil {
			s.cs(c, "%s isn't a registered nick.", argAt(args, 2))
			return
		}
		if target.ID == reg.FounderID {
			s.cs(c, "%s is the founder of %s already.", target.Name, reg.Name)
			return
		}
		if what == "SUCCESSOR" {
			logText = "to change the successor from " + orNoneP(reg.Successor) + " to " + target.Name
			reg.SuccessorID, reg.Successor = target.ID, target.Name
			save("%s gets %s if %s's account goes.", target.Name, reg.Name, reg.Founder)
			return
		}
		if _, err := s.store.RemoveAccess(reg, target.ID); err != nil {
			s.dbErr(c, s.chanServ, err)
			return
		}
		old := reg.Founder
		logText = "to change the founder from " + orNoneP(old) + " to " + target.Name
		reg.FounderID, reg.Founder = target.ID, target.Name
		if reg.SuccessorID == target.ID {
			reg.SuccessorID, reg.Successor = 0, ""
		}
		if asOper {
			s.audit(c, s.chanServ, "SET FOUNDER %s %s (was %s)", reg.Name, target.Name, old)
		}
		save("The founder of %s is now %s.", reg.Name, target.Name)
	case "MLOCK":
		s.mlockSet(c, reg, "SET", args[min(2, len(args)):])
	case "KEEPTOPIC":
		toggle(&reg.KeepTopic, "Keep topic", false, "keeptopic")
	case "TOPICLOCK":
		toggle(&reg.TopicLock, "Topic lock", false, "topiclock")
	case "SECURE":
		toggle(&reg.Secure, "Security", false, "secure")
	case "SECUREOPS", "LEAVEOPS":
		if toggle(&reg.SecureOps, "Secure ops", what == "LEAVEOPS", "secure ops") && reg.SecureOps && live != nil {
			s.csSync(reg)
		}
	case "PRIVATE":
		toggle(&reg.Private, "Private", false, "private")
	case "RESTRICTED":
		toggle(&reg.Restricted, "Restricted", false, "restricted")
	case "PEACE":
		toggle(&reg.Peace, "Peace", false, "peace")
	case "SECUREFOUNDER":
		toggle(&reg.SecureFounder, "Secure founder", false, "secure founder")
	case "SIGNKICK":
		toggle(&reg.SignKick, "Sign kick", false, "sign kick")
	case "AUTOOP":
		toggle(&reg.AutoOp, "Auto-op", false, "autoop")
	case "NOEXPIRE":
		if toggle(&reg.NoExpire, "No expire", false, "noexpire") {
			s.audit(c, s.chanServ, "SET NOEXPIRE %s %s", reg.Name, strings.ToUpper(val))
		}
	case "KEEPMODES":
		on, _ := onOff(val)
		reg.LastModes = ""
		if on && live != nil {
			reg.LastModes = liveModes(live)
		}
		toggle(&reg.KeepModes, "Keep modes", false, "keep modes")
	case "PERSIST":
		if toggle(&reg.Persist, "Persist", false, "persist") {
			if reg.Persist {
				s.persistJoin(reg)
			} else {
				s.persistPart(reg)
			}
		}
	case "OVERFLOW":
		s.csOverflow(c, reg, val)
	case "BANTYPE":
		n, err := strconv.Atoi(val)
		if err != nil || n < 0 || n > 3 {
			s.cs(c, "Syntax: SET BANTYPE %s 0-3 (0 %s, 1 %s, 2 %s, 3 %s)", reg.Name, banTypes[0], banTypes[1], banTypes[2], banTypes[3])
			return
		}
		reg.BanType = n
		logText = "to change the ban type to " + strconv.Itoa(n)
		save("Bans on %s now look like %s.", reg.Name, banTypes[n])
	case "ENTRYMSG":
		switch {
		case val == "":
			s.cs(c, "Syntax: SET ENTRYMSG %s <text>|OFF", reg.Name)
		case strings.EqualFold(val, "OFF"):
			if _, err := s.store.ClearEntryMsgs(reg); err != nil {
				s.dbErr(c, s.chanServ, err)
				return
			}
			s.csUsed(c, reg)
			s.csLog(c, reg.Name, false, "to remove all messages")
			s.cs(c, "%s has no entry message now.", reg.Name)
		default:
			text := irc.Truncate(val, 300)
			var err error
			if lines := s.store.EntryMsgs(reg); len(lines) > 0 {
				err = s.store.SetEntryMsg(reg, lines[0].ID, text)
			} else {
				err = s.store.AddEntryMsg(reg, text, c.user.Account, time.Now())
			}
			if err != nil {
				s.dbErr(c, s.chanServ, err)
				return
			}
			s.csUsed(c, reg)
			s.csLog(c, reg.Name, false, "to add a message")
			s.cs(c, "The entry message of %s is now: %s", reg.Name, text)
		}
	case "URL":
		switch {
		case val == "":
			s.cs(c, "Syntax: SET URL %s <url>|OFF", reg.Name)
		case strings.EqualFold(val, "OFF"):
			reg.URL = ""
			logText = "to unset it"
			save("%s has no URL now.", reg.Name)
		default:
			reg.URL = irc.Truncate(val, 300)
			logText = "to change it to " + reg.URL
			save("The URL of %s is now: %s", reg.Name, reg.URL)
		}
	case "MEMOLEVEL":
		lvl, ok := parseLevel(val)
		if !ok {
			s.cs(c, "Syntax: SET MEMOLEVEL %s <level> (same as /cs levels %s set memo <level>)", reg.Name, reg.Name)
			return
		}
		p, _ := findPrivilege("MEMO")
		if err := s.setPrivLevel(reg, p, lvl); err != nil {
			s.dbErr(c, s.chanServ, err)
			return
		}
		s.csUsed(c, reg)
		s.csLog(c, reg.Name, false, "to set MEMO to level %d", lvl)
		s.cs(c, "MEMO on %s now needs level %s.", reg.Name, store.LevelName(lvl))
	case "AUTOVOP":
		on, ok := onOff(val)
		if !ok {
			s.cs(c, "Syntax: SET AUTOVOP %s ON|OFF", reg.Name)
			return
		}
		p, _ := findPrivilege("AUTOVOICE")
		lvl := p.def
		if on {
			lvl = 0
		}
		if err := s.setPrivLevel(reg, p, lvl); err != nil {
			s.dbErr(c, s.chanServ, err)
			return
		}
		s.csLog(c, reg.Name, false, "to set AUTOVOICE to level %d", lvl)
		if on {
			s.csUsed(c, reg)
			s.cs(c, "Everyone who joins %s is voiced now (AUTOVOICE 0).", reg.Name)
			s.csSync(reg)
		} else {
			s.csUsed(c, reg)
			s.cs(c, "AUTOVOICE on %s is back to level %d.", reg.Name, lvl)
		}
	default:
		s.cs(c, "Unknown option %s. See /cs help set", what)
	}
}

// parseMlock reads "+ntk-s secret" into lock fields, starting from the
// lock reg has (for ADD) or an empty one (for SET). check validates the
// argument of a parameter mode (+f +F +L +E +B +J). It returns what was
// wrong, or "".
func parseMlock(reg *store.Channel, args []string, merge bool, check func(m byte, arg string) (string, string)) string {
	if len(args) == 0 || args[0] == "" {
		return "Give modes, like +nt-s or +k secret."
	}
	on, off := map[byte]bool{}, map[byte]bool{}
	key, limit := "", 0
	vals := map[byte]string{}
	if merge {
		for _, m := range []byte(reg.MlockOn) {
			on[m] = true
		}
		for _, m := range []byte(reg.MlockOff) {
			off[m] = true
		}
		key, limit = reg.MlockKey, reg.MlockLimit
		vals = mlockParams(reg)
	}
	params := args[1:]
	next := func() string {
		if len(params) == 0 {
			return ""
		}
		p := params[0]
		params = params[1:]
		return p
	}
	add := true
	for i := 0; i < len(args[0]); i++ {
		m := args[0][i]
		switch {
		case m == '+':
			add = true
		case m == '-':
			add = false
		case m == 'k' && add:
			if key = irc.CleanKey(next()); key == "" {
				return "+k in a mode lock needs a key: +k <key>"
			}
			delete(off, 'k')
		case m == 'l' && add:
			n, err := strconv.Atoi(next())
			if err != nil || n < 1 {
				return "+l in a mode lock needs a number: +l 50"
			}
			limit = n
			delete(off, 'l')
		case m == 'k' || m == 'l':
			off[m] = true
			if m == 'k' {
				key = ""
			} else {
				limit = 0
			}
		case state.IsParamMode(m) && add:
			arg := next()
			if arg == "" {
				return fmt.Sprintf("+%c in a mode lock needs a value.", m)
			}
			norm, why := check(m, arg)
			if why != "" {
				return fmt.Sprintf("+%c %s: %s", m, arg, why)
			}
			vals[m] = norm
			delete(off, m)
		case state.IsParamMode(m):
			off[m] = true
			delete(vals, m)
		case strings.IndexByte(mlockFlags, m) >= 0:
			if add {
				on[m], off[m] = true, false
			} else {
				off[m], on[m] = true, false
			}
		default:
			return mlockHelp(m)
		}
	}
	reg.MlockOn, reg.MlockOff, reg.MlockKey, reg.MlockLimit = flagString(on), flagString(off), key, limit
	setMlockParams(reg, vals)
	return ""
}

func mlockHelp(m byte) string {
	return fmt.Sprintf("A mode lock can hold %s, k, l and %s, not %c.", mlockFlags, state.ParamModes, m)
}

// mlockParams reads reg's locked parameter modes ("f [10j]:15 L #other").
func mlockParams(reg *store.Channel) map[byte]string {
	out := map[byte]string{}
	f := strings.Fields(reg.MlockParams)
	for i := 0; i+1 < len(f); i += 2 {
		if len(f[i]) == 1 && state.IsParamMode(f[i][0]) {
			out[f[i][0]] = f[i+1]
		}
	}
	return out
}

func setMlockParams(reg *store.Channel, vals map[byte]string) {
	var parts []string
	for _, m := range []byte(state.ParamModes) {
		if v, ok := vals[m]; ok {
			parts = append(parts, string(m), v)
		}
	}
	reg.MlockParams = strings.Join(parts, " ")
}

// unlockModes takes the letters in modes out of reg's lock.
func unlockModes(reg *store.Channel, modes string) string {
	drop := func(set string, m byte) string { return strings.ReplaceAll(set, string(m), "") }
	vals := mlockParams(reg)
	for i := 0; i < len(modes); i++ {
		m := modes[i]
		switch {
		case m == '+' || m == '-':
		case m == 'k':
			reg.MlockKey, reg.MlockOff = "", drop(reg.MlockOff, 'k')
		case m == 'l':
			reg.MlockLimit, reg.MlockOff = 0, drop(reg.MlockOff, 'l')
		case state.IsParamMode(m):
			delete(vals, m)
			reg.MlockOff = drop(reg.MlockOff, m)
		case strings.IndexByte(mlockFlags, m) >= 0:
			reg.MlockOn, reg.MlockOff = drop(reg.MlockOn, m), drop(reg.MlockOff, m)
		default:
			return mlockHelp(m)
		}
	}
	setMlockParams(reg, vals)
	return ""
}

func flagString(m map[byte]bool) string {
	var b []byte
	for _, f := range []byte(mlockFlags + "kl" + state.ParamModes) {
		if m[f] {
			b = append(b, f)
		}
	}
	return string(b)
}

// mlockString shows a mode lock like "+ntk-s" (the key itself stays hidden).
func mlockString(reg *store.Channel) string {
	on := reg.MlockOn
	var args []string
	if reg.MlockKey != "" {
		on += "k"
	}
	if reg.MlockLimit > 0 {
		on += "l"
		args = append(args, strconv.Itoa(reg.MlockLimit))
	}
	vals := mlockParams(reg)
	for _, m := range []byte(state.ParamModes) {
		if v, ok := vals[m]; ok {
			on += string(m)
			args = append(args, v)
		}
	}
	out := ""
	if on != "" {
		out = "+" + on
	}
	if reg.MlockOff != "" {
		out += "-" + reg.MlockOff
	}
	if out == "" {
		return "none"
	}
	if len(args) > 0 {
		out += " " + strings.Join(args, " ")
	}
	return out
}

// mlockChanges is what it takes to make ch match reg's mode lock (and +r).
func mlockChanges(reg *store.Channel, ch *state.Channel) []modeChange {
	var out []modeChange
	if !ch.Modes['r'] {
		out = append(out, modeChange{true, 'r', ""})
	}
	for _, m := range []byte(reg.MlockOn) {
		if !ch.Modes[m] {
			out = append(out, modeChange{true, m, ""})
		}
	}
	for _, m := range []byte(reg.MlockOff) {
		switch {
		case m == 'k' && ch.Modes['k']:
			out = append(out, modeChange{false, 'k', ch.Key})
		case m != 'k' && ch.Modes[m]:
			out = append(out, modeChange{false, m, ""})
		}
	}
	if reg.MlockKey != "" && (!ch.Modes['k'] || ch.Key != reg.MlockKey) {
		out = append(out, modeChange{true, 'k', reg.MlockKey})
	}
	if reg.MlockLimit > 0 && (!ch.Modes['l'] || ch.Limit != reg.MlockLimit) {
		out = append(out, modeChange{true, 'l', strconv.Itoa(reg.MlockLimit)})
	}
	vals := mlockParams(reg)
	for _, m := range []byte(state.ParamModes) {
		if v, ok := vals[m]; ok && (!ch.Modes[m] || ch.Param(m) != v) {
			out = append(out, modeChange{true, m, v})
		}
	}
	return out
}

func csList(s *Services, c *Client, args []string) {
	pattern := argAt(args, 0)
	if pattern == "" {
		s.cs(c, "Syntax: LIST <pattern>, like #bee*")
		return
	}
	all, err := s.store.Channels()
	if err != nil {
		s.dbErr(c, s.chanServ, err)
		return
	}
	n := 0
	for _, reg := range all {
		if !irc.Match(pattern, reg.Name) {
			continue
		}
		if reg.Private && !c.isOper() {
			if lvl, _ := s.store.Level(reg, c.accountID); lvl == store.LevelNone {
				continue
			}
		}
		n++
		if n <= 100 {
			desc := reg.Description
			if reg.Suspended(time.Now()) {
				desc = "[suspended] " + desc
			}
			s.cs(c, "  %-20s %s", reg.Name, desc)
		}
	}
	switch {
	case n > 100:
		s.cs(c, "%d channels match; showing the first 100.", n)
	case n == 1:
		s.cs(c, "1 channel matches.")
	default:
		s.cs(c, "%d channels match.", n)
	}
}

// csMode sets modes on ch as ChanServ (or the channel's bot). Changes that
// change nothing are dropped when applied; the rest go out in lines of at
// most 4 parameters.
func (s *Services) csMode(ch *state.Channel, changes []modeChange) {
	s.svcMode(s.whoSends(ch), ch, changes)
}

// svcMode sets channel modes as service sv. Status modes for people not in
// the channel, or ranks this server doesn't use, are left out.
func (s *Services) svcMode(sv *service, ch *state.Channel, changes []modeChange) {
	var list []state.ModeChange
	for _, m := range changes {
		sc := state.ModeChange{Add: m.add, Mode: m.mode, Arg: m.arg}
		if r := state.RankForMode(m.mode); r != 0 {
			t := s.net.UserByNick(m.arg)
			if t == nil || ch.Member(t) == nil || !s.rankEnabled(r) {
				continue
			}
			sc.UID = t.UID
		}
		list = append(list, sc)
	}
	if len(list) > 0 {
		s.emit(&state.ChannelMode{Source: sv.user.UID, Channel: ch.Name, By: sv.user.Nick, At: time.Now(), Changes: list})
	}
}

// earnedModes are the status modes lvl earns on reg, highest first: owner
// or admin with op, else op, halfop or voice.
func (s *Services) earnedModes(reg *store.Channel, lvl int) string {
	var b []byte
	switch {
	case s.rankEnabled(state.RankOwner) && s.levelHas(reg, lvl, "AUTOOWNER"):
		b = append(b, 'q')
	case s.rankEnabled(state.RankAdmin) && s.levelHas(reg, lvl, "AUTOPROTECT"):
		b = append(b, 'a')
	}
	switch {
	case s.levelHas(reg, lvl, "AUTOOP"):
		b = append(b, 'o')
	case s.rankEnabled(state.RankHalfop) && s.levelHas(reg, lvl, "AUTOHALFOP"):
		b = append(b, 'h')
	case s.levelHas(reg, lvl, "AUTOVOICE"):
		b = append(b, 'v')
	}
	return string(b)
}

// levelPrefixes is earnedModes as prefixes, like "~@".
func (s *Services) levelPrefixes(reg *store.Channel, lvl int) string {
	var b []byte
	for _, m := range []byte(s.earnedModes(reg, lvl)) {
		for _, r := range state.Ranks {
			if r.Mode == m {
				b = append(b, r.Prefix)
			}
		}
	}
	return string(b)
}

// giveModes gives u the modes their level earns in ch.
func (s *Services) giveModes(reg *store.Channel, ch *state.Channel, u *state.User) {
	var changes []modeChange
	for _, m := range []byte(s.earnedModes(reg, s.userLevel(reg, u))) {
		changes = append(changes, modeChange{true, m, u.Nick})
	}
	s.csMode(ch, changes)
}

// csAutoModes is giveModes unless the channel has AUTOOP off.
func (s *Services) csAutoModes(reg *store.Channel, ch *state.Channel, u *state.User) {
	if !reg.AutoOp {
		return
	}
	if c := s.clientOf(u); c != nil && c.accountID != 0 {
		if a, _ := s.store.AccountByID(c.accountID); a != nil && !a.AutoOp {
			return // NickServ SET AUTOOP OFF
		}
	}
	s.giveModes(reg, ch, u)
}

// secureAllows reports whether someone at lvl may hold status mode on reg
// with SECUREOPS: they must get it on join or be allowed to give it to
// themself. Voice is free.
func (s *Services) secureAllows(reg *store.Channel, lvl int, mode byte) bool {
	if mode == 'v' {
		return true
	}
	return s.levelHas(reg, lvl, autoPrivs[mode]) || s.levelHas(reg, lvl, modePrivs[mode][1])
}

func csUp(s *Services, c *Client, args []string) {
	reg, lvl, ok := s.csChan(c, argAt(args, 0), 0)
	if !ok {
		return
	}
	if s.chanAccount(c, reg) == 0 {
		s.cs(c, "Identify first: /ns identify <password>")
		s.logDeniedUnreg(c, s.chanServ)
		return
	}
	live, _, ok := s.liveTarget(c, reg, c.user.Nick)
	if !ok {
		return
	}
	if s.earnedModes(reg, lvl) == "" {
		s.cs(c, "Your level on %s (%s) earns no status.", reg.Name, store.LevelName(lvl))
		return
	}
	s.csUsed(c, reg)
	s.csLog(c, reg.Name, false, "to update the status modes of %s", c.user.Nick)
	s.giveModes(reg, live, c.user)
}

func csDown(s *Services, c *Client, args []string) {
	reg, _, ok := s.csChan(c, argAt(args, 0), 0)
	if !ok {
		return
	}
	live, _, ok := s.liveTarget(c, reg, c.user.Nick)
	if !ok {
		return
	}
	mem := live.Member(c.user)
	var changes []modeChange
	for _, r := range state.Ranks {
		if mem.Has(r.Rank) {
			changes = append(changes, modeChange{false, r.Mode, c.user.Nick})
		}
	}
	s.csUsed(c, reg)
	s.csLog(c, reg.Name, false, "to remove the status modes from %s", c.user.Nick)
	s.csMode(live, changes)
}

// csSaveTopic keeps the topic for KEEPTOPIC.
func (s *Services) csSaveTopic(reg *store.Channel, ch *state.Channel) {
	if !reg.KeepTopic {
		return
	}
	reg.Topic, reg.TopicBy, reg.TopicAt = ch.Topic, ch.TopicBy, ch.TopicAt
	if err := s.store.SaveChannel(reg); err != nil {
		s.log.Printf("services database: %v", err)
	}
}

// channelInUse reports whether someone on reg's access list is in it now.
func (s *Services) channelInUse(reg *store.Channel) bool {
	live := s.net.Channel(reg.Name)
	if live == nil {
		return false
	}
	for _, m := range live.Members {
		if s.userLevel(reg, m.User) > store.LevelNone {
			return true
		}
	}
	return false
}

// orNoneP is s, or "(none)" when empty (Anope's founder/successor words).
func orNoneP(s string) string {
	if s == "" {
		return "(none)"
	}
	return s
}
