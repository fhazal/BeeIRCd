package server

// The control socket behind `beeircd status`, `check`, `backup`
// and `rehash`, plus REHASH itself and the safety counters for status.

import (
	"github.com/fhazal/beeircd/internal/acmecert"

	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/fhazal/beeircd/internal/config"
	"github.com/fhazal/beeircd/internal/control"
	"github.com/fhazal/beeircd/internal/gate"
	"github.com/fhazal/beeircd/internal/history"
	"github.com/fhazal/beeircd/internal/irc"
	"github.com/fhazal/beeircd/internal/version"
)

// safetyState counts what the connection guards did today.
type safetyState struct {
	day string
	control.Safety
}

func (s *Server) safety() *control.Safety {
	day := time.Now().Format("2006-01-02")
	if s.safe.day != day {
		s.safe = safetyState{day: day}
	}
	return &s.safe.Safety
}

// openControl opens the control socket. A socket file left behind by a
// server that died is removed; one that answers means another BeeIRCd runs.
func (s *Server) openControl() error {
	path := s.cfg.Control.Socket
	if path == "" {
		return nil
	}
	if _, err := os.Stat(path); err == nil {
		if c, err := net.DialTimeout("unix", path, time.Second); err == nil {
			c.Close()
			return fmt.Errorf("control socket %s: another BeeIRCd is answering there", path)
		}
		os.Remove(path)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return err
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		return fmt.Errorf("control socket %s: %w", path, err)
	}
	os.Chmod(path, 0o660) // the beeircd user and its admin group
	s.ctl = ln
	return nil
}

func (s *Server) closeControl() {
	if s.ctl != nil {
		s.ctl.Close()
		os.Remove(s.cfg.Control.Socket)
	}
}

func (s *Server) controlLoop() {
	defer s.wg.Done()
	for {
		conn, err := s.ctl.Accept()
		if err != nil {
			return
		}
		s.wg.Add(1)
		go s.controlConn(conn)
	}
}

// onLoop runs f on the loop and waits for it; false when the server is
// stopping.
func (s *Server) onLoop(f func()) bool {
	done := make(chan struct{})
	select {
	case s.events <- func() { f(); close(done) }:
	case <-s.done:
		return false
	}
	select {
	case <-done:
		return true
	case <-s.done:
		return false
	}
}

func (s *Server) controlConn(conn net.Conn) {
	defer s.wg.Done()
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(5 * time.Second))
	var req control.Request
	line, err := bufio.NewReader(io.LimitReader(conn, control.MaxRequest)).ReadBytes('\n')
	if err != nil || json.Unmarshal(line, &req) != nil {
		json.NewEncoder(conn).Encode(control.Response{Error: "bad request"})
		return
	}
	if r, ok := s.panelControl(req, func(d time.Duration) { conn.SetDeadline(time.Now().Add(d)) }); ok {
		json.NewEncoder(conn).Encode(r)
		return
	}
	var resp control.Response
	stopping := func() { resp = control.Response{Error: "the server is stopping"} }
	switch req.Cmd {
	case control.CmdStatus:
		if !s.onLoop(func() { resp.Status = s.statusInfo() }) {
			stopping()
		}
	case control.CmdCheck:
		if !s.onLoop(func() { resp.Check = s.checkInfo() }) {
			stopping()
		}
	case control.CmdQuitReason:
		reason := strings.TrimSpace(req.Path)
		if len(reason) > 200 {
			reason = reason[:200]
		}
		if !s.onLoop(func() { s.quitReason = reason }) {
			stopping()
		}
	case control.CmdHandover:
		if err := s.RequestHandover(); err != nil {
			resp.Error = err.Error()
		} else {
			resp.Done = &control.Done{Text: "handing over"}
		}
	case control.CmdRehash:
		var err error
		if !s.onLoop(func() { resp.Rehash, err = s.rehash("beeircd rehash") }) {
			stopping()
		} else if err != nil {
			resp.Error = err.Error()
		}
	case control.CmdBackup:
		conn.SetDeadline(time.Now().Add(10 * time.Minute))
		b, err := s.backup(req.Path)
		if err != nil {
			resp.Error = err.Error()
		}
		resp.Backup = b
	default:
		resp.Error = "unknown command " + req.Cmd
	}
	json.NewEncoder(conn).Encode(resp)
}

