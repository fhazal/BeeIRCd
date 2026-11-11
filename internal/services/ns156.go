package services

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/fhazal/beeircd/internal/irc"
	"github.com/fhazal/beeircd/internal/state"
	"github.com/fhazal/beeircd/internal/store"
)

// NickServ commands of step 1.5.6a, as Anope 2.0.20 has them: CERT (and
// identifying by certificate), AJOIN, GLIST, SUSPEND/UNSUSPEND, LIST with
// options, GETEMAIL and UPDATE. Replies and log lines are Anope's.

func ns156Commands() map[string]svcCmd {
	return map[string]svcCmd{
		"CERT": {"CERT {ADD | DEL | LIST} [nick] [fingerprint]", "certificates that identify you",
			"CERT ADD [nick] [fingerprint]\nCERT DEL [nick] <fingerprint>\nCERT LIST [nick]\nModifies or displays the certificate list for your nick.\nIf you connect to IRC and provide a client certificate with a\nmatching fingerprint in the cert list, you will be\nautomatically identified to services. Services Operators\nmay provide a nick to modify other users' certificate lists.\nCERT ADD adds the certificate you are connected with now; SASL EXTERNAL\nlogs in with it before you even pick a nick.\nExample: /ns cert add", nsCert},
		"AJOIN": {"AJOIN {ADD | DEL | LIST} [nick] [#chan] [key]", "channels you join when you identify",
			"AJOIN ADD [nick] <#chan>[,#chan2] [key]\nAJOIN DEL [nick] <#chan>[,#chan2]\nAJOIN LIST [nick]\nThis command manages your auto join list. When you identify\nyou will automatically join the channels on your auto join list.\nServices Operators may provide a nick to modify other users'\nauto join lists.\nExample: /ns ajoin add #bchat", nsAjoin},
		"GLIST": {"GLIST [nick]", "your nicks and when they expire",
			"Lists all nicknames in your group, and when each expires. Services\nOperators may give a nick to see another group.", nsGlist},
		"SUSPEND": {"SUSPEND <nick> [+expiry] [reason]", "close an account (opers)",
			"Suspends a registered nickname, which prevents it from being used\nwhile keeping all the data for that nick. If an expiry is given\nthe nick will be released after that period of time, else the\ndefault expiry from the configuration is used. Opers with flag s.", nsSuspend},
		"UNSUSPEND": {"UNSUSPEND <nick>", "open a suspended account again (opers)",
			"Unsuspends a nickname which allows it to be used again. Opers with flag s.", nsUnsuspend},
		"GETEMAIL": {"GETEMAIL <email>", "accounts using an email (opers)",
			"Returns the matching accounts that used given email. Wildcards like\n*@example.org work. Opers only.", nsGetEmail},
		"UPDATE": {"UPDATE", "check your memos and channel status again",
			"Updates your current status, i.e. it checks for new memos,\nsets needed channel modes and updates your vhost and\nyour userflags (lastseentime, etc).", nsUpdate},
	}
}

// nsTarget is the account a CERT/AJOIN/GLIST command is about: c's own,
// or (for opers with flag s) nick's. modify tells whether it changes
// something: opers may then not touch other opers' lists (Anope's
// secureadmins).
func (s *Services) nsTarget(c *Client, nick string, modify bool) (*store.Account, bool) {
	if nick == "" {
		return s.identified(c)
	}
	if c.accountID == 0 {
		s.ns(c, "Identify first: /ns identify <password>")
		return nil, false
	}
	a, err := s.store.AccountByNick(nick)
	if err != nil {
		s.dbErr(c, s.nickServ, err)
		return nil, false
	}
	if a == nil {
		s.ns(c, "Nick %s isn't registered.", nick)
		return nil, false
	}
	if a.ID != c.accountID && !c.can('s') {
		s.ns(c, "Access denied.")
		return nil, false
	}
	if modify && a.ID != c.accountID && s.isServicesOper(a) {
		return a, true // checked by the caller: its message differs per command
	}
	return a, true
}

