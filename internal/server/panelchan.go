package server

import (
	"sort"
	"strings"

	"github.com/fhazal/beeircd/internal/control"
	"github.com/fhazal/beeircd/internal/irc"
	"github.com/fhazal/beeircd/internal/services"
	"github.com/fhazal/beeircd/internal/store"
)

// The channel manager. A channel's page for
// someone on its access list; changes run the real ChanServ commands.

// panelChan is channel name's page for po.
func (s *Server) panelChan(po *control.PanelOper, name string) *control.Response {
	a, bad := s.panelAccount(po)
	if bad != nil {
		return bad
	}
	v, why := s.svc.WebChan(a.ID, name)
	if v == nil {
		return &control.Response{Error: why}
	}
	reg := v.Reg
	c := &control.MyChan{Name: reg.Name, Level: v.Level, Founder: reg.Founder, Successor: reg.Successor, Description: reg.Description,
		URL: reg.URL, Registered: reg.Registered, People: []control.ChanPerson{}, MemoLimit: v.MemoLimit,
		Can: control.ChanCan{AccessList: v.Can.AccessList, AccessChange: v.Can.AccessChange, Akick: v.Can.Akick, Set: v.Can.Set,
			Topic: v.Can.Topic, Memo: v.Can.Memo, Founder: v.Can.Founder}}
	if v.Can.AccessChange {
		// ChanServ only lets you give levels below your own (the founder: any).
		for _, l := range []int{store.LevelVOP, store.LevelHOP, store.LevelAOP, store.LevelSOP, store.LevelQOP} {
			if l < v.Rank {
				c.Give = append(c.Give, store.LevelName(l))
			}
		}
	}
	if v.Can.Set {
		c.Toggles = services.ChanToggles(reg)
	}
	if ch := s.net.Channel(reg.Name); ch != nil {
		c.Topic, c.Users = ch.Topic, len(ch.Members)
		for _, m := range ch.SortedMembers() {
			if m.Hidden {
				continue // +D: nobody sees them yet
			}
			c.People = append(c.People, control.ChanPerson{Nick: m.User.Nick, Prefix: m.Prefix(), Away: m.User.Away != ""})
		}
		sort.SliceStable(c.People, func(i, j int) bool { return rank(c.People[i]) > rank(c.People[j]) })
	}
	online := map[string]bool{}
	for _, u := range s.net.Users() {
		if u.Account != "" {
			online[irc.Fold(u.Account)] = true
		}
	}
	for _, e := range v.Access {
		on := online[irc.Fold(e.Account)]
		if acct, _ := s.store.AccountByID(e.AccountID); on && (acct == nil || acct.HideStatus) {
			on = false // HIDE STATUS: nobody is told whether they are online
		}
		c.Access = append(c.Access, control.ChanAccess{Nick: e.Account, Level: store.LevelName(e.Level), By: e.AddedBy, At: e.AddedAt, Online: on,
			Locked: v.Rank != store.LevelFounder && e.Level >= v.Rank})
	}
	for _, k := range v.Akicks {
		c.Akicks = append(c.Akicks, control.ChanAkick{Mask: k.Mask, Reason: k.Reason, By: k.AddedBy, At: k.AddedAt})
	}
	for _, mm := range v.Memos {
		c.Memos = append(c.Memos, myMemo(mm))
	}
	return &control.Response{Chan: c}
}

// rank orders the People list by status: ~ & @ % + then the rest.
func rank(p control.ChanPerson) int {
	if p.Prefix == "" {
		return 0
	}
	return strings.IndexByte("+%@&~", p.Prefix[0]) + 1
}

// panelChanDo runs one ChanServ change for po (req: Name, Kind, Value,
// Mask, Reason; see control.CmdChanDo).
func (s *Server) panelChanDo(po *control.PanelOper, req control.Request) *control.Response {
	a, bad := s.panelAccount(po)
	if bad != nil {
		return bad
	}
	var args []string
	for _, x := range []string{req.Value, req.Mask, req.Reason} {
		if x = strings.TrimSpace(x); x != "" {
			args = append(args, x)
		}
	}
	ok, answer := s.svc.WebChanServ(a.ID, req.IP, req.Kind, req.Name, args)
	if !ok {
		if answer == "" {
			answer = "Nothing changed."
		}
		return &control.Response{Error: answer}
	}
	return &control.Response{Done: &control.Done{Text: answer}}
}

// panelChanMemo reads or deletes a memo of a channel's box.
func (s *Server) panelChanMemo(po *control.PanelOper, req control.Request) *control.Response {
	a, bad := s.panelAccount(po)
	if bad != nil {
		return bad
	}
	if req.Value != "read" && req.Value != "del" {
		return &control.Response{Error: "Bad request."}
	}
	if why := s.svc.WebChanMemo(a.ID, req.IP, req.Name, req.ID, req.Value == "del"); why != "" {
		return &control.Response{Error: why}
	}
	return &control.Response{Done: &control.Done{Text: "Done."}}
}
