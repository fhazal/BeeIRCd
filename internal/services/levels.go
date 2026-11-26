package services

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/fhazal/beeircd/internal/irc"
	"github.com/fhazal/beeircd/internal/state"
	"github.com/fhazal/beeircd/internal/store"
)

// Channel access works like Anope's: every entry on the access list has a
// level (1 to 10000; the xOP names are levels too), and each channel has a
// LEVELS table saying which level each privilege needs. A privilege at
// level 0 belongs to everyone in the channel, on the list or not.

// privilege is one thing a LEVELS table can hand out.
type privilege struct {
	name string
	flag byte // its letter in FLAGS: Anope 2.0's, but ACCESS_LIST has l (Anope gives it f, which also changes the lists)
	def  int  // Anope 2.1's default
	desc string
}

// privileges in Anope's order (alphabetical). Defaults from Anope 2.1's
// chanserv.example.conf; the BotServ ones (ASSIGN, BADWORDS, FANTASIA,
// GREET, SAY) from 2.0.20's chanserv and botserv example configs.
var privileges = []privilege{
	{"ACCESS_CHANGE", 'f', 10, "change the access lists"},
	{"ACCESS_LIST", 'l', 3, "see the access lists"},
	{"AKICK", 'K', 10, "use AKICK and ENFORCE"},
	{"ASSIGN", 's', store.LevelFounder, "assign or unassign a BotServ bot"},
	{"AUTOHALFOP", 'H', 4, "halfop (%) on join"},
	{"AUTOOP", 'O', 5, "op (@) on join"},
	{"AUTOOWNER", 'Q', 9999, "owner (~) on join"},
	{"AUTOPROTECT", 'A', 10, "admin (&) on join"},
	{"AUTOVOICE", 'V', 3, "voice (+) on join"},
	{"BADWORDS", 'K', 10, "change the BotServ bad words list"},
	{"BAN", 'b', 4, "use BAN"},
	{"FANTASIA", 'c', 3, "use fantasy commands (!op, !kick ...)"},
	{"FOUNDER", 'F', 10000, "founder rights (when SECUREFOUNDER is off)"},
	{"GETKEY", 'G', 5, "use GETKEY"},
	{"GREET", 'g', 5, "the bot shows your NickServ greet when you join"},
	{"HALFOP", 'h', 5, "halfop and dehalfop others"},
	{"HALFOPME", 'h', 4, "halfop yourself"},
	{"INFO", 'I', 9999, "see every detail in INFO"},
	{"INVITE", 'i', 5, "use INVITE"},
	{"KICK", 'k', 4, "use KICK"},
	{"MEMO", 'm', 10, "read and run the channel memo box"},
	{"MODE", 's', 9999, "use MODE (and the mode lock)"},
	{"NOKICK", 'N', 1, "never kicked by RESTRICTED"},
	{"OP", 'o', 5, "op and deop others"},
	{"OPME", 'o', 5, "op yourself"},
	{"OWNER", 'q', store.LevelFounder, "give owner (~) to others"},
	{"OWNERME", 'q', 9999, "owner (~) yourself"},
	{"PROTECT", 'a', 9999, "give admin (&) to others"},
	{"PROTECTME", 'a', 10, "admin (&) yourself"},
	{"SAY", 'B', 5, "make the bot SAY and ACT in the channel"},
	{"SET", 's', 9999, "use SET, ENTRYMSG and LOG"},
	{"SIGNKICK", 'K', 9999, "kicks you ask for don't show your nick"},
	{"TOPIC", 't', 5, "use TOPIC and change a locked topic"},
	{"UNBAN", 'u', 4, "unban others"},
	{"VOICE", 'v', 4, "voice and devoice others"},
	{"VOICEME", 'v', 3, "voice yourself"},
}

func findPrivilege(name string) (privilege, bool) {
	name = strings.ToUpper(name)
	for _, p := range privileges {
		if p.name == name {
			return p, true
		}
	}
	return privilege{}, false
}

