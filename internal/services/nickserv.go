package services

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/fhazal/beeircd/internal/irc"
	"github.com/fhazal/beeircd/internal/state"
	"github.com/fhazal/beeircd/internal/store"
)

const (
	minPasswordLen  = 6
	maxNicksPerAcct = 10
	dropCodeFor     = 2 * time.Minute
	minProtect      = 10 * time.Second
	maxProtect      = 10 * time.Minute
)

// nickServAliases are old names that still work but HELP doesn't show,
// as in Anope 2.1.
var nickServAliases = map[string]string{
	"GHOST":   "RECOVER",
	"RELEASE": "RECOVER",
	"ACCESS":  "ALIST",
}

func nickServCommands() map[string]svcCmd {
	return map[string]svcCmd{
		"REGISTER": {"REGISTER <password> [email]", "register the nick you are using",
			"Registers your current nick to you, so nobody else can use it.\nPasswords need at least 6 characters. The email is optional; opers use it to\nhelp if you forget your password.\nExample: /ns register honeypot42 bee@example.org", nsRegister},
		"IDENTIFY": {"IDENTIFY [account] <password>", "prove the nick is yours",
			"Identifies you to your account. Give an account to identify to it while\nusing another nick.\nExample: /ns identify honeypot42", nsIdentify},
		"LOGOUT": {"LOGOUT", "stop being identified",
			"You stay connected with the same nick, but no longer identified.", nsLogout},
		"RECOVER": {"RECOVER <nick> [password]", "get your nick back",
			"Takes back one of your nicks: whoever uses it (an old connection, someone\nelse) is disconnected, and you get the nick and are identified. No password\nneeded when you are identified already.\nExample: /ns recover Bee honeypot42", nsRecover},
		"GROUP": {"GROUP <nick> [password]", "add this nick to your account",
			"Adds the nick you are using now to the account that owns <nick>. After that\nboth nicks are yours and share one password and one memo box. No password\nneeded when you are identified to that account.\nExample, while using Bee_: /ns group Bee honeypot42", nsGroup},
		"UNGROUP": {"UNGROUP [nick]", "remove a nick from your account",
			"Takes one of your grouped nicks (the one you use, if you give none) out of\nyour account. The account's own name can't be ungrouped; DROP it instead.", nsUngroup},
		"ALIST": {"ALIST [nick]", "channels where you have access",
			"Lists the channels where your account is on the access list, with your level.\nOpers may give someone else's nick.", nsAlist},
		"SET": {"SET <option> <value>", "change your account",
			"SET PASSWORD <new password>\nSET EMAIL <email>|OFF\nSET PROTECT ON|<seconds>|OFF   rename people who don't identify in time (on, 60 s)\nSET HIDE EMAIL|MASK|STATUS|QUIT ON|OFF   what INFO shows others: email,\n    last user@host, oper status, last quit message (email and mask hidden)", nsSet},
		"SASET": {"SASET <option> <nick> <value>", "change someone's account (opers)",
			"Like SET, for another account. Needs oper flag s.\nExample: /ns saset password Bee newpass42", nsSaset},
		"INFO": {"INFO [nick]", "who owns a nick, when last seen",
			"Shows when a nick was registered and last seen. You and opers see more.", nsInfo},
		"DROP": {"DROP <nick> [code]", "unregister a nick or your account",
			"DROP <your account name> removes the account with all its nicks and memos.\nDROP <grouped nick> removes only that nick from your account.\nYou are asked to repeat it with a code first, so it can't happen by accident.\nOpers with flag s may drop other people's nicks.", nsDrop},
		"LIST": {"LIST <pattern>", "search registered nicks (opers)",
			"Lists registered nicks matching a pattern, like Bee* or *serv.", nsList},
	}
}

// identified returns c's account, or tells them to identify first.
func (s *Services) identified(c *Client) (*store.Account, bool) {
	if c.accountID == 0 {
		s.ns(c, "Identify first: /ns identify <password>")
		s.logDeniedUnreg(c, s.nickServ)
		return nil, false
	}
	a, err := s.store.AccountByID(c.accountID)
	if err != nil {
		s.dbErr(c, s.nickServ, err)
		return nil, false
	}
	if a == nil {
		s.ns(c, "Your account no longer exists.")
		s.logout(c, true)
		return nil, false
	}
	return a, true
}

