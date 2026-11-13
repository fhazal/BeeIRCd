package services

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/fhazal/beeircd/internal/config"
	"github.com/fhazal/beeircd/internal/irc"
	"github.com/fhazal/beeircd/internal/state"
	"github.com/fhazal/beeircd/internal/store"
)

// BotServ (Anope 2.0.20): opers create bots, channel founders assign one
// to their channel, and it sits there with op status. It talks for the
// channel (SAY, ACT), greets people, kicks for bad words and floods, and
// takes fantasy commands (!op, !kick ...).
//
// Bots made with BOT ADD are kept in the store; the service bots (ChanServ,
// NickServ ...) are bots too and may be assigned, like in Anope, but can't
// be changed or deleted. They have fixed negative IDs in the store.

// Anope's networkinfo limits for bot nicks, idents and hosts.
const (
	botUserLen = 10
	botHostLen = 64
)

// botMinUsers is botserv:minusers: the bot joins once a channel has this
// many people.
const botMinUsers = 1

// serviceBotIDs are the store IDs of the service bots.
var serviceBotIDs = map[string]int64{
	"NickServ": -1, "ChanServ": -2, "MemoServ": -3, "OperServ": -4,
	"HostServ": -5, "Global": -6, "BotServ": -7, "AdminServ": -8,
}

// botServ strings, as Anope's language.h has them.
const (
	msgBotNoSuch     = "Bot %s does not exist."
	msgBotNotAssign  = "You must assign a bot to the channel before using this command."
	msgBotNotOn      = "Bot is not on channel %s."
	msgChanNotReg    = "Channel %s isn't registered."
	msgAccessDenied  = "Access denied."
	msgNickRegisterd = "Nickname %s is already registered!"
)

func (s *Services) bs(c *Client, format string, a ...any) {
	s.svcReply(c, s.botServ, fmt.Sprintf(format, a...))
}

// botCommands are BotServ's commands.
func botServCommands() map[string]svcCmd {
	return map[string]svcCmd{
		"BOT": {"BOT ADD <nick> <user> <host> <real> | CHANGE <oldnick> <newnick> [<user> [<host> [<real>]]] | DEL <nick>", "make, change or delete bots (opers)",
			"BOT ADD <nick> <user> <host> <real name>\nBOT CHANGE <oldnick> <newnick> [user [host [real name]]]\nBOT DEL <nick>\nNeeds oper flag b. The service bots (ChanServ and the others) can't be\nchanged or deleted.", bsBot},
		"BOTLIST": {"BOTLIST", "the bots you can assign",
			"Lists every bot on the network. Bots marked with * are for IRC operators\nonly (SET PRIVATE).", bsBotList},
		"ASSIGN": {"ASSIGN <#channel> <nick>", "put a bot in your channel",
			"Puts a bot in a registered channel. It joins with op status while anyone is\nthere. Needs the ASSIGN privilege (the founder, by default).", bsAssign},
		"UNASSIGN": {"UNASSIGN <#channel>", "take the bot out again",
			"Takes the bot out of the channel. Its settings for the channel stay, for\nwhen you assign one again.", bsUnassign},
		"SAY": {"SAY <#channel> <text>", "make the bot say something",
			"The bot says the text in the channel. Needs the SAY privilege.", bsSay},
		"ACT": {"ACT <#channel> <text>", "make the bot do a /me",
			"The bot does a /me with the text in the channel. Needs the SAY privilege.", bsAct},
		"INFO": {"INFO {<#channel> | <nick>}", "about a bot or a channel's bot",
			"For a bot: its mask, real name, when it was made and how many channels use it.\nFor a channel: its bot and the bot options. Needs the INFO privilege.", bsInfo},
		"KICK": {"KICK <option> <#channel> {ON|OFF} [settings]", "make the bot kick for bad words, caps, floods ...",
			kickHelp(), bsKickCmd},
		"BADWORDS": {"BADWORDS <#channel> {ADD|DEL|LIST|CLEAR} [word|entry-num|list]", "the bad words list",
			"BADWORDS <#channel> ADD <word> [SINGLE | START | END]\nBADWORDS <#channel> DEL {<word> | <entry-num> | <list>}\nBADWORDS <#channel> LIST [<mask> | <list>]\nBADWORDS <#channel> CLEAR\nWords the BADWORDS kicker kicks for (KICK BADWORDS). Without a type the\nword counts anywhere, even inside other words; SINGLE only as a whole\nword, START at the start of a word, END at the end. Up to 32 words.\nNeeds the BADWORDS privilege.", bsBadwords},
		"SET": {"SET <#channel> <option> <value> | SET <nick> PRIVATE {ON | OFF}", "change the bot options",
			"SET <#channel> GREET {ON | OFF}      the bot shows greets (NickServ SET GREET)\nSET <#channel> FANTASY {ON | OFF}    !op, !kick and the like in the channel\nSET <#channel> BANEXPIRE <time>      bans by the bot lift after this (0 = never)\nSET <#channel> DONTKICKOPS {ON | OFF}     kickers leave ops alone\nSET <#channel> DONTKICKVOICES {ON | OFF}  kickers leave voiced people alone\nThese need the SET privilege. For opers:\nSET <#channel> NOBOT {ON | OFF}      no bot may be assigned (flag b)\nSET <nick> PRIVATE {ON | OFF}        only opers may assign the bot (flag b)", bsSet},
	}
}

