package server

import (
	"testing"
	"time"

	"github.com/fhazal/beeircd/internal/config"
	"github.com/fhazal/beeircd/internal/gate"
)

// The gate's refusals count like the server's own: one throttle refusal
// here and two in the gate make the three strikes for one ZLINE. The list
// for the gate keeps only IPs still refused.
func TestGateDenyList(t *testing.T) {
	s := testServerWith(t, func(c *config.Config) { c.Limits.ConnectRate = "1:60" }, noExempt)
	var (
		list        []gate.Deny
		learned     int
		zlined      bool
		stale       bool
		now         = time.Now()
		hits, bans  int
		firstOK     bool
		secondRefus bool
	)
	s.onLoop(func() {
		const ip = "192.0.2.50"
		firstOK = !s.throttleCount(ip, now)
		secondRefus = s.throttleCount(ip, now) // strike 1
		s.gateRefused(ip)                      // strike 2
		s.gateRefused(ip)                      // strike 3: ZLINE
		zlined = s.svc.ZlineFor(ip) != nil
		hits, bans = int(s.safety().Throttled), int(s.safety().Zlined)

		s.gateRefused("192.0.2.70") // the gate's list was out of date
		_, stale = s.throttle.hits["192.0.2.70"]

		s.gateDeny.learned = map[string]time.Time{"192.0.2.60": now, "nonsense": now, "192.0.2.61": now.Add(time.Second)}
		s.throttleCount("192.0.2.61", now)
		s.throttleCount("192.0.2.61", now)
		list = s.gateDenyList(now)
		learned = len(s.gateDeny.learned)
	})
	if !firstOK || !secondRefus {
		t.Fatalf("throttle: first let in %v, second refused %v", firstOK, secondRefus)
	}
	if !zlined || hits != 3 || bans != 1 {
		t.Fatalf("after 1+2 refusals: ZLINE %v, throttled %d, Z-lined %d; want true, 3, 1", zlined, hits, bans)
	}
	if stale {
		t.Fatal("a refusal for an IP that isn't refused counted against it")
	}
	if len(list) != 2 || list[0].Net != "192.0.2.50/32" || list[1].Net != "192.0.2.61/32" {
		t.Fatalf("gate list %+v", list)
	}
	if d := time.UnixMilli(list[0].Until).Sub(now); d < 9*time.Minute || d > 11*time.Minute {
		t.Errorf("throttle ZLINE ends in %v, want 10 minutes", d)
	}
	if d := time.UnixMilli(list[1].Until).Sub(now); d < 59*time.Second || d > 61*time.Second {
		t.Errorf("throttled IP ends in %v, want the 60 s window", d)
	}
	if learned != 1 {
		t.Errorf("%d learned IPs kept, want 1", learned)
	}
}
