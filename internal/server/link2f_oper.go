package server

// Step 2f: /oper on a server whose services are off (a leaf). Oper
// entries live with the accounts on the services server, so the leaf
// asks it, over the TLS link, in BeeIRCd-only lines:
//
//	:<uid> BEEOPER <services sid> <name> :<password>
//	:<sid> BEEOPERR <uid> OK :<json: account class flags title vhost swhois>
//	:<sid> BEEOPERR <uid> FAIL <numeric> :<text>
//
// The services server checks the entry, the user's host and the password
// (with its lockout), as for /oper there. The leaf only takes an answer
// for a request it sent, from the link it sent it on.

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/fhazal/beeircd/internal/irc"
	"github.com/fhazal/beeircd/internal/services"
	"github.com/fhazal/beeircd/internal/store"
)

// operRelayWait is how long a relayed /oper may take before another try
// is allowed.
const operRelayWait = 15 * time.Second

// debugLine hides the password of a BEEOPER line in the link debug log.
func debugLine(line string) string {
	if f := strings.Fields(line); len(f) > 1 && f[1] == "BEEOPER" {
		if i := strings.Index(line, " :"); i >= 0 {
			return line[:i] + " :<password>"
		}
	}
	return line
}

// operRelay is a relayed /oper waiting for its answer.
type operRelay struct {
	via  *linkConn
	name string
	at   time.Time
}

// operGrant is what the services server sends back on success.
type operGrant struct {
	Account string   `json:"account"`
	Class   string   `json:"class,omitempty"`
	Flags   string   `json:"flags"`
	Title   string   `json:"title,omitempty"`
	Vhost   string   `json:"vhost,omitempty"`
	Swhois  []string `json:"swhois,omitempty"`
}

// operRelayTo sends c's /oper to the services server.
func (s *Server) operRelayTo(c *Client, name, pw string) {
	u := c.user
	target := s.saslTarget()
	var r *remoteServer
	if target != "" {
		r = s.serverByName(target)
	}
	if r == nil || r.via == nil || !r.via.peer.Bee {
		s.reply(c, irc.ErrNoOperHost, "No oper entry for you from this host (the services server isn't linked right now)")
		s.snotice("Failed /oper as %s from %s (%s@%s): services server not linked", name, u.Nick, u.Ident, u.RealHost)
		return
	}
	if c.operRelay != nil && time.Since(c.operRelay.at) < operRelayWait {
		s.notice(c, "*** Still checking your last /oper. Try again in a moment.")
		return
	}
	c.operRelay = &operRelay{via: r.via, name: name, at: time.Now()}
	r.via.send(":" + c.uid + " BEEOPER " + r.sid + " " + name + " :" + pw)
}

