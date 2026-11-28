package server

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/fhazal/beeircd/internal/irc"
)

// Our own server notices carry the unrealircd.org/json-log tag for opers
// with message-tags: valid JSON with UnrealIRCd's fields.
func TestJSONLogTag(t *testing.T) {
	s, _, _ := fileServer(t, "")
	b := boss(t, s)
	b.send("CAP REQ :message-tags")
	b.expect("ACK :message-tags")
	b.send("MODE Boss +s +c")
	b.sync()

	login(t, s, "guest")
	line := b.expect("*** CONNECT: Client connecting")
	m, err := irc.Parse(line)
	if err != nil {
		t.Fatal(err)
	}
	var ev map[string]string
	if err := json.Unmarshal([]byte(m.Tags["unrealircd.org/json-log"]), &ev); err != nil {
		t.Fatalf("no JSON in %q: %v", line, err)
	}
	for k, want := range map[string]string{"level": "info", "subsystem": "connect", "event_id": "LOCAL_CLIENT_CONNECT", "log_source": "irc.test.local"} {
		if ev[k] != want {
			t.Errorf("%s = %q, want %q (%s)", k, ev[k], want, line)
		}
	}
	if !strings.HasPrefix(ev["msg"], "Client connecting on port ") || !strings.HasSuffix(ev["timestamp"], "Z") {
		t.Errorf("msg/timestamp: %v", ev)
	}

	// An oper without message-tags gets the plain notice.
	f := registered(t, s, "fern")
	b.send("AS OPER fern ADD CLASS netadmin HOST *@* PASSWORD " + emberPW)
	b.expect("fern is an oper now")
	f.send("OPER fern " + emberPW)
	f.expect(" 381 fern ")
	f.send("MODE fern +s +c")
	f.sync()
	login(t, s, "guest2")
	if l := f.expect("*** CONNECT: Client connecting"); strings.HasPrefix(l, "@") {
		t.Errorf("tag without message-tags: %s", l)
	}
	b.expect("*** CONNECT: Client connecting")
}

func TestSnoEvent(t *testing.T) {
	for _, c := range []struct {
		letter        byte
		text, sub, id string
	}{
		{'k', "Local kill by Boss", "kill", "KILL_COMMAND"},
		{'Q', "x", "connect", "REMOTE_CLIENT_DISCONNECT"},
		{'l', "x", "link", "LINK"},
		{'a', "RPC tool: user.kill", "rpc", "RPC_CALL"},
		{'a', "anything", "server", "NOTICE"},
	} {
		if sub, id := snoEvent(c.letter, c.text); sub != c.sub || id != c.id {
			t.Errorf("snoEvent(%c, %q) = %s %s, want %s %s", c.letter, c.text, sub, id, c.sub, c.id)
		}
	}
}

// An UnrealIRCd SLOG keeps its own json-log tag for our opers.
func TestJSONLogTagFromUnreal(t *testing.T) {
	hub, hop, _, _ := globopsTree(t)
	hop.send("CAP REQ :message-tags")
	hop.expect("ACK :message-tags")
	hop.send("MODE hubop +s +a")
	hop.send("PING :umode")
	hop.expect(" :umode")
	hub.fromLinkTo(t, "a.test.local", `@unrealircd.org/json-log={"msg":"hi"} :0AA SLOG info config CONFIG_X :hello from unreal`)
	if l := hop.expect("hello from unreal"); !strings.Contains(l, `unrealircd.org/json-log={"msg":"hi"}`) {
		t.Errorf("Unreal's tag lost: %s", l)
	}
}