// startBots brings back the bots kept in the store.
func (s *Services) startBots() {
	s.bots = make(map[int64]*service)
	for _, b := range s.store.Bots() {
		if s.net.UserByNick(b.Nick) != nil {
			s.log.Printf("BotServ: bot %s is not started: the nick is in use", b.Nick)
			continue
		}
		s.introduceBot(b)
	}
	s.bsWatchAll()
}

// introduceBot puts bot b on the network.
func (s *Services) introduceBot(b *store.Bot) *service {
	u := &state.User{
		UID:      s.net.NextUID(),
		Nick:     b.Nick,
		Ident:    b.Ident,
		Host:     b.Host,
		RealHost: b.Host,
		Realname: b.Realname,
		Server:   s.cfg.Server.Name,
		SignOn:   time.Now(),
		Service:  true,
	}
	if err := s.emit(&state.UserAdd{User: u}); err != nil {
		s.log.Printf("BotServ: bot %s is not started: %v", b.Nick, err)
		return nil
	}
	sv := &service{user: u, silent: true, botID: b.ID}
	s.services[irc.Fold(b.Nick)] = sv
	s.bots[b.ID] = sv
	return sv
}

// botByNick finds any bot (service bot or BOT ADD one) by nick, with its
// store ID.
func (s *Services) botByNick(nick string) (*service, int64) {
	sv := s.services[irc.Fold(nick)]
	if sv == nil {
		return nil, 0
	}
	if sv.botID != 0 {
		return sv, sv.botID
	}
	if id, ok := serviceBotIDs[sv.user.Nick]; ok {
		return sv, id
	}
	return nil, 0
}

// botByID finds the bot with store ID id.
func (s *Services) botByID(id int64) *service {
	if id > 0 {
		return s.bots[id]
	}
	for nick, sid := range serviceBotIDs {
		if sid == id {
			return s.services[irc.Fold(nick)]
		}
	}
	return nil
}

// botPrivate reports whether only opers may assign sv.
func (s *Services) botPrivate(sv *service) bool {
	if sv.botID > 0 {
		if b := s.store.BotByID(sv.botID); b != nil {
			return b.Private
		}
	}
	return false
}

// chanBot is the bot assigned to reg (nil = none) and reg's settings.
func (s *Services) chanBot(reg *store.Channel) (*service, store.ChanBot) {
	cb := s.store.ChanBot(reg)
	if cb.BotID == 0 {
		return nil, cb
	}
	return s.botByID(cb.BotID), cb
}

// botModes are botserv:botmodes ("ao"): op, and admin where the server
// has it.
func (s *Services) botModes(bot *state.User) []modeChange {
	out := []modeChange{{true, 'o', bot.Nick}}
	if s.cfg.FullPrefixes() {
		out = append([]modeChange{{true, 'a', bot.Nick}}, out...)
	}
	return out
}

// realPeople counts the members of ch who aren't services.
func realPeople(ch *state.Channel) int {
	n := 0
	for _, m := range ch.Members {
		if !m.User.Service {
			n++
		}
	}
	return n
}

// botJoin puts reg's bot in the live channel when it isn't there.
func (s *Services) botJoin(reg *store.Channel, sv *service) {
	ch := s.net.Channel(reg.Name)
	if sv == nil || ch != nil && ch.Member(sv.user) != nil {
		return
	}
	if s.emit(&state.Join{UID: sv.user.UID, Channel: reg.Name, TS: time.Now().Unix()}) != nil {
		return
	}
	if ch = s.net.Channel(reg.Name); ch != nil {
		s.svcMode(s.chanServ, ch, s.botModes(sv.user))
	}
}

// botPart takes sv out of channel name, with an optional reason.
func (s *Services) botPart(name string, sv *service, reason string) {
	ch := s.net.Channel(name)
	if sv == nil || ch == nil || ch.Member(sv.user) == nil {
		return
	}
	s.emit(&state.Part{UID: sv.user.UID, Channel: ch.Name, Reason: reason})
}

