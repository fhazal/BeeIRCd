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

// HostServ, as Anope 2.0.20 (modules/commands/hs_*.cpp and
// pseudoclients/hostserv.cpp). A vhost belongs to a nick; it shows when
// someone identifies using that nick, or with ON. Oper commands need flag h
// (Anope's hostserv/set, /del and /list).

// Anope's messages (include/language.h).
const (
	hostNotAssigned    = "Please contact an Operator to get a vHost assigned to this nick."
	hostSetError       = "A vHost must be in the format of a valid hostname."
	hostSetIdentError  = "A vHost ident must be in the format of a valid ident."
	hostSetTooLong     = "Error! The vHost is too long, please use a hostname shorter than %d characters."
	hostSetIdentTooLng = "Error! The vHost ident is too long, please use an ident shorter than %d characters."
	listIncorrectRange = "Incorrect range specified. The correct syntax is \x02#\x1ffrom\x1f-\x1fto\x1f\x02."
)

func hostServCommands() map[string]svcCmd {
	return map[string]svcCmd{
		"ON": {"ON", "activates your assigned vhost",
			"Activates the vhost currently assigned to the nick in use.\nWhen you use this command any user who performs a /whois\non you will see the vhost instead of your real host/IP address.", hsOn},
		"OFF": {"OFF", "deactivates your assigned vhost",
			"Deactivates the vhost currently assigned to the nick in use.\nWhen you use this command any user who performs a /whois\non you will see your real host/IP address.", hsOff},
		"GROUP": {"GROUP", "syncs the vhost for all nicks in a group",
			"This command allows users to set the vhost of their\nCURRENT nick to be the vhost for all nicks in the same\ngroup.", hsGroup},
		"REQUEST": {"REQUEST [ident@]vhost", "request a vHost for your nick",
			"Request the given vHost to be activated for your nick by the\nnetwork administrators. Please be patient while your request\nis being considered.", hsRequest},
		"SET": {"SET <nick> [ident@]hostmask", "set the vhost of another user",
			"Sets the vhost for the given nick to that of the given\nhostmask. If your IRCd supports vIdents, then using\nSET <nick> <ident>@<hostmask> set idents for users as\nwell as vhosts.", hsSet(false)},
		"SETALL": {"SETALL <nick> [ident@]hostmask", "set the vhost for all nicks in a group",
			"Sets the vhost for all nicks in the same group as that\nof the given nick. If your IRCd supports vIdents, then\nusing SETALL <nick> <ident>@<hostmask> will set idents\nfor users as well as vhosts.", hsSet(true)},
		"DEL": {"DEL <nick>", "delete the vhost of another user",
			"Deletes the vhost assigned to the given nick from the\ndatabase.", hsDel(false)},
		"DELALL": {"DELALL <nick>", "delete the vhost for all nicks in a group",
			"Deletes the vhost for all nicks in the same group as\nthat of the given nick.", hsDel(true)},
		"LIST": {"LIST [<key>|<#X-Y>]", "displays one or more vhost entries",
			"This command lists registered vhosts to the operator.\nIf a key is specified, only entries whose nick or vhost\nmatch the pattern given in key are displayed e.g. Rob* for\nall entries beginning with \"Rob\".\nIf a #X-Y style is used, only entries between the range of X\nand Y will be displayed, e.g. #1-3 will display the first 3\nnick/vhost entries.", hsList},
		"WAITING": {"WAITING", "retrieves the vhost requests",
			"This command retrieves the vhost requests.", hsWaiting},
		"ACTIVATE": {"ACTIVATE <nick>", "approve the requested vHost of a user",
			"Activate the requested vHost for the given nick.", hsActivate},
		"REJECT": {"REJECT <nick> [reason]", "reject the requested vHost of a user",
			"Reject the requested vHost for the given nick.", hsReject},
	}
}

func (s *Services) hs(c *Client, format string, a ...any) {
	s.svcReply(c, s.hostServ, fmt.Sprintf(format, a...))
}

// hsLog logs a HostServ command: COMMAND, or ADMIN for the oper ones.
func (s *Services) hsLog(c *Client, kind, format string, a ...any) {
	s.logCmd(c, s.hostServ, kind, "", fmt.Sprintf(format, a...))
}

