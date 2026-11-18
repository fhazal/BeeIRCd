package services

import (
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/fhazal/beeircd/internal/irc"
	"github.com/fhazal/beeircd/internal/store"
)

const (
	maxMemoLen       = 300
	maxMemoLimit     = 50
	maxMemoOperLimit = 500
)

func memoServCommands() map[string]svcCmd {
	return map[string]svcCmd{
		"SEND": {"SEND {<nick> | #chan} <text>", "leave a memo for someone (or a channel)",
			"Stores a message for a registered nick. They get it at once when online,\nor the next time they identify. A memo to a registered channel goes into its\nmemo box, which those with the MEMO privilege read (SOP by default).\nExample: /ms send nova Meeting about the new leaf, Friday 21:00 in #ops", msSend},
		"RSEND": {"RSEND <nick> <text>", "send a memo with a read receipt",
			"Like SEND, but when they first read it you get a memo from MemoServ saying so.\nFor nicks only, not channel memo boxes.\nExample: /ms rsend nova The new leaf is up, can you check it?", msRSend},
		"LIST": {"LIST [#chan] [<list> | NEW]", "your memos (or a channel's)",
			"Lists your memos: number, sender, date and whether you read it.\nLIST NEW shows only unread ones; LIST 2-5 only those. LIST #chan shows the\nchannel's memo box (MEMO privilege).", msList},
		"READ": {"READ [#chan] {<num> | <list> | LAST | NEW | ALL}", "read memos",
			"READ 2 reads memo 2, READ 1-3,5 several, READ NEW all unread ones,\nREAD LAST the newest, READ ALL every one. With #chan first: the channel's box.", msRead},
		"DEL": {"DEL [#chan] {<num> | <list> | LAST | READ | ALL}", "delete memos",
			"DEL 2 deletes memo 2, DEL 1-3,5 several, DEL LAST the newest, DEL READ the\nones you have read, DEL ALL every memo. With #chan first: the channel's box.", msDel},
		"SET": {"SET NOTIFY|LIMIT ...", "memo settings",
			"SET NOTIFY {ON | LOGON | NEW | OFF}   when you hear about memos: ON = when you\n    identify and when one arrives (default), LOGON = when you identify or come\n    back from /AWAY, NEW = when one arrives. With mail on: MAIL and NOMAIL.\nSET LIMIT [#chan] <n>      keep at most n memos, up to 50 (a channel box\n                           needs the MEMO privilege)", msSet},
		"IGNORE": {"IGNORE [#chan] ADD|DEL|LIST [nick]", "drop memos from someone",
			"Memos from a nick on this list are dropped without telling the sender.\nWith #chan first: the channel box's list (MEMO privilege).", msIgnore},
	}
}

// msIdentified is identified() with MemoServ's voice.
func (s *Services) msIdentified(c *Client) (*store.Account, bool) {
	if c.accountID == 0 {
		s.ms(c, "Identify first: /ns identify <password>")
		s.logDeniedUnreg(c, s.memoServ)
		return nil, false
	}
	a, err := s.store.AccountByID(c.accountID)
	if err != nil || a == nil {
		s.dbErr(c, s.memoServ, err)
		return nil, false
	}
	return a, true
}

// memoBox is someone's own memos or a channel's box.
type memoBox struct {
	acct  *store.Account
	reg   *store.Channel // set for a channel box
	memos []store.Memo
	s     *Services
	c     *Client // the reader, for their language
}

// none, noneNew and count are whole sentences about the box, so each
// language can word them its own way.
func (b *memoBox) none() string {
	if b.reg != nil {
		return fmt.Sprintf(b.s.tr(b.c, "%s has no memos."), b.reg.Name)
	}
	return b.s.tr(b.c, "You have no memos.")
}

func (b *memoBox) noneNew() string {
	if b.reg != nil {
		return fmt.Sprintf(b.s.tr(b.c, "%s has no new memos."), b.reg.Name)
	}
	return b.s.tr(b.c, "You have no new memos.")
}

