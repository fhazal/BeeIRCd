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

// HelpServ answers help on a topic from the owner's own answers, the
// server's HELP text (commands, CHMODES, UMODES) and the services' HELP
// pages. A question in words goes to the helpers: people with user mode +h
// (opers who set it, and accounts on the helper list, who get it when they
// identify). Questions live in memory only.

const helpServIntro = "HelpServ answers questions. Ask about a command or a mode, like /msg HelpServ join or /msg HelpServ chmodes, or ask in words, like /msg HelpServ how do I get a vhost?, and a helper messages you. Commands:"

const (
	helpQuestionLife = time.Hour        // an open question goes after this
	helpAskGap       = 30 * time.Second // between two questions from one person
	helpTopicLines   = 10               // lines in one owner topic
	helpTopicLen     = 400              // characters in one line
	helpQuestionLen  = 300              // characters in a question
)

// helpState is HelpServ's memory.
type helpState struct {
	topics    map[string]store.HelpTopic // owner's topics, by folded name
	helpers   map[int64]bool             // the helper list, by account ID
	questions []*helpQuestion            // open questions, oldest first
	seq       int                        // the last question number
	asked     map[string]time.Time       // by UID: when they last asked
	server    func(topic string) []string
}

// helpQuestion is one open question.
type helpQuestion struct {
	n        int
	uid      string
	nick     string
	mask     string // ident@host as helpers see it
	text     string
	at       time.Time
	taker    string // the helper's nick once taken
	takerUID string
}

func helpServCommands() map[string]svcCmd {
	return map[string]svcCmd{
		"ON": {"ON", "go on duty (helpers)",
			"Sets user mode +h: HelpServ sends you the questions people\nask. For opers and people on the helper list, on any server.", heOn},
		"OFF": {"OFF", "go off duty (helpers)",
			"Clears user mode +h: no more questions until ON.", heOff},
		"CANCEL": {"CANCEL", "takes your question back",
			"Takes back the question you asked, if no helper closed it yet.", heCancel},
		"LIST": {"LIST", "the open questions (helpers)",
			"Lists the open questions: who asked, how long ago, and which\nhelper took each. For people with user mode +h and opers.", heList},
		"TAKE": {"TAKE <number>", "take a question (helpers)",
			"Takes a question: the person who asked is told you are\nhelping them, the other helpers that it is taken. Then\nmessage them yourself. For people with user mode +h and opers.", heTake},
		"CLOSE": {"CLOSE <number> [note]", "close a question (helpers)",
			"Closes a question when it is answered (or can't be). The\nperson who asked sees the note if you give one. For people\nwith user mode +h and opers.", heClose},
		"TOPIC": {"TOPIC ADD <name> <text> | DEL <name> | LIST", "your own answers (flag s)",
			"Your own answers, asked before the built-in help.\nTOPIC ADD <name> <text>   adds a line to topic name (a new\n                          topic if there is none); up to 10 lines\nTOPIC DEL <name>          removes the topic\nTOPIC LIST                the topics\nPeople ask with /msg HelpServ <name>. Opers with flag s.", heTopic},
		"HELPER": {"HELPER ADD <nick> | DEL <nick> | LIST", "who gets user mode +h (flag s)",
			"The helper list: registered nicks that get user mode +h\n(helper) when they identify, without being opers. Helpers\nsee the questions people ask and switch +h off and on\nwith /msg HelpServ off and on. Opers may too, or /mode <nick> +h.\nOpers with flag s.", heHelper},
	}
}

// OnServerHelp gives HelpServ the server's HELP text: f returns the lines
// of a topic ("JOIN", "CHMODES"), nil when there is none.
func (s *Services) OnServerHelp(f func(topic string) []string) { s.help.server = f }

// IsHelper reports whether account is on the HelpServ helper list.
func (s *Services) IsHelper(account string) bool {
	if account == "" || len(s.help.helpers) == 0 {
		return false
	}
	a, _ := s.store.AccountByNick(account)
	return a != nil && s.help.helpers[a.ID]
}