// accountOf looks up the account that owns nick, reporting problems.
func (s *Services) accountOf(c *Client, nick string) (*store.Account, bool) {
	a, err := s.store.AccountByNick(nick)
	if err != nil {
		s.dbErr(c, s.nickServ, err)
		return nil, false
	}
	if a == nil {
		s.ns(c, "%s isn't a registered nick.", nick)
		return nil, false
	}
	return a, true
}

func validEmail(e string) bool {
	at := strings.IndexByte(e, '@')
	return at > 0 && at < len(e)-3 && strings.Contains(e[at:], ".") && !strings.ContainsAny(e, " ,;:<>")
}

func nsRegister(s *Services, c *Client, args []string) {
	u := c.user
	if len(args) == 0 {
		s.ns(c, "Syntax: REGISTER <password> [email]")
		return
	}
	if c.accountID != 0 {
		s.ns(c, "You're identified as %s already. To add %s to it: /ns group %s", u.Account, u.Nick, u.Account)
		return
	}
	if a, err := s.store.AccountByNick(u.Nick); err != nil {
		s.dbErr(c, s.nickServ, err)
		return
	} else if a != nil {
		s.ns(c, "%s is already registered. If it's yours: /ns identify <password>", u.Nick)
		return
	}
	if why := s.registerForbidden(u.Nick); why != "" {
		s.ns(c, "%s", why)
		return
	}
	pw := args[0]
	if len(pw) < minPasswordLen {
		s.ns(c, "Passwords need at least %d characters.", minPasswordLen)
		return
	}
	if irc.EqualFold(pw, u.Nick) {
		s.ns(c, "Your password can't be your nick.")
		return
	}
	email := ""
	if len(args) > 1 {
		email = args[1]
		if !validEmail(email) {
			s.ns(c, "%s doesn't look like an email address. You can leave it out: /ns register <password>", email)
			return
		}
		if why := s.emailForbidden(email); why != "" {
			s.ns(c, "%s", why)
			return
		}
	}
	nick, cmd := u.Nick, c.cmdName
	s.hashPassword(c, pw, func(hash string) {
		if c.user.Nick != nick {
			s.ns(c, "Your nick changed while registering. Please try again.")
			return
		}
		a, err := s.store.CreateAccount(nick, hash, email, time.Now())
		if errors.Is(err, store.ErrExists) {
			s.ns(c, "%s was just registered by someone else.", nick)
			return
		}
		if err != nil {
			s.dbErr(c, s.nickServ, err)
			return
		}
		s.log.Printf("registered nick %s (%s)", nick, c.ip)
		s.ns(c, "%s is now registered to you. Keep your password safe: you need it to identify.", nick)
		c.cmdName = cmd
		s.nsLogCmd(c, "to register %s (email: %s)", nick, orNone(email))
		s.login(c, a)
	})
}

func nsIdentify(s *Services, c *Client, args []string) {
	if len(args) == 0 {
		s.ns(c, "Syntax: IDENTIFY [account] <password>")
		return
	}
	nick, pw := c.user.Nick, args[0]
	if len(args) > 1 {
		nick, pw = args[0], args[1]
	}
	a, err := s.store.AccountByNick(nick)
	if err != nil {
		s.dbErr(c, s.nickServ, err)
		return
	}
	if a == nil {
		s.ns(c, "%s isn't registered. Register it: /ns register <password> [email]", nick)
		s.nsLogCmd(c, "and failed to identify to nonexistent account %s", nick)
		return
	}
	if c.accountID == a.ID {
		s.ns(c, "You're already identified as %s.", a.Name)
		return
	}
	cmd := c.cmdName
	s.checkPassword(c, a, pw, func(r pwResult) {
		c.cmdName = cmd
		if r != pwOK {
			s.pwProblem(c, a, r)
			if r == pwWrong {
				s.nsLogCmd(c, "and failed to identify to account %s", a.Name)
			}
			return
		}
		fresh, err := s.store.AccountByID(a.ID)
		if err != nil || fresh == nil {
			s.ns(c, "%s was dropped a moment ago.", a.Name)
			return
		}
		s.nsLogCmd(c, "and identified for account %s", fresh.Name) // before login: Anope's line shows the account the user had when asking
		s.ns(c, "Password accepted. You are identified as %s.", fresh.Name)
		s.login(c, fresh)
	})
}

