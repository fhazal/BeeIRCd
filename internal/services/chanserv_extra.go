package services

import (
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/fhazal/beeircd/internal/irc"
	"github.com/fhazal/beeircd/internal/state"
	"github.com/fhazal/beeircd/internal/store"
)

// ChanServ commands added in step 1.5.4 (Anope's MODE, TOPIC, ENTRYMSG,
// ENFORCE, LOG, SUSPEND, SYNC) and what PERSIST and KEEPMODES need.

func csModeCommand(s *Services, c *Client, args []string) {
	reg, _, ok := s.csPriv(c, argAt(args, 0), "MODE")
	if !ok {
		return
	}
	sub := strings.ToUpper(argAt(args, 1))
	switch {
	case sub == "LOCK":
		switch op := strings.ToUpper(argAt(args, 2)); op {
		case "ADD", "SET":
			s.mlockSet(c, reg, op, args[min(3, len(args)):])
		case "DEL":
			if argAt(args, 3) == "" {
				s.cs(c, "Syntax: MODE %s LOCK DEL <modes>", reg.Name)
				return
			}
			if problem := unlockModes(reg, args[3]); problem != "" {
				s.cs(c, "%s", problem)
				return
			}
			if err := s.store.SaveChannel(reg); err != nil {
				s.dbErr(c, s.chanServ, err)
				return
			}
			s.csUsed(c, reg)
			s.csLog(c, reg.Name, false, "to unlock %s", args[3])
			s.cs(c, "The mode lock of %s is now %s.", reg.Name, mlockString(reg))
		case "", "LIST":
			s.cs(c, "The mode lock of %s is %s.", reg.Name, mlockString(reg))
		default:
			s.cs(c, "Syntax: MODE %s LOCK ADD|DEL|SET|LIST <modes>", reg.Name)
		}
	case sub == "SET" || strings.HasPrefix(sub, "+") || strings.HasPrefix(sub, "-"):
		modes := args[1:]
		if sub == "SET" {
			modes = args[min(2, len(args)):]
		}
		if len(modes) == 0 {
			s.cs(c, "Syntax: MODE %s SET <modes>", reg.Name)
			return
		}
		live := s.net.Channel(reg.Name)
		if live == nil {
			s.cs(c, "%s is empty right now.", reg.Name)
			return
		}
		s.csUsed(c, reg)
		s.csLog(c, reg.Name, false, "to set %s", strings.Join(modes, " "))
		s.host.ApplyChannelModes(c.user.UID, s.chanServ.user.UID, live.Name, modes[0], modes[1:])
		if live = s.net.Channel(reg.Name); live != nil {
			s.csMode(live, mlockChanges(reg, live))
		}
	case sub == "CLEAR":
		s.modeClear(c, reg, strings.ToUpper(argAt(args, 2)))
	default:
		s.cs(c, "Syntax: MODE #chan LOCK|SET|CLEAR ... (see /cs help mode)")
	}
}

// mlockSet is MODE LOCK ADD or SET (and the old SET MLOCK).
func (s *Services) mlockSet(c *Client, reg *store.Channel, op string, modes []string) {
	if problem := parseMlock(reg, modes, op == "ADD"); problem != "" {
		s.cs(c, "%s", problem)
		return
	}
	if err := s.store.SaveChannel(reg); err != nil {
		s.dbErr(c, s.chanServ, err)
		return
	}
	s.csUsed(c, reg)
	s.csLog(c, reg.Name, false, "to lock %s", strings.Join(modes, " "))
	s.cs(c, "The mode lock of %s is now %s.", reg.Name, mlockString(reg))
	if live := s.net.Channel(reg.Name); live != nil {
		s.csMode(live, mlockChanges(reg, live))
	}
}

// csClearOld is the ConferenceRoom CLEAR: OPS takes every status but
// voice, as it always did.
func csClearOld(s *Services, c *Client, args []string) {
	reg, _, ok := s.csPriv(c, argAt(args, 0), "MODE")
	if !ok {
		return
	}
	what := strings.ToUpper(argAt(args, 1))
	if what == "" {
		s.cs(c, "Syntax: CLEAR #chan BANS|MODES|OPS|VOICES (or /cs mode #chan clear ...)")
		return
	}
	if what == "OPS" {
		what = "STATUS"
	}
	s.modeClear(c, reg, what)
}

