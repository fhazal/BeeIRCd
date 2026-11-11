package server

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fhazal/beeircd/internal/config"
	"github.com/fhazal/beeircd/internal/control"
	"github.com/fhazal/beeircd/internal/release"
)

func TestDailyUpdateCheck(t *testing.T) {
	var calls atomic.Int32
	answer := atomic.Value{}
	answer.Store("99.0.0")
	on := true
	s := testServerWith(t, func(c *config.Config) { c.Update.Check = &on }, func(s *Server) {
		s.upd.first, s.upd.every = time.Millisecond, 20*time.Millisecond
		s.upd.fetch = func(ctx context.Context, url string) (*release.Manifest, error) {
			calls.Add(1)
			v := answer.Load().(string)
			if v == "" {
				return nil, errors.New("release site down")
			}
			return &release.Manifest{Version: v, Date: "2026-11-20"}, nil
		}
	})
	waitFor(t, s, "the first check", func() bool { return s.upd.latest != nil })
	var u *control.UpdateInfo
	var noticed string
	s.onLoop(func() { u, noticed = s.statusInfo().Update, s.upd.noticed })
	if u == nil || u.Version != "99.0.0" || u.Date != "2026-11-20" || noticed != "99.0.0" {
		t.Errorf("status update %+v, noticed %q", u, noticed)
	}
	// A failing check keeps what was found; an older answer clears it.
	answer.Store("")
	n := calls.Load()
	waitFor(t, s, "a failed check", func() bool { return calls.Load() > n+1 })
	waitFor(t, s, "the error logged once", func() bool { return s.upd.lastErr == "release site down" && s.upd.latest != nil })
	answer.Store("0.0.0-a") // older than this test build (0.0.0-dev)
	waitFor(t, s, "the row to go", func() bool { return s.upd.latest == nil })
}

func TestUpdateCheckOff(t *testing.T) {
	s := testServer(t, nil) // test servers have [update] check = false
	time.Sleep(20 * time.Millisecond)
	s.onLoop(func() {
		if s.upd.started {
			t.Error("update check started although check = false")
		}
	})
}
