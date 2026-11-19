package server

import (
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/fhazal/beeircd/internal/config"
	"github.com/fhazal/beeircd/internal/irc"
	"github.com/fhazal/beeircd/internal/services"
	"github.com/fhazal/beeircd/internal/version"
)

// Spam reports, like UnrealIRCd's spamreport block and /SPAMREPORT: an
// oper (or a spam filter rule that removes someone) sends a spammer's IP
// to DroneBL or to any web address. LAN addresses, opers and people an
// EXCEPTION BAN covers are never reported.

// droneBLRPC is DroneBL's RPC2 address (a dronebl block's url overrides it).
const droneBLRPC = "https://dronebl.org/RPC2"

// spamReportAgain is how long the same IP isn't sent to the same place again.
const spamReportAgain = time.Hour

// spamReportTimeout bounds one report.
const spamReportTimeout = 15 * time.Second

// spamReports is what the loop remembers about reports.
type spamReports struct {
	sent map[string][]time.Time // block name -> times of recent reports
	last map[string]time.Time   // block name + " " + ip -> last report
}

// spamTarget is who a report is about.
type spamTarget struct {
	ip, nick, ident, host string
	oper                  bool
}

// spamReportClient is the HTTP client reports use. Reports are rare, so
// no connection is kept open between them.
var spamReportClient = &http.Client{Timeout: spamReportTimeout, Transport: &http.Transport{Proxy: http.ProxyFromEnvironment, DisableKeepAlives: true}}

// spamTargetOf finds who /SPAMREPORT names: a nick on the network or an IP.
func (s *Server) spamTargetOf(arg string) (spamTarget, string) {
	if p := net.ParseIP(arg); p != nil {
		return spamTarget{ip: p.String()}, ""
	}
	u := s.net.UserByNick(arg)
	if u == nil {
		return spamTarget{}, "No one is using the nick " + arg + "."
	}
	if u.Service {
		return spamTarget{}, u.Nick + " is a service."
	}
	if u.IP == "" || net.ParseIP(u.IP) == nil {
		return spamTarget{}, "No address is known for " + u.Nick + "."
	}
	return spamTarget{ip: net.ParseIP(u.IP).String(), nick: u.Nick, ident: u.TrueIdent(), host: u.RealHost, oper: u.HasMode('o')}, ""
}

// spamWho is "203.0.113.9 (spammer42)" or just the IP.
func (t spamTarget) who() string {
	if t.nick != "" {
		return t.ip + " (" + t.nick + ")"
	}
	return t.ip
}

// reportSpam sends t to the [[spamreport]] blocks that are on (only the
// one called only, when given). reason goes in the report. auto is a
// report from the spam filter: only blocks with auto_report =
// ["spamfilter"] take it. done gets one line per block (or why nothing
// was sent) on the loop, once every report has an answer.
func (s *Server) reportSpam(t spamTarget, only, reason string, auto bool, done func([]string)) {
	if done == nil {
		done = func([]string) {}
	}
	switch {
	case s.dnsblSkip(t.ip):
		done([]string{t.who() + " is a local network address; those are never reported."})
		return
	case t.oper:
		done([]string{t.nick + " is an oper; opers are never reported."})
		return
	case s.svc.Exempt(t.ident, t.host, t.ip):
		done([]string{t.who() + " is covered by an EXCEPTION BAN, so it isn't reported."})
		return
	}
	var blocks []config.SpamReport
	for _, r := range s.cfg.SpamReports {
		if !r.On() || auto && !r.Auto("spamfilter") || only != "" && !strings.EqualFold(r.Name, only) {
			continue
		}
		blocks = append(blocks, r)
	}
	if len(blocks) == 0 {
		switch {
		case only != "":
			done([]string{"No spam report place called " + only + " is on. /SPAMREPORT alone sends to every one."})
		case len(s.cfg.SpamReports) == 0:
			done([]string{"No spam report place is set up. Add a [[spamreport]] block to beeircd.conf (see ADMIN.md)."})
		default:
			done([]string{"Every spam report place is turned off."})
		}
		return
	}
	if s.spamRep.sent == nil {
		s.spamRep = spamReports{sent: map[string][]time.Time{}, last: map[string]time.Time{}}
	}
	now := time.Now()
	lines := make([]string, len(blocks))
	waiting := 0
	for i, r := range blocks {
		key := strings.ToLower(r.Name)
		if at, ok := s.spamRep.last[key+" "+t.ip]; ok && now.Sub(at) < spamReportAgain {
			lines[i] = t.ip + " was sent to " + r.Name + " less than an hour ago."
			continue
		}
		if r.Type == "dronebl" && r.RPCKey == "" {
			lines[i] = r.Name + " has no rpckey yet. Get one at https://dronebl.org/rpckey_signup, then: /msg AdminServ SPAMREPORT KEY " + r.Name + " <key>"
			continue
		}
		n, per := r.Rate()
		recent := s.spamRep.sent[key][:0]
		for _, at := range s.spamRep.sent[key] {
			if now.Sub(at) < per {
				recent = append(recent, at)
			}
		}
		s.spamRep.sent[key] = recent
		if len(recent) >= n {
			lines[i] = fmt.Sprintf("%s has had its %d reports in %s; try again shortly.", r.Name, n, lengthWords(per))
			continue
		}
		s.spamRep.sent[key] = append(recent, now)
		s.spamRep.last[key+" "+t.ip] = now
		waiting++
		go func(i int, r config.SpamReport) {
			err := sendSpamReport(r, t, reason)
			s.post(func() {
				if err != nil {
					delete(s.spamRep.last, strings.ToLower(r.Name)+" "+t.ip)
					lines[i] = "Reporting " + t.who() + " to " + r.Name + " failed: " + err.Error()
					s.sno('a', "Spam report of %s to %s failed: %v", t.who(), r.Name, err)
				} else {
					lines[i] = "Reported " + t.who() + " to " + r.Name + "."
					s.sno('a', "Reported %s to %s (%s)", t.who(), r.Name, reason)
				}
				if waiting--; waiting == 0 {
					done(lines)
				}
			})
		}(i, r)
	}
	if waiting == 0 {
		done(lines)
	}
}