func nsLogout(s *Services, c *Client, args []string) {
	if c.accountID == 0 {
		s.ns(c, "You aren't identified.")
		return
	}
	name := c.user.Account
	s.nsLogCmd(c, "to logout %s", c.user.Nick)
	s.logout(c, true)
	s.ns(c, "You are no longer identified as %s.", name)
	s.nickCheck(c)
}

// withOwnership runs then after proving c owns a: by being identified to it
// or by the password. wrong runs after a wrong password (for the log).
func (s *Services) withOwnership(c *Client, a *store.Account, pw string, then, wrong func()) {
	if c.accountID == a.ID {
		then()
		return
	}
	cmd := c.cmdName
	s.checkPassword(c, a, pw, func(r pwResult) {
		c.cmdName = cmd
		if r != pwOK {
			s.pwProblem(c, a, r)
			if r == pwWrong && wrong != nil {
				wrong()
			}
			return
		}
		then()
	})
}

// ghost disconnects whoever else uses nick.
func (s *Services) ghost(c *Client, nick string) {
	t := s.net.UserByNick(nick)
	if t == nil || t.Service || t == c.user {
		return
	}
	s.host.Disconnect(t.UID, "Killed (NickServ (RECOVER command used by "+c.user.Nick+"))")
}

func nsRecover(s *Services, c *Client, args []string) {
	if len(args) == 0 {
		s.ns(c, "Syntax: RECOVER <nick> [password]")
		return
	}
	nick := args[0]
	a, ok := s.accountOf(c, nick)
	if !ok {
		return
	}
	if irc.EqualFold(nick, c.user.Nick) {
		s.ns(c, "You are using %s already.", c.user.Nick)
		return
	}
	if c.accountID != a.ID && len(args) < 2 {
		s.ns(c, "Give the password of %s: /ns recover %s <password>", a.Name, nick)
		return
	}
	s.withOwnership(c, a, argAt(args, 1), func() {
		s.nsLogCmd(c, "for %s", nick)
		s.ghost(c, nick)
		fresh, err := s.store.AccountByID(a.ID)
		if err != nil || fresh == nil {
			return
		}
		s.login(c, fresh)
		if s.net.NickInUse(nick) {
			s.ns(c, "%s is still in use. Try again in a moment.", nick)
			return
		}
		s.forceNick(c, nick)
		s.ns(c, "You now have %s.", nick)
	}, func() { s.nsLogCmd(c, "with an invalid password for %s", a.Name) })
}

func nsGroup(s *Services, c *Client, args []string) {
	u := c.user
	if len(args) < 1 {
		s.ns(c, "Syntax: GROUP <nick> [password]")
		return
	}
	a, ok := s.accountOf(c, args[0])
	if !ok {
		return
	}
	if mine, err := s.store.AccountByNick(u.Nick); err != nil {
		s.dbErr(c, s.nickServ, err)
		return
	} else if mine != nil {
		if mine.ID == a.ID {
			s.ns(c, "%s is already part of %s.", u.Nick, a.Name)
		} else {
			s.ns(c, "%s is registered to another account.", u.Nick)
		}
		return
	}
	if why := s.registerForbidden(u.Nick); why != "" {
		s.ns(c, "%s", why)
		return
	}
	if c.accountID != a.ID && len(args) < 2 {
		s.ns(c, "Give the password of %s: /ns group %s <password>", a.Name, args[0])
		return
	}
	nick := u.Nick
	s.withOwnership(c, a, argAt(args, 1), func() {
		fresh, err := s.store.AccountByID(a.ID)
		if err != nil || fresh == nil {
			return
		}
		nicks, err := s.store.Nicks(fresh)
		if err != nil {
			s.dbErr(c, s.nickServ, err)
			return
		}
		if len(nicks) >= maxNicksPerAcct {
			s.ns(c, "%s has %d nicks already, the most an account can have.", fresh.Name, maxNicksPerAcct)
			return
		}
		if err := s.store.AddNick(fresh, nick, time.Now()); err != nil {
			if errors.Is(err, store.ErrExists) {
				s.ns(c, "%s was just registered by someone else.", nick)
			} else {
				s.dbErr(c, s.nickServ, err)
			}
			return
		}
		s.ns(c, "%s is now part of the account %s.", nick, fresh.Name)
		s.nsLogCmd(c, "to make %s join group of %s (%s) (email: %s)", nick, args[0], fresh.Name, orNone(fresh.Email))
		s.login(c, fresh)
	}, func() { s.nsLogCmd(c, "and failed to group to %s", args[0]) })
}