func (s *Services) helpLoad() {
	s.help.topics = map[string]store.HelpTopic{}
	s.help.helpers = map[int64]bool{}
	s.help.asked = map[string]time.Time{}
	topics, err := s.store.HelpTopics()
	if err != nil {
		s.log.Printf("services database: %v", err)
	}
	for _, t := range topics {
		s.help.topics[irc.Fold(t.Name)] = t
	}
	helpers, err := s.store.Helpers()
	if err != nil {
		s.log.Printf("services database: %v", err)
	}
	for _, h := range helpers {
		s.help.helpers[h.AccountID] = true
	}
	s.helpServ.groups = []helpGroup{
		{"For helpers", []string{"ON", "OFF", "LIST", "TAKE", "CLOSE"}},
		{"Commands for IRC operators", []string{"TOPIC", "HELPER"}},
	}
	s.helpServ.cmdNeed = map[string]byte{"TOPIC": 's', "HELPER": 's'}
}

func (s *Services) he(c *Client, format string, a ...any) {
	s.svcReply(c, s.helpServ, fmt.Sprintf(format, a...))
}

// helping reports whether c sees and takes questions: +h or an oper.
func (s *Services) helping(c *Client) bool {
	return c.user.HasMode('h') || c.isOper()
}

// helpAnswer is a line to HelpServ that is no HelpServ command.
func (s *Services) helpAnswer(c *Client, text string) {
	text = strings.TrimSpace(text)
	fields := strings.Fields(text)
	if len(fields) == 0 {
		return
	}
	if len(fields) <= 2 && s.helpTopic(c, fields) {
		return
	}
	if len(fields) == 1 {
		s.he(c, "I have no help on %s. Ask in words, like /msg HelpServ how do I register my nick?, and a helper answers.", fields[0])
		return
	}
	s.helpAsk(c, text)
}

// helpTopic answers a topic of one word, or "<service> <command>".
func (s *Services) helpTopic(c *Client, fields []string) bool {
	word := strings.TrimPrefix(fields[0], "/")
	if sv := s.serviceNamed(word); sv != nil && sv != s.helpServ {
		s.asHelpServ(c, func() { s.serviceHelp(c, sv, fields[1:]) })
		return true
	}
	if len(fields) != 1 {
		return false
	}
	if t, ok := s.help.topics[irc.Fold(word)]; ok {
		for _, l := range strings.Split(t.Text, "\n") {
			s.he(c, "%s", l)
		}
		return true
	}
	if s.help.server != nil {
		if lines := s.help.server(strings.ToUpper(word)); lines != nil {
			for _, l := range lines {
				s.he(c, "%s", l)
			}
			return true
		}
	}
	name := strings.ToUpper(word)
	for _, sv := range []*service{s.nickServ, s.chanServ, s.memoServ, s.hostServ, s.botServ, s.operServ} {
		if sv == nil || sv.need == 'o' && !c.isOper() {
			continue
		}
		if _, ok := sv.command(name); ok && s.canUse(c, sv, name) {
			s.he(c, "%s %s:", sv.user.Nick, name)
			s.asHelpServ(c, func() { s.serviceHelp(c, sv, []string{name}) })
			return true
		}
	}
	return false
}

// serviceNamed finds a service bot by nick ("NickServ") or alias ("ns").
func (s *Services) serviceNamed(word string) *service {
	if sv := s.services[irc.Fold(word)]; sv != nil && sv.botID == 0 && sv.alias != "" {
		return sv
	}
	for _, sv := range s.services {
		if sv.alias != "" && strings.EqualFold(sv.alias, word) {
			return sv
		}
	}
	return nil
}

// asHelpServ runs f with every service reply to c sent by HelpServ.
func (s *Services) asHelpServ(c *Client, f func()) {
	if c.reply != nil { // the panel's stand-in client: answers go there already
		f()
		return
	}
	c.reply = func(text string) { s.host.Notice(s.helpServ.user.UID, c.user.UID, text) }
	defer func() { c.reply = nil }()
	f()
}