// bsJoined runs after a person joined ch (registered as reg): the bot
// comes in, and greets them when it should.
func (s *Services) bsJoined(c *Client, reg *store.Channel, ch *state.Channel) {
	sv, cb := s.chanBot(reg)
	if sv == nil {
		return
	}
	if realPeople(ch) >= botMinUsers {
		s.botJoin(reg, sv)
	}
	if !cb.Greet || c.accountID == 0 || ch.Member(sv.user) == nil {
		return
	}
	greet := s.store.Greet(c.accountID)
	if greet == "" || !s.userHas(reg, c.user, "GREET") {
		return
	}
	name := c.user.Account
	if a, _ := s.store.AccountByID(c.accountID); a != nil {
		name = a.Name
	}
	s.host.ChannelMessage(sv.user.UID, ch.Name, 0, true, "["+name+"] "+greet)
}

// bsTidy takes a bot out of the channels where only services are left
// (Anope's OnLeaveChannel), unless the channel is PERSIST. channel ""
// checks every channel with a bot.
func (s *Services) bsTidy(channel string) {
	check := func(reg *store.Channel) {
		sv, _ := s.chanBot(reg)
		ch := s.net.Channel(reg.Name)
		if sv == nil || ch == nil || ch.Member(sv.user) == nil || reg.Persist {
			return
		}
		if realPeople(ch) < botMinUsers {
			s.botPart(ch.Name, sv, "")
		}
	}
	if channel != "" {
		if reg, err := s.store.ChannelByName(channel); err == nil && reg != nil {
			check(reg)
		}
		return
	}
	for _, a := range s.store.BotAssignments() {
		if reg, err := s.store.ChannelByName(a.Name); err == nil && reg != nil {
			check(reg)
		}
	}
}

// bsKeepModes gives the bot its modes back when someone took them (Anope's
// OnSetCorrectModes for the assigned bot).
func (s *Services) bsKeepModes(reg *store.Channel, ch *state.Channel) []modeChange {
	sv, _ := s.chanBot(reg)
	if sv == nil {
		return nil
	}
	m := ch.Member(sv.user)
	if m == nil {
		return nil
	}
	var fix []modeChange
	for _, bm := range s.botModes(sv.user) {
		if !m.Has(state.RankForMode(bm.mode)) {
			fix = append(fix, bm)
		}
	}
	return fix
}

// bsAdmin is Anope's botserv/administration privilege: oper flag s.
func bsAdmin(c *Client) bool { return c.can('s') }

// bsChanPriv reports whether c has privilege priv on reg.
func (s *Services) bsChanPriv(c *Client, reg *store.Channel, priv string) bool {
	return s.levelHas(reg, s.accountLevel(reg, s.chanAccount(c, reg)), priv)
}

// bsLog posts Anope's command line for BotServ (COMMAND, or OVERRIDE when
// an oper acted without the channel privilege).
func (s *Services) bsLog(c *Client, override bool, channel, format string, a ...any) {
	kind := logCommand
	if override {
		kind = logOverride
	}
	s.logCmd(c, s.botServ, kind, channel, fmt.Sprintf(format, a...))
}

// bsReg finds a registered channel for a BotServ command.
func (s *Services) bsReg(c *Client, name string) *store.Channel {
	reg, err := s.store.ChannelByName(name)
	if err != nil {
		s.dbErr(c, s.botServ, err)
		return nil
	}
	if reg == nil {
		s.bs(c, msgChanNotReg, name)
		return nil
	}
	return reg
}

// ---- BOT ----

func bsBot(s *Services, c *Client, args []string) {
	switch strings.ToUpper(argAt(args, 0)) {
	case "ADD":
		if !c.can('b') {
			s.bs(c, msgAccessDenied)
			s.logDenied(c, s.botServ)
			return
		}
		if len(args) < 5 {
			s.bs(c, "Syntax: BOT ADD <nick> <user> <host> <real>")
			return
		}
		bsBotAdd(s, c, args[1], args[2], args[3], strings.Join(args[4:], " "))
	case "CHANGE":
		if !c.can('b') {
			s.bs(c, msgAccessDenied)
			s.logDenied(c, s.botServ)
			return
		}
		if len(args) < 3 {
			s.bs(c, "Syntax: BOT CHANGE <oldnick> <newnick> [<user> [<host> [<real>]]]")
			return
		}
		bsBotChange(s, c, args[1:])
	case "DEL":
		if !c.can('b') {
			s.bs(c, msgAccessDenied)
			s.logDenied(c, s.botServ)
			return
		}
		if len(args) < 2 {
			s.bs(c, "Syntax: BOT DEL <nick>")
			return
		}
		bsBotDel(s, c, args[1])
	default:
		s.bs(c, "Syntax: BOT ADD <nick> <user> <host> <real> | CHANGE <oldnick> <newnick> [<user> [<host> [<real>]]] | DEL <nick>")
	}
}

