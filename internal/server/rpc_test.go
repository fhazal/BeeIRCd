package server

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fhazal/beeircd/internal/config"
	"github.com/fhazal/beeircd/internal/passwd"
)

// The JSON-RPC admin API: HTTPS, WebSocket and the socket file.

const rpcPass = "Tool-pass-2026"

var rpcHash = sync.OnceValue(func() string {
	h, err := passwd.Hash(rpcPass)
	if err != nil {
		panic(err)
	}
	return h
})

// rpcServer has the API on, with "tool" (every method) and "viewer"
// (user.list and stats.* only).
func rpcServer(t *testing.T) *Server {
	t.Helper()
	sock := filepath.Join(t.TempDir(), "rpc.sock")
	return testServer(t, func(c *config.Config) {
		c.RPC = config.RPC{Listen: "127.0.0.1:0", Socket: sock, Users: []config.RPCUser{
			{Name: "tool", Password: rpcHash()},
			{Name: "viewer", Password: rpcHash(), Allow: []string{"user.list", "stats.*"}},
		}}
	})
}

type rpcAnswer struct {
	Method string          `json:"method"`
	ID     json.RawMessage `json:"id"`
	Result json.RawMessage `json:"result"`
	Error  *rpcError       `json:"error"`
}

// rpcPost sends body to the API as user and returns the HTTP status and
// the answer.
func rpcPost(t *testing.T, s *Server, method, path, user, pass, body string) (int, rpcAnswer) {
	t.Helper()
	cl := &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, DisableKeepAlives: true}}
	req, err := http.NewRequest(method, "https://"+s.RPCAddr().String()+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if user != "" {
		req.SetBasicAuth(user, pass)
	}
	resp, err := cl.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var a rpcAnswer
	if resp.StatusCode == http.StatusOK {
		if err := json.Unmarshal(raw, &a); err != nil {
			t.Fatalf("answer %q: %v", raw, err)
		}
	}
	return resp.StatusCode, a
}

// call runs one method as tool; params is a JSON object ("" = none).
func rpcCallAs(t *testing.T, s *Server, user, method, params string) rpcAnswer {
	t.Helper()
	if params == "" {
		params = "{}"
	}
	code, a := rpcPost(t, s, "POST", "/api", user, rpcPass, `{"jsonrpc":"2.0","id":7,"method":"`+method+`","params":`+params+`}`)
	if code != http.StatusOK {
		t.Fatalf("%s: HTTP %d", method, code)
	}
	if a.Method != method || string(a.ID) != "7" {
		t.Errorf("%s: answer names %q id %s", method, a.Method, a.ID)
	}
	return a
}

// rpcOK is the result of a call that must work.
func rpcOK(t *testing.T, s *Server, method, params string) string {
	t.Helper()
	a := rpcCallAs(t, s, "tool", method, params)
	if a.Error != nil {
		t.Fatalf("%s %s: error %d %s", method, params, a.Error.Code, a.Error.Message)
	}
	return string(a.Result)
}

// rpcFails checks that a call is refused with code.
func rpcFails(t *testing.T, s *Server, method, params string, code int) {
	t.Helper()
	a := rpcCallAs(t, s, "tool", method, params)
	if a.Error == nil || a.Error.Code != code {
		t.Errorf("%s %s: want error %d, got %+v %s", method, params, code, a.Error, a.Result)
	}
}

func has(t *testing.T, what, got string, wants ...string) {
	t.Helper()
	for _, w := range wants {
		if !strings.Contains(got, w) {
			t.Errorf("%s lacks %s: %s", what, w, got)
		}
	}
}