func (b *memoBox) count(n int) string {
	switch {
	case b.reg != nil && n == 1:
		return fmt.Sprintf(b.s.tr(b.c, "%s has 1 memo."), b.reg.Name)
	case b.reg != nil:
		return fmt.Sprintf(b.s.tr(b.c, "%s has %d memos."), b.reg.Name, n)
	case n == 1:
		return b.s.tr(b.c, "You have 1 memo.")
	}
	return fmt.Sprintf(b.s.tr(b.c, "You have %d memos."), n)
}

// cmd is what commands about the box start with: "/ms read" or
// "/ms read #bchat".
func (b *memoBox) cmd(verb string) string {
	if b.reg != nil {
		return "/ms " + verb + " " + b.reg.Name
	}
	return "/ms " + verb
}

// msBox opens the box a command is about: the channel named first in args,
// else c's own. It returns the remaining args.
func (s *Services) msBox(c *Client, args []string) (*memoBox, []string, bool) {
	a, ok := s.msIdentified(c)
	if !ok {
		return nil, nil, false
	}
	if len(args) == 0 || !irc.IsChannel(args[0]) {
		memos, err := s.store.Memos(a)
		if err != nil {
			s.dbErr(c, s.memoServ, err)
			return nil, nil, false
		}
		return &memoBox{acct: a, memos: memos, s: s, c: c}, args, true
	}
	reg, err := s.store.ChannelByName(args[0])
	if err != nil {
		s.dbErr(c, s.memoServ, err)
		return nil, nil, false
	}
	if reg == nil {
		s.ms(c, "%s isn't registered with ChanServ.", args[0])
		return nil, nil, false
	}
	if !s.levelHas(reg, s.accountLevel(reg, c.accountID), "MEMO") {
		s.ms(c, "You need the MEMO privilege on %s for its memo box (%s there).", reg.Name, s.needWords(s.privLevel(reg, "MEMO")))
		return nil, nil, false
	}
	return &memoBox{acct: a, reg: reg, memos: s.store.ChanMemos(reg), s: s, c: c}, args[1:], true
}

func (s *Services) boxMarkRead(b *memoBox, m store.Memo) error {
	if b.reg != nil {
		return s.store.MarkChanMemoRead(m.ID)
	}
	if err := s.store.MarkRead(m.ID); err != nil {
		return err
	}
	s.memoReceipt(b.acct, m)
	return nil
}

// memoReceipt tells the sender of an RSEND memo that owner read it, with a
// memo from MemoServ (Anope's auto-memo), unless their box is full.
func (s *Services) memoReceipt(owner *store.Account, m store.Memo) {
	if !m.Receipt || m.Read {
		return
	}
	from, err := s.store.AccountByNick(m.From)
	if err != nil || from == nil {
		return
	}
	box, err := s.store.Memos(from)
	if err != nil || len(box) >= from.MemoLimit {
		return
	}
	text := fmt.Sprintf("[auto-memo] The memo you sent to %s on %s has been read.", owner.Name, showTime(m.Sent))
	if err := s.store.AddMemo(from, "MemoServ", text, time.Now()); err != nil {
		s.log.Printf("services database: %v", err)
		return
	}
	if from.MemoNotify {
		for _, oc := range s.onlineClients(from.ID) {
			s.ms(oc, "You have a new memo from MemoServ. Read it: /ms read %d", len(box)+1)
		}
	}
}

func (s *Services) boxDelete(b *memoBox, id int64) error {
	if b.reg != nil {
		return s.store.DeleteChanMemo(id)
	}
	return s.store.DeleteMemo(id)
}

func msSend(s *Services, c *Client, args []string)  { s.memoSend(c, args, false) }
func msRSend(s *Services, c *Client, args []string) { s.memoSend(c, args, true) }