// sendSpamReport makes one report (off the loop).
func sendSpamReport(r config.SpamReport, t spamTarget, reason string) error {
	ctx, cancel := context.WithTimeout(context.Background(), spamReportTimeout)
	defer cancel()
	if r.Type == "dronebl" {
		return sendDroneBL(ctx, r, t, reason)
	}
	fill := strings.NewReplacer("$ip", url.QueryEscape(t.ip), "$nick", url.QueryEscape(t.nick), "$reason", url.QueryEscape(reason))
	req, err := http.NewRequestWithContext(ctx, r.Method, fill.Replace(r.URL), nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "BeeIRCd/"+version.Version)
	resp, err := spamReportClient.Do(req)
	if err != nil {
		return reportNetErr(err)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("the address answered %s", resp.Status)
	}
	return nil
}

// droneBLAnswer is DroneBL's RPC2 reply.
type droneBLAnswer struct {
	Type    string `xml:"type,attr"`
	Code    string `xml:"code"`
	Message string `xml:"message"`
}

// sendDroneBL adds t.ip to DroneBL with RPC2.
func sendDroneBL(ctx context.Context, r config.SpamReport, t spamTarget, reason string) error {
	comment := "Spam on IRC"
	if reason != "" {
		comment += ": " + irc.Truncate(reason, 200)
	}
	var b strings.Builder
	b.WriteString(`<?xml version="1.0"?>` + "\n" + `<request key="`)
	xml.EscapeText(&b, []byte(r.RPCKey))
	b.WriteString(`"`)
	if r.Staging {
		b.WriteString(` staging="1"`)
	}
	fmt.Fprintf(&b, `><add ip="%s" type="%d" comment="`, t.ip, r.Class)
	xml.EscapeText(&b, []byte(comment))
	b.WriteString(`"/></request>`)
	to := droneBLRPC
	if r.URL != "" {
		to = r.URL
	}
	req, err := http.NewRequestWithContext(ctx, "POST", to, strings.NewReader(b.String()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "text/xml")
	req.Header.Set("User-Agent", "BeeIRCd/"+version.Version)
	resp, err := spamReportClient.Do(req)
	if err != nil {
		return reportNetErr(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	var a droneBLAnswer
	if err := xml.Unmarshal(body, &a); err != nil {
		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("DroneBL answered %s", resp.Status)
		}
		return fmt.Errorf("DroneBL's answer wasn't one I know")
	}
	switch {
	case a.Type == "success":
		return nil
	case strings.TrimSpace(a.Message) != "":
		return fmt.Errorf("DroneBL says: %s", strings.TrimSpace(a.Message))
	}
	return fmt.Errorf("DroneBL said no (%s)", orStr(strings.TrimSpace(a.Code), a.Type))
}

// reportNetErr drops the URL from an http error, so a key in it never shows.
func reportNetErr(err error) error {
	if ue, ok := err.(*url.Error); ok {
		if ue.Timeout() {
			return fmt.Errorf("no answer in %s", lengthWords(spamReportTimeout))
		}
		return ue.Err
	}
	return err
}

// cmdSpamReport is SPAMREPORT <nick|ip> [name] (opers, flag k).
func (s *Server) cmdSpamReport(c *Client, m irc.Message) {
	if !s.needFlag(c, nil, 'k') {
		return
	}
	t, problem := s.spamTargetOf(m.Params[0])
	if problem != "" {
		s.notice(c, "*** "+problem)
		return
	}
	by := c.user.Nick
	s.reportSpam(t, m.Param(1), "reported by "+by, false, func(lines []string) {
		if c.closed {
			return
		}
		for _, l := range lines {
			s.notice(c, "*** "+l)
		}
	})
}

// autoSpamReport reports someone a spam filter rule just removed or
// banned, to the places with auto_report = ["spamfilter"].
func (s *Server) autoSpamReport(c *Client, reason string) {
	if len(s.cfg.SpamReports) == 0 || c.isOper() {
		return
	}
	t := spamTarget{ip: c.ip, nick: c.nick, ident: c.ident, host: c.host}
	if c.user != nil {
		t.nick, t.ident, t.host = c.user.Nick, c.user.TrueIdent(), c.user.RealHost
	}
	if p := net.ParseIP(t.ip); p != nil {
		t.ip = p.String()
	}
	s.reportSpam(t, "", "spam filter: "+reason, true, nil)
}

// spamReportBlock is the [[spamreport]] block called name, or nil.
func (s *Server) spamReportBlock(name string) *config.SpamReport {
	for i := range s.cfg.SpamReports {
		if strings.EqualFold(s.cfg.SpamReports[i].Name, name) {
			return &s.cfg.SpamReports[i]
		}
	}
	return nil
}

// editSpamReport changes the [[spamreport]] block called name in
// beeircd.conf (adding a dronebl one when add and there is none) and
// takes the saved blocks into use.
func (s *Server) editSpamReport(name string, add bool, key, value string) string {
	if s.cfg.Path == "" {
		return "This server wasn't started from a config file, so this can't be saved."
	}
	fresh, err := config.Edit(s.cfg.Path, func(d *config.Doc) error {
		if b := s.spamReportBlock(name); b != nil {
			if !d.SetBlockKey("spamreport", "name", b.Name, key, value) {
				return fmt.Errorf("the [[spamreport]] block %s isn't in %s as written", b.Name, s.cfg.Path)
			}
			return nil
		}
		if !add {
			return fmt.Errorf("there is no spam report place called %s", name)
		}
		d.SetBlock("spamreport", []config.KV{{Key: "name", Value: config.Quote(name)}, {Key: "type", Value: `"dronebl"`}, {Key: key, Value: value}})
		return nil
	})
	if err != nil {
		s.log.Printf("AdminServ: %v", err)
		return "Not saved: " + err.Error()
	}
	s.cfg.SpamReports = fresh.SpamReports
	return ""
}

// setSpamReportKey saves (or with key "" removes) the DroneBL key of the
// place called name; who is for HISTORY, which never gets the key.
func (s *Server) setSpamReportKey(who, name, key string) (string, string) {
	if !validSpamName(name) {
		return "", "Name the place with letters, digits, - and _, like dronebl."
	}
	if b := s.spamReportBlock(name); b != nil && b.Type != "dronebl" {
		return "", b.Name + " is an http place; it has no rpckey."
	}
	if key != "" && (len(key) > 200 || strings.ContainsAny(key, "\"'<>&\\ \t")) {
		return "", "That doesn't look like a DroneBL key."
	}
	if key == "" && s.spamReportBlock(name) == nil {
		return "", "There is no spam report place called " + name + "."
	}
	old := "no key"
	if b := s.spamReportBlock(name); b != nil && b.RPCKey != "" {
		old = "key set"
	}
	if p := s.editSpamReport(name, true, "rpckey", config.Quote(key)); p != "" {
		return "", p
	}
	now := "no key"
	if key != "" {
		now = "key set"
	}
	s.recordBy(who, "spamreport", s.spamReportBlock(name).Name+" rpckey", old, now)
	if key == "" {
		return "The DroneBL key of " + name + " is removed.", ""
	}
	return "The DroneBL key of " + name + " is saved. /SPAMREPORT <nick|ip> now sends there.", ""
}

// setSpamReportStaging turns DroneBL's test list on or off for name.
func (s *Server) setSpamReportStaging(who, name string, on bool) (string, string) {
	b := s.spamReportBlock(name)
	switch {
	case b == nil:
		return "", "There is no spam report place called " + name + "."
	case b.Type != "dronebl":
		return "", b.Name + " is an http place; staging is for DroneBL."
	}
	old := map[bool]string{true: "on", false: "off"}[b.Staging]
	if p := s.editSpamReport(name, false, "staging", fmt.Sprint(on)); p != "" {
		return "", p
	}
	now := map[bool]string{true: "on", false: "off"}[on]
	s.recordBy(who, "spamreport", s.spamReportBlock(name).Name+" staging", old, now)
	if on {
		return "Reports to " + name + " go to DroneBL's test list now (nothing is really listed).", ""
	}
	return "Reports to " + name + " go to the real DroneBL list now.", ""
}

func validSpamName(n string) bool {
	if n == "" || len(n) > 30 {
		return false
	}
	for _, r := range strings.ToLower(n) {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
			return false
		}
	}
	return true
}

// spamReportLine describes a place for LIST and the panel (never the key).
func spamReportLine(r config.SpamReport) string {
	parts := []string{r.Type}
	if r.Type == "dronebl" {
		if r.RPCKey == "" {
			parts = append(parts, "no key yet")
		} else {
			parts = append(parts, "key set")
		}
		parts = append(parts, fmt.Sprintf("class %d", r.Class))
		if r.Staging {
			parts = append(parts, "test list (staging)")
		}
	} else if u, err := url.Parse(r.URL); err == nil {
		parts = append(parts, u.Host)
	}
	n, per := r.Rate()
	parts = append(parts, fmt.Sprintf("at most %d per %s", n, lengthWords(per)))
	if r.Auto("spamfilter") {
		parts = append(parts, "spam filter reports on its own")
	}
	if !r.On() {
		parts = append(parts, "off")
	}
	return strings.Join(parts, " · ")
}

// asSpamReport is AdminServ SPAMREPORT.
func asSpamReport(s *Server, c *Client, args []string) {
	if !s.needFlag(c, s.adminServ, 'a') {
		return
	}
	sub := strings.ToUpper(services.ArgAt(args, 0))
	name := services.ArgAt(args, 1)
	var msg, problem string
	switch sub {
	case "", "LIST":
		if len(s.cfg.SpamReports) == 0 {
			s.as(c, "No spam report places. Add one: /as spamreport key dronebl <your DroneBL key>")
			return
		}
		s.as(c, "Spam report places:")
		for _, r := range s.cfg.SpamReports {
			s.as(c, "  %-16s %s", r.Name, spamReportLine(r))
		}
		return
	case "KEY":
		key := services.ArgAt(args, 2)
		if name == "" || key == "" {
			s.as(c, "Syntax: SPAMREPORT KEY <name> <rpckey|OFF>")
			return
		}
		if strings.EqualFold(key, "off") {
			key = ""
		}
		msg, problem = s.setSpamReportKey(s.changedBy(c), name, key)
		if problem == "" {
			s.audit(c, s.adminServ, "SPAMREPORT KEY %s", name)
		}
	case "STAGING":
		on, ok := onOff(services.ArgAt(args, 2))
		if name == "" || !ok {
			s.as(c, "Syntax: SPAMREPORT STAGING <name> ON|OFF")
			return
		}
		msg, problem = s.setSpamReportStaging(s.changedBy(c), name, on)
		if problem == "" {
			s.audit(c, s.adminServ, "SPAMREPORT STAGING %s %s", name, map[bool]string{true: "ON", false: "OFF"}[on])
		}
	default:
		s.as(c, "Syntax: SPAMREPORT LIST | KEY <name> <rpckey|OFF> | STAGING <name> ON|OFF")
		return
	}
	if problem != "" {
		s.as(c, "%s", problem)
		return
	}
	s.as(c, "%s", msg)
}
