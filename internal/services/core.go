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
	"regexp"
	"strings"
	"time"

	"github.com/fhazal/beeircd/internal/config"
	"github.com/fhazal/beeircd/internal/extauth"
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
	// Snotice tells the opers whose snomask has letter and writes to the
	// log.
	Snotice(letter byte, text string)
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
	// CheckModeParam checks the argument of parameter mode m (+f +F +L
	// +E +B +J) on channel and returns it as stored, or why it can't be.
	CheckModeParam(channel string, m byte, arg string) (string, string)
	// ChannelMessage sends a NOTICE (or PRIVMSG) from fromUID to channel;
	// with status (a prefix like '@') only to members with that rank or up.
	ChannelMessage(fromUID, channel string, status byte, privmsg bool, text string)
	// ApplySetting changes a server setting the way AdminServ SET does,
	// answering uid as bot.
	ApplySetting(uid, bot, key, value string)
	// Post runs f on the loop later (from another goroutine).
	Post(f func())
	// DefaultHost is the host uid shows without a vhost: the cloak with +x,
	// else the real host.
	DefaultHost(uid string) string
	// Stats is what OperServ STATS shows about the server itself.
	Stats() HostStats
	// ResetPeak makes the people peak the number online now (STATS RESET).
	ResetPeak()
	// Rehash re-reads beeircd.conf like /REHASH (OperServ RELOAD). Services
	// run on the loop, so it must not post to the loop and wait.
	Rehash(by string) error
	// JoinBlocked says why uid may not join channel with key the normal
	// way: the numeric ("" = it may) and whether an invite would get it in
	// (bans, +i, +k, +l, +R), as JOIN itself decides.
	JoinBlocked(uid, channel, key string) (code string, invitable bool)
	// ForceJoin joins uid to channel past every mode and ban (SVSJOIN),
	// as if they had typed JOIN.
	ForceJoin(uid, channel string)
	// SetUserModes changes uid's user modes as MODE allows them, sent by
	// srcUID (UMODE). It reports an unknown mode letter.
	SetUserModes(uid, srcUID, modes string) bool
}

// HostStats are server figures for OperServ STATS.
type HostStats struct {
	Started   time.Time
	PeakUsers int
	Loop      string
	DNSBL     string   // hits per DNSBL list, "" when none is set up
	Links     []string // one line per server link, for STATS UPLINK
}

// Services is the running services.
type Services struct {
	// off: [services] enabled = false. The bots are made but never put
	// on the network, events are ignored and nobody is tracked; only the
	// network ban list (bans that arrive over links) stays in force.
	// People use the services server's bots over the link.
	off bool

	host Host
	cfg  *config.Config
	// botSeq numbers bots on the services pseudo-server.
	botSeq int
	// readonly is OperServ SET READONLY: no command, expiry or web form
	// changes the services data. Runtime only, off after a restart.
	readonly bool
	// noexpire is OperServ SET NOEXPIRE: nicks, channels, bans, forbids and
	// session exceptions never expire, and run-out ones stay in force.
	// Runtime only, off after a restart.
	noexpire bool
	// defcon is OperServ DEFCON (0 = 5, normal).
	defcon int
	log    *log.Logger
	net    *state.Network
	store  *store.Store

	// secretDone runs on the loop (as a new job) after every password
	// check or hash finished, so the server can go on with that client's
	// held lines in order.
	secretDone func()

	services map[string]*service // by folded nick
	nickServ *service
	memoServ *service
	chanServ *service
	operServ *service
	global   *service
	hostServ *service
	botServ  *service
	helpServ *service
	help     helpState          // HelpServ (helpserv.go)
	bots     map[int64]*service // BotServ bots made with BOT ADD, by store ID
	// BotServ kickers (botserv_kick.go)
	bsWatch     map[string]bool     // folded channels with a bot and a kicker on
	kickRuns    map[string]*kickRun // by folded channel
	amsg        map[string]*amsgLast
	kickPurged  time.Time
	bsAmsgOn    bool                 // a watched channel has the AMSG kicker on
	fantasyBot  *service             // while a fantasy command runs: answers for ChanServ
	fantasyMute bool                 // while !mute runs: BAN sets a quiet ban
	clients     map[string]*Client   // by UID: every person on the network
	lockouts    map[lockKey]*lockout // wrong passwords per account and source IP
	hashSem     chan struct{}        // limits password hashing running at once
	ext         extauth.Source       // the outside login source ([auth]), nil when off
	extCfg      config.Auth          // what ext was opened with
	extSem      chan struct{}        // limits questions to it running at once
	extLogged   time.Time            // when its last error went to the log
	pending     []state.Event        // events not handled yet (see Event)
	handling    bool

	// Oper data, kept in memory.
	netbans        []store.NetBan
	bansLoaded     bool                             // the first load is no change
	banHook        func(b store.NetBan, add bool)   // see OnBanChange
	geoLookup      func(ip string) (string, uint32) // see SetGeoLookup
	spam           spamState                        // spam filter rules (spamfilter.go)
	operHook       func()                           // see OnOperData
	banRegexps     map[string]*regexp.Regexp        // compiled SNLINE /regex/ masks
	news           []store.News
	sessionExcepts []store.SessionExcept
	linkedExcepts  []store.SessionExcept     // from the services server, on a server whose services are off
	forbids        map[string][]store.Forbid // by kind
	randNews       int                       // the RANDOMNEWS line the next connection gets
	ignores        []store.Ignore            // OperServ IGNORE
	banDue         time.Time                 // when the next timed ban, forbid or exception runs out (zero = none); see ExpireDue

	// SEEN, kept in memory and saved every minute (see SaveSeen).
	seen      map[string]*store.Seen // by folded nick
	seenDirty map[string]bool
	seenGone  []string
	seenSaved time.Time

	mailer      Mailer              // how mail goes out (tests replace it)
	resets      map[int64]resetInfo // NickServ RESETPASS codes by account
	webMemoSent map[int64]time.Time // Hive Panel memo SEND per account (senddelay), webhome.go

	now     func() time.Time   // the clock for log flood caps (tests replace it)
	hitCaps map[string]*hitCap // log channel flood caps by ban mask / IP, logevents.go
}