// helpPrune drops questions that are too old or whose asker left.
func (s *Services) helpPrune(now time.Time) {
	kept := s.help.questions[:0]
	for _, q := range s.help.questions {
		if now.Sub(q.at) < helpQuestionLife && s.clients[q.uid] != nil {
			kept = append(kept, q)
		}
	}
	clear(s.help.questions[len(kept):])
	s.help.questions = kept
	for uid, at := range s.help.asked {
		if now.Sub(at) >= helpAskGap || s.clients[uid] == nil {
			delete(s.help.asked, uid)
		}
	}
}

// helpersOnline are the people who see questions, not counting c.
func (s *Services) helpersOnline(except *Client) []*Client {
	var out []*Client
	for _, h := range s.clients {
		if h != except && !h.closed && h.user.HasMode('h') {
			out = append(out, h)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].user.Nick < out[j].user.Nick })
	return out
}

func (s *Services) helpQuestionOf(uid string) *helpQuestion {
	for _, q := range s.help.questions {
		if q.uid == uid {
			return q
		}
	}
	return nil
}

// helpAsk sends c's question to the helpers.
func (s *Services) helpAsk(c *Client, text string) {
	now := time.Now()
	s.helpPrune(now)
	if q := s.helpQuestionOf(c.user.UID); q != nil {
		s.he(c, "You have a question waiting (#%d). /msg HelpServ cancel takes it back.", q.n)
		return
	}
	if at, ok := s.help.asked[c.user.UID]; ok && now.Sub(at) < helpAskGap {
		s.he(c, "Please wait a moment before asking again.")
		return
	}
	if len(text) > helpQuestionLen {
		text = text[:helpQuestionLen]
	}
	s.help.seq++
	q := &helpQuestion{n: s.help.seq, uid: c.user.UID, nick: c.user.Nick, mask: c.user.Ident + "@" + c.user.Host, text: text, at: now}
	s.help.questions = append(s.help.questions, q)
	s.help.asked[c.user.UID] = now
	helpers := s.helpersOnline(c)
	for _, h := range helpers {
		s.host.Notice(s.helpServ.user.UID, h.user.UID, fmt.Sprintf("#%d %s (%s) asks: %s  (/msg HelpServ take %d)", q.n, q.nick, q.mask, q.text, q.n))
	}
	if len(helpers) == 0 {
		s.he(c, "No helper is online right now. Your question waits for an hour; a helper who comes online sees it.")
	} else {
		s.he(c, "I sent your question to the helpers (%d online). One of them will message you.", len(helpers))
	}
	s.logTo(s.helpServ, fmt.Sprintf("HELP: #%d %s asks: %s", q.n, anopeMask(c.user), q.text))
}

// helpOnDuty tells someone who just got +h about the waiting questions.
func (s *Services) helpOnDuty(c *Client) {
	s.helpPrune(time.Now())
	open := 0
	for _, q := range s.help.questions {
		if q.taker == "" && q.uid != c.user.UID {
			open++
		}
	}
	if open > 0 {
		s.he(c, "%s waiting for a helper: /msg HelpServ list", helpCount(open, "question is", "questions are"))
	}
}

func helpCount(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return strconv.Itoa(n) + " " + many
}

// helpGone runs when someone quits: their question goes, and a helper's
// taken questions open up again.
func (s *Services) helpGone(uid string) {
	delete(s.help.asked, uid)
	kept := s.help.questions[:0]
	for _, q := range s.help.questions {
		switch {
		case q.uid == uid:
			if q.takerUID != "" {
				s.host.Notice(s.helpServ.user.UID, q.takerUID, fmt.Sprintf("#%d %s left: the question is closed.", q.n, q.nick))
			}
			continue
		case q.takerUID == uid:
			q.taker, q.takerUID = "", ""
		}
		kept = append(kept, q)
	}
	clear(s.help.questions[len(kept):])
	s.help.questions = kept
}

