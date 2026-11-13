package config

import (
	"strings"
	"testing"
)

func TestWebIRCChecks(t *testing.T) {
	good := WebIRC{Name: "beeroom", Password: "gateway-secret-0123456789", Hosts: []string{"127.0.0.1", "10.0.0.0/24"}}
	cases := []struct {
		w    WebIRC
		want string
	}{
		{WebIRC{Password: good.Password, Hosts: good.Hosts}, "needs a name"},
		{WebIRC{Name: "bee room", Password: good.Password, Hosts: good.Hosts}, "may not have spaces"},
		{WebIRC{Name: "beeroom", Hosts: good.Hosts}, "needs a password"},
		{WebIRC{Name: "beeroom", Password: "short", Hosts: good.Hosts}, "at least 16 characters"},
		{WebIRC{Name: "beeroom", Password: good.Password}, "needs hosts"},
		{WebIRC{Name: "beeroom", Password: good.Password, Hosts: []string{"gw.example.net"}}, "must be an IP or CIDR"},
		{WebIRC{Name: "beeroom", Password: good.Password, Hosts: []string{"0.0.0.0/0"}}, "covers too much"},
		{WebIRC{Name: "beeroom", Password: good.Password, Hosts: []string{"::/0"}}, "covers too much"},
	}
	for _, tc := range cases {
		c := &Config{WebIRC: []WebIRC{tc.w}}
		err := c.checkWebIRC()
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%+v: got %v, want %q", tc.w, err, tc.want)
		}
	}
	c := &Config{WebIRC: []WebIRC{good, good}}
	if err := c.checkWebIRC(); err == nil || !strings.Contains(err.Error(), "twice") {
		t.Errorf("duplicate: %v", err)
	}
	c = &Config{WebIRC: []WebIRC{good, {Name: "kiwi", Password: "$argon2id$v=19$m=19456,t=2,p=1$x$y", Hosts: []string{"::1"}}}}
	if err := c.checkWebIRC(); err != nil {
		t.Fatalf("good blocks refused: %v", err)
	}
	for ip, want := range map[string]bool{"127.0.0.1": true, "10.0.0.77": true, "::ffff:10.0.0.5": true, "10.0.1.1": false, "::1": false, "nonsense": false} {
		if got := good.Allows(ip); got != want {
			t.Errorf("Allows(%s) = %v, want %v", ip, got, want)
		}
	}
	if got := len(c.WebIRCFor("::1")); got != 1 {
		t.Errorf("WebIRCFor(::1) = %d blocks", got)
	}
}
