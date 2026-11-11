package services

import (
	"fmt"
	"math/rand/v2"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/fhazal/beeircd/internal/irc"
	"github.com/fhazal/beeircd/internal/passwd"
	"github.com/fhazal/beeircd/internal/state"
	"github.com/fhazal/beeircd/internal/store"
)

// svcCmd is one service command.
type svcCmd struct {
	syntax string // shown in HELP, e.g. "REGISTER <password> [email]"
	short  string // one line in the HELP list
	long   string // HELP <command> text; lines split on \n
	run    func(s *Services, c *Client, args []string)
}

// service is a built-in bot like NickServ.
type service struct {
	user  *state.User
	alias string // "ns"
	intro string // first line of HELP
	cmds  map[string]svcCmd
	order []string // HELP order
	need  byte     // 'o' = opers only, 'a' = oper flag a, 0 = everyone
}

// reservedNicks can't be used by people even before their service exists.
var reservedNicks = []string{"NickServ", "ChanServ", "MemoServ", "OperServ", "AdminServ", "Global"}

func isReserved(nick string) bool {
	for _, r := range reservedNicks {
		if irc.EqualFold(r, nick) {
			return true
		}
	}
	return false
}

// startServices creates the service bots.
func (s *Services) startServices() {
	s.services = make(map[string]*service)
	s.nickServ = s.addService("NickServ", "ns", "Nickname Services", "NickServ keeps your nick yours. Commands:", nickServCommands())
	s.chanServ = s.addService("ChanServ", "cs", "Channel Services", "ChanServ keeps a channel yours. Commands:", chanServCommands())
	s.memoServ = s.addService("MemoServ", "ms", "Memo Services", "MemoServ keeps messages for people who are away. Commands:", memoServCommands())
	s.operServ = s.addService("OperServ", "os", "Operator Services", "OperServ is the toolbox for IRC operators. Commands:", operServCommands())
	s.operServ.need = 'o'
	s.global = s.addService("Global", "", "Network Announcements", "Global sends network notices. It takes no commands.", map[string]svcCmd{})
}

func (s *Services) addService(nick, alias, realname, intro string, cmds map[string]svcCmd, order ...string) *service {
	u := &state.User{
		UID:      s.net.NextUID(),
		Nick:     nick,
		Ident:    "services",
		Host:     s.cfg.Server.Name,
		RealHost: s.cfg.Server.Name,
		Realname: realname,
		Server:   s.cfg.Server.Name,
		SignOn:   s.host.Stats().Started,
		Service:  true,
	}
	if err := s.emit(&state.UserAdd{User: u}); err != nil {
		panic("service nick taken at start: " + nick)
	}
	sv := &service{user: u, alias: alias, intro: intro, cmds: cmds}
	for name := range cmds {
		sv.order = append(sv.order, name)
	}
	sort.Strings(sv.order)
	s.services[irc.Fold(nick)] = sv
	return sv
}

// serviceFor returns the service behind a user, or nil.
func (s *Services) serviceFor(u *state.User) *service {
	if !u.Service {
		return nil
	}
	return s.services[irc.Fold(u.Nick)]
}

// toService runs one line sent to a service.
func (s *Services) toService(c *Client, sv *service, text string) {
	fields := strings.Fields(text)
	if len(fields) == 0 {
		return
	}
	name := strings.ToUpper(fields[0])
	args := fields[1:]
	switch {
	case sv.need == 'o' && !c.isOper():
		s.svcReply(c, sv, sv.user.Nick+" is for IRC operators.")
		return
	case sv.need == 'a' && !c.can('a'):
		s.svcReply(c, sv, sv.user.Nick+" is for server admins (oper flag a).")
		return
	case sv.alias == "":
		s.svcReply(c, sv, sv.intro)
		return
	}
	if name == "HELP" {
		s.serviceHelp(c, sv, args)
		return
	}
	cmd, ok := sv.cmds[name]
	if !ok {
		s.svcReply(c, sv, fmt.Sprintf("Unknown command %s. Type /%s help for a list.", fields[0], sv.alias))
		return
	}
	cmd.run(s, c, args)
}