// botFieldsOK checks a bot's nick, ident and host the way Anope's BOT ADD
// and CHANGE do, answering c.
func (s *Services) botFieldsOK(c *Client, nick, user, host string) bool {
	switch {
	case nick != "" && len(nick) > s.cfg.Limits.NickLen:
		s.bs(c, "Bot nicks may only be %d characters long.", s.cfg.Limits.NickLen)
	case user != "" && len(user) > botUserLen:
		s.bs(c, "Bot idents may only be %d characters long.", botUserLen)
	case host != "" && len(host) > botHostLen:
		s.bs(c, "Bot hosts may only be %d characters long.", botHostLen)
	case nick != "" && !irc.ValidNick(nick, s.cfg.Limits.NickLen):
		s.bs(c, "Bot nicks may only contain valid nick characters.")
	case user != "" && !validIdent(user):
		s.bs(c, "Bot idents may only contain valid ident characters.")
	case host != "" && !validBotHost(host):
		s.bs(c, "Bot hosts may only contain valid host characters.")
	default:
		return true
	}
	return false
}

// validBotHost is Anope's IsHostValid: letters, digits, '.' and '-', not
// starting or ending with '.' or '-'.
func validBotHost(h string) bool {
	if h == "" || strings.ContainsAny(h[:1], ".-") || strings.ContainsAny(h[len(h)-1:], ".-") {
		return false
	}
	for i := 0; i < len(h); i++ {
		ch := h[i]
		if !(ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || ch >= '0' && ch <= '9' || ch == '.' || ch == '-') {
			return false
		}
	}
	return true
}

// botNickFree checks that nick can become a bot: no bot, no registered
// nick, nobody using it. self is a bot that may keep its own nick.
func (s *Services) botNickFree(c *Client, nick string, self *service) bool {
	if sv, _ := s.botByNick(nick); sv != nil && sv != self {
		s.bs(c, "Bot %s already exists.", nick)
		return false
	}
	if isReserved(nick) && self == nil {
		s.bs(c, "Bot %s already exists.", nick)
		return false
	}
	if a, err := s.store.AccountByNick(nick); err == nil && a != nil {
		s.bs(c, msgNickRegisterd, nick)
		return false
	}
	if u := s.net.UserByNick(nick); u != nil && (self == nil || u != self.user) {
		s.bs(c, "Nick %s is currently in use.", u.Nick)
		return false
	}
	return true
}

func bsBotAdd(s *Services, c *Client, nick, user, host, real string) {
	if sv, _ := s.botByNick(nick); sv != nil || isReserved(nick) {
		s.bs(c, "Bot %s already exists.", nick)
		return
	}
	if !s.botFieldsOK(c, nick, user, host) || !s.botNickFree(c, nick, nil) {
		return
	}
	b := &store.Bot{Nick: nick, Ident: user, Host: host, Realname: irc.Truncate(real, 50), Created: time.Now()}
	if err := s.store.AddBot(b); err != nil {
		if errors.Is(err, store.ErrExists) {
			s.bs(c, "Bot %s already exists.", nick)
			return
		}
		s.dbErr(c, s.botServ, err)
		return
	}
	if s.introduceBot(b) == nil {
		s.store.DeleteBot(b.ID)
		s.bs(c, "Nick %s is currently in use.", nick)
		return
	}
	s.logCmd(c, s.botServ, logAdmin, "", "ADD "+b.Nick+"!"+b.Ident+"@"+b.Host+" "+b.Realname)
	s.bs(c, "%s!%s@%s (%s) added to the bot list.", b.Nick, b.Ident, b.Host, b.Realname)
}