// memoSend is SEND, and RSEND when receipt is set: then the sender gets a
// memo back when it is first read.
func (s *Services) memoSend(c *Client, args []string, receipt bool) {
	cmd := map[bool]string{false: "SEND", true: "RSEND"}[receipt]
	if !c.isOper() && s.defconBlocks(c, s.memoServ, 3) {
		return
	}
	from, ok := s.msIdentified(c)
	if !ok {
		return
	}
	if len(args) < 2 {
		s.ms(c, "Syntax: %s", map[bool]string{false: "SEND {<nick> | #chan} <text>", true: "RSEND <nick> <text>"}[receipt])
		return
	}
	if receipt && irc.IsChannel(args[0]) {
		s.ms(c, "RSEND is for nicks; a channel's memo box has no single reader. Use SEND.")
		return
	}
	target, text := args[0], strings.Join(args[1:], " ")
	if d := s.cfg.Services.MemoSendDelayOr(); d > 0 && !c.isOper() && time.Since(c.lastMemoSend) < d {
		s.ms(c, "Please wait %d seconds before using the %s command again.", int(d.Seconds()), cmd)
		return
	}
	c.lastMemoSend = time.Now()
	if len(text) > maxMemoLen {
		s.ms(c, "Memos can be up to %d characters; yours has %d.", maxMemoLen, len(text))
		return
	}
	if irc.IsChannel(target) {
		s.memoToChannel(c, from, target, text)
		return
	}
	to, err := s.store.AccountByNick(target)
	if err != nil {
		s.dbErr(c, s.memoServ, err)
		return
	}
	if to == nil {
		s.ms(c, "%s isn't a registered nick.", target)
		return
	}
	ignored, err := s.store.Ignores(to, from.Name)
	if err == nil && !ignored {
		ignored, err = s.store.Ignores(to, c.user.Nick)
	}
	if err != nil {
		s.dbErr(c, s.memoServ, err)
		return
	}
	memos, err := s.store.Memos(to)
	if err != nil {
		s.dbErr(c, s.memoServ, err)
		return
	}
	if !ignored && len(memos) >= to.MemoLimit {
		s.ms(c, "%s's memo box is full.", to.Name)
		return
	}
	online := s.onlineClients(to.ID)
	if !ignored {
		if err := s.store.AddMemoReceipt(to, from.Name, text, time.Now(), receipt); err != nil {
			s.dbErr(c, s.memoServ, err)
			return
		}
		num := len(memos) + 1
		if to.MemoNotify {
			for _, oc := range online {
				s.ms(oc, "You have a new memo from %s. Read it: /ms read %d", from.Name, num)
			}
		}
		s.memoMail(to, from.Name, text, num)
	}
	if len(online) > 0 && to.MemoNotify {
		s.ms(c, "Memo sent to %s. They're online, so they were told right away.", to.Name)
	} else {
		s.ms(c, "Memo sent to %s. They'll see it the next time they identify.", to.Name)
	}
	if receipt {
		s.ms(c, "You'll get a memo from MemoServ when %s reads it.", to.Name)
	}
	s.msLog(c, "", "to send a memo to %s", args[0])
}

// memoToChannel puts a memo in a channel's box (Anope's way) and tells
// those in the channel who may read it.
func (s *Services) memoToChannel(c *Client, from *store.Account, name, text string) {
	reg, err := s.store.ChannelByName(name)
	if err != nil {
		s.dbErr(c, s.memoServ, err)
		return
	}
	if reg == nil {
		s.ms(c, "%s isn't registered with ChanServ.", name)
		return
	}
	if reg.Suspended(time.Now()) {
		s.ms(c, "%s is suspended.", reg.Name)
		return
	}
	memos := s.store.ChanMemos(reg)
	ignored := s.store.ChanIgnores(reg, from.Name) || s.store.ChanIgnores(reg, c.user.Nick)
	if !ignored && len(memos) >= reg.MemoLimit {
		s.ms(c, "The memo box of %s is full.", reg.Name)
		return
	}
	if !ignored {
		if err := s.store.AddChanMemo(reg, from.Name, text, time.Now()); err != nil {
			s.dbErr(c, s.memoServ, err)
			return
		}
		if live := s.net.Channel(reg.Name); live != nil {
			for _, m := range live.SortedMembers() {
				oc := s.clientOf(m.User)
				if oc == nil || oc.accountID == 0 || oc.accountID == from.ID || !s.levelHas(reg, s.accountLevel(reg, oc.accountID), "MEMO") {
					continue
				}
				s.ms(oc, "There is a new memo on %s from %s. Read it: /ms read %s %d", reg.Name, from.Name, reg.Name, len(memos)+1)
			}
		}
	}
	s.ms(c, "Memo sent to %s.", reg.Name)
	s.msLog(c, "", "to send a memo to %s", reg.Name)
}