// statusInfo is the status screen's numbers (on the loop).
func (s *Server) statusInfo() *control.Status {
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	total, _, opers := s.net.PeopleCounts()
	st := &control.Status{
		Version:         version.Version,
		Started:         s.created,
		PID:             os.Getpid(),
		Server:          s.cfg.Server.Name,
		Network:         s.cfg.Server.Network,
		OS:              runtime.GOOS,
		Arch:            runtime.GOARCH,
		CPUs:            runtime.NumCPU(),
		Online:          total,
		Peak:            s.maxLocal,
		Channels:        s.net.ChannelCount(),
		Opers:           opers,
		Services:        s.svc.BotNicks(),
		MemoryMB:        float64(ms.Sys-ms.HeapReleased) / (1 << 20),
		MachineMB:       machineMB(),
		LagAvgMS:        float64(s.loop.average()) / float64(time.Millisecond),
		Lag99MS:         float64(s.loop.percentile(0.99)) / float64(time.Millisecond),
		DBBytes:         fileSize(s.cfg.Services.Database) + fileSize(s.cfg.Services.Database+"-wal"),
		HistoryBytes:    s.hist.store.Bytes(),
		HistoryChannels: s.hist.store.Channels(),
		BackupDir:       s.cfg.Backup.Dir,
		Safety:          *s.safety(),
		Update:          s.upd.latest,
		Links:           s.linkStatus(),
		MaxUsers:        s.cfg.Limits.MaxUsers,
		ServicesOn:      !s.svc.Off(),
	}
	st.Gate, st.Local, st.Handover = "off", s.localUsers(), s.hand.took
	st.HandoverVersion = handoverVersion
	if s.gate != nil {
		st.Gate = gate.Proto
	}
	sum := s.store.Summary()
	st.Nicks, st.RegChans, st.Memos = sum.Nicks, sum.Channels, sum.Memos
	st.Spotlight = s.spotlight(time.Now())
	if t, ok := control.LastBackup(s.cfg.Backup.Dir); ok {
		st.LastBackup = t
	}
	for c := range s.opers {
		if c.user != nil {
			st.OperNicks = append(st.OperNicks, c.user.Nick)
		}
	}
	sort.Strings(st.OperNicks)
	s.lmu.Lock()
	for _, l := range s.listeners {
		st.Ports = append(st.Ports, control.Port{Address: l.addr, TLS: l.tls, Proxy: l.proxy, WS: l.websocket})
	}
	s.lmu.Unlock()
	if s.links.ln != nil {
		st.LinkPort = s.cfg.Link.Port
	}
	if s.certs != nil {
		leaf := s.certs.Leaf()
		src := "own files"
		if s.cfg.TLS.SelfSigned {
			src = "self-signed"
		}
		st.Cert = &control.Cert{Source: src, Names: leaf.DNSNames, NotAfter: leaf.NotAfter}
		if s.cfg.TLS.ACME {
			st.Cert.Source = "Let's Encrypt"
			if s.realCert() {
				st.Cert.RenewAt = acmecert.RenewAt(leaf)
			} else {
				st.Cert.Source = "Let's Encrypt (waiting; self-signed stand-in)"
			}
			st.Cert.RenewFailures, st.Cert.RenewError = s.acme.fails, s.acme.lastErr
		}
	}
	return st
}

func fileSize(p string) int64 {
	if fi, err := os.Stat(p); err == nil {
		return fi.Size()
	}
	return 0
}