// remoteOper is BEEOPER on the services server (or on the way to it).
func (s *Server) remoteOper(l *linkConn, m irc.Message) {
	if len(m.Params) < 3 || !l.peer.Bee {
		return
	}
	target, name, pw := m.Params[0], m.Params[1], m.Params[2]
	_, svcSID, _ := s.cfg.ServicesServer()
	if target != s.cfg.Server.SID && target != svcSID {
		if r := s.links.servers[target]; r != nil && r.via != l && r.via != nil && r.via.peer.Bee {
			r.via.send(m.String())
		}
		return
	}
	uid := m.Source
	u := s.net.UserByUID(uid)
	if u == nil || s.ours(uid) || !s.behind(l, uidServer(uid)) || s.svc.Off() {
		return
	}
	fail := func(numeric, text, why string) {
		s.operAnswer(uid, "FAIL "+numeric+" :"+text)
		s.snotice("Failed /oper as %s from %s (%s@%s on %s): %s", name, u.Nick, u.TrueIdent(), u.RealHost, u.Server, why)
	}
	a, err := s.store.AccountByNick(name)
	if err != nil {
		s.log.Printf("services database: %v", err)
		return
	}
	var o *store.Oper
	if a != nil {
		if o, err = s.operEntry(a); err != nil {
			s.log.Printf("services database: %v", err)
			return
		}
	}
	switch {
	case o == nil || !operHostOK(o, u):
		fail(irc.ErrNoOperHost, "No oper entry for you from this host", "no matching oper entry")
		return
	case o.Disabled != "":
		fail(irc.ErrNoOperHost, "Your oper entry is disabled: "+o.Disabled, "entry disabled")
		return
	case o.PassHash == "":
		fail(irc.ErrNoOperHost, "No oper password is set for "+o.Account+" yet (/oper once on "+s.cfg.Server.Name+")", "no oper password yet")
		return
	}
	if s.links.operBusy == nil {
		s.links.operBusy = map[string]*bool{}
	}
	busy := s.links.operBusy[uid]
	if busy == nil {
		busy = new(bool)
		s.links.operBusy[uid] = busy
	}
	gone := func() bool { return s.net.UserByUID(uid) == nil }
	s.svc.CheckSecret(busy, gone, operLockKey(a.ID), o.PassHash, pw, func(res services.PwResult) {
		if !*busy {
			delete(s.links.operBusy, uid)
		}
		switch res {
		case services.PwOK:
			v := s.viewOf(o)
			b, _ := json.Marshal(operGrant{Account: o.Account, Class: v.class, Flags: v.flags, Title: v.title, Vhost: v.vhost, Swhois: v.swhois})
			s.operAnswer(uid, "OK :"+string(b))
			s.snotice("%s (%s@%s) is now an operator on %s as %s (entry checked here): %s", u.Nick, u.TrueIdent(), u.RealHost, u.Server, o.Account, v.flags)
		case services.PwLocked:
			fail(irc.ErrPasswdMismatch, "Too many wrong passwords. Try again in "+services.RoundUp(s.lockedFor(operLockKey(a.ID))), "locked out")
		case services.PwBusy:
			s.operAnswer(uid, "FAIL NOTICE :Still checking your last password. Try again in a moment.")
		default:
			fail(irc.ErrPasswdMismatch, "Password incorrect", "wrong password")
		}
	})
}

// operAnswer sends a BEEOPERR line towards uid's server.
func (s *Server) operAnswer(uid, rest string) {
	if r := s.links.servers[uidServer(uid)]; r != nil && r.via != nil {
		r.via.send(":" + s.cfg.Server.SID + " BEEOPERR " + uid + " " + rest)
	}
}

// operAnswered is BEEOPERR: the services server's answer to a relayed
// /oper (or one passing through to another server).
func (s *Server) operAnswered(l *linkConn, m irc.Message) {
	if len(m.Params) < 3 || !l.peer.Bee {
		return
	}
	uid := m.Params[0]
	if !s.ours(uid) {
		if r := s.links.servers[uidServer(uid)]; r != nil && r.via != l && r.via != nil && r.via.peer.Bee {
			r.via.send(m.String())
		}
		return
	}
	c := s.clients[uid]
	if c == nil || c.user == nil || c.operRelay == nil || c.operRelay.via != l {
		return
	}
	name := c.operRelay.name
	c.operRelay = nil
	u := c.user
	text := m.Params[len(m.Params)-1]
	switch {
	case m.Params[1] == "FAIL" && len(m.Params) >= 4 && m.Params[2] == "NOTICE":
		s.notice(c, "*** "+text)
	case m.Params[1] == "FAIL" && len(m.Params) >= 4:
		numeric := m.Params[2]
		if numeric != irc.ErrPasswdMismatch {
			numeric = irc.ErrNoOperHost
		}
		s.reply(c, numeric, text)
		s.snotice("Failed /oper as %s from %s (%s@%s): %s (checked on the services server)", name, u.Nick, u.Ident, u.RealHost, text)
	case m.Params[1] == "OK":
		var g operGrant
		if json.Unmarshal([]byte(text), &g) != nil || g.Flags == "" {
			return
		}
		if s.links.noop {
			s.reply(c, irc.ErrNoOperHost, "No IRC operators on this server for now (NOOP)")
			return
		}
		flags, _ := cleanFlags(g.Flags)
		o := &store.Oper{Account: g.Account}
		s.operUpView(c, o, operView{class: g.Class, flags: flags, title: g.Title, vhost: g.Vhost, swhois: g.Swhois})
	}
}

// operRelayLost forgets relayed /opers that went out on l (the link went).
func (s *Server) operRelayLost(l *linkConn) {
	for _, c := range s.clients {
		if c.operRelay != nil && c.operRelay.via == l {
			c.operRelay = nil
			if c.user != nil {
				s.notice(c, "*** Your /oper went unanswered: the link to the services server dropped. Try again when it is back.")
			}
		}
	}
}