// Client is services' record of one person on the network.
type Client struct {
	s            *Services
	user         *state.User
	ip           string
	closed       bool         // gone from the network
	accountID    int64        // NickServ account, 0 when not identified
	langAccount  int64        // while set: replies use this account's language (it is logging in)
	enforceAt    time.Time    // rename to Guest at this time, unless identified
	enforceNick  string       // the nick enforceAt is about
	pwBusy       bool         // a password check is running
	saslLogin    bool         // login comes from SASL: 903 goes right after 900
	csUsedOn     string       // channel a ChanServ command passed its access check on (for LOG)
	lastMail     time.Time    // when services last mailed for this person (mail delay)
	lastMemoSend time.Time    // MemoServ SEND (senddelay)
	cmdName      string       // the command as typed, for log lines (Anope's source.command)
	vhostOn      bool         // a HostServ vhost is shown
	helpOn       bool         // had user mode +h at the last mode change (HelpServ)
	reply        func(string) // set for the Hive Panel's stand-in client: answers go here, not by notice
	logged       bool         // a command logged a change (the Hive Panel's stand-in reads it)
	webFlags     string       // the Hive Panel's stand-in for an oper: their oper flags
	webDone      func()       // the stand-in's command finished later (a password being hashed)
	superAdmin   bool         // OperServ SET SUPERADMIN: founder on every registered channel
	globalQueue  []string     // Global QUEUE: your lines, kept until you quit
}

// operFlags are c's oper flags: from the server for someone on IRC, from
// the oper entry for the Hive Panel's stand-in (it has no UID).
func (c *Client) operFlags() string {
	if c.reply != nil {
		return c.webFlags
	}
	return c.s.host.OperFlags(c.user.UID)
}

func (c *Client) isOper() bool    { return c.operFlags() != "" }
func (c *Client) can(f byte) bool { return strings.IndexByte(c.operFlags(), f) >= 0 }

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
		lockouts: make(map[lockKey]*lockout),
		resets:   make(map[int64]resetInfo),
		hashSem:  make(chan struct{}, 2),
		extSem:   make(chan struct{}, 8),

		banRegexps: make(map[string]*regexp.Regexp),
		seen:       make(map[string]*store.Seen),
		seenDirty:  make(map[string]bool),
		seenSaved:  time.Now(),
		off:        !cfg.Services.On(),
		now:        time.Now,
		hitCaps:    make(map[string]*hitCap),
	}
	if err := s.loadOperData(); err != nil {
		return nil, err
	}
	if err := s.loadSpamFilters(); err != nil {
		return nil, err
	}
	if err := s.loadSeen(); err != nil {
		return nil, err
	}
	return s, nil
}