func heCancel(s *Services, c *Client, _ []string) {
	q := s.helpQuestionOf(c.user.UID)
	if q == nil {
		s.he(c, "You have no question waiting.")
		return
	}
	s.helpRemove(q)
	if q.takerUID != "" {
		s.host.Notice(s.helpServ.user.UID, q.takerUID, fmt.Sprintf("#%d %s took the question back.", q.n, q.nick))
	}
	s.he(c, "Your question is taken back.")
}

func (s *Services) helpRemove(q *helpQuestion) {
	for i, o := range s.help.questions {
		if o == q {
			s.help.questions = append(s.help.questions[:i], s.help.questions[i+1:]...)
			return
		}
	}
}

func (s *Services) helpNotHelper(c *Client) bool {
	if s.helping(c) {
		return false
	}
	s.helpAnswer(c, c.cmdName)
	return true
}

func heList(s *Services, c *Client, args []string) {
	if s.helpNotHelper(c) {
		return
	}
	now := time.Now()
	s.helpPrune(now)
	if len(s.help.questions) == 0 {
		s.he(c, "No questions are waiting.")
		return
	}
	for _, q := range s.help.questions {
		who := "open"
		if q.taker != "" {
			who = "taken by " + q.taker
		}
		s.he(c, "#%d %s, %s ago, %s: %s", q.n, q.nick, roundAgo(now.Sub(q.at)), who, q.text)
	}
	s.he(c, "End of list: %s.", helpCount(len(s.help.questions), "question", "questions"))
}

// roundAgo is "12s", "4m", "1h".
func roundAgo(d time.Duration) string {
	switch {
	case d < time.Minute:
		return strconv.Itoa(int(d/time.Second)) + "s"
	case d < time.Hour:
		return strconv.Itoa(int(d/time.Minute)) + "m"
	}
	return strconv.Itoa(int(d/time.Hour)) + "h"
}

// helpNumbered finds question "3" or "#3".
func (s *Services) helpNumbered(c *Client, args []string, syntax string) *helpQuestion {
	if len(args) == 0 {
		s.he(c, "Syntax: %s", syntax)
		return nil
	}
	s.helpPrune(time.Now())
	n, err := strconv.Atoi(strings.TrimPrefix(args[0], "#"))
	if err == nil {
		for _, q := range s.help.questions {
			if q.n == n {
				return q
			}
		}
	}
	s.he(c, "There is no open question %s. /msg HelpServ list shows them.", args[0])
	return nil
}

func heTake(s *Services, c *Client, args []string) {
	if s.helpNotHelper(c) {
		return
	}
	q := s.helpNumbered(c, args, "TAKE <number>")
	if q == nil {
		return
	}
	switch {
	case q.uid == c.user.UID:
		s.he(c, "That is your own question.")
		return
	case q.takerUID == c.user.UID:
		s.he(c, "You already took #%d.", q.n)
		return
	case q.taker != "":
		s.he(c, "%s already took #%d.", q.taker, q.n)
		return
	}
	q.taker, q.takerUID = c.user.Nick, c.user.UID
	s.host.Notice(s.helpServ.user.UID, q.uid, c.user.Nick+" is helping you now and will message you.")
	s.he(c, "You took #%d. Message %s: /msg %s <answer>, then /msg HelpServ close %d.", q.n, q.nick, q.nick, q.n)
	for _, h := range s.helpersOnline(c) {
		if h.user.UID != q.uid {
			s.host.Notice(s.helpServ.user.UID, h.user.UID, fmt.Sprintf("#%d (%s) is taken by %s.", q.n, q.nick, c.user.Nick))
		}
	}
	s.logTo(s.helpServ, fmt.Sprintf("HELP: %s took #%d from %s", anopeMask(c.user), q.n, q.nick))
}

func heClose(s *Services, c *Client, args []string) {
	if s.helpNotHelper(c) {
		return
	}
	q := s.helpNumbered(c, args, "CLOSE <number> [note]")
	if q == nil {
		return
	}
	if q.taker != "" && q.takerUID != c.user.UID && !c.isOper() {
		s.he(c, "%s took #%d; only they (or an oper) can close it.", q.taker, q.n)
		return
	}
	s.helpRemove(q)
	note := strings.Join(args[1:], " ")
	msg := c.user.Nick + " closed your question."
	if note != "" {
		msg += " " + note
	}
	s.host.Notice(s.helpServ.user.UID, q.uid, msg)
	s.he(c, "#%d is closed.", q.n)
	s.logTo(s.helpServ, fmt.Sprintf("HELP: %s closed #%d from %s", anopeMask(c.user), q.n, q.nick))
}