// privLevel is the level reg asks for privilege name.
func (s *Services) privLevel(reg *store.Channel, name string) int {
	if l, ok := s.store.ChanLevels(reg)[name]; ok {
		return l
	}
	p, _ := findPrivilege(name)
	return p.def
}

// access is someone's access on a channel: a level (LevelFounder for the
// founder), or a FLAGS entry, whose letters say which privileges it has;
// its level is then its rank (see flagsRank), used to order entries and
// to say whom it may change.
type access struct {
	level int
	flags string
}

// accountLevel is an account's access on reg: LevelFounder for the founder
// (and, with SECUREFOUNDER off, for those with the FOUNDER privilege).
func (s *Services) accountLevel(reg *store.Channel, accountID int64) access {
	lvl, flags := s.store.ChanAccess(reg, accountID)
	if flags != "" {
		a := access{level: s.flagsRank(reg, flags), flags: flags}
		if !reg.SecureFounder && strings.IndexByte(flags, 'F') >= 0 && s.privLevel(reg, "FOUNDER") != store.LevelDisabled {
			a.level = store.LevelFounder
		}
		return a
	}
	if lvl != store.LevelFounder && lvl > store.LevelNone && !reg.SecureFounder {
		if need := s.privLevel(reg, "FOUNDER"); need != store.LevelDisabled && lvl >= need {
			return access{level: store.LevelFounder}
		}
	}
	return access{level: lvl}
}

// clientLevel is c's access on reg: the founder's while c is a super
// admin (OperServ SET SUPERADMIN), else that of the account c is
// identified to or recognised as (none without one).
func (s *Services) clientLevel(c *Client, reg *store.Channel) access {
	if s.isSuperAdmin(c) {
		return access{level: store.LevelFounder}
	}
	id := s.chanAccount(c, reg)
	if id == 0 {
		return access{}
	}
	return s.accountLevel(reg, id)
}

// flagsRank is the rank of a FLAGS entry: the highest level its
// privileges need on reg (founder-only ones count as the top level).
func (s *Services) flagsRank(reg *store.Channel, flags string) int {
	rank := store.LevelUOP
	for _, p := range privileges {
		if strings.IndexByte(flags, p.flag) < 0 {
			continue
		}
		need := s.privLevel(reg, p.name)
		if need == store.LevelFounder {
			need = store.LevelMax
		}
		if need > rank {
			rank = need
		}
	}
	return rank
}

// levelHas reports whether someone with access a on reg has privilege
// name. A FLAGS entry has the privileges of its letters, and those
// everyone has (level 0); LEVELS doesn't change it, as in Anope.
func (s *Services) levelHas(reg *store.Channel, a access, name string) bool {
	need := s.privLevel(reg, name)
	switch {
	case need == store.LevelDisabled:
		return false
	case a.level == store.LevelFounder:
		return true
	case a.flags != "":
		p, _ := findPrivilege(name)
		return need <= 0 || strings.IndexByte(a.flags, p.flag) >= 0
	case need == store.LevelFounder:
		return false
	}
	return a.level >= need
}

// userLevel is a connected user's access on reg (none when not identified).
func (s *Services) userLevel(reg *store.Channel, u *state.User) access {
	c := s.clientOf(u)
	if c == nil {
		return access{}
	}
	return s.clientLevel(c, reg)
}

// chanAccount is the account c's channel access on reg comes from: the one
// they identified to, or, when reg has SECURE off, the owner of their nick
// if their connection is recognised (NickServ ACCESS with SECURE off).
// 0 means none.
func (s *Services) chanAccount(c *Client, reg *store.Channel) int64 {
	if c.accountID != 0 || reg == nil || reg.Secure || c.user == nil {
		return c.accountID
	}
	a, err := s.store.AccountByNick(c.user.Nick)
	if err != nil || a == nil || !s.recognized(c, a) {
		return 0
	}
	return a.ID
}