func nsUngroup(s *Services, c *Client, args []string) {
	a, ok := s.identified(c)
	if !ok {
		return
	}
	nick := c.user.Nick
	if len(args) > 0 {
		nick = args[0]
	}
	owner, err := s.store.AccountByNick(nick)
	if err != nil {
		s.dbErr(c, s.nickServ, err)
		return
	}
	switch {
	case owner == nil || owner.ID != a.ID:
		s.ns(c, "%s isn't part of your account.", nick)
	case irc.EqualFold(nick, a.Name):
		s.ns(c, "%s is the account's own name and can't be ungrouped. To remove everything: /ns drop %s", a.Name, a.Name)
	default:
		if err := s.store.RemoveNick(nick); err != nil {
			s.dbErr(c, s.nickServ, err)
			return
		}
		s.ns(c, "%s is no longer part of %s.", nick, a.Name)
		s.nsLogCmd(c, "to make %s leave group of %s (email: %s)", nick, a.Name, orNone(a.Email))
	}
}

func nsAlist(s *Services, c *Client, args []string) {
	var a *store.Account
	if nick := argAt(args, 0); nick != "" {
		if !c.isOper() {
			s.ns(c, "Only opers may look at someone else's channels. Yours: /ns alist")
			return
		}
		var ok bool
		if a, ok = s.accountOf(c, nick); !ok {
			return
		}
	} else {
		var ok bool
		if a, ok = s.identified(c); !ok {
			return
		}
	}
	chans, err := s.store.Channels()
	if err != nil {
		s.dbErr(c, s.nickServ, err)
		return
	}
	n := 0
	for _, reg := range chans {
		lvl, err := s.store.Level(reg, a.ID)
		if err != nil || lvl == store.LevelNone {
			continue
		}
		if n == 0 {
			s.ns(c, "Channels where %s has access:", a.Name)
		}
		n++
		s.ns(c, "  %-20s %s", reg.Name, store.LevelName(lvl))
	}
	if n == 0 {
		s.ns(c, "%s isn't on the access list of any channel.", a.Name)
		return
	}
	s.ns(c, "%s.", plural(n, "channel"))
}

func nsSet(s *Services, c *Client, args []string) {
	a, ok := s.identified(c)
	if !ok {
		return
	}
	s.setOption(c, a, "SET", strings.ToUpper(argAt(args, 0)), args[min(1, len(args)):])
}

func nsSaset(s *Services, c *Client, args []string) {
	if !s.needFlag(c, s.nickServ, 's') {
		return
	}
	if len(args) < 3 {
		s.ns(c, "Syntax: SASET <option> <nick> <value>")
		return
	}
	a, ok := s.accountOf(c, args[1])
	if !ok {
		return
	}
	if s.isAdminAccount(a) && c.accountID != a.ID {
		s.ns(c, "%s is the server admin's account; only they can change it.", a.Name)
		return
	}
	opt := strings.ToUpper(args[0])
	if s.setOption(c, a, "SASET", opt, args[2:]) && opt != "PASSWORD" {
		s.audit(c, s.nickServ, "SASET %s %s", opt, a.Name)
	}
}

