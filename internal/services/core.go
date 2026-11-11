// Package services is BeeIRCd's built-in network services: NickServ,
// ChanServ, MemoServ, OperServ and Global. AdminServ is the server's own
// (it changes the server it runs on) and only uses the bot framework here.
//
// Services see the network through the event layer: the server hands every
// event to Event, and services keep their own record per user (identified
// account, identify deadline, pending codes) keyed by UID. Nothing here
// knows about connections, so once servers link, services on the hub work
// the same for people on any server.
//
// Everything runs on the server's loop goroutine.
package services

import (
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/fhazal/beeircd/internal/config"
	"github.com/fhazal/beeircd/internal/state"
	"github.com/fhazal/beeircd/internal/store"
)

// Host is what services need from the server.
type Host interface {
	// Emit applies a network change (see state.Event).
	Emit(ev state.Event) error
	// Notice sends a NOTICE from the user fromUID to toUID.
	Notice(fromUID, toUID, text string)
	// Numeric sends a numeric reply to uid.
	Numeric(uid, code string, params ...string)
	// ServerNotice sends a notice from the server to uid.
	ServerNotice(uid, text string)
	// Snotice tells opers with +s and writes to the log.
	Snotice(text string)
	// Disconnect closes uid's connection with reason as quit message.
	Disconnect(uid, reason string)
	// Kill is an oper-style KILL of uid by by.
	Kill(uid, by, reason string)
	// OperFlags are uid's oper flags, "" when not an oper.
	OperFlags(uid string) string
	// AccountGone runs after account id was dropped: whoever opered with
	// it loses oper status.
	AccountGone(id int64)
	// IsAdminAccount reports whether a is the admin from beeircd.conf.
	IsAdminAccount(a *store.Account) bool
	// ApplyChannelModes runs a mode string on a channel as srcUID with
	// services rank; errors go to uid.
	ApplyChannelModes(uid, srcUID, channel, modes string, args []string)
	// ChannelMessage sends a NOTICE (or PRIVMSG) from fromUID to channel;
	// with status (a prefix like '@') only to members with that rank or up.
	ChannelMessage(fromUID, channel string, status byte, privmsg bool, text string)
	// ApplySetting changes a server setting the way AdminServ SET does,
	// answering uid as bot.
	ApplySetting(uid, bot, key, value string)
	// Post runs f on the loop later (from another goroutine).
	Post(f func())
	// Stats is what OperServ STATS shows about the server itself.
	Stats() HostStats
}

// HostStats are server figures for OperServ STATS.
type HostStats struct {
	Started   time.Time
	PeakUsers int
	Loop      string
}

// Services is the running services.
type Services struct {
	host  Host
	cfg   *config.Config
	log   *log.Logger
	net   *state.Network
	store *store.Store

	services map[string]*service // by folded nick
	nickServ *service
	memoServ *service
	chanServ *service
	operServ *service
	global   *service
	clients  map[string]*Client // by UID: every person on the network
	lockouts map[int64]*lockout // wrong passwords per account
	hashSem  chan struct{}      // limits password hashing running at once
	pending  []state.Event      // events not handled yet (see Event)
	handling bool

	// Oper data, kept in memory.
	akills         []store.Akill
	news           []store.News
	sessionExcepts []store.SessionExcept
	forbids        map[string][]store.Forbid // by kind
}

// Client is services' record of one person on the network.
type Client struct {
	s           *Services
	user        *state.User
	ip          string
	closed      bool      // gone from the network
	accountID   int64     // NickServ account, 0 when not identified
	enforceAt   time.Time // rename to Guest at this time, unless identified
	enforceNick string    // the nick enforceAt is about
	pwBusy      bool      // a password check is running
	dropCode    string    // confirms /ns drop
	dropUntil   time.Time // dropCode is valid until then
	csUsedOn    string    // channel a ChanServ command passed its access check on (for LOG)
}

func (c *Client) isOper() bool    { return c.s.host.OperFlags(c.user.UID) != "" }
func (c *Client) can(f byte) bool { return strings.IndexByte(c.s.host.OperFlags(c.user.UID), f) >= 0 }

// New starts services on an open store. Call Start once the server can
// take events.
func New(host Host, cfg *config.Config, logger *log.Logger, net *state.Network, st *store.Store) (*Services, error) {
	s := &Services{
		host:     host,
		cfg:      cfg,
		log:      logger,
		net:      net,
		store:    st,
		services: make(map[string]*service),
		clients:  make(map[string]*Client),
		lockouts: make(map[int64]*lockout),
		hashSem:  make(chan struct{}, 2),
	}
	if err := s.loadOperData(); err != nil {
		return nil, err
	}
	return s, nil
}

