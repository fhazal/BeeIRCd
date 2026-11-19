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
	if s.setUserModes(c, u.UID, m.Params[1], m.Params[2:]...) {
		s.reply(c, irc.ErrUModeUnknownFlag, "Unknown MODE flag")
	}
}

// setUserModes changes c's user modes as the MODE command allows (+o only
// ever goes; +s needs an oper; +r is for services), sent by srcUID (the
// user, or OperServ for UMODE). +s takes the snomask change from args
// when there is one, like InspIRCd. It reports an unknown mode letter.
func (s *Server) setUserModes(c *Client, srcUID, modeStr string, args ...string) bool {
	u := c.user
	// cur is the modes as they will be; the event changes the real ones.
	cur := make(map[byte]bool, len(u.Modes))
	for k, v := range u.Modes {
		cur[k] = v
	}
	add, unknown, deopered := true, false, false
	sno, snoSet := c.snomask, false // the snomask as it will be, and whether +s/-s touched it
	if sno == "" && cur['s'] {
		sno = snomaskDefault
	}
	var changes []state.ModeChange
	flip := func(mc byte, on bool) {
		setFlag(cur, mc, on)
		changes = append(changes, state.ModeChange{Add: on, Mode: mc})
	}
	for i := 0; i < len(modeStr); i++ {
		mc := modeStr[i]
		switch mc {
		case '+':
			add = true
		case '-':
			add = false
		case 'i', 'w':
			if cur[mc] != add {
				flip(mc, add)
			}
		case 'o':
			if !add && cur['o'] { // anyone may drop oper, nobody may take it here
				flip('o', false)
				for _, m := range []byte("s" + operOnlyModes) {
					if cur[m] {
						flip(m, false)
					}
				}
				deopered = true
				if cur['h'] && !s.svc.IsHelper(u.Account) {
					flip('h', false) // an oper's +h goes with oper, a listed helper's stays
				}
			}
		case 'B', 'd', 'D', 'R', 'T', 'p', 'Z', 'g', 'G':
			if cur[mc] != add {
				flip(mc, add)
			}
		case 'H', 'W':
			// Opers only (UnrealIRCd): hide oper status, see who WHOISes you.
			if cur[mc] != add && (!add || c.isOper() && !deopered) {
				flip(mc, add)
			}
		case 'q':
			// Unkickable: opers with flag a only (UnrealIRCd self:unkickablemode).
			if cur[mc] != add && (!add || c.can('a') && !deopered) {
				flip(mc, add)
			}
		case 'h':
			// Helper (HelpServ): opers, people on HelpServ's helper list,
			// or services setting it.
			if cur['h'] != add && (!add || c.isOper() && !deopered || s.svc.IsHelper(u.Account) || s.isServiceUID(srcUID)) {
				flip('h', add)
			}
		case 'z':
			// Set by the server for TLS connections; nobody changes it.
		case 's':
			// Server notices (snomask.go). +s without letters gives the
			// default mask when none is set; +s <letters> changes the
			// mask ("+cq-k", or "cqk" to add); an empty mask is -s.
			if !add {
				if cur['s'] {
					flip('s', false)
				}
				sno, snoSet = "", true
				continue
			}
			change, has := "", false
			if len(args) > 0 {
				change, has, args = args[0], true, args[1:]
			}
			if !c.isOper() || deopered {
				continue
			}
			switch {
			case has:
				sno = applySnomask(sno, change)
			case sno == "":
				sno = snomaskDefault
			}
			snoSet = true
			if (sno != "") != cur['s'] {
				flip('s', sno != "")
			}
		case 'x':
			if s.cfg.CloakEnabled() && cur['x'] != add {
				flip('x', add)
			}
		case 'r':
			// Only services set it; ignored when asked for.
		default:
			unknown = true
		}
	}
	if len(changes) > 0 {
		s.emit(&state.UserMode{Source: srcUID, UID: u.UID, Changes: changes})
	}
	if deopered {
		c.operFlags, c.operTitle, c.operAccount, c.operSwhois, c.operClass = "", "", 0, nil, ""
		u.OperClass, u.OperName = "", ""
		c.snomask = ""
		delete(s.opers, c)
		c.noLag.Store(false)
		s.showOperVhost(c, "")
		s.sno('o', "%s is no longer an operator", u.Nick)
	} else if snoSet {
		c.snomask = sno
		if sno != "" {
			s.replySnomask(c)
		}
	}
	if s.cfg.CloakEnabled() && !s.svc.VHostOn(c.uid) && c.operVhost == "" { // a HostServ or oper vhost stays over +x/-x
		host := u.RealHost
		if u.Modes['x'] {
			host = s.cloakFor(u)
		}
		s.setHost(c, host)
	}
	return unknown
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
	var rank state.Rank
	if mem != nil {
		rank = mem.Top()
	}
	s.applyChannelModes(c, u, ch, rank, m.Params[1], m.Params[2:])
}