func nsCert(s *Services, c *Client, args []string) {
	sub := strings.ToUpper(argAt(args, 0))
	var nick, fp string
	switch {
	case sub == "LIST":
		nick = argAt(args, 1)
	case len(args) == 3:
		nick, fp = args[1], args[2]
	case len(args) == 2:
		fp = args[1]
	}
	if sub != "LIST" && sub != "ADD" && sub != "DEL" || sub == "DEL" && fp == "" {
		s.ns(c, "Syntax: CERT {ADD | DEL | LIST} [nick] [fingerprint]")
		return
	}
	a, ok := s.nsTarget(c, nick, sub != "LIST")
	if !ok {
		return
	}
	if sub != "LIST" && a.ID != c.accountID && s.isServicesOper(a) {
		s.ns(c, "You may view but not modify the certificate list of other Services Operators.")
		return
	}
	certs := s.store.Certs(a)
	switch {
	case sub == "LIST":
		if len(certs) == 0 {
			s.ns(c, "%s's certificate list is empty.", a.Name)
			return
		}
		s.ns(c, "Certificate list for %s:", a.Name)
		for _, f := range certs {
			s.ns(c, "    %s", f)
		}
	case a.Suspended(time.Now()):
		s.ns(c, "Nick %s is suspended.", a.Name)
	case sub == "ADD":
		if len(certs) >= s.cfg.Services.CertMax {
			s.ns(c, "Sorry, the maximum of %d certificate entries has been reached.", s.cfg.Services.CertMax)
			return
		}
		if a.ID == c.accountID {
			if c.user.CertFP == "" {
				s.ns(c, "You are not using a client certificate.")
				return
			}
			fp = c.user.CertFP
		}
		fp = store.NormalizeFingerprint(fp)
		if len(fp) < 32 || strings.Trim(fp, "0123456789abcdef") != "" {
			s.ns(c, "%s isn't a fingerprint. One is 64 hex digits (SHA-256), like WHOIS shows it.", fp)
			return
		}
		for _, f := range certs {
			if f == fp {
				s.ns(c, "Fingerprint %s already present on %s's certificate list.", fp, a.Name)
				return
			}
		}
		if s.store.AccountByCert(fp) != nil {
			s.ns(c, "Fingerprint %s is already in use.", fp)
			return
		}
		if _, err := s.store.AddCert(a, fp, time.Now()); err != nil {
			s.dbErr(c, s.nickServ, err)
			return
		}
		s.nsLog(c, a.ID, "to ADD certificate fingerprint %s to %s", fp, a.Name)
		s.ns(c, "%s added to %s's certificate list.", fp, a.Name)
	default:
		fp = store.NormalizeFingerprint(fp)
		gone, err := s.store.DelCert(a, fp)
		if err != nil {
			s.dbErr(c, s.nickServ, err)
			return
		}
		if !gone {
			s.ns(c, "%s not found on %s's certificate list.", fp, a.Name)
			return
		}
		s.nsLog(c, a.ID, "to DELETE certificate fingerprint %s from %s", fp, a.Name)
		s.ns(c, "%s deleted from %s's certificate list.", fp, a.Name)
	}
}

// loginsFull reports (and tells c) that a has maxlogins connections
// identified already.
func (s *Services) loginsFull(c *Client, a *store.Account) bool {
	max := s.cfg.Services.MaxLogins
	if max <= 0 || len(s.onlineClients(a.ID)) < max {
		return false
	}
	s.ns(c, "Account %s has already reached the maximum number of simultaneous logins (%d).", a.Name, max)
	return true
}

// certLogin identifies c by its certificate when connecting (Anope's
// OnFingerprint): to the account whose CERT list has it.
func (s *Services) certLogin(c *Client) {
	u := c.user
	if c.accountID != 0 || u.CertFP == "" {
		return
	}
	a := s.store.AccountByCert(u.CertFP)
	if a == nil || a.Suspended(time.Now()) || s.loginsFull(c, a) {
		return
	}
	s.login(c, a)
	s.ns(c, "SSL certificate fingerprint accepted, you are now identified to %s.", a.Name)
	s.logTo(s.nickServ, anopeMask(u)+" automatically identified for account "+a.Name+" via SSL certificate fingerprint")
}