// setOption carries out SET (or SASET, for opers) option on account a.
// It reports whether a change was made or started.
func (s *Services) setOption(c *Client, a *store.Account, cmd, opt string, args []string) bool {
	val := argAt(args, 0)
	whose := "Your"
	if cmd == "SASET" {
		whose = a.Name + "'s"
	}
	save := func(msg, logText string) bool {
		if err := s.store.SaveAccount(a); err != nil {
			s.dbErr(c, s.nickServ, err)
			return false
		}
		s.ns(c, "%s", msg)
		s.nsLog(c, a.ID, "%s", logText)
		return true
	}
	switch opt {
	case "PASSWORD":
		if len(val) < minPasswordLen {
			s.ns(c, "Passwords need at least %d characters.", minPasswordLen)
			return false
		}
		id, name, typed := a.ID, a.Name, c.cmdName
		s.hashPassword(c, val, func(hash string) {
			c.cmdName = typed
			fresh, err := s.store.AccountByID(id)
			if err != nil || fresh == nil {
				return
			}
			fresh.PassHash = hash
			if err := s.store.SaveAccount(fresh); err != nil {
				s.dbErr(c, s.nickServ, err)
				return
			}
			if cmd == "SASET" {
				delete(s.lockouts, id) // a forgotten password: let them in again
				s.audit(c, s.nickServ, "SASET PASSWORD %s", name)
			}
			s.ns(c, "%s password is changed.", whose)
			if id == c.accountID {
				s.nsLogCmd(c, "to change their password")
			} else {
				s.logCmd(c, s.nickServ, logAdmin, "", "to change the password of "+name)
			}
		})
		return true
	case "EMAIL":
		switch {
		case strings.EqualFold(val, "OFF"):
			a.Email = ""
			return save(whose+" email is removed.", "to unset the email of "+a.Name)
		case validEmail(val) && s.emailForbidden(val) != "" && cmd == "SET":
			s.ns(c, "%s", s.emailForbidden(val))
			return false
		case validEmail(val):
			a.Email = val
			return save(whose+" email is now "+val+".", "to change the email of "+a.Name+" to "+val)
		}
		s.ns(c, "Syntax: %s EMAIL <email>|OFF", cmd)
	case "PROTECT", "ENFORCE": // ENFORCE is the old name
		switch on, isOnOff := onOff(val); {
		case isOnOff && !on:
			a.Enforce = false
			return save("Protect is off: people using "+strings.ToLower(whose)+" nicks are only told they are registered.", "to disable kill for "+a.Name)
		case isOnOff:
			a.Enforce, a.Protect = true, 0
			return save(fmt.Sprintf("Protect is on: people who don't identify within %d seconds get renamed.", int(s.protectDelay(a).Seconds())), "to set kill on for "+a.Name)
		default:
			d, err := parseSeconds(val)
			if err != nil || d < minProtect || d > maxProtect {
				s.ns(c, "Syntax: %s PROTECT ON|<seconds>|OFF (between %d and %d seconds)", cmd, int(minProtect.Seconds()), int(maxProtect.Seconds()))
				return false
			}
			a.Enforce, a.Protect = true, int(d.Seconds())
			return save(fmt.Sprintf("Protect is on: people who don't identify within %d seconds get renamed.", a.Protect), "to set kill on for "+a.Name)
		}
	case "HIDE":
		field := strings.ToUpper(val)
		if field == "HOST" {
			field = "MASK" // the old name
		}
		on, ok := onOff(argAt(args, 1))
		var p *bool
		var what string
		switch field {
		case "EMAIL":
			p, what = &a.HideEmail, "email"
		case "MASK":
			p, what = &a.HideHost, "last user@host"
		case "STATUS":
			p, what = &a.HideStatus, "oper status"
		case "QUIT":
			p, what = &a.HideQuit, "last quit message"
		}
		if p == nil || !ok {
			s.ns(c, "Syntax: %s HIDE EMAIL|MASK|STATUS|QUIT ON|OFF", cmd)
			return false
		}
		*p = on
		shown := "shown to"
		if on {
			shown = "hidden from"
		}
		return save(fmt.Sprintf("%s %s is %s others in INFO.", whose, what, shown), "to change hide "+field+" to "+strings.ToUpper(argAt(args, 1))+" for "+a.Name)
	default:
		s.ns(c, "Syntax: %s PASSWORD|EMAIL|PROTECT|HIDE ... (see /ns help %s)", cmd, strings.ToLower(cmd))
	}
	return false
}

