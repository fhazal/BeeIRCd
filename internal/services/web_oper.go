package services

// The oper pages of the Hive Panel that change services: People's
// Accounts, the Services page's Vhosts, Bots, News, Sessions and DEFCON
// tabs and Overview's notice to everyone. Reading is direct; every change
// is the real NickServ, HostServ, BotServ or OperServ command, run by a
// stand-in for the oper that carries their oper flags, so the same checks,
// notices to opers and log lines apply as on IRC.

import (
	"sort"
	"strings"
	"time"

	"github.com/fhazal/beeircd/internal/irc"
	"github.com/fhazal/beeircd/internal/store"
)

// webOperCmds are the commands the oper pages may run, by service.
var webOperCmds = map[string]map[string]bool{
	"NickServ": {"SUSPEND": true, "UNSUSPEND": true, "DROP": true, "SASET": true},
	"HostServ": {"ACTIVATE": true, "REJECT": true, "SET": true, "DEL": true},
	"BotServ":  {"BOT": true},
	"OperServ": {"GLOBAL": true, "LOGONNEWS": true, "OPERNEWS": true, "RANDOMNEWS": true, "EXCEPTION": true, "DEFCON": true, "IGNORE": true},
}

func (s *Services) webOperService(name string) *service {
	switch name {
	case "NickServ":
		return s.nickServ
	case "HostServ":
		return s.hostServ
	case "BotServ":
		return s.botServ
	case "OperServ":
		return s.operServ
	}
	return nil
}

// WebOper runs service's command args[0] with args[1:] for account id, an
// oper with flags, at the Hive Panel (ip). done gets whether the command
// made a change and its answer; it is called on the loop, at once or, while
// a password is hashed, a moment later.
func (s *Services) WebOper(id int64, ip, flags, service string, args []string, done func(ok bool, answer string)) {
	a, why := s.webAccount(id)
	if a == nil {
		done(false, why)
		return
	}
	sv := s.webOperService(service)
	if sv == nil || len(args) == 0 || !webOperCmds[service][strings.ToUpper(args[0])] {
		done(false, "That can't be done here.")
		return
	}
	for _, x := range args {
		if x == "" || strings.ContainsAny(x, "\r\n\x00") {
			done(false, "Bad request.")
			return
		}
	}
	var out []string
	c := s.webClient(a, ip, strings.ToUpper(args[0]), &out)
	c.webFlags = flags
	finished := false
	finish := func() {
		if !finished {
			finished = true
			done(c.logged, strings.Join(out, " "))
		}
	}
	c.webDone = finish
	s.toService(c, sv, strings.Join(args, " "))
	if !c.pwBusy {
		finish()
	}
}

// AccountRow is one registered account on People's Accounts list.
type AccountRow struct {
	Name        string
	Email       string
	Registered  time.Time
	LastSeen    time.Time
	Nicks       int
	Online      int // connections identified to it
	Suspended   bool
	Oper        bool
	Unconfirmed bool
}

// WebAccounts lists the registered accounts whose name, a nick of theirs
// or email holds query (or matches it, with * and ?), by name.
func (s *Services) WebAccounts(query string) []AccountRow {
	nicks, err := s.store.AllNicks()
	if err != nil {
		s.log.Printf("services database: %v", err)
	}
	byAccount := map[string][]string{}
	for _, n := range nicks {
		k := irc.Fold(n.Account)
		byAccount[k] = append(byAccount[k], n.Nick)
	}
	online := map[int64]int{}
	for _, c := range s.clients {
		if !c.closed && c.accountID != 0 {
			online[c.accountID]++
		}
	}
	q := strings.ToLower(strings.TrimSpace(query))
	match := func(x string) bool {
		x = strings.ToLower(x)
		if strings.ContainsAny(q, "*?") {
			return irc.Match(q, x)
		}
		return strings.Contains(x, q)
	}
	now := s.KeepNow() // NOEXPIRE and READONLY keep run-out suspensions in force
	var out []AccountRow
	for _, a := range s.store.AllAccounts() {
		own := byAccount[irc.Fold(a.Name)]
		if q != "" {
			hit := match(a.Name) || a.Email != "" && match(a.Email)
			for _, n := range own {
				hit = hit || match(n)
			}
			if !hit {
				continue
			}
		}
		r := AccountRow{Name: a.Name, Email: a.Email, Registered: a.Registered, LastSeen: a.LastSeen, Nicks: max(1, len(own)),
			Online: online[a.ID], Suspended: a.Suspended(now), Oper: s.isServicesOper(a), Unconfirmed: a.Unconfirmed}
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return irc.Fold(out[i].Name) < irc.Fold(out[j].Name) })
	return out
}

// AccountView is one account opened on People's Accounts list.
type AccountView struct {
	AccountRow
	NickList      []string
	LastHost      string
	SuspendedBy   string
	SuspendReason string
	SuspendedAt   time.Time
	SuspendUntil  time.Time
	VHost         string
	Founder       []string // channels it founded
	Admin         bool     // the server admin's account: only they change it
	NoExpire      bool
}

