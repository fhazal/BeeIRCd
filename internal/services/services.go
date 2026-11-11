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
	user    *state.User
	alias   string // "ns"
	intro   string // first line of HELP
	cmds    map[string]svcCmd
	order   []string          // HELP order
	need    byte              // 'o' = opers only, 'a' = oper flag a, 0 = everyone
	aliases map[string]string // hidden old names, like GHOST for RECOVER

	// HELP layout (help.go).
	groups   []helpGroup
	cmdNeed  map[string]byte        // oper flag a command needs, 'o' for any oper
	cmdShown map[string]func() bool // false hides a command on this server
}

// command finds a command by name or hidden alias.
func (sv *service) command(name string) (svcCmd, bool) {
	name = strings.ToUpper(name)
	if real, ok := sv.aliases[name]; ok {
		name = real
	}
	cmd, ok := sv.cmds[name]
	return cmd, ok
}

// reservedNicks can't be used by people even before their service exists.
var reservedNicks = []string{"NickServ", "ChanServ", "MemoServ", "OperServ", "HostServ", "AdminServ", "Global"}

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
	nsCmds := nickServCommands()
	if nickServExtras != nil {
		for name, cmd := range nickServExtras() {
			nsCmds[name] = cmd
		}
	}
	for name, cmd := range ns156bCommands() {
		nsCmds[name] = cmd
	}
	s.nickServ = s.addService("NickServ", "ns", "Nickname Services", "NickServ keeps your nick yours. Commands:", nsCmds)
	s.nickServ.aliases = nickServAliases
	s.chanServ = s.addService("ChanServ", "cs", "Channel Services", "ChanServ keeps a channel yours. Commands:", chanServCommands())
	s.chanServ.aliases = chanServAliases
	msCmds := memoServCommands()
	for name, cmd := range ms156Commands() {
		msCmds[name] = cmd
	}
	s.memoServ = s.addService("MemoServ", "ms", "Memo Services", "MemoServ keeps messages for people who are away. Commands:", msCmds)
	osCmds := operServCommands()
	for name, cmd := range operServ155bCommands() {
		osCmds[name] = cmd
	}
	osCmds["OPER"] = svcCmd{"OPER LIST | INFO [class] | ADD <nick> <class> | DEL <nick>", "show and change the oper list",
		"OPER LIST            the opers and their classes\nOPER INFO [class]    what each class may do\nOPER ADD <nick> <class>   (flag a) like /as oper <nick> add class <class> host *@*\nOPER DEL <nick>            (flag a)\nThe full oper commands are in AdminServ: /as help oper", osOper}
	s.operServ = s.addService("OperServ", "os", "Operator Services", "OperServ is the toolbox for IRC operators. Commands:", osCmds)
	s.operServ.need = 'o'
	s.operServ.aliases = operServAliases
	s.hostServ = s.addService("HostServ", "hs", "vHost Service", "HostServ gives nicks a vHost, a host shown instead of yours. Commands:", hostServCommands())
	s.global = s.addService("Global", "", "Network Announcements", "Global sends network notices. It takes no commands.", map[string]svcCmd{})
	s.helpSetup()
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
	if s.ignored(c) { // OperServ IGNORE: dropped without a word
		return
	}
	name := strings.ToUpper(fields[0])
	args := fields[1:]
	c.cmdName = name // as typed, like Anope's source.command
	switch {
	case sv.need == 'o' && !c.isOper():
		s.svcReply(c, sv, sv.user.Nick+" is for IRC operators.")
		// Anope's operserv.cpp: OnBotPrivmsg for non-opers.
		s.logTo(sv, "Denied access to "+sv.user.Nick+" from "+anopeMask(c.user)+" (non-oper)")
		return
	case sv.need == 'a' && !c.can('a'):
		s.svcReply(c, sv, sv.user.Nick+" is for server admins (oper flag a).")
		s.logDenied(c, sv)
		return
	case sv.alias == "":
		s.svcReply(c, sv, sv.intro)
		return
	}
	if name == "HELP" {
		s.serviceHelp(c, sv, args)
		return
	}
	cmd, ok := sv.command(name)
	if !ok {
		s.svcReply(c, sv, fmt.Sprintf("Unknown command %s. Type /%s help for a list.", fields[0], sv.alias))
		return
	}
	c.csUsedOn = ""
	cmd.run(s, c, args)
	if sv == s.chanServ && c.csUsedOn != "" {
		if real, isAlias := sv.aliases[name]; isAlias {
			name = real
		}
		s.csLogUse(c, name, args)
		c.csUsedOn = ""
	}
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
	a.LastSeen, a.LastHost = now, u.TrueIdent()+"@"+u.RealHost
	if err := s.store.SaveAccount(a); err != nil {
		s.log.Printf("services database: %v", err)
	}
	s.reply(c, irc.RplLoggedIn, u.Prefix(), a.Name, "You are now logged in as "+a.Name)
	s.setRegistered(c, true)
	s.csOnIdentify(c)
	s.memoSummary(c, a)
	s.afterLogin(c, a)
	s.buddyOnline(c, a)
	s.hsLogin(c)
}

