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

	// Hive Panel (Stage 3). Every panel request names the oper it acts for
	// (As) and the web address they come from (IP); the server checks that
	// oper again each time, so a removed or changed oper is stopped at once.
	CmdPanelLogin = "panel-login" // Nick + Password + IP -> Oper
	CmdPanelWho   = "panel-who"   // the oper's current flags -> Oper
	CmdSettings   = "settings"    // -> Settings (flag a)
	CmdSet        = "set"         // Name + Value -> Changed (flag a)
	CmdUndo       = "undo"        // ID (a HISTORY entry) -> Changed (flag a)
	CmdPanelLog   = "panel-log"   // -> Events

	// People page (3b).
	CmdPeople = "people" // Query + Filter + Offset + Limit -> People
	CmdPerson = "person" // UID -> Person
	CmdNotice = "notice" // UID + Value (the text) -> Done
	CmdKill   = "kill"   // UID + Value (the reason) -> Done (flag k)
	CmdBan    = "ban"    // UID + Value (reason) + Life -> Done (flag k: OperServ AKILL)

	// Bans page (3c), all flag k like OperServ's ban commands.
	CmdBans     = "bans"      // Kind -> Bans
	CmdBanAdd   = "ban-add"   // Kind + Mask + Reason + Life -> Done
	CmdBanDel   = "ban-del"   // Kind + Mask -> Done
	CmdBanMatch = "ban-match" // Kind + Mask -> Match (who it would hit now)

	// Opers page (3d), flag a like AdminServ OPER. Undo is CmdUndo.
	CmdOpers    = "opers"     // -> Opers
	CmdOperSave = "oper-save" // Name + Class + Flags + Hosts + Password + New -> Changed
	CmdOperDel  = "oper-del"  // Name -> Changed

	// Linked servers page (3e), flag c like /CONNECT and /SQUIT.
	CmdLinks       = "links"        // -> Links
	CmdLinkConnect = "link-connect" // Name -> Done
	CmdLinkSplit   = "link-split"   // Name + Reason -> Done
	CmdLinkSave    = "link-save"    // Block + New -> Done
	CmdLinkDel     = "link-del"     // Name -> Done

	// Channels page (3h).
	CmdChannels      = "channels"       // Query + Filter + Offset + Limit -> Channels
	CmdChannel       = "channel"        // Name -> ChannelInfo
	CmdChanNotice    = "chan-notice"    // Name + Value -> Done (flag g)
	CmdChanTopic     = "chan-topic"     // Name + Value -> Done (flag s)
	CmdChanSuspend   = "chan-suspend"   // Name + Reason + Life -> Done (flag s)
	CmdChanUnsuspend = "chan-unsuspend" // Name -> Done (flag s)

	// Backups card (3i), owner only.
	CmdBackups   = "backups"    // -> Backups
	CmdBackupGet = "backup-get" // Name + Password -> Backup (the file the panel may send)

	// Network map (3k).
	CmdMapPlace = "map-place" // Name (server) + Value (place name) + Lat + Lon -> Done; no Lat/Lon = remove (flag c)
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
	Block  *LinkBlock `json:"block,omitempty"` // link-save
	Lat    *float64   `json:"lat,omitempty"`   // map-place
	Lon    *float64   `json:"lon,omitempty"`   // map-place
}

// Response carries the answer to one Request; Error is set when it failed.
type Response struct {
	Error  string  `json:"error,omitempty"`
	Status *Status `json:"status,omitempty"`
	Check  *Check  `json:"check,omitempty"`
	Backup *Backup `json:"backup,omitempty"`
	Rehash *Rehash `json:"rehash,omitempty"`

	Oper     *PanelOper   `json:"oper,omitempty"`
	Settings []Setting    `json:"settings,omitempty"`
	Changed  *Changed     `json:"changed,omitempty"`
	Events   []Event      `json:"events,omitempty"`
	People   *People      `json:"people,omitempty"`
	Person   *Person      `json:"person,omitempty"`
	Done     *Done        `json:"done,omitempty"`
	Bans     *Bans        `json:"bans,omitempty"`
	Match    *Match       `json:"match,omitempty"`
	Opers    *Opers       `json:"opers,omitempty"`
	Links    *Links       `json:"links,omitempty"`
	Channels *Channels    `json:"channels,omitempty"`
	Channel  *ChannelInfo `json:"channel,omitempty"`
	Backups  *BackupList  `json:"backups,omitempty"`
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
	// Seal changes when the oper password does; the panel keeps it from
	// sign-in and sends it back, so a new password ends old sessions. It
	// stays between the panel and the server (never sent to browsers).
	Seal string `json:"seal,omitempty"`
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
	AddedBy  string    `json:"added_by,omitempty"`
	AddedAt  time.Time `json:"added_at,omitzero"`
}

// OperClass is an oper class to pick.
type OperClass struct {
	Name  string `json:"name"`
	Flags string `json:"flags"`
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
	Places      []MapPlace  `json:"places"`     // where the owner put the servers (3k)
	FindByIP    bool        `json:"find_by_ip"` // [server] find_place_by_ip (3l)
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
}

// Done is what an action did, in words.
type Done struct {
	Text string `json:"text"`
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
	Online   int       `json:"online"`
	Peak     int       `json:"peak"`
	Channels int       `json:"channels"`
	Opers    int       `json:"opers"`
	Services []string  `json:"services"`
	// MemoryMB is what the Go runtime holds from the system.
	MemoryMB float64 `json:"memory_mb"`
	LagAvgMS float64 `json:"lag_avg_ms"`
	Lag99MS  float64 `json:"lag_99_ms"`
	DBBytes  int64   `json:"db_bytes"`
	// HistoryBytes and HistoryChannels: channel history in memory.
	HistoryBytes    int64  `json:"history_bytes"`
	HistoryChannels int    `json:"history_channels"`
	Cert            *Cert  `json:"cert,omitempty"`
	BackupDir       string `json:"backup_dir"`
	Safety          Safety `json:"safety"`
	// Update is a newer release the daily check found (nil: none).
	Update *UpdateInfo `json:"update,omitempty"`
	// Links are the server links (hub and leaf only; Stage 2).
	Links []Link `json:"links,omitempty"`

	// Stage 3 (Hive Panel overview).
	MaxUsers   int       `json:"max_users"`
	OperNicks  []string  `json:"oper_nicks,omitempty"` // opers on this server
	Nicks      int       `json:"nicks"`                // registered
	RegChans   int       `json:"reg_channels"`
	Memos      int       `json:"memos"`
	LastBackup time.Time `json:"last_backup,omitzero"`
	ServicesOn bool      `json:"services_on"`
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
}