func bsBotChange(s *Services, c *Client, args []string) {
	oldnick, nick, user, host := args[0], args[1], argAt(args, 2), argAt(args, 3)
	real := ""
	if len(args) > 4 {
		real = irc.Truncate(strings.Join(args[4:], " "), 50)
	}
	sv, id := s.botByNick(oldnick)
	if sv == nil {
		s.bs(c, msgBotNoSuch, oldnick)
		return
	}
	if id < 0 {
		s.bs(c, "Bot %s is not changeable.", sv.user.Nick)
		return
	}
	b := s.store.BotByID(id)
	if b == nil {
		s.bs(c, msgBotNoSuch, oldnick)
		return
	}
	if !s.botFieldsOK(c, nick, user, host) {
		return
	}
	if nick == b.Nick && (user == "" || user == b.Ident) && (host == "" || host == b.Host) && (real == "" || real == b.Realname) {
		s.bs(c, "The old information is the same as the new information specified.")
		return
	}
	if !irc.EqualFold(nick, b.Nick) && !s.botNickFree(c, nick, sv) {
		return
	}
	old := b.Nick
	b.Nick = nick
	if user != "" {
		b.Ident = user
	}
	if host != "" {
		b.Host = host
	}
	if real != "" {
		b.Realname = real
	}
	if err := s.store.SaveBot(b); err != nil {
		s.dbErr(c, s.botServ, err)
		return
	}
	if user == "" {
		// Only the nick (Anope sends a NICK change).
		s.emit(&state.NickChange{UID: sv.user.UID, Nick: b.Nick, TS: time.Now().Unix()})
		delete(s.services, irc.Fold(old))
		s.services[irc.Fold(b.Nick)] = sv
	} else {
		// A new ident or host: Anope quits the bot and brings it back.
		var chans []string
		for _, ch := range sv.user.Channels {
			chans = append(chans, ch.Name)
		}
		s.emit(&state.UserQuit{UID: sv.user.UID, Reason: "Quit: Be right back"})
		delete(s.services, irc.Fold(old))
		delete(s.bots, id)
		nsv := s.introduceBot(b)
		for _, name := range chans {
			reg, err := s.store.ChannelByName(name)
			switch {
			case err != nil || reg == nil:
			case reg.Persist:
				s.persistJoin(reg) // reopens the channel with its topic and modes
			default:
				s.botJoin(reg, nsv)
			}
		}
	}
	s.bs(c, "Bot %s has been changed to %s!%s@%s (%s).", old, b.Nick, b.Ident, b.Host, b.Realname)
	s.logCmd(c, s.botServ, logAdmin, "", "CHANGE "+old+" to "+b.Nick+"!"+b.Ident+"@"+b.Host+" "+b.Realname)
}

func bsBotDel(s *Services, c *Client, nick string) {
	sv, id := s.botByNick(nick)
	if sv == nil {
		s.bs(c, msgBotNoSuch, nick)
		return
	}
	if id < 0 {
		s.bs(c, "Bot %s is not deletable.", sv.user.Nick)
		return
	}
	var names []string // its channels, for PERSIST below
	for _, a := range s.store.BotAssignments() {
		if a.BotID == id {
			names = append(names, a.Name)
		}
	}
	chans, err := s.store.DeleteBot(id)
	if err != nil {
		s.dbErr(c, s.botServ, err)
		return
	}
	defer func() {
		// ChanServ holds the PERSIST channels the bot held open.
		for _, name := range names {
			if reg, err := s.store.ChannelByName(name); err == nil && reg != nil && reg.Persist {
				s.persistJoin(reg)
			}
		}
	}()
	if len(chans) > 0 {
		s.bsWatchAll()
	}
	s.emit(&state.UserQuit{UID: sv.user.UID, Reason: "Quit: Bot deleted by " + c.user.Nick})
	delete(s.services, irc.Fold(sv.user.Nick))
	delete(s.bots, id)
	s.logCmd(c, s.botServ, logAdmin, "", "DEL "+sv.user.Nick)
	s.bs(c, "Bot %s has been deleted.", nick)
}

// ---- BOTLIST ----

func bsBotList(s *Services, c *Client, args []string) {
	type row struct{ nick, mask string }
	var rows []row
	add := func(sv *service, private bool) {
		if private && !bsAdmin(c) && !c.can('b') {
			return
		}
		n := sv.user.Nick
		if private {
			n = "* " + n
		}
		rows = append(rows, row{n, sv.user.Ident + "@" + sv.user.Host})
	}
	for _, sv := range s.allBots() {
		add(sv, s.botPrivate(sv))
	}
	if len(rows) == 0 {
		s.bs(c, "There are no bots available at this time.")
		s.bs(c, "Ask a Services Operator to create one!")
		return
	}
	w := len("Nick")
	for _, r := range rows {
		w = max(w, len(r.nick))
	}
	s.bs(c, "Bot list:")
	s.bs(c, "  %-*s  %s", w, "Nick", "Mask")
	for _, r := range rows {
		s.bs(c, "  %-*s  %s", w, r.nick, r.mask)
	}
	s.bs(c, "%d bots available.", len(rows))
}

// allBots lists every bot, service bots included, by nick.
func (s *Services) allBots() []*service {
	var out []*service
	for _, sv := range s.services {
		if _, id := s.botByNick(sv.user.Nick); id != 0 {
			out = append(out, sv)
		}
	}
	sortByNick(out)
	return out
}