// userHas reports whether a connected user has privilege name on reg.
func (s *Services) userHas(reg *store.Channel, u *state.User, name string) bool {
	return s.levelHas(reg, s.userLevel(reg, u), name)
}

// needWords says who has a privilege, for "you need ..." replies, in the
// level names people know: a number between them (Anope's 9999) is the
// lowest name that covers it, "the founder" when none does.
func (s *Services) needWords(need int) string {
	if need == store.LevelFounder {
		return "the founder"
	}
	names := []int{store.LevelUOP, store.LevelVOP, store.LevelAOP, store.LevelSOP}
	if s.cfg.FullPrefixes() {
		names = []int{store.LevelUOP, store.LevelVOP, store.LevelHOP, store.LevelAOP, store.LevelSOP, store.LevelQOP}
	}
	for _, l := range names {
		if l >= need {
			return store.LevelName(l) + " or higher"
		}
	}
	return "the founder"
}

// csPriv loads a registered channel for a command that needs privilege
// name, and returns c's level on it.
func (s *Services) csPriv(c *Client, chanName, priv string) (*store.Channel, access, bool) {
	reg, lvl, ok := s.csChan(c, chanName, 0)
	if !ok {
		return nil, access{}, false
	}
	if s.chanAccount(c, reg) == 0 {
		s.cs(c, "Identify first: /ns identify <password>")
		s.logDeniedUnreg(c, s.chanServ)
		return nil, access{}, false
	}
	if !s.levelHas(reg, lvl, priv) {
		need := s.privLevel(reg, priv)
		switch {
		case need == store.LevelDisabled:
			s.cs(c, "%s is switched off on %s (see /cs levels %s list).", priv, reg.Name, reg.Name)
		case lvl.flags != "":
			p, _ := findPrivilege(priv)
			s.cs(c, "You need the %c flag (%s) on %s for that.", p.flag, priv, reg.Name)
		case need == store.LevelFounder:
			s.cs(c, "Only the founder of %s can do that.", reg.Name)
		default:
			s.cs(c, "You need to be %s on %s for that.", s.needWords(need), reg.Name)
		}
		return nil, access{}, false
	}
	return reg, lvl, true
}

// parseLevel reads a level: a number from 1 to 10000 or an xOP name.
func parseLevel(arg string) (int, bool) {
	switch strings.ToUpper(arg) {
	case "UOP":
		return store.LevelUOP, true
	case "VOP":
		return store.LevelVOP, true
	case "HOP":
		return store.LevelHOP, true
	case "AOP":
		return store.LevelAOP, true
	case "SOP":
		return store.LevelSOP, true
	case "QOP":
		return store.LevelQOP, true
	}
	n, err := strconv.Atoi(arg)
	if err != nil || n < 1 || n > store.LevelMax {
		return 0, false
	}
	return n, true
}

// accessChange checks that someone at lvl may put an entry at newLevel (0
// for removing) for target, who is at old now. It answers c when not.
func (s *Services) accessChange(c *Client, reg *store.Channel, lvl access, old, newLevel int, target string) bool {
	if lvl.level == store.LevelFounder {
		return true
	}
	if old >= lvl.level {
		s.cs(c, "You can't change %s: their level is the same as or above yours.", target)
		return false
	}
	if newLevel >= lvl.level {
		s.cs(c, "You can only give levels below your own (%s) on %s.", lvl.name(), reg.Name)
		return false
	}
	return true
}

// afterAccessChange gives the new modes to target's connections in reg.
func (s *Services) afterAccessChange(reg *store.Channel, accountID int64) {
	live := s.net.Channel(reg.Name)
	if live == nil {
		return
	}
	for _, oc := range s.onlineClients(accountID) {
		if live.Member(oc.user) != nil {
			s.csAutoModes(reg, live, oc.user)
		}
	}
}

