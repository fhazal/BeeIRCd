package config

import (
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
)

func TestSecurityConfigChecks(t *testing.T) {
	for _, tc := range []struct {
		c    Config
		want string
	}{
		{Config{SecurityGroups: []SecurityGroup{{Name: "x"}}}, "takes in nobody"},
		{Config{SecurityGroups: []SecurityGroup{{Name: "unknown-users", GroupMatch: GroupMatch{TLS: true}}}}, "can't be changed"},
		{Config{SecurityGroups: []SecurityGroup{{Name: "a", GroupMatch: GroupMatch{SecurityGroup: []string{"nope"}}}}}, `no group "nope"`},
		{Config{SecurityGroups: []SecurityGroup{{Name: "a", GroupMatch: GroupMatch{IP: []string{"10.0.0.0/33"}}}}}, "not an address"},
		{Config{SecurityGroups: []SecurityGroup{{Name: "a", GroupMatch: GroupMatch{TLS: true}, Exclude: GroupMatch{ReputationScore: 20000}}}}, "1 to 10000"},
		{Config{SecurityGroups: []SecurityGroup{{Name: "a", GroupMatch: GroupMatch{Rule: "reputation() < "}}}}, `[[security_group]] a: rule "reputation() <": reputation(): expected a number`},
		{Config{SecurityGroups: []SecurityGroup{{Name: "a", GroupMatch: GroupMatch{TLS: true}, Exclude: GroupMatch{Rule: "is_cool()"}}}}, `[[security_group]] a: rule "is_cool()": unknown word "is_cool"`},
		{Config{SecurityGroups: []SecurityGroup{{Name: "a", GroupMatch: GroupMatch{Rule: "in_security_group(nope)"}}}}, `a: there is no group "nope"`},
		{Config{SecurityGroups: []SecurityGroup{{Name: "a", GroupMatch: GroupMatch{Rule: "!in_security_group(A)"}}}}, "can't be in itself"},
		{Config{SecurityGroups: []SecurityGroup{{Name: "a", GroupMatch: GroupMatch{Rule: "  "}}}}, "takes in nobody"},
		{Config{ConnThrottle: ConnThrottle{Local: "20"}}, "count:seconds"},
		{Config{ConnThrottle: ConnThrottle{Except: []string{"vip"}}}, `no group "vip"`},
		{Config{Restricts: []Restrict{{Command: "nick"}}}, "needed to connect"},
		{Config{Restricts: []Restrict{{Command: "list"}, {Command: "LIST"}}}, "twice"},
		{Config{Restricts: []Restrict{{Command: "a b"}}}, "not a command"},
		{Config{SpamFilter: SpamFilter{ExceptGroups: []string{"nope"}}}, "except_groups"},
	} {
		var err error
		for _, f := range []func() error{tc.c.checkGroups, tc.c.checkConnThrottle, tc.c.checkRestrict, tc.c.checkSpamFilter} {
			if err = f(); err != nil {
				break
			}
		}
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("want %q, got %v", tc.want, err)
		}
	}
	good := Config{
		SecurityGroups: []SecurityGroup{{Name: "known-users", GroupMatch: GroupMatch{Identified: true}}, {Name: "nl", GroupMatch: GroupMatch{Mask: []string{"~C:NL"}}},
			{Name: "newbies", GroupMatch: GroupMatch{Rule: "reputation() < 10 && !in_security_group(nl)"}, Exclude: GroupMatch{Rule: "is_tls()"}}},
		ConnThrottle: ConnThrottle{Local: "off", Except: []string{"nl", "unknown-users"}},
		Restricts:    []Restrict{{Command: "private-message", Except: []string{"known-users"}}},
	}
	for _, f := range []func() error{good.checkGroups, good.checkConnThrottle, good.checkRestrict} {
		if err := f(); err != nil {
			t.Error(err)
		}
	}
	if g := good.Groups(); len(g) != 6 || g[2].ReputationScore != 0 || g[4].Name != "nl" || g[5].UserRule() == nil || g[4].UserRule() != nil {
		t.Errorf("groups: %+v", g)
	}
}

// The group items sit right in the [[security_group]] block, exclude
// ones under [security_group.exclude].
func TestSecurityGroupTOML(t *testing.T) {
	var c Config
	_, err := toml.Decode(`
[[security_group]]
name = "trusted"
reputation_score = 100
mask = ["~C:NL"]
connect_time = "1h"
rule = "reputation() < 10 && match_mask('*!*@*.example.net')"
[security_group.exclude]
webirc = true
rule = "is_identified()"
`, &c)
	if err != nil {
		t.Fatal(err)
	}
	g := c.SecurityGroups[0]
	if g.ReputationScore != 100 || g.Mask[0] != "~C:NL" || g.ConnectTime.Hours() != 1 || !g.Exclude.WebIRC ||
		g.UserRule().String() != "reputation() < 10 && match_mask('*!*@*.example.net')" || g.Exclude.UserRule() == nil {
		t.Errorf("%+v", g)
	}
}