func sortByNick(list []*service) {
	for i := 1; i < len(list); i++ {
		for j := i; j > 0 && irc.Fold(list[j].user.Nick) < irc.Fold(list[j-1].user.Nick); j-- {
			list[j], list[j-1] = list[j-1], list[j]
		}
	}
}

// ---- ASSIGN, UNASSIGN ----

func bsAssign(s *Services, c *Client, args []string) {
	if len(args) < 2 {
		s.bs(c, "Syntax: ASSIGN <#channel> <nick>")
		return
	}
	reg := s.bsReg(c, args[0])
	if reg == nil {
		return
	}
	sv, id := s.botByNick(args[1])
	if sv == nil {
		s.bs(c, msgBotNoSuch, args[1])
		return
	}
	cb := s.store.ChanBot(reg)
	has := s.bsChanPriv(c, reg, "ASSIGN")
	if cb.NoBot || (!has && !bsAdmin(c)) {
		s.bs(c, msgAccessDenied)
		return
	}
	if s.botPrivate(sv) && !bsAdmin(c) {
		s.bs(c, msgAccessDenied)
		return
	}
	if cb.BotID == id {
		s.bs(c, "Bot %s is already assigned to channel %s.", sv.user.Nick, args[0])
		return
	}
	old := s.botByID(cb.BotID)
	cb.BotID = id
	if err := s.store.SetChanBot(reg, cb); err != nil {
		s.dbErr(c, s.botServ, err)
		return
	}
	s.bsLog(c, !has, reg.Name, "for %s", sv.user.Nick)
	s.bsWatchUpdate(reg)
	if old != nil && old != sv && !(old == s.chanServ && reg.Persist) {
		s.botPart(reg.Name, old, "UNASSIGN from "+c.user.Nick)
	}
	if ch := s.net.Channel(reg.Name); ch != nil && (realPeople(ch) >= botMinUsers || reg.Persist) {
		s.botJoin(reg, sv)
	}
	if reg.Persist && sv != s.chanServ {
		s.persistPart(reg) // the bot holds the channel open now
	}
	s.bs(c, "Bot %s has been assigned to %s.", sv.user.Nick, reg.Name)
}

func bsUnassign(s *Services, c *Client, args []string) {
	if len(args) < 1 {
		s.bs(c, "Syntax: UNASSIGN <#channel>")
		return
	}
	reg := s.bsReg(c, args[0])
	if reg == nil {
		return
	}
	has := s.bsChanPriv(c, reg, "ASSIGN")
	if !bsAdmin(c) && !has {
		s.bs(c, msgAccessDenied)
		return
	}
	sv, cb := s.chanBot(reg)
	if cb.BotID == 0 {
		s.bs(c, msgBotNotAssign)
		return
	}
	cb.BotID = 0
	if err := s.store.SetChanBot(reg, cb); err != nil {
		s.dbErr(c, s.botServ, err)
		return
	}
	s.bsWatchUpdate(reg)
	if sv != nil {
		s.bsLog(c, !has, reg.Name, "for %s", sv.user.Nick)
		if reg.Persist {
			s.persistJoin(reg) // ChanServ keeps a PERSIST channel open again
		}
		if !(sv == s.chanServ && reg.Persist) {
			s.botPart(reg.Name, sv, "UNASSIGN from "+c.user.Nick)
		}
	}
	s.bs(c, "There is no bot assigned to %s anymore.", reg.Name)
}

// ---- SAY, ACT ----

// bsTalkTarget checks SAY and ACT: the channel, the privilege and a bot
// that is in the channel.
func (s *Services) bsTalkTarget(c *Client, name string) (*store.Channel, *service, bool, bool) {
	reg := s.bsReg(c, name)
	if reg == nil {
		return nil, nil, false, false
	}
	has := s.bsChanPriv(c, reg, "SAY")
	if !has && !bsAdmin(c) {
		s.bs(c, msgAccessDenied)
		return nil, nil, false, false
	}
	sv, _ := s.chanBot(reg)
	if sv == nil {
		s.bs(c, msgBotNotAssign)
		return nil, nil, false, false
	}
	if ch := s.net.Channel(reg.Name); ch == nil || ch.Member(sv.user) == nil {
		s.bs(c, msgBotNotOn, reg.Name)
		return nil, nil, false, false
	}
	return reg, sv, !has, true
}

func bsSay(s *Services, c *Client, args []string) {
	if len(args) < 2 {
		s.bs(c, "Syntax: SAY <#channel> <text>")
		return
	}
	text := strings.Join(args[1:], " ")
	reg, sv, override, ok := s.bsTalkTarget(c, args[0])
	if !ok {
		return
	}
	if text[0] == '\x01' {
		s.bs(c, "Syntax: SAY <#channel> <text>")
		return
	}
	s.host.ChannelMessage(sv.user.UID, reg.Name, 0, true, text)
	s.bsLog(c, override, reg.Name, "to say: %s", text)
}

