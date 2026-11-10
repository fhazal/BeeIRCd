package server

import (
	"strconv"
	"strings"
	"time"

	"github.com/fhazal/beeircd/internal/irc"
	"github.com/fhazal/beeircd/internal/state"
)

type modeChange struct {
	add  bool
	mode byte
	arg  string
}

// formatModes turns changes into "+o-v" and its arguments.
func formatModes(changes []modeChange) []string {
	var b strings.Builder
	var args []string
	sign := byte(0)
	for _, ch := range changes {
		s := byte('-')
		if ch.add {
			s = '+'
		}
		if s != sign {
			b.WriteByte(s)
			sign = s
		}
		b.WriteByte(ch.mode)
		if ch.arg != "" {
			args = append(args, ch.arg)
		}
	}
	return append([]string{b.String()}, args...)
}

func (s *Server) cmdMode(c *Client, m irc.Message) {
	if irc.IsChannel(m.Params[0]) {
		s.channelMode(c, m)
	} else {
		s.userMode(c, m)
	}
}

func (s *Server) userMode(c *Client, m irc.Message) {
	u := c.user
	if !irc.EqualFold(m.Params[0], u.Nick) {
		if s.net.UserByNick(m.Params[0]) == nil {
			s.reply(c, irc.ErrNoSuchNick, m.Params[0], "No such nick/channel")
		} else {
			s.reply(c, irc.ErrUsersDontMatch, "Can't change mode for other users")
		}
		return
	}
	if len(m.Params) < 2 {
		s.replyBare(c, irc.RplUModeIs, u.ModeString())
		return
	}
	add, unknown := true, false
	var changes []modeChange
	for i := 0; i < len(m.Params[1]); i++ {
		mc := m.Params[1][i]
		switch mc {
		case '+':
			add = true
		case '-':
			add = false
		case 'i', 'w':
			if u.Modes[mc] != add {
				setFlag(u.Modes, mc, add)
				changes = append(changes, modeChange{add: add, mode: mc})
			}
		case 'o':
			if !add && u.Modes['o'] { // anyone may drop oper, nobody may take it here
				delete(u.Modes, 'o')
				changes = append(changes, modeChange{mode: 'o'})
			}
		case 'x':
			if s.cfg.CloakEnabled() && u.Modes['x'] != add {
				setFlag(u.Modes, 'x', add)
				changes = append(changes, modeChange{add: add, mode: 'x'})
			}
		case 'r', 's':
			// Set by services or for opers only; ignored when asked for.
		default:
			unknown = true
		}
	}
	if unknown {
		s.reply(c, irc.ErrUModeUnknownFlag, "Unknown MODE flag")
	}
	if len(changes) > 0 {
		c.send(irc.Message{Source: u.Nick, Command: "MODE", Params: []string{u.Nick, formatModes(changes)[0]}}.String())
	}
	if s.cfg.CloakEnabled() {
		host := u.RealHost
		if u.Modes['x'] {
			host = s.cloakFor(u)
		}
		s.setHost(c, host)
	}
}

func setFlag(m map[byte]bool, f byte, on bool) {
	if on {
		m[f] = true
	} else {
		delete(m, f)
	}
}