func csAccess(s *Services, c *Client, args []string) {
	name, sub := argAt(args, 0), strings.ToUpper(argAt(args, 1))
	switch sub {
	case "ADD":
		reg, lvl, ok := s.csPriv(c, name, "ACCESS_CHANGE")
		if !ok {
			return
		}
		if argAt(args, 3) == "" {
			s.cs(c, "Syntax: ACCESS %s ADD <nick> <level> [description]", reg.Name)
			return
		}
		newLevel, ok := parseLevel(args[3])
		if !ok {
			s.cs(c, "%s isn't a level. Give a number from 1 to %d, or VOP, AOP, SOP and so on.", args[3], store.LevelMax)
			return
		}
		target, ok := s.csAccount(c, reg, args[2])
		if !ok {
			return
		}
		old, oldFlags := s.store.ChanAccess(reg, target.ID)
		if s.neverOpBlocks(c, target, old, newLevel) {
			return
		}
		// A FLAGS entry ranks by what its letters need now (LEVELS SET may
		// have changed that since it was set), not by the level stored.
		if !s.accessChange(c, reg, lvl, s.accountLevel(reg, target.ID).level, newLevel, target.Name) {
			return
		}
		// A FLAGS entry stored at this level still becomes a plain one.
		if old == newLevel && oldFlags == "" {
			s.cs(c, "%s is at level %s on %s already.", target.Name, store.LevelName(newLevel), reg.Name)
			return
		}
		note := irc.Truncate(strings.Join(args[4:], " "), 100)
		if _, err := s.store.SetLevel(reg, target, newLevel, c.user.Account, note, time.Now()); err != nil {
			s.dbErr(c, s.chanServ, err)
			return
		}
		s.csUsed(c, reg)
		s.csLog(c, reg.Name, false, "to add %s with level %d", target.Name, newLevel)
		if old == store.LevelNone {
			s.cs(c, "%s added to the access list of %s at level %s.", target.Name, reg.Name, store.LevelName(newLevel))
		} else {
			s.cs(c, "%s moved from level %s to %s on %s.", target.Name, access{level: old, flags: oldFlags}.name(), store.LevelName(newLevel), reg.Name)
		}
		s.afterAccessChange(reg, target.ID)
	case "DEL":
		// Taking yourself off needs no privilege (Anope).
		if a, _ := s.store.AccountByNick(argAt(args, 2)); a != nil && a.ID == c.accountID && c.accountID != 0 {
			if reg, lvl, ok := s.csChan(c, name, 0); ok {
				s.accessDel(c, reg, lvl, 0, args[2], "ACCESS")
			}
			return
		}
		reg, lvl, ok := s.csPriv(c, name, "ACCESS_CHANGE")
		if !ok {
			return
		}
		s.accessDel(c, reg, lvl, 0, argAt(args, 2), "ACCESS")
	case "", "LIST", "VIEW":
		reg, _, ok := s.csPriv(c, name, "ACCESS_LIST")
		if !ok {
			return
		}
		s.accessList(c, reg, sub == "VIEW", argAt(args, 2))
	case "CLEAR":
		reg, _, ok := s.csChan(c, name, store.LevelFounder)
		if !ok {
			return
		}
		entries, _ := s.store.AccessList(reg, 0)
		for _, e := range entries {
			if _, err := s.store.RemoveAccess(reg, e.AccountID); err != nil {
				s.dbErr(c, s.chanServ, err)
				return
			}
		}
		s.csUsed(c, reg)
		s.csLog(c, reg.Name, false, "to clear the access list")
		s.cs(c, "The access list of %s is now empty (%d removed).", reg.Name, len(entries))
	default:
		s.cs(c, "Syntax: ACCESS #chan ADD|DEL|LIST|VIEW|CLEAR ...")
	}
}