// chanMemoNotice tells c about unread memos in reg's box, if c may read it.
func (s *Services) chanMemoNotice(c *Client, reg *store.Channel) {
	if c.accountID == 0 || !s.levelHas(reg, s.accountLevel(reg, c.accountID), "MEMO") {
		return
	}
	unread := 0
	for _, m := range s.store.ChanMemos(reg) {
		if !m.Read {
			unread++
		}
	}
	switch unread {
	case 0:
	case 1:
		s.ms(c, "%s has 1 new memo. Read it: /ms list %s", reg.Name, reg.Name)
	default:
		s.ms(c, "%s has %d new memos. Read them: /ms list %s", reg.Name, unread, reg.Name)
	}
}

// onlineClients returns the connections identified to account id.
func (s *Services) onlineClients(id int64) []*Client {
	var out []*Client
	for _, c := range s.clients {
		if c.accountID == id {
			out = append(out, c)
		}
	}
	return out
}

// memoSummary tells someone who just identified about unread memos.
func (s *Services) memoSummary(c *Client, a *store.Account) {
	if !a.MemoSignon {
		return
	}
	memos, err := s.store.Memos(a)
	if err != nil {
		return
	}
	var unread []int
	for i, m := range memos {
		if !m.Read {
			unread = append(unread, i+1)
		}
	}
	switch len(unread) {
	case 0:
	case 1:
		s.ms(c, "You have 1 new memo. Read it: /ms read %d", unread[0])
	default:
		s.ms(c, "You have %d new memos. Read them: /ms read new", len(unread))
	}
}

func msList(s *Services, c *Client, args []string) {
	b, args, ok := s.msBox(c, args)
	if !ok {
		return
	}
	memos := b.memos
	if len(memos) == 0 {
		s.ms(c, "%s", b.none())
		return
	}
	show := make([]int, len(memos))
	for i := range show {
		show[i] = i
	}
	if arg := argAt(args, 0); arg != "" {
		var problem string
		if show, problem = memoPick(b, arg); problem == "syntax" || strings.EqualFold(arg, "READ") {
			s.ms(c, "Syntax: LIST [#chan] [<list> | NEW]")
			return
		} else if problem != "" {
			s.ms(c, "%s", problem)
			return
		}
	}
	from, sent := s.tr(c, "From"), s.tr(c, "Sent (UTC)")
	width := max(4, utf8.RuneCountInString(from))
	for _, m := range memos {
		width = max(width, len(m.From))
	}
	if b.reg != nil {
		s.ms(c, "Memo box of %s:", b.reg.Name)
	}
	s.ms(c, "  %-3s %-*s  %-12s  %s", "#", width, from, sent, "")
	for _, i := range show {
		m := memos[i]
		state := s.tr(c, "read")
		if !m.Read {
			state = s.tr(c, "new")
		}
		s.ms(c, "  %-3d %-*s  %-12s  %s", i+1, width, m.From, m.Sent.UTC().Format("2 Jan 15:04"), state)
	}
	unread := 0
	for _, m := range memos {
		if !m.Read {
			unread++
		}
	}
	if unread == 0 {
		s.ms(c, "%s, none new. Delete one: %s <num>", plural(len(memos), "memo"), b.cmd("del"))
	} else {
		s.ms(c, "%s, %d new. Read one: %s <num>", plural(len(memos), "memo"), unread, b.cmd("read"))
	}
}

