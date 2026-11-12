package config

import (
	"strings"
	"testing"
)

var key80 = strings.Repeat("Ab3", 30)

func TestConfig2c(t *testing.T) {
	c, err := Load(write(t, good))
	if err != nil {
		t.Fatal(err)
	}
	if c.Server.Casemapping != "ascii" || c.Cloak.Method != "beeircd" || c.Cloak.Prefix != "Clk" || !c.Services.On() || c.UnrealCloak() {
		t.Errorf("defaults: %q %q %q %v", c.Server.Casemapping, c.Cloak.Method, c.Cloak.Prefix, c.Services.On())
	}
	unreal := strings.Replace(good, `secret = "0123456789abcdef-test"`,
		`method = "unreal"
keys = ["`+key80+`1", "`+key80+`2", "`+key80+`3"]`, 1)
	c, err = Load(write(t, strings.Replace(unreal, `sid = "0BE"`, "sid = \"0BE\"\ncasemapping = \"rfc1459\"\n[services]\nenabled = false", 1)))
	if err != nil {
		t.Fatal(err)
	}
	if !c.UnrealCloak() || c.Services.On() || c.Server.Casemapping != "rfc1459" {
		t.Errorf("unreal cloak %v services %v casemapping %q", c.UnrealCloak(), c.Services.On(), c.Server.Casemapping)
	}
	for _, bad := range []struct{ from, to, want string }{
		{`sid = "0BE"`, "sid = \"0BE\"\ncasemapping = \"strict\"", "casemapping"},
		{`secret = "0123456789abcdef-test"`, `method = "inspircd"`, "method"},
		{`secret = "0123456789abcdef-test"`, `method = "unreal"`, "three keys"},
		{`secret = "0123456789abcdef-test"`, "method = \"unreal\"\nkeys = [\"short\", \"b\", \"c\"]", "80 to 1000"},
		{`secret = "0123456789abcdef-test"`, "method = \"unreal\"\nkeys = [\"" + key80 + "\", \"" + key80 + "\", \"" + key80 + "x\"]", "differ"},
	} {
		if _, err := Load(write(t, strings.Replace(good, bad.from, bad.to, 1))); err == nil || !strings.Contains(err.Error(), bad.want) {
			t.Errorf("%q: want %q, got %v", bad.to, bad.want, err)
		}
	}
}
