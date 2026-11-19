package server

import (
	"strings"
	"time"

	"github.com/fhazal/beeircd/internal/config"
	"github.com/fhazal/beeircd/internal/irc"
	"github.com/fhazal/beeircd/internal/state"
	"github.com/fhazal/beeircd/internal/store"
)

// Server notice letters (snomasks), with InspIRCd 4's meanings. An oper
// with user mode +s gets the server notices whose letter is in their mask.
//
//	a announcements and everything else    r rehash
//	c/C client connects here / elsewhere   q/Q quits here / elsewhere
//	k/K kills here / for other servers     o/O opers here / elsewhere
//	x/X bans set here / from other servers f spam filter
//	d DNSBL                                l/L server links / further away
//	n/N nick changes here / elsewhere      t STATS requests
//	g/G oper chat (GLOBOPS) here / elsewhere
//	v oper override
const (
	// snomaskLetters are all the letters a mask may hold.
	snomaskLetters = "acdfgklnoqrtvxCGKLNOQX"
	// snomaskDefault is what +s without letters gives: everything opers
	// got before snomasks, so n, N, t, C, Q and v are off.
	snomaskDefault = "acdfgkloqrxGKLOX"
)

// snoCategory is the word that starts a server notice with letter, like
// InspIRCd 4 ("*** CONNECT: Client connecting ..."): a capital letter is the
// same news from another server, so only the letters InspIRCd words that way
// get a REMOTE prefix.
func snoCategory(letter byte) string {
	switch letter {
	case 'c':
		return "CONNECT"
	case 'C':
		return "REMOTECONNECT"
	case 'q':
		return "QUIT"
	case 'Q':
		return "REMOTEQUIT"
	case 'o', 'O':
		return "OPER"
	case 'x', 'X':
		return "XLINE"
	case 'k', 'K':
		return "KILL"
	case 'r':
		return "REHASH"
	case 't':
		return "STATS"
	case 'l':
		return "LINK"
	case 'L':
		return "REMOTELINK"
	case 'f':
		return "FILTER"
	case 'd':
		return "DNSBL"
	case 'n':
		return "NICK"
	case 'N':
		return "REMOTENICK"
	case 'g', 'G':
		return "GLOBOPS"
	case 'v':
		return "OVERRIDE"
	}
	return "ANNOUNCEMENT"
}

// applySnomask changes the mask cur by change: "+cq-k" adds c and q and
// takes k away, "cqk" (no sign) adds, "+*" adds every letter and "-*" takes
// them all away. Unknown letters are left out. The
// result lists the letters lowercase first, then uppercase.
func applySnomask(cur, change string) string {
	var set [128]bool
	for i := 0; i < len(cur); i++ {
		set[cur[i]&0x7f] = true
	}
	add := true
	for i := 0; i < len(change); i++ {
		switch l := change[i]; {
		case l == '+':
			add = true
		case l == '-':
			add = false
		case l == '*':
			for j := 0; j < len(snomaskLetters); j++ {
				set[snomaskLetters[j]] = add
			}
		case strings.IndexByte(snomaskLetters, l) >= 0:
			set[l] = add
		}
	}
	var b strings.Builder
	for _, from := range []byte{'a', 'A'} {
		for l := from; l < from+26; l++ {
			if set[l] && strings.IndexByte(snomaskLetters, l) >= 0 {
				b.WriteByte(l)
			}
		}
	}
	return b.String()
}

// snoMask is c's server notice mask: its own letters, or the default when
// it has +s without any set (a +s that came another way than MODE or
// /OPER).
func (c *Client) snoMask() string {
	if c.snomask == "" {
		return snomaskDefault
	}
	return c.snomask
}

// wantsSno reports whether c gets server notices with letter.
func (c *Client) wantsSno(letter byte) bool {
	return c.user != nil && c.isOper() && c.user.Modes['s'] && strings.IndexByte(c.snoMask(), letter) >= 0
}

// replySnomask sends c its mask as 008, like InspIRCd.
func (s *Server) replySnomask(c *Client) {
	s.reply(c, irc.RplSnomask, "+"+c.snoMask(), "Server notice mask")
}

// snoSubsystem is the admin API log subsystem for notices with letter
// ("" lets rpcLog tell by the text).
func snoSubsystem(letter byte) string {
	switch letter {
	case 'l', 'L':
		return "link"
	case 'c':
		return "connect"
	case 'q':
		return "quit"
	}
	return ""
}

// setUserModeLine is setUserModes for a mode line from beeircd.conf or an
// API: "+s +cq" is the modes and then their arguments.
func (s *Server) setUserModeLine(c *Client, srcUID, line string) bool {
	w := strings.Fields(line)
	if len(w) == 0 {
		return s.setUserModes(c, srcUID, "")
	}
	return s.setUserModes(c, srcUID, w[0], w[1:]...)
}

// snoWho is how a server notice names u, like InspIRCd 4: its real mask
// and IP, "nick (ident@host) [ip]".
func snoWho(u *state.User) string {
	return u.Nick + " (" + u.TrueIdent() + "@" + u.RealHost + ") [" + u.IP + "]"
}

// snoRealMask is u's real mask, "nick!ident@host", the way InspIRCd 4's
// connect, quit and kill notices name a user (GetRealMask).
func snoRealMask(u *state.User) string {
	return u.Nick + "!" + u.TrueIdent() + "@" + u.RealHost
}

