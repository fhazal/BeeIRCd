package link

import (
	"strings"
	"testing"

	"github.com/fhazal/beeircd/internal/irc"
)

// Fuzz targets for the server-link parser. Longer runs:
//   go test -run '^$' -fuzz=FuzzLinkLine -fuzztime=5m ./internal/link/
// scripts/fuzz.sh runs every target for an hour.

func fuzzCodec(bee bool) *unrealCodec {
	return &unrealCodec{bee: bee, paramModes: "kfLlFH", listModes: "beI", alwaysModes: "kLf", setModes: "lFH", rankModes: "qaohv"}
}

// clean checks that encoded lines can't break the framing or the next line.
func clean(t *testing.T, what string, lines []string) {
	t.Helper()
	// Every link line passes irc.CleanFraming when sent; it must then
	// still be one line that parses, with a command.
	for _, l := range lines {
		w := irc.CleanFraming(l)
		if strings.ContainsAny(w, "\r\n\x00") {
			t.Fatalf("%s: framing left in %q", what, w)
		}
		if m, err := irc.Parse(w); err != nil || m.Command == "" {
			t.Fatalf("%s encodes to %q, which doesn't parse: %v", what, w, err)
		}
	}
}

// roundTrip decodes line and encodes what came out again: neither may
// panic, and what is sent on must stay one clean line each.
func roundTrip(t *testing.T, line string) {
	m, err := irc.Parse(line)
	if err != nil {
		return
	}
	if m.Source == "" || strings.ContainsAny(m.Source, "\r\n\x00 ") {
		// Decoding must still not panic; the server re-encodes only from
		// its own events, which always name a source.
		fuzzCodec(false).Decode(m)
		return
	}
	for _, bee := range []bool{false, true} {
		c := fuzzCodec(bee)
		switch it := c.Decode(m).(type) {
		case nil:
		case *User:
			clean(t, line, c.User(*it, "0AB"))
		case *Channel:
			clean(t, line, c.Channel(*it, "0AB"))
		case *Topic:
			clean(t, line, []string{c.Topic(*it, "0AB")})
		default:
			clean(t, line, c.Encode(it))
		}
	}
}

var linkSeeds = []string{
	":0AB UID bee 1 1700000000 ~bee host.example 0ABAAAAAA Bee +ixz * cloak.example fwAAAQ== :The Bee",
	":0AB UID bee 1 1700000000 ~bee host.example 0ABAAAAAA 0 +i vhost.example * * :x",
	":0AB SJOIN 1700000000 #hive +ntk key :@0ABAAAAAA +0ABAAAAAB &*!*@spam \"*!*@ok 'inv!*@*",
	":0AB SJOIN 1700000000 #hive +lfL 10 [5j]:15 #over :*~@%+0ABAAAAAA",
	":0AB SJOIN 1700000000 #hive + :",
	":0ABAAAAAA MODE #hive +ovbk-l 0ABAAAAAB 0ABAAAAAB *!*@x key",
	":0ABAAAAAA MODE 0ABAAAAAA +iw",
	":0AB TKL + G ~bot *.example oper 1700000000 1800000000 :no bots",
	":0AB TKL + Q * *Serv oper 0 1700000000 :reserved",
	":0AB TKL - Z * 203.0.113.0/24 oper",
	":0AB TKL + n * eW91IHNwYW0= oper 0 1700000000 :realname",
	":0AB SVSLOGIN * 0ABAAAAAA Bee",
	":0AB SVSNICK 0ABAAAAAA 0ABAAAAAA 0",
	":0AB SVSJOIN 0ABAAAAAA #a,#b keys",
	":0ABAAAAAA TOPIC #hive 0ABAAAAAA 1700000000 :topic",
	":0ABAAAAAA KICK #hive 0ABAAAAAB :out",
	":0ABAAAAAA PRIVMSG #hive :hello",
	"@time=2026-11-12T00:00:00.000Z :0ABAAAAAA NOTICE $*.example :network",
	":0ABAAAAAA CHGHOST 0ABAAAAAB new.host",
	":0AB 311 0ABAAAAAA bee ~bee host * :The Bee",
	":0AB SVSKILL 0ABAAAAAA :bye",
}

func FuzzLinkLine(f *testing.F) {
	for _, s := range linkSeeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, line string) { roundTrip(t, line) })
}

// FuzzLinkUID builds UID lines from fuzzed fields (a burst).
func FuzzLinkUID(f *testing.F) {
	f.Add("bee", "1700000000", "~bee", "host.example", "0ABAAAAAA", "Bee", "+ixz", "cloak", "fwAAAQ==", "The Bee")
	f.Add("", "", "", "", "", "", "", "", "", "")
	f.Fuzz(func(t *testing.T, nick, ts, ident, host, uid, account, modes, cloak, ip, gecos string) {
		roundTrip(t, ":0AB UID "+nick+" 1 "+ts+" "+ident+" "+host+" "+uid+" "+account+" "+modes+" * "+cloak+" "+ip+" :"+gecos)
	})
}

// FuzzLinkSJOIN builds SJOIN lines from fuzzed fields (a burst).
func FuzzLinkSJOIN(f *testing.F) {
	f.Add("1700000000", "#hive", "+ntlk", "10 key", "@0ABAAAAAA +0ABAAAAAB &*!*@spam")
	f.Add("0", "#", "+", "", "")
	f.Fuzz(func(t *testing.T, ts, channel, modes, args, members string) {
		roundTrip(t, ":0AB SJOIN "+ts+" "+channel+" "+modes+" "+args+" :"+members)
	})
}

// Found by FuzzLinkLine: "TKL + :" (an empty ban type) crashed the
// decoder, and with it the whole server.
func TestDecodeBanEmptyType(t *testing.T) {
	for _, line := range []string{":0AB TKL + :", ":0AB TKL - ", ":0AB TKL + GG * * x", ":0AB TKL"} {
		m, _ := irc.Parse(line)
		if got := fuzzCodec(true).Decode(m); got != nil {
			t.Errorf("%q decoded to %#v", line, got)
		}
	}
}
