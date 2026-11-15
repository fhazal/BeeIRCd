package services

import (
	"strings"

	"github.com/fhazal/beeircd/internal/store"
)

// The Hive Panel's channel manager.
// A channel page shows what the person's ChanServ level may see; every
// change is the real ChanServ command through a stand-in client, so the
// checks, answers and log lines are the ones IRC has. No oper overrides:
// the stand-in never has oper flags.

// ChanCan is what someone's level on a channel allows (Anope privileges).
type ChanCan struct {
	AccessList, AccessChange, Akick, Set, Topic, Memo, Founder bool
}

// ChanView is one channel as the channel manager shows it.
type ChanView struct {
	Reg       *store.Channel
	Level     string
	Can       ChanCan
	Access    []store.AccessEntry // with AccessList
	Akicks    []store.Akick       // with Akick
	Memos     []store.Memo        // with Memo: the channel's memo box
	MemoLimit int
}

// webChanToggles are the SET options the panel shows as switches.
var webChanToggles = []string{"KEEPTOPIC", "TOPICLOCK", "SECUREOPS", "RESTRICTED", "PEACE", "PRIVATE", "SIGNKICK", "PERSIST", "KEEPMODES", "SECURE"}

// ChanToggles reads reg's switches by SET option name.
func ChanToggles(reg *store.Channel) map[string]bool {
	return map[string]bool{"KEEPTOPIC": reg.KeepTopic, "TOPICLOCK": reg.TopicLock, "SECUREOPS": reg.SecureOps, "RESTRICTED": reg.Restricted,
		"PEACE": reg.Peace, "PRIVATE": reg.Private, "SIGNKICK": reg.SignKick, "PERSIST": reg.Persist, "KEEPMODES": reg.KeepModes, "SECURE": reg.Secure}
}

// WebChan is channel name for account id: only for someone on its access
// list (or its founder).
func (s *Services) WebChan(id int64, name string) (*ChanView, string) {
	a, why := s.webAccount(id)
	if a == nil {
		return nil, why
	}
	reg, err := s.store.ChannelByName(name)
	if err != nil {
		s.log.Printf("services database: %v", err)
		return nil, "Something went wrong on the server."
	}
	var lvl access
	if reg != nil {
		lvl = s.accountLevel(reg, a.ID)
	}
	if reg == nil || lvl.level == store.LevelNone {
		return nil, "You aren't on the access list of " + name + "."
	}
	v := &ChanView{Reg: reg, Level: lvl.name(), MemoLimit: reg.MemoLimit, Can: ChanCan{
		AccessList: s.levelHas(reg, lvl, "ACCESS_LIST"), AccessChange: s.levelHas(reg, lvl, "ACCESS_CHANGE"),
		Akick: s.levelHas(reg, lvl, "AKICK"), Set: s.levelHas(reg, lvl, "SET"), Topic: s.levelHas(reg, lvl, "TOPIC"),
		Memo: s.levelHas(reg, lvl, "MEMO"), Founder: lvl.level == store.LevelFounder}}
	if v.Can.AccessList {
		v.Access, _ = s.store.AccessList(reg, 0)
	}
	if v.Can.Akick {
		v.Akicks, _ = s.store.Akicks(reg)
	}
	if v.Can.Memo {
		v.Memos = s.store.ChanMemos(reg)
	}
	return v, ""
}

// webChanCmds are the ChanServ commands the channel manager runs, with the
// subcommands or SET options it may use.
var webChanCmds = map[string]map[string]bool{
	"ACCESS": {"ADD": true, "DEL": true},
	"AKICK":  {"ADD": true, "DEL": true},
	"SET":    {"DESCRIPTION": true, "URL": true, "SUCCESSOR": true, "FOUNDER": true},
	"TOPIC":  nil, // TOPIC #chan <text>
	"DROP":   nil, // DROP #chan #chan (the name twice, as on IRC)
}

func init() {
	for _, t := range webChanToggles {
		webChanCmds["SET"][t] = true
	}
}

// WebChanServ runs ChanServ cmd #chan args... for account id at the Hive
// Panel (ip). It returns whether ChanServ made the change and its answer.
func (s *Services) WebChanServ(id int64, ip, cmd, channel string, args []string) (bool, string) {
	a, why := s.webAccount(id)
	if a == nil {
		return false, why
	}
	cmd = strings.ToUpper(cmd)
	subs, ok := webChanCmds[cmd]
	if !ok {
		return false, "That can't be done here."
	}
	if subs != nil && (len(args) == 0 || !subs[strings.ToUpper(args[0])]) {
		return false, "That can't be done here."
	}
	for _, x := range args {
		if strings.ContainsAny(x, "\r\n\x00") {
			return false, "Bad request."
		}
	}
	if cmd == "TOPIC" { // always TOPIC #chan SET <text>: a topic starting "Append ..." or "Lock ..." is still a topic
		args = append([]string{"SET"}, strings.Join(args, " "))
	}
	full := append([]string{channel}, args...)
	if cmd == "SET" { // SET #chan OPTION value
		full = append([]string{channel, strings.ToUpper(args[0])}, args[1:]...)
	}
	var out []string
	c := s.webClient(a, ip, cmd, &out)
	chanServCommands()[cmd].run(s, c, full)
	answer := strings.Join(out, " ")
	if cmd == "TOPIC" && c.logged && answer == "" { // on IRC the new topic itself is the answer
		answer = "The topic of " + channel + " is set."
	}
	return c.logged, answer
}

// ownChanMemo finds memo memoID in reg's box.
func (s *Services) ownChanMemo(reg *store.Channel, memoID int64) (store.Memo, bool) {
	for _, m := range s.store.ChanMemos(reg) {
		if m.ID == memoID {
			return m, true
		}
	}
	return store.Memo{}, false
}

// WebChanMemo reads (marks read) or deletes memo memoID of channel name's
// box, for someone with the MEMO privilege there.
func (s *Services) WebChanMemo(id int64, ip, name string, memoID int64, del bool) string {
	v, why := s.WebChan(id, name)
	if v == nil {
		return why
	}
	if !v.Can.Memo {
		return "You need the MEMO privilege on " + v.Reg.Name + " for its memo box."
	}
	m, ok := s.ownChanMemo(v.Reg, memoID)
	if !ok {
		return "That memo is gone."
	}
	var err error
	switch {
	case del:
		err = s.store.DeleteChanMemo(m.ID)
		if err == nil {
			a, _ := s.webAccount(id)
			var out []string
			s.msLog(s.webClient(a, ip, "DEL", &out), v.Reg.Name, "on a memo from %s", m.From)
		}
	case !m.Read:
		err = s.store.MarkChanMemoRead(m.ID)
	}
	if err != nil {
		s.log.Printf("services database: %v", err)
		return "Something went wrong on the server."
	}
	return ""
}
