package server

import (
	"context"
	"net/http"
	"time"

	"github.com/fhazal/beeircd/internal/control"
	"github.com/fhazal/beeircd/internal/release"
	"github.com/fhazal/beeircd/internal/version"
)

// updState is the daily release check as the loop sees it.
type updState struct {
	latest  *control.UpdateInfo // a newer signed release, nil if none
	noticed string              // the version opers were told about
	lastErr string              // logged once until it changes
	first   time.Duration       // delay before the first check (tests: short)
	every   time.Duration
	fetch   func(ctx context.Context, url string) (*release.Manifest, error) // tests replace it
	started bool
}

// updateLoop looks once a day for a newer signed release. It sends nothing
// about the server: one GET for release.json and its signature. It reads
// [update] again before each check, so a rehash is followed.
func (s *Server) updateLoop() {
	defer s.wg.Done()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		<-s.done
		cancel()
	}()
	fetch := s.upd.fetch
	if fetch == nil {
		client := &http.Client{Timeout: 30 * time.Second}
		fetch = func(ctx context.Context, url string) (*release.Manifest, error) {
			return release.Fetch(ctx, client, url)
		}
	}
	wait, every := s.upd.first, s.upd.every
	for {
		select {
		case <-s.done:
			return
		case <-time.After(wait):
		}
		wait = every
		var on bool
		var url string
		got := make(chan struct{})
		s.post(func() { on, url = s.updateSetting(); close(got) })
		select {
		case <-got:
		case <-s.done:
			return
		}
		if !on {
			continue
		}
		m, err := fetch(ctx, url)
		if ctx.Err() != nil {
			return
		}
		s.post(func() { s.updateResult(m, err) })
	}
}

func (s *Server) updateResult(m *release.Manifest, err error) {
	if err != nil {
		if err.Error() != s.upd.lastErr {
			s.upd.lastErr = err.Error()
			s.log.Printf("update check: %v", err)
		}
		return
	}
	s.upd.lastErr = ""
	if release.TestVersion(m.Version) || !release.Newer(m.Version, version.Version) {
		s.upd.latest = nil
		return
	}
	s.upd.latest = &control.UpdateInfo{Version: m.Version, Date: m.Date, How: UpdateCommand}
	if s.upd.noticed != m.Version {
		s.upd.noticed = m.Version
		s.sno('a', "BeeIRCd %s is out (you run %s): %s installs it", m.Version, version.Version, UpdateCommand)
	}
}

// UpdateCommand is how this machine installs a new version: `beeircd
// update`, or a new image in Docker (set by main).
var UpdateCommand = "beeircd update"

// startUpdateCheck starts the daily check unless [update] check = false.
func (s *Server) startUpdateCheck() {
	if !s.cfg.Update.CheckOn() || s.upd.started {
		return
	}
	s.upd.started = true
	if s.upd.first == 0 {
		s.upd.first = 2 * time.Minute
	}
	if s.upd.every == 0 {
		s.upd.every = 24 * time.Hour
	}
	s.wg.Add(1)
	go s.updateLoop()
}

// updateSetting is [update] as the loop has it now: a rehash can switch the
// check off or point it somewhere else. Off also drops what was found.
func (s *Server) updateSetting() (on bool, url string) {
	on, url = s.cfg.Update.CheckOn(), s.cfg.Update.URL
	if url == "" {
		url = release.DefaultURL
	}
	if !on {
		s.upd.latest = nil
	}
	return on, url
}