// memoPick turns "2", "1-3,5", "NEW", "LAST", "ALL" or "READ" into memo
// positions (0-based). problem is "syntax" or a sentence for the user.
func memoPick(b *memoBox, arg string) (picks []int, problem string) {
	memos := b.memos
	if arg == "" {
		return nil, "syntax"
	}
	if len(memos) == 0 {
		return nil, b.none()
	}
	switch strings.ToUpper(arg) {
	case "LAST":
		return []int{len(memos) - 1}, ""
	case "ALL":
		for i := range memos {
			picks = append(picks, i)
		}
		return picks, ""
	case "NEW", "READ":
		wantRead := strings.EqualFold(arg, "READ")
		for i, m := range memos {
			if m.Read == wantRead {
				picks = append(picks, i)
			}
		}
		if len(picks) == 0 && !wantRead {
			return nil, b.noneNew()
		}
		return picks, ""
	}
	nums, isList := numList(arg, len(memos))
	if !isList {
		return nil, "syntax"
	}
	if len(nums) == 0 {
		return nil, fmt.Sprintf(b.s.tr(b.c, "There is no memo %s."), arg) + " " + b.count(len(memos))
	}
	for _, n := range nums {
		picks = append(picks, n-1)
	}
	return picks, ""
}

func msRead(s *Services, c *Client, args []string) {
	b, args, ok := s.msBox(c, args)
	if !ok {
		return
	}
	picks, problem := memoPick(b, argAt(args, 0))
	if problem == "syntax" || strings.EqualFold(argAt(args, 0), "READ") {
		s.ms(c, "Syntax: READ [#chan] {<num> | <list> | LAST | NEW | ALL}")
		return
	}
	if problem != "" {
		s.ms(c, "%s", problem)
		return
	}
	for _, i := range picks {
		m := b.memos[i]
		where := ""
		if b.reg != nil {
			where = " on " + b.reg.Name
		}
		s.ms(c, "Memo %d%s from %s · %s", i+1, where, m.From, showTime(m.Sent))
		s.ms(c, "  %s", m.Text)
		if !m.Read {
			if err := s.boxMarkRead(b, m); err != nil {
				s.dbErr(c, s.memoServ, err)
				return
			}
		}
	}
}

func msDel(s *Services, c *Client, args []string) {
	b, args, ok := s.msBox(c, args)
	if !ok {
		return
	}
	arg := strings.ToUpper(argAt(args, 0))
	picks, problem := memoPick(b, arg)
	switch {
	case problem == "syntax" || arg == "NEW":
		s.ms(c, "Syntax: DEL [#chan] {<num> | <list> | LAST | READ | ALL}")
		return
	case problem != "":
		s.ms(c, "%s", problem)
		return
	}
	for _, i := range picks {
		if err := s.boxDelete(b, b.memos[i].ID); err != nil {
			s.dbErr(c, s.memoServ, err)
			return
		}
	}
	channel := ""
	if b.reg != nil {
		channel = b.reg.Name
	}
	switch {
	case len(picks) == 0:
	case arg == "ALL":
		s.msLog(c, channel, "on ALL memos")
	case arg == "LAST":
		s.msLog(c, channel, "on LAST memo")
	default:
		for _, i := range picks {
			s.msLog(c, channel, "on memo %d", i+1)
		}
	}
	switch {
	case len(picks) == 0:
		s.ms(c, "Nothing to delete.")
	case arg == "ALL":
		s.ms(c, "All %s deleted.", plural(len(picks), "memo"))
	case arg == "READ":
		s.ms(c, "%s you had read deleted.", plural(len(picks), "memo"))
	case len(picks) == 1:
		s.ms(c, "Memo %d deleted. The ones after it moved up.", picks[0]+1)
	default:
		s.ms(c, "%s deleted. The ones after them moved up.", plural(len(picks), "memo"))
	}
}

