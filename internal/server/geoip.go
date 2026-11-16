package server

import (
	"context"
	"net/http"
	"sort"
	"strconv"
	"time"

	"github.com/fhazal/beeircd/internal/control"
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
		s.log.Printf("geoip: %s database %s, made %s", kind, db.Type, time.Unix(int64(db.Built), 0).UTC().Format("2006-01-02"))
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

// whereMax is how many countries and networks the panel gets; the rest
// are counted together.
const whereMax = 50

// panelWhere counts the people of the network by country and network;
// nil when GeoIP is off.
func (s *Server) panelWhere() *control.Where {
	if !s.geoOn() {
		return nil
	}
	out := &control.Where{Countries: []control.Place{}, Networks: []control.Place{}}
	if db := s.geo.set.Country; db != nil {
		out.Source, out.Built = db.Type, time.Unix(int64(db.Built), 0).UTC()
	} else if db := s.geo.set.ASN; db != nil {
		out.Source, out.Built = db.Type, time.Unix(int64(db.Built), 0).UTC()
	}
	countries, networks := map[string]*control.Place{}, map[uint32]*control.Place{}
	for _, u := range s.net.Users() {
		if u.Service {
			continue
		}
		out.Total++
		if u.Country == "" {
			out.NoCountry++
		} else if p := countries[u.Country]; p != nil {
			p.People++
		} else {
			name := s.geo.set.Lookup(u.IP).Name
			if name == "" {
				name = u.Country
			}
			countries[u.Country] = &control.Place{Code: u.Country, Name: name, People: 1}
		}
		if u.ASN == 0 {
			out.NoNetwork++
		} else if p := networks[u.ASN]; p != nil {
			p.People++
		} else {
			networks[u.ASN] = &control.Place{Code: "AS" + strconv.FormatUint(uint64(u.ASN), 10), Name: u.ASOrg, People: 1}
		}
	}
	out.Countries, out.MoreCountries = topPlaces(countries)
	out.Networks, out.MoreNetworks = topPlaces(networks)
	return out
}

// topPlaces sorts the places, biggest first (then by code), and keeps
// whereMax of them; more is the people in the rest.
func topPlaces[K comparable](m map[K]*control.Place) (list []control.Place, more int) {
	list = make([]control.Place, 0, len(m))
	for _, p := range m {
		list = append(list, *p)
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].People != list[j].People {
			return list[i].People > list[j].People
		}
		return list[i].Code < list[j].Code
	})
	if len(list) > whereMax {
		for _, p := range list[whereMax:] {
			more += p.People
		}
		list = list[:whereMax]
	}
	return list, more
}