func bsAct(s *Services, c *Client, args []string) {
	if len(args) < 2 {
		s.bs(c, "Syntax: ACT <#channel> <text>")
		return
	}
	reg, sv, override, ok := s.bsTalkTarget(c, args[0])
	if !ok {
		return
	}
	text := strings.ReplaceAll(strings.Join(args[1:], " "), "\x01", "")
	if text == "" {
		return
	}
	s.host.ChannelMessage(sv.user.UID, reg.Name, 0, true, "\x01ACTION "+text+"\x01")
	s.bsLog(c, override, reg.Name, "to say: %s", text)
}

// ---- INFO ----

func bsInfo(s *Services, c *Client, args []string) {
	if len(args) < 1 {
		s.bs(c, "Syntax: INFO {<#channel> | <nick>}")
		return
	}
	q := args[0]
	if sv, id := s.botByNick(q); sv != nil {
		s.bs(c, "Information for bot %s:", sv.user.Nick)
		created := sv.user.SignOn
		if b := s.store.BotByID(id); b != nil {
			created = b.Created
		}
		opts := "None"
		if s.botPrivate(sv) {
			opts = "Private"
		}
		var chans []string
		for _, a := range s.store.BotAssignments() {
			if a.BotID == id {
				chans = append(chans, a.Name)
			}
		}
		s.bs(c, "       Mask: %s@%s", sv.user.Ident, sv.user.Host)
		s.bs(c, "  Real name: %s", sv.user.Realname)
		s.bs(c, "    Created: %s", showTime(created))
		s.bs(c, "    Options: %s", opts)
		s.bs(c, "    Used on: %d channel(s)", len(chans))
		if bsAdmin(c) && len(chans) > 0 {
			line := ""
			for _, ch := range chans {
				line += " " + ch + " "
				if len(line) > 300 {
					s.bs(c, "%s", line)
					line = ""
				}
			}
			if line != "" {
				s.bs(c, "%s", line)
			}
		}
		return
	}
	if !irc.IsChannel(q) {
		s.bs(c, "%s is not a valid bot or registered channel.", q)
		return
	}
	reg, err := s.store.ChannelByName(q)
	if err != nil {
		s.dbErr(c, s.botServ, err)
		return
	}
	if reg == nil {
		s.bs(c, "%s is not a valid bot or registered channel.", q)
		return
	}
	if !s.bsChanPriv(c, reg, "INFO") && !bsAdmin(c) {
		s.bs(c, msgAccessDenied)
		return
	}
	sv, cb := s.chanBot(reg)
	nick := "not assigned yet"
	if sv != nil {
		nick = sv.user.Nick
	}
	onOff := func(v bool) string {
		if v {
			return "Enabled"
		}
		return "Disabled"
	}
	s.bs(c, "Information for channel %s:", reg.Name)
	line := func(label, v string) { s.bs(c, "%18s %s", label+":", v) }
	line("Bot nick", nick)
	line("Ban expiry", banExpiryWords(cb.BanExpire))
	line("Fantasy", onOff(cb.Fantasy))
	line("Greet", onOff(cb.Greet))
	line("Don't kick ops", onOff(cb.DontKickOps))
	line("Don't kick voices", onOff(cb.DontKickVoices))
	if cb.NoBot {
		line("No bot", "Enabled")
	}
	s.bsKickInfo(c, reg)
}

func banExpiryWords(d time.Duration) string {
	if d == 0 {
		return "never"
	}
	return durWords(d)
}

// ---- SET ----