// applyChannelModes carries out a mode string on ch for src, who holds
// srcRank (OperServ passes servicesRank). Errors go to c.
func (s *Server) applyChannelModes(c *Client, src *state.User, ch *state.Channel, srcRank state.Rank, modes string, args []string) {
	next := func() (string, bool) {
		if len(args) == 0 {
			return "", false
		}
		a := args[0]
		args = args[1:]
		return a, true
	}
	// can reports whether src's rank allows a mode change that needs need.
	denied := false
	can := func(need state.Rank) bool {
		if srcRank >= need {
			return true
		}
		denied = true
		return false
	}
	var out []state.ModeChange // checked here, applied by the event
	adds := map[byte]int{}     // list entries added so far, for the list limit
	add, used := true, 0
	now := time.Now()
	for i := 0; i < len(modes); i++ {
		mc := modes[i]
		switch {
		case mc == '+':
			add = true
		case mc == '-':
			add = false
		case mc == 'b' || mc == 'e' || mc == 'I':
			arg, ok := next()
			if !ok {
				if (mc == 'b' && !(s.cfg.Channels.HideBans && !c.isOper())) || srcRank >= state.RankHalfop {
					s.sendList(c, ch, mc)
				} else {
					denied = true
				}
				continue
			}
			if !can(state.RankHalfop) || used >= maxModeParams {
				continue
			}
			used++
			mask, valid := irc.NormalizeBan(arg)
			if !valid && irc.BanTooLong(arg) {
				s.reply(c, irc.ErrInvalidModeParam, ch.Name, string(mc), arg, "Ban mask is too long")
				continue
			}
			if !valid {
				s.reply(c, irc.ErrInvalidModeParam, ch.Name, string(mc), arg, "Invalid ban mask or unknown extban (/HELP EXTBANS lists them)")
				continue
			}
			if !irc.ExtbanAllowed(mc, mask) {
				s.reply(c, irc.ErrInvalidModeParam, ch.Name, string(mc), arg, "This extban can't be used in +"+string(mc)+" (/HELP EXTBANS)")
				continue
			}
			if g := groupOfMask(mask); add && g != "" && !s.cfg.GroupKnown(g) {
				s.reply(c, irc.ErrInvalidModeParam, ch.Name, string(mc), arg, "There is no security group "+g+" on this server")
				continue
			}
			if add {
				if len(ch.Lists[mc])+adds[mc] >= maxListEntries {
					s.reply(c, irc.ErrBanListFull, ch.Name, mask, "Channel list is full")
					continue
				}
				adds[mc]++
			}
			out = append(out, state.ModeChange{Add: add, Mode: mc, Arg: mask})
		case state.RankForMode(mc) != 0:
			arg, ok := next()
			if !ok {
				continue
			}
			rank := state.RankForMode(mc)
			if !s.rankEnabled(rank) {
				s.reply(c, irc.ErrUnknownMode, string(mc), "is unknown mode char to me for "+ch.Name)
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
			self := t == src && !add
			if !self && !can(rankNeeded(rank)) {
				continue
			}
			// Owners and admins are protected: only an owner may take
			// status from them.
			if !add && !self && tm.AtLeast(state.RankAdmin) && srcRank < state.RankOwner {
				denied = true
				continue
			}
			out = append(out, state.ModeChange{Add: add, Mode: mc, Arg: t.Nick, UID: t.UID})
		case mc == 'k':
			arg, ok := next()
			if add && !ok {
				continue
			}
			if !can(state.RankOp) {
				continue
			}
			if add {
				key := irc.CleanKey(arg)
				if key == "" || key == ch.Key {
					continue
				}
				out = append(out, state.ModeChange{Add: true, Mode: 'k', Arg: key})
			} else {
				out = append(out, state.ModeChange{Mode: 'k'})
			}
		case mc == 'l':
			var arg string
			if add {
				var ok bool
				if arg, ok = next(); !ok {
					continue
				}
			}
			if !can(state.RankOp) {
				continue
			}
			if add {
				n, err := strconv.Atoi(arg)
				if err != nil || n <= 0 || (ch.Modes['l'] && n == ch.Limit) {
					continue
				}
				out = append(out, state.ModeChange{Add: true, Mode: 'l', Arg: strconv.Itoa(n)})
			} else {
				out = append(out, state.ModeChange{Mode: 'l'})
			}
		case state.IsParamMode(mc):
			var arg string
			if add {
				var ok bool
				if arg, ok = next(); !ok {
					continue
				}
			}
			if !can(state.RankOp) {
				continue
			}
			if !add {
				out = append(out, state.ModeChange{Mode: mc})
				continue
			}
			norm, why := s.checkParamMode(ch.Name, mc, arg)
			if why != "" {
				s.reply(c, irc.ErrInvalidModeParam, ch.Name, string(mc), arg, why)
				continue
			}
			out = append(out, state.ModeChange{Add: true, Mode: mc, Arg: norm})
		case strings.IndexByte(halfopFlagModes, mc) >= 0, strings.IndexByte(opFlagModes, mc) >= 0:
			need := state.RankOp
			if strings.IndexByte(halfopFlagModes, mc) >= 0 {
				need = state.RankHalfop
			}
			if !can(need) {
				continue
			}
			out = append(out, state.ModeChange{Add: add, Mode: mc})
		case mc == 'O' || mc == 'P':
			// Oper-only (+O) and permanent (+P) channels, like UnrealIRCd:
			// only opers set or remove them. When a service acts for
			// someone (ChanServ MODE SET), that person must be an oper too;
			// a service on its own (no asker) may.
			if c != nil && !c.isOper() || c == nil && !src.Service {
				if c != nil {
					s.reply(c, irc.ErrNoPrivileges, "Permission Denied - only IRC operators may set +"+string(mc))
				}
				continue
			}
			out = append(out, state.ModeChange{Add: add, Mode: mc})
		case mc == 'r' || mc == 'Z':
			// Only ChanServ sets +r; +Z follows +z by itself.
		default:
			s.reply(c, irc.ErrUnknownMode, string(mc), "is unknown mode char to me for "+ch.Name)
		}
	}
	if denied {
		if srcRank == state.RankHalfop {
			s.reply(c, irc.ErrChanOPrivsNeeded, ch.Name, "You need a higher channel status for that")
		} else {
			s.reply(c, irc.ErrChanOPrivsNeeded, ch.Name, "You're not channel operator")
		}
	}
	if len(out) == 0 {
		return
	}
	ev := &state.ChannelMode{Source: src.UID, Channel: ch.Name, By: src.Nick, At: now, Changes: out}
	s.emit(ev) // ChanServ looks at the result (MLOCK, SECURE) afterwards
}

// rankNeeded is the rank needed to give or take rank r from someone else,
// following UnrealIRCd: owners give ~ and &, ops give @ and %, halfops +.
func rankNeeded(r state.Rank) state.Rank {
	switch r {
	case state.RankOwner, state.RankAdmin:
		return state.RankOwner
	case state.RankOp, state.RankHalfop:
		return state.RankOp
	}
	return state.RankHalfop
}

// servicesRank is what services and OperServ act with: above everyone.
const servicesRank = state.RankOwner << 1

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