// WebAccount opens the account a nick belongs to; nil when there is none.
func (s *Services) WebAccount(nick string) *AccountView {
	a, err := s.store.AccountByNick(nick)
	if err != nil {
		s.log.Printf("services database: %v", err)
		return nil
	}
	if a == nil {
		return nil
	}
	nicks, err := s.store.Nicks(a)
	if err != nil {
		s.log.Printf("services database: %v", err)
	}
	if len(nicks) == 0 {
		nicks = []string{a.Name}
	}
	v := &AccountView{
		AccountRow: AccountRow{Name: a.Name, Email: a.Email, Registered: a.Registered, LastSeen: a.LastSeen, Nicks: len(nicks),
			Online: len(s.onlineClients(a.ID)), Suspended: a.Suspended(s.KeepNow()), Oper: s.isServicesOper(a), Unconfirmed: a.Unconfirmed},
		NickList: nicks, LastHost: a.LastHost, Admin: s.isAdminAccount(a), NoExpire: a.NoExpire,
	}
	if v.Suspended {
		v.SuspendedBy, v.SuspendReason, v.SuspendedAt, v.SuspendUntil = a.SuspendedBy, a.SuspendReason, a.SuspendedAt, a.SuspendUntil
	}
	for _, n := range nicks {
		if vh, ok := s.store.VHostOf(n); ok {
			v.VHost = vh.Mask()
			break
		}
	}
	chans, _ := s.store.Channels()
	for _, c := range chans {
		if c.FounderID == a.ID {
			v.Founder = append(v.Founder, c.Name)
		}
	}
	sort.Slice(v.Founder, func(i, j int) bool { return irc.Fold(v.Founder[i]) < irc.Fold(v.Founder[j]) })
	return v
}

// WebVHosts are the waiting HostServ requests (oldest first) and the
// vhosts in use (by nick).
func (s *Services) WebVHosts() (waiting, active []store.VHost) {
	waiting = s.store.VHostRequests()
	sort.SliceStable(waiting, func(i, j int) bool { return waiting[i].Created.Before(waiting[j].Created) })
	active = s.store.VHosts()
	sort.SliceStable(active, func(i, j int) bool { return irc.Fold(active[i].Nick) < irc.Fold(active[j].Nick) })
	return waiting, active
}

// BotRow is one BotServ bot and how many channels it sits in.
type BotRow struct {
	store.Bot
	Channels int
}

// WebBots are the BotServ bots, by nick.
func (s *Services) WebBots() []BotRow {
	var out []BotRow
	for _, b := range s.store.Bots() {
		out = append(out, BotRow{*b, len(s.store.ChannelsWithBot(b.ID))})
	}
	sort.Slice(out, func(i, j int) bool { return irc.Fold(out[i].Nick) < irc.Fold(out[j].Nick) })
	return out
}

// NewsCmd is the OperServ command for a news kind ("logon", "oper",
// "random"), "" when there is none.
func NewsCmd(kind string) string {
	for cmd, k := range newsKinds {
		if k.kind == kind {
			return cmd
		}
	}
	return ""
}

// NewsAbout says who sees a news kind and when.
func NewsAbout(kind string) string { return newsKinds[NewsCmd(kind)].when }

// MaxNews is how many lines each news list keeps.
const MaxNews = maxNews

// WebNews is one news list, in the order /os <kind>news list numbers it.
func (s *Services) WebNews(kind string) []store.News { return s.newsOf(kind) }

// NewsNumber is news item id's number in its list (from 1), 0 when gone.
func (s *Services) NewsNumber(kind string, id int64) int {
	for i, n := range s.newsOf(kind) {
		if n.ID == id {
			return i + 1
		}
	}
	return 0
}

// SessionHost is one address and how many connections it has.
type SessionHost struct {
	IP    string
	Count int
	Limit int // what it may have (0 = no limit)
}

// WebSessions are the session limit, the addresses with the most
// connections (at most max) and the exceptions.
func (s *Services) WebSessions(max int) (limit int, busiest []SessionHost, excepts []store.SessionExcept) {
	limit = s.cfg.Limits.SessionLimit
	for ip, n := range s.sessionCounts() {
		if ip == "" {
			continue
		}
		busiest = append(busiest, SessionHost{IP: ip, Count: n, Limit: s.SessionLimitFor(ip, "")})
	}
	sort.Slice(busiest, func(i, j int) bool {
		if busiest[i].Count != busiest[j].Count {
			return busiest[i].Count > busiest[j].Count
		}
		return busiest[i].IP < busiest[j].IP
	})
	if len(busiest) > max {
		busiest = busiest[:max]
	}
	now := s.banNow() // NOEXPIRE keeps run-out entries in force
	for _, e := range s.sessionExcepts {
		if e.Expires.IsZero() || now.Before(e.Expires) {
			excepts = append(excepts, e)
		}
	}
	return limit, busiest, excepts
}

// DefconWords says what DEFCON level n does.
func DefconWords(n int) string { return defconWords[n] }

// WebIgnores are the live OperServ IGNORE entries.
func (s *Services) WebIgnores() []store.Ignore {
	now := time.Now()
	var out []store.Ignore
	for _, g := range s.ignores {
		if g.Expires.IsZero() || g.Expires.Unix() == 0 || now.Before(g.Expires) {
			out = append(out, g)
		}
	}
	return out
}

// NameBanMatch is how many people now online (opers not counted) a forbid
// on name would move off their nick or kick out of a channel, as
// enforceForbid does.
func (s *Services) NameBanMatch(name string) (n int, problem string) {
	name = strings.TrimSpace(name)
	if name == "" || strings.ContainsAny(name, " ,\r\n") {
		return 0, "Give one nick or channel name, * and ? allowed."
	}
	if strings.Trim(name, "*?#") == "" {
		return 0, name + " would match every name. Give a narrower one."
	}
	if irc.IsChannel(name) {
		for _, ch := range s.net.Channels() {
			if !irc.Match(name, ch.Name) {
				continue
			}
			for _, m := range ch.SortedMembers() {
				if oc := s.clientOf(m.User); oc != nil && !oc.isOper() {
					n++
				}
			}
		}
		return n, ""
	}
	for _, c := range s.clients {
		if !c.closed && !c.isOper() && irc.Match(name, c.user.Nick) {
			n++
		}
	}
	return n, ""
}