func msSet(s *Services, c *Client, args []string) {
	a, ok := s.msIdentified(c)
	if !ok {
		return
	}
	switch strings.ToUpper(argAt(args, 0)) {
	case "NOTIFY":
		s.msSetNotify(c, a, argAt(args, 1))
	case "LIMIT":
		b, rest, ok := s.msBox(c, args[1:])
		if !ok {
			return
		}
		top := maxMemoLimit
		if c.isOper() {
			top = maxMemoOperLimit
		}
		n, err := strconv.Atoi(argAt(rest, 0))
		if err != nil || n < 1 || n > top {
			s.ms(c, "Syntax: SET LIMIT [#chan] <n>, from 1 to %d", top)
			return
		}
		if b.reg != nil {
			b.reg.MemoLimit = n
			if err := s.store.SaveChannel(b.reg); err != nil {
				s.dbErr(c, s.memoServ, err)
				return
			}
			s.ms(c, "The memo box of %s now keeps at most %s.", b.reg.Name, plural(n, "memo"))
			return
		}
		a.MemoLimit = n
		if err := s.store.SaveAccount(a); err != nil {
			s.dbErr(c, s.memoServ, err)
			return
		}
		s.ms(c, "You now keep at most %s.", plural(n, "memo"))
	default:
		s.ms(c, "Syntax: SET NOTIFY|LIMIT ... (see /ms help set)")
	}
}

func msIgnore(s *Services, c *Client, args []string) {
	b, args, ok := s.msBox(c, args)
	if !ok {
		return
	}
	sub, nick := strings.ToUpper(argAt(args, 0)), argAt(args, 1)
	who := "You ignore"
	if b.reg != nil {
		who = "The memo box of " + b.reg.Name + " ignores"
	}
	switch {
	case sub == "LIST":
		var list []string
		if b.reg != nil {
			list = s.store.ChanMemoIgnores(b.reg)
		} else {
			var err error
			if list, err = s.store.MemoIgnores(b.acct); err != nil {
				s.dbErr(c, s.memoServ, err)
				return
			}
		}
		if len(list) == 0 {
			s.ms(c, "%s nobody.", who)
			return
		}
		s.ms(c, "Memos from these nicks are dropped: %s", strings.Join(list, ", "))
	case (sub == "ADD" || sub == "DEL") && nick == "":
		s.ms(c, "Syntax: IGNORE [#chan] %s <nick>", sub)
	case sub == "ADD":
		var added bool
		var err error
		if b.reg != nil {
			added, err = s.store.AddChanMemoIgnore(b.reg, nick)
		} else {
			added, err = s.store.AddMemoIgnore(b.acct, nick)
		}
		switch {
		case err != nil:
			s.dbErr(c, s.memoServ, err)
		case !added:
			s.ms(c, "%s %s already.", who, nick)
		default:
			s.ms(c, "Memos from %s are now dropped.", nick)
		}
	case sub == "DEL":
		var removed bool
		var err error
		if b.reg != nil {
			removed, err = s.store.DelChanMemoIgnore(b.reg, nick)
		} else {
			removed, err = s.store.DelMemoIgnore(b.acct, nick)
		}
		switch {
		case err != nil:
			s.dbErr(c, s.memoServ, err)
		case !removed:
			if b.reg != nil {
				s.ms(c, "The memo box of %s doesn't ignore %s.", b.reg.Name, nick)
			} else {
				s.ms(c, "You don't ignore %s.", nick)
			}
		default:
			s.ms(c, "Memos from %s arrive again.", nick)
		}
	default:
		s.ms(c, "Syntax: IGNORE [#chan] ADD|DEL|LIST [nick]")
	}
}