func (s *Server) channelMode(c *Client, m irc.Message) {
	u := c.user
	ch := s.net.Channel(m.Params[0])
	if ch == nil {
		s.reply(c, irc.ErrNoSuchChannel, m.Params[0], "No such channel")
		return
	}
	mem := ch.Member(u)
	if len(m.Params) < 2 {
		modes, params := ch.ModeString(mem != nil)
		s.replyBare(c, irc.RplChannelModeIs, append([]string{ch.Name, modes}, params...)...)
		s.replyBare(c, irc.RplCreationTime, ch.Name, strconv.FormatInt(ch.TS, 10))
		return
	}
	isOp := mem != nil && mem.Op
	args := m.Params[2:]
	next := func() (string, bool) {
		if len(args) == 0 {
			return "", false
		}
		a := args[0]
		args = args[1:]
		return a, true
	}
	var out []modeChange
	add, needOp, used := true, false, 0
	now := time.Now()
	for i := 0; i < len(m.Params[1]); i++ {
		mc := m.Params[1][i]
		switch mc {
		case '+':
			add = true
		case '-':
			add = false
		case 'b', 'e', 'I':
			arg, ok := next()
			if !ok {
				if mc == 'b' || isOp {
					s.sendList(c, ch, mc)
				} else {
					needOp = true
				}
				continue
			}
			if !isOp {
				needOp = true
				continue
			}
			if used >= maxModeParams {
				continue
			}
			used++
			mask := irc.NormalizeMask(arg)
			if add {
				if len(ch.Lists[mc]) >= maxListEntries {
					s.reply(c, irc.ErrBanListFull, ch.Name, mask, "Channel list is full")
					continue
				}
				if ch.AddListEntry(mc, mask, u.Nick, now) {
					out = append(out, modeChange{true, mc, mask})
				}
			} else if stored, ok := ch.RemoveListEntry(mc, mask); ok {
				out = append(out, modeChange{false, mc, stored})
			}
		case 'o', 'v':
			arg, ok := next()
			if !ok {
				continue
			}
			if !isOp {
				needOp = true
				continue
			}
			if used >= maxModeParams {
				continue
			}
			used++
			t := s.net.UserByNick(arg)
			if t == nil {
				s.reply(c, irc.ErrNoSuchNick, arg, "No such nick/channel")
				continue
			}
			tm := ch.Member(t)
			if tm == nil {
				s.reply(c, irc.ErrUserNotInChannel, t.Nick, ch.Name, "They aren't on that channel")
				continue
			}
			flag := &tm.Op
			if mc == 'v' {
				flag = &tm.Voice
			}
			if *flag != add {
				*flag = add
				out = append(out, modeChange{add, mc, t.Nick})
			}
		case 'k':
			arg, ok := next()
			if add && !ok {
				continue
			}
			if !isOp {
				needOp = true
				continue
			}
			if add {
				key := cleanKey(arg)
				if key == "" || key == ch.Key {
					continue
				}
				ch.Key = key
				ch.Modes['k'] = true
				out = append(out, modeChange{true, 'k', key})
			} else if ch.Modes['k'] {
				out = append(out, modeChange{false, 'k', ch.Key})
				delete(ch.Modes, 'k')
				ch.Key = ""
			}
		case 'l':
			var arg string
			if add {
				var ok bool
				if arg, ok = next(); !ok {
					continue
				}
			}
			if !isOp {
				needOp = true
				continue
			}
			if add {
				n, err := strconv.Atoi(arg)
				if err != nil || n <= 0 || (ch.Modes['l'] && n == ch.Limit) {
					continue
				}
				ch.Limit = n
				ch.Modes['l'] = true
				out = append(out, modeChange{true, 'l', strconv.Itoa(n)})
			} else if ch.Modes['l'] {
				delete(ch.Modes, 'l')
				ch.Limit = 0
				out = append(out, modeChange{false, 'l', ""})
			}
		case 'i', 'm', 'n', 'p', 's', 't', 'R', 'M':
			if !isOp {
				needOp = true
				continue
			}
			if ch.Modes[mc] != add {
				setFlag(ch.Modes, mc, add)
				out = append(out, modeChange{add, mc, ""})
			}
		case 'r':
			// Only ChanServ sets +r.
		default:
			s.reply(c, irc.ErrUnknownMode, string(mc), "is unknown mode char to me for "+ch.Name)
		}
	}
	if needOp {
		s.reply(c, irc.ErrChanOPrivsNeeded, ch.Name, "You're not channel operator")
	}
	if len(out) > 0 {
		params := append([]string{ch.Name}, formatModes(out)...)
		s.broadcast(ch, s.out(u, "MODE", params...), nil)
	}
}

// cleanKey drops characters a key can't hold and caps it at 23.
func cleanKey(k string) string {
	k = strings.Map(func(r rune) rune {
		if r <= ' ' || r == ',' || r == ':' {
			return -1
		}
		return r
	}, k)
	return irc.Truncate(k, 23)
}

func (s *Server) sendList(c *Client, ch *state.Channel, list byte) {
	item, end, endText := irc.RplBanList, irc.RplEndOfBanList, "End of Channel Ban List"
	switch list {
	case 'e':
		item, end, endText = irc.RplExceptList, irc.RplEndOfExcept, "End of Channel Exception List"
	case 'I':
		item, end, endText = irc.RplInviteList, irc.RplEndOfInvite, "End of Channel Invite List"
	}
	for _, e := range ch.Lists[list] {
		s.replyBare(c, item, ch.Name, e.Mask, e.SetBy, strconv.FormatInt(e.SetAt.Unix(), 10))
	}
	s.reply(c, end, ch.Name, endText)
}