// csAccount finds the registered account behind nick for an access change.
func (s *Services) csAccount(c *Client, reg *store.Channel, nick string) (*store.Account, bool) {
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
	return target, true
}

// neverOpBlocks refuses, when target has NickServ SET NEVEROP on (Anope's
// NEVEROP), to put it on an access list it isn't on yet (old is
// store.LevelNone) or to raise an entry it has to op level (AOP) or
// above. Lowering an entry, changing it below op level, or removing it
// still works; entries it has already are left alone.
func (s *Services) neverOpBlocks(c *Client, target *store.Account, old, new int) bool {
	if !target.NeverOp {
		return false
	}
	if old != store.LevelNone && (new <= old || new < store.LevelAOP) {
		return false
	}
	s.cs(c, "%s has NEVEROP on and can't be added to access lists.", target.Name)
	return true
}

// accessDel removes entries by nick or number list. level is the xOP list
// they must be on (0 = any), list its name for replies.
func (s *Services) accessDel(c *Client, reg *store.Channel, lvl access, level int, arg, list string) {
	what := "the access list"
	if level != 0 {
		what = "the " + list + " list"
	}
	if arg == "" {
		s.cs(c, "Syntax: %s %s DEL {<nick> | <num> | <list>}", list, reg.Name)
		return
	}
	entries, err := s.store.AccessList(reg, level)
	if err != nil {
		s.dbErr(c, s.chanServ, err)
		return
	}
	var gone []store.AccessEntry
	if nums, isList := numList(arg, len(entries)); isList {
		for _, i := range nums {
			gone = append(gone, entries[i-1])
		}
		if len(gone) == 0 {
			s.cs(c, "No entry %s on %s of %s.", arg, what, reg.Name)
			return
		}
	} else {
		target, ok := s.csAccount(c, reg, arg)
		if !ok {
			return
		}
		for _, e := range entries {
			if e.AccountID == target.ID {
				gone = append(gone, e)
			}
		}
		if len(gone) == 0 {
			s.cs(c, "%s isn't on %s of %s.", target.Name, what, reg.Name)
			return
		}
	}
	var names []string
	for _, e := range gone {
		self := e.AccountID == c.accountID // you may always take yourself off
		if !self && lvl.level != store.LevelFounder && s.accountLevel(reg, e.AccountID).level >= lvl.level {
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
		s.csUsed(c, reg)
		s.csLog(c, reg.Name, false, "to delete %s", strings.Join(names, ", "))
		s.cs(c, "%s removed from %s of %s.", strings.Join(names, ", "), what, reg.Name)
	}
}

// accessList shows the whole access list (ACCESS LIST/VIEW).
func (s *Services) accessList(c *Client, reg *store.Channel, view bool, spec string) {
	entries, err := s.store.AccessList(reg, 0)
	if err != nil {
		s.dbErr(c, s.chanServ, err)
		return
	}
	show := pickEntries(spec, len(entries), func(i int) string { return entries[i].Account })
	if spec != "" && len(show) == 0 {
		s.cs(c, "No entry on the access list of %s matches %s.", reg.Name, spec)
		return
	}
	width := len(reg.Founder)
	for _, e := range entries {
		width = max(width, len(e.Account))
	}
	width = max(width, 4)
	gets := func(lvl access) string {
		if p := s.levelPrefixes(reg, lvl); p != "" {
			return p
		}
		return "–"
	}
	s.cs(c, "Access list of %s", reg.Name)
	s.cs(c, "  %-3s %-7s %-*s  %s", "#", "Level", width, "Nick", "Gets")
	if spec == "" {
		s.cs(c, "  %-3s %-7s %-*s  %s", "–", "Founder", width, reg.Founder, gets(access{level: store.LevelFounder}))
	}
	for _, i := range show {
		e := entries[i]
		line := fmt.Sprintf("  %-3d %-7s %-*s  %s", i+1, entryName(e), width, e.Account, gets(access{level: e.Level, flags: e.Flags}))
		if view {
			line = fmt.Sprintf("%-*s  added by %s, %s", 22+width, line, e.AddedBy, e.AddedAt.UTC().Format("2 Jan 2006"))
		}
		if e.Note != "" {
			line += " · " + e.Note
		}
		s.cs(c, "%s", line)
	}
	n := len(show)
	if spec == "" {
		n++
	}
	s.cs(c, "%s. Change one: /cs access %s add <nick> <level>", plural(n, "entry"), reg.Name)
}

func csLevels(s *Services, c *Client, args []string) {
	name, sub := argAt(args, 0), strings.ToUpper(argAt(args, 1))
	switch sub {
	case "", "LIST":
		reg, _, ok := s.csPriv(c, name, "ACCESS_LIST")
		if !ok {
			return
		}
		changed := s.store.ChanLevels(reg)
		s.cs(c, "Levels of %s (* = changed from the default):", reg.Name)
		for _, p := range privileges {
			l := s.privLevel(reg, p.name)
			word := strconv.Itoa(l)
			switch l {
			case store.LevelFounder:
				word = "founder"
			case store.LevelDisabled:
				word = "disabled"
			}
			mark := " "
			if _, ok := changed[p.name]; ok {
				mark = "*"
			}
			s.cs(c, "  %-14s %8s%s  %s", p.name, word, mark, p.desc)
		}
		s.cs(c, "Change one: /cs levels %s set <privilege> <level>|FOUNDER", reg.Name)
	case "SET", "DISABLE", "DIS":
		reg, _, ok := s.csChan(c, name, store.LevelFounder)
		if !ok {
			return
		}
		if argAt(args, 2) == "" {
			if sub == "SET" {
				s.cs(c, "Syntax: LEVELS %s SET <privilege> {<0-%d> | FOUNDER}", reg.Name, store.LevelMax)
			} else {
				s.cs(c, "Syntax: LEVELS %s DISABLE <privilege>", reg.Name)
			}
			return
		}
		p, found := findPrivilege(argAt(args, 2))
		if !found {
			s.cs(c, "%s isn't a privilege. See /cs levels %s list.", argAt(args, 2), reg.Name)
			return
		}
		lvl := store.LevelDisabled
		if sub == "SET" {
			v := argAt(args, 3)
			n, err := strconv.Atoi(v)
			switch {
			case strings.EqualFold(v, "FOUNDER"):
				lvl = store.LevelFounder
			case err == nil && n >= 0 && n <= store.LevelMax:
				lvl = n
			default:
				s.cs(c, "Syntax: LEVELS %s SET %s {<0-%d> | FOUNDER}. 0 means everyone in the channel.", reg.Name, p.name, store.LevelMax)
				return
			}
		}
		if err := s.setPrivLevel(reg, p, lvl); err != nil {
			s.dbErr(c, s.chanServ, err)
			return
		}
		s.csUsed(c, reg)
		if lvl == store.LevelDisabled {
			s.csLog(c, reg.Name, false, "to disable %s", p.name)
		} else {
			s.csLog(c, reg.Name, false, "to set %s to level %d", p.name, lvl)
		}
		switch lvl {
		case store.LevelDisabled:
			s.cs(c, "%s is now switched off on %s.", p.name, reg.Name)
		case store.LevelFounder:
			s.cs(c, "%s on %s is now for the founder only.", p.name, reg.Name)
		default:
			s.cs(c, "%s on %s now needs level %d.", p.name, reg.Name, lvl)
		}
		s.csSync(reg)
	case "RESET":
		reg, _, ok := s.csChan(c, name, store.LevelFounder)
		if !ok {
			return
		}
		if err := s.store.ResetChanLevels(reg); err != nil {
			s.dbErr(c, s.chanServ, err)
			return
		}
		s.csUsed(c, reg)
		s.csLog(c, reg.Name, false, "to reset all levels")
		s.cs(c, "Every level of %s is back to its default.", reg.Name)
		s.csSync(reg)
	default:
		s.cs(c, "Syntax: LEVELS #chan SET|DISABLE|LIST|RESET ...")
	}
}

// setPrivLevel stores a privilege level, leaving no row for the default.
func (s *Services) setPrivLevel(reg *store.Channel, p privilege, lvl int) error {
	if lvl == p.def {
		changed := s.store.ChanLevels(reg)
		if _, ok := changed[p.name]; !ok {
			return nil
		}
		delete(changed, p.name)
		if err := s.store.ResetChanLevels(reg); err != nil {
			return err
		}
		for name, l := range changed {
			if err := s.store.SetChanLevel(reg, name, l); err != nil {
				return err
			}
		}
		return nil
	}
	return s.store.SetChanLevel(reg, p.name, lvl)
}

// csStatus is STATUS (and WHY): what access someone has and why.
func csStatus(s *Services, c *Client, args []string) {
	name, nick := argAt(args, 0), argAt(args, 1)
	self := nick == "" || irc.EqualFold(nick, c.user.Nick)
	var reg *store.Channel
	var ok bool
	if self {
		reg, _, ok = s.csChan(c, name, 0)
	} else {
		reg, _, ok = s.csPriv(c, name, "ACCESS_LIST")
	}
	if !ok {
		return
	}
	who := c.user.Nick
	var accountID int64
	if self {
		accountID = c.accountID
	} else {
		who = nick
		if u := s.net.UserByNick(nick); u != nil {
			if oc := s.clientOf(u); oc != nil {
				accountID = oc.accountID
			}
			if accountID == 0 {
				s.cs(c, "%s isn't identified, so they have no access on %s.", u.Nick, reg.Name)
				return
			}
		} else {
			a, err := s.store.AccountByNick(nick)
			if err != nil || a == nil {
				s.cs(c, "%s isn't online or a registered nick.", nick)
				return
			}
			accountID, who = a.ID, a.Name
		}
	}
	if accountID == 0 {
		s.cs(c, "You aren't identified, so you have no access on %s.", reg.Name)
		return
	}
	acct := s.store.AccountNameByID(accountID)
	raw, rawFlags := s.store.ChanAccess(reg, accountID)
	at := "at level " + store.LevelName(raw)
	if rawFlags != "" {
		at = "with flags +" + rawFlags
	}
	lvl := s.accountLevel(reg, accountID)
	switch {
	case raw == store.LevelFounder:
		s.cs(c, "%s is the founder of %s (account %s).", who, reg.Name, acct)
	case raw == store.LevelNone:
		s.cs(c, "%s (account %s) isn't on the access list of %s.", who, acct, reg.Name)
	case lvl.level == store.LevelFounder:
		s.cs(c, "%s (account %s) is on the access list of %s %s, which has founder rights (SECUREFOUNDER is off).", who, acct, reg.Name, at)
	default:
		s.cs(c, "%s (account %s) is on the access list of %s %s.", who, acct, reg.Name, at)
	}
	var have []string
	for _, p := range privileges {
		if s.levelHas(reg, lvl, p.name) {
			have = append(have, p.name)
		}
	}
	sort.Strings(have)
	if len(have) == 0 {
		s.cs(c, "  Privileges: none")
		return
	}
	for i, line := range wrapNames(have, 60) {
		if i == 0 {
			s.cs(c, "  Privileges: %s", line)
		} else {
			s.cs(c, "              %s", line)
		}
	}
}

// name is how access is shown: a level's name, or +flags.
func (a access) name() string {
	if a.flags != "" && a.level != store.LevelFounder {
		return "+" + a.flags
	}
	return store.LevelName(a.level)
}

// entryName is how an access list entry is shown.
func entryName(e store.AccessEntry) string {
	return access{level: e.Level, flags: e.Flags}.name()
}
