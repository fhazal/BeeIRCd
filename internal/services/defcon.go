package services

// OperServ DEFCON (Anope's os_defcon, with its usual levels
// fixed instead of configured). 5 is normal; each lower level adds to
// the one above:
//   4  no new nick or channel registrations
//   3  no new memos (opers still may)
//   2  new connections are disconnected (opers' and linked servers'
//      netjoins are not)
//   1  services answer IRC operators only
// Everyone gets a Global notice when the level changes.

import (
	"fmt"
	"sort"
	"strconv"
	"time"

	"github.com/fhazal/beeircd/internal/irc"
)

// defconBusy is the reply Anope gives while DEFCON blocks something.
const defconBusy = "Services are in DefCon mode, please try again later."

// DefconLevel is the network's DEFCON level (5 = normal).
func (s *Services) DefconLevel() int {
	if s.defcon == 0 {
		return 5
	}
	return s.defcon
}

// defconBlocks reports whether level-or-lower is on, and tells c why.
func (s *Services) defconBlocks(c *Client, sv *service, level int) bool {
	if s.DefconLevel() > level {
		return false
	}
	s.svcReply(c, sv, defconBusy)
	return true
}

var defconWords = map[int]string{
	5: "normal",
	4: "no new nick or channel registrations",
	3: "no new registrations or memos",
	2: "no registrations, memos or new connections",
	1: "services for IRC operators only, no new connections",
}

func osDefcon(s *Services, c *Client, args []string) {
	if len(args) == 0 {
		s.os(c, "Services are now at DEFCON %d: %s.", s.DefconLevel(), defconWords[s.DefconLevel()])
		return
	}
	if !s.needFlag(c, s.operServ, 'a') {
		return
	}
	n, err := strconv.Atoi(args[0])
	if err != nil || n < 1 || n > 5 {
		s.os(c, "Syntax: DEFCON [1|2|3|4|5]")
		return
	}
	was := s.DefconLevel()
	s.defcon = n
	s.audit(c, s.operServ, "DEFCON %d (was %d)", n, was)
	s.osLog(c, "to change the DefCon level to %d", n)
	s.os(c, "Services are now at DEFCON %d: %s.", n, defconWords[n])
	if n != was {
		text := fmt.Sprintf("The Defcon level is now at: %d", n) // Anope's wording
		for _, oc := range s.clients {
			if !oc.closed {
				s.host.Notice(s.global.user.UID, oc.user.UID, text)
			}
		}
		s.snotice('a', "%s changed the DEFCON level to %d (%s)", c.user.Nick, n, defconWords[n])
	}
}

// defconNewUser disconnects someone who just connected at DEFCON 2 or 1.
// Users older than a minute are a netjoin, not new.
func (s *Services) defconNewUser(c *Client) bool {
	u := c.user
	if s.DefconLevel() > 2 || u.Service || c.isOper() || time.Since(u.SignOn) > time.Minute {
		return false
	}
	s.host.Kill(u.UID, s.operServ.user.Nick, "This network is in DEFCON "+strconv.Itoa(s.DefconLevel())+": no new connections for now, please try again later")
	return true
}

// defconOperOnly is DEFCON 1: services ignore everyone but opers.
func (s *Services) defconOperOnly(c *Client, sv *service) bool {
	if s.DefconLevel() > 1 || c.isOper() {
		return false
	}
	s.svcReply(c, sv, defconBusy)
	return true
}

// AddCommands adds commands the server runs to an existing service
// (OperServ JUPE, NOOP, SERVER live in the server).
func (s *Services) AddCommands(botNick string, cmds map[string]BotCommand) {
	sv := s.services[irc.Fold(botNick)]
	if sv == nil {
		return
	}
	for name, bc := range cmds {
		run := bc.Run
		sv.cmds[name] = svcCmd{bc.Syntax, bc.Short, bc.Long, func(_ *Services, c *Client, args []string) { run(c.user.UID, args) }}
		sv.order = append(sv.order, name)
	}
	sort.Strings(sv.order)
}
