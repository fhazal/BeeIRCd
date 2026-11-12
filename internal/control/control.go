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
)

// MaxRequest is the most bytes one request may be.
const MaxRequest = 4096

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
	IP       string `json:"ip,omitempty"`       // their web address
	Nick     string `json:"nick,omitempty"`     // panel-login
	Password string `json:"password,omitempty"` // panel-login
	Name     string `json:"name,omitempty"`     // set
	Value    string `json:"value,omitempty"`    // set
	ID       int64  `json:"id,omitempty"`       // undo

	Query  string `json:"query,omitempty"`  // people: search text
	Filter string `json:"filter,omitempty"` // people: all, in, guests, opers
	Offset int    `json:"offset,omitempty"`
	Limit  int    `json:"limit,omitempty"`
	UID    string `json:"uid,omitempty"`  // person, notice, kill, ban
	Life   string `json:"life,omitempty"` // ban: "7d", "0" = until removed, "" = default
}

// Response carries the answer to one Request; Error is set when it failed.
type Response struct {
	Error  string  `json:"error,omitempty"`
	Status *Status `json:"status,omitempty"`
	Check  *Check  `json:"check,omitempty"`
	Backup *Backup `json:"backup,omitempty"`
	Rehash *Rehash `json:"rehash,omitempty"`

	Oper     *PanelOper `json:"oper,omitempty"`
	Settings []Setting  `json:"settings,omitempty"`
	Changed  *Changed   `json:"changed,omitempty"`
	Events   []Event    `json:"events,omitempty"`
	People   *People    `json:"people,omitempty"`
	Person   *Person    `json:"person,omitempty"`
	Done     *Done      `json:"done,omitempty"`
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
	Since    time.Time `json:"since,omitempty"`
	LagMS    float64   `json:"lag_ms,omitempty"`
	Software string    `json:"software,omitempty"`
	Behind   int       `json:"behind,omitempty"` // servers behind it
	Error    string    `json:"error,omitempty"`  // last connect error
	Retry    time.Time `json:"retry,omitempty"`  // next autoconnect try
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
