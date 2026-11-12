package server

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fhazal/beeircd/internal/control"
)

// Step 3i: the Backups card.
func TestPanelBackups(t *testing.T) {
	s, _, _ := fileServer(t, "")
	boss(t, s)
	as := func(req control.Request) *control.Response {
		req.As, req.IP = "Boss", "127.0.0.1"
		return panelAsk(t, s, req)
	}
	l := as(control.Request{Cmd: control.CmdBackups}).Backups
	if l == nil || len(l.Files) != 0 || l.Dir == "" || l.Keep == 0 {
		t.Fatalf("empty list: %+v", l)
	}
	// One from the command line, then one from the panel a moment later.
	cli := panelAsk(t, s, control.Request{Cmd: control.CmdBackup}).Backup
	if cli == nil {
		t.Fatal("no CLI backup")
	}
	old := filepath.Join(l.Dir, "beeircd-2020-01-01-0400.tar.gz")
	os.Rename(cli.File, old)
	pb := as(control.Request{Cmd: control.CmdBackup}).Backup
	if pb == nil {
		t.Fatal("no panel backup")
	}
	l = as(control.Request{Cmd: control.CmdBackups}).Backups
	if len(l.Files) != 2 || l.Files[0].Name != filepath.Base(pb.File) || l.Files[0].By != "Boss" ||
		l.Files[1].Name != filepath.Base(old) || l.Files[1].By != "" || l.Files[0].Bytes != pb.Bytes {
		t.Fatalf("list: %+v", l.Files)
	}
	name := l.Files[0].Name

	// Names that aren't a backup in the folder.
	for _, n := range []string{"", "../beeircd.conf", "beeircd-x.tar.gz/../../etc", `..\beeircd-1.tar.gz`, ".made-by.json", "beeircd-.tar.gz"} {
		if r := as(control.Request{Cmd: control.CmdBackupGet, Name: n, Password: operPW}); r.Backup != nil || r.Error != "That isn't a backup." {
			t.Errorf("%q: %+v", n, r)
		}
	}
	if r := as(control.Request{Cmd: control.CmdBackupGet, Name: "beeircd-1999-01-01-0000.tar.gz", Password: operPW}); r.Backup != nil || !strings.Contains(r.Error, "isn't there") {
		t.Errorf("missing: %+v", r)
	}
	// Wrong password: refused, not signed out, written down.
	if r := as(control.Request{Cmd: control.CmdBackupGet, Name: name, Password: "nope"}); r.Backup != nil || r.Error != "Wrong password." || r.Denied != "" {
		t.Errorf("wrong password: %+v", r)
	}
	if r := as(control.Request{Cmd: control.CmdBackupGet, Name: name}); r.Backup != nil || r.Error != "Wrong password." {
		t.Errorf("no password: %+v", r)
	}
	r := as(control.Request{Cmd: control.CmdBackupGet, Name: name, Password: operPW})
	if r.Backup == nil || r.Backup.File != filepath.Join(l.Dir, name) || r.Backup.Bytes != pb.Bytes {
		t.Fatalf("right password: %+v", r)
	}
	ev := as(control.Request{Cmd: control.CmdPanelLog}).Events
	if ev[0].What != "downloaded backup "+name || !strings.Contains(ev[1].What, "wrong password") {
		t.Errorf("events: %+v", ev[:3])
	}
	// The note file is not a backup and never pruned as one.
	if _, err := os.Stat(filepath.Join(l.Dir, ".made-by.json")); err != nil {
		t.Error(err)
	}
	for _, b := range control.Backups(l.Dir) {
		if strings.Contains(b, "made-by") {
			t.Errorf("note listed as a backup: %s", b)
		}
	}
}
