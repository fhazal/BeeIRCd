package server

// Step 3l: servers place themselves on the network map. A server's place
// is its owner's [[map.place]] for it, else the city of its time zone
// (tzplace: Asia/Kuala_Lumpur is Kuala Lumpur; UTC says nothing). Nothing
// is looked up online. BeeIRCd servers tell each other their own place:
//
//	:<sid> BEEPLACE <server> <lat> <lon> <from> :<place name>
//
// from is "ip" (its IP address, asked of ipinfo.io once), "tz" (its time
// zone) or "owner" (its owner placed it). Sent to
// BeeIRCd links only, when a link has synced and when a place changes;
// passed on like other server lines. A [[map.place]] here always wins over
// what a server says about itself.

import (
	"context"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/fhazal/beeircd/internal/config"
	"github.com/fhazal/beeircd/internal/control"
	"github.com/fhazal/beeircd/internal/tzplace"
	"github.com/fhazal/beeircd/internal/version"
)

// heardPlace is what a server said about its own place.
type heardPlace struct {
	sid  string
	p    control.MapPlace
	from string // "tz" or "owner"
}

var localZone = sync.OnceValue(tzplace.Local)

// selfPlace is where this server says it is.
func (s *Server) selfPlace() (control.MapPlace, string, bool) {
	if p := s.cfg.Map.Place(s.cfg.Server.Name); p != nil {
		return control.MapPlace{Server: s.cfg.Server.Name, Name: p.Name, Lat: p.Lat, Lon: p.Lon}, "owner", true
	}
	if ip := s.links.ipPlace; ip != nil && s.cfg.FindPlaceByIP() {
		p := *ip
		p.Server = s.cfg.Server.Name
		return p, "ip", true
	}
	zone := s.links.zone // set by tests; else the machine's
	if zone == "" {
		zone = localZone()
	}
	if tp, ok := tzplace.Lookup(zone); ok {
		return control.MapPlace{Server: s.cfg.Server.Name, Name: tp.Name, Lat: tp.Lat, Lon: tp.Lon}, "tz", true
	}
	return control.MapPlace{}, "", false
}

func placeLine(sid string, p control.MapPlace, from string) string {
	f := func(v float64) string { return strconv.FormatFloat(math.Round(v*1e4)/1e4, 'f', -1, 64) }
	return ":" + sid + " BEEPLACE " + p.Server + " " + f(p.Lat) + " " + f(p.Lon) + " " + from + " :" + p.Name
}

// sendPlaces gives a BeeIRCd link that just synced our place and the ones
// we heard from servers not behind it.
func (s *Server) sendPlaces(l *linkConn) {
	if !l.peer.Bee {
		return
	}
	if p, from, ok := s.selfPlace(); ok {
		l.send(placeLine(s.cfg.Server.SID, p, from))
	}
	for _, h := range s.links.places {
		if r := s.links.servers[h.sid]; r != nil && r.via != l {
			l.send(placeLine(h.sid, h.p, h.from))
		}
	}
}

// announcePlace tells every BeeIRCd link where we are now.
func (s *Server) announcePlace() {
	p, from, ok := s.selfPlace()
	if !ok {
		return
	}
	line := placeLine(s.cfg.Server.SID, p, from)
	for o := range s.links.conns {
		if o.up && !o.closed && o.eos && o.peer.Bee {
			o.send(line)
		}
	}
}

// remotePlace is BEEPLACE from a link: a server behind it says where it is.
func (s *Server) remotePlace(l *linkConn, source string, params []string) {
	if len(params) < 5 {
		return
	}
	src := s.sourceServer(l, source)
	if src == nil || !strings.EqualFold(src.name, params[0]) {
		return // a server only places itself
	}
	lat, e1 := strconv.ParseFloat(params[1], 64)
	lon, e2 := strconv.ParseFloat(params[2], 64)
	from, name := params[3], strings.Join(strings.Fields(params[4]), " ")
	if e1 != nil || e2 != nil || math.IsNaN(lat) || math.IsNaN(lon) || lat < -90 || lat > 90 || lon < -180 || lon > 180 ||
		(from != "tz" && from != "owner" && from != "ip") || !utf8.ValidString(name) || utf8.RuneCountInString(name) > 60 {
		return
	}
	if s.links.places == nil {
		s.links.places = map[string]heardPlace{}
	}
	s.links.places[strings.ToLower(src.name)] = heardPlace{sid: src.sid, from: from,
		p: control.MapPlace{Server: src.name, Name: name, Lat: lat, Lon: lon}}
	line := placeLine(src.sid, s.links.places[strings.ToLower(src.name)].p, from)
	for o := range s.links.conns {
		if o != l && o.up && !o.closed && o.eos && o.peer.Bee {
			o.send(line)
		}
	}
}