// parseSeconds reads "45", "45s" or "2m".
func parseSeconds(v string) (time.Duration, error) {
	if n, err := strconv.Atoi(v); err == nil {
		return time.Duration(n) * time.Second, nil
	}
	return time.ParseDuration(v)
}

func nsInfo(s *Services, c *Client, args []string) {
	nick := c.user.Nick
	if len(args) > 0 {
		nick = args[0]
	}
	a, err := s.store.AccountByNick(nick)
	if err != nil {
		s.dbErr(c, s.nickServ, err)
		return
	}
	if a == nil {
		s.ns(c, "%s isn't registered.", nick)
		return
	}
	nicks, err := s.store.Nicks(a)
	if err != nil {
		s.dbErr(c, s.nickServ, err)
		return
	}
	shown := nick
	for _, n := range nicks {
		if irc.EqualFold(n, nick) {
			shown = n
		}
	}
	insider := c.accountID == a.ID || c.user.HasMode('o')
	s.ns(c, "%s · registered %s (%s)", shown, showTime(a.Registered), ago(a.Registered))
	if !irc.EqualFold(shown, a.Name) {
		s.ns(c, "  Account       %s", a.Name)
	}
	if !a.HideStatus || insider {
		if o, err := s.store.OperByAccount(a.ID); err == nil && o != nil || s.isAdminAccount(a) {
			title := "IRC operator"
			if s.isAdminAccount(a) {
				title = "Server Admin"
			} else if o.Title != "" {
				title = o.Title
			}
			s.ns(c, "  Oper          %s", title)
		}
	}
	online := false
	for _, other := range s.clients {
		if other.accountID == a.ID && !other.closed {
			online = true
		}
	}
	if online {
		s.ns(c, "  Last seen     online now")
	} else {
		s.ns(c, "  Last seen     %s (%s)", showTime(a.LastSeen), ago(a.LastSeen))
	}
	if a.LastHost != "" && (!a.HideHost || insider) {
		s.ns(c, "  Last host     %s", a.LastHost)
	}
	if a.LastQuit != "" && !online && (!a.HideQuit || insider) {
		s.ns(c, "  Last quit     %s", a.LastQuit)
	}
	if a.Email != "" && (!a.HideEmail || insider) {
		s.ns(c, "  Email         %s", a.Email)
	}
	var others []string
	for _, n := range nicks {
		if !irc.EqualFold(n, shown) {
			others = append(others, n)
		}
	}
	if len(others) > 0 {
		s.ns(c, "  Also uses     %s", strings.Join(others, ", "))
	}
	var opts []string
	if a.Enforce {
		opts = append(opts, fmt.Sprintf("protect (%d s)", int(s.protectDelay(a).Seconds())))
	}
	for _, h := range []struct {
		on   bool
		name string
	}{{a.HideEmail, "email"}, {a.HideHost, "mask"}, {a.HideStatus, "status"}, {a.HideQuit, "quit"}} {
		if h.on {
			opts = append(opts, h.name+" hidden")
		}
	}
	if len(opts) == 0 {
		opts = []string{"none"}
	}
	s.ns(c, "  Options       %s", strings.Join(opts, ", "))
}

