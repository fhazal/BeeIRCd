// Package control is how the `beeircd` admin commands talk to the running
// server: one JSON request and one JSON answer over a unix socket that
// only the beeircd user and its admin group can open. There is no network
// port.
package control

import (
	"bufio"
	"encoding/json"
	"errors"
	"net"
	"time"
)

// Commands.
const (
	CmdStatus = "status"
	CmdCheck  = "check"
	CmdBackup = "backup"
	CmdRehash = "rehash"
	// CmdQuitReason sets the quit message for the next stop (Path holds
	// the text), so `beeircd update` can say why everyone is leaving.
	CmdQuitReason = "quit-reason"
	// CmdHandover asks the server to hand everyone over to a new server
	// process (the program file as it is on disk now) through the door
	// keeper, so nobody is disconnected. Only behind the door keeper
	// (Status.Gate is a version); an error otherwise.
	CmdHandover = "handover"

	// Hive Panel. Every panel request names the oper it acts for
	// (As) and the web address they come from (IP); the server checks that
	// oper again each time, so a removed or changed oper is stopped at once.
	CmdPanelLogin = "panel-login" // Nick + Password + IP -> Oper
	CmdPanelHello = "panel-hello" // -> Hello (no sign-in: what the sign-in page says)
	CmdPanelWho   = "panel-who"   // the oper's current flags -> Oper
	CmdSettings   = "settings"    // -> Settings (flag a)
	CmdClasses    = "classes"     // -> Classes (flag a)
	CmdSet        = "set"         // Name + Value -> Changed (flag a)
	CmdUndo       = "undo"        // ID (a HISTORY entry) -> Changed (flag a)
	CmdPanelLog   = "panel-log"   // -> Events

	// People page.
	CmdPeople = "people" // Query + Filter + Offset + Limit -> People
	CmdPerson = "person" // UID -> Person
	CmdNotice = "notice" // UID + Value (the text) -> Done
	CmdKill   = "kill"   // UID + Value (the reason) -> Done (flag k)
	CmdBan    = "ban"    // UID + Value (reason) + Life -> Done (flag k: OperServ AKILL)
	CmdWhere  = "where"  // -> Where (flag h; error when GeoIP is off)

	// CmdMetaClear deletes all IRCv3 metadata of a person (UID) or a
	// channel (Name); any oper, like METADATA CLEAR on IRC.
	CmdMetaClear = "meta-clear" // UID or Name -> Done

	// Bans page, all flag k like OperServ's ban commands.
	CmdBans     = "bans"      // Kind -> Bans
	CmdBanAdd   = "ban-add"   // Kind + Mask + Reason + Life -> Done
	CmdBanDel   = "ban-del"   // Kind + Mask -> Done
	CmdBanMatch = "ban-match" // Kind + Mask -> Match (who it would hit now)

	// Opers page, flag a like AdminServ OPER. Undo is CmdUndo.
	CmdOpers    = "opers"     // -> Opers
	CmdOperSave = "oper-save" // Name + Class + Flags + Hosts + Password + New -> Changed
	CmdOperDel  = "oper-del"  // Name -> Changed

	// Linked servers page, flag c like /CONNECT and /SQUIT.
	CmdLinks       = "links"        // -> Links
	CmdLinkConnect = "link-connect" // Name -> Done
	CmdLinkSplit   = "link-split"   // Name + Reason -> Done
	CmdLinkSave    = "link-save"    // Block + New -> Done
	CmdLinkDel     = "link-del"     // Name -> Done

	// Channels page.
	CmdChannels      = "channels"       // Query + Filter + Offset + Limit -> Channels
	CmdChannel       = "channel"        // Name -> ChannelInfo
	CmdChanNotice    = "chan-notice"    // Name + Value -> Done (flag g)
	CmdChanTopic     = "chan-topic"     // Name + Value -> Done (flag s)
	CmdChanRename    = "chan-rename"    // Name + Value (new name) + Reason -> Done (flag s)
	CmdChanSuspend   = "chan-suspend"   // Name + Reason + Life -> Done (flag s)
	CmdChanUnsuspend = "chan-unsuspend" // Name -> Done (flag s)

	// Backups card, owner only.
	CmdBackups   = "backups"    // -> Backups
	CmdBackupGet = "backup-get" // Name + Password -> Backup (the file the panel may send)

	// Network map.
	CmdMapPlace = "map-place" // Name (server) + Value (place name) + Lat + Lon -> Done; no Lat/Lon = remove (flag c)

	// Members (Hive Chat h1): anyone signed in with a NickServ account,
	// opers too. Their own account only.
	CmdMyNick  = "my-nick"  // -> MyNick
	CmdMySet   = "my-set"   // Name (NickServ SET option) + Value -> Done (NickServ's answer)
	CmdChatKey = "chat-key" // -> Chat (a one-time SASL key for BeeRoom, 60 s)
	CmdMyMeta  = "my-meta"  // Name (avatar, color, status, ...) + Value ("" removes) -> Done

	// Member Home and Memos (members and opers, their own account only).
	CmdMyHome   = "my-home"   // -> Home
	CmdMyMemos  = "my-memos"  // -> Memos
	CmdMemoRead = "memo-read" // ID (memo) -> Done; marks it read
	CmdMemoDel  = "memo-del"  // ID (memo) -> Done
	CmdMemoSend = "memo-send" // Name (nick or #channel) + Value (text) -> Done (MemoServ's answer)

	// Channel manager: one channel the person is on the access list of.
	CmdMyChan   = "my-chan"   // Name -> Chan
	CmdChanDo   = "chan-do"   // Name + Kind (ACCESS, AKICK, SET, TOPIC, DROP) + Value/Mask/Reason -> Done (ChanServ's answer)
	CmdChanMemo = "chan-memo" // Name + ID + Value ("read" or "del") -> Done

	// Oper pages that run the real services commands with the oper's flags.
	CmdGlobal   = "global"   // Value (the text) -> Done (flag g: OperServ GLOBAL)
	CmdAccounts = "accounts" // Query + Filter + Offset + Limit -> Accounts (flag s)
	CmdAccount  = "account"  // Name (a nick) -> Account (flag s)
	CmdSvcData  = "svc-data" // Kind (vhosts, bots, news, sessions, defcon) -> Svc
	CmdServDo   = "serv-do"  // Kind (the action) + Name + Value + Mask + Reason + Life + Password + ID -> Done
)