// clearRanks says which status modes each MODE CLEAR word takes.
var clearRanks = map[string]string{"OPS": "o", "HALFOPS": "h", "VOICES": "v", "PROTECTS": "a", "OWNERS": "q", "STATUS": "qaoh"}

// clearLists says which list modes each MODE CLEAR word empties.
var clearLists = map[string]byte{"BANS": 'b', "EXCEPTS": 'e', "INVITES": 'I'}

func (s *Services) modeClear(c *Client, reg *store.Channel, what string) {
	if what == "" {
		what = "ALL"
	}
	live := s.net.Channel(reg.Name)
	if live == nil {
		s.cs(c, "%s is empty right now.", reg.Name)
		return
	}
	var changes []modeChange
	ranks, lists, modes := "", "", false
	switch {
	case what == "ALL":
		ranks, lists, modes = "qaohv", "beI", true
	case what == "MODES":
		modes = true
	case clearRanks[what] != "":
		ranks = clearRanks[what]
	case clearLists[what] != 0:
		lists = string(clearLists[what])
	default:
		s.cs(c, "Syntax: MODE %s CLEAR [BANS|EXCEPTS|INVITES|OPS|HALFOPS|VOICES|PROTECTS|OWNERS|MODES|ALL]", reg.Name)
		return
	}
	for _, l := range []byte(lists) {
		for _, e := range live.Lists[l] {
			changes = append(changes, modeChange{false, l, e.Mask})
		}
	}
	if ranks != "" {
		for _, m := range live.SortedMembers() {
			if m.User.Service {
				continue
			}
			for _, r := range state.Ranks {
				if m.Has(r.Rank) && strings.IndexByte(ranks, r.Mode) >= 0 {
					changes = append(changes, modeChange{false, r.Mode, m.User.Nick})
				}
			}
		}
	}
	if modes {
		var flags []byte
		for m, on := range live.Modes {
			if on && m != 'r' && m != 'k' && m != 'l' {
				flags = append(flags, m)
			}
		}
		sort.Slice(flags, func(i, j int) bool { return flags[i] < flags[j] })
		for _, m := range flags {
			changes = append(changes, modeChange{false, m, ""})
		}
		if live.Modes['k'] {
			changes = append(changes, modeChange{false, 'k', live.Key})
		}
		if live.Modes['l'] {
			changes = append(changes, modeChange{false, 'l', ""})
		}
	}
	s.csUsed(c, reg)
	s.csLog(c, reg.Name, false, "to clear %s", strings.ToLower(what))
	s.csMode(live, changes)
	if modes {
		s.csMode(live, mlockChanges(reg, live))
	}
	word := strings.ToLower(what)
	if what == "STATUS" {
		word = "ops"
	}
	s.cs(c, "Cleared %s in %s (%d).", word, reg.Name, len(changes))
}

func csTopic(s *Services, c *Client, args []string) {
	name, sub := argAt(args, 0), strings.ToUpper(argAt(args, 1))
	if sub == "LOCK" || sub == "UNLOCK" {
		reg, _, ok := s.csPriv(c, name, "SET")
		if !ok {
			return
		}
		reg.TopicLock = sub == "LOCK"
		if err := s.store.SaveChannel(reg); err != nil {
			s.dbErr(c, s.chanServ, err)
			return
		}
		s.csUsed(c, reg)
		if reg.TopicLock {
			s.csLog(c, reg.Name, false, "to enable topiclock")
		} else {
			s.csLog(c, reg.Name, false, "to disable topiclock")
		}
		if reg.TopicLock {
			s.cs(c, "The topic of %s is locked: only those with TOPIC may change it.", reg.Name)
		} else {
			s.cs(c, "The topic of %s is unlocked.", reg.Name)
		}
		return
	}
	reg, _, ok := s.csPriv(c, name, "TOPIC")
	if !ok {
		return
	}
	if sub == "" {
		s.cs(c, "Syntax: TOPIC %s [SET|APPEND|PREPEND|LOCK|UNLOCK] [text]", reg.Name)
		return
	}
	live := s.net.Channel(reg.Name)
	if live == nil {
		s.cs(c, "%s is empty right now.", reg.Name)
		return
	}
	var topic string
	switch sub {
	case "SET":
		topic = strings.Join(args[2:], " ")
	case "APPEND", "PREPEND":
		text := strings.Join(args[2:], " ")
		if text == "" {
			s.cs(c, "Syntax: TOPIC %s %s <text>", reg.Name, sub)
			return
		}
		switch {
		case live.Topic == "":
			topic = text
		case sub == "APPEND":
			topic = live.Topic + " | " + text
		default:
			topic = text + " | " + live.Topic
		}
	default:
		topic = strings.Join(args[1:], " ")
	}
	topic = irc.Truncate(topic, s.cfg.Limits.TopicLen)
	s.csUsed(c, reg)
	if topic != "" {
		s.csLog(c, reg.Name, false, "to change the topic to: %s", topic)
	} else {
		s.csLog(c, reg.Name, false, "to unset the topic")
	}
	s.emit(&state.Topic{Source: s.chanServ.user.UID, Channel: live.Name, Text: topic, By: c.user.Nick, At: time.Now()})
	s.csSaveTopic(reg, live)
}

