package server

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// Oper classes, oper vhost and swhois, DISABLE/ENABLE,
// HISTORY/DIFF/UNDO, OS OPER, admin_hosts.

const emberPW = "Hive-2026-ember"

// lastChange reads the newest change number from HISTORY.
func lastChange(t *testing.T, b *tc) string {
	t.Helper()
	b.sync() // skip lines still waiting from earlier commands
	b.send("AS HISTORY 1")
	m := regexp.MustCompile(`#(\d+) `).FindStringSubmatch(b.expect(" #"))
	if m == nil {
		t.Fatal("no change number")
	}
	return m[1]
}

func TestOperClasses(t *testing.T) {
	s, path, _ := fileServer(t, "")
	b := boss(t, s)
	e := registered(t, s, "ember")

	b.send("AS CLASS LIST")
	b.expect("netadmin*")
	b.expect("4 classes (* = built in")

	// An entry with a class and a CIDR host.
	b.send("AS OPER ember ADD CLASS globop HOST *@127.0.0.0/8 PASSWORD " + emberPW)
	b.expect(asLine("Boss", "ember is an oper now: class globop, hk (hosts, kill), from *@127.0.0.0/8."))
	b.send("AS OPER ember ADD FLAGS k HOST *@* PASSWORD " + emberPW) // class-only: shows the class and its flags
	b.expect(asLine("Boss", "ember is an oper already (class globop, flags hk). Change it: /as oper ember set flags <letters>"))
	e.send("OPER ember " + emberPW)
	e.expect(" 381 ember :You are now an IRC operator")
	e.expect("*** Oper class globop: hk (hosts, kill)")
	b.send("WHOIS ember")
	b.expect(" 313 Boss ember :is an IRC Operator")

	// A class with a vhost and swhois lines; moving ember into it.
	b.send("AS CLASS staff ADD FLAGS hk")
	b.expect("Class staff: flags hk")
	b.send("AS CLASS staff SET VHOST staff.example.org")
	b.expect("vhost staff.example.org")
	b.send("AS CLASS staff SET SWHOIS looks very helpful | likes bees")
	b.expect("Class staff:")
	b.send("AS CLASS staff SET TITLE is a Staff Member")
	b.expect("title is a Staff Member")
	conf, _ := os.ReadFile(path)
	if !strings.Contains(string(conf), `vhost = "staff.example.org"`) || !strings.Contains(string(conf), "[[operclass]]") {
		t.Fatalf("class not saved:\n%s", conf)
	}
	b.send("AS OPER ember SET CLASS staff")
	e.expect(" 396 ember staff.example.org :is now your displayed host")
	b.expect("ember: class staff")
	b.send("WHOIS ember")
	b.expect(" 311 Boss ember ~ember staff.example.org ")
	b.expect(" 313 Boss ember :is a Staff Member")
	b.expect(" 320 Boss ember :looks very helpful")
	b.expect(" 320 Boss ember :likes bees")
	b.send("AS CLASS staff DEL")
	b.expect("staff is in use by ember. Move them to another class first.")

	// Only classes whose flags you have.
	b.send("AS OPER ember SET FLAGS z")
	b.expect("z isn't an oper flag")

	// DISABLE de-opers at once and refuses /oper; ENABLE lets them back.
	b.send("AS OPER ember DISABLE on holiday")
	e.expect(" 396 ember 127.0.0.1 :is now your displayed host")
	e.expect("*** Your oper entry was disabled (on holiday), so you are no longer an operator.")
	b.expect("ember may not /oper now (on holiday)")
	e.send("OPER ember " + emberPW)
	e.expect(" 491 ember :Your oper entry is disabled: on holiday")
	b.send("AS OPER LIST")
	b.expect("DISABLED: on holiday")
	b.send("AS OPER ember ENABLE")
	b.expect("ember may /oper again.")
	e.send("OPER ember " + emberPW)
	e.expect(" 381 ember ")

	// HISTORY / DIFF / UNDO.
	b.send("AS HISTORY")
	b.expect("OPER   ember")
	b.send("AS CLASS staff SET VHOST hive.example.org")
	e.expect(" 396 ember hive.example.org ")
	n := lastChange(t, b)
	b.send("AS DIFF " + n)
	b.expect("CLASS staff")
	b.expect("before: flags hk, title is a Staff Member, vhost staff.example.org")
	b.expect("after:  flags hk, title is a Staff Member, vhost hive.example.org")
	b.send("AS UNDO " + n)
	b.expect("Class staff is back to how it was before #" + n + ".")
	e.expect(" 396 ember staff.example.org ")

	b.send("AS OPER ember SET CLASS globop")
	e.expect(" 396 ember 127.0.0.1 ")
	n = lastChange(t, b)
	b.send("AS UNDO " + n)
	b.expect("The oper entry of ember is back to how it was before #" + n + ".")
	e.expect(" 396 ember staff.example.org ")

	b.send("AS SET max-users 321")
	b.expect("max-users is now 321")
	n = lastChange(t, b)
	b.send("AS UNDO " + n)
	b.expect("max-users is now ")

	// Anope's OS OPER on the same list.
	b.send("OS OPER LIST")
	b.expect(osLine("Boss", "ember                staff"))
	b.send("OS OPER INFO globop")
	b.expect(osLine("Boss", "Class globop: hk (hosts, kill)"))
	e.send("OS OPER DEL Boss")
	e.expect("You need oper flag a")
}

func TestOperVhostOverHostServ(t *testing.T) {
	s, _, _ := fileServer(t, "")
	b := boss(t, s)
	e := registered(t, s, "ember")
	b.send("HS SET ember ember.example.org")
	b.expect("vHost for")
	e.send("HS ON")
	e.expect(" 396 ember ember.example.org ")
	b.send("AS CLASS staff ADD FLAGS h")
	b.expect("Class staff")
	b.send("AS CLASS staff SET VHOST staff.example.org")
	b.expect("vhost staff.example.org")
	b.send("AS OPER ember ADD CLASS staff HOST *@* PASSWORD " + emberPW)
	b.expect("ember is an oper now")
	e.send("OPER ember " + emberPW)
	e.expect(" 396 ember staff.example.org ")
	e.send("MODE ember -o")
	e.expect(" 396 ember ember.example.org ") // the HostServ vhost comes back
}

func TestAdminHosts(t *testing.T) {
	s, _, _ := fileServer(t, "admin_hosts = [\"*@10.0.0.0/8\"]\n")
	b := registered(t, s, "Boss")
	b.send("OPER Boss " + operPW)
	b.expect(" 491 Boss :No oper entry for you from this host")

	s2, _, _ := fileServer(t, "admin_hosts = [\"*@127.0.0.0/8\"]\nadmin_vhost = \"admin.example.org\"\n")
	b2 := registered(t, s2, "Boss")
	b2.send("OPER Boss " + operPW)
	b2.expect(" 396 Boss admin.example.org ")
	b2.expect(" 381 Boss ")
	b2.send("AS OPER LIST")
	b2.expect("*@127.0.0.0/8 (server admin from beeircd.conf)")
}