// MaxRequest is the most bytes one request may be (the Hive Panel takes
// requests of up to 16 KB from browsers and passes them on).
const MaxRequest = 16 << 10

// Request asks the server for one thing. For backup, Path "" makes a
// dated backup; any other value an undated one in the backup folder that
// the caller moves where it wants (the server never writes outside its
// backup folder: it runs locked down, and the socket must not become a
// way to write files anywhere).
type Request struct {
	Cmd  string `json:"cmd"`
	Path string `json:"path,omitempty"`

	// Hive Panel fields.
	As       string `json:"as,omitempty"`       // the signed-in oper's account
	Seal     string `json:"seal,omitempty"`     // PanelOper.Seal from sign-in
	IP       string `json:"ip,omitempty"`       // their web address
	Nick     string `json:"nick,omitempty"`     // panel-login
	Password string `json:"password,omitempty"` // panel-login
	Name     string `json:"name,omitempty"`     // set
	Value    string `json:"value,omitempty"`    // set
	ID       int64  `json:"id,omitempty"`       // undo

	Query  string     `json:"query,omitempty"`  // people: search text
	Filter string     `json:"filter,omitempty"` // people: all, in, guests, opers
	Offset int        `json:"offset,omitempty"`
	Limit  int        `json:"limit,omitempty"`
	UID    string     `json:"uid,omitempty"`  // person, notice, kill, ban
	Life   string     `json:"life,omitempty"` // ban: "7d", "0" = until removed, "" = default
	Kind   string     `json:"kind,omitempty"` // bans: akill, zline, shun, eline
	Mask   string     `json:"mask,omitempty"`
	Reason string     `json:"reason,omitempty"`
	Class  string     `json:"class,omitempty"` // oper-save
	Flags  string     `json:"flags,omitempty"` // oper-save: flags on top of the class
	Hosts  string     `json:"hosts,omitempty"` // oper-save: masks, space or comma between
	New    bool       `json:"new,omitempty"`   // oper-save, link-save: add, not change
	Reach  string     `json:"reach,omitempty"` // oper-save: home or network (network owner only); "" keeps it
	Block  *LinkBlock `json:"block,omitempty"` // link-save
	Lat    *float64   `json:"lat,omitempty"`   // map-place
	Lon    *float64   `json:"lon,omitempty"`   // map-place
	Mail   *MailForm  `json:"mail,omitempty"`  // mail-save, mail-test
}

// Response carries the answer to one Request; Error is set when it failed.
type Response struct {
	Error  string  `json:"error,omitempty"`
	Status *Status `json:"status,omitempty"`
	Check  *Check  `json:"check,omitempty"`
	Backup *Backup `json:"backup,omitempty"`
	Rehash *Rehash `json:"rehash,omitempty"`

	Oper        *PanelOper   `json:"oper,omitempty"`
	Settings    []Setting    `json:"settings,omitempty"`
	Changed     *Changed     `json:"changed,omitempty"`
	Events      []Event      `json:"events,omitempty"`
	People      *People      `json:"people,omitempty"`
	Person      *Person      `json:"person,omitempty"`
	Where       *Where       `json:"where,omitempty"`
	Done        *Done        `json:"done,omitempty"`
	Bans        *Bans        `json:"bans,omitempty"`
	Match       *Match       `json:"match,omitempty"`
	Opers       *Opers       `json:"opers,omitempty"`
	Links       *Links       `json:"links,omitempty"`
	Classes     *Classes     `json:"classes,omitempty"`
	Channels    *Channels    `json:"channels,omitempty"`
	Channel     *ChannelInfo `json:"channel,omitempty"`
	Backups     *BackupList  `json:"backups,omitempty"`
	MyNick      *MyNick      `json:"myNick,omitempty"`
	Hello       *Hello       `json:"hello,omitempty"`
	Chat        *ChatKey     `json:"chat,omitempty"`
	Home        *MyHome      `json:"home,omitempty"`
	Memos       *MyMemos     `json:"memos,omitempty"`
	Chan        *MyChan      `json:"chan,omitempty"`
	Spam        *Spam        `json:"spam,omitempty"`
	SpamCheck   *SpamCheck   `json:"spamCheck,omitempty"`
	SpamTest    *SpamTest    `json:"spamTest,omitempty"`
	SpamReports *SpamReports `json:"spamReports,omitempty"`
	Mail        *Mail        `json:"mail,omitempty"`
	MailTest    *MailTest    `json:"mailTest,omitempty"`
	Accounts    *Accounts    `json:"accounts,omitempty"`
	Account     *Account     `json:"account,omitempty"`
	Svc         *Svc         `json:"svc,omitempty"`
	// Denied is set (with Error) when the oper may not do this: signed
	// out, or missing a flag. The panel then signs them out or says so.
	Denied string `json:"denied,omitempty"` // "signin" or "flag"
}

