package server

import "testing"

// A client that asks for standard-replies gets FAIL/WARN/NOTE with a code
// for BeeIRCd's own errors; others keep the server notice.
func TestStandardReplies(t *testing.T) {
	s, _, _ := fileServer(t, "")
	b := boss(t, s)
	b.send("CHGHOST Boss bad_host!")
	b.expect("NOTICE Boss :*** /ChgHost Error")

	b.send("CAP REQ :standard-replies")
	b.expect("CAP Boss ACK :standard-replies")
	b.send("CHGHOST Boss bad_host!")
	b.expect(" FAIL CHGHOST INVALID_HOST bad_host! :A host may only contain")
	b.send("KLINE +soon *@x.example")
	b.expect(":" + s.cfg.Server.Name + " FAIL KLINE INVALID_DURATION +soon :+soon isn't a time.")
	b.send("UNKLINE *@nothing.example")
	b.expect(" FAIL UNKLINE NO_SUCH_KLINE *@nothing.example :There is no K-line on *@nothing.example.")
	b.send("OPER Boss whatever")
	b.expect(" FAIL OPER ALREADY_OPER :You are already an IRC operator")

	e := capLogin(t, s, "ember", "standard-replies")
	e.send("JOIN #w")
	e.expect(" 366 ember #w ")
	b.send("SATOPIC #w :same")
	e.expect(" TOPIC #w :same")
	b.send("SATOPIC #w :same")
	b.expect(" WARN SATOPIC TOPIC_UNCHANGED #w :The topic on #w is already")
}
