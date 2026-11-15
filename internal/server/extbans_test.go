package server

import "testing"

// The UnrealIRCd and InspIRCd extbans: each one does its job, whichever
// name it was given.
func TestExtbansActing(t *testing.T) {
	s := testServer(t, nil)
	op := login(t, s, "op")
	x := login(t, s, "x")
	joinAll("#t", op, x)

	// InspIRCd's and UnrealIRCd's names become the short form.
	op.send("MODE #t +b ~mute:nobody")
	op.expect("MODE #t +b ~m:nobody!*@*")
	op.send("MODE #t +b ~nickchange:nobody")
	op.expect("MODE #t +b ~n:nobody!*@*")
	op.send("MODE #t +e ~j:x")
	op.expect(" 696 op #t e ~j:x :This extban can't be used in +e")

	// ~n: no nick changes, but still talking; a plain ban stops both.
	op.send("MODE #t +b ~n:x!*@*")
	op.expect("+b ~n:x!*@*")
	x.send("NICK y")
	x.expect(" 437 x #t :Cannot change nickname while banned on channel")
	x.send("PRIVMSG #t :still here")
	op.expect("PRIVMSG #t :still here")
	op.send("MODE #t -b ~n:x!*@*")
	op.expect("-b ~n:x!*@*")

	// ~T: text bans for everyone below halfop.
	op.send("MODE #t +bb ~T:block:*http* ~T:censor:*darn*")
	op.expect("+bb ~T:block:*http* ~T:censor:*darn*")
	x.send("PRIVMSG #t :see http://spam")
	x.expect(" 404 x #t :Message blocked by a text ban (~T) of this channel")
	x.send("PRIVMSG #t :oh darnit")
	op.expect("PRIVMSG #t :oh <censored>")

	// msgbypass: +m, but x may talk.
	op.send("MODE #t +me ~m:moderated:x!*@*")
	op.expect("+me ~m:moderated:x!*@*")
	x.send("PRIVMSG #t :I may talk")
	op.expect("PRIVMSG #t :I may talk")
	op.send("MODE #t -m")
	op.expect("-m")

	// InspIRCd's acting bans.
	op.send("MODE #t +b ~noctcp:x!*@*")
	op.expect("+b ~noctcp:x!*@*")
	x.send("PRIVMSG #t :\x01VERSION\x01")
	x.expect(" 404 x #t :You may not send CTCPs to this channel")
	op.send("MODE #t +b ~stripcolor:x!*@*")
	op.expect("+b ~stripcolor:x!*@*")
	x.send("PRIVMSG #t :\x0304red\x03 text")
	op.expect("PRIVMSG #t :red text")

	// ~p: no part message.
	op.send("MODE #t +b ~p:x!*@*")
	op.expect("+b ~p:x!*@*")
	x.send("PART #t :goodbye all")
	op.expect(":x!~x@127.0.0.1 PART #t")
}

func TestExtbansOpsAndInherit(t *testing.T) {
	s := testServer(t, nil)
	op := login(t, s, "op")
	x := login(t, s, "x")
	y := login(t, s, "y")
	joinAll("#main", op)
	joinAll("#side", op)
	joinAll("#side", x)

	// ~j: may not join, nothing else.
	op.send("MODE #main +b ~j:y!*@*")
	op.expect("+b ~j:y!*@*")
	y.send("JOIN #main")
	y.expect(" 474 y #main ")

	// ~i:#main: #side uses #main's bans.
	op.send("MODE #side +b ~i:#main")
	op.expect("+b ~i:#main")
	y.send("JOIN #side")
	y.expect(" 474 y #side ")

	// ~c: people in a channel.
	op.send("MODE #main +b ~c:#side")
	op.expect("+b ~c:#side")
	x.send("JOIN #main")
	x.expect(" 474 x #main ")
	op.send("MODE #main -b ~c:#side")
	op.expect("-b ~c:#side")

	// ~nokick and ~blockinvite stop an op's KICK and INVITE.
	op.send("MODE #side +o x")
	op.expect("+o x")
	op.send("MODE #side +bb ~nokick:x!*@* ~blockinvite:x!*@*")
	op.expect("+bb ~nokick:x!*@* ~blockinvite:x!*@*")
	x.send("KICK #side op :bye")
	x.expect(" 482 x #side :You may not kick people in this channel")
	op.send("MODE #side +i")
	op.expect("+i")
	x.send("INVITE y #side")
	x.expect(" 518 x :Cannot invite at channel #side (you're banned from inviting)")
}
