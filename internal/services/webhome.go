package services

import (
	"sort"
	"strings"
	"time"

	"github.com/fhazal/beeircd/internal/irc"
	"github.com/fhazal/beeircd/internal/state"
	"github.com/fhazal/beeircd/internal/store"
)

// Member Home and the Memos page of the Hive Panel. Reading is direct and only ever about the signed-in
// account; sending is the real MemoServ SEND through a stand-in client, so
// the same limits, ignores, notices and log lines apply as on IRC.

// webClient is a stand-in for account a at the Hive Panel (ip): replies
// land in *out instead of a notice.
func (s *Services) webClient(a *store.Account, ip, cmd string, out *[]string) *Client {
	return &Client{s: s, ip: ip, accountID: a.ID, cmdName: cmd,
		user:  &state.User{Nick: a.Name, Ident: "hive", Host: "hive-panel", RealHost: ip, IP: ip, Account: a.Name},
		reply: func(text string) { *out = append(*out, text) }}
}

// webAccount is the live account behind id, or why not.
func (s *Services) webAccount(id int64) (*store.Account, string) {
	a, err := s.store.AccountByID(id)
	if err != nil {
		s.log.Printf("services database: %v", err)
		return nil, "Something went wrong on the server."
	}
	if a == nil {
		return nil, "Your account no longer exists."
	}
	if a.Suspended(time.Now()) {
		return nil, a.Name + " is suspended."
	}
	return a, ""
}

// WebMemos returns account id's memos (oldest first, like /ms list) and
// how many the box keeps.
func (s *Services) WebMemos(id int64) ([]store.Memo, int, string) {
	a, why := s.webAccount(id)
	if a == nil {
		return nil, 0, why
	}
	memos, err := s.store.Memos(a)
	if err != nil {
		s.log.Printf("services database: %v", err)
		return nil, 0, "Something went wrong on the server."
	}
	return memos, a.MemoLimit, ""
}

// ownMemo finds memo memoID in account a's own box: its place (from 1) and
// the memo. Another account's memo is never found.
func (s *Services) ownMemo(a *store.Account, memoID int64) (int, store.Memo, bool) {
	memos, err := s.store.Memos(a)
	if err != nil {
		return 0, store.Memo{}, false
	}
	for i, m := range memos {
		if m.ID == memoID {
			return i + 1, m, true
		}
	}
	return 0, store.Memo{}, false
}

// WebMemoRead marks memo memoID read (opening it on the Memos page), like
// /ms read <num>.
func (s *Services) WebMemoRead(id, memoID int64) string {
	a, why := s.webAccount(id)
	if a == nil {
		return why
	}
	_, m, ok := s.ownMemo(a, memoID)
	if !ok {
		return "That memo is gone."
	}
	if !m.Read {
		if err := s.store.MarkRead(m.ID); err != nil {
			s.log.Printf("services database: %v", err)
			return "Something went wrong on the server."
		}
		s.memoReceipt(a, m)
	}
	return ""
}

// WebMemoDel deletes memo memoID, like /ms del <num>, and logs it the same.
func (s *Services) WebMemoDel(id, memoID int64, ip string) string {
	a, why := s.webAccount(id)
	if a == nil {
		return why
	}
	num, m, ok := s.ownMemo(a, memoID)
	if !ok {
		return "That memo is gone."
	}
	if err := s.store.DeleteMemo(m.ID); err != nil {
		s.log.Printf("services database: %v", err)
		return "Something went wrong on the server."
	}
	var out []string
	s.msLog(s.webClient(a, ip, "DEL", &out), "", "on memo %d", num)
	return ""
}

// WebMemoSend runs MemoServ SEND to (a nick or #channel) with text for
// account id. It returns whether the memo went and MemoServ's answer.
func (s *Services) WebMemoSend(id int64, ip, to, text string) (bool, string) {
	a, why := s.webAccount(id)
	if a == nil {
		return false, why
	}
	to, text = strings.TrimSpace(to), strings.TrimSpace(text)
	if to == "" || text == "" || strings.ContainsAny(to+text, "\r\n\x00") || strings.ContainsRune(to, ' ') {
		return false, "Give a nick (or #channel) and a message."
	}
	var out []string
	c := s.webClient(a, ip, "SEND", &out)
	if s.webMemoSent == nil {
		s.webMemoSent = map[int64]time.Time{}
	}
	c.lastMemoSend = s.webMemoSent[a.ID] // senddelay counts across page loads
	msSend(s, c, []string{to, text})
	s.webMemoSent[a.ID] = c.lastMemoSend
	answer := strings.Join(out, " ")
	return strings.HasPrefix(answer, "Memo sent to "), answer
}

