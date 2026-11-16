package server

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"github.com/fhazal/beeircd/internal/geoip"
	"github.com/fhazal/beeircd/internal/irc"
	"github.com/fhazal/beeircd/internal/state"
)

// GeoIP: each user's country and network (ASN), looked up on this server
// in DB-IP Lite (downloaded once a month) or MaxMind files. For ~C: and
// ~A: bans and the WHOIS lines opers see.

type geoState struct {
	set     geoip.Set
	first   time.Duration // delay before the first download check (tests: short)
	every   time.Duration
	client  *http.Client
	lastErr string
	now     func() time.Time
}

// startGeoIP loads the database files (missing ones are fetched later)
// and starts the daily check for new ones.
func (s *Server) startGeoIP() {
	if !s.cfg.GeoIP.On() {
		return
	}
	paths := s.geoPaths()
	for i, kind := range geoip.Kinds {
		db, err := geoip.Open(paths[i])
		if err != nil {
			continue // not there yet: the download brings it
		}
		s.geoSet(kind, db)
	}
	if !s.cfg.GeoIP.DownloadOn() {
		if s.geo.set.Country == nil && s.geo.set.ASN == nil {
			s.log.Printf("geoip: no database at %s or %s (download is off)", paths[0], paths[1])
		}
		return
	}
	if s.geo.first == 0 {
		s.geo.first = time.Minute
	}
	if s.geo.every == 0 {
		s.geo.every = 24 * time.Hour
	}
	if s.geo.client == nil {
		s.geo.client = &http.Client{Timeout: 2 * time.Minute}
	}
	if s.geo.now == nil {
		s.geo.now = time.Now
	}
	s.wg.Add(1)
	go s.geoLoop(paths)
}

func (s *Server) geoPaths() []string {
	country, asn := s.cfg.GeoIPFiles()
	return []string{country, asn}
}

func (s *Server) geoSet(kind string, db *geoip.DB) {
	if kind == "country" {
		s.geo.set.Country = db
	} else {
		s.geo.set.ASN = db
	}
}

// geoLoop fetches a database that is missing or over 35 days old, once a
// day at most.
func (s *Server) geoLoop(paths []string) {
	defer s.wg.Done()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		<-s.done
		cancel()
	}()
	wait := s.geo.first
	for {
		select {
		case <-s.done:
			return
		case <-time.After(wait):
		}
		wait = s.geo.every
		for i, kind := range geoip.Kinds {
			stale := make(chan bool, 1)
			s.post(func() { stale <- geoip.Stale(s.geoDB(kind), s.geo.now()) })
			select {
			case <-s.done:
				return
			case old := <-stale:
				if !old {
					continue
				}
			}
			db, err := geoip.Download(ctx, s.geo.client, kind, paths[i], s.geo.now())
			if ctx.Err() != nil {
				return
			}
			s.post(func() { s.geoDownloaded(kind, db, err) })
		}
	}
}

func (s *Server) geoDB(kind string) *geoip.DB {
	if kind == "country" {
		return s.geo.set.Country
	}
	return s.geo.set.ASN
}

func (s *Server) geoDownloaded(kind string, db *geoip.DB, err error) {
	if err != nil {
		if err.Error() != s.geo.lastErr {
			s.geo.lastErr = err.Error()
			s.log.Printf("geoip: %v", err)
		}
		return
	}
	s.geo.lastErr = ""
	s.geoSet(kind, db)
	s.log.Printf("geoip: new %s database (%s)", kind, db.Type)
	for _, u := range s.net.Users() {
		s.geoFill(u)
	}
}

// geoFill sets u's country and network from its IP.
func (s *Server) geoFill(u *state.User) {
	if u.Service || u.IP == "" {
		return
	}
	info := s.geo.set.Lookup(u.IP)
	if info.Country != "" || u.Country == "" {
		u.Country = info.Country
	}
	if info.ASN != 0 || u.ASN == 0 {
		u.ASN, u.ASOrg = info.ASN, info.ASOrg
	}
}

// whoisGeo adds the country and network lines (UnrealIRCd's 344 and 569).
func (s *Server) whoisGeo(u *state.User, reply func(code string, p ...string)) {
	if u.Country != "" {
		name := s.geo.set.Lookup(u.IP).Name
		if name == "" {
			name = u.Country
		}
		reply(irc.RplWhoisCountry, u.Nick, u.Country, "is connecting from "+name)
	}
	if u.ASN != 0 {
		text := "is connecting from AS" + strconv.FormatUint(uint64(u.ASN), 10)
		if u.ASOrg != "" {
			text += " [" + u.ASOrg + "]"
		}
		reply(irc.RplWhoisASN, u.Nick, strconv.FormatUint(uint64(u.ASN), 10), text)
	}
}
