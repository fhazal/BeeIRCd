package link

import (
	"strings"
	"testing"

	"github.com/fhazal/beeircd/internal/config"
)

// Both sides must fold names the same way, and only one may run services.
func TestAgreeSettings(t *testing.T) {
	pair := func(leaf, hub func(*Local)) (Handshake, Handshake) {
		lb := config.LinkServer{Name: "hub.bee.net", Host: "h", Fingerprint: fpHub}
		hb := config.LinkServer{Name: "leaf.bee.net", Fingerprint: fpLeaf}
		ll, hl := local("leaf.bee.net", "0LF"), local("hub.bee.net", "0HB", hb)
		leaf(&ll)
		hub(&hl)
		return NewUnreal(ll, true, &lb, fpHub, none), NewUnreal(hl, false, nil, fpLeaf, none)
	}
	keep := func(*Local) {}
	cases := []struct {
		name      string
		leaf, hub func(*Local)
		want      string // "" = links
	}{
		{"same ascii", keep, keep, ""},
		{"both rfc1459", func(l *Local) { l.Casemapping = "rfc1459" }, func(l *Local) { l.Casemapping = "rfc1459" }, ""},
		{"casemapping differs", func(l *Local) { l.Casemapping = "rfc1459" }, keep, "casemapping differs"},
		{"hub services only", keep, func(l *Local) { l.Services = true }, ""},
		{"both services", func(l *Local) { l.Services = true }, func(l *Local) { l.Services = true }, "both"},
	}
	for _, c := range cases {
		a, b := pair(c.leaf, c.hub)
		_, err := pump(t, a, b)
		switch {
		case c.want == "" && err != nil:
			t.Errorf("%s: refused: %v", c.name, err)
		case c.want != "" && (err == nil || !strings.Contains(err.Error(), c.want)):
			t.Errorf("%s: want %q, got %v", c.name, c.want, err)
		}
	}
}

// A BeeIRCd from before 2c sends no BEECASE: it folds rfc1459.
func TestAgreeOldBee(t *testing.T) {
	h := &unreal{me: Local{Casemapping: "ascii"}, peer: Peer{Bee: true, Tokens: map[string]string{}}}
	if err := h.agree(); err == nil || !strings.Contains(err.Error(), "rfc1459") {
		t.Errorf("got %v", err)
	}
	h = &unreal{me: Local{Casemapping: "rfc1459"}, peer: Peer{Tokens: map[string]string{}}}
	if err := h.agree(); err == nil || !strings.Contains(err.Error(), "UnrealIRCd uses casemapping ascii") {
		t.Errorf("unreal + rfc1459: got %v", err)
	}
}