// Event takes one applied network event. Services act on it after the
// command that caused it has finished (see Flush), so their notices and
// modes come after the server's own replies, as clients expect.
func (s *Services) Event(ev state.Event) {
	switch e := ev.(type) {
	case *state.UserAdd:
		if !e.User.Service {
			s.clients[e.User.UID] = &Client{s: s, user: e.User, ip: e.User.IP}
		}
	case *state.UserQuit:
		if c := s.clients[e.UID]; c != nil {
			c.closed = true
		}
	}
	s.pending = append(s.pending, ev)
}

// Flush handles the events taken since the last Flush. The server calls it
// after every piece of loop work.
func (s *Services) Flush() {
	if s.handling {
		return
	}
	s.handling = true
	defer func() { s.handling = false }()
	for len(s.pending) > 0 {
		ev := s.pending[0]
		s.pending = s.pending[1:]
		s.handle(ev)
	}
}

func (s *Services) handle(ev state.Event) {
	switch e := ev.(type) {
	case *state.UserAdd:
		if c := s.clients[e.User.UID]; c != nil && !c.closed {
			s.connected(c)
		}
	case *state.UserQuit:
		if c := s.clients[e.UID]; c != nil {
			s.saveQuit(c, e.Reason)
			delete(s.clients, e.UID)
		}
	case *state.NickChange:
		if c := s.clients[e.UID]; c != nil && !c.closed {
			s.nickCheck(c)
		}
	case *state.Join:
		c := s.clients[e.UID]
		ch := s.net.Channel(e.Channel)
		if c != nil && !c.closed && ch != nil && ch.Member(c.user) != nil {
			s.csJoin(c, ch, e.Created)
		}
	case *state.ChannelMode:
		if ch := s.net.Channel(e.Channel); ch != nil && e.Source != s.chanServ.user.UID && len(e.Changes) > 0 {
			s.csAfterUserModes(ch, fromState(e.Changes))
		}
		if ch := s.net.Channel(e.Channel); ch != nil && len(e.Changes) > 0 {
			s.csModesChanged(ch)
		}
	case *state.Topic:
		if ch := s.net.Channel(e.Channel); ch != nil && e.Source != s.chanServ.user.UID {
			s.csTopicChanged(ch)
		}
	}
}

// connected runs once a new person is on the network: an account they
// logged in to with SASL first, then the nick check.
func (s *Services) connected(c *Client) {
	if c.user.Account != "" {
		if a, err := s.store.AccountByNick(c.user.Account); err == nil && a != nil {
			c.accountID = a.ID
			a.LastSeen, a.LastHost = time.Now(), c.user.Ident+"@"+c.user.RealHost
			if err := s.store.SaveAccount(a); err != nil {
				s.log.Printf("services database: %v", err)
			}
			s.memoSummary(c, a)
		}
	}
	s.nickCheck(c)
}

// Tick runs the timers: identify deadlines every second, expiry hourly
// (the server decides when).
func (s *Services) Tick(now time.Time) {
	for _, c := range s.clients {
		if !c.closed && !c.enforceAt.IsZero() && now.After(c.enforceAt) {
			s.enforce(c)
		}
	}
	s.Flush()
}

// Expire drops accounts, channels and AKILLs that ran out.
func (s *Services) Expire(now time.Time) {
	s.expireAccounts(now)
	s.expireChannels(now)
	s.expireSuspends(now)
	s.expireAkills(now)
	s.Flush()
}

// AccountID is the account uid is identified to, 0 for none.
func (s *Services) AccountID(uid string) int64 {
	if c := s.clients[uid]; c != nil {
		return c.accountID
	}
	return 0
}

// client returns services' record of u, or nil.
func (s *Services) clientOf(u *state.User) *Client {
	if u == nil {
		return nil
	}
	return s.clients[u.UID]
}

// Small bridges so the command code reads as before.

func (s *Services) emit(ev state.Event) error { return s.host.Emit(ev) }
func (s *Services) reply(c *Client, code string, params ...string) {
	s.host.Numeric(c.user.UID, code, params...)
}
func (s *Services) notice(c *Client, text string) { s.host.ServerNotice(c.user.UID, text) }
func (s *Services) quit(c *Client, reason string) { s.host.Disconnect(c.user.UID, reason) }
func (s *Services) snotice(format string, a ...any) {
	s.host.Snotice(fmt.Sprintf(format, a...))
}
func (s *Services) killUser(u *state.User, by, reason string) { s.host.Kill(u.UID, by, reason) }
func (s *Services) isAdminAccount(a *store.Account) bool      { return s.host.IsAdminAccount(a) }
func (s *Services) post(f func())                             { s.host.Post(func() { f(); s.Flush() }) }

// rankEnabled reports whether this network uses rank r.
func (s *Services) rankEnabled(r state.Rank) bool {
	if s.cfg.FullPrefixes() {
		return true
	}
	return r == state.RankOp || r == state.RankVoice
}