func TestRPCAccess(t *testing.T) {
	s := rpcServer(t)
	if code, _ := rpcPost(t, s, "POST", "/api", "", "", "{}"); code != http.StatusUnauthorized {
		t.Errorf("no name: HTTP %d", code)
	}
	if code, _ := rpcPost(t, s, "POST", "/other", "tool", rpcPass, "{}"); code != http.StatusNotFound {
		t.Errorf("wrong path: HTTP %d", code)
	}
	if code, _ := rpcPost(t, s, "GET", "/api", "tool", rpcPass, ""); code != http.StatusMethodNotAllowed {
		t.Errorf("GET: HTTP %d", code)
	}
	has(t, "rpc.info", rpcOK(t, s, "rpc.info", ""), `"user.list"`, `"server_ban.add"`, `"module":"rpc/spamfilter"`)
	if got := rpcOK(t, s, "rpc.set_issuer", `{"name":"Belon"}`); got != "true" {
		t.Errorf("set_issuer: %s", got)
	}
	rpcFails(t, s, "no.such_method", "", rpcNoMethod)
	rpcFails(t, s, "user.get", `["Bee"]`, rpcInvalidParams)
	// An empty array is no params: PHP's json_encode writes an empty list so.
	has(t, "empty array params", rpcOK(t, s, "server_ban_exception.list", `[ ]`), `"list":`)
	rpcFails(t, s, "user.get", `{"nick":5}`, rpcInvalidParams)
	if _, a := rpcPost(t, s, "POST", "/api", "tool", rpcPass, "{not json"); a.Error == nil || a.Error.Code != rpcParseError {
		t.Errorf("bad JSON: %+v", a.Error)
	}
	if _, a := rpcPost(t, s, "POST", "/api", "tool", rpcPass, `{"jsonrpc":"2.0","method":"user.list"}`); a.Error == nil || a.Error.Code != rpcInvalidRequest {
		t.Errorf("no id: %+v", a.Error)
	}

	// viewer may list people and read the numbers, nothing else.
	if a := rpcCallAs(t, s, "viewer", "user.list", ""); a.Error != nil {
		t.Errorf("viewer user.list: %+v", a.Error)
	}
	if a := rpcCallAs(t, s, "viewer", "stats.get", ""); a.Error != nil {
		t.Errorf("viewer stats.get: %+v", a.Error)
	}
	if a := rpcCallAs(t, s, "viewer", "user.kill", `{"nick":"x","reason":"y"}`); a.Error == nil || a.Error.Code != rpcDenied {
		t.Errorf("viewer user.kill: %+v", a.Error)
	}
	if a := rpcCallAs(t, s, "viewer", "rpc.info", ""); a.Error != nil {
		t.Errorf("viewer rpc.info: %+v", a.Error)
	}
	// The live log needs a connection that stays open.
	rpcFails(t, s, "log.subscribe", `{"sources":["all"]}`, rpcInvalidRequest)

	// Wrong passwords lock the address out for a minute.
	for i := 0; i < rpcFailMax; i++ {
		if code, _ := rpcPost(t, s, "POST", "/api", "tool", "wrong-password", "{}"); code != http.StatusUnauthorized {
			t.Fatalf("wrong password %d: HTTP %d", i, code)
		}
	}
	if code, _ := rpcPost(t, s, "POST", "/api", "tool", rpcPass, "{}"); code != http.StatusTooManyRequests {
		t.Errorf("after %d wrong passwords: HTTP %d", rpcFailMax, code)
	}
}