// PanelOper is a signed-in oper as the panel shows them.
type PanelOper struct {
	Account string `json:"account"`
	Class   string `json:"class,omitempty"`
	Flags   string `json:"flags"`
	Owner   bool   `json:"owner,omitempty"`
	// Member is a sign-in with the NickServ password: only the member
	// pages (Chat, My nick, My channels), never the admin ones.
	Member bool `json:"member,omitempty"`
	// NoOperPass is set on a member session whose account is an oper with
	// no oper password yet, so Home can say how to get one: "owner" (the
	// first /oper from admin_hosts sets it), "owner-conf" (no admin_hosts:
	// admin_password in beeircd.conf) or "oper" (an admin sets it).
	NoOperPass string `json:"noOperPass,omitempty"`
	// Chat is BeeRoom's address ([web] chat_url) when the Chat tab is on.
	Chat string `json:"chat,omitempty"`
	// Seal changes when the oper password does; the panel keeps it from
	// sign-in and sends it back, so a new password ends old sessions. It
	// stays between the panel and the server (never sent to browsers).
	Seal string `json:"seal,omitempty"`
	// IP is the address the browser signed in from (filled by the panel).
	IP string `json:"ip,omitempty"`
}

// Setting is one setting the panel can change (AdminServ SET's list).
type Setting struct {
	Name  string `json:"name"`
	Table string `json:"table"`
	Key   string `json:"key"`
	Help  string `json:"help,omitempty"`
	Kind  string `json:"kind"` // number, time, onoff, text
	Min   int64  `json:"min,omitempty"`
	Max   int64  `json:"max,omitempty"`
	Value string `json:"value"`
	// Choices are the security groups (groups) or oper flags (flagchannels).
	Choices []string `json:"choices,omitempty"`
}

// People is one page of the People list.
type People struct {
	Online   int           `json:"online"` // everyone, not counting services
	LoggedIn int           `json:"logged_in"`
	Guests   int           `json:"guests"`
	Opers    int           `json:"opers"`
	Matched  int           `json:"matched"` // after search and filter
	Offset   int           `json:"offset"`
	Rows     []PersonBrief `json:"rows"`
	Geo      bool          `json:"geo,omitempty"` // the rows have country and network
}

// PersonBrief is one row of the People list.
type PersonBrief struct {
	UID      string    `json:"uid"`
	Nick     string    `json:"nick"`
	Host     string    `json:"host"` // the shown (often cloaked) host
	Server   string    `json:"server"`
	Account  string    `json:"account,omitempty"`
	Oper     bool      `json:"oper,omitempty"`
	Channels int       `json:"channels"`
	SignOn   time.Time `json:"signon"`
	// Country (ISO code) and network (ASN) from GeoIP, for opers with
	// flag h only.
	Country string `json:"country,omitempty"`
	ASN     uint32 `json:"asn,omitempty"`
	ASOrg   string `json:"as_org,omitempty"`
}

// Person is everything the panel shows about one person. RealHost and IP
// are only filled in for opers with flag h.
type Person struct {
	PersonBrief
	Ident    string    `json:"ident"`
	Realname string    `json:"realname"`
	RealHost string    `json:"real_host,omitempty"`
	IP       string    `json:"ip,omitempty"`
	TLS      bool      `json:"tls"`
	Away     string    `json:"away,omitempty"`
	Modes    string    `json:"modes"`
	Chans    []string  `json:"chans"`             // with their status (@#lobby)
	Idle     time.Time `json:"idle,omitzero"`     // last message (people on this server)
	OperAs   string    `json:"oper_as,omitempty"` // class or flags
	Local    bool      `json:"local"`             // on this server
	// CountryName is the country's English name (flag h, GeoIP on).
	CountryName string `json:"country_name,omitempty"`
	// Metadata is their IRCv3 metadata (avatar, color, status, ...).
	Metadata map[string]string `json:"metadata,omitempty"`
}

// Where is how many people connect from each country and network (GeoIP,
// flag h): the biggest first, at most whereMax of each.
type Where struct {
	Countries []Place `json:"countries"`
	Networks  []Place `json:"networks"`
	// MoreCountries/MoreNetworks count the people in the ones left out.
	MoreCountries int `json:"more_countries,omitempty"`
	MoreNetworks  int `json:"more_networks,omitempty"`
	// NoCountry/NoNetwork are people GeoIP knows nothing about (local
	// and gateway addresses).
	NoCountry int       `json:"no_country"`
	NoNetwork int       `json:"no_network"`
	Total     int       `json:"total"`
	Source    string    `json:"source"`         // the database ("DBIP-Country-Lite")
	Built     time.Time `json:"built,omitzero"` // when it was made
}

// Place is one country ("MY", "Malaysia") or network ("AS4788", its owner).
type Place struct {
	Code   string `json:"code"`
	Name   string `json:"name,omitempty"`
	People int    `json:"people"`
}

// Bans is the Bans page: how many of each kind, and the list of one.
type Bans struct {
	Counts  map[string]int `json:"counts"`
	Kind    string         `json:"kind"`
	Rows    []Ban          `json:"rows"`
	Default string         `json:"default"` // ban_expiry, like "30d"
}

// Ban is one network ban.
type Ban struct {
	Kind    string    `json:"kind"`
	Mask    string    `json:"mask"`
	Reason  string    `json:"reason"`
	SetBy   string    `json:"set_by"`
	SetAt   time.Time `json:"set_at"`
	Expires time.Time `json:"expires,omitzero"` // zero = until removed
}

// Match is what a ban would hit if added now.
type Match struct {
	Mask   string `json:"mask"`
	People int    `json:"people"`
}