func csEntryMsg(s *Services, c *Client, args []string) {
	reg, _, ok := s.csPriv(c, argAt(args, 0), "SET")
	if !ok {
		return
	}
	lines := s.store.EntryMsgs(reg)
	switch sub := strings.ToUpper(argAt(args, 1)); sub {
	case "ADD":
		text := irc.Truncate(strings.Join(args[2:], " "), 300)
		if text == "" {
			s.cs(c, "Syntax: ENTRYMSG %s ADD <text>", reg.Name)
			return
		}
		if len(lines) >= maxEntryMsgs {
			s.cs(c, "The entry message of %s has %d lines already, the most it can.", reg.Name, maxEntryMsgs)
			return
		}
		if err := s.store.AddEntryMsg(reg, text, c.user.Account, time.Now()); err != nil {
			s.dbErr(c, s.chanServ, err)
			return
		}
		s.csUsed(c, reg)
		s.csLog(c, reg.Name, false, "to add a message")
		s.cs(c, "Line %d added to the entry message of %s.", len(lines)+1, reg.Name)
	case "DEL":
		nums, isList := numList(argAt(args, 2), len(lines))
		if !isList {
			s.cs(c, "Syntax: ENTRYMSG %s DEL {<num> | <list>}", reg.Name)
			return
		}
		if len(nums) == 0 {
			s.cs(c, "The entry message of %s has no line %s.", reg.Name, args[2])
			return
		}
		for _, n := range nums {
			if err := s.store.DelEntryMsg(reg, lines[n-1].ID); err != nil {
				s.dbErr(c, s.chanServ, err)
				return
			}
		}
		s.csUsed(c, reg)
		s.csLog(c, reg.Name, false, "to remove a message")
		s.cs(c, "%s removed from the entry message of %s.", plural(len(nums), "line"), reg.Name)
	case "", "LIST":
		if len(lines) == 0 {
			s.cs(c, "%s has no entry message.", reg.Name)
			return
		}
		s.cs(c, "Entry message of %s:", reg.Name)
		for i, e := range lines {
			s.cs(c, "  %-3d %s  (%s, %s)", i+1, e.Text, e.AddedBy, e.AddedAt.UTC().Format("2 Jan 2006"))
		}
	case "CLEAR":
		n, err := s.store.ClearEntryMsgs(reg)
		if err != nil {
			s.dbErr(c, s.chanServ, err)
			return
		}
		s.csUsed(c, reg)
		s.csLog(c, reg.Name, false, "to remove all messages")
		s.cs(c, "%s has no entry message now (%s removed).", reg.Name, plural(n, "line"))
	default:
		s.cs(c, "Syntax: ENTRYMSG #chan ADD|DEL|LIST|CLEAR ...")
	}
}

