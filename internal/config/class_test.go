package config

import (
	"strings"
	"testing"
	"time"

	"github.com/BurntSushi/toml"
)

// A [[class]] block reads from beeircd.conf as the docs show it, and what
// it leaves unset comes from [limits].
func TestConnClassTOML(t *testing.T) {
	var c Config
	_, err := toml.Decode(`
[limits]
sendq = 500000
max_channels = 20

[[security_group]]
name = "from-xx"
mask = ["~C:XX"]

[[class]]
name = "strict"
security_group = ["from-xx", "websocket-users"]
session_limit = 1
max_users = 50
sendq = 262144
ping_interval = "60s"
flood_burst = 5
flood_rate = 1
max_channels = 10

[[class]]
name = "blocked"
security_group = ["from-xx"]
deny = true
`, &c)
	if err != nil {
		t.Fatal(err)
	}
	c.SetDefaults()
	if err := c.checkConnClasses(); err != nil {
		t.Fatal(err)
	}
	if len(c.ConnClasses) != 2 || !c.ConnClasses[1].Deny || c.ConnClasses[0].SecurityGroup[1] != "websocket-users" {
		t.Fatalf("classes: %+v", c.ConnClasses)
	}
	got := c.LimitsOf(&c.ConnClasses[0])
	want := ClassLimits{SessionLimit: 1, MaxUsers: 50, SendQ: 262144, PingInterval: time.Minute, PingTimeout: c.Limits.PingTimeout.Duration,
		FloodBurst: 5, FloodRate: 1, MaxChannels: 10}
	if got != want {
		t.Errorf("strict: got %+v, want %+v", got, want)
	}
	def := c.LimitsOf(nil)
	if def.SendQ != 500000 || def.MaxChannels != 20 || def.SessionLimit != c.Limits.SessionLimit || def.FloodRate != c.Limits.FloodRate {
		t.Errorf("default class is not [limits]: %+v", def)
	}
	// A class setting nothing but its groups is [limits] too.
	if l := c.LimitsOf(&c.ConnClasses[1]); l != def {
		t.Errorf("blocked: got %+v, want %+v", l, def)
	}
	if ch := Diff(&Config{}, &c); !strings.Contains(changes(ch), "connect classes") {
		t.Errorf("diff: %s", changes(ch))
	}
}

func changes(ch []Change) string {
	var out []string
	for _, c := range ch {
		out = append(out, c.String())
	}
	return strings.Join(out, "; ")
}

func TestConnClassChecks(t *testing.T) {
	groups := []SecurityGroup{{Name: "nl", GroupMatch: GroupMatch{Mask: []string{"~C:NL"}}}}
	for _, tc := range []struct {
		k    []ConnClass
		want string
	}{
		{[]ConnClass{{Name: "bad name", SecurityGroup: []string{"nl"}}}, "must be a word"},
		{[]ConnClass{{Name: "Default", SecurityGroup: []string{"nl"}}}, "another name"},
		{[]ConnClass{{Name: "a", SecurityGroup: []string{"nl"}}, {Name: "A", SecurityGroup: []string{"tls-users"}}}, `"A" is there twice`},
		{[]ConnClass{{Name: "a"}}, "takes in nobody"},
		{[]ConnClass{{Name: "a", SecurityGroup: []string{"nope"}}}, `no security group "nope"`},
		{[]ConnClass{{Name: "a", SecurityGroup: []string{"nl"}, SessionLimit: -1}}, "session_limit can't be below 0"},
		{[]ConnClass{{Name: "a", SecurityGroup: []string{"nl"}, MaxChannels: -3}}, "max_channels can't be below 0"},
		{[]ConnClass{{Name: "a", SecurityGroup: []string{"nl"}, SendQ: 100}}, "sendq is at least 8192"},
		{[]ConnClass{{Name: "a", SecurityGroup: []string{"nl"}, PingTimeout: Duration{time.Second}}}, "ping_timeout is at least 10s"},
	} {
		c := Config{SecurityGroups: groups, ConnClasses: tc.k}
		if err := c.checkConnClasses(); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%+v: want %q, got %v", tc.k, tc.want, err)
		}
	}
	ok := Config{SecurityGroups: groups, ConnClasses: []ConnClass{
		{Name: "nl", SecurityGroup: []string{"nl", "unknown-users"}, SendQ: 8192, PingInterval: Duration{10 * time.Second}},
		{Name: "web", SecurityGroup: []string{"websocket-users"}, Deny: true},
	}}
	if err := ok.checkConnClasses(); err != nil {
		t.Error(err)
	}
	// Load runs the class checks too, so beeircd check and a rehash refuse
	// a bad block.
	if _, err := Load(write(t, good+"\n[[class]]\nname = \"a\"\nsecurity_group = [\"nope\"]\n")); err == nil || !strings.Contains(err.Error(), `[[class]] a: no security group "nope"`) {
		t.Errorf("Load: %v", err)
	}
	if _, err := Load(write(t, good+"\n[[class]]\nname = \"a\"\nsecurity_group = [\"tls-users\"]\nsendq = 100000\n")); err != nil {
		t.Errorf("Load: %v", err)
	}
}
