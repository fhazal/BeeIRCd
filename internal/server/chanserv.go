package server

import (
	"fmt"
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
	mlockFlags        = "MRimnpst"
)

func chanServCommands() map[string]svcCmd {
	level := func(l int) svcCmd {
		n := store.LevelName(l)
		who := "SOPs and the founder"
		if l == store.LevelSOP {
			who = "the founder"
		}
		return svcCmd{n + " #chan ADD|DEL|LIST|CLEAR [nick]", "edit the " + n + " list",
			fmt.Sprintf("Adds or removes people on the %s level. Adding someone who is on another\nlevel moves them. %s can change this list; VOP and up can see it.\nExample: /cs %s #bchat add ember", n, who, strings.ToLower(n)),
			csLevelList(l)}
	}
	return map[string]svcCmd{
		"REGISTER": {"REGISTER #chan <description>", "register a channel you op in",
			"Registers a channel to your account; you become its Founder. You need to be\nidentified and a channel operator in it.\nExample: /cs register #bchat Be kind, stay curious", csRegister},
		"DROP": {"DROP #chan", "unregister a channel (founder)",
			"Removes the registration with its access list and AKICKs. You are asked to\nrepeat it with a code first.", csDrop},
		"INFO": {"INFO #chan", "who owns a channel and its settings",
			"Shows the founder, successor, description, mode lock and options.", csInfo},
		"SOP":    level(store.LevelSOP),
		"AOP":    level(store.LevelAOP),
		"VOP":    level(store.LevelVOP),
		"UOP":    level(store.LevelUOP),
		"ACCESS": {"ACCESS #chan LIST", "every level in one list", "Shows the founder and every SOP, AOP, VOP and UOP. VOP and up can see it.", csAccess},
		"AKICK": {"AKICK #chan ADD|DEL|LIST [mask] [reason]", "keep someone out for good",
			"Anyone matching an AKICK mask is banned and kicked when they join, unless they\nare on the access list. SOP and up.\nExample: /cs akick #bchat add *!*@*.badhost.example spamming", csAkick},
		"OP":      {"OP #chan [nick]", "op someone (or yourself)", "AOP and up. With SECURE on, only AOP and up can be opped.", csModeCmd('o', true)},
		"DEOP":    {"DEOP #chan [nick]", "deop someone (or yourself)", "AOP and up can deop others; anyone can deop themself.", csModeCmd('o', false)},
		"VOICE":   {"VOICE #chan [nick]", "voice someone (or yourself)", "AOP and up can voice others; VOP and up can voice themself.", csModeCmd('v', true)},
		"DEVOICE": {"DEVOICE #chan [nick]", "devoice someone (or yourself)", "AOP and up can devoice others; anyone can devoice themself.", csModeCmd('v', false)},
		"KICK":    {"KICK #chan <nick> [reason]", "kick through ChanServ", "AOP and up. Nobody can kick someone on the same level or above, except the founder.", csKick},
		"BAN":     {"BAN #chan <nick|mask>", "ban and kick", "AOP and up. A nick bans that person's host.", csBan},
		"UNBAN":   {"UNBAN #chan [nick|mask]", "remove bans", "Without a nick, removes every ban on you (UOP and up). AOP and up can unban\nothers.", csUnban},
		"INVITE":  {"INVITE #chan", "invite yourself", "UOP and up. Gets you past +i.", csInvite},
		"TOPIC":   {"TOPIC #chan <text>", "set the topic", "AOP and up. Works even with TOPICLOCK on.", csTopic},
		"CLEAR":   {"CLEAR #chan BANS|MODES|OPS|VOICES", "clean up a channel", "SOP and up. MODES puts the mode lock back afterwards.", csClear},
		"SET": {"SET #chan <option> <value>", "change channel settings",
			"SET #chan DESC <text>\nSET #chan FOUNDER <nick>            (founder)\nSET #chan SUCCESSOR <nick>|OFF     (founder) gets the channel if the founder's\n                                    account goes\nSET #chan MLOCK <modes>            like +nt-s or +ntk secret\nSET #chan KEEPTOPIC ON|OFF         put the topic back when the channel is reopened (on)\nSET #chan TOPICLOCK ON|OFF         only AOP and up may change the topic\nSET #chan SECURE ON|OFF            only AOP and up may be opped\nSET #chan PRIVATE ON|OFF           hide from /cs list and /cs info for outsiders\nSET #chan ENTRYMSG <text>|OFF      a notice to everyone who joins\nSET #chan URL <url>|OFF\nEverything but FOUNDER and SUCCESSOR: SOP and up.", csSet},
		"LIST": {"LIST <pattern>", "search registered channels", "Lists registered channels matching a pattern, like #bee*. Private ones are\nleft out unless you are on their access list.", csList},
	}
}