// checkInfo checks what only the running server can: its own database.
func (s *Server) checkInfo() *control.Check {
	sum := s.store.Summary()
	ck := &control.Check{Nicks: sum.Nicks, Channels: sum.Channels, Memos: sum.Memos, HistoryChannels: s.hist.store.Channels()}
	if err := s.store.Check(); err != nil {
		ck.DBError = err.Error()
	}
	return ck
}

// backup writes one .tar.gz: beeircd.conf, a live copy of services.db,
// the channel history and the certificate. The database copy and the
// history snapshot are taken on the loop; packing runs here, off it.
func (s *Server) backup(dst string) (*control.Backup, error) {
	var (
		tmp, conf, cert, key, secret string
		linkCert, linkKey            string
		snap                         history.Snapshot
		keep                         int
		dir                          string
		dbErr                        error
	)
	if !s.onLoop(func() {
		dir, keep = s.cfg.Backup.Dir, s.cfg.Backup.Keep
		conf, cert, key, secret = s.cfg.Path, s.cfg.TLS.Cert, s.cfg.TLS.Key, s.cfg.History.Secret
		if s.cfg.Link.Linked() {
			linkCert, linkKey = s.cfg.Link.Cert, s.cfg.Link.Key
		}
		if err := os.MkdirAll(dir, 0o700); err != nil {
			dbErr = err
			return
		}
		tmp, dbErr = os.MkdirTemp(dir, ".backup-")
		if dbErr != nil {
			return
		}
		dbErr = s.store.BackupTo(filepath.Join(tmp, "services.db"))
		snap = s.historySnapshot()
	}) {
		return nil, errors.New("the server is stopping")
	}
	if tmp != "" {
		defer os.RemoveAll(tmp)
	}
	if dbErr != nil {
		return nil, fmt.Errorf("copying the services database: %w", dbErr)
	}
	files := []control.File{
		{Name: "beeircd.conf", Path: conf, Part: "config"},
		{Name: "services.db", Path: filepath.Join(tmp, "services.db"), Part: "services database (live copy)"},
	}
	if snap != nil {
		hp := filepath.Join(tmp, "history.db")
		if err := history.Save(hp, secret, snap); err != nil {
			return nil, fmt.Errorf("saving channel history: %w", err)
		}
		files = append(files, control.File{Name: "history.db", Path: hp, Part: "channel history"})
	}
	if cert != "" {
		files = append(files,
			control.File{Name: "tls/" + filepath.Base(cert), Path: cert, Part: "certificate"},
			control.File{Name: "tls/" + filepath.Base(key), Path: key, Part: "certificate"})
	}
	// The link certificate is this server's identity to the others: a new
	// one means every linked server has to pin the new fingerprint.
	if linkCert != "" && fileSize(linkCert) > 0 && fileSize(linkKey) > 0 {
		files = append(files,
			control.File{Name: "link/" + filepath.Base(linkCert), Path: linkCert, Part: "link certificate"},
			control.File{Name: "link/" + filepath.Base(linkKey), Path: linkKey, Part: "link certificate"})
	}
	dated := dst == ""
	if dated {
		dst = control.FreeBackupName(dir, time.Now())
	} else {
		// Undated: the caller moves it where it wants. Never a path from
		// the request; Prune and LastBackup skip this name.
		dst = filepath.Join(dir, fmt.Sprintf(".named-%d.tar.gz", time.Now().UnixNano()))
	}
	if _, err := os.Stat(dst); err == nil {
		return nil, fmt.Errorf("%s exists already", dst)
	}
	n, parts, err := control.WriteArchive(dst, files)
	if err != nil {
		return nil, err
	}
	if dated {
		control.Prune(dir, keep)
	}
	s.post(func() { s.log.Printf("backup written to %s (%d bytes)", dst, n) })
	return &control.Backup{File: dst, Bytes: n, Parts: parts}, nil
}

// ---- REHASH ----

