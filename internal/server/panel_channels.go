package server

// The Hive Panel's Channels page. Every channel on the network
// (secret ones too: opers see them, as in /LIST for opers), biggest first,
// plus registered channels that are suspended and empty. One channel's
// details: topic, modes (never the key), ban counts, who is in it (ops
// first) and its ChanServ registration. Actions: a notice to everyone in
// it (flag g, from Global with the oper's name), the topic (flag s, as
// ChanServ) and ChanServ SUSPEND / UNSUSPEND (flag s).

import (
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/fhazal/beeircd/internal/control"
	"github.com/fhazal/beeircd/internal/irc"
	"github.com/fhazal/beeircd/internal/state"
	"github.com/fhazal/beeircd/internal/store"
)

const channelsPage = 50

// modeLetters is a channel's modes as letters (+ntk), never the key.
func modeLetters(ch *state.Channel) string {
	var b []byte
	for m, on := range ch.Modes {
		if on {
			b = append(b, m)
		}
	}
	if ch.Key != "" && !strings.ContainsRune(string(b), 'k') {
		b = append(b, 'k')
	}
	if ch.Limit > 0 && !strings.ContainsRune(string(b), 'l') {
		b = append(b, 'l')
	}
	for m := range ch.Params {
		if !strings.ContainsRune(string(b), rune(m)) {
			b = append(b, m)
		}
	}
	sort.Slice(b, func(i, j int) bool { return b[i] < b[j] })
	if len(b) == 0 {
		return ""
	}
	return "+" + string(b)
}

func visibleCount(ch *state.Channel) int {
	n := 0
	for _, m := range ch.Members {
		if !m.User.Service {
			n++
		}
	}
	return n
}

func (s *Server) panelChannels(req control.Request) *control.Channels {
	now := s.svc.KeepNow() // NOEXPIRE and READONLY keep run-out suspensions in force
	regs := map[string]*store.Channel{}
	out := &control.Channels{}
	for _, r := range s.svc.RegChannels() {
		regs[irc.Fold(r.Name)] = r
		out.Registered++
		if r.Suspended(now) {
			out.Suspended++
		}
	}
	q := strings.ToLower(strings.TrimSpace(req.Query))
	if len(q) > 100 {
		q = q[:100]
	}
	var rows []control.ChannelBrief
	seen := map[string]bool{}
	for _, ch := range s.net.Channels() {
		out.Open++
		key := irc.Fold(ch.Name)
		seen[key] = true
		n := visibleCount(ch)
		if n > out.BiggestN {
			out.Biggest, out.BiggestN = ch.Name, n
		}
		r := regs[key]
		rows = append(rows, control.ChannelBrief{Name: ch.Name, People: n, Topic: ch.Topic, Modes: modeLetters(ch),
			Registered: r != nil, Suspended: r != nil && r.Suspended(now), Open: true})
	}
	// Suspended channels are usually empty: list them too.
	for key, r := range regs {
		if !seen[key] && r.Suspended(now) {
			rows = append(rows, control.ChannelBrief{Name: r.Name, Registered: true, Suspended: true})
		}
	}
	var match []control.ChannelBrief
	for _, r := range rows {
		switch req.Filter {
		case "registered":
			if !r.Registered {
				continue
			}
		case "unregistered":
			if r.Registered {
				continue
			}
		case "suspended":
			if !r.Suspended {
				continue
			}
		}
		if q != "" && !strings.Contains(strings.ToLower(r.Name), q) && !strings.Contains(strings.ToLower(r.Topic), q) {
			continue
		}
		match = append(match, r)
	}
	sort.Slice(match, func(i, j int) bool {
		if match[i].People != match[j].People {
			return match[i].People > match[j].People
		}
		return irc.Fold(match[i].Name) < irc.Fold(match[j].Name)
	})
	out.Matched = len(match)
	limit := req.Limit
	if limit <= 0 {
		limit = channelsPage
	}
	limit = min(limit, peopleMaxPage)
	off := max(0, min(req.Offset, len(match)))
	out.Offset = off
	out.Rows = append([]control.ChannelBrief{}, match[off:min(off+limit, len(match))]...)
	return out
}