func TestRPCUsersAndChannels(t *testing.T) {
	s := rpcServer(t)
	bee, ember := login(t, s, "Bee"), login(t, s, "ember")
	bee.send("JOIN #x")
	bee.expect(" 366 ")

	has(t, "user.list", rpcOK(t, s, "user.list", ""), `"name":"Bee"`, `"name":"ember"`, `"hostname":"127.0.0.1"`)
	has(t, "user.get", rpcOK(t, s, "user.get", `{"nick":"bee"}`), `"client":{`, `"details":"Bee!~bee@127.0.0.1"`,
		`"channels":[{"level":"o","name":"#x"}]`, `"username":"~bee"`, `"servername":"irc.test.local"`, `"connected_since":"20`,
		`"cloakedhost":"127.0.0.1"`) // always there: the panel's kickban bans *!*@cloakedhost
	rpcFails(t, s, "user.get", `{"nick":"nobody"}`, rpcNotFound)
	rpcFails(t, s, "user.get", `{}`, rpcInvalidParams)

	rpcOK(t, s, "user.set_nick", `{"nick":"ember","newnick":"amber"}`)
	ember.expect(":ember!~ember@127.0.0.1 NICK amber")
	rpcFails(t, s, "user.set_nick", `{"nick":"amber","newnick":"Bee"}`, rpcExists)
	rpcFails(t, s, "user.set_nick", `{"nick":"amber","newnick":"1bad"}`, rpcInvalidName)
	rpcFails(t, s, "user.set_nick", `{"nick":"NickServ","newnick":"x"}`, rpcRefused)
	rpcOK(t, s, "user.set_vhost", `{"nick":"amber","vhost":"hive.example.org"}`)
	ember.expect(" 396 amber hive.example.org ")
	rpcFails(t, s, "user.set_vhost", `{"nick":"amber","vhost":"bad host"}`, rpcInvalidName)
	rpcOK(t, s, "user.set_username", `{"nick":"amber","username":"queen"}`)
	rpcOK(t, s, "user.set_realname", `{"nick":"amber","realname":"Amber of the Hive"}`)
	rpcOK(t, s, "user.set_mode", `{"nick":"amber","modes":"+B"}`)
	ember.expect("MODE amber +B")
	rpcFails(t, s, "user.set_mode", `{"nick":"amber","modes":"+o"}`, rpcRefused)
	has(t, "user.get", rpcOK(t, s, "user.get", `{"nick":"amber"}`), `"vhost":"hive.example.org"`, `"username":"queen"`,
		`"realname":"Amber of the Hive"`, `"details":"amber!~ember@127.0.0.1"`)

	rpcOK(t, s, "user.join", `{"nick":"amber","channel":"#x"}`)
	bee.expect(":amber!queen@hive.example.org JOIN #x")
	has(t, "channel.list", rpcOK(t, s, "channel.list", ""), `"name":"#x"`, `"num_users":2`)
	has(t, "channel.get", rpcOK(t, s, "channel.get", `{"channel":"#X"}`), `"channel":{`, `"modes":"nt"`, `"level":"o"`, `"name":"amber"`, `"bans":[]`)
	if got := rpcOK(t, s, "channel.get", `{"channel":"#x"}`); strings.Contains(got, `"hostname"`) {
		t.Errorf("channel.get at level 3 has the members' details: %s", got)
	}
	// Level 4, as unrealircd-webpanel asks: each member is a whole user.
	has(t, "channel.get level 4", rpcOK(t, s, "channel.get", `{"channel":"#x","object_detail_level":4}`),
		`"level":"o"`, `"hostname":"127.0.0.1"`, `"details":"amber!~ember@127.0.0.1"`, `"realname":"Amber of the Hive"`)
	rpcFails(t, s, "channel.get", `{"channel":"#nowhere"}`, rpcNotFound)
	rpcOK(t, s, "channel.set_mode", `{"channel":"#x","modes":"+ml","parameters":"25"}`)
	bee.expect("MODE #x +ml 25")
	rpcOK(t, s, "channel.set_mode", `{"channel":"#x","modes":"+b","parameters":"*!*@spam.example.net"}`)
	bee.expect("MODE #x +b *!*@spam.example.net")
	rpcFails(t, s, "channel.set_mode", `{"channel":"#x","modes":"+b","parameters":"~nosuchextban:x"}`, rpcInvalidParams)
	has(t, "channel.get", rpcOK(t, s, "channel.get", `{"channel":"#x"}`), `"bans":[{"name":"*!*@spam.example.net"`, `"modes":"lmnt 25"`)
	rpcOK(t, s, "channel.set_topic", `{"channel":"#x","topic":"Hello from the API","set_by":"Belon"}`)
	bee.expect("TOPIC #x :Hello from the API")
	has(t, "channel.get", rpcOK(t, s, "channel.get", `{"channel":"#x"}`), `"topic":"Hello from the API"`, `"topic_set_by":"Belon"`)
	rpcOK(t, s, "user.part", `{"nick":"amber","channel":"#x"}`)
	bee.expect(":amber!queen@hive.example.org PART #x")
	rpcFails(t, s, "channel.kick", `{"channel":"#x","nick":"amber","reason":"bye"}`, rpcNotFound)
	rpcOK(t, s, "user.join", `{"nick":"amber","channel":"#x"}`)
	rpcOK(t, s, "channel.kick", `{"channel":"#x","nick":"amber","reason":"bye"}`)
	bee.expect(":irc.test.local KICK #x amber :bye")

	has(t, "stats.get", rpcOK(t, s, "stats.get", ""), `"channel":{"total":1}`, `"server_ban":{`, `"oper":0`)
	has(t, "server.list", rpcOK(t, s, "server.list", ""), `"name":"irc.test.local"`, `"id":"0BE"`, `"boot_time":"20`)
	has(t, "server.get", rpcOK(t, s, "server.get", ""), `"server":{`, `"name":"irc.test.local"`,
		`"chanmodes":["beI","k","`, `"usermodes":"`, `"protocol":6100`) // the panel's mode editor and server page
	rpcFails(t, s, "server.get", `{"server":"no.such.server"}`, rpcNotFound)
	has(t, "server.module_list", rpcOK(t, s, "server.module_list", ""), `"name":"beeircd"`)

	rpcOK(t, s, "user.quit", `{"nick":"amber","reason":"see you"}`)
	ember.expect("ERROR :Closing Link: 127.0.0.1 (see you)")
	has(t, "whowas.get", rpcOK(t, s, "whowas.get", `{"nick":"amber"}`), `"name":"amber"`, `"realname":"Amber of the Hive"`)
	rpcOK(t, s, "rpc.set_issuer", `{"name":"x"}`) // per connection: no effect on the next call
	rpcOK(t, s, "user.kill", `{"nick":"Bee","reason":"enough"}`)
	bee.expect("ERROR :Closing Link: 127.0.0.1 (Killed (tool (enough)))")
}

