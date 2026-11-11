package server

import (
	"strconv"
	"strings"
	"time"

	"github.com/fhazal/beeircd/internal/config"
	"github.com/fhazal/beeircd/internal/irc"
	"github.com/fhazal/beeircd/internal/state"
)

// IRCd-style ban commands (UnrealIRCd habit). They are short forms of the
// OperServ commands, so there is one list behind both:
//
//	/GLINE [-]<mask> [<duration> <reason>]   = /os akill
//	/ZLINE, /SHUN, /QLINE (/os sqline), /RLINE (/os snline), /ELINE
//	(/os exception ban). No arguments lists them.
var xlineTargets = map[string]struct {
	os   string // the OperServ command
	flag byte
}{
	"GLINE": {"AKILL", 'k'},
	"ZLINE": {"ZLINE", 'k'},
	"SHUN":  {"SHUN", 'k'},
	"QLINE": {"SQLINE", 's'},
	"RLINE": {"SNLINE", 'k'},
	"ELINE": {"EXCEPTION BAN", 'k'},
}

func init() {
	for name := range xlineTargets {
		commands[name] = command{0, false, (*Server).cmdXline}
	}
}

func (s *Server) cmdXline(c *Client, m irc.Message) {
	t := xlineTargets[m.Command]
	if !s.needFlag(c, nil, t.flag) {
		return
	}
	var text string
	switch p := m.Params; {
	case len(p) == 0:
		text = t.os + " LIST"
	case strings.HasPrefix(p[0], "-"):
		text = t.os + " DEL " + p[0][1:]
	default:
		mask := strings.TrimPrefix(p[0], "+")
		rest := p[1:]
		expiry := ""
		if len(rest) > 1 {
			if d, ok := xlineDuration(rest[0]); ok {
				expiry, rest = d, rest[1:]
			}
		}
		reason := strings.Join(rest, " ")
		if reason == "" {
			reason = "No reason given"
		}
		text = strings.TrimSpace(t.os + " ADD " + expiry + " " + mask + " " + reason)
	}
	s.svc.MessageAlias(c.uid, "os", strings.Join(strings.Fields(text), " "))
}

// xlineDuration reads UnrealIRCd's durations (1d, 2h30m, 3600 = seconds,
// 0 = until removed) as an OperServ +expiry.
func xlineDuration(arg string) (string, bool) {
	if arg == "0" {
		return "", true
	}
	if n, err := strconv.Atoi(arg); err == nil && n > 0 {
		return "+" + strconv.Itoa(max(1, n/60)) + "m", true
	}
	if d, err := config.ParseDuration(arg); err == nil && d >= time.Minute {
		return "+" + arg, true
	}
	return "", false
}

// shunAllowed are the commands a shunned person may still use
// (UnrealIRCd's list).
var shunAllowed = map[string]bool{"PING": true, "PONG": true, "QUIT": true, "PART": true, "ADMIN": true}

// timedBan is a ~t: list entry and when it lifts.
type timedBan struct {
	channel string
	mode    byte
	mask    string
	until   time.Time
}

// noteTimedBans remembers ~t: entries a mode change added.
func (s *Server) noteTimedBans(e *state.ChannelMode) {
	for _, m := range e.Changes {
		if !m.Add || (m.Mode != 'b' && m.Mode != 'e' && m.Mode != 'I') {
			continue
		}
		if mins, ok := irc.TimedBan(m.Arg); ok {
			s.timedBans = append(s.timedBans, timedBan{e.Channel, m.Mode, m.Arg, time.Now().Add(time.Duration(mins) * time.Minute)})
		}
	}
}

// expireTimedBans lifts ~t: entries whose time is up.
func (s *Server) expireTimedBans(now time.Time) {
	keep := s.timedBans[:0]
	var due []timedBan
	for _, t := range s.timedBans {
		if now.Before(t.until) {
			keep = append(keep, t)
		} else {
			due = append(due, t)
		}
	}
	s.timedBans = keep
	for _, t := range due {
		ch := s.net.Channel(t.channel)
		if ch == nil {
			continue
		}
		for _, e := range ch.Lists[t.mode] {
			if e.Mask == t.mask {
				s.emit(&state.ChannelMode{Channel: ch.Name, By: s.cfg.Server.Name, At: now,
					Changes: []state.ModeChange{{Add: false, Mode: t.mode, Arg: t.mask}}})
				break
			}
		}
	}
}