// Event takes one applied network event. Services act on it after the
// command that caused it has finished (see Flush), so their notices and
// modes come after the server's own replies, as clients expect.
func (s *Services) Event(ev state.Event) {
	if s.off {
		return
	}
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
	s.seenEvent(ev)
	switch e := ev.(type) {
	case *state.UserAdd:
		if c := s.clients[e.User.UID]; c != nil && !c.closed {
			s.connected(c)
		}
	case *state.Away:
		// Back from /AWAY: SET NOTIFY LOGON tells about new memos (Anope).
		if c := s.clients[e.UID]; c != nil && !c.closed && e.Message == "" && c.accountID != 0 {
			if a, _ := s.store.AccountByID(c.accountID); a != nil {
				s.memoSummary(c, a)
			}
		}
	case *state.UserMode:
		s.logUserMode(e)
		if c := s.clients[e.UID]; c != nil && !c.closed {
			s.saveModes(c)
			s.isSuperAdmin(c) // de-opered: SUPERADMIN ends
			if on := c.user.HasMode('h'); on != c.helpOn {
				c.helpOn = on
				if on {
					s.helpOnDuty(c)
				}
			}
		}
	case *state.UserQuit:
		if c := s.clients[e.UID]; c != nil {
			s.saveModes(c)
			s.saveQuit(c, e.Reason)
			s.dropSuperAdmin(c, "quit")
			delete(s.clients, e.UID)
			s.buddyOffline(c.accountID)
			s.helpGone(e.UID)
		}
	case *state.NickChange:
		s.logNick(e)
		if c := s.clients[e.UID]; c != nil && !c.closed {
			s.nickCheck(c)
		}
	case *state.HostChange:
		s.logHost(e)
	case *state.Join:
		s.logJoin(e)
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
		s.logModes(e)
	case *state.ChannelTS:
		if ch := s.net.Channel(e.Channel); ch != nil {
			s.csLostTS(ch)
		}
	case *state.Topic:
		if ch := s.net.Channel(e.Channel); ch != nil && e.Source != s.chanServ.user.UID {
			s.csTopicChanged(ch)
		}
	case *state.Part:
		s.logPart(e.UID, e.Channel, true)
		s.bsForget(e.UID, e.Channel)
		s.bsTidy(e.Channel)
	case *state.Kick:
		s.logPart(e.UID, e.Channel, false)
		s.bsForget(e.UID, e.Channel)
		s.bsTidy(e.Channel)
	}
	if q, quit := ev.(*state.UserQuit); quit {
		s.bsForget(q.UID, "")
		s.bsTidy("")
	}
}

// connected runs once a new person is on the network: an account they
// logged in to with SASL first, then the nick check.
func (s *Services) connected(c *Client) {
	if s.defconNewUser(c) {
		return
	}
	if c.user.Account != "" {
		if a, err := s.store.AccountByNick(c.user.Account); err == nil && a != nil {
			c.accountID = a.ID
			a.LastSeen, a.LastHost = time.Now(), c.user.Ident+"@"+c.user.RealHost
			if err := s.store.SaveAccount(a); err != nil {
				s.log.Printf("services database: %v", err)
			}
			s.memoSummary(c, a)
			s.afterLogin(c, a)
			s.buddyOnline(c, a)
		}
	}
	s.certLogin(c)
	s.nickCheck(c)
}

// Tick runs the timers: identify deadlines every second. The full expiry
// pass runs hourly (the server decides when); ExpireDue drops timed bans
// as they run out.
func (s *Services) Tick(now time.Time) {
	if s.off {
		return
	}
	for _, c := range s.clients {
		if !c.closed && !c.enforceAt.IsZero() && now.After(c.enforceAt) {
			s.enforce(c)
		}
	}
	if now.Sub(s.seenSaved) >= seenSaveEach {
		s.SaveSeen()
	}
	s.flushHitCaps(now)
	s.Flush()
}

// ExpireDue drops timed bans, forbids, exceptions and spam filters as soon
// as the first one runs out, so the "expired" notice isn't up to an hour
// late. It is cheap to call every tick: nothing happens before the
// earliest expiry time.
func (s *Services) ExpireDue(now time.Time) {
	if !s.banDue.IsZero() && !now.Before(s.banDue) {
		s.expireBans(now)
		if !s.banDue.IsZero() && !now.Before(s.banDue) {
			s.banDue = now.Add(time.Minute) // nothing went (database trouble): try again later
		}
	}
	if !s.spam.due.IsZero() && !now.Before(s.spam.due) {
		s.expireSpamFilters(now)
		if !s.spam.due.IsZero() && !now.Before(s.spam.due) {
			s.spam.due = now.Add(time.Minute)
		}
	}
	s.Flush()
}

// Expire drops accounts, channels and AKILLs that ran out.
func (s *Services) Expire(now time.Time) {
	if s.off {
		s.expireBans(now)
		s.expireSpamFilters(now)
		return
	}
	s.expireAccountSuspends(now)
	s.expireAccounts(now)
	s.expireUnconfirmed(now)
	s.expireChannels(now)
	s.expireSuspends(now)
	s.expireBans(now)
	s.expireSpamFilters(now)
	s.expireSeen(now)
	s.liveIgnores()
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
func (s *Services) snotice(letter byte, format string, a ...any) {
	s.host.Snotice(letter, fmt.Sprintf(format, a...))
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