func (s *Server) cs(c *Client, format string, a ...any) {
	s.svcReply(c, s.chanServ, fmt.Sprintf(format, a...))
}

func (s *Server) cmdCS(c *Client, m irc.Message) {
	s.toService(c, s.chanServ, strings.Join(m.Params, " "))
}

// csChan loads a registered channel and c's level on it. With need above
// zero, c must be identified and at least that level.
func (s *Server) csChan(c *Client, name string, need int) (*store.Channel, int, bool) {
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
	lvl, err := s.store.Level(reg, c.accountID)
	if err != nil {
		s.dbErr(c, s.chanServ, err)
		return nil, 0, false
	}
	if need > 0 {
		switch {
		case c.accountID == 0:
			s.cs(c, "Identify first: /ns identify <password>")
			return nil, 0, false
		case lvl < need && need == store.LevelFounder:
			s.cs(c, "Only the founder of %s can do that.", reg.Name)
			return nil, 0, false
		case lvl < need:
			s.cs(c, "You need to be %s or higher on %s for that.", store.LevelName(need), reg.Name)
			return nil, 0, false
		}
	}
	return reg, lvl, true
}

// levelOf is a connected user's level on reg.
func (s *Server) levelOf(reg *store.Channel, u *state.User) int {
	c := s.clientOf(u)
	if c == nil || c.accountID == 0 {
		return store.LevelNone
	}
	lvl, err := s.store.Level(reg, c.accountID)
	if err != nil {
		return store.LevelNone
	}
	return lvl
}

