package server

import (
	"testing"

	"github.com/fhazal/beeircd/internal/passwd"
	"github.com/fhazal/beeircd/internal/store"
)

// A lower oper can't KILL, CHGHOST, CHGIDENT, CHGNAME or SA an oper who
// has a flag they lack; they can still change themselves, and the higher
// oper can act on them.
func TestOperRankGuard(t *testing.T) {
	s, _, _ := fileServer(t, "")
	b := boss(t, s)
	g := registered(t, s, "Guard")
	h, _ := passwd.Hash(operPW)
	s.onLoop(func() {
		a, _ := s.store.AccountByNick("Guard")
		s.store.SaveOper(&store.Oper{AccountID: a.ID, Account: a.Name, Flags: "khg", Hosts: []string{"*@*"}, PassHash: h, AddedBy: "test"})
	})
	g.send("OPER Guard " + operPW)
	g.expect(" 381 Guard ")

	for _, cmd := range []string{"KILL Boss :bye", "CHGHOST Boss evil.host", "CHGIDENT Boss evil", "CHGNAME Boss :evil"} {
		g.send(cmd)
		g.expect(" 481 Guard :Permission Denied - Boss has oper flag a, which you don't have.")
	}
	b.send("PING :still-here")
	b.expect("PONG")

	g.send("CHGNAME Guard :Night Watch")
	g.send("WHOIS Guard")
	g.expect(" 311 Guard Guard ")

	b.send("CHGHOST Guard watch.host")
	g.expect("watch.host")
	b.send("KILL Guard :done")
	g.expect("done")
}