// parseVHost checks [ident@]host as Anope does (vhost_chars, dots, the
// first/last character, lengths), telling c what is wrong.
func (s *Services) parseVHost(c *Client, arg string) (ident, host string, ok bool) {
	h := s.cfg.HostServ
	ident, host = "", arg
	if i := strings.LastIndexByte(arg, '@'); i >= 0 {
		ident, host = arg[:i], arg[i+1:]
		if len(ident) > h.UserLen {
			s.hs(c, hostSetIdentTooLng, h.UserLen)
			return "", "", false
		}
		if !validIdent(ident) {
			s.hs(c, "%s", hostSetIdentError)
			return "", "", false
		}
	}
	if len(host) > h.HostLen {
		s.hs(c, hostSetTooLong, h.HostLen)
		return "", "", false
	}
	if !s.validVHost(host) {
		s.hs(c, "%s", hostSetError)
		return "", "", false
	}
	return ident, host, true
}

// validVHost is Anope's IsHostValid with the networkinfo vhost rules.
func (s *Services) validVHost(host string) bool {
	h := s.cfg.HostServ
	if host == "" {
		return false
	}
	if strings.ContainsAny(host[:1], h.StartOrEnd()) || strings.ContainsAny(host[len(host)-1:], h.StartOrEnd()) {
		return false
	}
	dots := 0
	for i := 0; i < len(host); i++ {
		if strings.IndexByte(h.VhostChars, host[i]) < 0 {
			return false
		}
		if host[i] == '.' {
			dots++
		}
	}
	return dots > 0 || h.AllowUndotted
}

// validIdent is Anope's IsIdentValid: letters, digits and "-._[]\`^{|}",
// not starting with "-".
func validIdent(ident string) bool {
	if ident == "" || ident[0] == '-' {
		return false
	}
	for i := 0; i < len(ident); i++ {
		ch := ident[i]
		if !(ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || ch >= '0' && ch <= '9' || strings.IndexByte("-._[]\\`^{|}", ch) >= 0) {
			return false
		}
	}
	return true
}

// ownNick reports whether the nick c is using is registered to their
// account (Anope's na->nc == source.GetAccount()).
func (s *Services) ownNick(c *Client) bool {
	if c.accountID == 0 {
		return false
	}
	a, err := s.store.AccountByNick(c.user.Nick)
	return err == nil && a != nil && a.ID == c.accountID
}

// --- showing and hiding a vhost ---

// showVHost makes v the host (and ident) c shows.
func (s *Services) showVHost(c *Client, v store.VHost) {
	u := c.user
	ident := v.Ident
	if ident == "" {
		ident = u.TrueIdent()
	}
	s.emit(&state.HostChange{UID: u.UID, Host: v.Host, Ident: ident})
	s.host.Numeric(u.UID, irc.RplHostHidden, v.Host, "is now your displayed host")
	c.vhostOn = true
}

// hideVHost puts back the cloak (or the real host) and the real ident.
func (s *Services) hideVHost(c *Client) {
	u := c.user
	host := s.host.DefaultHost(u.UID)
	c.vhostOn = false
	if host == u.Host && u.RealIdent == "" {
		return
	}
	s.emit(&state.HostChange{UID: u.UID, Host: host, Ident: u.TrueIdent()})
	s.host.Numeric(u.UID, irc.RplHostHidden, host, "is now your displayed host")
}

// activated tells c their vhost shows (HostServ's line).
func (s *Services) activated(c *Client, v store.VHost) {
	if v.Ident != "" {
		s.hs(c, "Your vhost of \x02%s\x02@\x02%s\x02 is now activated.", v.Ident, v.Host)
	} else {
		s.hs(c, "Your vhost of \x02%s\x02 is now activated.", v.Host)
	}
}

// hsLogin shows the vhost of the nick c identified with (Anope's
// OnUserLogin; also NS UPDATE).
func (s *Services) hsLogin(c *Client) {
	if !s.ownNick(c) {
		return
	}
	v, ok := s.store.VHostOf(c.user.Nick)
	if !ok || c.vhostOn && c.user.Host == v.Host && (v.Ident == "" || c.user.Ident == v.Ident) {
		return
	}
	s.showVHost(c, v)
	s.activated(c, v)
}

// RestoreHost gives uid back the host they show without an oper vhost:
// their HostServ vhost when it is on, else the cloak or real host.
func (s *Services) RestoreHost(uid string) {
	c := s.clients[uid]
	if c == nil || c.closed {
		return
	}
	if c.vhostOn && s.ownNick(c) {
		if v, ok := s.store.VHostOf(c.user.Nick); ok {
			s.showVHost(c, v)
			return
		}
	}
	s.hideVHost(c)
}

