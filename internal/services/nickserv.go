package services

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/fhazal/beeircd/internal/irc"
	"github.com/fhazal/beeircd/internal/state"
	"github.com/fhazal/beeircd/internal/store"
)

const (
	minPasswordLen  = 6
	maxNicksPerAcct = 10
	maxAccessMasks  = 32 // Anope's accessmax
)

// nickServAliases are names that still work but HELP doesn't show. Anope
// 2.0.20 ships GHOST and RELEASE commented out in its config; we keep them
// on because many people type them by habit.
var nickServAliases = map[string]string{
	"GHOST":   "RECOVER",
	"RELEASE": "RECOVER",
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
		"UNGROUP": {"UNGROUP [nick]", "make a nick an account of its own",
			"Takes one of your nicks (the one you use, if you give none) out of your\naccount and makes it an account of its own, with the same password and\nemail. Ungrouping the account's name gives the account the name of its\noldest other nick.", nsUngroup},
		"ACCESS": {"ACCESS {ADD | DEL | LIST} [nick] [mask]", "hosts that recognise you",
			"ACCESS ADD [nick] <user@host>\nACCESS DEL [nick] <user@host>\nACCESS LIST [nick]\nA connection from a host on your list is \"recognised\": NickServ doesn't\nrename it, and STATUS says 2. It is not identified: with SECURE on (the\ndefault) you still need your password for your channels and memos. With\nSECURE off, a recognised connection counts as you. Masks look like\n~bee@*.example.org; up to 32. Opers with flag s may give someone's nick.\nExample: /ns access add ~bee@*.example.org", nsAccess},
		"STATUS": {"STATUS [nick ...]", "is someone the owner of their nick",
			"Answers STATUS <nick> <code> <account> for each nick (yours if you give none):\n  0  not online, or the nick isn't registered\n  1  not recognised as the nick's owner\n  2  recognised by the ACCESS list only\n  3  identified with the password", nsStatus},
		"ALIST": {"ALIST [nick]", "channels where you have access",
			"Lists the channels where your account is on the access list, with your level.\nOpers may give someone else's nick.", nsAlist},
		"SET": {"SET <option> <value>", "change your account",
			"SET PASSWORD <new password>\nSET EMAIL <email>|OFF\nSET KILL {ON | QUICK | IMMED | OFF}   rename people who use your nick and don't\n    identify: ON after 60 s (default), QUICK after 20 s, IMMED at once (only\n    where the network allows it)\nSET SECURE {ON | OFF}   on (default): your ACCESS list never stands in for\n    your password\nSET HIDE {EMAIL | STATUS | USERMASK | QUIT} {ON | OFF}   what INFO shows others:\n    email, oper status, last user@host, last quit message (email and\n    user@host hidden)\nSET AUTOOP {ON | OFF}      ChanServ gives you your status when you join (on)\nSET DISPLAY <nick>         which of your nicks is the account's name\nSET GREET [message]       what a channel bot says when you join (BotServ\n    SET GREET); without a message, it is removed\nSET KEEPMODES {ON | OFF}   your user modes (+iwx) come back when you identify\nSET PRIVATE {ON | OFF}     hide you from NickServ LIST (on)\nSET URL [url]              a URL in INFO; without one, it is removed", nsSet},
		"SASET": {"SASET <option> <nick> <value>", "change someone's account (opers)",
			"Like SET, for another account. Needs oper flag s.\nExample: /ns saset password Bee newpass42\nSASET NOEXPIRE <nick> {ON | OFF}   the account never expires", nsSaset},
		"INFO": {"INFO [nick]", "who owns a nick, when last seen",
			"Shows when a nick was registered and last seen. You and opers see more.", nsInfo},
		"DROP": {"DROP <nick>", "unregister one of your nicks",
			"Removes the nick from your account; anyone may register it after that.\nYour account stays while it has other nicks. Dropping the account's name\ngives the account the name of its oldest other nick. Dropping the last nick\nremoves the account with its memos, and you lose the channels you own.\nOpers with flag s may drop anyone's nick, except other opers'.", nsDrop},
		"LIST": {"LIST <pattern> [SUSPENDED] [NOEXPIRE] [UNCONFIRMED]", "search registered nicks",
			"Lists registered nicknames that match a given pattern, like Bee* or\n*!*@*.example.org (nick!last user@host). Accounts with SET PRIVATE on are\nleft out, except for opers. #1-10 shows those entries. Opers may add\nSUSPENDED or NOEXPIRE to see only those (! marks NOEXPIRE nicks).", nsList},
	}
}

