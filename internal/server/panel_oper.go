package server

// The Hive Panel's oper pages that change services: People's Accounts, the
// Services page's Vhosts, Bots, News, Sessions and DEFCON tabs and
// Overview's notice to everyone. Every change is the real NickServ,
// HostServ, BotServ or OperServ command, run for the oper with their oper
// flags (services.WebOper), so the checks, notices to opers and log lines
// are the same as on IRC.

import (
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/fhazal/beeircd/internal/control"
	"github.com/fhazal/beeircd/internal/services"
)

const (
	accountsPage    = 50
	accountsMaxPage = 200
	busiestShown    = 10
)

// servAct is one action of CmdServDo: the oper flag it needs, the service
// and how its command line is made from the request (problem when the
// request can't be one).
type servAct struct {
	flag    byte
	service string
	args    func(s *Server, r control.Request) (args []string, problem string)
	event   func(r control.Request) string // for the panel's event log
}

// word is one argument that must be one word.
func word(v, what string) (string, string) {
	v = strings.TrimSpace(v)
	if v == "" {
		return "", "Give " + what + "."
	}
	if strings.ContainsAny(v, " \t\r\n\x00") {
		return "", "That " + strings.TrimPrefix(strings.TrimPrefix(what, "a "), "an ") + " can't have spaces."
	}
	return v, ""
}

// text is free text made one line ("" allowed).
func text(v string) string { return strings.Join(strings.Fields(v), " ") }

// expiry is a panel length ("7d", "0" or "" = none) as services' +expiry.
func expiry(life string) (string, string) {
	d, ok := panelLife(life)
	if !ok {
		return "", "That length isn't one I know. Use 1h, 1d, 7d, 30d or 0 for until removed."
	}
	if d <= 0 {
		return "", ""
	}
	return "+" + shortLength(d), ""
}

// shortLength is d as 7d, 12h or 30m.
func shortLength(d time.Duration) string {
	switch {
	case d%(24*time.Hour) == 0:
		return strconv.FormatInt(int64(d/(24*time.Hour)), 10) + "d"
	case d%time.Hour == 0:
		return strconv.FormatInt(int64(d/time.Hour), 10) + "h"
	}
	return strconv.FormatInt(int64((d+time.Minute-1)/time.Minute), 10) + "m"
}

func oneNick(cmd ...string) func(*Server, control.Request) ([]string, string) {
	return func(_ *Server, r control.Request) ([]string, string) {
		n, p := word(r.Name, "a nick")
		return append(cmd, n), p
	}
}

