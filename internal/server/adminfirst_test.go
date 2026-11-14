package server

import (
	"os"
	"strings"
	"testing"
)

// QA 14 Nov 2026: with admin_password empty and no admin_hosts, anyone who
// registered the admin's nick first could set the admin oper password with
// their first /oper and take the admin oper over.
func TestFirstOperNeedsAdminHosts(t *testing.T) {
	s, path, _ := fileServer(t, "") // fileConf: admin = "Boss", no admin_password
	s.cfg.Services.AdminHosts = nil
	x := registered(t, s, "Boss") // a stranger got the admin nick first
	x.send("OPER Boss attacker-pass-2026")
	x.expect(" 491 Boss ")
	x.expect("admin_password")
	x.send("MODE Boss")
	if l := x.expect(" 221 Boss "); strings.Contains(l[strings.LastIndex(l, " "):], "o") {
		t.Errorf("stranger is an oper: %s", l)
	}
	raw, _ := os.ReadFile(path)
	if strings.Contains(string(raw), "admin_password = \"$argon2id$") {
		t.Errorf("stranger's password saved:\n%s", raw)
	}
}