func (s *Server) panelChannel(name string) *control.ChannelInfo {
	now := s.svc.KeepNow() // NOEXPIRE and READONLY keep run-out suspensions in force
	ch := s.net.Channel(name)
	reg := s.svc.RegChannel(name)
	if ch == nil && reg == nil {
		return nil
	}
	info := &control.ChannelInfo{Members: []string{}}
	if ch != nil {
		info.ChannelBrief = control.ChannelBrief{Name: ch.Name, People: visibleCount(ch), Topic: ch.Topic, Modes: modeLetters(ch), Open: true}
		info.TopicBy, info.TopicAt, info.Limit, info.HasKey = ch.TopicBy, ch.TopicAt, ch.Limit, ch.Key != ""
		info.Bans, info.Excepts, info.Invites = len(ch.Lists['b']), len(ch.Lists['e']), len(ch.Lists['I'])
		type mem struct {
			rank state.Rank
			name string
		}
		var ms []mem
		for _, m := range ch.Members {
			if m.User.Service && m.User.Nick != "ChanServ" {
				continue
			}
			ms = append(ms, mem{m.Top(), m.Prefix() + m.User.Nick})
		}
		sort.Slice(ms, func(i, j int) bool {
			if ms[i].rank != ms[j].rank {
				return ms[i].rank > ms[j].rank
			}
			return irc.Fold(strings.TrimLeft(ms[i].name, "~&@%+")) < irc.Fold(strings.TrimLeft(ms[j].name, "~&@%+"))
		})
		for _, m := range ms {
			info.Members = append(info.Members, m.name)
		}
	} else {
		info.ChannelBrief = control.ChannelBrief{Name: reg.Name}
	}
	if reg != nil {
		info.Registered, info.Suspended = true, reg.Suspended(now)
		info.Founder, info.RegisteredAt, info.LastUsed, info.Description = reg.Founder, reg.Registered, reg.LastUsed, reg.Description
		if reg.MlockOn != "" || reg.MlockOff != "" {
			info.Mlock = strings.TrimSpace("+" + reg.MlockOn + " -" + reg.MlockOff)
			info.Mlock = strings.TrimSuffix(strings.TrimSuffix(info.Mlock, " -"), "+")
		}
		if info.Suspended {
			info.SuspendedBy, info.SuspendWhy = reg.SuspendedBy, reg.SuspendReason
			if reg.SuspendUntil.Unix() > 0 {
				info.SuspendTill = reg.SuspendUntil
			}
		}
	}
	return info
}

func (s *Server) panelChanNotice(po *control.PanelOper, ip, name, text string) control.Response {
	text = strings.Join(strings.Fields(text), " ")
	if text == "" {
		return control.Response{Error: "Type a message first."}
	}
	if utf8.RuneCountInString(text) > noticeMax {
		return control.Response{Error: fmt.Sprintf("That is too long (%d characters at most).", noticeMax)}
	}
	if problem := s.svc.PanelChanNotice(po.Account, name, text); problem != "" {
		return control.Response{Error: problem}
	}
	s.panelEvent(po.Account, ip, "sent a notice to "+name)
	return control.Response{Done: &control.Done{Text: "Notice sent to everyone in " + name + "."}}
}

func (s *Server) panelChanTopic(po *control.PanelOper, ip, name, text string) control.Response {
	if problem := s.svc.PanelChanTopic(po.Account, name, text); problem != "" {
		return control.Response{Error: problem}
	}
	s.panelEvent(po.Account, ip, "set the topic of "+name)
	if strings.TrimSpace(text) == "" {
		return control.Response{Done: &control.Done{Text: "The topic of " + name + " is cleared."}}
	}
	return control.Response{Done: &control.Done{Text: "The topic of " + name + " is set."}}
}

// panelChanRename renames a channel (IRCv3 channel-rename) with the same
// checks as RENAME from an IRC operator.
func (s *Server) panelChanRename(po *control.PanelOper, ip, name, newName, reason string) control.Response {
	done, err := s.renameFromPanel(name, newName, reason)
	if err != nil {
		return control.Response{Error: err.Error()}
	}
	s.panelEvent(po.Account, ip, "renamed "+name+" to "+strings.TrimSpace(newName))
	return control.Response{Done: &control.Done{Text: done}}
}

func (s *Server) panelChanSuspend(po *control.PanelOper, ip, name, reason, life string) control.Response {
	d, ok := panelLife(life)
	if !ok {
		return control.Response{Error: "That length isn't one I know. Use 1h, 1d, 7d, 30d or 0 for until unsuspended."}
	}
	if d < 0 {
		d = 0
	}
	kicked, problem := s.svc.PanelChanSuspend(po.Account, name, reason, d)
	if problem != "" {
		return control.Response{Error: problem}
	}
	how := "until unsuspended"
	if d > 0 {
		how = "for " + lengthWords(d)
	}
	s.panelEvent(po.Account, ip, "suspended "+name+" "+how)
	return control.Response{Done: &control.Done{Text: fmt.Sprintf("%s is suspended %s. %d removed.", name, how, kicked)}}
}

func (s *Server) panelChanUnsuspend(po *control.PanelOper, ip, name string) control.Response {
	if problem := s.svc.PanelChanUnsuspend(po.Account, name); problem != "" {
		return control.Response{Error: problem}
	}
	s.panelEvent(po.Account, ip, "unsuspended "+name)
	return control.Response{Done: &control.Done{Text: name + " can be used again."}}
}
