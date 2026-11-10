package server

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/fhazal/beeircd/internal/irc"
	"github.com/fhazal/beeircd/internal/store"
)

const (
	minPasswordLen  = 6
	maxNicksPerAcct = 10
	maxAccessMasks  = 10
	dropCodeFor     = 2 * time.Minute
)

func nickServCommands() map[string]svcCmd {
	return map[string]svcCmd{
		"REGISTER": {"REGISTER <password> [email]", "register the nick you are using",
			"Registers your current nick to you, so nobody else can use it.\nPasswords need at least 6 characters. The email is optional; opers use it to\nhelp if you forget your password.\nExample: /ns register honeypot42 bee@example.org", nsRegister},
		"IDENTIFY": {"IDENTIFY [nick] <password>", "prove the nick is yours",
			"Identifies you to your account. Give a nick to identify to an account while\nusing another nick.\nExample: /ns identify honeypot42", nsIdentify},
		"LOGOUT": {"LOGOUT", "stop being identified",
			"You stay connected with the same nick, but no longer identified.", nsLogout},
		"GHOST": {"GHOST <nick> [password]", "free your nick from an old connection",
			"Disconnects whoever is using one of your nicks, for example an old connection\nthat didn't close. No password needed when you are identified.\nExample: /ns ghost Bee_ honeypot42", nsGhost},
		"RECOVER": {"RECOVER <nick> [password]", "GHOST, then take the nick",
			"Like GHOST, then changes your nick to it and identifies you.\nExample: /ns recover Bee honeypot42", nsRecover},
		"GROUP": {"GROUP <nick> <password>", "add this nick to your account",
			"Adds the nick you are using now to the account that owns <nick>. After that\nboth nicks are yours and share one password and one memo box.\nExample, while using Bee_: /ns group Bee honeypot42", nsGroup},
		"UNGROUP": {"UNGROUP <nick>", "remove a nick from your account",
			"Takes one of your grouped nicks out of your account. The account's own name\ncan't be ungrouped; use DROP for that.", nsUngroup},
		"ACCESS": {"ACCESS ADD|DEL|LIST [mask]", "hosts that identify you by themselves",
			"Connections from a host on this list are identified automatically. The list is\nempty until you add one. Masks look like ~bee@*.example.org.\nExample: /ns access add ~bee@*.example.org", nsAccess},
		"SET": {"SET PASSWORD|EMAIL|ENFORCE|HIDE ...", "change your account",
			"SET PASSWORD <new password>\nSET EMAIL <email> or SET EMAIL OFF\nSET ENFORCE ON|OFF   rename people who don't identify within 60 seconds (on)\nSET HIDE EMAIL|HOST ON|OFF   what INFO shows to others (both hidden)", nsSet},
		"INFO": {"INFO [nick]", "who owns a nick, when last seen",
			"Shows when a nick was registered and last seen. You and opers see more.", nsInfo},
		"DROP": {"DROP", "unregister your account",
			"Removes your account with all its nicks and memos. You are asked to repeat\nthe command with a code first, so it can't happen by accident.", nsDrop},
		"LIST": {"LIST <pattern>", "search registered nicks (opers)",
			"Lists registered nicks matching a pattern, like Bee* or *serv.", nsList},
	}
}

