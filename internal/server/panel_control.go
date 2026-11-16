package server

// What the Hive Panel asks over the control socket. The panel
// (beeircd-web) keeps the browser sessions; this side decides. Sign-in is
// the oper nick and oper password, as /oper (same entry, same lockout),
// with the entry's host masks checked against the web address. Every
// later request names the oper again and is checked again here: a
// removed, disabled or changed oper is stopped at the next click, and a
// flag the oper lacks is refused here whatever the page showed.

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/fhazal/beeircd/internal/control"
	"github.com/fhazal/beeircd/internal/mail"
	"github.com/fhazal/beeircd/internal/passwd"
	"github.com/fhazal/beeircd/internal/services"
	"github.com/fhazal/beeircd/internal/state"
	"github.com/fhazal/beeircd/internal/store"
)

// panelLoginWait is how long a sign-in may take (the password check runs
// off the loop and may queue behind others).
const panelLoginWait = 15 * time.Second

// panelEventsShown is how many "What happened" lines the panel gets.
const panelEventsShown = 50

// webUser stands for someone at ip in the Hive Panel, for the host masks
// of oper entries ("*@203.0.113.*", "*@*"). There is no ident or hostname.
func webUser(ip string) *state.User {
	return &state.User{Ident: "web", RealHost: ip, IP: ip, Host: ip}
}

// panelEvent notes e in "What happened" and the log.
func (s *Server) panelEvent(who, ip, what string) {
	s.log.Printf("Hive Panel: %s (%s) %s", who, ip, what)
	if err := s.store.AddPanelEvent(store.PanelEvent{At: time.Now(), Who: who, IP: ip, What: what}); err != nil {
		s.log.Printf("services database: %v", err)
	}
}

// panelOper looks up the oper an account is now (on the loop). problem is
// set when they may not use the panel.
func (s *Server) panelOper(account, ip string) (po *control.PanelOper, a *store.Account, o *store.Oper, problem string) {
	if s.svc.Off() {
		return nil, nil, nil, "Sign in to the Hive Panel on the server that runs services."
	}
	if account == "" || ip == "" {
		return nil, nil, nil, "Please sign in."
	}
	a, err := s.store.AccountByNick(account)
	if err != nil {
		s.log.Printf("services database: %v", err)
		return nil, nil, nil, "Something went wrong on the server. Please try again in a moment."
	}
	if a != nil {
		if o, err = s.operEntry(a); err != nil {
			s.log.Printf("services database: %v", err)
			return nil, nil, nil, "Something went wrong on the server. Please try again in a moment."
		}
	}
	switch {
	case o == nil:
		return nil, a, nil, "no oper entry"
	case !operHostOK(o, webUser(ip)):
		return nil, a, o, "the oper entry doesn't cover " + ip
	case o.Disabled != "":
		return nil, a, o, "the oper entry is disabled"
	}
	v := s.viewOf(o)
	if v.flags == "" {
		return nil, a, o, "the oper entry has no flags"
	}
	return &control.PanelOper{Account: o.Account, Class: v.class, Flags: v.flags, Owner: s.isAdminAccount(a), Seal: passSeal(o.PassHash), Chat: s.cfg.Web.ChatURL}, a, o, ""
}

// passSeal is a short fingerprint of an oper password hash (see
// control.PanelOper.Seal).
func passSeal(hash string) string {
	sum := sha256.Sum256([]byte("hive panel seal:" + hash))
	return hex.EncodeToString(sum[:8])
}

var (
	dummyOnce sync.Once
	dummyHash string
)

// panelDummyCheck spends what a password check costs (off the loop).
var panelDummyCheck = func(pw string) {
	dummyOnce.Do(func() { dummyHash, _ = passwd.Hash("hive panel dummy password") })
	passwd.Check(pw, dummyHash)
}