// logout ends c's identification.
func (s *Services) logout(c *Client, tell bool) {
	u := c.user
	if c.accountID == 0 {
		return
	}
	s.touchAccount(c)
	s.emit(&state.Login{UID: u.UID})
	id := c.accountID
	c.accountID = 0
	defer s.buddyOffline(id)
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

// saveQuit keeps "last seen" and the quit message of someone leaving.
func (s *Services) saveQuit(c *Client, reason string) {
	if c.accountID == 0 {
		return
	}
	a, err := s.store.AccountByID(c.accountID)
	if err != nil || a == nil {
		return
	}
	a.LastSeen, a.LastHost, a.LastQuit = time.Now(), c.user.Ident+"@"+c.user.RealHost, reason
	if err := s.store.SaveAccount(a); err != nil {
		s.log.Printf("services database: %v", err)
	}
}

// killMode is a's SET KILL mode as it works on this network: IMMED counts
// as ON where the network doesn't allow it (Anope's allowkillimmed).
func (s *Services) killMode(a *store.Account) int {
	if a.Protect == store.KillImmed && !s.cfg.Services.AllowKillImmed {
		return store.KillOn
	}
	if a.Protect != store.KillQuick && a.Protect != store.KillImmed {
		return store.KillOn
	}
	return a.Protect
}

// killDelay is how long someone has to identify to a's nick.
func (s *Services) killDelay(a *store.Account) time.Duration {
	switch s.killMode(a) {
	case store.KillQuick:
		return s.cfg.Services.KillQuick.Duration
	case store.KillImmed:
		return 0
	}
	return s.cfg.Services.IdentifyWithin.Duration
}

// onAccess reports whether c's connection matches one of a's NickServ
// ACCESS masks: ident@real host or ident@the host others see (Anope
// matches the real host, the vhost and the cloak).
func (s *Services) onAccess(c *Client, a *store.Account) bool {
	masks, err := s.store.Access(a)
	if err != nil || len(masks) == 0 {
		return false
	}
	u := c.user
	real, shown := u.TrueIdent()+"@"+u.RealHost, u.Ident+"@"+u.Host
	for _, m := range masks {
		if irc.Match(m, real) || irc.Match(m, shown) {
			return true
		}
	}
	return false
}

// recognized is Anope's IsRecognized(): on a's access list while a has
// SECURE off. A recognised connection is treated as the owner of the nick,
// without being identified.
func (s *Services) recognized(c *Client, a *store.Account) bool {
	return !a.Secure && s.onAccess(c, a)
}

// setRegistered sets or clears user mode +r.
func (s *Services) setRegistered(c *Client, on bool) {
	u := c.user
	s.emit(&state.UserMode{UID: u.UID, Changes: umodes(on, "r")})
}

// nickCheck runs after registration and after every nick change: is the
// nick registered to someone else? It follows Anope 2.0.20's Validate:
// a recognised connection (ACCESS list, SECURE off) is left alone; one on
// the access list with SECURE on is told, but not renamed.
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
	if a.Suspended(time.Now()) {
		s.ns(c, "Nick %s is suspended.", u.Nick)
		return
	}
	if s.certValidate(c, a) {
		return
	}
	onAccess := s.onAccess(c, a)
	if onAccess && !a.Secure {
		a.LastSeen, a.LastHost = time.Now(), u.TrueIdent()+"@"+u.RealHost
		if err := s.store.SaveAccount(a); err != nil {
			s.log.Printf("services database: %v", err)
		}
		return
	}
	if !a.Enforce || onAccess {
		s.ns(c, "%s is a registered nick. If it's yours: /ns identify <password>", u.Nick)
		return
	}
	if s.killMode(a) == store.KillImmed {
		s.ns(c, "%s is a registered nick, and you may not use it.", u.Nick)
		guest := s.guestNick()
		s.forceNick(c, guest)
		s.ns(c, "Your nick was changed to %s.", guest)
		return
	}
	wait := s.killDelay(a)
	s.ns(c, "%s is a registered nick. Identify within %d seconds: /ns identify <password>", u.Nick, int(wait.Seconds()))
	c.enforceAt = time.Now().Add(wait)
	c.enforceNick = u.Nick
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
		if inUse[a.ID] || a.NoExpire || a.SuspendedBy != "" {
			continue
		}
		changes, err := s.store.DeleteAccount(a.ID)
		if err != nil {
			s.log.Printf("services database: %v", err)
			continue
		}
		s.applyFounderChanges(changes, "founder "+a.Name+" expired")
		s.log.Printf("expired account %s (last seen %s)", a.Name, a.LastSeen.UTC().Format(time.DateOnly))
		s.logTo(s.nickServ, "Expiring nickname "+a.Name+" (group: "+a.Name+") (e-mail: "+orNone(a.Email)+")")
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
	case strings.HasSuffix(word, "y") && !strings.ContainsAny(word[len(word)-2:len(word)-1], "aeiou"): // entry -> entries, day -> days
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