// restartOnly says whether a changed setting waits for a restart.
func restartOnly(ch config.Change) bool {
	switch ch.Section {
	case "":
		return ch.Key == "ports"
	case "server":
		return !serverLive[ch.Key]
	case "cloak", "tls", "control":
		return true
	case "rpc":
		return ch.Key != "user" // [[rpc.user]] blocks apply live
	case "link":
		return ch.Key != "server" // [[link.server]] blocks apply live
	case "services":
		return ch.Key == "database"
	case "history":
		return ch.Key == "file" || ch.Key == "secret"
	}
	return false
}

// panelRestartOnly says whether a changed setting is one the Hive Panel
// (beeircd-web) reads only when it starts: all of [web] but members, which
// the server answers for. chat_url the server uses at once too, but the
// panel lets only the old address show in its Chat tab until it restarts.
func panelRestartOnly(ch config.Change) bool {
	return ch.Section == "web" && ch.Key != "members"
}

// serverLive are the [server] settings a rehash applies at once: they are
// read each time they are used. The rest (name, SID, cloaking...) is
// fixed while the server runs.
var serverLive = map[string]bool{
	"description": true, "resolve_hosts": true, "hide_servers": true, "connect_modes": true,
	"oper_modes": true, "oper_join": true, "oper_join_flag": true, "auto_join": true, "auto_join_skip": true, "ping_cookie": true, "web_redirect": true,
	"oper_tag": true,
}

// keepRestartOnly copies the settings that need a restart from old into
// fresh, so applying fresh changes only what is safe live.
func keepRestartOnly(fresh, old *config.Config) {
	fresh.Listen = old.Listen
	live := fresh.Server
	fresh.Server = old.Server
	fresh.Server.Description, fresh.Server.ResolveHosts = live.Description, live.ResolveHosts
	fresh.Server.HideServers, fresh.Server.ConnectModes = live.HideServers, live.ConnectModes
	fresh.Server.OperModes, fresh.Server.OperJoin = live.OperModes, live.OperJoin
	fresh.Server.PingCookie, fresh.Server.WebRedirect = live.PingCookie, live.WebRedirect
	fresh.Server.OperTag = live.OperTag
	fresh.Server.AutoJoin, fresh.Server.AutoJoinSkip, fresh.Server.OperJoinFlag = live.AutoJoin, live.AutoJoinSkip, live.OperJoinFlag
	fresh.Cloak, fresh.TLS, fresh.Control = old.Cloak, old.TLS, old.Control
	fresh.RPC.Listen, fresh.RPC.Socket = old.RPC.Listen, old.RPC.Socket
	servers := fresh.Link.Servers
	fresh.Link = old.Link
	fresh.Link.Servers = servers
	fresh.Services.Database = old.Services.Database
	fresh.History.File, fresh.History.Secret = old.History.File, old.History.Secret
}