// panelLogin checks a sign-in and calls done with the answer (on the loop).
func (s *Server) panelLogin(req control.Request, done func(control.Response)) {
	nick, pw, ip := strings.TrimSpace(req.Nick), req.Password, req.IP
	wrong := control.Response{Error: "Wrong nick or password.", Denied: "signin"}
	if nick == "" || pw == "" || ip == "" || len(nick) > 64 || len(pw) > 256 || !utf8.ValidString(nick) {
		done(wrong)
		return
	}
	po, a, o, problem := s.panelOper(nick, ip)
	// A registered nick always gets its NickServ password checked: with
	// Members off a right one is told so (memberLogin), not counted wrong.
	members := a != nil
	if po == nil && members {
		s.memberLogin(a, nick, pw, ip, done)
		return
	}
	if po == nil {
		if a == nil && o == nil && strings.HasPrefix(problem, "Sign in") || strings.HasPrefix(problem, "Something") {
			done(control.Response{Error: problem, Denied: "signin"})
			return
		}
		s.panelEvent(nick, ip, "could not sign in: "+problem)
		s.snotice("Hive Panel: refused sign-in as %s from %s (%s)", nick, ip, problem)
		// Answer as slowly as a real password check would, so the time
		// taken doesn't tell which nicks are opers.
		go func() {
			panelDummyCheck(pw)
			s.post(func() { done(wrong) })
		}()
		return
	}
	if o.PassHash == "" && members {
		s.memberLogin(a, nick, pw, ip, done)
		return
	}
	if o.PassHash == "" && !s.outsideOpers() {
		s.panelEvent(nick, ip, "could not sign in: no oper password set yet")
		done(control.Response{Error: "No oper password is set for " + o.Account + " yet. /oper once on IRC first.", Denied: "signin"})
		return
	}
	key := operLockKey(a.ID)
	s.svc.CheckOper(new(bool), func() bool { return false }, key, ip, a.Name, o.PassHash, pw, func(res services.PwResult) {
		switch res {
		case services.PwOK:
			s.panelEvent(po.Account, ip, "signed in")
			s.snotice("Hive Panel: %s signed in from %s: %s", po.Account, ip, po.Flags)
			done(control.Response{Oper: po})
		case services.PwLocked:
			if members { // the NickServ password has its own lock
				s.memberLogin(a, nick, pw, ip, done)
				return
			}
			done(control.Response{Error: "Too many wrong passwords for this oper. Try again in " + services.RoundUp(s.lockedFor(key, ip)) + ".", Denied: "signin"})
		default:
			if members { // maybe their NickServ password: the member pages
				s.memberLogin(a, nick, pw, ip, func(r control.Response) {
					if r.Oper != nil || r.Error == memberOffMsg { // the right NickServ password: not a wrong oper password
						s.svc.ForgiveWrong(key, ip)
					}
					done(r)
				})
				return
			}
			s.panelEvent(nick, ip, "could not sign in: wrong password")
			s.snotice("Hive Panel: wrong password for %s from %s", nick, ip)
			done(wrong)
		}
	})
}

// panelAllowed checks req's oper again for flag need (0: any oper; 'O':
// the owner; 'm': members too). The answer is nil when allowed.
func (s *Server) panelAllowed(req control.Request, need byte) (*control.PanelOper, *control.Response) {
	if mp, deny, ok := s.panelMember(req); ok {
		switch {
		case deny != nil:
			return nil, deny
		case need == 'm':
			return mp, nil
		}
		return mp, &control.Response{Error: "Only opers can do that.", Denied: "flag"}
	}
	po, _, _, problem := s.panelOper(req.As, req.IP)
	if po == nil {
		if !strings.HasPrefix(problem, "Something") && !strings.HasPrefix(problem, "Sign in") {
			problem = "Please sign in again."
		}
		return nil, &control.Response{Error: problem, Denied: "signin"}
	}
	switch {
	case req.Seal != po.Seal:
		return nil, &control.Response{Error: "Your oper password changed. Please sign in again.", Denied: "signin"}
	case need == 'O' && !po.Owner:
		return po, &control.Response{Error: "Only the owner can do that.", Denied: "flag"}
	case need != 0 && need != 'O' && need != 'm' && strings.IndexByte(po.Flags, need) < 0:
		return po, &control.Response{Error: fmt.Sprintf("You need oper flag %c (%s) for that.", need, flagNames[need]), Denied: "flag"}
	}
	return po, nil
}

// panelSettings lists what Settings can change.
func (s *Server) panelSettings() []control.Setting {
	kinds := map[byte]string{'i': "number", 'd': "time", 'b': "onoff", 't': "text"}
	var out []control.Setting
	for _, st := range settings() {
		cs := control.Setting{Name: st.name, Table: st.table, Key: st.key, Help: st.help, Kind: kinds[st.kind], Value: st.get(s.cfg)}
		if st.kind == 'i' || st.kind == 'd' {
			cs.Min, cs.Max = st.lo, st.hi
		} else if st.kind == 't' {
			cs.Max = st.hi
		}
		out = append(out, cs)
	}
	return out
}

