package services

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
		who := "SOPs and up"
		switch l {
		case store.LevelQOP:
			who = "Only the founder"
		case store.LevelSOP:
			who = "QOPs and the founder"
		}
		extra := ""
		if l == store.LevelQOP || l == store.LevelHOP {
			extra = "\nOnly on servers with ~&@%+ prefixes."
		}
		return svcCmd{n + " #chan ADD|DEL|LIST|CLEAR [nick]", "edit the " + n + " list",
			fmt.Sprintf("%[1]s #chan ADD <nick> [description]   adding someone on another level moves them\n%[1]s #chan DEL {<nick> | <num> | <list>}   numbers from LIST, like 2 or 1-3,5\n%[1]s #chan LIST [<pattern> | <list>]\n%[1]s #chan CLEAR\n%[2]s can change this list; VOP and up can see it.%[3]s\nExample: /cs %[4]s #bchat add ember helps with the wiki", n, who, extra, strings.ToLower(n)),
			csLevelList(l)}
	}
	return map[string]svcCmd{
		"REGISTER": {"REGISTER #chan [description]", "register a channel you op in",
			"Registers a channel to your account; you become its Founder. You need to be\nidentified and a channel operator in it.\nExample: /cs register #bchat Be kind, stay curious", csRegister},
		"DROP": {"DROP #chan [code]", "unregister a channel (founder)",
			"Removes the registration with its access list and AKICKs. You are asked to\nrepeat it with a code first. Opers with flag s may drop any channel.", csDrop},
		"INFO": {"INFO #chan", "who owns a channel and its settings",
			"Shows the founder, successor, description, mode lock and options.", csInfo},
		"QOP":    level(store.LevelQOP),
		"SOP":    level(store.LevelSOP),
		"HOP":    level(store.LevelHOP),
		"AOP":    level(store.LevelAOP),
		"VOP":    level(store.LevelVOP),
		"UOP":    level(store.LevelUOP),
		"ACCESS": {"ACCESS #chan LIST", "every level in one list", "Shows the founder and everyone on the access list. VOP and up can see it.", csAccess},
		"AKICK": {"AKICK #chan ADD|DEL|LIST|VIEW|ENFORCE|CLEAR ...", "keep someone out for good",
			"AKICK #chan ADD {<nick> | <mask>} [reason]\nAKICK #chan DEL {<mask> | <num> | <list>}   numbers from LIST, like 2 or 1-3,5\nAKICK #chan LIST [<mask> | <list>]\nAKICK #chan VIEW [<mask> | <list>]        with who added it and when\nAKICK #chan ENFORCE   kick everyone in the channel who matches now\nAKICK #chan CLEAR\nAnyone matching an AKICK is banned and kicked when they join, unless they are\non the access list. SOP and up.\nExample: /cs akick #bchat add *!*@*.badhost.example spamming", csAkick},
		"OP":        {"OP #chan [nick]", "op someone (or yourself)", "AOP and up. With SECURE on, only AOP and up can be opped.", csModeCmd('o', true)},
		"DEOP":      {"DEOP #chan [nick]", "deop someone (or yourself)", "AOP and up can deop others; anyone can deop themself.", csModeCmd('o', false)},
		"VOICE":     {"VOICE #chan [nick]", "voice someone (or yourself)", "HOP and up can voice others; VOP and up can voice themself.", csModeCmd('v', true)},
		"DEVOICE":   {"DEVOICE #chan [nick]", "devoice someone (or yourself)", "HOP and up can devoice others; anyone can devoice themself.", csModeCmd('v', false)},
		"HALFOP":    {"HALFOP #chan [nick]", "halfop someone (or yourself)", "AOP and up; HOP and up for themself. Only with ~&@%+ prefixes.", csModeCmd('h', true)},
		"DEHALFOP":  {"DEHALFOP #chan [nick]", "take halfop away", "AOP and up can dehalfop others; anyone can dehalfop themself.", csModeCmd('h', false)},
		"PROTECT":   {"PROTECT #chan [nick]", "give admin (&) status", "QOP and up; SOP and up for themself. Only with ~&@%+ prefixes.", csModeCmd('a', true)},
		"DEPROTECT": {"DEPROTECT #chan [nick]", "take admin (&) status away", "QOP and up can take it from others; anyone from themself.", csModeCmd('a', false)},
		"OWNER":     {"OWNER #chan [nick]", "give owner (~) status", "The founder; QOP and up for themself. Only with ~&@%+ prefixes.", csModeCmd('q', true)},
		"DEOWNER":   {"DEOWNER #chan [nick]", "take owner (~) status away", "The founder can take it from others; anyone from themself.", csModeCmd('q', false)},
		"UP":        {"UP #chan", "give yourself every status you earn", "Gives you the modes your level earns, like when you join.", csUp},
		"DOWN":      {"DOWN #chan", "drop all your channel status", "Takes every status mode you have in the channel.", csDown},
		"KICK":      {"KICK #chan <nick> [reason]", "kick through ChanServ", "AOP and up. Nobody can kick someone on the same level or above, except the founder.", csKick},
		"BAN":       {"BAN #chan <nick|mask>", "ban and kick", "AOP and up. A nick bans that person's host.", csBan},
		"UNBAN":     {"UNBAN #chan [nick|mask]", "remove bans", "Without a nick, removes every ban on you (UOP and up). AOP and up can unban\nothers.", csUnban},
		"INVITE":    {"INVITE #chan [nick]", "invite yourself or someone", "UOP and up invite themself past +i, +k and +l; AOP and up may invite others.", csInvite},
		"TOPIC":     {"TOPIC #chan <text>", "set the topic", "AOP and up. Works even with TOPICLOCK on.", csTopic},
		"CLEAR":     {"CLEAR #chan BANS|MODES|OPS|VOICES", "clean up a channel", "SOP and up. MODES puts the mode lock back afterwards.", csClear},
		"SET": {"SET <option> #chan <value>", "change channel settings",
			"SET DESCRIPTION #chan <text>\nSET FOUNDER #chan <nick>            (founder)\nSET SUCCESSOR #chan <nick>|OFF     (founder) gets the channel if the founder's\n                                    account goes\nSET MLOCK #chan <modes>            like +nt-s or +ntk secret\nSET KEEPTOPIC #chan ON|OFF         put the topic back when the channel is reopened (on)\nSET TOPICLOCK #chan ON|OFF         only AOP and up may change the topic\nSET SECURE #chan ON|OFF            only AOP and up may be opped\nSET PRIVATE #chan ON|OFF           hide from /cs list and /cs info for outsiders\nSET ENTRYMSG #chan <text>|OFF      a notice to everyone who joins\nSET URL #chan <url>|OFF\nEverything but FOUNDER and SUCCESSOR: SOP and up. Opers with flag s may set\nthe FOUNDER of any channel. SET #chan <option> works too.", csSet},
		"LIST": {"LIST <pattern>", "search registered channels", "Lists registered channels matching a pattern, like #bee*. Private ones are\nleft out unless you are on their access list.", csList},
	}
}