func init() { nickServExtras = ns156Commands }

// nickServExtras adds the commands from other files (ns156.go).
var nickServExtras func() map[string]svcCmd

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
	if s.defconBlocks(c, s.nickServ, 4) {
		return
	}
	u := c.user
	if len(args) == 0 {
		s.ns(c, "Syntax: REGISTER <password> [email]")
		return
	}
	if c.accountID != 0 {
		s.ns(c, "You're identified as %s already. To add %s to it: /ns group %s", u.Account, u.Nick, u.Account)
		return
	}
	if d := s.cfg.Services.RegDelayOr(); d > 0 && !c.isOper() && time.Now().Unix()-u.NickTS < int64(d.Seconds()) {
		s.ns(c, "You must have been using this nick for at least %d seconds to register.", int(d.Seconds()))
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
	if len(args) < 2 && s.cfg.Services.ForceEmailOn() {
		s.ns(c, "Syntax: REGISTER <password> <email>")
		return
	}
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
		if n := s.cfg.Services.MaxMemos; n != store.DefaultMemoLimit {
			a.MemoLimit = n
			s.store.SaveAccount(a)
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
	if a.Suspended(time.Now()) {
		s.ns(c, "Nick %s is suspended.", a.Name)
		return
	}
	if s.loginsFull(c, a) {
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
		s.ns(c, "Password accepted - you are now recognized.")     // Anope 2.0.20 ns_identify
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
	if a.Suspended(time.Now()) {
		s.ns(c, "Nick %s is suspended.", a.Name)
		return
	}
	if c.accountID != a.ID && len(args) < 2 {
		s.ns(c, "Give the password of %s: /ns recover %s <password>", a.Name, nick)
		return
	}
	if c.accountID != a.ID && s.loginsFull(c, a) {
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
	if a.Suspended(time.Now()) {
		s.ns(c, "Nick %s is suspended.", a.Name)
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
	if c.accountID != a.ID && s.loginsFull(c, a) {
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
	nicks, err := s.store.Nicks(a)
	if err != nil {
		s.dbErr(c, s.nickServ, err)
		return
	}
	if len(nicks) == 1 {
		s.ns(c, "Your nick isn't grouped with any other, so you can't ungroup it.")
		return
	}
	owner, err := s.store.AccountByNick(nick)
	if err != nil {
		s.dbErr(c, s.nickServ, err)
		return
	}
	switch {
	case owner == nil:
		s.ns(c, "%s isn't registered.", nick)
		return
	case owner.ID != a.ID:
		s.ns(c, "%s isn't part of your account.", nick)
		return
	}
	var rest []string
	for _, n := range nicks {
		if irc.EqualFold(n, nick) {
			nick = n // as registered
		} else {
			rest = append(rest, n)
		}
	}
	if _, err := s.store.SplitNick(nick, time.Now()); err != nil {
		s.dbErr(c, s.nickServ, err)
		return
	}
	name := a.Name
	if irc.EqualFold(nick, a.Name) {
		name = rest[0] // the oldest other nick, as Anope moves the display
		if err := s.renameAccount(a.ID, name); err != nil {
			s.dbErr(c, s.nickServ, err)
			return
		}
	}
	s.nsLogCmd(c, "to make %s leave group of %s (email: %s)", nick, name, orNone(a.Email))
	s.ns(c, "%s has been ungrouped from %s. It is an account of its own now, with the same password.", nick, name)
	// Whoever uses the nick while identified to the old account is no
	// longer on a nick of their own: -r, as Anope does.
	if t := s.clientOf(s.net.UserByNick(nick)); t != nil && !t.closed && t.accountID == a.ID {
		s.setRegistered(t, false)
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
				s.clearLockouts(id) // a forgotten password: let them in again
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
	case "KILL", "PROTECT", "ENFORCE": // PROTECT (Anope 2.1) and ENFORCE (ours before) are hidden names
		switch strings.ToUpper(val) {
		case "ON":
			a.Enforce, a.Protect = true, store.KillOn
			return save(fmt.Sprintf("Kill protection is on for %s: people who don't identify within %d seconds get renamed.", a.Name, int(s.killDelay(a).Seconds())), "to set kill on for "+a.Name)
		case "QUICK":
			a.Enforce, a.Protect = true, store.KillQuick
			return save(fmt.Sprintf("Kill protection is on for %s, quick: people who don't identify within %d seconds get renamed.", a.Name, int(s.killDelay(a).Seconds())), "to set kill quick for "+a.Name)
		case "IMMED":
			if !s.cfg.Services.AllowKillImmed {
				s.ns(c, "The IMMED option is not available on this network.")
				return false
			}
			a.Enforce, a.Protect = true, store.KillImmed
			return save("Kill protection is on for "+a.Name+", with no delay: people who use the nick without identifying are renamed at once.", "to set kill immed for "+a.Name)
		case "OFF":
			a.Enforce = false
			return save("Kill protection is off for "+a.Name+": people using the nick are only told it is registered.", "to disable kill for "+a.Name)
		}
		s.ns(c, "Syntax: %s KILL {ON | QUICK | IMMED | OFF}", setSyntaxHead(cmd))
	case "SECURE":
		on, ok := onOff(val)
		if !ok {
			s.ns(c, "Syntax: %s SECURE {ON | OFF}", setSyntaxHead(cmd))
			return false
		}
		a.Secure = on
		if on {
			return save("Security is on for "+a.Name+": the ACCESS list never stands in for the password.", "to enable secure for "+a.Name)
		}
		return save("Security is off for "+a.Name+": a connection matching the ACCESS list counts as the owner.", "to disable secure for "+a.Name)
	case "HIDE":
		typed := strings.ToUpper(val)
		field := typed
		if field == "MASK" || field == "HOST" {
			field = "USERMASK" // Anope 2.1's and our old names
		}
		on, ok := onOff(argAt(args, 1))
		var p *bool
		var what string
		switch field {
		case "EMAIL":
			p, what = &a.HideEmail, "email"
		case "USERMASK":
			p, what = &a.HideHost, "last user@host"
		case "STATUS":
			p, what = &a.HideStatus, "oper status"
		case "QUIT":
			p, what = &a.HideQuit, "last quit message"
		}
		if p == nil || !ok {
			s.ns(c, "Syntax: %s HIDE {EMAIL | STATUS | USERMASK | QUIT} {ON | OFF}", setSyntaxHead(cmd))
			return false
		}
		*p = on
		shown := "shown to"
		if on {
			shown = "hidden from"
		}
		msg := fmt.Sprintf("%s %s is %s others in INFO.", whose, what, shown)
		if field == "STATUS" {
			if o, _ := s.store.OperByAccount(a.ID); o == nil { // QA 1.0.0: not an oper, so say what it means
				msg += " " + a.Name + " has no oper entry now, so INFO shows no oper status anyway; this applies if it gets one."
			}
		}
		return save(msg, "to change hide "+typed+" to "+strings.ToUpper(argAt(args, 1))+" for "+a.Name)
	case "AUTOOP":
		on, ok := onOff(val)
		if !ok {
			s.ns(c, "Syntax: %s AUTOOP {ON | OFF}", setSyntaxHead(cmd))
			return false
		}
		a.AutoOp = on
		if on {
			return save("Services will from now on set status modes on "+a.Name+" in channels.", "to enable autoop for "+a.Name)
		}
		return save("Services will no longer set status modes on "+a.Name+" in channels.", "to disable autoop for "+a.Name)
	case "KEEPMODES":
		on, ok := onOff(val)
		if !ok {
			s.ns(c, "Syntax: %s KEEPMODES {ON | OFF}", setSyntaxHead(cmd))
			return false
		}
		a.KeepModes, a.LastModes = on, ""
		if on {
			for _, oc := range s.onlineClients(a.ID) {
				a.LastModes = keptModes(oc.user)
			}
			return save("Keep modes for "+a.Name+" is now on.", "to enable keepmodes for "+a.Name)
		}
		return save("Keep modes for "+a.Name+" is now off.", "to disable keepmodes for "+a.Name)
	case "PRIVATE":
		on, ok := onOff(val)
		if !ok {
			s.ns(c, "Syntax: %s PRIVATE {ON | OFF}", setSyntaxHead(cmd))
			return false
		}
		a.Private = on
		if on {
			return save("Private option is now on for "+a.Name+".", "to enable private for "+a.Name)
		}
		return save("Private option is now off for "+a.Name+".", "to disable private for "+a.Name)
	case "NOEXPIRE":
		if cmd != "SASET" {
			s.ns(c, "Syntax: SASET NOEXPIRE <nick> {ON | OFF}")
			return false
		}
		on, ok := onOff(val)
		if !ok {
			s.ns(c, "Syntax: SASET NOEXPIRE <nick> {ON | OFF}")
			return false
		}
		a.NoExpire = on
		if on {
			return save("Nick "+a.Name+" will not expire.", "to enable noexpire for "+a.Name)
		}
		return save("Nick "+a.Name+" will expire.", "to disable noexpire for "+a.Name)
	case "URL":
		url := strings.Join(args, " ")
		a.URL = irc.Truncate(url, 300)
		if err := s.store.SaveAccount(a); err != nil {
			s.dbErr(c, s.nickServ, err)
			return false
		}
		if url == "" {
			s.ns(c, "URL for %s unset.", a.Name)
		} else {
			s.ns(c, "URL for %s set to %s.", a.Name, a.URL)
		}
		return true
	case "GREET":
		// Anope's greet module: shown by a channel's bot when you join a
		// channel with BotServ SET GREET on and the GREET privilege.
		greet := irc.Truncate(strings.Join(args, " "), 200)
		if err := s.store.SetGreet(a.ID, greet); err != nil {
			s.dbErr(c, s.nickServ, err)
			return false
		}
		if greet == "" {
			s.nsLog(c, a.ID, "to unset the greet of %s", a.Name)
			s.ns(c, "Greet message for %s unset.", a.Name)
		} else {
			s.nsLog(c, a.ID, "to change the greet of %s", a.Name)
			s.ns(c, "Greet message for %s changed to %s.", a.Name, greet)
		}
		return true
	case "DISPLAY":
		nicks, err := s.store.Nicks(a)
		if err != nil {
			s.dbErr(c, s.nickServ, err)
			return false
		}
		target := ""
		for _, n := range nicks {
			if irc.EqualFold(n, val) {
				target = n
			}
		}
		if val == "" || target == "" {
			s.ns(c, "The new display MUST be a nickname of the nickname group %s.", a.Name)
			return false
		}
		old := a.Name
		if err := s.renameAccount(a.ID, target); err != nil {
			s.dbErr(c, s.nickServ, err)
			return false
		}
		s.nsLog(c, a.ID, "to change the display of %s to %s", old, target)
		s.ns(c, "The new display is now %s.", target)
		return true
	default:
		s.ns(c, "Syntax: %s PASSWORD|EMAIL|KILL|SECURE|HIDE|AUTOOP|DISPLAY|GREET|KEEPMODES|PRIVATE|URL ... (see /ns help %s)", cmd, strings.ToLower(cmd))
	}
	return false
}

// setSyntaxHead is how a SET syntax line starts: "SET", or "SASET <nick>".
func setSyntaxHead(cmd string) string {
	if cmd == "SASET" {
		return "SASET <nick>"
	}
	return cmd
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
	if a.URL != "" {
		s.ns(c, "  URL           %s", a.URL)
	}
	if g := s.store.Greet(a.ID); g != "" {
		s.ns(c, "  Greet         %s", g)
	}
	if a.Suspended(time.Now()) {
		// ns_suspend show = "suspended, by, reason, on, expires" (everyone).
		s.ns(c, "  Suspended     This nickname is suspended.")
		s.ns(c, "  Suspended by  %s", a.SuspendedBy)
		if a.SuspendReason != "" {
			s.ns(c, "  Reason        %s", a.SuspendReason)
		}
		s.ns(c, "  Suspended on  %s", showTime(a.SuspendedAt))
		if a.SuspendUntil.Unix() != 0 {
			s.ns(c, "  Expires       %s", showTime(a.SuspendUntil))
		}
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
		switch s.killMode(a) {
		case store.KillQuick:
			opts = append(opts, "quick kill")
		case store.KillImmed:
			opts = append(opts, "immediate kill")
		default:
			opts = append(opts, "kill protection")
		}
	}
	if a.Secure {
		opts = append(opts, "security")
	}
	if a.Private {
		opts = append(opts, "private")
	}
	if a.AutoOp {
		opts = append(opts, "auto-op")
	}
	if a.KeepModes {
		opts = append(opts, "keep modes")
	}
	if a.NoExpire && insider {
		opts = append(opts, "no expire")
	}
	for _, h := range []struct {
		on   bool
		name string
	}{{a.HideEmail, "email"}, {a.HideHost, "usermask"}, {a.HideStatus, "status"}, {a.HideQuit, "quit"}} {
		if h.on {
			opts = append(opts, h.name+" hidden")
		}
	}
	if len(opts) == 0 {
		opts = []string{"none"}
	}
	s.ns(c, "  Options       %s", strings.Join(opts, ", "))
	s.showOperInfo(c, s.nickServ, false, a.ID)
}

func nsDrop(s *Services, c *Client, args []string) {
	if len(args) == 0 {
		s.ns(c, "Syntax: DROP <nick>")
		return
	}
	owner, ok := s.accountOf(c, args[0])
	if !ok {
		return
	}
	mine := c.accountID == owner.ID
	switch {
	case !mine && c.accountID == 0 && !c.can('s'):
		s.ns(c, "Identify first: /ns identify <password>")
		s.logDeniedUnreg(c, s.nickServ)
		return
	case !mine && !c.can('s'):
		s.ns(c, "%s isn't part of your account.", args[0])
		return
	case !mine && s.isServicesOper(owner):
		s.ns(c, "You may not drop other opers' nicks.")
		return
	}
	nicks, err := s.store.Nicks(owner)
	if err != nil {
		s.dbErr(c, s.nickServ, err)
		return
	}
	nick := args[0]
	for _, n := range nicks {
		if irc.EqualFold(n, nick) {
			nick = n // as registered
		}
	}
	s.nsLog(c, owner.ID, "to drop nickname %s (group: %s) (email: %s)", nick, owner.Name, orNone(owner.Email))
	if !mine {
		s.audit(c, s.nickServ, "DROP %s (account %s)", nick, owner.Name)
	}
	if len(nicks) <= 1 { // the last nick: the account goes with it
		by := "by its owner"
		if !mine {
			by = "by " + c.user.Nick
		}
		if err := s.dropAccount(owner, "founder "+owner.Name+" was dropped", by); err != nil {
			s.dbErr(c, s.nickServ, err)
			return
		}
		s.ns(c, "%s has been dropped.", nick)
		return
	}
	if err := s.store.RemoveNick(nick); err != nil {
		s.dbErr(c, s.nickServ, err)
		return
	}
	s.vhostGone(nick) // Anope's OnNickDrop: its vhost goes too
	if irc.EqualFold(nick, owner.Name) {
		next := nicks[1] // the oldest other nick, as Anope moves the display
		if err := s.renameAccount(owner.ID, next); err != nil {
			s.dbErr(c, s.nickServ, err)
			return
		}
		s.ns(c, "%s has been dropped. The account is now called %s.", nick, next)
		return
	}
	s.ns(c, "%s has been dropped.", nick)
}

// renameAccount gives account id the name of one of its nicks and tells
// everyone using it (account-notify, 900).
func (s *Services) renameAccount(id int64, name string) error {
	if err := s.store.RenameAccount(id, name); err != nil {
		return err
	}
	s.log.Printf("account %d renamed to %s", id, name)
	for _, other := range s.clients {
		if other.accountID == id && !other.closed {
			s.emit(&state.Login{UID: other.user.UID, Account: name})
			s.reply(other, irc.RplLoggedIn, other.user.Prefix(), name, "You are now logged in as "+name)
		}
	}
	return nil
}

// isServicesOper reports whether account a belongs to an IRC operator
// (Anope's IsServicesOper, for secureadmins).
func (s *Services) isServicesOper(a *store.Account) bool {
	if s.isAdminAccount(a) {
		return true
	}
	o, err := s.store.OperByAccount(a.ID)
	return err == nil && o != nil
}

func nsAccess(s *Services, c *Client, args []string) {
	a, ok := s.identified(c)
	if !ok {
		return
	}
	sub := strings.ToUpper(argAt(args, 0))
	var nick, mask string
	if sub == "LIST" {
		nick = argAt(args, 1)
	} else {
		if len(args) == 3 {
			nick = args[1]
		}
		if len(args) > 1 {
			mask = args[len(args)-1]
		}
	}
	target := a
	if nick != "" {
		t, ok := s.accountOf(c, nick)
		if !ok {
			return
		}
		if t.ID != a.ID && !c.can('s') {
			s.ns(c, "Only opers with flag s may use someone else's access list.")
			s.logDenied(c, s.nickServ)
			return
		}
		if t.ID != a.ID && sub != "LIST" && s.isServicesOper(t) {
			s.ns(c, "You may look at, but not change, the access list of other opers.")
			return
		}
		target = t
	}
	if mask != "" && (!strings.Contains(mask, "@") || strings.Contains(mask, "!")) {
		s.ns(c, "Masks look like user@host, for example ~bee@*.example.org.")
		return
	}
	masks, err := s.store.Access(target)
	if err != nil {
		s.dbErr(c, s.nickServ, err)
		return
	}
	has := func(m string) string {
		for _, x := range masks {
			if strings.EqualFold(x, m) {
				return x
			}
		}
		return ""
	}
	switch {
	case sub == "LIST":
		if len(masks) == 0 {
			s.ns(c, "%s's access list is empty.", target.Name)
			return
		}
		s.ns(c, "Access list for %s:", target.Name)
		for _, m := range masks {
			if mask == "" || irc.Match(mask, m) {
				s.ns(c, "    %s", m)
			}
		}
	case sub == "ADD" && mask != "":
		if len(masks) >= maxAccessMasks {
			s.ns(c, "%s's access list is full (%d masks).", target.Name, maxAccessMasks)
			return
		}
		if has(mask) != "" {
			s.ns(c, "%s is already on %s's access list.", mask, target.Name)
			return
		}
		if _, err := s.store.AddAccess(target, mask); err != nil {
			s.dbErr(c, s.nickServ, err)
			return
		}
		s.nsLog(c, target.ID, "to ADD mask %s to %s", mask, target.Name)
		s.ns(c, "%s added to %s's access list.", mask, target.Name)
	case sub == "DEL" && mask != "":
		m := has(mask)
		if m == "" {
			s.ns(c, "%s isn't on %s's access list.", mask, target.Name)
			return
		}
		if _, err := s.store.DelAccess(target, m); err != nil {
			s.dbErr(c, s.nickServ, err)
			return
		}
		s.nsLog(c, target.ID, "to DELETE mask %s from %s", m, target.Name)
		s.ns(c, "%s deleted from %s's access list.", m, target.Name)
	default:
		s.ns(c, "Syntax: ACCESS ADD [nick] <user@host> | DEL [nick] <user@host> | LIST [nick]")
	}
}

func nsStatus(s *Services, c *Client, args []string) {
	nicks := args
	if len(nicks) == 0 {
		nicks = []string{c.user.Nick}
	}
	if len(nicks) > 16 {
		nicks = nicks[:16]
	}
	name := func(id int64) string {
		if a, err := s.store.AccountByID(id); err == nil && a != nil {
			return a.Name
		}
		return ""
	}
	for _, nick := range nicks {
		code, acct := 0, ""
		tc := s.clientOf(s.net.UserByNick(nick))
		a, err := s.store.AccountByNick(nick)
		if err != nil {
			s.dbErr(c, s.nickServ, err)
			return
		}
		switch {
		case tc == nil || tc.closed:
		case a != nil && tc.accountID == a.ID:
			code, acct = 3, a.Name
		case a != nil && s.recognized(tc, a):
			code, acct = 2, name(tc.accountID)
		case a == nil:
		default:
			code, acct = 1, name(tc.accountID)
		}
		s.ns(c, "STATUS %s %d %s", nick, code, acct)
	}
}

// dropAccount removes a with its nicks and memos and logs out everyone
// using it. founderWhy and by go to the log.
func (s *Services) dropAccount(a *store.Account, founderWhy, by string) error {
	id, name := a.ID, a.Name
	nicks, _ := s.store.Nicks(a)
	defer func() {
		for _, n := range nicks {
			s.vhostGone(n) // their vhosts went with the account
		}
	}()
	changes, err := s.store.DeleteAccount(id)
	if err != nil {
		return err
	}
	s.clearLockouts(id)
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