// Opers is the Opers page.
type Opers struct {
	Rows    []OperRow   `json:"rows"`
	Classes []OperClass `json:"classes"`
	// OwnerView is set for the network owner: only they change Reach.
	OwnerView bool `json:"owner_view,omitempty"`
}

// OperRow is one oper entry. The owner (the [services] admin) comes first
// and can't be changed here.
type OperRow struct {
	Account  string    `json:"account"`
	Class    string    `json:"class,omitempty"`
	Flags    string    `json:"flags,omitempty"` // their own, on top of the class
	All      string    `json:"all"`             // class + own: what they can do
	Hosts    []string  `json:"hosts"`
	Title    string    `json:"title,omitempty"`
	Online   bool      `json:"online"`
	Password bool      `json:"password"` // an oper password is set
	Disabled string    `json:"disabled,omitempty"`
	Owner    bool      `json:"owner,omitempty"`
	Reach    string    `json:"reach,omitempty"` // "network": admin powers on every server; "" home
	AddedBy  string    `json:"added_by,omitempty"`
	AddedAt  time.Time `json:"added_at,omitzero"`
}

// OperClass is an oper class to pick.
type OperClass struct {
	Name  string `json:"name"`
	Flags string `json:"flags"`
}

// Classes is the Connect classes card on the Settings page: the [[class]]
// blocks in file order, then the default class last.
type Classes struct {
	Classes []ClassInfo `json:"classes"`
}

// ClassInfo is one connect class. Default is the class of everyone no
// [[class]] takes in (Groups is empty then). MaxUsers 0 means no maximum.
// Each limit carries the value that applies and whether the class set it
// itself (Own) or it comes from [limits].
type ClassInfo struct {
	Name         string     `json:"name"`
	Default      bool       `json:"default,omitempty"`
	Groups       []string   `json:"groups"`
	People       int        `json:"people"`
	MaxUsers     int        `json:"max_users"`
	Deny         bool       `json:"deny"`
	SessionLimit ClassLimit `json:"session_limit"` // connections per IP
	SendQ        ClassLimit `json:"sendq"`         // bytes
	PingInterval ClassLimit `json:"ping_interval"` // seconds
	FloodBurst   ClassLimit `json:"flood_burst"`
	FloodRate    ClassLimit `json:"flood_rate"`
	MaxChannels  ClassLimit `json:"max_channels"`
}

// ClassLimit is one limit of a class.
type ClassLimit struct {
	Value int  `json:"value"`
	Own   bool `json:"own"` // set by the class; false = from [limits]
}

// Links is the Linked servers page.
type Links struct {
	Role        string      `json:"role"` // standalone, hub, leaf
	Self        string      `json:"self"`
	Fingerprint string      `json:"fingerprint,omitempty"` // ours, for the other side's block
	LinkPort    int         `json:"link_port,omitempty"`
	Map         []MapServer `json:"map"`
	Blocks      []LinkBlock `json:"blocks"`
	Status      []Link      `json:"status"`
	Places      []MapPlace  `json:"places"`     // where the owner put the servers
	FindByIP    bool        `json:"find_by_ip"` // [server] find_place_by_ip
	// OutsideAdmins is [link] outside_admins: network or none.
	OutsideAdmins string `json:"outside_admins"`
}

// MapPlace is where a server is on the network map: set by the owner,
// never looked up.
type MapPlace struct {
	Server string  `json:"server"`
	Name   string  `json:"name"`
	Lat    float64 `json:"lat"`
	Lon    float64 `json:"lon"`
	// From is "" for a place set here ([[map.place]]), else where it came
	// from: "its time zone" or "its owner" (the server said so itself).
	From string `json:"from,omitempty"`
}

// MapServer is one server of the network, for the map.
type MapServer struct {
	Name   string  `json:"name"`
	SID    string  `json:"sid,omitempty"`
	Uplink string  `json:"uplink,omitempty"` // name of the server it hangs off ("" = us)
	Users  int     `json:"users"`
	LagMS  float64 `json:"lag_ms,omitempty"`
	Self   bool    `json:"self,omitempty"`
	Desc   string  `json:"desc,omitempty"`
}

// LinkBlock is one [[link.server]] block. Password is never sent to the
// panel; HasPassword says whether one is set, and a save with Password ""
// keeps it.
type LinkBlock struct {
	Name        string `json:"name"`
	Host        string `json:"host,omitempty"`
	Port        int    `json:"port,omitempty"`
	Fingerprint string `json:"fingerprint"`
	Autoconnect bool   `json:"autoconnect"`
	HasPassword bool   `json:"has_password,omitempty"`
	Password    string `json:"password,omitempty"`
	NoPassword  bool   `json:"no_password,omitempty"` // save: remove the password
	// Rule and RuleFor are the block's link rule, shown only: a save keeps
	// what beeircd.conf has.
	Rule    string `json:"rule,omitempty"`
	RuleFor string `json:"rule_for,omitempty"`
}

// Channels is one page of the Channels list.
type Channels struct {
	Open       int            `json:"open"`
	Registered int            `json:"registered"`
	Suspended  int            `json:"suspended"`
	Biggest    string         `json:"biggest,omitempty"`
	BiggestN   int            `json:"biggest_n,omitempty"`
	Matched    int            `json:"matched"`
	Offset     int            `json:"offset"`
	Rows       []ChannelBrief `json:"rows"`
}

// ChannelBrief is one row of the Channels list.
type ChannelBrief struct {
	Name       string `json:"name"`
	People     int    `json:"people"`
	Topic      string `json:"topic,omitempty"`
	Modes      string `json:"modes,omitempty"` // letters only, no key
	Registered bool   `json:"registered,omitempty"`
	Suspended  bool   `json:"suspended,omitempty"`
	Open       bool   `json:"open"`
}