func csRegister(s *Server, c *Client, args []string) {
	u := c.user
	name := argAt(args, 0)
	if c.accountID == 0 {
		s.cs(c, "Identify first: /ns identify <password>")
		return
	}
	if !irc.IsChannel(name) {
		s.cs(c, "Syntax: REGISTER #chan <description>")
		return
	}
	desc := strings.Join(args[1:], " ")
	if desc == "" {
		s.cs(c, "Give a short description: /cs register %s <description>", name)
		return
	}
	if reg, err := s.store.ChannelByName(name); err != nil {
		s.dbErr(c, s.chanServ, err)
		return
	} else if reg != nil {
		s.cs(c, "%s is registered already. Its founder is %s.", reg.Name, reg.Founder)
		return
	}
	live := s.net.Channel(name)
	if live == nil || live.Member(u) == nil || !live.Member(u).Op {
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
	s.csMode(live, mlockChanges(reg, live))
}

func csDrop(s *Server, c *Client, args []string) {
	reg, _, ok := s.csChan(c, argAt(args, 0), store.LevelFounder)
	if !ok {
		return
	}
	code := argAt(args, 1)
	key := irc.Fold(reg.Name) + ":"
	if code == "" || c.dropCode != key+code || time.Now().After(c.dropUntil) {
		if code != "" {
			s.cs(c, "That code is wrong or too old.")
		}
		fresh := randomCode()
		c.dropCode, c.dropUntil = key+fresh, time.Now().Add(dropCodeFor)
		s.cs(c, "This removes the registration of %s with its access list and AKICKs, for good.", reg.Name)
		s.cs(c, "To go ahead within 2 minutes: /cs drop %s %s", reg.Name, fresh)
		return
	}
	c.dropCode = ""
	if err := s.store.DeleteChannel(reg.ID); err != nil {
		s.dbErr(c, s.chanServ, err)
		return
	}
	if live := s.net.Channel(reg.Name); live != nil {
		s.csMode(live, []modeChange{{false, 'r', ""}})
	}
	s.log.Printf("dropped channel %s (by %s)", reg.Name, c.user.Account)
	s.cs(c, "%s is no longer registered.", reg.Name)
}

func csInfo(s *Server, c *Client, args []string) {
	reg, lvl, ok := s.csChan(c, argAt(args, 0), 0)
	if !ok {
		return
	}
	if reg.Private && lvl == store.LevelNone && !c.user.HasMode('o') {
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
	s.cs(c, "  Mode lock     %s", mlockString(reg))
	if s.channelInUse(reg) {
		s.cs(c, "  Last used     in use now")
	} else {
		s.cs(c, "  Last used     %s (%s)", showTime(reg.LastUsed), ago(reg.LastUsed))
	}
	var opts []string
	for _, o := range []struct {
		on   bool
		name string
	}{{reg.KeepTopic, "keep topic"}, {reg.TopicLock, "topic lock"}, {reg.Secure, "secure"}, {reg.Private, "private"}} {
		if o.on {
			opts = append(opts, o.name)
		}
	}
	if len(opts) == 0 {
		opts = []string{"none"}
	}
	s.cs(c, "  Options       %s", strings.Join(opts, ", "))
	if reg.EntryMsg != "" {
		s.cs(c, "  Entry message %s", reg.EntryMsg)
	}
}

// csLevelList handles SOP, AOP, VOP and UOP.
func csLevelList(level int) func(*Server, *Client, []string) {
	n := store.LevelName(level)
	return func(s *Server, c *Client, args []string) {
		name, sub, nick := argAt(args, 0), strings.ToUpper(argAt(args, 1)), argAt(args, 2)
		need := store.LevelSOP
		if level == store.LevelSOP {
			need = store.LevelFounder
		}
		if sub == "LIST" {
			need = store.LevelVOP
		}
		if sub == "" {
			s.cs(c, "Syntax: %s #chan ADD|DEL|LIST|CLEAR [nick]", n)
			return
		}
		reg, lvl, ok := s.csChan(c, name, need)
		if !ok {
			return
		}
		switch sub {
		case "ADD", "DEL":
			if nick == "" {
				s.cs(c, "Syntax: %s %s %s <nick>", n, sub, reg.Name)
				return
			}
			target, err := s.store.AccountByNick(nick)
			if err != nil {
				s.dbErr(c, s.chanServ, err)
				return
			}
			if target == nil {
				s.cs(c, "%s isn't a registered nick.", nick)
				return
			}
			if target.ID == reg.FounderID {
				s.cs(c, "%s is the founder of %s.", target.Name, reg.Name)
				return
			}
			old, err := s.store.Level(reg, target.ID)
			if err != nil {
				s.dbErr(c, s.chanServ, err)
				return
			}
			if old >= lvl && lvl != store.LevelFounder {
				s.cs(c, "You can't change %s: their level is the same as or above yours.", target.Name)
				return
			}
			if sub == "DEL" {
				if old != level {
					s.cs(c, "%s isn't on the %s list of %s.", target.Name, n, reg.Name)
					return
				}
				if _, err := s.store.RemoveAccess(reg, target.ID); err != nil {
					s.dbErr(c, s.chanServ, err)
					return
				}
				s.cs(c, "%s removed from the %s list of %s.", target.Name, n, reg.Name)
				return
			}
			if old == level {
				s.cs(c, "%s is on the %s list of %s already.", target.Name, n, reg.Name)
				return
			}
			if _, err := s.store.SetLevel(reg, target, level, c.user.Account, time.Now()); err != nil {
				s.dbErr(c, s.chanServ, err)
				return
			}
			if old == store.LevelNone {
				s.cs(c, "%s added to the %s list of %s.", target.Name, n, reg.Name)
			} else {
				s.cs(c, "%s moved from %s to %s on %s.", target.Name, store.LevelName(old), n, reg.Name)
			}
			if live := s.net.Channel(reg.Name); live != nil {
				for _, oc := range s.onlineClients(target.ID) {
					if live.Member(oc.user) != nil {
						s.csAutoModes(live, oc.user, level)
					}
				}
			}
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
			width := 4
			for _, e := range entries {
				width = max(width, len(e.Account))
			}
			s.cs(c, "%s list of %s:", n, reg.Name)
			for i, e := range entries {
				s.cs(c, "  %-3d %-*s  added by %s, %s", i+1, width, e.Account, e.AddedBy, e.AddedAt.UTC().Format("2 Jan 2006"))
			}
			s.cs(c, "%s.", plural(len(entries), "entry"))
		case "CLEAR":
			if level == store.LevelSOP && lvl != store.LevelFounder {
				return
			}
			removed, err := s.store.ClearLevel(reg, level)
			if err != nil {
				s.dbErr(c, s.chanServ, err)
				return
			}
			s.cs(c, "The %s list of %s is now empty (%d removed).", n, reg.Name, removed)
		default:
			s.cs(c, "Syntax: %s #chan ADD|DEL|LIST|CLEAR [nick]", n)
		}
	}
}

func csAccess(s *Server, c *Client, args []string) {
	if sub := strings.ToUpper(argAt(args, 1)); sub != "" && sub != "LIST" {
		s.cs(c, "Syntax: ACCESS #chan LIST. To change it: /cs aop #chan add <nick> (or sop, vop, uop)")
		return
	}
	reg, _, ok := s.csChan(c, argAt(args, 0), store.LevelVOP)
	if !ok {
		return
	}
	entries, err := s.store.AccessList(reg, 0)
	if err != nil {
		s.dbErr(c, s.chanServ, err)
		return
	}
	width := len(reg.Founder)
	for _, e := range entries {
		width = max(width, len(e.Account))
	}
	width = max(width, 4)
	gets := map[int]string{store.LevelFounder: "@", store.LevelSOP: "@", store.LevelAOP: "@", store.LevelVOP: "+", store.LevelUOP: "–"}
	also := map[int]string{
		store.LevelFounder: "everything",
		store.LevelSOP:     "add/remove AOP, VOP, UOP · akick",
		store.LevelAOP:     "kick, ban, topic, invite",
		store.LevelVOP:     "voice themself",
		store.LevelUOP:     "invite and unban themself",
	}
	row := func(l int, nick string) {
		s.cs(c, "  %-8s %-*s  %-5s  %s", store.LevelName(l), width, nick, gets[l], also[l])
	}
	s.cs(c, "Access list of %s", reg.Name)
	s.cs(c, "  %-8s %-*s  %-5s  %s", "Level", width, "Nick", "Gets", "Can also")
	row(store.LevelFounder, reg.Founder)
	for _, e := range entries {
		row(e.Level, e.Account)
	}
	s.cs(c, "%s. Change one: /cs aop %s del <nick>", plural(len(entries)+1, "entry"), reg.Name)
}

func csAkick(s *Server, c *Client, args []string) {
	reg, _, ok := s.csChan(c, argAt(args, 0), store.LevelSOP)
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
	case sub == "LIST":
		if len(list) == 0 {
			s.cs(c, "The AKICK list of %s is empty.", reg.Name)
			return
		}
		s.cs(c, "AKICK list of %s:", reg.Name)
		for i, k := range list {
			s.cs(c, "  %-3d %s  by %s · %s", i+1, k.Mask, k.AddedBy, k.Reason)
		}
	case (sub == "ADD" || sub == "DEL") && arg == "":
		s.cs(c, "Syntax: AKICK %s %s <mask>", reg.Name, sub)
	case sub == "ADD":
		if len(list) >= maxAkicks {
			s.cs(c, "The AKICK list of %s is full (%d).", reg.Name, maxAkicks)
			return
		}
		mask := irc.NormalizeMask(arg)
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
		s.cs(c, "Added %s to the AKICK list of %s.", mask, reg.Name)
		if live := s.net.Channel(reg.Name); live != nil {
			for _, m := range live.SortedMembers() {
				if !m.User.Service && s.levelOf(reg, m.User) == store.LevelNone && matchesMask(mask, m.User) {
					s.csMode(live, []modeChange{{true, 'b', mask}})
					s.csKickUser(live, m.User, reason)
				}
			}
		}
	case sub == "DEL":
		mask := arg
		if n, err := strconv.Atoi(arg); err == nil && n >= 1 && n <= len(list) {
			mask = list[n-1].Mask
		} else {
			mask = irc.NormalizeMask(arg)
		}
		removed, err := s.store.DelAkick(reg, mask)
		if err != nil {
			s.dbErr(c, s.chanServ, err)
			return
		}
		if !removed {
			s.cs(c, "%s isn't on the AKICK list of %s.", mask, reg.Name)
			return
		}
		s.cs(c, "Removed %s from the AKICK list of %s.", mask, reg.Name)
	default:
		s.cs(c, "Syntax: AKICK #chan ADD|DEL|LIST [mask] [reason]")
	}
}

func matchesMask(mask string, u *state.User) bool {
	for _, m := range u.Masks() {
		if irc.Match(mask, m) {
			return true
		}
	}
	return false
}

// liveTarget finds the channel as it is now and a member in it.
func (s *Server) liveTarget(c *Client, reg *store.Channel, nick string) (*state.Channel, *state.User, bool) {
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

func csModeCmd(mode byte, add bool) func(*Server, *Client, []string) {
	return func(s *Server, c *Client, args []string) {
		name := argAt(args, 0)
		nick := argAt(args, 1)
		if nick == "" {
			nick = c.user.Nick
		}
		self := irc.EqualFold(nick, c.user.Nick)
		need := store.LevelAOP
		switch {
		case self && !add:
			need = 0
		case self && mode == 'v':
			need = store.LevelVOP
		}
		reg, lvl, ok := s.csChan(c, name, need)
		if !ok {
			return
		}
		live, t, ok := s.liveTarget(c, reg, nick)
		if !ok {
			return
		}
		tl := s.levelOf(reg, t)
		if mode == 'o' && add && reg.Secure && tl < store.LevelAOP {
			s.cs(c, "%s isn't AOP or higher on %s, and SECURE is on.", t.Nick, reg.Name)
			return
		}
		if !add && !self && tl > lvl && lvl != store.LevelFounder {
			s.cs(c, "You can't do that to %s: their level is above yours.", t.Nick)
			return
		}
		s.csMode(live, []modeChange{{add, mode, t.Nick}})
	}
}

func csKick(s *Server, c *Client, args []string) {
	reg, lvl, ok := s.csChan(c, argAt(args, 0), store.LevelAOP)
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
	if s.levelOf(reg, t) >= lvl && lvl != store.LevelFounder {
		s.cs(c, "You can't kick %s: their level is the same as or above yours.", t.Nick)
		return
	}
	reason := strings.Join(args[2:], " ")
	if reason == "" {
		reason = "Requested"
	}
	s.csKickUser(live, t, "("+c.user.Nick+") "+reason)
}

// csKickUser kicks u from ch as ChanServ.
func (s *Server) csKickUser(ch *state.Channel, u *state.User, reason string) {
	s.broadcast(ch, s.out(s.chanServ.user, "KICK", ch.Name, u.Nick, irc.Truncate(reason, s.cfg.Limits.KickLen)).text(), nil)
	s.net.Part(u, ch)
}

func csBan(s *Server, c *Client, args []string) {
	reg, lvl, ok := s.csChan(c, argAt(args, 0), store.LevelAOP)
	if !ok {
		return
	}
	arg := argAt(args, 1)
	if arg == "" {
		s.cs(c, "Syntax: BAN %s <nick|mask>", reg.Name)
		return
	}
	live := s.net.Channel(reg.Name)
	if live == nil {
		s.cs(c, "%s is empty right now.", reg.Name)
		return
	}
	mask := irc.NormalizeMask(arg)
	t := s.net.UserByNick(arg)
	if t != nil && !t.Service {
		if s.levelOf(reg, t) >= lvl && lvl != store.LevelFounder {
			s.cs(c, "You can't ban %s: their level is the same as or above yours.", t.Nick)
			return
		}
		mask = "*!*@" + t.Host
	}
	s.csMode(live, []modeChange{{true, 'b', mask}})
	for _, m := range live.SortedMembers() {
		if matchesMask(mask, m.User) && s.levelOf(reg, m.User) < lvl {
			s.csKickUser(live, m.User, "Banned by "+c.user.Nick)
		}
	}
}

func csUnban(s *Server, c *Client, args []string) {
	arg := argAt(args, 1)
	self := arg == "" || irc.EqualFold(arg, c.user.Nick)
	need := store.LevelAOP
	if self {
		need = store.LevelUOP
	}
	reg, _, ok := s.csChan(c, argAt(args, 0), need)
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
	var lift []modeChange
	for _, e := range live.Lists['b'] {
		if who != nil && matchesMask(e.Mask, who) || who == nil && irc.EqualFold(e.Mask, irc.NormalizeMask(arg)) {
			lift = append(lift, modeChange{false, 'b', e.Mask})
		}
	}
	if len(lift) == 0 {
		if self {
			s.cs(c, "You aren't banned in %s.", reg.Name)
		} else {
			s.cs(c, "No ban in %s matches %s.", reg.Name, arg)
		}
		return
	}
	s.csMode(live, lift)
	s.cs(c, "%s removed in %s.", plural(len(lift), "ban"), reg.Name)
}

func csInvite(s *Server, c *Client, args []string) {
	reg, _, ok := s.csChan(c, argAt(args, 0), store.LevelUOP)
	if !ok {
		return
	}
	live := s.net.Channel(reg.Name)
	switch {
	case live == nil:
		s.cs(c, "%s is empty; just join it.", reg.Name)
	case live.Member(c.user) != nil:
		s.cs(c, "You are in %s already.", reg.Name)
	default:
		live.Invited[c.user.UID] = true
		c.deliver(s.out(s.chanServ.user, "INVITE", c.user.Nick, live.Name))
	}
}

func csTopic(s *Server, c *Client, args []string) {
	reg, _, ok := s.csChan(c, argAt(args, 0), store.LevelAOP)
	if !ok {
		return
	}
	live := s.net.Channel(reg.Name)
	if live == nil {
		s.cs(c, "%s is empty right now.", reg.Name)
		return
	}
	topic := irc.Truncate(strings.Join(args[1:], " "), s.cfg.Limits.TopicLen)
	live.Topic, live.TopicBy, live.TopicAt = topic, c.user.Nick, time.Now()
	s.broadcast(live, s.out(s.chanServ.user, "TOPIC", live.Name, topic).text(), nil)
	s.csSaveTopic(reg, live)
}

func csClear(s *Server, c *Client, args []string) {
	reg, _, ok := s.csChan(c, argAt(args, 0), store.LevelSOP)
	if !ok {
		return
	}
	live := s.net.Channel(reg.Name)
	if live == nil {
		s.cs(c, "%s is empty right now.", reg.Name)
		return
	}
	var changes []modeChange
	what := strings.ToUpper(argAt(args, 1))
	switch what {
	case "BANS":
		for _, e := range live.Lists['b'] {
			changes = append(changes, modeChange{false, 'b', e.Mask})
		}
	case "OPS", "VOICES":
		for _, m := range live.SortedMembers() {
			if what == "OPS" && m.Op {
				changes = append(changes, modeChange{false, 'o', m.User.Nick})
			}
			if what == "VOICES" && m.Voice {
				changes = append(changes, modeChange{false, 'v', m.User.Nick})
			}
		}
	case "MODES":
		for m, on := range live.Modes {
			if on && m != 'r' && m != 'k' && m != 'l' {
				changes = append(changes, modeChange{false, m, ""})
			}
		}
		if live.Modes['k'] {
			changes = append(changes, modeChange{false, 'k', live.Key})
		}
		if live.Modes['l'] {
			changes = append(changes, modeChange{false, 'l', ""})
		}
	default:
		s.cs(c, "Syntax: CLEAR #chan BANS|MODES|OPS|VOICES")
		return
	}
	s.csMode(live, changes)
	if what == "MODES" {
		s.csMode(live, mlockChanges(reg, live))
	}
	s.cs(c, "Cleared %s in %s (%d).", strings.ToLower(what), reg.Name, len(changes))
}

func csSet(s *Server, c *Client, args []string) {
	what := strings.ToUpper(argAt(args, 1))
	need := store.LevelSOP
	if what == "FOUNDER" || what == "SUCCESSOR" {
		need = store.LevelFounder
	}
	reg, _, ok := s.csChan(c, argAt(args, 0), need)
	if !ok {
		return
	}
	val := strings.Join(args[min(2, len(args)):], " ")
	save := func(format string, a ...any) {
		if err := s.store.SaveChannel(reg); err != nil {
			s.dbErr(c, s.chanServ, err)
			return
		}
		s.cs(c, format, a...)
	}
	toggle := func(field *bool, label string) {
		on, ok := onOff(val)
		if !ok {
			s.cs(c, "Syntax: SET %s %s ON|OFF", reg.Name, what)
			return
		}
		*field = on
		state := "off"
		if on {
			state = "on"
		}
		save("%s is now %s for %s.", label, state, reg.Name)
	}
	switch what {
	case "DESC":
		if val == "" {
			s.cs(c, "Syntax: SET %s DESC <text>", reg.Name)
			return
		}
		reg.Description = irc.Truncate(val, 200)
		save("The description of %s is now: %s", reg.Name, reg.Description)
	case "FOUNDER", "SUCCESSOR":
		if what == "SUCCESSOR" && strings.EqualFold(val, "OFF") {
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
			reg.SuccessorID, reg.Successor = target.ID, target.Name
			save("%s gets %s if %s's account goes.", target.Name, reg.Name, reg.Founder)
			return
		}
		if _, err := s.store.RemoveAccess(reg, target.ID); err != nil {
			s.dbErr(c, s.chanServ, err)
			return
		}
		reg.FounderID, reg.Founder = target.ID, target.Name
		if reg.SuccessorID == target.ID {
			reg.SuccessorID, reg.Successor = 0, ""
		}
		save("The founder of %s is now %s.", reg.Name, target.Name)
	case "MLOCK":
		if problem := parseMlock(reg, args[min(2, len(args)):]); problem != "" {
			s.cs(c, "%s", problem)
			return
		}
		save("The mode lock of %s is now %s.", reg.Name, mlockString(reg))
		if live := s.net.Channel(reg.Name); live != nil {
			s.csMode(live, mlockChanges(reg, live))
		}
	case "KEEPTOPIC":
		toggle(&reg.KeepTopic, "Keep topic")
	case "TOPICLOCK":
		toggle(&reg.TopicLock, "Topic lock")
	case "SECURE":
		toggle(&reg.Secure, "Secure")
	case "PRIVATE":
		toggle(&reg.Private, "Private")
	case "ENTRYMSG", "URL":
		field, label := &reg.EntryMsg, "entry message"
		if what == "URL" {
			field, label = &reg.URL, "URL"
		}
		switch {
		case val == "":
			s.cs(c, "Syntax: SET %s %s <text>|OFF", reg.Name, what)
		case strings.EqualFold(val, "OFF"):
			*field = ""
			save("%s has no %s now.", reg.Name, label)
		default:
			*field = irc.Truncate(val, 300)
			save("The %s of %s is now: %s", label, reg.Name, *field)
		}
	default:
		s.cs(c, "Syntax: SET #chan DESC|FOUNDER|SUCCESSOR|MLOCK|KEEPTOPIC|TOPICLOCK|SECURE|PRIVATE|ENTRYMSG|URL ...")
	}
}

// parseMlock reads "+ntk-s secret" into reg's lock fields. It returns what
// was wrong, or "".
func parseMlock(reg *store.Channel, args []string) string {
	if len(args) == 0 || args[0] == "" {
		return "Syntax: SET #chan MLOCK <modes>, like +nt-s or +ntk secret"
	}
	on, off := map[byte]bool{}, map[byte]bool{}
	key, limit := "", 0
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
			if key = cleanKey(next()); key == "" {
				return "+k in a mode lock needs a key: SET #chan MLOCK +k <key>"
			}
			delete(off, 'k')
		case m == 'l' && add:
			n, err := strconv.Atoi(next())
			if err != nil || n < 1 {
				return "+l in a mode lock needs a number: SET #chan MLOCK +l 50"
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
		case strings.IndexByte(mlockFlags, m) >= 0:
			if add {
				on[m], off[m] = true, false
			} else {
				off[m], on[m] = true, false
			}
		default:
			return fmt.Sprintf("A mode lock can hold %s, k and l, not %c.", mlockFlags, m)
		}
	}
	reg.MlockOn, reg.MlockOff, reg.MlockKey, reg.MlockLimit = flagString(on), flagString(off), key, limit
	return ""
}

func flagString(m map[byte]bool) string {
	var b []byte
	for _, f := range []byte(mlockFlags + "kl") {
		if m[f] {
			b = append(b, f)
		}
	}
	return string(b)
}

// mlockString shows a mode lock like "+ntk-s" (the key itself stays hidden).
func mlockString(reg *store.Channel) string {
	on := reg.MlockOn
	if reg.MlockKey != "" {
		on += "k"
	}
	if reg.MlockLimit > 0 {
		on += "l " + strconv.Itoa(reg.MlockLimit)
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
		case m == 'l' && ch.Modes['l']:
			out = append(out, modeChange{false, 'l', ""})
		case m != 'k' && m != 'l' && ch.Modes[m]:
			out = append(out, modeChange{false, m, ""})
		}
	}
	if reg.MlockKey != "" && (!ch.Modes['k'] || ch.Key != reg.MlockKey) {
		out = append(out, modeChange{true, 'k', reg.MlockKey})
	}
	if reg.MlockLimit > 0 && (!ch.Modes['l'] || ch.Limit != reg.MlockLimit) {
		out = append(out, modeChange{true, 'l', strconv.Itoa(reg.MlockLimit)})
	}
	return out
}