func TestRPCBansAndSpamfilter(t *testing.T) {
	s := rpcServer(t)
	has(t, "server_ban.add", rpcOK(t, s, "server_ban.add", `{"name":"*@192.0.2.9","type":"gline","reason":"spam run","duration_string":"1h"}`),
		`"tkl":{`, `"type":"gline"`, `"name":"*@192.0.2.9"`, `"reason":"spam run"`, `"set_by":"tool"`, `"expire_at":"20`)
	rpcFails(t, s, "server_ban.add", `{"name":"*@192.0.2.9","type":"gline","reason":"again"}`, rpcExists)
	rpcFails(t, s, "server_ban.add", `{"name":"*@192.0.2.10","type":"nonsense","reason":"x"}`, rpcInvalidParams)
	rpcFails(t, s, "server_ban.add", `{"name":"*@192.0.2.10","type":"gline","reason":"x","duration_string":"soon"}`, rpcInvalidParams)
	rpcFails(t, s, "server_ban.add", `{"name":"*@*","type":"gline","reason":"everyone"}`, rpcInvalidName)
	rpcOK(t, s, "server_ban.add", `{"name":"192.0.2.77","type":"gzline","reason":"bad address","set_by":"Belon"}`)
	rpcOK(t, s, "server_ban.add", `{"name":"pest@198.51.100.7","type":"kline","reason":"local only","duration_string":"600"}`)
	list := rpcOK(t, s, "server_ban.list", "")
	has(t, "server_ban.list", list, `"name":"*@192.0.2.9"`, `"type":"gzline"`, `"set_by":"Belon"`, `"type":"kline"`, `"duration_string":"permanent"`,
		`"expire_at_string":"Never"`, `"set_at_string":"`+time.Now().UTC().Format("Mon Jan _2")) // asctime, as UnrealIRCd
	has(t, "server_ban.get", rpcOK(t, s, "server_ban.get", `{"name":"*@192.0.2.9","type":"gline"}`), `"reason":"spam run"`)
	has(t, "server_ban.del", rpcOK(t, s, "server_ban.del", `{"name":"*@192.0.2.9","type":"gline"}`), `"name":"*@192.0.2.9"`)
	rpcFails(t, s, "server_ban.get", `{"name":"*@192.0.2.9","type":"gline"}`, rpcNotFound)
	rpcOK(t, s, "server_ban.del", `{"name":"pest@198.51.100.7","type":"kline"}`)
	rpcOK(t, s, "server_ban.del", `{"name":"192.0.2.77","type":"zline"}`)
	has(t, "server_ban.list", rpcOK(t, s, "server_ban.list", ""), `"list":[]`)

	has(t, "exception add", rpcOK(t, s, "server_ban_exception.add", `{"name":"*@192.0.2.50","reason":"our bridge"}`), `"type":"except"`, `"name":"*@192.0.2.50"`)
	has(t, "exception list", rpcOK(t, s, "server_ban_exception.list", ""), `"reason":"our bridge"`)
	rpcOK(t, s, "server_ban_exception.get", `{"name":"*@192.0.2.50"}`)
	rpcOK(t, s, "server_ban_exception.del", `{"name":"*@192.0.2.50"}`)
	rpcFails(t, s, "server_ban_exception.del", `{"name":"*@192.0.2.50"}`, rpcNotFound)
	// unrealircd-webpanel's durations: weeks, days and hours in one, 0w0d0h until removed.
	has(t, "joined units", rpcOK(t, s, "server_ban_exception.add", `{"name":"*@192.0.2.51","reason":"a day","duration_string":"0w1d0h"}`),
		`"expire_at_string":"`+time.Now().Add(24*time.Hour).UTC().Format("Mon Jan _2"))
	has(t, "all zero", rpcOK(t, s, "server_ban_exception.add", `{"name":"*@192.0.2.52","reason":"always","duration_string":"0w0d0h"}`),
		`"expire_at_string":"Never"`)

	rule := `"name":"*buy cheap pills now*","match_type":"simple","spamfilter_targets":"cp","ban_action":"block"`
	has(t, "spamfilter.add", rpcOK(t, s, "spamfilter.add", `{`+rule+`,"reason":"pill spam"}`),
		`"type":"spamfilter"`, `"name":"*buy cheap pills now*"`, `"ban_action":"block"`, `"reason":"pill spam"`, `"hits":0`)
	rpcFails(t, s, "spamfilter.add", `{`+rule+`,"reason":"again"}`, rpcExists)
	rpcFails(t, s, "spamfilter.add", `{"name":"x","match_type":"simple","spamfilter_targets":"c","ban_action":"explode","reason":"r"}`, rpcInvalidParams)
	has(t, "spamfilter.list", rpcOK(t, s, "spamfilter.list", ""), `"name":"*buy cheap pills now*"`, `"set_at_string":"`, `"expire_at_string":"Never"`)
	has(t, "spamfilter.get", rpcOK(t, s, "spamfilter.get", `{`+rule+`}`), `"match_type":"simple"`)
	bee, ember := login(t, s, "Bee"), login(t, s, "ember")
	ember.send("PRIVMSG Bee :buy cheap pills now, friend")
	ember.expect("pill spam")
	bee.quiet("pills")
	has(t, "stats.get", rpcOK(t, s, "stats.get", ""), `"spamfilter":1`)
	rpcOK(t, s, "spamfilter.del", `{`+rule+`}`)
	rpcFails(t, s, "spamfilter.get", `{`+rule+`}`, rpcNotFound)
}