// identified returns c's account, or tells them to identify first.
func (s *Server) identified(c *Client) (*store.Account, bool) {
	if c.accountID == 0 {
		s.ns(c, "Identify first: /ns identify <password>")
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
func (s *Server) accountOf(c *Client, nick string) (*store.Account, bool) {
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

func nsRegister(s *Server, c *Client, args []string) {
	u := c.user
	if len(args) == 0 {
		s.ns(c, "Syntax: REGISTER <password> [email]")
		return
	}
	if c.accountID != 0 {
		s.ns(c, "You're identified as %s already. To add %s to it: /ns group %s <password>", u.Account, u.Nick, u.Account)
		return
	}
	if a, err := s.store.AccountByNick(u.Nick); err != nil {
		s.dbErr(c, s.nickServ, err)
		return
	} else if a != nil {
		s.ns(c, "%s is already registered. If it's yours: /ns identify <password>", u.Nick)
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
	}
	nick := u.Nick
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
		s.login(c, a)
	})
}

func nsIdentify(s *Server, c *Client, args []string) {
	if len(args) == 0 {
		s.ns(c, "Syntax: IDENTIFY [nick] <password>")
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
		return
	}
	if c.accountID == a.ID {
		s.ns(c, "You're already identified as %s.", a.Name)
		return
	}
	s.checkPassword(c, a, pw, func(r pwResult) {
		if r != pwOK {
			s.pwProblem(c, a, r)
			return
		}
		fresh, err := s.store.AccountByID(a.ID)
		if err != nil || fresh == nil {
			s.ns(c, "%s was dropped a moment ago.", a.Name)
			return
		}
		s.ns(c, "Password accepted. You are identified as %s.", fresh.Name)
		s.login(c, fresh)
	})
}

func nsLogout(s *Server, c *Client, args []string) {
	if c.accountID == 0 {
		s.ns(c, "You aren't identified.")
		return
	}
	name := c.user.Account
	s.logout(c, true)
	s.ns(c, "You are no longer identified as %s.", name)
	s.nickCheck(c)
}

// ghostTarget finds whom GHOST or RECOVER is about.
func (s *Server) ghostTarget(c *Client, args []string, cmd string) (string, *store.Account, bool) {
	if len(args) == 0 {
		s.ns(c, "Syntax: %s <nick> [password]", cmd)
		return "", nil, false
	}
	nick := args[0]
	a, ok := s.accountOf(c, nick)
	if !ok {
		return "", nil, false
	}
	if c.accountID != a.ID && len(args) < 2 {
		s.ns(c, "Give the password of %s: /ns %s %s <password>", a.Name, strings.ToLower(cmd), nick)
		return "", nil, false
	}
	return nick, a, true
}

// withOwnership runs then after proving c owns a: by being identified to it
// or by the password.
func (s *Server) withOwnership(c *Client, a *store.Account, pw string, then func()) {
	if c.accountID == a.ID {
		then()
		return
	}
	s.checkPassword(c, a, pw, func(r pwResult) {
		if r != pwOK {
			s.pwProblem(c, a, r)
			return
		}
		then()
	})
}

// ghost disconnects whoever uses nick; false when nobody does.
func (s *Server) ghost(c *Client, nick string) bool {
	t := s.net.UserByNick(nick)
	if t == nil || t.Service || t == c.user {
		return false
	}
	if tc := s.clientOf(t); tc != nil {
		s.quit(tc, "Killed (NickServ (GHOST command used by "+c.user.Nick+"))")
		return true
	}
	return false
}

func nsGhost(s *Server, c *Client, args []string) {
	nick, a, ok := s.ghostTarget(c, args, "GHOST")
	if !ok {
		return
	}
	if t := s.net.UserByNick(nick); t == nil || t == c.user {
		s.ns(c, "Nobody else is using %s.", nick)
		return
	}
	s.withOwnership(c, a, argAt(args, 1), func() {
		if s.ghost(c, nick) {
			s.ns(c, "%s was disconnected. The nick is free.", nick)
		} else {
			s.ns(c, "Nobody else is using %s.", nick)
		}
	})
}

func nsRecover(s *Server, c *Client, args []string) {
	nick, a, ok := s.ghostTarget(c, args, "RECOVER")
	if !ok {
		return
	}
	if irc.EqualFold(nick, c.user.Nick) {
		s.ns(c, "You are using %s already.", c.user.Nick)
		return
	}
	s.withOwnership(c, a, argAt(args, 1), func() {
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
	})
}

func nsGroup(s *Server, c *Client, args []string) {
	u := c.user
	if len(args) < 1 {
		s.ns(c, "Syntax: GROUP <nick> <password>")
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
		s.login(c, fresh)
	})
}

func nsUngroup(s *Server, c *Client, args []string) {
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
		s.ns(c, "%s is the account's own name and can't be ungrouped. To remove everything: /ns drop", a.Name)
	default:
		if err := s.store.RemoveNick(nick); err != nil {
			s.dbErr(c, s.nickServ, err)
			return
		}
		s.ns(c, "%s is no longer part of %s.", nick, a.Name)
	}
}

func nsAccess(s *Server, c *Client, args []string) {
	a, ok := s.identified(c)
	if !ok {
		return
	}
	sub := strings.ToUpper(argAt(args, 0))
	mask := argAt(args, 1)
	switch {
	case sub == "LIST":
		masks, err := s.store.Access(a)
		if err != nil {
			s.dbErr(c, s.nickServ, err)
			return
		}
		if len(masks) == 0 {
			s.ns(c, "Your access list is empty.")
			return
		}
		s.ns(c, "Access list of %s:", a.Name)
		for i, m := range masks {
			s.ns(c, "  %d  %s", i+1, m)
		}
	case (sub == "ADD" || sub == "DEL") && mask == "":
		s.ns(c, "Syntax: ACCESS %s <mask>", sub)
	case sub == "ADD":
		if !strings.Contains(mask, "@") || strings.ContainsAny(mask, "! ") {
			s.ns(c, "Masks look like ~bee@*.example.org.")
			return
		}
		masks, err := s.store.Access(a)
		if err != nil {
			s.dbErr(c, s.nickServ, err)
			return
		}
		if len(masks) >= maxAccessMasks {
			s.ns(c, "Your access list is full (%d masks).", maxAccessMasks)
			return
		}
		if added, err := s.store.AddAccess(a, mask); err != nil {
			s.dbErr(c, s.nickServ, err)
		} else if !added {
			s.ns(c, "%s is on your access list already.", mask)
		} else {
			s.ns(c, "Added %s. Connections from there are identified automatically.", mask)
		}
	case sub == "DEL":
		if removed, err := s.store.DelAccess(a, mask); err != nil {
			s.dbErr(c, s.nickServ, err)
		} else if !removed {
			s.ns(c, "%s isn't on your access list.", mask)
		} else {
			s.ns(c, "Removed %s from your access list.", mask)
		}
	default:
		s.ns(c, "Syntax: ACCESS ADD|DEL|LIST [mask]")
	}
}