// panelSet changes one setting for po (on the loop).
func (s *Server) panelSet(po *control.PanelOper, ip, name, value string) control.Response {
	st, ok := findSetting(name)
	if !ok {
		return control.Response{Error: "There is no setting " + name + "."}
	}
	old, now, id, problem := s.setSetting(po.Account+" (Hive Panel)", st, strings.TrimSpace(value))
	if problem != "" {
		return control.Response{Error: problem}
	}
	if old != now {
		s.panelEvent(po.Account, ip, fmt.Sprintf("changed %s from %s to %s", st.name, old, now))
		s.snotice("Hive Panel: %s changed %s to %s (was %s)", po.Account, st.name, now, old)
	}
	return control.Response{Changed: &control.Changed{Name: st.name, Old: old, New: now, ID: id}}
}

// panelUndo puts a setting back as it was before HISTORY entry id.
func (s *Server) panelUndo(po *control.PanelOper, ip string, id int64) control.Response {
	ch, ok, err := s.store.ChangeByID(id)
	if err != nil {
		s.log.Printf("services database: %v", err)
		return control.Response{Error: "Something went wrong on the server. Please try again in a moment."}
	}
	if ok && ch.Kind == "oper" {
		return s.panelOperUndo(po, ip, ch)
	}
	if !ok || ch.Kind != "set" {
		return control.Response{Error: "That change can't be undone here any more."}
	}
	st, found := findSetting(ch.Key)
	if !found {
		return control.Response{Error: "That setting is gone."}
	}
	if cur := st.get(s.cfg); cur != ch.New {
		return control.Response{Error: fmt.Sprintf("%s has changed again since (it is now %s).", st.name, cur)}
	}
	return s.panelSet(po, ip, st.name, ch.Old)
}

// panelEvents is "What happened"; addresses only for opers with flag a.
func (s *Server) panelEvents(po *control.PanelOper) ([]control.Event, error) {
	evs, err := s.store.PanelEvents(panelEventsShown)
	if err != nil {
		return nil, err
	}
	out := make([]control.Event, 0, len(evs))
	for _, e := range evs {
		ev := control.Event{At: e.At, Who: e.Who, What: e.What}
		if strings.IndexByte(po.Flags, 'a') >= 0 {
			ev.IP = e.IP
		}
		out = append(out, ev)
	}
	return out, nil
}