// certValidate is Anope's OnNickValidate of ns_cert: someone taking a nick
// whose account has their certificate is identified to it.
func (s *Services) certValidate(c *Client, a *store.Account) bool {
	fp := c.user.CertFP
	if fp == "" || c.accountID == a.ID {
		return false
	}
	if owner := s.store.AccountByCert(fp); owner == nil || owner.ID != a.ID {
		return false
	}
	if s.loginsFull(c, a) {
		return false
	}
	s.login(c, a)
	s.ns(c, "SSL certificate fingerprint accepted, you are now identified.")
	s.logTo(s.nickServ, anopeMask(c.user)+" automatically identified for account "+a.Name+" via SSL certificate fingerprint")
	return true
}

func nsAjoin(s *Services, c *Client, args []string) {
	sub := strings.ToUpper(argAt(args, 0))
	rest := args[min(1, len(args)):]
	// An optional nick comes first unless it is the channel.
	nick := ""
	if sub == "LIST" {
		nick = argAt(rest, 0)
	} else if len(rest) > 0 && !irc.IsChannel(rest[0]) {
		nick, rest = rest[0], rest[1:]
	}
	if sub != "LIST" && sub != "ADD" && sub != "DEL" || sub != "LIST" && len(rest) == 0 {
		s.ns(c, "Syntax: AJOIN {ADD | DEL | LIST} [nick] [#chan] [key]")
		return
	}
	a, ok := s.nsTarget(c, nick, sub != "LIST")
	if !ok {
		return
	}
	list := s.store.Ajoins(a)
	switch {
	case sub == "LIST":
		if len(list) == 0 {
			s.ns(c, "%s's auto join list is empty.", a.Name)
			return
		}
		s.ns(c, "%s's auto join list:", a.Name)
		s.ns(c, "  Number  Channel             Key")
		for i, e := range list {
			s.ns(c, "  %-6d  %-18s  %s", i+1, e.Channel, e.Key)
		}
	case a.Suspended(time.Now()):
		s.ns(c, "Nick %s is suspended.", a.Name)
	case sub == "ADD":
		key := argAt(rest, 1)
		var added, already, invalid []string
		for _, ch := range strings.Split(rest[0], ",") {
			if ch == "" {
				continue
			}
			if len(list)+len(added) >= s.cfg.Services.AjoinMax {
				s.ns(c, "Sorry, the maximum of %d auto join entries has been reached.", s.cfg.Services.AjoinMax)
				return
			}
			if !irc.ValidChannel(ch, s.cfg.Limits.ChannelLen) {
				s.ns(c, "Channel %s is not valid.", ch)
				return
			}
			dup := false
			for _, e := range list {
				dup = dup || irc.EqualFold(e.Channel, ch)
			}
			for _, e := range added {
				dup = dup || irc.EqualFold(e, ch)
			}
			if dup {
				already = append(already, ch)
				continue
			}
			k := key
			if k != "" && irc.CleanKey(k) != k {
				invalid = append(invalid, ch)
				continue
			}
			if err := s.store.AddAjoin(a, ch, k); err != nil {
				s.dbErr(c, s.nickServ, err)
				return
			}
			added = append(added, ch)
		}
		if len(already) > 0 {
			s.ns(c, "%s is already on %s's auto join list.", strings.Join(already, ", "), a.Name)
		}
		if len(invalid) > 0 {
			s.ns(c, "%s had an invalid key specified, and was thus ignored.", strings.Join(invalid, ", "))
		}
		if len(added) == 0 {
			return
		}
		s.nsLog(c, a.ID, "to ADD channel %s to %s", strings.Join(added, ","), a.Name)
		s.ns(c, "%s added to %s's auto join list.", strings.Join(added, ", "), a.Name)
	default:
		var gone, missing []string
		for _, ch := range strings.Split(rest[0], ",") {
			if ch == "" {
				continue
			}
			ok, err := s.store.DelAjoin(a, ch)
			if err != nil {
				s.dbErr(c, s.nickServ, err)
				return
			}
			if ok {
				gone = append(gone, ch)
			} else {
				missing = append(missing, ch)
			}
		}
		if len(missing) > 0 {
			s.ns(c, "%s was not found on %s's auto join list.", strings.Join(missing, ", "), a.Name)
		}
		if len(gone) == 0 {
			return
		}
		s.nsLog(c, a.ID, "to DELETE channel %s from %s", strings.Join(gone, ","), a.Name)
		s.ns(c, "%s was removed from %s's auto join list.", strings.Join(gone, ", "), a.Name)
	}
}