// HomeAccess is one entry for Member Home's "Access lists" card.
type HomeAccess struct {
	Channel string
	Who     string // account, or the AKICK mask
	Level   string // SOP, AOP ... or "AKICK"
	By      string
	At      time.Time
}

// HomeChan is one of the member's channels.
type HomeChan struct {
	Name    string
	Level   string
	Founder bool
}

// HomeNeed is something on Member Home's "Needs you" card.
type HomeNeed struct {
	Text, Hint, Page string
}

// Home is what Member Home shows that services know.
type Home struct {
	Name       string
	VHost      string
	Registered time.Time
	Nicks      []string
	Memos      []store.Memo
	MemoLimit  int
	Channels   []HomeChan
	OnMyLists  int // people on the access lists of channels they founded
	Access     []HomeAccess
	Needs      []HomeNeed
}

// homeAccessMax is how many access changes Home lists.
const homeAccessMax = 5

// WebHome gathers Member Home for account id. Only what the member may
// see on IRC: their own account and memos; access lists and AKICKs only of
// channels where they hold ACCESS_LIST / AKICK, plus their own entries.
func (s *Services) WebHome(id int64) (*Home, string) {
	a, why := s.webAccount(id)
	if a == nil {
		return nil, why
	}
	h := &Home{Name: a.Name, Registered: a.Registered, MemoLimit: a.MemoLimit, Channels: []HomeChan{}, Access: []HomeAccess{}, Needs: []HomeNeed{}}
	h.Nicks, _ = s.store.Nicks(a)
	if v, ok := s.store.VHostOf(a.Name); ok {
		h.VHost = v.Host
		if v.Ident != "" {
			h.VHost = v.Ident + "@" + v.Host
		}
	}
	memos, err := s.store.Memos(a)
	if err != nil {
		s.log.Printf("services database: %v", err)
		return nil, "Something went wrong on the server."
	}
	h.Memos = memos
	chans, err := s.store.Channels()
	if err != nil {
		s.log.Printf("services database: %v", err)
		return nil, "Something went wrong on the server."
	}
	people := map[int64]bool{}
	for _, reg := range chans {
		lvl := s.accountLevel(reg, a.ID)
		mine := lvl.level != store.LevelNone
		list, _ := s.store.AccessList(reg, 0)
		for _, e := range list {
			if e.AccountID == a.ID || (mine && s.levelHas(reg, lvl, "ACCESS_LIST")) {
				h.Access = append(h.Access, HomeAccess{Channel: reg.Name, Who: e.Account, Level: store.LevelName(e.Level), By: e.AddedBy, At: e.AddedAt})
			}
			if reg.FounderID == a.ID && e.AccountID != a.ID {
				people[e.AccountID] = true
			}
		}
		if !mine {
			continue
		}
		if s.levelHas(reg, lvl, "AKICK") {
			kicks, _ := s.store.Akicks(reg)
			for _, k := range kicks {
				h.Access = append(h.Access, HomeAccess{Channel: reg.Name, Who: k.Mask, Level: "AKICK", By: k.AddedBy, At: k.AddedAt})
			}
		}
		founder := reg.FounderID == a.ID
		h.Channels = append(h.Channels, HomeChan{Name: reg.Name, Level: lvl.name(), Founder: founder})
		if founder && reg.SuccessorID == 0 {
			h.Needs = append(h.Needs, HomeNeed{Text: reg.Name + " has no successor",
				Hint: "If your nick expires, nobody takes the channel over: /msg ChanServ SET " + reg.Name + " SUCCESSOR <nick>", Page: "chan/" + reg.Name}) // the channel manager sets it
		}
	}
	h.OnMyLists = len(people)
	sort.Slice(h.Channels, func(i, j int) bool { return irc.Fold(h.Channels[i].Name) < irc.Fold(h.Channels[j].Name) })
	sort.SliceStable(h.Access, func(i, j int) bool { return h.Access[i].At.After(h.Access[j].At) })
	if len(h.Access) > homeAccessMax {
		h.Access = h.Access[:homeAccessMax]
	}
	if a.Email == "" {
		h.Needs = append([]HomeNeed{{Text: "Your nick has no email", Hint: "Without one you can't get a lost password back: /msg NickServ SET EMAIL <address>", Page: ""}}, h.Needs...)
	}
	if a.MemoLimit > 0 && len(memos) >= a.MemoLimit-1 {
		h.Needs = append(h.Needs, HomeNeed{Text: "Your memo box is almost full", Hint: "New memos are refused when it is full. Delete the ones you don't need.", Page: "memos"})
	}
	return h, ""
}