// The socket file: no password, one request per line, and the live log.
func TestRPCSocketAndLog(t *testing.T) {
	s := rpcServer(t)
	conn, err := net.Dial("unix", s.cfg.RPC.Socket)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	r := bufio.NewReader(conn)
	var events []string
	ask := func(line string) string {
		t.Helper()
		conn.SetDeadline(time.Now().Add(5 * time.Second))
		if _, err := io.WriteString(conn, line+"\n"); err != nil {
			t.Fatal(err)
		}
		for {
			got, err := r.ReadString('\n')
			if err != nil {
				t.Fatalf("after %s: %v", line, err)
			}
			if !strings.Contains(got, `"method":"log.event"`) {
				return got
			}
			events = append(events, got) // log lines come in between the answers
		}
	}
	has(t, "user.list", ask(`{"jsonrpc":"2.0","id":1,"method":"user.list"}`), `"id":1`, `"result":{"list":[`)
	has(t, "set_issuer", ask(`{"jsonrpc":"2.0","id":2,"method":"rpc.set_issuer","params":{"name":"Belon"}}`), `"result":true`)
	has(t, "subscribe", ask(`{"jsonrpc":"2.0","id":"three","method":"log.subscribe","params":{"sources":["connect","rpc"]}}`), `"id":"three"`, `"result":true`)
	login(t, s, "newbie")
	conn.SetDeadline(time.Now().Add(5 * time.Second))
	line, err := r.ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	has(t, "log event", line, `"method":"log.event"`, `"subsystem":"connect"`, `"event_id":"LOCAL_CLIENT_CONNECT"`, `Client connecting on port `, `newbie!~newbie@127.0.0.1 (127.0.0.1)`, `"log_source":"irc.test.local"`)

	// What changes something is in the log, with who asked.
	has(t, "kill", ask(`{"jsonrpc":"2.0","id":4,"method":"user.kill","params":{"nick":"newbie","reason":"test"}}`), `"id":4`, `"result":true`)
	has(t, "the log line of the kill", strings.Join(events, ""), `RPC Belon: user.kill {\"nick\":\"newbie\",\"reason\":\"test\"}`)
	has(t, "unsubscribe", ask(`{"jsonrpc":"2.0","id":5,"method":"log.unsubscribe"}`), `"result":true`)
	has(t, "log.list", ask(`{"jsonrpc":"2.0","id":6,"method":"log.list","params":{"sources":["!connect","all"]}}`), `"event_id":"RPC_CALL"`)
	if got := ask(`{"jsonrpc":"2.0","id":7,"method":"log.list","params":{"sources":["!connect","all"]}}`); strings.Contains(got, "LOCAL_CLIENT_CONNECT") {
		t.Errorf("!connect still lists connects: %s", got)
	}
	has(t, "bad line", ask(`nonsense`), `"code":-32700`)
}