func csEnforce(s *Services, c *Client, args []string) {
	reg, _, ok := s.csPriv(c, argAt(args, 0), "AKICK")
	if !ok {
		return
	}
	what := strings.ToUpper(argAt(args, 1))
	live := s.net.Channel(reg.Name)
	if what != "" && live == nil {
		s.cs(c, "%s is empty right now.", reg.Name)
		return
	}
	n := 0
	switch what {
	case "SECUREOPS":
		n = s.stripStatus(reg, live)
		s.csUsed(c, reg)
		s.csLog(c, reg.Name, false, "to enforce secureops")
		s.cs(c, "SECUREOPS enforced on %s: %s.", reg.Name, plural(n, "status mode")+" taken")
		return
	case "RESTRICTED":
		n = s.enforceKick(reg, func(u *state.User) bool { return !s.userHas(reg, u, "NOKICK") }, true,
			"This channel is for its access list only.")
	case "REGONLY":
		n = s.enforceKick(reg, func(u *state.User) bool { return u.Account == "" }, false,
			"Identify to a registered nick to stay in "+reg.Name+".")
	case "BANS":
		n = s.enforceKick(reg, func(u *state.User) bool { return live.IsBanned(u) }, false, "Banned")
	case "LIMIT":
		if !live.HasMode('l') {
			s.cs(c, "%s has no limit (+l).", reg.Name)
			return
		}
		over := 0
		for _, m := range live.Members {
			if !m.User.Service {
				over++
			}
		}
		over -= live.Limit
		// The newest guess we have: people without status or access go first.
		members := live.SortedMembers()
		sort.SliceStable(members, func(i, j int) bool {
			return members[i].Ranks == 0 && s.userLevel(reg, members[i].User) == 0 &&
				!(members[j].Ranks == 0 && s.userLevel(reg, members[j].User) == 0)
		})
		for _, m := range members {
			if over <= 0 || s.net.Channel(reg.Name) == nil {
				break
			}
			if m.User.Service {
				continue
			}
			s.csKickUser(live, m.User, "The channel is full.")
			over--
			n++
		}
	default:
		s.cs(c, "Syntax: ENFORCE %s SECUREOPS|RESTRICTED|REGONLY|BANS|LIMIT", reg.Name)
		return
	}
	s.csUsed(c, reg)
	s.csLog(c, reg.Name, false, "to enforce %s", map[string]string{"RESTRICTED": "restricted", "REGONLY": "registered only", "BANS": "bans", "LIMIT": "limit"}[what])
	s.cs(c, "%s enforced on %s: %s kicked.", what, reg.Name, plural(n, "person"))
}

// enforceKick kicks (and with ban, bans) everyone in reg out matches.
func (s *Services) enforceKick(reg *store.Channel, out func(*state.User) bool, ban bool, reason string) int {
	live := s.net.Channel(reg.Name)
	if live == nil {
		return 0
	}
	n := 0
	for _, m := range live.SortedMembers() {
		if m.User.Service || !out(m.User) {
			continue
		}
		if ban {
			s.csMode(live, []modeChange{{true, 'b', banMask(reg, m.User)}})
		}
		s.csKickUser(live, m.User, reason)
		n++
		if s.net.Channel(reg.Name) == nil {
			break
		}
	}
	return n
}

// stripStatus takes every status (but voice) from people not allowed it.
func (s *Services) stripStatus(reg *store.Channel, live *state.Channel) int {
	var changes []modeChange
	for _, m := range live.SortedMembers() {
		if m.User.Service {
			continue
		}
		lvl := s.userLevel(reg, m.User)
		for _, r := range state.Ranks {
			if m.Has(r.Rank) && !s.secureAllows(reg, lvl, r.Mode) {
				changes = append(changes, modeChange{false, r.Mode, m.User.Nick})
			}
		}
	}
	s.csMode(live, changes)
	return len(changes)
}

func csSyncCmd(s *Services, c *Client, args []string) {
	reg, _, ok := s.csPriv(c, argAt(args, 0), "ACCESS_CHANGE")
	if !ok {
		return
	}
	if s.net.Channel(reg.Name) == nil {
		s.cs(c, "%s is empty right now.", reg.Name)
		return
	}
	s.csSync(reg)
	s.csUsed(c, reg)
	s.csLog(c, reg.Name, false, "")
	s.cs(c, "Everyone in %s has the status their level earns now.", reg.Name)
}

