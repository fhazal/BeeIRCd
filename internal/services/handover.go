package services

import "time"

// Handover is what services remember only in memory and one server
// process tells the next at a seamless upgrade (internal/server): what
// they know about each person online, and the switches an oper set.
// Everything else is in the database or is rebuilt from it.
type Handover struct {
	Users    map[string]HandoverUser // by UID
	ReadOnly bool                    // OperServ SET READONLY
	NoExpire bool                    // OperServ SET NOEXPIRE
	Defcon   int                     // OperServ DEFCON (0 = 5, normal)
}

// HandoverUser is the services' own record of one person.
type HandoverUser struct {
	AccountID    int64     // NickServ account they are logged in to
	EnforceAt    time.Time // identify by then, or the nick goes
	EnforceNick  string
	VhostOn      bool
	HelpOn       bool
	SuperAdmin   bool
	LastMail     time.Time
	LastMemoSend time.Time
	GlobalQueue  []string
}

// HandoverSave copies what the next server process needs. Call it on the
// loop, after Flush.
func (s *Services) HandoverSave() *Handover {
	h := &Handover{Users: make(map[string]HandoverUser, len(s.clients)), ReadOnly: s.readonly, NoExpire: s.noexpire, Defcon: s.defcon}
	for uid, c := range s.clients {
		if c.closed || c.user == nil {
			continue
		}
		h.Users[uid] = HandoverUser{
			AccountID:    c.accountID,
			EnforceAt:    c.enforceAt,
			EnforceNick:  c.enforceNick,
			VhostOn:      c.vhostOn,
			HelpOn:       c.helpOn,
			SuperAdmin:   c.superAdmin,
			LastMail:     c.lastMail,
			LastMemoSend: c.lastMemoSend,
			GlobalQueue:  append([]string(nil), c.globalQueue...),
		}
	}
	return h
}

// HandoverLoad takes over after the server put the last process's users
// and channels into the network (state.Network.Load, no events): services
// get to know every person again, quietly: no greeting, no "please
// identify", no channel entry messages, as none of them just arrived. h
// is the last process's HandoverSave (nil: nothing known, everyone counts
// as not logged in); uid turns one of its UIDs into today's ("" = that
// person didn't come along).
func (s *Services) HandoverLoad(h *Handover, uid func(old string) string) {
	if s.off {
		return
	}
	for _, u := range s.net.Users() {
		if u.Service || s.clients[u.UID] != nil {
			continue
		}
		s.clients[u.UID] = &Client{s: s, user: u, ip: u.IP}
	}
	if h != nil {
		s.readonly, s.noexpire, s.defcon = h.ReadOnly, h.NoExpire, h.Defcon
		for old, hu := range h.Users {
			c := s.clients[uid(old)]
			if c == nil {
				continue
			}
			c.accountID, c.enforceAt, c.enforceNick = hu.AccountID, hu.EnforceAt, hu.EnforceNick
			c.vhostOn, c.helpOn, c.superAdmin = hu.VhostOn, hu.HelpOn, hu.SuperAdmin
			c.lastMail, c.lastMemoSend, c.globalQueue = hu.LastMail, hu.LastMemoSend, hu.GlobalQueue
		}
	}
	// A bot's channels came along with the network; which of them its
	// kickers watch is worked out from the database, as at any start.
	s.bsWatchAll()
}