func bsSet(s *Services, c *Client, args []string) {
	if len(args) < 3 {
		s.bs(c, "Syntax: SET <#channel> <option> <value> | SET <nick> PRIVATE {ON | OFF}")
		return
	}
	target, opt, val := args[0], strings.ToUpper(args[1]), args[2]
	c.cmdName = "SET " + opt
	if opt == "PRIVATE" {
		bsSetPrivate(s, c, target, val)
		return
	}
	reg := s.bsReg(c, target)
	if reg == nil {
		return
	}
	cb := s.store.ChanBot(reg)
	if opt == "NOBOT" {
		if !c.can('b') {
			s.bs(c, msgAccessDenied)
			s.logDenied(c, s.botServ)
			return
		}
		on, ok := onOff(val)
		if !ok {
			s.bs(c, "Syntax: SET <#channel> NOBOT {ON | OFF}")
			return
		}
		cb.NoBot = on
		var bot *service
		if on {
			bot = s.botByID(cb.BotID)
			cb.BotID = 0
		}
		if err := s.store.SetChanBot(reg, cb); err != nil {
			s.dbErr(c, s.botServ, err)
			return
		}
		s.bsWatchUpdate(reg)
		if on {
			s.logCmd(c, s.botServ, logAdmin, reg.Name, "to enable nobot")
			if bot != nil {
				if reg.Persist {
					s.persistJoin(reg)
				}
				if !(bot == s.chanServ && reg.Persist) {
					s.botPart(reg.Name, bot, "UNASSIGN from "+c.user.Nick)
				}
			}
			s.bs(c, "No-bot mode is now on on channel %s.", reg.Name)
		} else {
			s.logCmd(c, s.botServ, logAdmin, reg.Name, "to disable nobot")
			s.bs(c, "No-bot mode is now off on channel %s.", reg.Name)
		}
		return
	}
	has := s.bsChanPriv(c, reg, "SET")
	if !bsAdmin(c) && !has {
		s.bs(c, msgAccessDenied)
		return
	}
	toggle := func(field *bool, word, onMsg, offMsg, logOn, logOff string) {
		on, ok := onOff(val)
		if !ok {
			s.bs(c, "Syntax: SET <#channel> %s {ON | OFF}", word)
			return
		}
		*field = on
		if err := s.store.SetChanBot(reg, cb); err != nil {
			s.dbErr(c, s.botServ, err)
			return
		}
		s.bsWatchUpdate(reg) // FANTASY decides whether lines go to BotServ
		if on {
			s.bsLog(c, !has, reg.Name, "%s", logOn)
			s.bs(c, onMsg, reg.Name)
		} else {
			s.bsLog(c, !has, reg.Name, "%s", logOff)
			s.bs(c, offMsg, reg.Name)
		}
	}
	switch opt {
	case "GREET":
		toggle(&cb.Greet, "GREET", "Greet mode is now on on channel %s.", "Greet mode is now off on channel %s.", "to enable greets", "to disable greets")
	case "FANTASY":
		toggle(&cb.Fantasy, "FANTASY", "Fantasy mode is now on on channel %s.", "Fantasy mode is now off on channel %s.", "to enable fantasy", "to disable fantasy")
	case "DONTKICKOPS":
		toggle(&cb.DontKickOps, "DONTKICKOPS", "Bot won't kick ops on channel %s.", "Bot will kick ops on channel %s.", "to enable dontkickops", "to disable dontkickops")
	case "DONTKICKVOICES":
		toggle(&cb.DontKickVoices, "DONTKICKVOICES", "Bot won't kick voices on channel %s.", "Bot will kick voices on channel %s.", "to enable dontkickvoices", "to disable dontkickvoices")
	case "BANEXPIRE":
		d, err := config.ParseDuration(strings.ToLower(val))
		if val == "0" {
			d, err = 0, nil
		}
		if err != nil || d < 0 {
			s.bs(c, "Invalid expiry time.")
			return
		}
		if d > 24*time.Hour {
			s.bs(c, "Ban expiry may not be longer than 1 day.")
			return
		}
		cb.BanExpire = d.Truncate(time.Second)
		if err := s.store.SetChanBot(reg, cb); err != nil {
			s.dbErr(c, s.botServ, err)
			return
		}
		s.bsLog(c, !has, reg.Name, "to change banexpire to %d", int64(cb.BanExpire/time.Second))
		if cb.BanExpire == 0 {
			s.bs(c, "Bot bans will no longer automatically expire.")
		} else {
			s.bs(c, "Bot bans will automatically expire after %s.", durWords(cb.BanExpire))
		}
	default:
		s.bs(c, "Unknown option %s. Type /bs help set for the list.", opt)
	}
}

func bsSetPrivate(s *Services, c *Client, nick, val string) {
	if !c.can('b') {
		s.bs(c, msgAccessDenied)
		s.logDenied(c, s.botServ)
		return
	}
	sv, id := s.botByNick(nick)
	if sv == nil {
		s.bs(c, msgBotNoSuch, nick)
		return
	}
	on, ok := onOff(val)
	if !ok {
		s.bs(c, "Syntax: SET <nick> PRIVATE {ON | OFF}")
		return
	}
	b := s.store.BotByID(id)
	if b == nil {
		s.bs(c, "Bot %s is not changeable.", sv.user.Nick)
		return
	}
	b.Private = on
	if err := s.store.SaveBot(b); err != nil {
		s.dbErr(c, s.botServ, err)
		return
	}
	if on {
		s.bs(c, "Private mode of bot %s is now on.", b.Nick)
	} else {
		s.bs(c, "Private mode of bot %s is now off.", b.Nick)
	}
}