func nsDrop(s *Services, c *Client, args []string) {
	if len(args) == 0 {
		s.ns(c, "Syntax: DROP <nick> [code]   (your account name drops the whole account)")
		return
	}
	nick, code := args[0], argAt(args, 1)
	owner, ok := s.accountOf(c, nick)
	if !ok {
		return
	}
	asOper := c.accountID != owner.ID
	switch {
	case asOper && c.accountID == 0 && !c.can('s'):
		s.ns(c, "Identify first: /ns identify <password>")
		s.logDeniedUnreg(c, s.nickServ)
		return
	case asOper && !c.can('s'):
		s.ns(c, "%s isn't part of your account.", nick)
		return
	case asOper && s.isAdminAccount(owner):
		s.ns(c, "%s belongs to the server admin's account; only they can drop it.", nick)
		return
	}
	whole := irc.EqualFold(nick, owner.Name)
	key := irc.Fold(nick) + ":"
	if code == "" || c.dropCode != key+code || time.Now().After(c.dropUntil) {
		fresh := randomCode()
		c.dropCode, c.dropUntil = key+fresh, time.Now().Add(dropCodeFor)
		if code != "" {
			s.ns(c, "That code is wrong or too old.")
		}
		if whole {
			nicks, err := s.store.Nicks(owner)
			if err != nil {
				s.dbErr(c, s.nickServ, err)
				return
			}
			s.ns(c, "This removes the account %s with its nicks (%s) and all its memos, for good.", owner.Name, strings.Join(nicks, ", "))
		} else {
			s.ns(c, "This removes %s from the account %s. The account and its other nicks stay.", nick, owner.Name)
		}
		s.ns(c, "To go ahead within 2 minutes: /ns drop %s %s", nick, fresh)
		return
	}
	c.dropCode = ""
	s.nsLog(c, owner.ID, "to drop nickname %s (group: %s) (email: %s)", nick, owner.Name, orNone(owner.Email))
	if !whole {
		if err := s.store.RemoveNick(nick); err != nil {
			s.dbErr(c, s.nickServ, err)
			return
		}
		if asOper {
			s.audit(c, s.nickServ, "DROP %s (nick of %s)", nick, owner.Name)
		}
		s.ns(c, "%s is no longer registered.", nick)
		return
	}
	name := owner.Name
	by := "by its owner"
	if asOper {
		by = "by " + c.user.Nick
	}
	if err := s.dropAccount(owner, "founder "+name+" was dropped", by); err != nil {
		s.dbErr(c, s.nickServ, err)
		return
	}
	if asOper {
		s.audit(c, s.nickServ, "DROP %s", name)
	}
	s.ns(c, "The account %s is gone.", name)
}

// dropAccount removes a with its nicks and memos and logs out everyone
// using it. founderWhy and by go to the log.
func (s *Services) dropAccount(a *store.Account, founderWhy, by string) error {
	id, name := a.ID, a.Name
	changes, err := s.store.DeleteAccount(id)
	if err != nil {
		return err
	}
	delete(s.lockouts, id)
	s.applyFounderChanges(changes, founderWhy)
	s.log.Printf("dropped account %s (%s)", name, by)
	for _, other := range s.clients {
		if other.accountID == id {
			other.accountID = 0 // no touchAccount: it's gone
			s.emit(&state.Login{UID: other.user.UID})
			s.setRegistered(other, false)
			s.reply(other, irc.RplLoggedOut, other.user.Prefix(), "You are now logged out")
		}
	}
	s.host.AccountGone(id)
	return nil
}

func nsList(s *Services, c *Client, args []string) {
	if !c.user.HasMode('o') {
		s.ns(c, "LIST is for opers.")
		return
	}
	pattern := argAt(args, 0)
	if pattern == "" {
		s.ns(c, "Syntax: LIST <pattern>")
		return
	}
	all, err := s.store.AllNicks()
	if err != nil {
		s.dbErr(c, s.nickServ, err)
		return
	}
	n := 0
	for _, e := range all {
		if !irc.Match(pattern, e.Nick) {
			continue
		}
		n++
		if n <= 100 {
			if irc.EqualFold(e.Nick, e.Account) {
				s.ns(c, "  %s", e.Nick)
			} else {
				s.ns(c, "  %s (account %s)", e.Nick, e.Account)
			}
		}
	}
	if n > 100 {
		s.ns(c, "%d nicks match; showing the first 100.", n)
	} else {
		if n == 1 {
			s.ns(c, "1 nick matches.")
		} else {
			s.ns(c, "%d nicks match.", n)
		}
	}
}

func argAt(args []string, i int) string {
	if i < len(args) {
		return args[i]
	}
	return ""
}

// orNone is s, or "none" when empty (Anope's log wording).
func orNone(s string) string {
	if s == "" {
		return "none"
	}
	return s
}