// autoJoin joins c to the channels on a's auto join list (Anope's
// ns_ajoin OnUserLogin): channels that would refuse them are left out,
// unless their access lets ChanServ invite them.
func (s *Services) autoJoin(c *Client, a *store.Account) {
	u := c.user
	for _, e := range s.store.Ajoins(a) {
		if len(u.Channels) >= s.cfg.Limits.MaxChannels {
			return
		}
		reg, _ := s.store.ChannelByName(e.Channel)
		if reg != nil && reg.Suspended(time.Now()) {
			continue
		}
		if _, ok := s.forbidden(store.ForbidChan, e.Channel); ok && !c.isOper() {
			continue
		}
		live := s.net.Channel(e.Channel)
		needInvite := false
		if live != nil {
			if live.Member(u) != nil {
				continue
			}
			key := e.Key
			if live.HasMode('k') && reg != nil && s.userHas(reg, u, "GETKEY") {
				key = live.Key
			}
			switch {
			case live.HasMode('O') && !u.HasMode('o'):
				continue
			case live.IsBanned(u):
				needInvite = true
			case live.HasMode('i') && !live.Matches('I', u):
				needInvite = true
			case live.HasMode('k') && key != live.Key:
				needInvite = true
			case live.HasMode('l') && len(live.Members) >= live.Limit:
				needInvite = true
			case live.HasMode('R') && u.Account == "":
				needInvite = true
			}
		}
		if needInvite {
			if reg == nil || !s.userHas(reg, u, "INVITE") {
				continue
			}
			s.emit(&state.Invite{Source: s.chanServ.user.UID, UID: u.UID, Channel: live.Name})
		}
		s.host.ForceJoin(u.UID, e.Channel)
	}
}

func nsGlist(s *Services, c *Client, args []string) {
	nick := argAt(args, 0)
	a, ok := s.nsTarget(c, nick, false)
	if !ok {
		return
	}
	nicks, err := s.store.Nicks(a)
	if err != nil {
		s.dbErr(c, s.nickServ, err)
		return
	}
	if nick != "" {
		s.ns(c, "List of nicknames in the group of %s:", a.Name)
	} else {
		s.ns(c, "List of nicknames in your group:")
	}
	expires := "Does not expire"
	if !a.NoExpire && a.SuspendedBy == "" && s.cfg.Services.NickExpire.Duration > 0 {
		expires = showTime(a.LastSeen.Add(s.cfg.Services.NickExpire.Duration))
	}
	width := 4
	for _, n := range nicks {
		width = max(width, len(n))
	}
	s.ns(c, "  %-*s  %s", width, "Nick", "Expires")
	for _, n := range nicks {
		s.ns(c, "  %-*s  %s", width, n, expires)
	}
	s.ns(c, "%d nickname(s) in the group.", len(nicks))
}

func nsSuspend(s *Services, c *Client, args []string) {
	if !s.needFlag(c, s.nickServ, 's') {
		return
	}
	if len(args) == 0 {
		s.ns(c, "Syntax: SUSPEND <nick> [+expiry] [reason]")
		return
	}
	nick, rest := args[0], args[1:]
	var life time.Duration
	if len(rest) > 0 && strings.HasPrefix(rest[0], "+") {
		d, ok := parseExpiry(rest[0])
		if !ok {
			s.ns(c, "Invalid expiry time.")
			return
		}
		life, rest = d, rest[1:]
	}
	a, err := s.store.AccountByNick(nick)
	if err != nil {
		s.dbErr(c, s.nickServ, err)
		return
	}
	if a == nil {
		s.ns(c, "Nick %s isn't registered.", nick)
		return
	}
	if s.isServicesOper(a) || s.isAdminAccount(a) {
		s.ns(c, "You may not suspend other Services Operators' nicknames.")
		return
	}
	if a.Suspended(time.Now()) {
		s.ns(c, "%s is already suspended.", a.Name)
		return
	}
	reason := irc.Truncate(strings.Join(rest, " "), 200)
	now := time.Now()
	a.SuspendedBy, a.SuspendReason, a.SuspendedAt, a.SuspendUntil = c.user.Nick, reason, now, time.Unix(0, 0)
	if life > 0 {
		a.SuspendUntil = now.Add(life)
	}
	a.LastQuit = reason
	if err := s.store.SaveAccount(a); err != nil {
		s.dbErr(c, s.nickServ, err)
		return
	}
	// Whoever is identified to it, or uses one of its nicks, is logged out
	// and renamed.
	nicks, _ := s.store.Nicks(a)
	for _, oc := range s.clients {
		if oc.closed {
			continue
		}
		uses := false
		for _, n := range nicks {
			uses = uses || irc.EqualFold(n, oc.user.Nick)
		}
		if oc.accountID == a.ID {
			s.logout(oc, true)
		}
		if uses {
			guest := s.guestNick()
			s.forceNick(oc, guest)
			s.ns(oc, "Nick %s is suspended.", nick)
		}
	}
	shown := orNone(reason)
	if reason == "" {
		shown = "No reason"
	}
	until := "never"
	if life > 0 {
		until = anopeTime(a.SuspendUntil)
	}
	s.logCmd(c, s.nickServ, logAdmin, "", fmt.Sprintf("for %s (%s), expires on %s", nick, shown, until))
	s.ns(c, "Nick %s is now suspended.", nick)
}