func heTopic(s *Services, c *Client, args []string) {
	sub := ""
	if len(args) > 0 {
		sub = strings.ToUpper(args[0])
	}
	switch {
	case sub == "LIST":
		if len(s.help.topics) == 0 {
			s.he(c, "There are no topics. TOPIC ADD <name> <text> adds one.")
			return
		}
		keys := make([]string, 0, len(s.help.topics))
		for k := range s.help.topics {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			t := s.help.topics[k]
			lines := strings.Split(t.Text, "\n")
			s.he(c, "%s (%s, by %s): %s", t.Name, helpCount(len(lines), "line", "lines"), t.SetBy, lines[0])
		}
		s.he(c, "End of list.")
	case sub == "ADD" && len(args) >= 3:
		name, text := args[1], strings.Join(args[2:], " ")
		if strings.ContainsAny(name, "\n\r") || len(name) > 32 {
			s.he(c, "A topic name is one word of up to 32 characters.")
			return
		}
		if len(text) > helpTopicLen {
			s.he(c, "A line can be up to %d characters.", helpTopicLen)
			return
		}
		t, had := s.help.topics[irc.Fold(name)]
		if had {
			if strings.Count(t.Text, "\n")+1 >= helpTopicLines {
				s.he(c, "Topic %s has %d lines already; TOPIC DEL %s starts it again.", t.Name, helpTopicLines, t.Name)
				return
			}
			t.Text += "\n" + text
		} else {
			t = store.HelpTopic{Name: name, Text: text}
		}
		t.SetBy, t.SetAt = c.user.Nick, time.Now()
		if err := s.store.SetHelpTopic(t); err != nil {
			s.dbErr(c, s.helpServ, err)
			return
		}
		s.help.topics[irc.Fold(name)] = t
		n := strings.Count(t.Text, "\n") + 1
		s.he(c, "Topic %s has %s now. People see it with /msg HelpServ %s", t.Name, helpCount(n, "line", "lines"), t.Name)
		s.logCmd(c, s.helpServ, "ADMIN", "", "to add a line to topic "+t.Name)
	case sub == "DEL" && len(args) == 2:
		t, had := s.help.topics[irc.Fold(args[1])]
		if !had {
			s.he(c, "There is no topic %s.", args[1])
			return
		}
		if _, err := s.store.DelHelpTopic(t.Name); err != nil {
			s.dbErr(c, s.helpServ, err)
			return
		}
		delete(s.help.topics, irc.Fold(t.Name))
		s.he(c, "Topic %s is removed.", t.Name)
		s.logCmd(c, s.helpServ, "ADMIN", "", "to remove topic "+t.Name)
	default:
		s.he(c, "Syntax: TOPIC ADD <name> <text> | DEL <name> | LIST")
	}
}