func (s *Services) serviceHelp(c *Client, sv *service, args []string) {
	if len(args) > 0 {
		cmd, ok := sv.cmds[strings.ToUpper(args[0])]
		if !ok {
			s.svcReply(c, sv, fmt.Sprintf("No help for %s. Type /%s help for a list.", args[0], sv.alias))
			return
		}
		s.svcReply(c, sv, "Syntax: "+cmd.syntax)
		for _, l := range strings.Split(cmd.long, "\n") {
			s.svcReply(c, sv, "  "+l)
		}
		return
	}
	s.svcReply(c, sv, sv.intro)
	width := 0
	for _, name := range sv.order {
		width = max(width, len(sv.cmds[name].syntax))
	}
	for _, name := range sv.order {
		cmd := sv.cmds[name]
		s.svcReply(c, sv, fmt.Sprintf("  %-*s  %s", width, cmd.syntax, cmd.short))
	}
	s.svcReply(c, sv, fmt.Sprintf("More on one command: /%s help <command>", sv.alias))
}

// svcReply sends a notice from a service.
func (s *Services) svcReply(c *Client, sv *service, text string) {
	s.host.Notice(sv.user.UID, c.user.UID, text)
}

func (s *Services) ns(c *Client, format string, a ...any) {
	s.svcReply(c, s.nickServ, fmt.Sprintf(format, a...))
}
func (s *Services) ms(c *Client, format string, a ...any) {
	s.svcReply(c, s.memoServ, fmt.Sprintf(format, a...))
}

// dbErr reports a database failure without details to the user.
func (s *Services) dbErr(c *Client, sv *service, err error) {
	s.log.Printf("services database: %v", err)
	s.svcReply(c, sv, "Something went wrong on the server. Please try again in a moment.")
}

// Passwords are checked off the loop, so a slow hash never stalls chat.

// PwResult is how a password check went.
type PwResult int

type pwResult = PwResult

// Password check results.
const (
	PwOK PwResult = iota
	PwWrong
	PwLocked
	PwBusy
)

const (
	pwOK     = PwOK
	pwWrong  = PwWrong
	pwLocked = PwLocked
	pwBusy   = PwBusy
)

type lockout struct {
	fails int
	until time.Time
}

const (
	maxWrongPasswords = 5
	lockoutFor        = 10 * time.Minute
)

// lockedFor reports how long the account is still locked.
func (s *Services) lockedFor(id int64) time.Duration {
	if l := s.lockouts[id]; l != nil && time.Now().Before(l.until) {
		return time.Until(l.until)
	}
	return 0
}

// checkPassword checks pw against a's hash and calls result on the loop.
func (s *Services) checkPassword(c *Client, a *store.Account, pw string, result func(pwResult)) {
	s.checkSecret(c, a.ID, a.PassHash, pw, result)
}

// checkSecret checks pw against hash off the loop for c.
func (s *Services) checkSecret(c *Client, key int64, hash, pw string, result func(pwResult)) {
	s.CheckSecret(&c.pwBusy, func() bool { return c.closed }, key, hash, pw, result)
}

// CheckSecret checks pw against hash off the loop and calls result on the
// loop, unless gone() says the asker left meanwhile. busy is the asker's
// one-check-at-a-time flag. Wrong tries count against key (an account ID,
// or a negative ID for oper passwords); 5 wrong ones lock it for 10 minutes.
func (s *Services) CheckSecret(busy *bool, gone func() bool, key int64, hash, pw string, result func(PwResult)) {
	if s.lockedFor(key) > 0 {
		result(pwLocked)
		return
	}
	if *busy {
		result(pwBusy)
		return
	}
	*busy = true
	id := key
	go func() {
		s.hashSem <- struct{}{}
		ok := passwd.Check(pw, hash)
		<-s.hashSem
		s.post(func() {
			*busy = false
			if gone() {
				return
			}
			if ok {
				delete(s.lockouts, id)
				result(pwOK)
				return
			}
			l := s.lockouts[id]
			if l == nil {
				l = &lockout{}
				s.lockouts[id] = l
			}
			l.fails++
			if l.fails >= maxWrongPasswords {
				l.fails = 0
				l.until = time.Now().Add(lockoutFor)
			}
			result(pwWrong)
		})
	}()
}