// The upgrade works at /api and at / (where unrealircd-webpanel and the
// UnrealIRCd RPC libraries open it: wss://host:port).
func TestRPCWebSocket(t *testing.T) {
	s := rpcServer(t)
	for i, path := range []string{"/api", "/"} {
		raw, err := net.Dial("tcp", s.RPCAddr().String())
		if err != nil {
			t.Fatal(err)
		}
		conn := tls.Client(raw, &tls.Config{InsecureSkipVerify: true})
		t.Cleanup(func() { conn.Close() })
		conn.SetDeadline(time.Now().Add(5 * time.Second))
		k := make([]byte, 16)
		rand.Read(k)
		auth := base64.StdEncoding.EncodeToString([]byte("tool:" + rpcPass))
		io.WriteString(conn, "GET "+path+" HTTP/1.1\r\nHost: irc.test.local\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Version: 13\r\n"+
			"Sec-WebSocket-Key: "+base64.StdEncoding.EncodeToString(k)+"\r\nAuthorization: Basic "+auth+"\r\n\r\n")
		c := &wsc{t: t, conn: conn, r: bufio.NewReader(conn)}
		resp, err := http.ReadResponse(c.r, nil)
		if err != nil || resp.StatusCode != http.StatusSwitchingProtocols {
			t.Fatalf("upgrade at %s: %v %v", path, resp, err)
		}
		ask := func(line string) string {
			t.Helper()
			c.send(line)
			op, data := c.next()
			if op != 0x1 {
				t.Fatalf("frame type %d", op)
			}
			return string(bytes.TrimSpace(data))
		}
		has(t, "stats.get", ask(`{"jsonrpc":"2.0","id":1,"method":"stats.get"}`), `"id":1`, `"user":{`)
		has(t, "subscribe", ask(`{"jsonrpc":"2.0","id":2,"method":"log.subscribe","params":{"sources":["all"]}}`), `"result":true`)
		nick := fmt.Sprintf("visitor%d", i)
		login(t, s, nick)
		for {
			_, data := c.next()
			if strings.Contains(string(data), "): "+nick+"!~") {
				break
			}
		}
	}
}