var servActs = map[string]servAct{
	"suspend": {'s', "NickServ", func(_ *Server, r control.Request) ([]string, string) {
		n, p := word(r.Name, "a nick")
		if p != "" {
			return nil, p
		}
		args := []string{"SUSPEND", n}
		e, p := expiry(r.Life)
		if p != "" {
			return nil, p
		}
		if e != "" {
			args = append(args, e)
		}
		if why := text(r.Reason); why != "" {
			args = append(args, why)
		}
		return args, ""
	}, func(r control.Request) string { return "suspended the account of " + r.Name }},
	"unsuspend": {'s', "NickServ", oneNick("UNSUSPEND"), func(r control.Request) string { return "unsuspended the account of " + r.Name }},
	"drop":      {'s', "NickServ", oneNick("DROP"), func(r control.Request) string { return "dropped the nick " + r.Name }},
	"password": {'s', "NickServ", func(_ *Server, r control.Request) ([]string, string) {
		n, p := word(r.Name, "a nick")
		if p != "" {
			return nil, p
		}
		pw, p := word(r.Password, "a new password")
		return []string{"SASET", "PASSWORD", n, pw}, p
	}, func(r control.Request) string { return "set a new password for " + r.Name }},
	"email": {'s', "NickServ", func(_ *Server, r control.Request) ([]string, string) {
		n, p := word(r.Name, "a nick")
		if p != "" {
			return nil, p
		}
		mail := strings.TrimSpace(r.Value)
		if mail == "" {
			mail = "OFF"
		}
		mail, p = word(mail, "an email address")
		return []string{"SASET", "EMAIL", n, mail}, p
	}, func(r control.Request) string { return "changed the email of " + r.Name }},

	"vhost-ok": {'h', "HostServ", oneNick("ACTIVATE"), func(r control.Request) string { return "approved the vhost of " + r.Name }},
	"vhost-no": {'h', "HostServ", func(_ *Server, r control.Request) ([]string, string) {
		n, p := word(r.Name, "a nick")
		args := []string{"REJECT", n}
		if why := text(r.Reason); why != "" {
			args = append(args, why)
		}
		return args, p
	}, func(r control.Request) string { return "turned down the vhost of " + r.Name }},
	"vhost-set": {'h', "HostServ", func(_ *Server, r control.Request) ([]string, string) {
		n, p := word(r.Name, "a nick")
		if p != "" {
			return nil, p
		}
		v, p := word(r.Value, "a vhost")
		return []string{"SET", n, v}, p
	}, func(r control.Request) string { return "set the vhost of " + r.Name }},
	"vhost-del": {'h', "HostServ", oneNick("DEL"), func(r control.Request) string { return "removed the vhost of " + r.Name }},

	// Bots: Name is the bot's nick (now), Value the new nick (change),
	// Mask user@host and Reason the real name.
	"bot-add": {'b', "BotServ", func(_ *Server, r control.Request) ([]string, string) {
		n, p := word(r.Name, "a nick")
		if p != "" {
			return nil, p
		}
		user, host, p := userHost(r.Mask)
		if p != "" {
			return nil, p
		}
		real := text(r.Reason)
		if real == "" {
			real = n
		}
		return []string{"BOT", "ADD", n, user, host, real}, ""
	}, func(r control.Request) string { return "added the bot " + r.Name }},
	"bot-change": {'b', "BotServ", func(_ *Server, r control.Request) ([]string, string) {
		old, p := word(r.Name, "the bot's nick")
		if p != "" {
			return nil, p
		}
		n, p := word(r.Value, "a nick")
		if p != "" {
			return nil, p
		}
		user, host, p := userHost(r.Mask)
		if p != "" {
			return nil, p
		}
		args := []string{"BOT", "CHANGE", old, n, user, host}
		if real := text(r.Reason); real != "" {
			args = append(args, real)
		}
		return args, ""
	}, func(r control.Request) string { return "changed the bot " + r.Name }},
	"bot-del": {'b', "BotServ", oneNick("BOT", "DEL"), func(r control.Request) string { return "deleted the bot " + r.Name }},

	// News: Name is the kind (logon, oper, random).
	"news-add": {'g', "OperServ", func(_ *Server, r control.Request) ([]string, string) {
		cmd := services.NewsCmd(r.Name)
		if cmd == "" {
			return nil, "That isn't a kind of news."
		}
		t := text(r.Value)
		if t == "" {
			return nil, "Type the news first."
		}
		return []string{cmd, "ADD", t}, ""
	}, func(r control.Request) string { return "added " + r.Name + " news" }},
	"news-del": {'g', "OperServ", func(s *Server, r control.Request) ([]string, string) {
		cmd := services.NewsCmd(r.Name)
		if cmd == "" {
			return nil, "That isn't a kind of news."
		}
		n := s.svc.NewsNumber(r.Name, r.ID)
		if n == 0 {
			return nil, "That news is gone already."
		}
		return []string{cmd, "DEL", strconv.Itoa(n)}, ""
	}, func(r control.Request) string { return "deleted " + r.Name + " news" }},

	// Session exceptions: Mask, Value (the limit), Reason, Life.
	"except-add": {'k', "OperServ", func(_ *Server, r control.Request) ([]string, string) {
		m, p := word(r.Mask, "an address mask")
		if p != "" {
			return nil, p
		}
		n, err := strconv.Atoi(strings.TrimSpace(r.Value))
		if err != nil || n < 0 || n > 10000 {
			return nil, "The limit is a number from 0 to 10000 (0 = no limit)."
		}
		args := []string{"EXCEPTION", "ADD"}
		e, p := expiry(r.Life)
		if p != "" {
			return nil, p
		}
		if e != "" {
			args = append(args, e)
		}
		args = append(args, m, strconv.Itoa(n))
		if why := text(r.Reason); why != "" {
			args = append(args, why)
		}
		return args, ""
	}, func(r control.Request) string { return "added a session exception" }},
	"except-del": {'k', "OperServ", func(_ *Server, r control.Request) ([]string, string) {
		m, p := word(r.Mask, "an address mask")
		return []string{"EXCEPTION", "DEL", m}, p
	}, func(r control.Request) string { return "removed a session exception" }},

	// Ignores: Mask, Reason, Life ("0" or "" = until removed).
	"ignore-add": {'k', "OperServ", func(_ *Server, r control.Request) ([]string, string) {
		m, p := word(r.Mask, "a nick or mask")
		if p != "" {
			return nil, p
		}
		d, ok := panelLife(r.Life)
		if !ok {
			return nil, "That length isn't one I know. Use 1h, 1d, 7d, 30d or 0 for until removed."
		}
		t := "0"
		if d > 0 {
			t = shortLength(d)
		}
		args := []string{"IGNORE", "ADD", t, m}
		if why := text(r.Reason); why != "" {
			args = append(args, why)
		}
		return args, ""
	}, func(r control.Request) string { return "added an ignore" }},
	"ignore-del": {'k', "OperServ", func(_ *Server, r control.Request) ([]string, string) {
		m, p := word(r.Mask, "a nick or mask")
		return []string{"IGNORE", "DEL", m}, p
	}, func(r control.Request) string { return "removed an ignore" }},

	"defcon": {'a', "OperServ", func(_ *Server, r control.Request) ([]string, string) {
		n, err := strconv.Atoi(strings.TrimSpace(r.Value))
		if err != nil || n < 1 || n > 5 {
			return nil, "DEFCON is a level from 1 to 5."
		}
		return []string{"DEFCON", strconv.Itoa(n)}, ""
	}, func(r control.Request) string { return "set DEFCON " + strings.TrimSpace(r.Value) }},

	"global": {'g', "OperServ", func(_ *Server, r control.Request) ([]string, string) {
		t := text(r.Value)
		if t == "" {
			return nil, "Type a message first."
		}
		if utf8.RuneCountInString(t) > noticeMax {
			return nil, fmt.Sprintf("That is too long (%d characters at most).", noticeMax)
		}
		return []string{"GLOBAL", t}, ""
	}, func(control.Request) string { return "sent a notice to everyone" }},
}