// ChannelInfo is one channel opened in the list.
type ChannelInfo struct {
	ChannelBrief
	TopicBy      string    `json:"topic_by,omitempty"`
	TopicAt      time.Time `json:"topic_at,omitzero"`
	Limit        int       `json:"limit,omitempty"`
	HasKey       bool      `json:"has_key,omitempty"`
	Bans         int       `json:"bans"`
	Excepts      int       `json:"excepts"`
	Invites      int       `json:"invites"`
	Members      []string  `json:"members"` // ops first, with their status (@nick +nick nick)
	Founder      string    `json:"founder,omitempty"`
	RegisteredAt time.Time `json:"registered_at,omitzero"`
	LastUsed     time.Time `json:"last_used,omitzero"`
	Description  string    `json:"description,omitempty"`
	Mlock        string    `json:"mlock,omitempty"`
	SuspendedBy  string    `json:"suspended_by,omitempty"`
	SuspendWhy   string    `json:"suspend_why,omitempty"`
	SuspendTill  time.Time `json:"suspend_till,omitzero"`
	// Metadata is the channel's IRCv3 metadata (live, or kept by ChanServ).
	Metadata map[string]string `json:"metadata,omitempty"`
}

// Done is what an action did, in words.
type Done struct {
	Text string `json:"text"`
}

// Handover is how a hand-over to this server process went.
type Handover struct {
	Kept    int    `json:"kept"`              // people who stayed connected
	Of      int    `json:"of"`                // people on the server when it handed over
	Problem string `json:"problem,omitempty"` // why it didn't work: then nobody stayed
}

// ChatKey signs the Chat tab's BeeRoom in: SASL PLAIN with Account and
// Key, once, within a minute.
type ChatKey struct {
	URL     string `json:"url"`
	Account string `json:"account"`
	Key     string `json:"key"`
}

// MyHome is Member Home: the signed-in account at a glance. Every part is
// about that account only; detail lives on its own page.
type MyHome struct {
	Name       string        `json:"name"`
	VHost      string        `json:"vhost,omitempty"`
	Registered time.Time     `json:"registered"`
	Nicks      []string      `json:"nicks"`
	Unread     int           `json:"unread"`
	Kept       int           `json:"kept"`
	MemoLimit  int           `json:"memoLimit"`
	Memos      []MyMemo      `json:"memos"` // the newest few, newest first
	Channels   []HomeChannel `json:"channels"`
	OnMyLists  int           `json:"onMyLists"` // people on the lists of channels they founded
	Access     []HomeAccess  `json:"access"`    // newest additions they may see
	Conns      []MyConn      `json:"conns"`
	Needs      []HomeNeed    `json:"needs"`
	Users      int           `json:"users"` // the network, as LUSERS says
	ChanCount  int           `json:"chanCount"`
	Servers    int           `json:"servers"`
}

// HomeChannel is one of the member's channels as it is right now.
type HomeChannel struct {
	Name    string    `json:"name"`
	Level   string    `json:"level"`
	Founder bool      `json:"founder,omitempty"`
	Users   int       `json:"users"`
	Status  string    `json:"status,omitempty"` // "@", "+" ... one of their connections holds there
	TopicBy string    `json:"topicBy,omitempty"`
	TopicAt time.Time `json:"topicAt,omitempty"`
}

// HomeAccess is one access list or AKICK entry: who was added, by whom.
type HomeAccess struct {
	Channel string    `json:"channel"`
	Who     string    `json:"who"`
	Level   string    `json:"level"`
	By      string    `json:"by"`
	At      time.Time `json:"at"`
}

// MyConn is one connection identified to the account.
type MyConn struct {
	Nick  string    `json:"nick"`
	Since time.Time `json:"since"`
	Away  bool      `json:"away,omitempty"`
	Web   bool      `json:"web,omitempty"` // came through a web chat (WEBIRC)
}

// HomeNeed is one thing on "Needs you"; Page is where to fix it.
type HomeNeed struct {
	Text string `json:"text"`
	Hint string `json:"hint"`
	Page string `json:"page"`
}

// MyMemo is one memo in the member's own box.
type MyMemo struct {
	ID   int64     `json:"id"`
	From string    `json:"from"`
	Sent time.Time `json:"sent"`
	Text string    `json:"text"`
	Read bool      `json:"read,omitempty"`
}

// MyMemos is the Memos page: the whole box, oldest first like /ms list.
type MyMemos struct {
	Memos []MyMemo `json:"memos"`
	Limit int      `json:"limit"`
}

// MyChan is one channel in the channel manager. Lists are only filled
// when the person's level may see them (Can).
type MyChan struct {
	Name        string          `json:"name"`
	Level       string          `json:"level"`
	Founder     string          `json:"founder"`
	Successor   string          `json:"successor,omitempty"`
	Description string          `json:"description,omitempty"`
	URL         string          `json:"url,omitempty"`
	Registered  time.Time       `json:"registered"`
	Topic       string          `json:"topic,omitempty"`
	Users       int             `json:"users"`
	Can         ChanCan         `json:"can"`
	Give        []string        `json:"give,omitempty"`    // with Can.AccessChange: the levels they may give (below their own)
	Toggles     map[string]bool `json:"toggles,omitempty"` // with Can.Set: SET option -> on
	People      []ChanPerson    `json:"people"`
	Access      []ChanAccess    `json:"access,omitempty"`
	Akicks      []ChanAkick     `json:"akicks,omitempty"`
	Memos       []MyMemo        `json:"memos,omitempty"`
	MemoLimit   int             `json:"memoLimit,omitempty"`
	Stats       *ChanStatsView  `json:"stats,omitempty"` // only for someone who may see the channel's stats
}

