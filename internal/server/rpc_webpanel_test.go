package server

import (
	"encoding/json"
	"strings"
	"testing"
)

// unrealircd-webpanel: services bots have reputation 0 and +S (its
// "Services Bot" badge); everyone has a reputation key.
func TestRPCServicesUsers(t *testing.T) {
	s := rpcServer(t)
	login(t, s, "ann")
	got := rpcOK(t, s, "user.list", "")
	var list struct {
		List []struct {
			Name string `json:"name"`
			User struct {
				Modes      string `json:"modes"`
				Reputation *int   `json:"reputation"`
			} `json:"user"`
		} `json:"list"`
	}
	if err := json.Unmarshal([]byte(got), &list); err != nil {
		t.Fatalf("user.list: %v: %s", err, got)
	}
	sawNS := false
	for _, u := range list.List {
		if u.User.Reputation == nil {
			t.Errorf("%s has no reputation", u.Name)
		}
		if u.Name == "NickServ" {
			sawNS = true
			if !strings.Contains(u.User.Modes, "S") || *u.User.Reputation != 0 {
				t.Errorf("NickServ: modes %q reputation %d", u.User.Modes, *u.User.Reputation)
			}
		}
	}
	if !sawNS {
		t.Errorf("no NickServ in user.list: %s", got)
	}
}

// server.list for a server behind a link shows what it runs.
func TestRPCRemoteServerInfo(t *testing.T) {
	hub, leaf := linkedPair(t)
	waitLink(t, "the hub has the leaf's SINFO", func() bool {
		var ok bool
		hub.s.onLoop(func() { r := hub.s.links.servers["0LF"]; ok = r != nil && !r.boot.IsZero() })
		return ok
	})
	var got []byte
	hub.s.onLoop(func() {
		for _, n := range hub.s.netServers() {
			if n.sid == "0LF" {
				got, _ = json.Marshal(hub.s.rpcServer(n, hub.s.rpcPeoplePerServer()))
			}
		}
	})
	has(t, "the leaf", string(got), `"name":"leaf.test.local"`, `"boot_time":"20`, `"software":"BeeIRCd`, `"protocol":6100`, `"chanmodes":["beI"`, `"usermodes":"`)
	// The leaf learns the hub's services server too.
	var svc []byte
	leaf.s.onLoop(func() {
		for _, n := range leaf.s.netServers() {
			if strings.HasPrefix(n.name, "services.") {
				svc, _ = json.Marshal(leaf.s.rpcServer(n, leaf.s.rpcPeoplePerServer()))
			}
		}
	})
	has(t, "the hub's services", string(svc), `"software":"BeeIRCd`, `"boot_time":"20`)
}