// VHostOn reports whether uid shows a HostServ vhost (the server keeps it
// when user mode +x changes).
func (s *Services) VHostOn(uid string) bool {
	c := s.clients[uid]
	return c != nil && c.vhostOn
}

// vhostSet runs after an oper set or approved nick's vhost: with
// activate_on_set, whoever uses that nick sees it at once.
func (s *Services) vhostSet(nick string) {
	if !s.cfg.HostServ.ActivateOnSet {
		return
	}
	v, ok := s.store.VHostOf(nick)
	if !ok {
		return
	}
	for _, oc := range s.clients {
		if !oc.closed && irc.EqualFold(oc.user.Nick, nick) && s.ownNick(oc) {
			s.showVHost(oc, v)
			s.activated(oc, v)
		}
	}
}

// vhostGone runs after nick's vhost was deleted: whoever shows it gets
// their normal host back (Anope's OnDeleteVhost).
func (s *Services) vhostGone(nick string) {
	for _, oc := range s.clients {
		if !oc.closed && oc.vhostOn && irc.EqualFold(oc.user.Nick, nick) {
			s.hideVHost(oc)
		}
	}
}

// --- commands for everyone ---

func hsOn(s *Services, c *Client, args []string) {
	if _, ok := s.identified(c); !ok {
		return
	}
	v, ok := s.store.VHostOf(c.user.Nick)
	if !s.ownNick(c) || !ok {
		s.hs(c, "%s", hostNotAssigned)
		return
	}
	s.showVHost(c, v)
	s.activated(c, v)
	s.hsLog(c, logCommand, "to enable their vhost of %s", v.Mask())
}

func hsOff(s *Services, c *Client, args []string) {
	if _, ok := s.identified(c); !ok {
		return
	}
	if _, ok := s.store.VHostOf(c.user.Nick); !s.ownNick(c) || !ok {
		s.hs(c, "%s", hostNotAssigned)
		return
	}
	s.hideVHost(c)
	s.hsLog(c, logCommand, "to disable their vhost")
	s.hs(c, "Your vhost was removed and the normal cloaking restored.")
}

func hsGroup(s *Services, c *Client, args []string) {
	a, ok := s.identified(c)
	if !ok {
		return
	}
	v, has := s.store.VHostOf(c.user.Nick)
	if !s.ownNick(c) || !has {
		s.hs(c, "%s", hostNotAssigned)
		return
	}
	now := time.Now()
	for _, n := range s.store.GroupNicks(c.user.Nick) {
		if err := s.store.SetVHost(n, v.Ident, v.Host, v.Creator, now); err != nil {
			s.dbErr(c, s.hostServ, err)
			return
		}
	}
	if v.Ident != "" {
		s.hs(c, "All vhosts in the group \x02%s\x02 have been set to \x02%s\x02@\x02%s\x02.", a.Name, v.Ident, v.Host)
	} else {
		s.hs(c, "All vhosts in the group \x02%s\x02 have been set to \x02%s\x02.", a.Name, v.Host)
	}
}

func hsRequest(s *Services, c *Client, args []string) {
	if _, ok := s.identified(c); !ok {
		return
	}
	if len(args) == 0 {
		s.hs(c, "Syntax: REQUEST [ident@]vhost")
		return
	}
	if !s.ownNick(c) {
		s.hs(c, "Access denied.")
		return
	}
	ident, host, ok := s.parseVHost(c, args[0])
	if !ok {
		return
	}
	if d := s.cfg.Services.MemoSendDelayOr(); s.cfg.HostServ.MemoOpers && d > 0 && time.Since(c.lastMemoSend) < d {
		s.hs(c, "Please wait %d seconds before requesting a new vHost.", int(d.Seconds()))
		c.lastMemoSend = time.Now()
		return
	}
	if err := s.store.RequestVHost(c.user.Nick, ident, host, time.Now()); err != nil {
		s.dbErr(c, s.hostServ, err)
		return
	}
	mask := host
	if ident != "" {
		mask = ident + "@" + host
	}
	s.hs(c, "Your vHost has been requested.")
	if s.cfg.HostServ.MemoOpers {
		c.lastMemoSend = time.Now()
		if opers, err := s.store.Opers(); err == nil {
			for _, o := range opers {
				if a, _ := s.store.AccountByID(o.AccountID); a != nil {
					s.deliverMemo(a, s.hostServ.user.Nick, fmt.Sprintf("[auto memo] vHost \x02%s\x02 has been requested by %s.", mask, c.user.Nick))
				}
			}
		}
	}
	s.hsLog(c, logCommand, "to request new vhost %s", mask)
}

