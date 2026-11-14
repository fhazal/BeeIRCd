package server

import (
	"sort"
	"strings"

	"github.com/fhazal/beeircd/internal/control"
	"github.com/fhazal/beeircd/internal/irc"
	"github.com/fhazal/beeircd/internal/state"
	"github.com/fhazal/beeircd/internal/store"
)

// Member Home and the Memos page.
// Home only reads; the memo actions run MemoServ's own rules. Everything
// is about the signed-in account (po) only.

// homeMemos is how many of the newest memos Home lists.
const homeMemos = 3

func myMemo(m store.Memo) control.MyMemo {
	return control.MyMemo{ID: m.ID, From: m.From, Sent: m.Sent, Text: m.Text, Read: m.Read}
}

// panelAccount is po's account, or the answer when it is gone.
func (s *Server) panelAccount(po *control.PanelOper) (*store.Account, *control.Response) {
	a, err := s.store.AccountByNick(po.Account)
	if err != nil || a == nil {
		return nil, &control.Response{Error: "Your account no longer exists.", Denied: "signin"}
	}
	return a, nil
}

// panelHome is Member Home for po.
func (s *Server) panelHome(po *control.PanelOper) *control.Response {
	a, bad := s.panelAccount(po)
	if bad != nil {
		return bad
	}
	h, why := s.svc.WebHome(a.ID)
	if h == nil {
		return &control.Response{Error: why}
	}
	home := &control.MyHome{Name: h.Name, VHost: h.VHost, Registered: h.Registered, Nicks: h.Nicks, MemoLimit: h.MemoLimit,
		Kept: len(h.Memos), OnMyLists: h.OnMyLists, Memos: []control.MyMemo{}, Channels: []control.HomeChannel{},
		Access: []control.HomeAccess{}, Conns: []control.MyConn{}, Needs: []control.HomeNeed{}}
	for i := len(h.Memos) - 1; i >= 0; i-- {
		m := h.Memos[i]
		if !m.Read {
			home.Unread++
		}
		if len(home.Memos) < homeMemos {
			home.Memos = append(home.Memos, myMemo(m))
		}
	}
	// Their connections: identified to this account, wherever they are.
	mine := map[*state.User]bool{}
	for _, u := range s.net.Users() {
		if u.Service || u.Account == "" || !irc.EqualFold(u.Account, a.Name) {
			continue
		}
		mine[u] = true
		mc := control.MyConn{Nick: u.Nick, Since: u.SignOn, Away: u.Away != ""}
		if c := s.clients[u.UID]; c != nil && c.webirc != "" {
			mc.Web = true
		}
		home.Conns = append(home.Conns, mc)
	}
	sort.Slice(home.Conns, func(i, j int) bool { return home.Conns[i].Since.Before(home.Conns[j].Since) })
	for _, hc := range h.Channels {
		c := control.HomeChannel{Name: hc.Name, Level: hc.Level, Founder: hc.Founder}
		if ch := s.net.Channel(hc.Name); ch != nil {
			c.Users = len(ch.Members)
			if ch.Topic != "" && !ch.TopicAt.IsZero() {
				by, _, _ := strings.Cut(ch.TopicBy, "!") // a nick, never a host
				c.TopicBy, c.TopicAt = by, ch.TopicAt
			}
			var best *state.Member
			for _, m := range ch.Members {
				if mine[m.User] && m.Top() > 0 && (best == nil || m.Top() > best.Top()) {
					best = m
				}
			}
			if best != nil {
				c.Status = best.Prefix()
			}
		}
		home.Channels = append(home.Channels, c)
	}
	for _, e := range h.Access {
		home.Access = append(home.Access, control.HomeAccess{Channel: e.Channel, Who: e.Who, Level: e.Level, By: e.By, At: e.At})
	}
	for _, n := range h.Needs {
		home.Needs = append(home.Needs, control.HomeNeed{Text: n.Text, Hint: n.Hint, Page: n.Page})
	}
	total, _, _ := s.net.PeopleCounts()
	home.Users, home.ChanCount, home.Servers = total, s.net.ChannelCount(), len(s.netServers())
	return &control.Response{Home: home}
}

// panelMemos is the Memos page: the whole box.
func (s *Server) panelMemos(po *control.PanelOper) *control.Response {
	a, bad := s.panelAccount(po)
	if bad != nil {
		return bad
	}
	memos, limit, why := s.svc.WebMemos(a.ID)
	if why != "" {
		return &control.Response{Error: why}
	}
	out := &control.MyMemos{Memos: []control.MyMemo{}, Limit: limit}
	for _, m := range memos {
		out.Memos = append(out.Memos, myMemo(m))
	}
	return &control.Response{Memos: out}
}

// panelMemoAct reads or deletes memo id, or sends one, for po.
func (s *Server) panelMemoAct(po *control.PanelOper, cmd, ip string, id int64, to, text string) *control.Response {
	a, bad := s.panelAccount(po)
	if bad != nil {
		return bad
	}
	switch cmd {
	case control.CmdMemoRead:
		if why := s.svc.WebMemoRead(a.ID, id); why != "" {
			return &control.Response{Error: why}
		}
		return &control.Response{Done: &control.Done{Text: "Read."}}
	case control.CmdMemoDel:
		if why := s.svc.WebMemoDel(a.ID, id, ip); why != "" {
			return &control.Response{Error: why}
		}
		return &control.Response{Done: &control.Done{Text: "Memo deleted."}}
	}
	ok, answer := s.svc.WebMemoSend(a.ID, ip, to, text)
	if !ok {
		return &control.Response{Error: answer}
	}
	return &control.Response{Done: &control.Done{Text: answer}}
}