func nsUnsuspend(s *Services, c *Client, args []string) {
	if !s.needFlag(c, s.nickServ, 's') {
		return
	}
	nick := argAt(args, 0)
	if nick == "" {
		s.ns(c, "Syntax: UNSUSPEND <nick>")
		return
	}
	a, err := s.store.AccountByNick(nick)
	if err != nil {
		s.dbErr(c, s.nickServ, err)
		return
	}
	if a == nil {
		s.ns(c, "Nick %s isn't registered.", nick)
		return
	}
	if a.SuspendedBy == "" {
		s.ns(c, "Nick %s is not suspended.", nick)
		return
	}
	by, reason := a.SuspendedBy, a.SuspendReason
	s.unsuspendAccount(a)
	if reason == "" {
		reason = "No reason"
	}
	s.logCmd(c, s.nickServ, logAdmin, "", fmt.Sprintf("for %s which was suspended by %s for: %s", nick, by, reason))
	s.ns(c, "Nick %s is now released.", nick)
}

func (s *Services) unsuspendAccount(a *store.Account) {
	a.SuspendedBy, a.SuspendReason, a.SuspendedAt, a.SuspendUntil = "", "", time.Unix(0, 0), time.Unix(0, 0)
	a.LastSeen = time.Now() // don't expire it the moment it opens
	if err := s.store.SaveAccount(a); err != nil {
		s.log.Printf("services database: %v", err)
	}
}

// expireAccountSuspends lifts suspensions that ran out (Anope logs
// "Expiring suspend for nick").
func (s *Services) expireAccountSuspends(now time.Time) {
	for _, a := range s.store.SuspendedAccounts() {
		if !a.Suspended(now) {
			s.unsuspendAccount(a)
			s.logTo(s.nickServ, "Expiring suspend for "+a.Name)
		}
	}
}

func nsGetEmail(s *Services, c *Client, args []string) {
	if !c.isOper() {
		s.ns(c, "Access denied.")
		return
	}
	email := argAt(args, 0)
	if email == "" {
		s.ns(c, "Syntax: GETEMAIL <email>")
		return
	}
	s.logCmd(c, s.nickServ, logAdmin, "", "on "+email)
	found := s.store.AccountsByEmail(email)
	for _, a := range found {
		s.ns(c, "Email matched: %s (%s) to %s.", a.Name, a.Email, email)
	}
	if len(found) == 0 {
		s.ns(c, "No registrations matching %s were found.", email)
	}
}

func nsUpdate(s *Services, c *Client, args []string) {
	a, ok := s.identified(c)
	if !ok {
		return
	}
	a.LastSeen, a.LastHost = time.Now(), c.user.Ident+"@"+c.user.RealHost
	if err := s.store.SaveAccount(a); err != nil {
		s.log.Printf("services database: %v", err)
	}
	s.csOnIdentify(c)
	s.memoSummary(c, a)
	s.hsLogin(c)
	s.ns(c, "Status updated (memos, vhost, chmodes, flags).")
}

