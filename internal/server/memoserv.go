package server

import (
	"fmt"
	"strconv"
	"strings"
	"time"

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
		"SEND": {"SEND <nick> <text>", "leave a memo for someone",
			"Stores a message for a registered nick. They get it at once when online,\nor the next time they identify.\nExample: /ms send nova Meeting about the new leaf, Friday 21:00 in #ops", msSend},
		"LIST": {"LIST", "your memos",
			"Lists your memos: number, sender, date and whether you read it.", msList},
		"READ": {"READ <num>|NEW|LAST", "read a memo",
			"READ 2 reads memo 2, READ NEW all unread ones, READ LAST the newest.", msRead},
		"DEL": {"DEL <num>|ALL|READ", "delete memos",
			"DEL 2 deletes memo 2, DEL READ the ones you have read, DEL ALL every memo.", msDel},
		"SET": {"SET NOTIFY|LIMIT ...", "memo settings",
			"SET NOTIFY ON|OFF   be told the moment a memo arrives (on)\nSET LIMIT <n>       keep at most n memos, up to 50", msSet},
		"IGNORE": {"IGNORE ADD|DEL|LIST [nick]", "drop memos from someone",
			"Memos from a nick on this list are dropped without telling the sender.", msIgnore},
	}
}

// msIdentified is identified() with MemoServ's voice.
func (s *Server) msIdentified(c *Client) (*store.Account, bool) {
	if c.accountID == 0 {
		s.ms(c, "Identify first: /ns identify <password>")
		return nil, false
	}
	a, err := s.store.AccountByID(c.accountID)
	if err != nil || a == nil {
		s.dbErr(c, s.memoServ, err)
		return nil, false
	}
	return a, true
}

func msSend(s *Server, c *Client, args []string) {
	from, ok := s.msIdentified(c)
	if !ok {
		return
	}
	if len(args) < 2 {
		s.ms(c, "Syntax: SEND <nick> <text>")
		return
	}
	target, text := args[0], strings.Join(args[1:], " ")
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
		if err := s.store.AddMemo(to, from.Name, text, time.Now()); err != nil {
			s.dbErr(c, s.memoServ, err)
			return
		}
		num := len(memos) + 1
		if to.MemoNotify {
			for _, oc := range online {
				s.ms(oc, "You have a new memo from %s. Read it: /ms read %d", from.Name, num)
			}
		}
	}
	if len(online) > 0 && to.MemoNotify {
		s.ms(c, "Memo sent to %s. They're online, so they were told right away.", to.Name)
	} else {
		s.ms(c, "Memo sent to %s. They'll see it the next time they identify.", to.Name)
	}
}

// onlineClients returns the connections identified to account id.
func (s *Server) onlineClients(id int64) []*Client {
	var out []*Client
	for _, c := range s.clients {
		if c.accountID == id {
			out = append(out, c)
		}
	}
	return out
}