// snoMaskIP is the real mask and IP, "nick!ident@host (ip)", as InspIRCd 4's
// connect and quit notices give them.
func snoMaskIP(u *state.User) string {
	return snoRealMask(u) + " (" + u.IP + ")"
}

// snoDate is a time the way InspIRCd 4's notices give it ("Thu 19 Nov 2026
// 11:00:00"), in UTC.
func snoDate(t time.Time) string {
	return t.UTC().Format("Mon 2 Jan 2006 15:04:05")
}

// connClass is the class a connect notice names: BeeIRCd has no connect
// classes, so it is the user's first security group (known-users or
// unknown-users).
func connClass(u *state.User) string {
	if len(u.Groups) > 0 {
		return u.Groups[0]
	}
	return "unknown-users"
}

// snoOperUp is InspIRCd 4's oper-up notice after who (snoWho): "is now a
// server operator of type <b>Network Administrator</b> (using account
// <b>name</b>)." (core_oper.cpp), without the account part when there is
// none (a remote oper, opertype.cpp without ~name).
func snoOperUp(who, rank, account string) string {
	if account == "" {
		return who + " is now a server operator of type \x02" + rank + "\x02."
	}
	return who + " is now a server operator of type \x02" + rank + "\x02 (using account \x02" + account + "\x02)."
}

// operFail is the server notice for a failed /OPER as account by u,
// worded like InspIRCd 4: why says what went wrong ("they specified the
// wrong password").
func (s *Server) operFail(letter byte, u *state.User, account, why string) {
	s.sno(letter, "%s failed to log into the \x02%s\x02 oper account because %s.", snoWho(u), account, why)
}

// operFailWhy is the reason for operFail from the answer the services
// server gave a /OPER relayed to it. wrongPw is the [messages]
// reject_password text, matched as well as the built-in one.
func operFailWhy(answer, wrongPw string) string {
	switch {
	case answer == config.DefaultRejectPassword, answer == wrongPw:
		return "they specified the wrong password"
	case strings.HasPrefix(answer, "Too many wrong passwords"):
		return "they tried too many wrong passwords"
	case strings.HasPrefix(answer, "The login server can't be reached"):
		return "the login server can't be reached"
	case strings.HasPrefix(answer, "Your oper entry is disabled"):
		return "their oper account is disabled"
	case strings.HasPrefix(answer, "No oper password is set"):
		return "no oper password is set yet"
	case strings.HasPrefix(answer, "No oper entry for you from this host"):
		return "they are connecting from the wrong user@host"
	}
	return "the services server said: " + answer
}

// serverSynced reports whether the server with SID sid has finished its
// burst: notices about its users coming and going (C, Q, N, O) are left
// out while a link is bursting, like InspIRCd's quiet bursts.
func (s *Server) serverSynced(sid string) bool {
	r := s.links.servers[sid]
	return r != nil && r.synced
}

// snoBanAdded is InspIRCd's notice for a new ban: "nick added a timed
// G-line on mask, expires in 1 hour (on <date>): reason", or "a permanent
// G-line" when expires is zero. The time it names is the ban's own length,
// expires less setAt, like InspIRCd's ADDLINE duration.
func (s *Server) snoBanAdded(letter byte, by, kind, mask, reason string, setAt, expires time.Time) {
	if !expires.IsZero() {
		life := expires.Sub(setAt).Round(time.Second)
		if life < time.Second {
			life = time.Second
		}
		s.sno(letter, "%s added a timed %s on %s, expires in %s (on %s): %s",
			by, store.BanWord(kind), mask, longDuration(life), snoDate(expires), reason)
		return
	}
	s.sno(letter, "%s added a permanent %s on %s: %s", by, store.BanWord(kind), mask, reason)
}

// snoBanRemoved is InspIRCd's notice for a removed ban: "nick removed
// G-line on mask: reason" (ending at the mask when there is no reason).
func (s *Server) snoBanRemoved(letter byte, by, kind, mask, reason string) {
	if reason == "" {
		s.sno(letter, "%s removed %s on %s", by, store.BanWord(kind), mask)
		return
	}
	s.sno(letter, "%s removed %s on %s: %s", by, store.BanWord(kind), mask, reason)
}

// snoNick is InspIRCd's nick change notice, "User old changed their
// nickname to new": n for people here, N for people elsewhere (not while
// their server is still bursting). Services bots are left out.
func (s *Server) snoNick(u *state.User, old string) {
	if u.Service || old == u.Nick {
		return
	}
	sid := uidServer(u.UID)
	switch {
	case sid == s.cfg.Server.SID:
		s.snoOnly('n', "User "+old+" changed their nickname to "+u.Nick)
	case s.serverSynced(sid):
		s.snoOnly('N', "User "+old+" changed their nickname to "+u.Nick)
	}
}

// snoRemoteQuit is InspIRCd's notice for someone elsewhere leaving,
// "Client exiting on server X: nick!ident@host (ip) [reason]". Netsplits
// don't come here; neither do services bots or servers still bursting.
func (s *Server) snoRemoteQuit(u *state.User, reason string) {
	if u.Service || s.ours(u.UID) || !s.serverSynced(uidServer(u.UID)) {
		return
	}
	s.snoOnly('Q', "Client exiting on server "+u.Server+": "+snoMaskIP(u)+" ["+reason+"]")
}