// mapPlaces is every place the map can show: our owner's [[map.place]]
// first, then what servers said about themselves, then our own time zone.
func (s *Server) mapPlaces() []control.MapPlace {
	out := make([]control.MapPlace, 0, len(s.cfg.Map.Places)+len(s.links.places)+1)
	have := map[string]bool{}
	for _, p := range s.cfg.Map.Places {
		out = append(out, control.MapPlace{Server: p.Server, Name: p.Name, Lat: p.Lat, Lon: p.Lon})
		have[strings.ToLower(p.Server)] = true
	}
	for k, h := range s.links.places {
		if !have[k] {
			p := h.p
			p.From = fromWords[h.from]
			out = append(out, p)
			have[k] = true
		}
	}
	if !have[strings.ToLower(s.cfg.Server.Name)] {
		if p, from, ok := s.selfPlace(); ok && from != "owner" {
			p.From = fromWords[from]
			out = append(out, p)
		}
	}
	return out
}

var fromWords = map[string]string{"tz": "its time zone", "owner": "its owner", "ip": "its IP address (ipinfo.io)"}

// placeLookupURL is asked where this server's own IP address is ("" =
// never; the tests set that). Only the server's own address goes out:
// the request carries no other.
var placeLookupURL = "https://ipinfo.io/json"

// ipPlaceFile keeps the answer, so a server asks once a month at most.
const ipPlaceKeep = 30 * 24 * time.Hour

type ipPlaceCache struct {
	At   time.Time `json:"at"`
	Name string    `json:"name"`
	Lat  float64   `json:"lat"`
	Lon  float64   `json:"lon"`
}

func (s *Server) ipPlaceFile() string {
	if s.cfg.Services.Database == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(s.cfg.Services.Database), "map-place.json")
}

// startPlaceLookup finds where this server is from its IP, once: from the
// kept answer when it is fresh, else by asking ipinfo.io. Not when the
// owner placed the server or turned it off ([server] find_place_by_ip).
func (s *Server) startPlaceLookup() {
	if placeLookupURL == "" || os.Getenv("BEEIRCD_NO_PLACE_LOOKUP") == "1" || !s.cfg.FindPlaceByIP() || s.cfg.Map.Place(s.cfg.Server.Name) != nil || s.links.ipAsked {
		return
	}
	s.links.ipAsked = true
	file, url := s.ipPlaceFile(), placeLookupURL
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		p, err := lookupIPPlace(file, url, time.Now())
		s.post(func() {
			if err != nil {
				s.log.Printf("network map: no place from the IP address (%v); the time zone is used", err)
				return
			}
			s.links.ipPlace = p
			s.announcePlace()
		})
	}()
}

// lookupIPPlace reads the kept answer or asks url. The place is rounded
// to 0.1 degree (about 10 km): a city, never a street.
func lookupIPPlace(file, url string, now time.Time) (*control.MapPlace, error) {
	if file != "" {
		if b, err := os.ReadFile(file); err == nil {
			var c ipPlaceCache
			if json.Unmarshal(b, &c) == nil && now.Sub(c.At) < ipPlaceKeep && c.Name != "" {
				return &control.MapPlace{Name: c.Name, Lat: c.Lat, Lon: c.Lon}, nil
			}
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "BeeIRCd/"+version.Version+" (network map)")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, &placeErr{"ipinfo.io answered " + resp.Status}
	}
	var in struct {
		City, Region, Country, Loc string
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 16<<10)).Decode(&in); err != nil {
		return nil, err
	}
	la, lo, ok := strings.Cut(in.Loc, ",")
	lat, e1 := strconv.ParseFloat(strings.TrimSpace(la), 64)
	lon, e2 := strconv.ParseFloat(strings.TrimSpace(lo), 64)
	if !ok || e1 != nil || e2 != nil || lat < -90 || lat > 90 || lon < -180 || lon > 180 {
		return nil, &placeErr{"no place in the answer"}
	}
	name := in.City
	for _, n := range []string{in.Region, in.Country} {
		if name == "" {
			name = n
		}
	}
	name = strings.Join(strings.Fields(name), " ")
	if !utf8.ValidString(name) || utf8.RuneCountInString(name) > 60 || strings.ContainsAny(name, "\"\\") {
		name = ""
	}
	if name == "" {
		name = "Here"
	}
	p := &control.MapPlace{Name: name, Lat: math.Round(lat*10) / 10, Lon: math.Round(lon*10) / 10}
	if file != "" {
		if b, err := json.Marshal(ipPlaceCache{At: now, Name: p.Name, Lat: p.Lat, Lon: p.Lon}); err == nil {
			tmp := file + ".part"
			if os.WriteFile(tmp, b, 0o600) == nil {
				os.Rename(tmp, file)
			}
		}
	}
	return p, nil
}

type placeErr struct{ s string }

func (e *placeErr) Error() string { return e.s }

// samePlace reports whether two [[map.place]] entries (or none) agree.
func samePlace(a, b *config.MapPlace) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}