// userHost splits user@host for a bot.
func userHost(v string) (user, host, problem string) {
	v = strings.TrimSpace(v)
	user, host, ok := strings.Cut(v, "@")
	if !ok || user == "" || host == "" || strings.ContainsAny(v, " \t\r\n\x00") {
		return "", "", "Give the bot's user@host, like bot@services.example."
	}
	return user, host, ""
}

// panelServDo runs one services change for po; reply gets the answer, on
// the loop, at once or (a password being hashed) a moment later.
func (s *Server) panelServDo(po *control.PanelOper, req control.Request, act servAct, reply func(control.Response)) {
	a, bad := s.panelAccount(po)
	if bad != nil {
		reply(*bad)
		return
	}
	args, problem := act.args(s, req)
	if problem != "" {
		reply(control.Response{Error: problem})
		return
	}
	s.svc.WebOper(a.ID, req.IP, po.Flags, act.service, args, func(ok bool, answer string) {
		if !ok {
			if answer == "" {
				answer = "Nothing changed."
			}
			reply(control.Response{Error: answer})
			return
		}
		s.panelEvent(po.Account, req.IP, act.event(req))
		if answer == "" {
			answer = "Done."
		}
		reply(control.Response{Done: &control.Done{Text: answer}})
	})
}

// panelAccounts is People's Accounts list.
func (s *Server) panelAccounts(req control.Request) *control.Accounts {
	q := req.Query
	if len(q) > 100 {
		q = q[:100]
	}
	rows := s.svc.WebAccounts(q)
	out := &control.Accounts{Rows: []control.AccountRow{}}
	var match []services.AccountRow
	for _, r := range rows {
		out.Total++
		if r.Online > 0 {
			out.Online++
		}
		if r.Suspended {
			out.Suspended++
		}
		if r.Unconfirmed {
			out.Unconfirmed++
		}
		switch req.Filter {
		case "online":
			if r.Online == 0 {
				continue
			}
		case "suspended":
			if !r.Suspended {
				continue
			}
		case "unconfirmed":
			if !r.Unconfirmed {
				continue
			}
		}
		match = append(match, r)
	}
	out.Matched = len(match)
	limit := req.Limit
	if limit <= 0 {
		limit = accountsPage
	}
	limit = min(limit, accountsMaxPage)
	off := max(0, min(req.Offset, len(match)))
	out.Offset = off
	for _, r := range match[off:min(off+limit, len(match))] {
		out.Rows = append(out.Rows, accountRow(r))
	}
	return out
}

