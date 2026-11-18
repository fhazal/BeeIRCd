package server

import "testing"

// QA 1.2.0 bug 1: on a fresh install the admin from beeircd.conf sets their
// own title, WHOIS lines and vhost; nobody else can, and flags stay locked.
func TestOwnerSetsOwnLook(t *testing.T) {
	s, _, _ := fileServer(t, "")
	b := boss(t, s)
	b.send("AS OPER Boss SET TITLE Net Boss")
	b.expect(asLine("Boss", `Boss: title is now Net Boss.`))
	b.send("AS OPER Boss SET SWHOIS is the Founder|keeps the hive")
	b.expect(asLine("Boss", "Boss: WHOIS lines are now is the Founder | keeps the hive."))
	b.send("WHOIS Boss")
	b.expect(" 313 Boss Boss :is an IRC Operator (Net Boss)")
	b.expect(" 320 Boss Boss :is the Founder")
	b.expect(" 320 Boss Boss :keeps the hive")
	b.send("AS OPER Boss SET VHOST staff.test.local")
	b.expect(" 396 Boss staff.test.local ")
	b.expect(asLine("Boss", "Boss: vhost is now staff.test.local."))

	// The look entry stays out of the list: the admin has one line.
	b.send("AS OPER LIST")
	b.expect(asLine("Boss", "1 oper."))

	// Flags still come from beeircd.conf.
	b.send("AS OPER Boss SET FLAGS g")
	b.expect(asLine("Boss", "Boss is the server admin from beeircd.conf; change it there."))

	ember := registered(t, s, "ember")
	b.send("AS OPER ember ADD FLAGS ag HOST *@*")
	b.expect(asLine("Boss", "ember is an oper now"))
	ember.send("AS OPER Boss SET TITLE Usurper") // not opered: AdminServ refuses
	ember.expect("AdminServ")
	b.send("AS OPER ember SET PASSWORD " + operPW)
	b.expect(asLine("Boss", "ember has a new oper password."))
	ember.send("OPER ember " + operPW)
	ember.expect(" 381 ember ")
	ember.send("AS OPER Boss SET TITLE Usurper")
	ember.expect(asLine("ember", "Only Boss can change their own title, WHOIS lines and vhost."))

	b.send("AS OPER Boss SET TITLE OFF")
	b.expect(asLine("Boss", "Boss: title is now none."))
	b.send("WHOIS Boss")
	b.expect(" 313 Boss Boss :is a Network Administrator") // the class title again
}