// memoSummary tells someone who just identified about unread memos.
func (s *Server) memoSummary(c *Client, a *store.Account) {
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

func msList(s *Server, c *Client, args []string) {
	a, ok := s.msIdentified(c)
	if !ok {
		return
	}
	memos, err := s.store.Memos(a)
	if err != nil {
		s.dbErr(c, s.memoServ, err)
		return
	}
	if len(memos) == 0 {
		s.ms(c, "You have no memos.")
		return
	}
	width := 4
	for _, m := range memos {
		width = max(width, len(m.From))
	}
	s.ms(c, "  %-3s %-*s  %-12s  %s", "#", width, "From", "Sent", "")
	unread := 0
	for i, m := range memos {
		state := "read"
		if !m.Read {
			state = "new"
			unread++
		}
		s.ms(c, "  %-3d %-*s  %-12s  %s", i+1, width, m.From, m.Sent.UTC().Format("2 Jan 15:04"), state)
	}
	if unread == 0 {
		s.ms(c, "%s, none new. Delete one: /ms del <num>", plural(len(memos), "memo"))
	} else {
		s.ms(c, "%s, %d new. Read one: /ms read <num>", plural(len(memos), "memo"), unread)
	}
}

// memoPick turns "2", "NEW", "LAST" into memo positions (0-based).
func memoPick(memos []store.Memo, arg string) ([]int, string) {
	switch strings.ToUpper(arg) {
	case "":
		return nil, "syntax"
	case "LAST":
		if len(memos) == 0 {
			return nil, "You have no memos."
		}
		return []int{len(memos) - 1}, ""
	case "NEW":
		var out []int
		for i, m := range memos {
			if !m.Read {
				out = append(out, i)
			}
		}
		if len(out) == 0 {
			return nil, "You have no new memos."
		}
		return out, ""
	}
	n, err := strconv.Atoi(arg)
	if err != nil || n < 1 || n > len(memos) {
		if len(memos) == 0 {
			return nil, "You have no memos."
		}
		return nil, fmt.Sprintf("There is no memo %s. You have %s.", arg, plural(len(memos), "memo"))
	}
	return []int{n - 1}, ""
}

func msRead(s *Server, c *Client, args []string) {
	a, ok := s.msIdentified(c)
	if !ok {
		return
	}
	memos, err := s.store.Memos(a)
	if err != nil {
		s.dbErr(c, s.memoServ, err)
		return
	}
	picks, problem := memoPick(memos, argAt(args, 0))
	if problem == "syntax" {
		s.ms(c, "Syntax: READ <num>|NEW|LAST")
		return
	}
	if problem != "" {
		s.ms(c, "%s", problem)
		return
	}
	for _, i := range picks {
		m := memos[i]
		s.ms(c, "Memo %d from %s · %s", i+1, m.From, showTime(m.Sent))
		s.ms(c, "  %s", m.Text)
		if !m.Read {
			if err := s.store.MarkRead(m.ID); err != nil {
				s.dbErr(c, s.memoServ, err)
				return
			}
		}
	}
}

func msDel(s *Server, c *Client, args []string) {
	a, ok := s.msIdentified(c)
	if !ok {
		return
	}
	memos, err := s.store.Memos(a)
	if err != nil {
		s.dbErr(c, s.memoServ, err)
		return
	}
	arg := strings.ToUpper(argAt(args, 0))
	var picks []int
	switch arg {
	case "":
		s.ms(c, "Syntax: DEL <num>|ALL|READ")
		return
	case "ALL":
		for i := range memos {
			picks = append(picks, i)
		}
	case "READ":
		for i, m := range memos {
			if m.Read {
				picks = append(picks, i)
			}
		}
	default:
		var problem string
		if picks, problem = memoPick(memos, arg); problem != "" {
			s.ms(c, "%s", problem)
			return
		}
	}
	for _, i := range picks {
		if err := s.store.DeleteMemo(memos[i].ID); err != nil {
			s.dbErr(c, s.memoServ, err)
			return
		}
	}
	switch {
	case len(picks) == 0:
		s.ms(c, "Nothing to delete.")
	case arg == "ALL":
		s.ms(c, "All %s deleted.", plural(len(picks), "memo"))
	case arg == "READ":
		s.ms(c, "%s you had read deleted.", plural(len(picks), "memo"))
	default:
		s.ms(c, "Memo %d deleted. The ones after it moved up one number.", picks[0]+1)
	}
}

func msSet(s *Server, c *Client, args []string) {
	a, ok := s.msIdentified(c)
	if !ok {
		return
	}
	switch strings.ToUpper(argAt(args, 0)) {
	case "NOTIFY":
		on, ok := onOff(argAt(args, 1))
		if !ok {
			s.ms(c, "Syntax: SET NOTIFY ON|OFF")
			return
		}
		a.MemoNotify = on
		if err := s.store.SaveAccount(a); err != nil {
			s.dbErr(c, s.memoServ, err)
			return
		}
		if on {
			s.ms(c, "You'll be told the moment a memo arrives.")
		} else {
			s.ms(c, "You'll hear about new memos when you identify.")
		}
	case "LIMIT":
		top := maxMemoLimit
		if c.user.HasMode('o') {
			top = maxMemoOperLimit
		}
		n, err := strconv.Atoi(argAt(args, 1))
		if err != nil || n < 1 || n > top {
			s.ms(c, "Syntax: SET LIMIT <n>, from 1 to %d", top)
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

func msIgnore(s *Server, c *Client, args []string) {
	a, ok := s.msIdentified(c)
	if !ok {
		return
	}
	sub, nick := strings.ToUpper(argAt(args, 0)), argAt(args, 1)
	switch {
	case sub == "LIST":
		list, err := s.store.MemoIgnores(a)
		if err != nil {
			s.dbErr(c, s.memoServ, err)
			return
		}
		if len(list) == 0 {
			s.ms(c, "You ignore nobody.")
			return
		}
		s.ms(c, "Memos from these nicks are dropped: %s", strings.Join(list, ", "))
	case (sub == "ADD" || sub == "DEL") && nick == "":
		s.ms(c, "Syntax: IGNORE %s <nick>", sub)
	case sub == "ADD":
		if added, err := s.store.AddMemoIgnore(a, nick); err != nil {
			s.dbErr(c, s.memoServ, err)
		} else if !added {
			s.ms(c, "You ignore %s already.", nick)
		} else {
			s.ms(c, "Memos from %s are now dropped.", nick)
		}
	case sub == "DEL":
		if removed, err := s.store.DelMemoIgnore(a, nick); err != nil {
			s.dbErr(c, s.memoServ, err)
		} else if !removed {
			s.ms(c, "You don't ignore %s.", nick)
		} else {
			s.ms(c, "Memos from %s arrive again.", nick)
		}
	default:
		s.ms(c, "Syntax: IGNORE ADD|DEL|LIST [nick]")
	}
}
