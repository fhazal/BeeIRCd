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
}

// Response carries the answer to one Request; Error is set when it failed.
type Response struct {
	Error  string  `json:"error,omitempty"`
	Status *Status `json:"status,omitempty"`
	Check  *Check  `json:"check,omitempty"`
	Backup *Backup `json:"backup,omitempty"`
	Rehash *Rehash `json:"rehash,omitempty"`
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
