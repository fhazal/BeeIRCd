package services

import (
	"testing"
	"time"

	"github.com/fhazal/beeircd/internal/store"
)

// Services open a registered channel as old as its registration, never
// older than 1970 or newer than now.
func TestRegTS(t *testing.T) {
	now := time.Now().Unix()
	if got := regTS(&store.Channel{Registered: time.Unix(1500000000, 0)}); got != 1500000000 {
		t.Errorf("registered 2017: TS %d", got)
	}
	for _, reg := range []time.Time{{}, time.Unix(0, 0), time.Now().Add(time.Hour)} {
		if got := regTS(&store.Channel{Registered: reg}); got < now || got > now+2 {
			t.Errorf("registered %v: TS %d, want now", reg, got)
		}
	}
}
