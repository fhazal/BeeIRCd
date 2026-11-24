package server

import (
	"strings"
	"testing"

	"github.com/fhazal/beeircd/internal/control"
)

// Every panel command against every oper flag. A command is
// refused with Denied "flag" exactly when the oper lacks its flag ('O' =
// the owner only, 0 = any oper).
func TestPanelFlagMatrix(t *testing.T) {
	s, _, _ := fileServer(t, "")
	b := boss(t, s)
	for _, f := range allFlags {
		nick := "op" + string(f)
		registered(t, s, nick)
		b.send("AS OPER " + nick + " ADD FLAGS " + string(f) + " HOST *@127.0.0.0/8 PASSWORD " + emberPW)
		b.expect(nick + " is an oper now")
	}
	need := map[string]byte{
		control.CmdPanelWho: 0, control.CmdPanelLog: 0, control.CmdStatus: 0,
		control.CmdPeople: 0, control.CmdPerson: 0, control.CmdNotice: 0, control.CmdWhere: 'h',
		control.CmdSettings: 'a', control.CmdClasses: 'a', control.CmdSet: 'a', control.CmdUndo: 'a', control.CmdCheck: 'a', control.CmdRehash: 'a',
		control.CmdBackup: 'O', control.CmdBackups: 'O', control.CmdBackupGet: 'O',
		control.CmdKill: 'k', control.CmdBan: 'k',
		control.CmdBans: 'k', control.CmdBanAdd: 'k', control.CmdBanDel: 'k', control.CmdBanMatch: 'k',
		control.CmdOpers: 'a', control.CmdOperSave: 'a', control.CmdOperDel: 'a',
		control.CmdLinks: 'c', control.CmdLinkConnect: 'c', control.CmdLinkSplit: 'c', control.CmdLinkSave: 'c', control.CmdLinkDel: 'c', control.CmdMapPlace: 'c',
		control.CmdChannels: 0, control.CmdChannel: 0, control.CmdChanNotice: 'g',
		control.CmdChanTopic: 's', control.CmdChanSuspend: 's', control.CmdChanUnsuspend: 's', control.CmdMetaClear: 0,
		control.CmdAccounts: 's', control.CmdAccount: 's', control.CmdGlobal: 'g', // the Services tabs and serv-do: TestPanelOperPages
	}
	who := []string{"Boss"}
	for _, f := range allFlags {
		who = append(who, "op"+string(f))
	}
	for cmd, f := range need {
		for _, as := range who {
			flags := allFlags
			if as != "Boss" {
				flags = as[2:]
			}
			allowed := f == 0 || f == 'O' && as == "Boss" || f != 'O' && strings.IndexByte(flags, f) >= 0
			// Harmless arguments: nothing here changes anything that matters.
			r := panelAsk(t, s, control.Request{Cmd: cmd, As: as, IP: "127.0.0.1", Name: "nobody-here", UID: "000AAAAAA",
				Kind: "akill", Mask: "*@192.0.2.250", Block: &control.LinkBlock{Name: "x.example.net"}})
			denied := r.Denied == "flag"
			if denied == allowed {
				t.Errorf("%s as %s (flags %s): allowed=%v, answer %+v", cmd, as, flags, allowed, r.Error)
			}
			if r.Denied == "signin" {
				t.Errorf("%s as %s: signed out: %s", cmd, as, r.Error)
			}
		}
	}
	// Every command the panel has is in the matrix.
	if len(need) != 41 {
		t.Errorf("matrix has %d commands", len(need))
	}
}