// hashPassword hashes pw off the loop for c and calls done with the hash.
func (s *Services) hashPassword(c *Client, pw string, done func(hash string)) {
	if c.pwBusy {
		s.ns(c, "Still busy with your last password. Try again in a moment.")
		return
	}
	s.HashSecret(&c.pwBusy, func() bool { return c.closed }, pw, func(h string, err error) {
		if err != nil {
			s.dbErr(c, s.nickServ, err)
			return
		}
		done(h)
	})
}

// HashSecret hashes pw off the loop and calls done on the loop, unless
// gone() says the asker left. The caller checks *busy first.
func (s *Services) HashSecret(busy *bool, gone func() bool, pw string, done func(hash string, err error)) {
	*busy = true
	go func() {
		s.hashSem <- struct{}{}
		h, err := passwd.Hash(pw)
		<-s.hashSem
		s.post(func() {
			*busy = false
			if !gone() {
				done(h, err)
			}
		})
	}()
}

// pwProblem says what was wrong with a password check, for NickServ.
func (s *Services) pwProblem(c *Client, a *store.Account, r pwResult) {
	switch r {
	case pwWrong:
		s.ns(c, "Wrong password for %s.", a.Name)
	case pwLocked:
		s.ns(c, "Too many wrong passwords for %s. Try again in %s.", a.Name, roundUp(s.lockedFor(a.ID)))
	case pwBusy:
		s.ns(c, "Still checking your last password. Try again in a moment.")
	}
}

func roundUp(d time.Duration) string {
	m := int((d + time.Minute - 1) / time.Minute)
	if m <= 1 {
		return "1 minute"
	}
	return strconv.Itoa(m) + " minutes"
}

// login identifies c's user to account a and tells everyone who should know.
func (s *Services) login(c *Client, a *store.Account) {
	u := c.user
	if c.accountID == a.ID {
		return
	}
	if c.accountID != 0 {
		s.logout(c, false)
	}
	now := time.Now()
	s.emit(&state.Login{UID: u.UID, Account: a.Name})
	c.accountID = a.ID
	c.enforceAt = time.Time{}
	a.LastSeen, a.LastHost = now, u.Ident+"@"+u.RealHost
	if err := s.store.SaveAccount(a); err != nil {
		s.log.Printf("services database: %v", err)
	}
	s.reply(c, irc.RplLoggedIn, u.Prefix(), a.Name, "You are now logged in as "+a.Name)
	s.setRegistered(c, true)
	s.csOnIdentify(c)
	s.memoSummary(c, a)
}

// logout ends c's identification.
func (s *Services) logout(c *Client, tell bool) {
	u := c.user
	if c.accountID == 0 {
		return
	}
	s.touchAccount(c)
	s.emit(&state.Login{UID: u.UID})
	c.accountID = 0
	if tell {
		s.reply(c, irc.RplLoggedOut, u.Prefix(), "You are now logged out")
	}
	s.setRegistered(c, false)
}

// touchAccount saves "last seen" for an identified user.
func (s *Services) touchAccount(c *Client) {
	if c.accountID == 0 || c.user == nil {
		return
	}
	a, err := s.store.AccountByID(c.accountID)
	if err != nil || a == nil {
		return
	}
	a.LastSeen, a.LastHost = time.Now(), c.user.Ident+"@"+c.user.RealHost
	if err := s.store.SaveAccount(a); err != nil {
		s.log.Printf("services database: %v", err)
	}
}

// setRegistered sets or clears user mode +r.
func (s *Services) setRegistered(c *Client, on bool) {
	u := c.user
	s.emit(&state.UserMode{UID: u.UID, Changes: umodes(on, "r")})
}

