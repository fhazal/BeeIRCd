package server

import (
	"strings"

	"github.com/fhazal/beeircd/internal/irc"
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
//	v oper override
const (
	// snomaskLetters are all the letters a mask may hold.
	snomaskLetters = "acdfklnoqrtvxCKLNOQX"
	// snomaskDefault is what +s without letters gives: everything opers
	// got before snomasks, so n, N, t, C, Q and v are off.
	snomaskDefault = "acdfkloqrxKLOX"
)

// applySnomask changes the mask cur by change: "+cq-k" adds c and q and
// takes k away, "cqk" (no sign) adds. Unknown letters are left out. The
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