// panelControl answers the Hive Panel's commands, and the admin commands
// when the panel sends them for an oper (As set). It reports false for a
// command it doesn't handle.
func (s *Server) panelControl(req control.Request, setDeadline func(time.Duration)) (control.Response, bool) {
	var resp control.Response
	stopping := control.Response{Error: "the server is stopping"}
	// run checks the oper on the loop and then runs f there.
	run := func(need byte, f func(po *control.PanelOper)) control.Response {
		if !s.onLoop(func() {
			po, deny := s.panelAllowed(req, need)
			if deny != nil {
				resp = *deny
				return
			}
			f(po)
		}) {
			return stopping
		}
		return resp
	}
	switch req.Cmd {
	case control.CmdPanelLogin:
		setDeadline(panelLoginWait + 5*time.Second)
		ch := make(chan control.Response, 1)
		if !s.onLoop(func() { s.panelLogin(req, func(r control.Response) { ch <- r }) }) {
			return stopping, true
		}
		select {
		case r := <-ch:
			return r, true
		case <-time.After(panelLoginWait):
			return control.Response{Error: "The server took too long to check the password. Please try again."}, true
		case <-s.done:
			return stopping, true
		}
	case control.CmdPanelHello:
		if !s.onLoop(func() { resp.Hello = &control.Hello{Members: s.cfg.Web.Members && !s.svc.Off()} }) {
			return stopping, true
		}
		return resp, true
	case control.CmdPanelWho:
		return run('m', func(po *control.PanelOper) { resp.Oper = po }), true
	case control.CmdMyNick:
		return run('m', func(po *control.PanelOper) { resp = *s.panelMyNick(po) }), true
	case control.CmdChatKey:
		return run('m', func(po *control.PanelOper) { resp = *s.panelChatKey(po) }), true
	case control.CmdMySet:
		return run('m', func(po *control.PanelOper) { resp = *s.panelMySet(po, req.IP, req.Name, req.Value) }), true
	case control.CmdMyHome:
		return run('m', func(po *control.PanelOper) { resp = *s.panelHome(po) }), true
	case control.CmdMyMemos:
		return run('m', func(po *control.PanelOper) { resp = *s.panelMemos(po) }), true
	case control.CmdMyChan:
		return run('m', func(po *control.PanelOper) { resp = *s.panelChan(po, req.Name) }), true
	case control.CmdChanDo:
		return run('m', func(po *control.PanelOper) { resp = *s.panelChanDo(po, req) }), true
	case control.CmdChanMemo:
		return run('m', func(po *control.PanelOper) { resp = *s.panelChanMemo(po, req) }), true
	case control.CmdMemoRead, control.CmdMemoDel, control.CmdMemoSend:
		return run('m', func(po *control.PanelOper) { resp = *s.panelMemoAct(po, req.Cmd, req.IP, req.ID, req.Name, req.Value) }), true
	case control.CmdPanelLog:
		return run(0, func(po *control.PanelOper) {
			evs, err := s.panelEvents(po)
			if err != nil {
				s.log.Printf("services database: %v", err)
				resp.Error = "Something went wrong on the server."
			}
			resp.Events = evs
		}), true
	case control.CmdSettings:
		return run('a', func(*control.PanelOper) { resp.Settings = s.panelSettings() }), true
	case control.CmdSet:
		return run('a', func(po *control.PanelOper) { resp = s.panelSet(po, req.IP, req.Name, req.Value) }), true
	case control.CmdUndo:
		return run('a', func(po *control.PanelOper) { resp = s.panelUndo(po, req.IP, req.ID) }), true
	case control.CmdPeople:
		return run(0, func(po *control.PanelOper) { resp.People = s.panelPeople(po, req) }), true
	case control.CmdPerson:
		return run(0, func(po *control.PanelOper) {
			if resp.Person = s.panelPerson(po, req.UID); resp.Person == nil {
				resp.Error = "They have left."
			}
		}), true
	case control.CmdWhere:
		return run('h', func(*control.PanelOper) {
			if resp.Where = s.panelWhere(); resp.Where == nil {
				resp.Error = "GeoIP is off on this server."
			}
		}), true
	case control.CmdNotice:
		return run(0, func(po *control.PanelOper) { resp = s.panelNotice(po, req.IP, req.UID, req.Value) }), true
	case control.CmdKill:
		return run('k', func(po *control.PanelOper) { resp = s.panelKill(po, req.IP, req.UID, req.Value) }), true
	case control.CmdBan:
		return run('k', func(po *control.PanelOper) { resp = s.panelBan(po, req.IP, req.UID, req.Value, req.Life) }), true
	case control.CmdSpam:
		return run('k', func(*control.PanelOper) { resp = *s.panelSpam() }), true
	case control.CmdSpamCheck:
		return run('k', func(*control.PanelOper) { resp = *s.panelSpamCheck(req) }), true
	case control.CmdSpamAdd:
		return run('k', func(po *control.PanelOper) { resp = *s.panelSpamAdd(po, req) }), true
	case control.CmdSpamDel:
		return run('k', func(po *control.PanelOper) { resp = *s.panelSpamDel(po, req) }), true
	case control.CmdMail:
		return run('O', func(*control.PanelOper) { resp.Mail = s.panelMail() }), true
	case control.CmdMailSave:
		return run('O', func(po *control.PanelOper) { resp = s.panelMailSave(po, req.IP, req.Mail) }), true
	case control.CmdMailReg:
		return run('O', func(po *control.PanelOper) { resp = s.panelMailReg(po, req.IP, req.Value) }), true
	case control.CmdMailTest:
		setDeadline(mail.Timeout + 40*time.Second)
		return s.panelMailTest(req, run), true
	case control.CmdMailDNS:
		setDeadline(30 * time.Second)
		return s.panelMailDNS(run), true
	case control.CmdSpamTest:
		return run('k', func(*control.PanelOper) { resp = *s.panelSpamTest(req) }), true
	case control.CmdBans:
		return run('k', func(*control.PanelOper) { resp = s.panelBans(req.Kind) }), true
	case control.CmdBanAdd:
		return run('k', func(po *control.PanelOper) { resp = s.panelBanAdd(po, req.IP, req) }), true
	case control.CmdBanDel:
		return run('k', func(po *control.PanelOper) { resp = s.panelBanDel(po, req.IP, req) }), true
	case control.CmdBanMatch:
		return run('k', func(*control.PanelOper) { resp = s.panelBanMatch(req) }), true
	case control.CmdOpers:
		return run('a', func(*control.PanelOper) {
			o, problem := s.panelOpers()
			resp.Opers, resp.Error = o, problem
		}), true
	case control.CmdOperSave:
		hash := ""
		if req.Password != "" {
			// Hashed here, off the loop (argon2id takes a moment).
			if len(req.Password) < minOperPassword {
				return control.Response{Error: fmt.Sprintf("An oper password needs at least %d characters.", minOperPassword)}, true
			}
			h, err := passwd.Hash(req.Password)
			if err != nil {
				s.log.Printf("hashing a password: %v", err)
				return control.Response{Error: "Something went wrong on the server."}, true
			}
			hash = h
		}
		return run('a', func(po *control.PanelOper) { resp = s.panelOperSave(po, req.IP, req, hash) }), true
	case control.CmdOperDel:
		return run('a', func(po *control.PanelOper) { resp = s.panelOperDel(po, req.IP, req.Name) }), true
	case control.CmdLinks:
		return run('c', func(*control.PanelOper) { resp.Links = s.panelLinks() }), true
	case control.CmdLinkConnect:
		return run('c', func(po *control.PanelOper) { resp = s.panelLinkConnect(po, req.IP, req.Name) }), true
	case control.CmdLinkSplit:
		return run('c', func(po *control.PanelOper) { resp = s.panelLinkSplit(po, req.IP, req.Name, req.Reason) }), true
	case control.CmdLinkSave:
		return run('c', func(po *control.PanelOper) { resp = s.panelLinkSave(po, req.IP, req.Block, req.New) }), true
	case control.CmdLinkDel:
		return run('c', func(po *control.PanelOper) { resp = s.panelLinkDel(po, req.IP, req.Name) }), true
	case control.CmdMapPlace:
		return run('c', func(po *control.PanelOper) { resp = s.panelMapPlace(po, req.IP, req) }), true
	case control.CmdChannels:
		return run(0, func(*control.PanelOper) { resp.Channels = s.panelChannels(req) }), true
	case control.CmdChannel:
		return run(0, func(*control.PanelOper) {
			if resp.Channel = s.panelChannel(req.Name); resp.Channel == nil {
				resp.Error = "There is no channel " + req.Name + " (any more)."
			}
		}), true
	case control.CmdChanNotice:
		return run('g', func(po *control.PanelOper) { resp = s.panelChanNotice(po, req.IP, req.Name, req.Value) }), true
	case control.CmdChanTopic:
		return run('s', func(po *control.PanelOper) { resp = s.panelChanTopic(po, req.IP, req.Name, req.Value) }), true
	case control.CmdChanSuspend:
		return run('s', func(po *control.PanelOper) { resp = s.panelChanSuspend(po, req.IP, req.Name, req.Reason, req.Life) }), true
	case control.CmdChanUnsuspend:
		return run('s', func(po *control.PanelOper) { resp = s.panelChanUnsuspend(po, req.IP, req.Name) }), true
	}
	if req.As == "" {
		return resp, false
	}
	// The admin commands, for an oper in the panel.
	switch req.Cmd {
	case control.CmdStatus:
		return run(0, func(*control.PanelOper) { resp.Status = s.statusInfo() }), true
	case control.CmdCheck:
		return run('a', func(po *control.PanelOper) {
			resp.Check = s.checkInfo()
			s.panelEvent(po.Account, req.IP, "ran a check")
		}), true
	case control.CmdRehash:
		return run('a', func(po *control.PanelOper) {
			r, err := s.rehash("the Hive Panel (" + po.Account + ")")
			if err != nil {
				resp.Error = err.Error()
				return
			}
			resp.Rehash = r
			s.panelEvent(po.Account, req.IP, "rehashed beeircd.conf")
		}), true
	case control.CmdBackup:
		var po *control.PanelOper
		if r := run('O', func(p *control.PanelOper) { po = p }); r.Error != "" {
			return r, true
		}
		setDeadline(10 * time.Minute)
		b, err := s.backup("")
		if err != nil {
			return control.Response{Error: err.Error()}, true
		}
		if err := control.NoteMadeBy(filepath.Dir(b.File), b.File, po.Account); err != nil {
			s.post(func() { s.log.Printf("backup: noting who made it: %v", err) })
		}
		s.onLoop(func() { s.panelEvent(po.Account, req.IP, "made a backup") })
		return control.Response{Backup: b}, true
	case control.CmdBackups:
		return run('O', func(*control.PanelOper) { resp.Backups = s.panelBackups() }), true
	case control.CmdBackupGet:
		setDeadline(panelLoginWait + 5*time.Second)
		return s.panelBackupGetWait(req), true
	}
	return control.Response{Error: "The Hive Panel can't do " + req.Cmd + "."}, true
}