// nickCheck runs after registration and after every nick change: is the
// nick registered to someone else?
func (s *Services) nickCheck(c *Client) {
	u := c.user
	c.enforceAt = time.Time{}
	a, err := s.store.AccountByNick(u.Nick)
	if err != nil {
		s.log.Printf("services database: %v", err)
		return
	}
	if a == nil || c.accountID == a.ID {
		return
	}
	if c.accountID == 0 && s.accessMatch(a, u) {
		s.login(c, a)
		s.ns(c, "You were identified as %s because your host is on its access list.", a.Name)
		return
	}
	wait := s.cfg.Services.IdentifyWithin.Duration
	if a.Enforce {
		s.ns(c, "%s is a registered nick. Identify within %d seconds: /ns identify <password>", u.Nick, int(wait.Seconds()))
		c.enforceAt = time.Now().Add(wait)
		c.enforceNick = u.Nick
		return
	}
	s.ns(c, "%s is a registered nick. If it's yours: /ns identify <password>", u.Nick)
}

func (s *Services) accessMatch(a *store.Account, u *state.User) bool {
	masks, err := s.store.Access(a)
	if err != nil {
		return false
	}
	for _, m := range masks {
		if irc.Match(m, u.Ident+"@"+u.RealHost) || irc.Match(m, u.Ident+"@"+u.IP) {
			return true
		}
	}
	return false
}

// enforce renames someone who didn't identify in time.
func (s *Services) enforce(c *Client) {
	c.enforceAt = time.Time{}
	u := c.user
	if u == nil || !irc.EqualFold(u.Nick, c.enforceNick) {
		return
	}
	a, err := s.store.AccountByNick(u.Nick)
	if err != nil || a == nil || c.accountID == a.ID {
		return
	}
	guest := s.guestNick()
	s.forceNick(c, guest)
	s.ns(c, "You didn't identify, so your nick was changed to %s.", guest)
}

func (s *Services) guestNick() string {
	for {
		n := fmt.Sprintf("%s%05d", s.cfg.Services.GuestPrefix, rand.IntN(100000))
		if !s.net.NickInUse(n) {
			return n
		}
	}
}

// forceNick changes c's nick as the server.
func (s *Services) forceNick(c *Client, nick string) {
	u := c.user
	if err := s.emit(&state.NickChange{UID: u.UID, Nick: nick, TS: time.Now().Unix()}); err != nil {
		return
	}
	s.nickCheck(c)
}

// expireAccounts drops accounts nobody used for nick_expire, except those
// in use right now.
func (s *Services) expireAccounts(now time.Time) {
	old, err := s.store.Expired(now.Add(-s.cfg.Services.NickExpire.Duration))
	if err != nil {
		s.log.Printf("services database: %v", err)
		return
	}
	inUse := make(map[int64]bool)
	for _, c := range s.clients {
		inUse[c.accountID] = true
	}
	for _, a := range old {
		if inUse[a.ID] {
			continue
		}
		changes, err := s.store.DeleteAccount(a.ID)
		if err != nil {
			s.log.Printf("services database: %v", err)
			continue
		}
		s.applyFounderChanges(changes, "founder "+a.Name+" expired")
		s.log.Printf("expired account %s (last seen %s)", a.Name, a.LastSeen.UTC().Format(time.DateOnly))
	}
}

// Times shown to people.

func showTime(t time.Time) string { return t.UTC().Format("2 Jan 2006 15:04 UTC") }

func ago(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return plural(int(d.Minutes()), "minute") + " ago"
	case d < 48*time.Hour:
		return plural(int(d.Hours()), "hour") + " ago"
	default:
		return plural(int(d.Hours()/24), "day") + " ago"
	}
}

func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	switch {
	case word == "person":
		word = "people"
	case strings.HasSuffix(word, "y"):
		word = word[:len(word)-1] + "ies"
	default:
		word += "s"
	}
	return strconv.Itoa(n) + " " + word
}

func onOff(v string) (bool, bool) {
	switch strings.ToUpper(v) {
	case "ON":
		return true, true
	case "OFF":
		return false, true
	}
	return false, false
}

// randomCode is a short code people repeat to confirm something.
func randomCode() string {
	const chars = "abcdefghjkmnpqrstuvwxyz23456789"
	b := make([]byte, 6)
	for i := range b {
		b[i] = chars[rand.IntN(len(chars))]
	}
	return string(b)
}