// ChanStatsView is the Stats tab of a channel. Periods is only filled when
// On (ChanServ SET CHANSTATS), in the order day, week, month, total.
type ChanStatsView struct {
	On      bool              `json:"on"`
	Periods []ChanStatsPeriod `json:"periods,omitempty"`
}

// ChanStatsPeriod is one period's top list and totals. Key is day, week,
// month or total.
type ChanStatsPeriod struct {
	Key    string          `json:"key"`
	Top    []ChanStatsRow  `json:"top"`
	Totals ChanStatsTotals `json:"totals"`
}

// ChanStatsRow is one person in a top list.
type ChanStatsRow struct {
	Nick    string `json:"nick"`
	Letters int64  `json:"letters"`
	Words   int64  `json:"words"`
	Lines   int64  `json:"lines"`
	Smileys int64  `json:"smileys"`
	Actions int64  `json:"actions"`
	You     bool   `json:"you,omitempty"`
}

// ChanStatsTotals is what everyone counted wrote together.
type ChanStatsTotals struct {
	Lines   int64 `json:"lines"`
	Words   int64 `json:"words"`
	Letters int64 `json:"letters"`
	Smileys int64 `json:"smileys"`
	Actions int64 `json:"actions"`
	Kicks   int64 `json:"kicks"`
	Modes   int64 `json:"modes"`
	Topics  int64 `json:"topics"`
}

// ChanCan is what the person's ChanServ level allows on the channel.
type ChanCan struct {
	AccessList   bool `json:"accessList,omitempty"`
	AccessChange bool `json:"accessChange,omitempty"`
	Akick        bool `json:"akick,omitempty"`
	Set          bool `json:"set,omitempty"`
	Topic        bool `json:"topic,omitempty"`
	Memo         bool `json:"memo,omitempty"`
	Founder      bool `json:"founder,omitempty"`
}

// ChanPerson is someone in the channel now.
type ChanPerson struct {
	Nick   string `json:"nick"`
	Prefix string `json:"prefix,omitempty"`
	Away   bool   `json:"away,omitempty"`
}

// ChanAccess is one access list entry.
type ChanAccess struct {
	Nick   string    `json:"nick"`
	Level  string    `json:"level"`
	By     string    `json:"by,omitempty"`
	At     time.Time `json:"at"`
	Online bool      `json:"online,omitempty"`
	Locked bool      `json:"locked,omitempty"` // at or above the viewer's level: they can't change or remove it
}

// ChanAkick is one AKICK entry.
type ChanAkick struct {
	Mask   string    `json:"mask"`
	Reason string    `json:"reason,omitempty"`
	By     string    `json:"by,omitempty"`
	At     time.Time `json:"at"`
}

// Hello is what the sign-in page may know before anyone signs in.
type Hello struct {
	Members bool `json:"members"` // [web] members: NickServ accounts may sign in
}

// MyNick is a member's own NickServ account (My nick and My channels).
type MyNick struct {
	Name       string      `json:"name"`
	Registered time.Time   `json:"registered"`
	Email      string      `json:"email,omitempty"` // shown masked: b•••@example.org
	Nicks      []string    `json:"nicks"`           // the account's grouped nicks
	Kill       string      `json:"kill"`            // on, quick, immed or off (SET KILL)
	HideEmail  bool        `json:"hideEmail"`
	HideStatus bool        `json:"hideStatus"`
	AutoOp     bool        `json:"autoOp"`
	Private    bool        `json:"private"`
	ChanStats  bool        `json:"chanStats"` // SET CHANSTATS: what I say in channels with it on is counted
	Greet      string      `json:"greet,omitempty"`
	Channels   []MyChannel `json:"channels"`
	// Metadata is the IRCv3 metadata kept on the account (avatar, color,
	// status, ...); IRC apps that support it show it.
	Metadata map[string]string `json:"metadata,omitempty"`
	MetaOn   bool              `json:"metaOn"` // metadata is offered (ircv3_drafts on)
}

// MyChannel is one channel where the member is founder or on the list.
type MyChannel struct {
	Name  string `json:"name"`
	Level string `json:"level"` // Founder, Successor, SOP, AOP, HOP, VOP or a number
	Users int    `json:"users"` // in the channel now
}

// Changed is what one set or undo did.
type Changed struct {
	Name string `json:"name"`
	Old  string `json:"old"`
	New  string `json:"new"`
	ID   int64  `json:"id,omitempty"` // HISTORY entry, for undo
}

// Event is one line of "What happened".
type Event struct {
	At   time.Time `json:"at"`
	Who  string    `json:"who"`
	IP   string    `json:"ip,omitempty"`
	What string    `json:"what"`
}

// Port is one open port.
type Port struct {
	Address string `json:"address"`
	TLS     bool   `json:"tls"`
	Proxy   bool   `json:"proxy"`
	WS      bool   `json:"websocket,omitempty"`
}

// Spotlight is what the Hive Panel's Spotlight card tells, one story at a
// time. Every part is optional; none of them repeats another number.
type Spotlight struct {
	Room          *SpotRoom    `json:"room,omitempty"`     // busiest room right now
	Newest        *SpotMember  `json:"newest,omitempty"`   // newest account, last 7 days
	DayPeak       *SpotPeak    `json:"day_peak,omitempty"` // most people at once today
	Anniversaries []SpotMember `json:"anniversaries,omitempty"`
}

// SpotRoom is the channel where most people talked in the last 5 minutes
// (never a +s or +p channel).
type SpotRoom struct {
	Name    string `json:"name"`
	Talkers int    `json:"talkers"`
	Members int    `json:"members"`
	Topic   string `json:"topic,omitempty"`
}