func TestRPCConfig(t *testing.T) {
	hash := "$argon2id$v=19$m=65536,t=3,p=4$c2FsdA$aGFzaA"
	for name, r := range map[string]config.RPC{
		"listen without users": {Listen: "127.0.0.1:8600"},
		"a plain password":     {Users: []config.RPCUser{{Name: "tool", Password: "secret"}}},
		"a name with a space":  {Users: []config.RPCUser{{Name: "my tool", Password: hash}}},
		"the same name twice":  {Users: []config.RPCUser{{Name: "tool", Password: hash}, {Name: "TOOL", Password: hash}}},
		"a bad allow":          {Users: []config.RPCUser{{Name: "tool", Password: hash, Allow: []string{"everything"}}}},
		"a bad address":        {Users: []config.RPCUser{{Name: "tool", Password: hash, IPs: []string{"home"}}}},
	} {
		c := config.Config{RPC: r}
		if err := c.CheckRPC(); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
	u := config.RPCUser{Allow: []string{"user.*", "server_ban.list"}, IPs: []string{"192.0.2.0/24"}}
	for method, want := range map[string]bool{"user.kill": true, "server_ban.list": true, "server_ban.add": false, "users.x": false} {
		if u.Allows(method) != want {
			t.Errorf("Allows(%s) = %v", method, !want)
		}
	}
	// This machine may always call; ips adds other addresses.
	if !u.From(net.ParseIP("192.0.2.9")) || !u.From(net.ParseIP("127.0.0.1")) || u.From(net.ParseIP("203.0.113.1")) ||
		!(config.RPCUser{}).From(net.ParseIP("::1")) || (config.RPCUser{}).From(net.ParseIP("192.0.2.9")) {
		t.Error("From is wrong")
	}
}

// A refused address and an unknown name get the same answer and the same
// count, so a caller can't find out which names exist.
func TestRPCRefusalsLookAlike(t *testing.T) {
	s := testServer(t, func(c *config.Config) {
		c.RPC = config.RPC{Listen: "127.0.0.1:0", Users: []config.RPCUser{
			{Name: "faraway", Password: rpcHash(), IPs: []string{"10.250.77.20"}},
		}}
	})
	if rc, why := s.rpcLogin("faraway", rpcPass, net.ParseIP("10.250.77.20")); rc == nil {
		t.Fatalf("the allowed address is refused: %s", why)
	}
	known, unknown := net.ParseIP("10.250.77.30"), net.ParseIP("10.250.77.31")
	for i := 0; i < rpcFailMax; i++ {
		_, a := s.rpcLogin("faraway", rpcPass, known)
		_, b := s.rpcLogin("nobody", rpcPass, unknown)
		if a != b || a != "Wrong name or password." {
			t.Fatalf("try %d: %q and %q", i+1, a, b)
		}
	}
	_, a := s.rpcLogin("faraway", rpcPass, known)
	_, b := s.rpcLogin("nobody", rpcPass, unknown)
	if a != b || !strings.Contains(a, "Too many") {
		t.Fatalf("after the lock: %q and %q", a, b)
	}
	if rc, why := s.rpcLogin("faraway", rpcPass, net.ParseIP("10.250.77.20")); rc == nil {
		t.Fatalf("the allowed address is locked with the stranger: %s", why)
	}
}

// name_ban.* are the nick and channel forbids (Q-lines): a forbidden nick
// in use is changed to a guest nick, a forbidden channel is emptied.
func TestRPCNameBans(t *testing.T) {
	s := rpcServer(t)
	pest, ember := login(t, s, "Pestbot1"), login(t, s, "ember")
	ember.send("JOIN #warez")
	ember.expect(" 366 ")

	has(t, "name_ban.add nick", rpcOK(t, s, "name_ban.add", `{"name":"Pestbot*","reason":"spam bots","duration_string":"0w1d0h"}`),
		`"tkl":{`, `"type":"qline"`, `"type_string":"Q-Line"`, `"name":"Pestbot*"`, `"reason":"spam bots"`, `"set_by":"tool"`,
		`"expire_at":"20`, `"duration_string":"24h00m"`)
	pest.expect("That nick is forbidden by the network")
	has(t, "name_ban.add channel", rpcOK(t, s, "name_ban.add", `{"name":"#warez","reason":"no warez","set_by":"Belon"}`),
		`"name":"#warez"`, `"set_by":"Belon"`, `"expire_at_string":"Never"`)
	ember.expect("This channel is forbidden: no warez")

	rpcFails(t, s, "name_ban.add", `{"name":"pestbot*","reason":"again"}`, rpcExists)
	rpcFails(t, s, "name_ban.add", `{"name":"*","reason":"everyone"}`, rpcInvalidName)
	rpcFails(t, s, "name_ban.add", `{"name":"1bad","reason":"x"}`, rpcInvalidName)
	rpcFails(t, s, "name_ban.add", `{"name":"Spammer"}`, rpcInvalidParams)
	rpcFails(t, s, "name_ban.add", `{"reason":"x"}`, rpcInvalidParams)

	has(t, "name_ban.list", rpcOK(t, s, "name_ban.list", ""), `"name":"Pestbot*"`, `"name":"#warez"`, `"set_at_string":"`)
	has(t, "name_ban.get", rpcOK(t, s, "name_ban.get", `{"name":"pestbot*"}`), `"reason":"spam bots"`)
	has(t, "stats.get", rpcOK(t, s, "stats.get", ""), `"name_ban":2`)

	pest.send("NICK Pestbot2")
	pest.expect(" 432 ")
	ember.send("JOIN #warez")
	ember.expect("#warez")

	has(t, "name_ban.del", rpcOK(t, s, "name_ban.del", `{"name":"PESTBOT*"}`), `"name":"Pestbot*"`)
	rpcOK(t, s, "name_ban.del", `{"name":"#warez"}`)
	rpcFails(t, s, "name_ban.get", `{"name":"Pestbot*"}`, rpcNotFound)
	rpcFails(t, s, "name_ban.del", `{"name":"#warez"}`, rpcNotFound)
	has(t, "name_ban.list", rpcOK(t, s, "name_ban.list", ""), `"list":[]`)
	pest.send("NICK Pestbot3")
	pest.expect("NICK Pestbot3")
}

// server.disconnect is SQUIT by a tool: refusals over HTTPS, the split on
// a linked pair.
func TestRPCServerDisconnect(t *testing.T) {
	s := rpcServer(t)
	rpcFails(t, s, "server.disconnect", `{}`, rpcInvalidParams)
	rpcFails(t, s, "server.disconnect", `{"link":"irc.test.local"}`, rpcInvalidParams)
	rpcFails(t, s, "server.disconnect", `{"link":"no.such.server","reason":"x"}`, rpcNotFound)

	hub, _ := linkedPair(t)
	op := login(t, hub.s, "op")
	netadminOn(t, hub, op, "op")
	op.send("MODE op +s")
	call := func(params string) (res any, e *rpcError) {
		hub.s.onLoop(func() {
			res, e = hub.s.rpcServerDisconnect(&rpcConn{name: "tool", issuer: "Belon"}, json.RawMessage(params))
		})
		return
	}
	if _, e := call(`{"link":"leaf.test.local","reason":"moving it"}`); e != nil {
		t.Fatalf("disconnect: %+v", e)
	}
	op.expect("SQUIT: Server \x02leaf.test.local\x02 removed from network by RPC Belon")
	op.expect("Server \x02leaf.test.local\x02 split: moving it")
	waitLink(t, "leaf split", func() bool {
		var gone bool
		hub.s.onLoop(func() { gone = hub.s.serverByName("leaf.test.local") == nil })
		return gone
	})
	if _, e := call(`{"link":"leaf.test.local"}`); e == nil || e.Code != rpcNotFound {
		t.Errorf("second disconnect: %+v", e)
	}
}

// rpc.add_timer runs a request again and again on a stream until
// rpc.del_timer; it needs a stream and checks what it is given.
func TestRPCTimers(t *testing.T) {
	s := rpcServer(t)
	rpcFails(t, s, "rpc.add_timer", `{"timer_id":"t","every_msec":500,"request":{"jsonrpc":"2.0","method":"stats.get","id":9}}`, rpcInvalidRequest)
	rpcFails(t, s, "rpc.del_timer", `{"timer_id":"t"}`, rpcNotFound)

	conn, err := net.Dial("unix", s.cfg.RPC.Socket)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	r := bufio.NewReader(conn)
	send := func(line string) {
		t.Helper()
		if _, err := io.WriteString(conn, line+"\n"); err != nil {
			t.Fatal(err)
		}
	}
	read := func() string {
		t.Helper()
		conn.SetDeadline(time.Now().Add(5 * time.Second))
		got, err := r.ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	ask := func(line string) string { send(line); return read() }
	add := func(id int, params string) string {
		return ask(fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":"rpc.add_timer","params":%s}`, id, params))
	}
	req := `"request":{"jsonrpc":"2.0","method":"stats.get","params":null,"id":424242}`
	has(t, "too often", add(1, `{"timer_id":"t","every_msec":100,`+req+`}`), `"code":-32602`, `250`)
	has(t, "no request", add(2, `{"timer_id":"t","every_msec":300}`), `"code":-32602`)
	has(t, "bad request", add(3, `{"timer_id":"t","every_msec":300,"request":{"method":"stats.get"}}`), `"code":-32602`)
	has(t, "no such method", add(4, `{"timer_id":"t","every_msec":300,"request":{"jsonrpc":"2.0","method":"nope.x","id":1}}`), `"code":-32602`)
	has(t, "timer in a timer", add(5, `{"timer_id":"t","every_msec":300,"request":{"jsonrpc":"2.0","method":"rpc.add_timer","id":1}}`), `"code":-32602`)
	has(t, "no id", add(6, `{"every_msec":300,`+req+`}`), `"code":-32602`)
	has(t, "added", add(7, `{"timer_id":"t","every_msec":300,`+req+`}`), `"id":7`, `"result":true`)
	for i := 0; i < 3; i++ {
		has(t, "timer run", read(), `"id":424242`, `"method":"stats.get"`, `"user":{`)
	}
	has(t, "same name", add(8, `{"timer_id":"t","every_msec":300,`+req+`}`), `"code":-1001`)
	send(`{"jsonrpc":"2.0","id":9,"method":"rpc.del_timer","params":{"timer_id":"t"}}`)
	for {
		got := read()
		if strings.Contains(got, `"id":9`) {
			has(t, "deleted", got, `"result":true`)
			break
		}
		has(t, "a run before the delete", got, `"id":424242`)
	}
	// Nothing more comes; the next answer is the next call's.
	time.Sleep(800 * time.Millisecond)
	has(t, "after delete", ask(`{"jsonrpc":"2.0","id":10,"method":"rpc.del_timer","params":{"timer_id":"t"}}`), `"id":10`, `"code":-1000`)

	// A timer ends with its connection.
	has(t, "second", add(11, `{"timer_id":"u","every_msec":250,`+req+`}`), `"result":true`)
	read()
	conn.Close()
	time.Sleep(600 * time.Millisecond) // the timer goroutine must stop (the test's wait on shutdown would hang otherwise)
}