func heHelper(s *Services, c *Client, args []string) {
	sub := ""
	if len(args) > 0 {
		sub = strings.ToUpper(args[0])
	}
	switch {
	case sub == "LIST":
		hs, err := s.store.Helpers()
		if err != nil {
			s.dbErr(c, s.helpServ, err)
			return
		}
		if len(hs) == 0 {
			s.he(c, "The helper list is empty. HELPER ADD <nick> adds someone; opers set +h on themselves.")
			return
		}
		for _, h := range hs {
			name := s.store.AccountNameByID(h.AccountID)
			on := "offline"
			for _, o := range s.clients {
				if o.accountID == h.AccountID && !o.closed {
					on = "online as " + o.user.Nick
					if !o.user.HasMode('h') {
						on += " (+h off)"
					}
					break
				}
			}
			s.he(c, "%s, %s, added by %s on %s", name, on, h.AddedBy, h.AddedAt.UTC().Format("2006-01-02"))
		}
		s.he(c, "End of list: %s.", helpCount(len(hs), "helper", "helpers"))
	case (sub == "ADD" || sub == "DEL") && len(args) == 2:
		a, _ := s.store.AccountByNick(args[1])
		if a == nil {
			s.he(c, "%s isn't registered.", args[1])
			return
		}
		if sub == "ADD" {
			ok, err := s.store.AddHelper(store.Helper{AccountID: a.ID, AddedBy: c.user.Nick, AddedAt: time.Now()})
			if err != nil {
				s.dbErr(c, s.helpServ, err)
				return
			}
			if !ok {
				s.he(c, "%s is on the helper list already.", a.Name)
				return
			}
			s.help.helpers[a.ID] = true
			s.he(c, "%s is a helper now: they get user mode +h when they identify.", a.Name)
			s.logCmd(c, s.helpServ, "ADMIN", "", "to add "+a.Name+" to the helper list")
			for _, o := range s.clients {
				if o.accountID == a.ID && !o.closed {
					s.helpModeOn(o)
				}
			}
			return
		}
		ok, err := s.store.DelHelper(a.ID)
		if err != nil {
			s.dbErr(c, s.helpServ, err)
			return
		}
		if !ok {
			s.he(c, "%s isn't on the helper list.", a.Name)
			return
		}
		delete(s.help.helpers, a.ID)
		s.he(c, "%s is off the helper list.", a.Name)
		s.logCmd(c, s.helpServ, "ADMIN", "", "to remove "+a.Name+" from the helper list")
		for _, o := range s.clients {
			if o.accountID == a.ID && !o.closed && o.user.HasMode('h') && !o.isOper() {
				s.helpSetMode(o, false)
			}
		}
	default:
		s.he(c, "Syntax: HELPER ADD <nick> | DEL <nick> | LIST")
	}
}

// helpModeOn gives a listed helper +h (on identify, or HELPER ADD).
func (s *Services) helpModeOn(c *Client) {
	if c.user.HasMode('h') || !s.help.helpers[c.accountID] {
		return
	}
	s.helpSetMode(c, true)
	s.he(c, "You are a helper: user mode +h shows you the questions people ask HelpServ. /msg HelpServ off switches it off, /msg HelpServ on back on.")
}

// helpSetMode sets or clears +h as HelpServ, for people on any server.
func (s *Services) helpSetMode(c *Client, on bool) {
	if c.user.HasMode('h') == on {
		return
	}
	s.emit(&state.UserMode{Source: s.helpServ.user.UID, UID: c.user.UID, Changes: umodes(on, "h")})
}

// mayHelp reports whether c may have +h: an oper or a listed helper.
func (s *Services) mayHelp(c *Client) bool {
	return c.isOper() || c.accountID != 0 && s.help.helpers[c.accountID]
}

func heOn(s *Services, c *Client, _ []string) {
	switch {
	case c.user.HasMode('h'):
		s.he(c, "You are on duty already (user mode +h).")
	case !s.mayHelp(c):
		s.he(c, "Only opers and people on the helper list go on duty. To ask something, just type your question: /msg HelpServ how do I ...?")
	default:
		s.helpSetMode(c, true)
		s.he(c, "You are on duty: HelpServ sends you people's questions. /msg HelpServ off when you go.")
	}
}

func heOff(s *Services, c *Client, _ []string) {
	if !c.user.HasMode('h') {
		if s.mayHelp(c) {
			s.he(c, "You are off duty already.")
		} else {
			s.he(c, "Only opers and people on the helper list go on and off duty.")
		}
		return
	}
	s.helpSetMode(c, false)
	s.he(c, "You are off duty. /msg HelpServ on when you are back.")
}

// helpModeOff takes +h from someone who logged out and isn't an oper.
func (s *Services) helpModeOff(c *Client, id int64) {
	if c.user.HasMode('h') && s.help.helpers[id] && !c.isOper() {
		s.helpSetMode(c, false)
	}
}