func accountRow(r services.AccountRow) control.AccountRow {
	return control.AccountRow{Name: r.Name, Email: r.Email, Registered: r.Registered, LastSeen: r.LastSeen, Nicks: r.Nicks,
		Online: r.Online, Suspended: r.Suspended, Oper: r.Oper, Unconfirmed: r.Unconfirmed}
}

// panelAccountView is one account opened from the list.
func (s *Server) panelAccountView(name string) control.Response {
	v := s.svc.WebAccount(strings.TrimSpace(name))
	if v == nil {
		return control.Response{Error: "No account has the nick " + name + "."}
	}
	out := &control.Account{AccountRow: accountRow(v.AccountRow), NickList: v.NickList, LastHost: v.LastHost, VHost: v.VHost,
		Founder: v.Founder, NoExpire: v.NoExpire}
	if out.Founder == nil {
		out.Founder = []string{}
	}
	if v.Suspended {
		out.SuspendedBy, out.SuspendReason, out.SuspendedAt = v.SuspendedBy, v.SuspendReason, v.SuspendedAt
		if v.SuspendUntil.Unix() > 0 {
			out.SuspendUntil = v.SuspendUntil
		}
	}
	switch {
	case v.Admin:
		out.Locked = "This is the server owner's account. Only they can change it."
	case v.Oper:
		out.Locked = "This is an oper's account. Services don't suspend, drop or change opers' accounts."
	}
	return control.Response{Account: out}
}

// svcFlags are the flags each Services tab needs (any one of them).
var svcFlags = map[string]string{"vhosts": "h", "bots": "b", "news": "g", "sessions": "k", "defcon": "ak"}

// panelSvc is one tab of the Services page.
func (s *Server) panelSvc(po *control.PanelOper, kind string) control.Response {
	has := func(f byte) bool { return strings.IndexByte(po.Flags, f) >= 0 }
	out := &control.Svc{Kind: kind}
	switch kind {
	case "vhosts":
		waiting, active := s.svc.WebVHosts()
		for _, v := range waiting {
			out.Waiting = append(out.Waiting, control.VHostRow{Nick: v.Nick, VHost: v.Mask(), Creator: v.Creator, Created: v.Created})
		}
		for _, v := range active {
			out.Active = append(out.Active, control.VHostRow{Nick: v.Nick, VHost: v.Mask(), Creator: v.Creator, Created: v.Created})
		}
	case "bots":
		for _, b := range s.svc.WebBots() {
			out.Bots = append(out.Bots, control.BotRow{Nick: b.Nick, User: b.Ident, Host: b.Host, Real: b.Realname, Created: b.Created,
				Private: b.Private, Channels: b.Channels})
		}
	case "news":
		for _, k := range []string{"logon", "oper", "random"} {
			l := control.NewsList{Kind: k, About: services.NewsAbout(k), Max: services.MaxNews, Items: []control.NewsItem{}}
			for _, n := range s.svc.WebNews(k) {
				l.Items = append(l.Items, control.NewsItem{ID: n.ID, Text: n.Text, SetBy: n.SetBy, SetAt: n.SetAt})
			}
			out.News = append(out.News, l)
		}
	case "sessions":
		limit, busiest, excepts := s.svc.WebSessions(busiestShown)
		out.SessionLimit = limit
		for _, b := range busiest {
			out.Busiest = append(out.Busiest, control.SessionRow{IP: b.IP, Count: b.Count, Limit: b.Limit})
		}
		for _, e := range excepts {
			out.Excepts = append(out.Excepts, control.SessionExcept{Mask: e.Mask, Limit: e.Max, Reason: e.Reason, SetBy: e.SetBy, SetAt: e.SetAt, Expires: e.Expires})
		}
	case "defcon":
		out.Defcon = s.svc.DefconLevel()
		for n := 1; n <= 5; n++ {
			out.Levels = append(out.Levels, services.DefconWords(n))
		}
		out.CanDefcon, out.CanIgnore = has('a'), has('k')
		if out.CanIgnore {
			for _, g := range s.svc.WebIgnores() {
				r := control.IgnoreRow{Mask: g.Mask, Reason: g.Reason, SetBy: g.SetBy, SetAt: g.SetAt}
				if g.Expires.Unix() > 0 {
					r.Expires = g.Expires
				}
				out.Ignores = append(out.Ignores, r)
			}
		}
	default:
		return control.Response{Error: "That isn't a Services tab."}
	}
	return control.Response{Svc: out}
}