// csSync gives everyone in reg the modes they earn and, with SECUREOPS,
// takes status from those not allowed it.
func (s *Services) csSync(reg *store.Channel) {
	live := s.net.Channel(reg.Name)
	if live == nil {
		return
	}
	if reg.SecureOps {
		s.stripStatus(reg, live)
	}
	for _, m := range live.SortedMembers() {
		if !m.User.Service {
			s.csAutoModes(reg, live, m.User)
		}
	}
}

// csUsed notes that a ChanServ command of c did what it was asked on
// reg, so LOG can report it once the command has run. Commands call it
// at the point they succeed; refused or failed commands aren't logged.
func (s *Services) csUsed(c *Client, reg *store.Channel) { c.csUsedOn = reg.Name }

// csLogUse tells reg's LOG targets that c used command.
func (s *Services) csLogUse(c *Client, command string, args []string) {
	reg, err := s.store.ChannelByName(c.csUsedOn)
	if err != nil || reg == nil {
		return
	}
	var rest []string
	for _, a := range args {
		if !irc.EqualFold(a, reg.Name) {
			rest = append(rest, a)
		}
	}
	text := c.user.Nick + " used " + command
	if len(rest) > 0 {
		text += " " + strings.Join(rest, " ")
	}
	for _, l := range s.store.LogSettings(reg) {
		if l.Command != command {
			continue
		}
		switch l.Method {
		case "MEMO":
			if err := s.store.AddChanMemo(reg, s.chanServ.user.Nick, text, time.Now()); err != nil {
				s.log.Printf("services database: %v", err)
			}
		default:
			var status byte
			if l.Status != "" {
				status = l.Status[0]
			}
			s.host.ChannelMessage(s.chanServ.user.UID, reg.Name, status, l.Method == "MESSAGE", text)
		}
	}
}

func csLog(s *Services, c *Client, args []string) {
	reg, _, ok := s.csPriv(c, argAt(args, 0), "SET")
	if !ok {
		return
	}
	if argAt(args, 1) == "" {
		list := s.store.LogSettings(reg)
		if len(list) == 0 {
			s.cs(c, "%s logs nothing. Add one: /cs log %s <command> NOTICE", reg.Name, reg.Name)
			return
		}
		s.cs(c, "Log settings of %s:", reg.Name)
		for _, l := range list {
			to := "everyone"
			if l.Status != "" {
				to = l.Status + " and up"
			}
			s.cs(c, "  %-10s %-8s %s", l.Command, l.Method, to)
		}
		return
	}
	command := strings.ToUpper(strings.TrimPrefix(strings.ToLower(args[1]), "chanserv/"))
	if real, ok := s.chanServ.aliases[command]; ok {
		command = real
	}
	if _, ok := s.chanServ.cmds[command]; !ok {
		s.cs(c, "ChanServ has no command %s.", args[1])
		return
	}
	method := strings.ToUpper(argAt(args, 2))
	switch method {
	case "NONE":
		n := 0
		for _, l := range s.store.LogSettings(reg) {
			if l.Command == command {
				if _, err := s.store.DelLog(reg, l.Command, l.Method); err != nil {
					s.dbErr(c, s.chanServ, err)
					return
				}
				n++
			}
		}
		if n == 0 {
			s.cs(c, "%s doesn't log %s.", reg.Name, command)
		} else {
			s.csUsed(c, reg)
			s.csLog(c, reg.Name, false, "to remove logging for %s with method %s", command, strings.ToUpper(argAt(args, 2)))
			s.cs(c, "%s no longer logs %s.", reg.Name, command)
		}
		return
	case "MESSAGE", "NOTICE", "MEMO":
	default:
		s.cs(c, "Syntax: LOG %s %s MESSAGE|NOTICE|MEMO [status] (or NONE)", reg.Name, command)
		return
	}
	status := argAt(args, 3)
	if status != "" {
		r := state.RankForPrefix(status[0])
		if r == 0 || len(status) > 1 || !s.rankEnabled(r) || method == "MEMO" {
			s.cs(c, "The status is one prefix this server uses, like @ (not for MEMO).")
			return
		}
	}
	if err := s.store.SetLog(reg, store.LogSetting{Command: command, Method: method, Status: status}); err != nil {
		s.dbErr(c, s.chanServ, err)
		return
	}
	s.csUsed(c, reg)
	if status != "" {
		s.csLog(c, reg.Name, false, "to log %s with method %s %s", command, method, status)
	} else {
		s.csLog(c, reg.Name, false, "to log %s with method %s", command, method)
	}
	switch {
	case method == "MEMO":
		s.cs(c, "%s now leaves a channel memo whenever someone uses %s.", reg.Name, command)
	case status != "":
		s.cs(c, "%s now tells %s and up whenever someone uses %s.", reg.Name, status, command)
	default:
		s.cs(c, "%s now tells everyone in it whenever someone uses %s.", reg.Name, command)
	}
}