func nsSet(s *Server, c *Client, args []string) {
	a, ok := s.identified(c)
	if !ok {
		return
	}
	what := strings.ToUpper(argAt(args, 0))
	val := argAt(args, 1)
	save := func(msg string) {
		if err := s.store.SaveAccount(a); err != nil {
			s.dbErr(c, s.nickServ, err)
			return
		}
		s.ns(c, "%s", msg)
	}
	switch what {
	case "PASSWORD":
		if len(val) < minPasswordLen {
			s.ns(c, "Passwords need at least %d characters.", minPasswordLen)
			return
		}
		id := a.ID
		s.hashPassword(c, val, func(hash string) {
			fresh, err := s.store.AccountByID(id)
			if err != nil || fresh == nil {
				return
			}
			fresh.PassHash = hash
			if err := s.store.SaveAccount(fresh); err != nil {
				s.dbErr(c, s.nickServ, err)
				return
			}
			s.ns(c, "Your password is changed.")
		})
	case "EMAIL":
		switch {
		case strings.EqualFold(val, "OFF"):
			a.Email = ""
			save("Your email is removed.")
		case validEmail(val):
			a.Email = val
			save("Your email is now " + val + ".")
		default:
			s.ns(c, "Syntax: SET EMAIL <email> or SET EMAIL OFF")
		}
	case "ENFORCE":
		on, ok := onOff(val)
		if !ok {
			s.ns(c, "Syntax: SET ENFORCE ON|OFF")
			return
		}
		a.Enforce = on
		if on {
			save("Enforce is on: people who use your nicks without identifying get renamed.")
		} else {
			save("Enforce is off: people using your nicks are only told they are registered.")
		}
	case "HIDE":
		field := strings.ToUpper(val)
		on, ok := onOff(argAt(args, 2))
		if !ok || (field != "EMAIL" && field != "HOST") {
			s.ns(c, "Syntax: SET HIDE EMAIL|HOST ON|OFF")
			return
		}
		if field == "EMAIL" {
			a.HideEmail = on
		} else {
			a.HideHost = on
		}
		state := "shown to"
		if on {
			state = "hidden from"
		}
		save(fmt.Sprintf("Your %s is %s others in INFO.", strings.ToLower(field), state))
	default:
		s.ns(c, "Syntax: SET PASSWORD|EMAIL|ENFORCE|HIDE ... (see /ns help set)")
	}
}

func nsInfo(s *Server, c *Client, args []string) {
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
	online := false
	for _, other := range s.clients {
		if other.accountID == a.ID {
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
		opts = append(opts, "enforce")
	}
	if a.HideEmail {
		opts = append(opts, "email hidden")
	}
	if a.HideHost {
		opts = append(opts, "host hidden")
	}
	if len(opts) == 0 {
		opts = []string{"none"}
	}
	s.ns(c, "  Options       %s", strings.Join(opts, ", "))
}

func nsDrop(s *Server, c *Client, args []string) {
	a, ok := s.identified(c)
	if !ok {
		return
	}
	code := argAt(args, 0)
	if code == "" || code != c.dropCode || time.Now().After(c.dropUntil) {
		nicks, err := s.store.Nicks(a)
		if err != nil {
			s.dbErr(c, s.nickServ, err)
			return
		}
		c.dropCode, c.dropUntil = randomCode(), time.Now().Add(dropCodeFor)
		if code != "" {
			s.ns(c, "That code is wrong or too old.")
		}
		s.ns(c, "This removes the account %s with its nicks (%s) and all its memos, for good.", a.Name, strings.Join(nicks, ", "))
		s.ns(c, "To go ahead within 2 minutes: /ns drop %s", c.dropCode)
		return
	}
	c.dropCode = ""
	name := a.Name
	if err := s.dropAccount(a, "founder "+name+" dropped the account", "by its owner"); err != nil {
		s.dbErr(c, s.nickServ, err)
		return
	}
	s.ns(c, "The account %s is gone.", name)
}

// dropAccount removes a with its nicks and memos and logs out everyone
// using it. founderWhy and by go to the log.
func (s *Server) dropAccount(a *store.Account, founderWhy, by string) error {
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
			other.user.Account = ""
			s.setRegistered(other, false)
			o := s.out(other.user, "ACCOUNT", "*")
			o.needCap = "account-notify"
			s.toNeighbours(other.user, o, true)
			s.reply(other, irc.RplLoggedOut, other.user.Prefix(), "You are now logged out")
		}
		if other.operAccount == id {
			s.deoper(other, "Your account is gone, so you are no longer an operator.")
		}
	}
	return nil
}

func nsList(s *Server, c *Client, args []string) {
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