// --- oper commands (flag h) ---

func hsSet(all bool) func(*Services, *Client, []string) {
	return func(s *Services, c *Client, args []string) {
		if !s.needFlag(c, s.hostServ, 'h') {
			return
		}
		if len(args) < 2 {
			name := "SET"
			if all {
				name = "SETALL"
			}
			s.hs(c, "Syntax: %s <nick> [ident@]hostmask", name)
			return
		}
		nick := args[0]
		a, err := s.store.AccountByNick(nick)
		if err != nil {
			s.dbErr(c, s.hostServ, err)
			return
		}
		if a == nil {
			s.hs(c, "Nick \x02%s\x02 isn't registered.", nick)
			return
		}
		ident, host, ok := s.parseVHost(c, args[1])
		if !ok {
			return
		}
		nicks := []string{nick}
		if all {
			nicks = s.store.GroupNicks(nick)
		}
		now := time.Now()
		for _, n := range nicks {
			if err := s.store.SetVHost(n, ident, host, c.user.Nick, now); err != nil {
				s.dbErr(c, s.hostServ, err)
				return
			}
		}
		mask := host
		if ident != "" {
			mask = ident + "@" + host
		}
		s.hsLog(c, logAdmin, "to set the vhost of %s to %s", nick, mask)
		what := "VHost for"
		if all {
			what = "VHost for group"
		}
		if ident != "" {
			s.hs(c, "%s \x02%s\x02 set to \x02%s\x02@\x02%s\x02.", what, nick, ident, host)
		} else {
			s.hs(c, "%s \x02%s\x02 set to \x02%s\x02.", what, nick, host)
		}
		for _, n := range nicks {
			s.vhostSet(n)
		}
	}
}

func hsDel(all bool) func(*Services, *Client, []string) {
	return func(s *Services, c *Client, args []string) {
		if !s.needFlag(c, s.hostServ, 'h') {
			return
		}
		if len(args) == 0 {
			name := "DEL"
			if all {
				name = "DELALL"
			}
			s.hs(c, "Syntax: %s <nick>", name)
			return
		}
		nick := args[0]
		a, err := s.store.AccountByNick(nick)
		if err != nil {
			s.dbErr(c, s.hostServ, err)
			return
		}
		if a == nil {
			s.hs(c, "Nick \x02%s\x02 isn't registered.", nick)
			return
		}
		nicks := []string{nick}
		if all {
			nicks = s.store.GroupNicks(nick)
		}
		for _, n := range nicks {
			if _, err := s.store.DelVHost(n); err != nil {
				s.dbErr(c, s.hostServ, err)
				return
			}
			s.vhostGone(n)
		}
		if all {
			s.hsLog(c, logAdmin, "for all nicks in group %s", a.Name)
			s.hs(c, "vhosts for group \x02%s\x02 have been removed.", a.Name)
		} else {
			s.hsLog(c, logAdmin, "for user %s", nick)
			s.hs(c, "Vhost for \x02%s\x02 removed.", nick)
		}
	}
}

// hsTable prints rows under headers with aligned columns (Anope's
// ListFormatter).
func (s *Services) hsTable(c *Client, head []string, rows [][]string) {
	width := make([]int, len(head))
	for i, h := range head {
		width[i] = len(h)
	}
	for _, r := range rows {
		for i, v := range r {
			width[i] = max(width[i], len(v))
		}
	}
	line := func(r []string) string {
		var b strings.Builder
		for i, v := range r {
			if i > 0 {
				b.WriteString("  ")
			}
			if i < len(r)-1 {
				b.WriteString(fmt.Sprintf("%-*s", width[i], v))
			} else {
				b.WriteString(v)
			}
		}
		return "  " + b.String()
	}
	s.hs(c, "%s", line(head))
	for _, r := range rows {
		s.hs(c, "%s", line(r))
	}
}