func (s *Services) cs(c *Client, format string, a ...any) {
	s.svcReply(c, s.chanServ, fmt.Sprintf(format, a...))
}

// csChan loads a registered channel and c's level on it. With need above
// zero, c must be identified and at least that level.
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

func (s *Services) csChan(c *Client, name string, need int) (*store.Channel, int, bool) {
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
func (s *Services) levelOf(reg *store.Channel, u *state.User) int {
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

func csRegister(s *Services, c *Client, args []string) {
	u := c.user
	name := argAt(args, 0)
	if c.accountID == 0 {
		s.cs(c, "Identify first: /ns identify <password>")
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
	s.csMode(live, mlockChanges(reg, live))
	s.csAutoModes(live, u, store.LevelFounder)
}

func csDrop(s *Services, c *Client, args []string) {
	reg, asOper, ok := s.csChanOper(c, argAt(args, 0))
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
	s.log.Printf("dropped channel %s (by %s)", reg.Name, c.user.Nick)
	if asOper {
		s.audit(c, s.chanServ, "DROP %s (founder %s)", reg.Name, reg.Founder)
	}
	s.cs(c, "%s is no longer registered.", reg.Name)
}

func csInfo(s *Services, c *Client, args []string) {
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
func csLevelList(level int) func(*Services, *Client, []string) {
	n := store.LevelName(level)
	return func(s *Services, c *Client, args []string) {
		name, sub, arg := argAt(args, 0), strings.ToUpper(argAt(args, 1)), argAt(args, 2)
		if (level == store.LevelQOP || level == store.LevelHOP) && !s.cfg.FullPrefixes() {
			s.cs(c, "%s lists are only used on servers with ~&@%%+ prefixes; this one uses @+.", n)
			return
		}
		need := store.LevelSOP
		switch level {
		case store.LevelQOP:
			need = store.LevelFounder
		case store.LevelSOP:
			need = store.LevelFounder
			if s.cfg.FullPrefixes() {
				need = store.LevelQOP
			}
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
		entries, err := s.store.AccessList(reg, level)
		if err != nil {
			s.dbErr(c, s.chanServ, err)
			return
		}
		switch sub {
		case "ADD":
			if arg == "" {
				s.cs(c, "Syntax: %s %s ADD <nick> [description]", n, reg.Name)
				return
			}
			target, ok := s.csAccessTarget(c, reg, lvl, arg)
			if !ok {
				return
			}
			old, _ := s.store.Level(reg, target.ID)
			if old == level {
				s.cs(c, "%s is on the %s list of %s already.", target.Name, n, reg.Name)
				return
			}
			note := irc.Truncate(strings.Join(args[3:], " "), 100)
			if _, err := s.store.SetLevel(reg, target, level, c.user.Account, note, time.Now()); err != nil {
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
		case "DEL":
			if arg == "" {
				s.cs(c, "Syntax: %s %s DEL {<nick> | <num> | <list>}", n, reg.Name)
				return
			}
			var gone []store.AccessEntry
			if nums, isList := numList(arg, len(entries)); isList {
				for _, i := range nums {
					gone = append(gone, entries[i-1])
				}
				if len(gone) == 0 {
					s.cs(c, "No entry %s on the %s list of %s.", arg, n, reg.Name)
					return
				}
			} else {
				target, ok := s.csAccessTarget(c, reg, lvl, arg)
				if !ok {
					return
				}
				for _, e := range entries {
					if e.AccountID == target.ID {
						gone = append(gone, e)
					}
				}
				if len(gone) == 0 {
					s.cs(c, "%s isn't on the %s list of %s.", target.Name, n, reg.Name)
					return
				}
			}
			var names []string
			for _, e := range gone {
				if level >= lvl && lvl != store.LevelFounder {
					s.cs(c, "You can't change %s: their level is the same as or above yours.", e.Account)
					continue
				}
				if _, err := s.store.RemoveAccess(reg, e.AccountID); err != nil {
					s.dbErr(c, s.chanServ, err)
					return
				}
				names = append(names, e.Account)
			}
			if len(names) > 0 {
				s.cs(c, "%s removed from the %s list of %s.", strings.Join(names, ", "), n, reg.Name)
			}
		case "LIST":
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
			if level >= store.LevelSOP && lvl < need {
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

// csAccessTarget finds the account an access list change is about and
// checks that c (at level lvl) may change it.
func (s *Services) csAccessTarget(c *Client, reg *store.Channel, lvl int, nick string) (*store.Account, bool) {
	target, err := s.store.AccountByNick(nick)
	if err != nil {
		s.dbErr(c, s.chanServ, err)
		return nil, false
	}
	if target == nil {
		s.cs(c, "%s isn't a registered nick.", nick)
		return nil, false
	}
	if target.ID == reg.FounderID {
		s.cs(c, "%s is the founder of %s.", target.Name, reg.Name)
		return nil, false
	}
	old, err := s.store.Level(reg, target.ID)
	if err != nil {
		s.dbErr(c, s.chanServ, err)
		return nil, false
	}
	if old >= lvl && lvl != store.LevelFounder {
		s.cs(c, "You can't change %s: their level is the same as or above yours.", target.Name)
		return nil, false
	}
	return target, true
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

func csAccess(s *Services, c *Client, args []string) {
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
	also := map[int]string{
		store.LevelFounder: "everything",
		store.LevelQOP:     "add/remove SOP and below · akick",
		store.LevelSOP:     "add/remove AOP and below · akick",
		store.LevelAOP:     "kick, ban, topic, invite",
		store.LevelHOP:     "kick and voice people below them",
		store.LevelVOP:     "voice themself",
		store.LevelUOP:     "invite and unban themself",
	}
	row := func(l int, nick string) {
		gets := s.levelPrefixes(l)
		if gets == "" {
			gets = "–"
		}
		s.cs(c, "  %-8s %-*s  %-5s  %s", store.LevelName(l), width, nick, gets, also[l])
	}
	s.cs(c, "Access list of %s", reg.Name)
	s.cs(c, "  %-8s %-*s  %-5s  %s", "Level", width, "Nick", "Gets", "Can also")
	row(store.LevelFounder, reg.Founder)
	for _, e := range entries {
		row(e.Level, e.Account)
	}
	s.cs(c, "%s. Change one: /cs aop %s del <nick>", plural(len(entries)+1, "entry"), reg.Name)
}

func csAkick(s *Services, c *Client, args []string) {
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
		mask := irc.NormalizeMask(arg) // a nick becomes nick!*@*
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
		s.akickEnforce(reg, []store.Akick{{Mask: mask, Reason: reason}})
	case sub == "DEL":
		var masks []string
		if nums, isList := numList(arg, len(list)); isList {
			for _, i := range nums {
				masks = append(masks, list[i-1].Mask)
			}
		} else {
			masks = []string{irc.NormalizeMask(arg)}
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
		s.cs(c, "Removed %s from the AKICK list of %s.", strings.Join(removed, ", "), reg.Name)
	case sub == "ENFORCE":
		n := s.akickEnforce(reg, list)
		s.cs(c, "AKICK enforced on %s: %s.", reg.Name, plural(n, "person")+" kicked")
	case sub == "CLEAR":
		for _, k := range list {
			if _, err := s.store.DelAkick(reg, k.Mask); err != nil {
				s.dbErr(c, s.chanServ, err)
				return
			}
		}
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
		if m.User.Service || s.levelOf(reg, m.User) != store.LevelNone {
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

func matchesMask(mask string, u *state.User) bool {
	for _, m := range u.Masks() {
		if irc.Match(mask, m) {
			return true
		}
	}
	return false
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
		need := csModeNeed(mode, self)
		if self && !add {
			need = 0
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
		if add && reg.Secure && tl < csSecureLevel(mode) {
			s.cs(c, "%s isn't %s or higher on %s, and SECURE is on.", t.Nick, store.LevelName(csSecureLevel(mode)), reg.Name)
			return
		}
		if !add && !self && tl > lvl && lvl != store.LevelFounder {
			s.cs(c, "You can't do that to %s: their level is above yours.", t.Nick)
			return
		}
		s.csMode(live, []modeChange{{add, mode, t.Nick}})
	}
}

func csKick(s *Services, c *Client, args []string) {
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
func (s *Services) csKickUser(ch *state.Channel, u *state.User, reason string) {
	s.svcKick(s.chanServ, ch, u, reason)
}

// svcKick kicks u from ch as a service.
func (s *Services) svcKick(sv *service, ch *state.Channel, u *state.User, reason string) {
	s.emit(&state.Kick{Source: sv.user.UID, UID: u.UID, Channel: ch.Name, Reason: irc.Truncate(reason, s.cfg.Limits.KickLen)})
}

func csBan(s *Services, c *Client, args []string) {
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

func csUnban(s *Services, c *Client, args []string) {
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

func csInvite(s *Services, c *Client, args []string) {
	nick := argAt(args, 1)
	need := store.LevelUOP
	if nick != "" && !irc.EqualFold(nick, c.user.Nick) {
		need = store.LevelAOP
	}
	reg, _, ok := s.csChan(c, argAt(args, 0), need)
	if !ok {
		return
	}
	who := c.user
	if need == store.LevelAOP {
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
		s.emit(&state.Invite{Source: s.chanServ.user.UID, UID: who.UID, Channel: live.Name})
		if who != c.user {
			s.cs(c, "%s is invited to %s.", who.Nick, reg.Name)
		}
	}
}

func csTopic(s *Services, c *Client, args []string) {
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
	s.emit(&state.Topic{Source: s.chanServ.user.UID, Channel: live.Name, Text: topic, By: c.user.Nick, At: time.Now()})
	s.csSaveTopic(reg, live)
}

func csClear(s *Services, c *Client, args []string) {
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
			for _, r := range state.Ranks {
				if !m.Has(r.Rank) || (what == "OPS") == (r.Rank == state.RankVoice) {
					continue
				}
				changes = append(changes, modeChange{false, r.Mode, m.User.Nick})
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

func csSet(s *Services, c *Client, args []string) {
	// Anope's order is SET <option> #chan; ours used to be SET #chan <option>.
	// Both work: the word starting with # is the channel.
	if len(args) >= 2 && !irc.IsChannel(args[0]) && irc.IsChannel(args[1]) {
		args = append([]string{args[1], args[0]}, args[2:]...)
	}
	what := strings.ToUpper(argAt(args, 1))
	if what == "DESC" {
		what = "DESCRIPTION"
	}
	var reg *store.Channel
	var ok, asOper bool
	switch what {
	case "FOUNDER":
		reg, asOper, ok = s.csChanOper(c, argAt(args, 0))
	case "SUCCESSOR":
		reg, _, ok = s.csChan(c, argAt(args, 0), store.LevelFounder)
	case "":
		s.cs(c, "Syntax: SET <option> #chan <value> (see /cs help set)")
		return
	default:
		reg, _, ok = s.csChan(c, argAt(args, 0), store.LevelSOP)
	}
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
			s.cs(c, "Syntax: SET %s %s ON|OFF", what, reg.Name)
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
	case "DESCRIPTION":
		if val == "" {
			s.cs(c, "Syntax: SET DESCRIPTION %s <text>", reg.Name)
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
		old := reg.Founder
		reg.FounderID, reg.Founder = target.ID, target.Name
		if reg.SuccessorID == target.ID {
			reg.SuccessorID, reg.Successor = 0, ""
		}
		if asOper {
			s.audit(c, s.chanServ, "SET FOUNDER %s %s (was %s)", reg.Name, target.Name, old)
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
			s.cs(c, "Syntax: SET %s %s <text>|OFF", what, reg.Name)
		case strings.EqualFold(val, "OFF"):
			*field = ""
			save("%s has no %s now.", reg.Name, label)
		default:
			*field = irc.Truncate(val, 300)
			save("The %s of %s is now: %s", label, reg.Name, *field)
		}
	default:
		s.cs(c, "Syntax: SET DESCRIPTION|FOUNDER|SUCCESSOR|MLOCK|KEEPTOPIC|TOPICLOCK|SECURE|PRIVATE|ENTRYMSG|URL #chan ...")
	}
}

// parseMlock reads "+ntk-s secret" into reg's lock fields. It returns what
// was wrong, or "".
func parseMlock(reg *store.Channel, args []string) string {
	if len(args) == 0 || args[0] == "" {
		return "Syntax: SET MLOCK #chan <modes>, like +nt-s or +ntk secret"
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
			if key = irc.CleanKey(next()); key == "" {
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
// dropped when applied; the rest go out in lines of at most 4 parameters.
func (s *Services) csMode(ch *state.Channel, changes []modeChange) {
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
		s.emit(&state.ChannelMode{Source: s.chanServ.user.UID, Channel: ch.Name, By: "ChanServ", At: time.Now(), Changes: list})
	}
}

// levelModes are the status modes a level earns, highest first. With @+
// prefixes, owner and admin levels get @ and HOP gets +.
func (s *Services) levelModes(lvl int) string {
	full := s.cfg.FullPrefixes()
	switch {
	case lvl >= store.LevelQOP && full:
		return "qo"
	case lvl >= store.LevelSOP && full:
		return "ao"
	case lvl >= store.LevelAOP:
		return "o"
	case lvl >= store.LevelHOP && full:
		return "h"
	case lvl >= store.LevelVOP:
		return "v"
	}
	return ""
}

// levelPrefixes is levelModes as prefixes, like "~@".
func (s *Services) levelPrefixes(lvl int) string {
	var b []byte
	for _, m := range []byte(s.levelModes(lvl)) {
		for _, r := range state.Ranks {
			if r.Mode == m {
				b = append(b, r.Prefix)
			}
		}
	}
	return string(b)
}

// csAutoModes gives the modes a level earns.
func (s *Services) csAutoModes(ch *state.Channel, u *state.User, lvl int) {
	var changes []modeChange
	for _, m := range []byte(s.levelModes(lvl)) {
		changes = append(changes, modeChange{true, m, u.Nick})
	}
	s.csMode(ch, changes)
}

// csModeNeed is the level needed to give mode to someone (self false) or
// to yourself.
func csModeNeed(mode byte, self bool) int {
	switch mode {
	case 'q':
		if self {
			return store.LevelQOP
		}
		return store.LevelFounder
	case 'a':
		if self {
			return store.LevelSOP
		}
		return store.LevelQOP
	case 'h':
		if self {
			return store.LevelHOP
		}
		return store.LevelAOP
	case 'v':
		if self {
			return store.LevelVOP
		}
		return store.LevelHOP
	}
	return store.LevelAOP
}

// csSecureLevel is the level SECURE asks of someone given mode.
func csSecureLevel(mode byte) int {
	switch mode {
	case 'q':
		return store.LevelQOP
	case 'a':
		return store.LevelSOP
	case 'h':
		return store.LevelHOP
	case 'v':
		return store.LevelNone
	}
	return store.LevelAOP
}

func csUp(s *Services, c *Client, args []string) {
	reg, lvl, ok := s.csChan(c, argAt(args, 0), store.LevelUOP)
	if !ok {
		return
	}
	live, _, ok := s.liveTarget(c, reg, c.user.Nick)
	if !ok {
		return
	}
	if s.levelModes(lvl) == "" {
		s.cs(c, "Your level on %s (%s) earns no status.", reg.Name, store.LevelName(lvl))
		return
	}
	s.csAutoModes(live, c.user, lvl)
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
		if s.levelOf(reg, m.User) > store.LevelNone {
			return true
		}
	}
	return false
}