// rehash re-reads beeircd.conf (on the loop). A file that doesn't pass the
// checks is refused and nothing changes.
func (s *Server) rehash(by string) (*control.Rehash, error) {
	if s.cfg.Path == "" {
		return nil, errors.New("this server wasn't started from a config file")
	}
	s.sno('r', "%s is rehashing beeircd.conf on %s", by, s.cfg.Server.Name)
	fresh, err := config.Load(s.cfg.Path)
	if err != nil {
		return nil, fmt.Errorf("beeircd.conf not used: %w", err)
	}
	old := *s.cfg
	out := &control.Rehash{}
	for _, ch := range config.Diff(&old, fresh) {
		if panelRestartOnly(ch) {
			out.NeedsRestart = append(out.NeedsRestart, ch.String()+" (restart beeircd-web)")
		} else if restartOnly(ch) {
			out.NeedsRestart = append(out.NeedsRestart, ch.String())
		} else {
			out.Applied = append(out.Applied, ch.String())
		}
	}
	keepRestartOnly(fresh, &old)
	*s.cfg = *fresh // services share this Config
	s.reclass()     // [[class]] blocks may have changed: nobody is dropped for it
	s.retick()      // and may ping more often
	if old.Server.OperTag != fresh.Server.OperTag {
		s.resendOperLooks()
	}
	if old.Services.LogChannel != fresh.Services.LogChannel {
		s.svc.SyncLogChannel(old.Services.LogChannel)
	}
	s.svc.SyncLogFile() // a new path, or try again after an error
	s.linkRehash()
	s.startUpdateCheck() // switched on now; the running check reads [update] each time
	if !samePlace(old.Map.Place(old.Server.Name), fresh.Map.Place(fresh.Server.Name)) || old.FindPlaceByIP() != fresh.FindPlaceByIP() {
		s.startPlaceLookup() // the owner's place may be gone: find one from the IP
		s.announcePlace()
	}
	clear(s.floodCache)
	s.hist.store.SetMax(int64(fresh.History.MaxMemoryMB) << 20)
	s.net.NewChannelParams = nil
	if d := fresh.History.Default; d != "" {
		lines, age, _ := config.ParseHistoryMode(d)
		s.net.NewChannelParams = map[byte]string{'H': s.clampHistory(lines, age, false)}
	}
	if s.certs != nil {
		changed, err := s.certs.ReloadIfChanged()
		switch {
		case err != nil:
			out.NeedsRestart = append(out.NeedsRestart, "certificate: "+err.Error()+" (the old one stays)")
		case changed:
			out.Applied = append(out.Applied, "certificate read again")
			s.certGen++
		}
	}
	s.syncGate() // the certificate and handshake time limit, behind the gate
	s.sno('r', "Rehash done: %s", rehashSummary(out))
	return out, nil
}

func rehashSummary(r *control.Rehash) string {
	text := "nothing changed"
	if len(r.Applied) > 0 {
		text = "applied " + strings.Join(r.Applied, ", ")
	}
	if len(r.NeedsRestart) > 0 {
		text += "; waits for a restart: " + strings.Join(r.NeedsRestart, ", ")
	}
	return text
}

// Rehash re-reads the config from outside the loop (SIGHUP) and logs what
// happened.
func (s *Server) Rehash() {
	s.post(func() {
		if _, err := s.rehash("SIGHUP"); err != nil {
			s.sno('r', "SIGHUP: %v", err)
		}
	})
}

// certTick picks up a renewed certificate file once a minute.
func (s *Server) certTick(now time.Time) {
	if s.certs == nil || now.Sub(s.certChecked) < time.Minute {
		return
	}
	s.certChecked = now
	changed, err := s.certs.ReloadIfChanged()
	switch {
	case err != nil && s.certErr != err.Error():
		s.certErr = err.Error()
		s.sno('a', "The certificate file changed but doesn't load (%v); the old one stays", err)
	case changed:
		s.certErr = ""
		s.certsChanged()
		s.sno('a', "New certificate loaded, valid until %s", s.certs.Leaf().NotAfter.Format("2 Jan 2006"))
	}
}

// cmdRehash is REHASH for opers with flag a: like `beeircd rehash`.
func (s *Server) cmdRehash(c *Client, m irc.Message) {
	if !s.needFlag(c, nil, saFlag) {
		return
	}
	s.reply(c, irc.RplRehashing, filepath.Base(s.cfg.Path), "Rehashing")
	r, err := s.rehash(c.user.Nick)
	if err != nil {
		s.notice(c, "*** "+err.Error())
		return
	}
	if !c.wantsSno('r') { // they already saw "Rehash done" as a server notice
		s.notice(c, "*** "+rehashSummary(r))
	}
}

// machineMB is the machine's total memory from /proc/meminfo (Linux), or
// 0 where that file is missing.
func machineMB() float64 {
	b, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0
	}
	return memTotalMB(string(b))
}

// memTotalMB reads MemTotal (in kB) from /proc/meminfo text.
func memTotalMB(meminfo string) float64 {
	for _, line := range strings.Split(meminfo, "\n") {
		if f := strings.Fields(line); len(f) >= 2 && f[0] == "MemTotal:" {
			kb, err := strconv.ParseFloat(f[1], 64)
			if err != nil {
				return 0
			}
			return kb / 1024
		}
	}
	return 0
}
