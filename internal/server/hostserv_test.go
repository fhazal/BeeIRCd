package server

import (
	"strings"
	"testing"
)

// Step 1.5.7: HostServ as Anope 2.0.20.

func hsLine(nick, text string) string {
	return ":HostServ!services@irc.test.local NOTICE " + nick + " :" + text
}

func TestHostServSetOnOff(t *testing.T) {
	s, _, _ := fileServer(t, "")
	b := boss(t, s)
	u := registered(t, s, "user")
	w := login(t, s, "watch")
	w.send("CAP REQ :chghost")
	w.expect("ACK :chghost")
	joinAll("#room", u, w)

	// Nothing assigned yet; non-opers can't SET.
	u.send("HS ON")
	u.expect(hsLine("user", "Please contact an Operator to get a vHost assigned to this nick."))
	u.send("HS SET user x.example.org")
	u.expect(hsLine("user", "You need oper flag h (hosts) for that."))

	// Anope's checks.
	b.send("HS SET user bad_host.org")
	b.expect(hsLine("Boss", "A vHost must be in the format of a valid hostname."))
	b.send("HS SET user nodots")
	b.expect(hsLine("Boss", "A vHost must be in the format of a valid hostname."))
	b.send("HS SET user -start.org")
	b.expect(hsLine("Boss", "A vHost must be in the format of a valid hostname."))
	b.send("HS SET user " + strings.Repeat("a", 61) + ".org")
	b.expect(hsLine("Boss", "Error! The vHost is too long, please use a hostname shorter than 64 characters."))
	b.send("HS SET user waytoolongident@ok.org")
	b.expect(hsLine("Boss", "Error! The vHost ident is too long, please use an ident shorter than 10 characters."))
	b.send("HS SET nobody x.org")
	b.expect(hsLine("Boss", "Nick \x02nobody\x02 isn't registered."))

	// SET: stored, not shown yet (activate_on_set off).
	b.send("HS SET user cool.example.org")
	b.expect(hsLine("Boss", "VHost for \x02user\x02 set to \x02cool.example.org\x02."))
	u.quiet("cool.example.org")

	// ON shows it; chghost neighbours see CHGHOST.
	u.send("HS ON")
	u.expect(" 396 user cool.example.org :is now your displayed host")
	u.expect(hsLine("user", "Your vhost of \x02cool.example.org\x02 is now activated."))
	w.expect(":user!~user@127.0.0.1 CHGHOST ~user cool.example.org")
	u.send("WHOIS user")
	u.expect(" 311 user user ~user cool.example.org ")

	// OFF puts the normal host back.
	u.send("HS OFF")
	u.expect(" 396 user 127.0.0.1 :is now your displayed host")
	u.expect(hsLine("user", "Your vhost was removed and the normal cloaking restored."))
	w.expect(":user!~user@cool.example.org CHGHOST ~user 127.0.0.1")

	// Identifying shows it (Anope's OnUserLogin).
	u.send("NS LOGOUT")
	u.expect("You are no longer identified")
	u.send("NS IDENTIFY " + pw)
	u.expect(hsLine("user", "Your vhost of \x02cool.example.org\x02 is now activated."))

	// A vident changes the ident too; network bans still see the real one.
	b.send("HS SET user bee@ident.example.org")
	b.expect(hsLine("Boss", "VHost for \x02user\x02 set to \x02bee\x02@\x02ident.example.org\x02."))
	u.send("HS ON")
	w.expect(":user!~user@cool.example.org CHGHOST bee ident.example.org")
	u.send("WHOIS user")
	u.expect(" 311 user user bee ident.example.org ")

	// DEL takes it off whoever shows it.
	b.send("HS DEL user")
	b.expect(hsLine("Boss", "Vhost for \x02user\x02 removed."))
	w.expect(":user!bee@ident.example.org CHGHOST ~user 127.0.0.1")
	u.send("HS ON")
	u.expect(hsLine("user", "Please contact an Operator to get a vHost assigned to this nick."))
}

func TestHostServRequestsAndGroups(t *testing.T) {
	s, _, _ := fileServer(t, "")
	b := boss(t, s)
	u := registered(t, s, "user")
	u.send("NICK user2")
	u.expect("NICK")
	u.send("NS GROUP user")
	u.expect("is now part of the account user")

	u.send("HS REQUEST my.example.org")
	u.expect(hsLine("user2", "Your vHost has been requested."))
	b.send("HS WAITING")
	b.expect(hsLine("Boss", "  Number  Nick   Vhost           Created"))
	b.expect(hsLine("Boss", "  1       user2  my.example.org  "))
	b.expect(hsLine("Boss", "Displayed \x021\x02 records (\x021\x02 total)."))
	b.send("HS REJECT user2 not that one")
	b.expect(hsLine("Boss", "vHost for user2 has been rejected."))
	b.send("HS ACTIVATE user2")
	b.expect(hsLine("Boss", "No request for nick user2 found."))

	u.send("HS REQUEST other.example.org")
	u.expect("Your vHost has been requested.")
	b.send("HS ACTIVATE user2")
	b.expect(hsLine("Boss", "vHost for user2 has been activated."))
	u.send("HS ON")
	u.expect(hsLine("user2", "Your vhost of \x02other.example.org\x02 is now activated."))

	// GROUP copies it to every nick of the account.
	u.send("HS GROUP")
	u.expect(hsLine("user2", "All vhosts in the group \x02user\x02 have been set to \x02other.example.org\x02."))
	b.send("HS LIST")
	b.expect(hsLine("Boss", "  Number  Nick   Vhost              Creator  Created"))
	b.expect("  1       user   other.example.org  Boss     ")
	b.expect("  2       user2  other.example.org  Boss     ")
	b.expect(hsLine("Boss", "Displayed all records (count: \x022\x02)."))
	b.send("HS LIST #2-2")
	b.expect("  2       user2  other.example.org")
	b.expect(hsLine("Boss", "Displayed records from \x022\x02 to \x022\x02."))
	b.send("HS LIST #3-1")
	b.expect(hsLine("Boss", "Incorrect range specified. The correct syntax is \x02#\x1ffrom\x1f-\x1fto\x1f\x02."))

	// DELALL clears the group; dropping a nick drops its vhost.
	b.send("HS DELALL user")
	b.expect(hsLine("Boss", "vhosts for group \x02user\x02 have been removed."))
	b.send("HS LIST")
	b.expect(hsLine("Boss", "No records to display."))
	b.send("HS SETALL user all.example.org")
	b.expect(hsLine("Boss", "VHost for group \x02user\x02 set to \x02all.example.org\x02."))
	u.send("HS ON")
	u.expect(" 396 user2 all.example.org ")
	u.send("NS DROP user2") // a grouped nick drops at once (2.0.20)
	u.expect(" 396 user2 127.0.0.1 :is now your displayed host")
	u.expect("user2 has been dropped.")
	b.send("HS LIST")
	b.expect("  1       user  all.example.org  Boss     ")
	b.expect(hsLine("Boss", "Displayed all records (count: \x021\x02)."))
}