func csList(s *Server, c *Client, args []string) {
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
		if reg.Private && !c.user.HasMode('o') {
			if lvl, _ := s.store.Level(reg, c.accountID); lvl == store.LevelNone {
				continue
			}
		}
		n++
		if n <= 100 {
			s.cs(c, "  %-20s %s", reg.Name, reg.Description)
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

// csMode sets modes on ch as ChanServ. Changes that change nothing are
// skipped; the rest go out in lines of at most 4 parameters.
func (s *Server) csMode(ch *state.Channel, changes []modeChange) {
	now := time.Now()
	var applied []modeChange
	for _, m := range changes {
		switch m.mode {
		case 'o', 'v':
			t := s.net.UserByNick(m.arg)
			if t == nil || ch.Member(t) == nil {
				continue
			}
			tm := ch.Member(t)
			flag := &tm.Op
			if m.mode == 'v' {
				flag = &tm.Voice
			}
			if *flag == m.add {
				continue
			}
			*flag = m.add
			m.arg = t.Nick
		case 'b':
			if m.add {
				if !ch.AddListEntry('b', m.arg, "ChanServ", now) {
					continue
				}
			} else {
				stored, ok := ch.RemoveListEntry('b', m.arg)
				if !ok {
					continue
				}
				m.arg = stored
			}
		case 'k':
			if m.add {
				if ch.Modes['k'] && ch.Key == m.arg {
					continue
				}
				ch.Key, ch.Modes['k'] = m.arg, true
			} else {
				if !ch.Modes['k'] {
					continue
				}
				m.arg = ch.Key
				ch.Key = ""
				delete(ch.Modes, 'k')
			}
		case 'l':
			if m.add {
				n, _ := strconv.Atoi(m.arg)
				if ch.Modes['l'] && ch.Limit == n {
					continue
				}
				ch.Limit, ch.Modes['l'] = n, true
			} else {
				if !ch.Modes['l'] {
					continue
				}
				ch.Limit = 0
				delete(ch.Modes, 'l')
				m.arg = ""
			}
		default:
			if ch.Modes[m.mode] == m.add {
				continue
			}
			setFlag(ch.Modes, m.mode, m.add)
		}
		applied = append(applied, m)
	}
	for len(applied) > 0 {
		n, params := 0, 0
		for n < len(applied) {
			if applied[n].arg != "" {
				if params == maxModeParams {
					break
				}
				params++
			}
			n++
		}
		batch := applied[:n]
		applied = applied[n:]
		s.broadcast(ch, s.out(s.chanServ.user, "MODE", append([]string{ch.Name}, formatModes(batch)...)...), nil)
	}
}

// csAutoModes gives the modes a level earns.
func (s *Server) csAutoModes(ch *state.Channel, u *state.User, lvl int) {
	switch {
	case lvl >= store.LevelAOP:
		s.csMode(ch, []modeChange{{true, 'o', u.Nick}})
	case lvl == store.LevelVOP:
		s.csMode(ch, []modeChange{{true, 'v', u.Nick}})
	}
}

// csSaveTopic keeps the topic for KEEPTOPIC.
func (s *Server) csSaveTopic(reg *store.Channel, ch *state.Channel) {
	if !reg.KeepTopic {
		return
	}
	reg.Topic, reg.TopicBy, reg.TopicAt = ch.Topic, ch.TopicBy, ch.TopicAt
	if err := s.store.SaveChannel(reg); err != nil {
		s.log.Printf("services database: %v", err)
	}
}

// channelInUse reports whether someone on reg's access list is in it now.
func (s *Server) channelInUse(reg *store.Channel) bool {
	live := s.net.Channel(reg.Name)
	if live == nil {
		return false
	}
	for _, m := range live.Members {
		if s.levelOf(reg, m.User) > store.LevelNone {
			return true
		}
	}
	return false
}