// nsList is Anope 2.0.20's LIST: everyone may search; private accounts are
// left out for people who aren't opers, and opers may add SUSPENDED and
// NOEXPIRE. A pattern like #1-10 shows those entries.
func nsList(s *Services, c *Client, args []string) {
	pattern := argAt(args, 0)
	if pattern == "" {
		s.ns(c, "Syntax: LIST <pattern> [SUSPENDED] [NOEXPIRE] [UNCONFIRMED]")
		return
	}
	oper := c.isOper()
	from, to := 0, 0
	if pattern[0] == '#' {
		a, b, ok := strings.Cut(pattern[1:], "-")
		n1, e1 := strconv.Atoi(a)
		n2, e2 := strconv.Atoi(b)
		if !ok || e1 != nil || e2 != nil {
			s.ns(c, "Incorrect range specified. The correct syntax is #from-to.")
			return
		}
		from, to, pattern = n1, n2, "*"
	}
	var suspended, noexpire, unconfirmed bool
	if oper {
		for _, k := range args[1:] {
			switch strings.ToUpper(k) {
			case "SUSPENDED":
				suspended = true
			case "NOEXPIRE":
				noexpire = true
			case "UNCONFIRMED":
				unconfirmed = true // no unconfirmed accounts here: matches nothing
			}
		}
	}
	all, err := s.store.AllNicks()
	if err != nil {
		s.dbErr(c, s.nickServ, err)
		return
	}
	now := time.Now()
	type row struct{ nick, mask string }
	var rows []row
	count, shown := 0, 0
	listMax := s.cfg.Services.ListMax
	for _, e := range all {
		a, _ := s.store.AccountByNick(e.Nick)
		if a == nil {
			continue
		}
		mine := a.ID == c.accountID
		switch {
		case a.Private && !oper && !mine:
			continue
		case noexpire && !a.NoExpire, suspended && !a.Suspended(now), unconfirmed:
			continue
		}
		last := a.LastHost
		if last == "" {
			last = "*@*"
		}
		if !irc.EqualFold(e.Nick, pattern) && !irc.Match(pattern, e.Nick+"!"+last) {
			continue
		}
		count++
		if (from == 0 && to == 0 || count >= from && count <= to) && shown < listMax {
			shown++
			nick := e.Nick
			if oper && a.NoExpire {
				nick = "!" + nick
			}
			mask := a.LastHost
			switch {
			case a.HideHost && !oper && !mine:
				mask = "[Hostname hidden]"
			case a.Suspended(now):
				mask = "[Suspended]"
			}
			rows = append(rows, row{nick, mask})
		}
	}
	s.ns(c, "List of entries matching %s:", pattern)
	width := 4
	for _, r := range rows {
		width = max(width, len(r.nick))
	}
	if len(rows) > 0 {
		s.ns(c, "  %-*s  %s", width, "Nick", "Last usermask")
	}
	for _, r := range rows {
		s.ns(c, "  %-*s  %s", width, r.nick, r.mask)
	}
	s.ns(c, "End of list - %d/%d matches shown.", shown, count)
}

// keepModesChars are the user modes SET KEEPMODES brings back: the ones
// people set themselves.
const keepModesChars = "iwx"

// keptModes is the part of u's modes KEEPMODES keeps, like "+iw".
func keptModes(u *state.User) string {
	var b []byte
	for _, m := range []byte(keepModesChars) {
		if u.HasMode(m) {
			b = append(b, m)
		}
	}
	if len(b) == 0 {
		return ""
	}
	return "+" + string(b)
}

// saveModes keeps c's user modes for KEEPMODES.
func (s *Services) saveModes(c *Client) {
	if c.accountID == 0 || c.user == nil {
		return
	}
	a, err := s.store.AccountByID(c.accountID)
	if err != nil || a == nil || !a.KeepModes {
		return
	}
	if m := keptModes(c.user); m != a.LastModes {
		a.LastModes = m
		if err := s.store.SaveAccount(a); err != nil {
			s.log.Printf("services database: %v", err)
		}
	}
}

// afterLogin runs once someone is identified (IDENTIFY, SASL, certificate):
// their kept user modes come back and the auto join list is joined.
func (s *Services) afterLogin(c *Client, a *store.Account) {
	if a.KeepModes && a.LastModes != "" {
		s.host.SetUserModes(c.user.UID, s.nickServ.user.UID, a.LastModes)
	}
	s.autoJoin(c, a)
}