func csSuspend(s *Services, c *Client, args []string) {
	if !c.can('s') {
		s.cs(c, "SUSPEND is for IRC operators with flag s.")
		return
	}
	reg, _, ok := s.csLookup(c, argAt(args, 0))
	if !ok {
		return
	}
	if reg.Suspended(time.Now()) {
		s.cs(c, "%s is suspended already (by %s). UNSUSPEND it first to change that.", reg.Name, reg.SuspendedBy)
		return
	}
	rest := args[1:]
	var life time.Duration
	if len(rest) > 0 && strings.HasPrefix(rest[0], "+") {
		d, ok := parseExpiry(rest[0])
		if !ok {
			s.cs(c, "%s isn't a time. Use +30m, +12h, +7d, or leave it out for until UNSUSPEND.", rest[0])
			return
		}
		life, rest = d, rest[1:]
	}
	reason := irc.Truncate(strings.Join(rest, " "), 200)
	if reason == "" {
		reason = "No reason given"
	}
	now := time.Now()
	reg.SuspendedBy, reg.SuspendReason, reg.SuspendedAt, reg.SuspendUntil = c.user.Nick, reason, now, time.Unix(0, 0)
	if life > 0 {
		reg.SuspendUntil = now.Add(life)
	}
	if err := s.store.SaveChannel(reg); err != nil {
		s.dbErr(c, s.chanServ, err)
		return
	}
	kicked := s.enforceKick(reg, func(u *state.User) bool { return !s.isOperUser(u) }, false, "This channel is suspended: "+reason)
	until := "until UNSUSPEND"
	if life > 0 {
		until = "for " + durWords(life)
	}
	s.audit(c, s.chanServ, "SUSPEND %s %s: %s", reg.Name, until, reason)
	expires := "never"
	if life > 0 {
		expires = anopeTime(reg.SuspendUntil)
	}
	s.csLogAdmin(c, reg.Name, "(%s), expires on %s", reason, expires)
	s.cs(c, "%s is suspended %s (%s kicked).", reg.Name, until, plural(kicked, "person"))
}

func csUnsuspend(s *Services, c *Client, args []string) {
	if !c.can('s') {
		s.cs(c, "UNSUSPEND is for IRC operators with flag s.")
		return
	}
	reg, _, ok := s.csLookup(c, argAt(args, 0))
	if !ok {
		return
	}
	if reg.SuspendedBy == "" {
		s.cs(c, "%s isn't suspended.", reg.Name)
		return
	}
	s.csLogAdmin(c, reg.Name, "which was suspended by %s for: %s", reg.SuspendedBy, orNoneReason(reg.SuspendReason))
	s.unsuspend(reg)
	s.audit(c, s.chanServ, "UNSUSPEND %s", reg.Name)
	s.cs(c, "%s can be used again.", reg.Name)
}

func (s *Services) unsuspend(reg *store.Channel) {
	reg.SuspendedBy, reg.SuspendReason, reg.SuspendedAt, reg.SuspendUntil = "", "", time.Unix(0, 0), time.Unix(0, 0)
	reg.LastUsed = time.Now() // don't expire it the moment it opens
	if err := s.store.SaveChannel(reg); err != nil {
		s.log.Printf("services database: %v", err)
	}
}

// expireSuspends opens channels whose suspension ran out.
func (s *Services) expireSuspends(now time.Time) {
	all, err := s.store.Channels()
	if err != nil {
		return
	}
	for _, reg := range all {
		if reg.SuspendedBy != "" && !reg.Suspended(now) {
			s.unsuspend(reg)
			s.log.Printf("suspension of %s ran out", reg.Name)
			s.logTo(s.global, "CS_SUSPEND: Expiring suspend for "+reg.Name) // Anope: Log(this) in cs_suspend
		}
	}
}