func hsList(s *Services, c *Client, args []string) {
	if !s.needFlag(c, s.hostServ, 'h') {
		return
	}
	all := s.store.VHosts()
	key := argAt(args, 0)
	from, to := 0, 0
	if strings.HasPrefix(key, "#") {
		a, b, ok := strings.Cut(key[1:], "-")
		var e1, e2 error
		from, e1 = strconv.Atoi(a)
		to, e2 = strconv.Atoi(b)
		if !ok || e1 != nil || e2 != nil || from < 1 || to < from {
			s.hs(c, "%s", listIncorrectRange)
			return
		}
	}
	var rows [][]string
	for i, v := range all {
		n := i + 1
		switch {
		case from > 0 && (n < from || n > to):
			continue
		case from == 0 && key != "" && !irc.Match(key, v.Nick) && !irc.Match(key, v.Host):
			continue
		}
		rows = append(rows, []string{strconv.Itoa(n), v.Nick, v.Mask(), v.Creator, v.Created.UTC().Format("Jan 02 15:04:05 2006 UTC")})
	}
	if len(rows) == 0 {
		s.hs(c, "No records to display.")
		return
	}
	s.hsTable(c, []string{"Number", "Nick", "Vhost", "Creator", "Created"}, rows)
	switch {
	case from > 0:
		s.hs(c, "Displayed records from \x02%d\x02 to \x02%d\x02.", from, to)
	case key != "":
		s.hs(c, "Displayed records matching key \x02%s\x02 (count: \x02%d\x02).", key, len(rows))
	default:
		s.hs(c, "Displayed all records (count: \x02%d\x02).", len(rows))
	}
}

func hsWaiting(s *Services, c *Client, args []string) {
	if !s.needFlag(c, s.hostServ, 'h') {
		return
	}
	reqs := s.store.VHostRequests()
	var rows [][]string
	for i, r := range reqs {
		rows = append(rows, []string{strconv.Itoa(i + 1), r.Nick, r.Mask(), r.Created.UTC().Format("Jan 02 15:04:05 2006 UTC")})
	}
	if len(rows) > 0 {
		s.hsTable(c, []string{"Number", "Nick", "Vhost", "Created"}, rows)
	}
	s.hs(c, "Displayed \x02%d\x02 records (\x02%d\x02 total).", len(rows), len(reqs))
}

func hsActivate(s *Services, c *Client, args []string) {
	if !s.needFlag(c, s.hostServ, 'h') {
		return
	}
	if len(args) == 0 {
		s.hs(c, "Syntax: ACTIVATE <nick>")
		return
	}
	nick := args[0]
	r, ok := s.store.VHostRequest(nick)
	if !ok {
		s.hs(c, "No request for nick %s found.", nick)
		return
	}
	if err := s.store.SetVHost(nick, r.Ident, r.Host, c.user.Nick, time.Now()); err != nil {
		s.dbErr(c, s.hostServ, err)
		return
	}
	if _, err := s.store.DelVHostRequest(nick); err != nil {
		s.dbErr(c, s.hostServ, err)
		return
	}
	if s.cfg.HostServ.MemoUser {
		if a, _ := s.store.AccountByNick(nick); a != nil {
			s.deliverMemo(a, s.hostServ.user.Nick, "[auto memo] Your requested vHost has been approved.")
		}
	}
	s.hs(c, "vHost for %s has been activated.", r.Nick)
	s.hsLog(c, logCommand, "for %s for vhost %s", r.Nick, r.Mask())
	s.vhostSet(nick)
}

func hsReject(s *Services, c *Client, args []string) {
	if !s.needFlag(c, s.hostServ, 'h') {
		return
	}
	if len(args) == 0 {
		s.hs(c, "Syntax: REJECT <nick> [reason]")
		return
	}
	nick, reason := args[0], strings.Join(args[1:], " ")
	r, ok := s.store.VHostRequest(nick)
	if !ok {
		s.hs(c, "No request for nick %s found.", nick)
		return
	}
	if _, err := s.store.DelVHostRequest(nick); err != nil {
		s.dbErr(c, s.hostServ, err)
		return
	}
	if s.cfg.HostServ.MemoUser {
		if a, _ := s.store.AccountByNick(nick); a != nil {
			msg := "[auto memo] Your requested vHost has been rejected."
			if reason != "" {
				msg = "[auto memo] Your requested vHost has been rejected. Reason: " + reason
			}
			s.deliverMemo(a, s.hostServ.user.Nick, msg)
		}
	}
	s.hs(c, "vHost for %s has been rejected.", r.Nick)
	if reason == "" {
		reason = "no reason"
	}
	s.hsLog(c, logCommand, "to reject vhost for %s (%s)", r.Nick, reason)
}