// SpotMember is an account: when it registered, and for anniversaries how
// many years ago.
type SpotMember struct {
	Name  string    `json:"name"`
	Since time.Time `json:"since"`
	Years int       `json:"years,omitempty"`
}

// SpotPeak is the most people on this server at once today.
type SpotPeak struct {
	People int       `json:"people"`
	At     time.Time `json:"at"`
}

// Cert describes the certificate in use.
type Cert struct {
	Source   string    `json:"source"` // "Let's Encrypt", "own files", "self-signed"
	Names    []string  `json:"names"`
	NotAfter time.Time `json:"not_after"`
	RenewAt  time.Time `json:"renew_at,omitzero"`
	// RenewFailures and RenewError: Let's Encrypt renewals that failed in a
	// row, and why the last one did.
	RenewFailures int    `json:"renew_failures,omitempty"`
	RenewError    string `json:"renew_error,omitempty"`
}

// Safety is what the connection guards did today (server time).
type Safety struct {
	Throttled int `json:"throttled"`
	Zlined    int `json:"zlined"`
	DNSBLHits int `json:"dnsbl_hits"`
}

// Status is the `beeircd status` screen.
type Status struct {
	Version  string    `json:"version"`
	Started  time.Time `json:"started"`
	PID      int       `json:"pid"`
	Server   string    `json:"server"`
	Network  string    `json:"network"`
	Ports    []Port    `json:"ports"`
	LinkPort int       `json:"link_port,omitempty"` // TLS port for server links, 0 = none
	OS       string    `json:"os"`                  // like "linux"
	Arch     string    `json:"arch"`                // like "amd64"
	CPUs     int       `json:"cpus"`
	Online   int       `json:"online"`
	Peak     int       `json:"peak"`
	Channels int       `json:"channels"`
	Opers    int       `json:"opers"`
	Services []string  `json:"services"`
	// MemoryMB is what the Go runtime holds from the system.
	MemoryMB float64 `json:"memory_mb"`
	// MachineMB is the machine's total memory (0: unknown).
	MachineMB float64 `json:"machine_mb,omitempty"`
	LagAvgMS  float64 `json:"lag_avg_ms"`
	Lag99MS   float64 `json:"lag_99_ms"`
	DBBytes   int64   `json:"db_bytes"`
	// HistoryBytes and HistoryChannels: channel history in memory.
	HistoryBytes    int64  `json:"history_bytes"`
	HistoryChannels int    `json:"history_channels"`
	Cert            *Cert  `json:"cert,omitempty"`
	BackupDir       string `json:"backup_dir"`
	Safety          Safety `json:"safety"`
	// Gate is the door keeper's protocol version when the server runs
	// behind it (beeircd run on Linux), "off" when it runs without one.
	// Empty: a server too old to say (it can't hand people over).
	Gate string `json:"gate,omitempty"`
	// HandoverVersion is the hand-over snapshot format the server writes.
	// 0: a server too old to say, which writes version 1.
	HandoverVersion int `json:"handover_version,omitempty"`
	// Local is the people on this server (Online counts the network).
	Local int `json:"local"`
	// Handover says how this server process took over from the last one;
	// nil when it started fresh.
	Handover *Handover `json:"handover,omitempty"`
	// Update is a newer release the daily check found (nil: none).
	Update *UpdateInfo `json:"update,omitempty"`
	// Links are the server links (hub and leaf only).
	Links []Link `json:"links,omitempty"`

	// Hive Panel overview.
	MaxUsers   int       `json:"max_users"`
	OperNicks  []string  `json:"oper_nicks,omitempty"` // opers on this server
	Nicks      int       `json:"nicks"`                // registered
	RegChans   int       `json:"reg_channels"`
	Memos      int       `json:"memos"`
	LastBackup time.Time `json:"last_backup,omitzero"`
	ServicesOn bool      `json:"services_on"`
	Spotlight  Spotlight `json:"spotlight"`
}

// Link is one server link for the status screen.
type Link struct {
	Name     string    `json:"name"`
	SID      string    `json:"sid,omitempty"`
	State    string    `json:"state"` // up, linking, connecting, down
	Out      bool      `json:"out"`   // we connect (autoconnect) / we connected
	Since    time.Time `json:"since,omitzero"`
	LagMS    float64   `json:"lag_ms,omitempty"`
	Software string    `json:"software,omitempty"`
	Behind   int       `json:"behind,omitempty"` // servers behind it
	Error    string    `json:"error,omitempty"`  // last connect error
	Retry    time.Time `json:"retry,omitzero"`   // next autoconnect try
}

// Check is what only the running server can check: its own database
// (locked to everyone else) and the history it holds.
type Check struct {
	Nicks           int    `json:"nicks"`
	Channels        int    `json:"channels"`
	Memos           int    `json:"memos"`
	DBError         string `json:"db_error,omitempty"`
	HistoryChannels int    `json:"history_channels"`
}

// Backup is a finished backup.
type Backup struct {
	File  string   `json:"file"`
	Bytes int64    `json:"bytes"`
	Parts []string `json:"parts"`
}

// BackupList lists the dated backups in the backup folder, newest first.
type BackupList struct {
	Dir   string       `json:"dir"`
	Keep  int          `json:"keep"`
	Files []BackupFile `json:"files"`
}

// BackupFile is one dated backup. By is set for the ones made in the
// Hive Panel ("Bee"); the others came from `beeircd backup` or its timer.
type BackupFile struct {
	Name  string    `json:"name"`
	Made  time.Time `json:"made"`
	Bytes int64     `json:"bytes"`
	By    string    `json:"by,omitempty"`
}