// isOperUser reports whether u is an IRC operator.
func (s *Services) isOperUser(u *state.User) bool { return s.host.OperFlags(u.UID) != "" }

// persistJoin puts ChanServ in reg so the channel stays open, opening it
// if nobody is in it.
func (s *Services) persistJoin(reg *store.Channel) {
	if live := s.net.Channel(reg.Name); live != nil && live.Member(s.chanServ.user) != nil {
		return
	}
	ev := &state.Join{UID: s.chanServ.user.UID, Channel: reg.Name, TS: time.Now().Unix()}
	if s.emit(ev) != nil {
		return
	}
	if live := s.net.Channel(reg.Name); live != nil && ev.Created {
		s.restoreChannel(reg, live)
	}
}

// persistPart takes ChanServ out of reg (the channel closes if it was the
// last one there).
func (s *Services) persistPart(reg *store.Channel) {
	if live := s.net.Channel(reg.Name); live != nil && live.Member(s.chanServ.user) != nil {
		s.emit(&state.Part{UID: s.chanServ.user.UID, Channel: live.Name})
	}
}

// restoreChannel brings back what a registered channel keeps when it is
// opened: the topic (KEEPTOPIC), the modes (KEEPMODES) and the mode lock.
func (s *Services) restoreChannel(reg *store.Channel, ch *state.Channel) {
	if reg.KeepTopic && reg.Topic != "" {
		s.emit(&state.Topic{Source: s.chanServ.user.UID, Channel: ch.Name, Text: reg.Topic, By: reg.TopicBy, At: reg.TopicAt})
	}
	if reg.KeepModes && reg.LastModes != "" {
		s.csMode(ch, parseModes(reg.LastModes))
	}
	s.csMode(ch, mlockChanges(reg, ch))
}

// liveModes writes ch's modes the way KEEPMODES keeps them: "+ntkl key 50"
// (without +r).
func liveModes(ch *state.Channel) string {
	var flags []byte
	for m, on := range ch.Modes {
		if on && m != 'r' && m != 'k' && m != 'l' {
			flags = append(flags, m)
		}
	}
	sort.Slice(flags, func(i, j int) bool { return flags[i] < flags[j] })
	var params []string
	if ch.Modes['k'] {
		flags = append(flags, 'k')
		params = append(params, ch.Key)
	}
	if ch.Modes['l'] {
		flags = append(flags, 'l')
		params = append(params, strconv.Itoa(ch.Limit))
	}
	if len(flags) == 0 {
		return ""
	}
	return strings.Join(append([]string{"+" + string(flags)}, params...), " ")
}

// parseModes reads liveModes' form back into changes.
func parseModes(text string) []modeChange {
	fields := strings.Fields(text)
	if len(fields) == 0 {
		return nil
	}
	params := fields[1:]
	var out []modeChange
	for _, m := range []byte(strings.TrimPrefix(fields[0], "+")) {
		arg := ""
		if m == 'k' || m == 'l' {
			if len(params) == 0 {
				continue
			}
			arg, params = params[0], params[1:]
		}
		out = append(out, modeChange{true, m, arg})
	}
	return out
}

// csModesChanged keeps the modes of a KEEPMODES channel.
func (s *Services) csModesChanged(ch *state.Channel) {
	reg, err := s.store.ChannelByName(ch.Name)
	if err != nil || reg == nil || !reg.KeepModes {
		return
	}
	if now := liveModes(ch); now != reg.LastModes {
		reg.LastModes = now
		if err := s.store.SaveChannel(reg); err != nil {
			s.log.Printf("services database: %v", err)
		}
	}
}

// startPersist opens every PERSIST channel (when services start).
func (s *Services) startPersist() {
	all, err := s.store.Channels()
	if err != nil {
		s.log.Printf("services database: %v", err)
		return
	}
	for _, reg := range all {
		if reg.Persist && !reg.Suspended(time.Now()) {
			s.persistJoin(reg)
		}
	}
}

// orNoneReason is r, or Anope's "No reason" when empty.
func orNoneReason(r string) string {
	if r == "" {
		return "No reason"
	}
	return r
}
