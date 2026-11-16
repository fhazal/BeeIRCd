package server

// The Hive Panel's Backups card (owner only). It lists the dated
// backups in the backup folder and hands one out after the owner types
// their password again: a backup holds every password hash and the
// server's private keys, so a browser left signed in must not be enough.
// The server only says which file may go; the panel reads it itself (same
// user, and the server never sends files over the socket).

import (
	"os"
	"path/filepath"
	"time"

	"github.com/fhazal/beeircd/internal/control"
	"github.com/fhazal/beeircd/internal/services"
)

// panelBackups lists the backups; on the loop.
func (s *Server) panelBackups() *control.BackupList {
	dir := s.cfg.Backup.Dir
	return &control.BackupList{Dir: dir, Keep: s.cfg.Backup.Keep, Files: control.ListBackups(dir)}
}

// panelBackupGet checks the owner's password again and answers with the
// file the panel may send. done runs on the loop.
func (s *Server) panelBackupGet(req control.Request, done func(control.Response)) {
	po, deny := s.panelAllowed(req, 'O')
	if deny != nil {
		done(*deny)
		return
	}
	name := req.Name
	if !control.IsBackupName(name) {
		done(control.Response{Error: "That isn't a backup."})
		return
	}
	path := filepath.Join(s.cfg.Backup.Dir, name)
	fi, err := os.Stat(path)
	if err != nil || !fi.Mode().IsRegular() {
		done(control.Response{Error: "That backup isn't there any more."})
		return
	}
	if req.Password == "" || len(req.Password) > 256 {
		done(control.Response{Error: "Wrong password."})
		return
	}
	_, a, o, problem := s.panelOper(po.Account, req.IP)
	if a == nil || o == nil || (o.PassHash == "" && !s.outsideOpers()) {
		done(control.Response{Error: problem, Denied: "signin"})
		return
	}
	key := operLockKey(a.ID)
	s.svc.CheckOper(new(bool), func() bool { return false }, key, req.IP, a.Name, o.PassHash, req.Password, func(res services.PwResult) {
		switch res {
		case services.PwOK:
			s.panelEvent(po.Account, req.IP, "downloaded backup "+name)
			s.snotice("Hive Panel: %s downloaded backup %s from %s", po.Account, name, req.IP)
			done(control.Response{Backup: &control.Backup{File: path, Bytes: fi.Size()}})
		case services.PwLocked:
			done(control.Response{Error: "Too many wrong passwords for this oper. Try again in " + services.RoundUp(s.lockedFor(key, req.IP)) + "."})
		default:
			s.panelEvent(po.Account, req.IP, "could not download backup "+name+": wrong password")
			s.snotice("Hive Panel: wrong password for %s's backup download from %s", po.Account, req.IP)
			done(control.Response{Error: "Wrong password."})
		}
	})
}

// panelBackupGetWait runs panelBackupGet and waits for the password check.
func (s *Server) panelBackupGetWait(req control.Request) control.Response {
	ch := make(chan control.Response, 1)
	if !s.onLoop(func() { s.panelBackupGet(req, func(r control.Response) { ch <- r }) }) {
		return control.Response{Error: "the server is stopping"}
	}
	select {
	case r := <-ch:
		return r
	case <-time.After(panelLoginWait):
		return control.Response{Error: "The server took too long to check the password. Please try again."}
	case <-s.done:
		return control.Response{Error: "the server is stopping"}
	}
}