// Rehash is what re-reading beeircd.conf did.
type Rehash struct {
	Applied      []string `json:"applied"`
	NeedsRestart []string `json:"needs_restart"`
}

// ErrNotRunning means nothing answers on the socket.
var ErrNotRunning = errors.New("the server is not running")

// Ask sends req to the server at socket and waits up to timeout.
func Ask(socket string, req Request, timeout time.Duration) (*Response, error) {
	conn, err := net.DialTimeout("unix", socket, 2*time.Second)
	if err != nil {
		return nil, ErrNotRunning
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(timeout))
	if err := json.NewEncoder(conn).Encode(req); err != nil {
		return nil, err
	}
	var resp Response
	if err := json.NewDecoder(bufio.NewReader(conn)).Decode(&resp); err != nil {
		return nil, err
	}
	if resp.Error != "" {
		return &resp, errors.New(resp.Error)
	}
	return &resp, nil
}

// UpdateInfo is a newer release than the running one.
type UpdateInfo struct {
	Version string `json:"version"`
	Date    string `json:"date"`
	How     string `json:"how"` // the command that installs it
}

// Accounts is People's Accounts list: registered nicks, online or not.
type Accounts struct {
	Total       int          `json:"total"`
	Online      int          `json:"online"`
	Suspended   int          `json:"suspended"`
	Unconfirmed int          `json:"unconfirmed"`
	Matched     int          `json:"matched"`
	Offset      int          `json:"offset"`
	Rows        []AccountRow `json:"rows"`
}

// AccountRow is one registered account.
type AccountRow struct {
	Name        string    `json:"name"`
	Email       string    `json:"email,omitempty"`
	Registered  time.Time `json:"registered"`
	LastSeen    time.Time `json:"last_seen,omitzero"`
	Nicks       int       `json:"nicks"`
	Online      int       `json:"online"`
	Suspended   bool      `json:"suspended,omitempty"`
	Oper        bool      `json:"oper,omitempty"`
	Unconfirmed bool      `json:"unconfirmed,omitempty"`
}

// Account is one account opened from the Accounts list.
type Account struct {
	AccountRow
	NickList      []string  `json:"nick_list"`
	LastHost      string    `json:"last_host,omitempty"`
	SuspendedBy   string    `json:"suspended_by,omitempty"`
	SuspendReason string    `json:"suspend_reason,omitempty"`
	SuspendedAt   time.Time `json:"suspended_at,omitzero"`
	SuspendUntil  time.Time `json:"suspend_until,omitzero"` // zero = until lifted
	VHost         string    `json:"vhost,omitempty"`
	Founder       []string  `json:"founder"`
	NoExpire      bool      `json:"no_expire,omitempty"`
	// Locked says why the actions are off: the owner's account, or an
	// oper's (NickServ won't change those from here), "" when they're on.
	Locked string `json:"locked,omitempty"`
}

// Svc is one tab of the Services page.
type Svc struct {
	Kind string `json:"kind"`

	Waiting []VHostRow `json:"waiting,omitempty"` // vhosts
	Active  []VHostRow `json:"active,omitempty"`

	Bots []BotRow `json:"bots,omitempty"` // bots

	News []NewsList `json:"news,omitempty"` // news

	SessionLimit int             `json:"session_limit"` // sessions
	Busiest      []SessionRow    `json:"busiest,omitempty"`
	Excepts      []SessionExcept `json:"excepts,omitempty"`

	Defcon  int         `json:"defcon,omitempty"` // defcon: 5 = normal
	Levels  []string    `json:"levels,omitempty"` // what levels 1-5 do
	Ignores []IgnoreRow `json:"ignores,omitempty"`
	// CanDefcon and CanIgnore say what the DEFCON tab may change (flags a, k).
	CanDefcon bool `json:"can_defcon,omitempty"`
	CanIgnore bool `json:"can_ignore,omitempty"`
}

// VHostRow is a vhost in use or one waiting for an oper.
type VHostRow struct {
	Nick    string    `json:"nick"`
	VHost   string    `json:"vhost"`
	Creator string    `json:"creator,omitempty"`
	Created time.Time `json:"created"`
}

// BotRow is one BotServ bot.
type BotRow struct {
	Nick     string    `json:"nick"`
	User     string    `json:"user"`
	Host     string    `json:"host"`
	Real     string    `json:"real"`
	Created  time.Time `json:"created"`
	Private  bool      `json:"private,omitempty"`
	Channels int       `json:"channels"`
}

// NewsList is one kind of news: logon, oper or random.
type NewsList struct {
	Kind  string     `json:"kind"`
	About string     `json:"about"`
	Max   int        `json:"max"`
	Items []NewsItem `json:"items"`
}

// NewsItem is one news line.
type NewsItem struct {
	ID    int64     `json:"id"`
	Text  string    `json:"text"`
	SetBy string    `json:"set_by"`
	SetAt time.Time `json:"set_at"`
}

// SessionRow is an address and how many connections it has.
type SessionRow struct {
	IP    string `json:"ip"`
	Count int    `json:"count"`
	Limit int    `json:"limit"`
}

// SessionExcept lets an address have more (or fewer) connections.
type SessionExcept struct {
	Mask    string    `json:"mask"`
	Limit   int       `json:"limit"`
	Reason  string    `json:"reason"`
	SetBy   string    `json:"set_by"`
	SetAt   time.Time `json:"set_at"`
	Expires time.Time `json:"expires,omitzero"`
}

// IgnoreRow is someone services ignore.
type IgnoreRow struct {
	Mask    string    `json:"mask"`
	Reason  string    `json:"reason"`
	SetBy   string    `json:"set_by"`
	SetAt   time.Time `json:"set_at"`
	Expires time.Time `json:"expires,omitzero"`
}
