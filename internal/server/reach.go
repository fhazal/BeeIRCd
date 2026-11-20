package server

import (
	"fmt"
	"strings"

	"github.com/fhazal/beeircd/internal/config"
	"github.com/fhazal/beeircd/internal/state"
	"github.com/fhazal/beeircd/internal/store"
)

// Admin reach. An admin's AdminServ powers count on their own server
// (home, the default) or, when the network owner gives it, on every
// server that takes admins from elsewhere ([link] outside_admins =
// "network"). The network owner is the services server's admin from
// beeircd.conf.

// reachWords says where reach counts, for notices.
func (s *Server) reachWords(reach string) string {
	if reach == store.ReachNetwork {
		return "the whole network"
	}
	return s.cfg.Server.Name + " only"
}

// isOwner reports whether c is the network owner: on the services server,
// logged in to (or opered as) the account of [services] admin.
func (s *Server) isOwner(c *Client) bool {
	if s.svc.Off() || s.cfg.Services.Admin == "" || c == nil || c.user == nil {
		return false
	}
	for _, id := range []int64{s.accountID(c), c.operAccount} {
		if id == 0 {
			continue
		}
		if a, err := s.store.AccountByID(id); err == nil && a != nil && s.isAdminAccount(a) {
			return true
		}
	}
	return false
}

// ownsServer reports whether c may change this server's owner-only
// settings (outside-admins): the network owner on the services server;
// on a leaf, which has no network owner of its own, the admin from this
// server's beeircd.conf, opered here.
func (s *Server) ownsServer(c *Client) bool {
	if !s.svc.Off() {
		return s.isOwner(c)
	}
	admin := s.cfg.Services.Admin
	return admin != "" && c != nil && c.user != nil && c.isOper() &&
		strings.EqualFold(c.user.OperName, admin) && strings.EqualFold(c.user.Account, admin)
}

// ownerOfServer names who ownsServer means, for refusals.
func (s *Server) ownerOfServer() string {
	if !s.svc.Off() {
		return s.ownerWhere()
	}
	if admin := s.cfg.Services.Admin; admin != "" {
		return admin + " (the admin in beeircd.conf), opered on " + s.cfg.Server.Name + ","
	}
	return "the admin in beeircd.conf"
}

// reachOf is uid's admin reach as this server knows it: store.ReachNetwork
// or "" (home). Someone elsewhere has the reach their server sends; the
// network owner, opered on another server, always has network reach here
// on the services server.
func (s *Server) reachOf(uid string) string {
	if c := s.clients[uid]; c != nil {
		if c.isOper() {
			return c.operReach
		}
		return ""
	}
	u := s.net.UserByUID(uid)
	if u == nil || !u.Modes['o'] {
		return ""
	}
	if s.svc.Off() {
		// A leaf has only what the services server sent (trustedReach).
		return cleanReach(u.OperReach)
	}
	return s.grantedReach(u)
}

// trustedReach is the reach to keep for u from a bee-operlook value
// that came in on l. The services server gave every network reach there
// is, so it trusts only its own records (grantedReach); a leaf takes the
// reach only from a services server linked to it, which sends what its
// records say. From anywhere else it is home, so a modified server can't
// give its own admins network reach.
func (s *Server) trustedReach(l *linkConn, u *state.User, value string) string {
	if !s.svc.Off() {
		return s.grantedReach(u)
	}
	if l.peer.Bee && l.peer.Tokens["BEESVC"] == "1" {
		return readOperReach(value)
	}
	return ""
}

// grantedReach is u's reach by the services server's own records, never
// what another server claims: the oper entry it granted them, or the
// owner's account.
func (s *Server) grantedReach(u *state.User) string {
	uid := u.UID
	if g, ok := s.links.remoteOpers[uid]; ok {
		if o, err := s.store.OperByAccount(g.id); err == nil && o.Network() {
			return store.ReachNetwork
		}
	}
	if strings.IndexByte(u.OperFlags, 'a') >= 0 {
		if id := s.svc.AccountID(uid); id != 0 {
			if a, err := s.store.AccountByID(id); err == nil && a != nil && s.isAdminAccount(a) {
				return store.ReachNetwork
			}
		}
	}
	return ""
}

// ownerWhere names the network owner for refusals: "the network owner
// (ember on hub.example.net)", or without the nick when this server
// doesn't know it (a leaf).
func (s *Server) ownerWhere() string {
	if !s.svc.Off() {
		if admin := s.cfg.Services.Admin; admin != "" {
			return "the network owner (" + admin + " on " + s.cfg.Server.Name + ")"
		}
		return "the network owner on " + s.cfg.Server.Name
	}
	if t := s.saslTarget(); t != "" {
		if r := s.serverByName(t); r != nil {
			if up := s.links.servers[r.uplink]; up != nil {
				t = up.name // the services pseudo-server hangs off the hub
			}
		}
		return "the network owner on " + t
	}
	return "the network owner on the services server"
}

// outsideRefusal says why c, an admin on another server, may not run
// AdminServ commands here; "" when they may: this server takes outside
// admins and c's reach is network.
func (s *Server) outsideRefusal(c *Client) string {
	if s.cfg.Link.OutsideAdmins == config.OutsideNone {
		return fmt.Sprintf("%s doesn't take admin commands from other servers (outside_admins = none). Ask whoever runs it.", s.cfg.Server.Name)
	}
	if c.operReach == store.ReachNetwork {
		return ""
	}
	u := c.user
	return fmt.Sprintf("Your admin reach is %s only. Whole-network reach is given by %s: /as oper %s set reach network",
		u.Server, s.ownerWhere(), orStr(u.Account, u.Nick))
}

// outsideNote logs an AdminServ command an admin on another server runs
// here and tells the opers with +a.
func (s *Server) outsideNote(c *Client, line string) {
	u := c.user
	s.sno('a', "Outside AdminServ from %s (%s on %s): %s", u.Nick, orStr(u.Account, "no account"), u.Server, line)
}

// lockedForOutside is the lock-out guard: what an admin on another
// server may never change here, so whoever runs this server can't be
// locked out of it. It tells c and returns true when c is from outside.
func (s *Server) lockedForOutside(c *Client, what string) bool {
	if s.ours(c.uid) {
		return false
	}
	s.as(c, "%s on %s is changed only by people on it, so nobody from another server can lock its admin out.", what, s.cfg.Server.Name)
	return true
}
