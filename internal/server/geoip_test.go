package server

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fhazal/beeircd/internal/config"
	"github.com/fhazal/beeircd/internal/control"
	"github.com/fhazal/beeircd/internal/geoip"
	"github.com/fhazal/beeircd/internal/geoip/geoiptest"
)

// GeoIP: the country file is on disk, the ASN file comes from the
// monthly download; both show in WHOIS and work in ~C: and ~A: bans.
func TestGeoIP(t *testing.T) {
	dir := t.TempDir()
	countryFile := geoiptest.Build(24, 6, []geoiptest.Net{{P: "127.0.0.0/8", Data: geoiptest.Country("NL", "Netherlands")}})
	if err := os.WriteFile(filepath.Join(dir, "country.mmdb"), countryFile, 0o644); err != nil {
		t.Fatal(err)
	}
	asnFile := geoiptest.Build(24, 6, []geoiptest.Net{{P: "127.0.0.0/8", Data: map[string]any{
		"autonomous_system_number": uint32(64500), "autonomous_system_organization": "Example Net"}}})
	gz := func(b []byte) []byte {
		var out bytes.Buffer
		zw := gzip.NewWriter(&out)
		zw.Write(b)
		zw.Close()
		return out.Bytes()
	}
	web := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "asn") {
			w.Write(gz(asnFile))
			return
		}
		w.Write(gz(countryFile))
	}))
	defer web.Close()
	old := geoip.DBIPURL
	geoip.DBIPURL = func(kind, month string) string { return web.URL + "/dbip-" + kind + "-lite-" + month + ".mmdb.gz" }
	defer func() { geoip.DBIPURL = old }()

	on := true
	s := testServerWith(t, func(c *config.Config) {
		c.GeoIP = config.GeoIP{Enabled: &on, CountryDB: filepath.Join(dir, "country.mmdb"), ASNDB: filepath.Join(dir, "asn.mmdb")}
	}, func(s *Server) {
		s.geo.first = time.Millisecond
		s.geo.client = web.Client()
	})
	op := login(t, s, "op")
	x := login(t, s, "x")

	// the country is known at once; the network once the download lands
	op.send("WHOIS op")
	op.expect(" 344 op op NL :is connecting from Netherlands")
	deadline := time.Now().Add(3 * time.Second)
	for {
		got := make(chan bool)
		s.post(func() { got <- s.geo.set.ASN != nil })
		if <-got {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the ASN database was not downloaded")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := os.Stat(filepath.Join(dir, "asn.mmdb")); err != nil {
		t.Fatal("download not saved:", err)
	}
	// users already online got their network too
	op.send("WHOIS op")
	op.expect(" 569 op op 64500 :is connecting from AS64500 [Example Net]")

	// others see it only as an oper
	x.send("WHOIS op")
	if line := x.expect(" 318 "); strings.Contains(line, "344") {
		t.Fatal(line)
	}

	op.send("JOIN #geo")
	op.expect("JOIN #geo")
	op.send("MODE #geo +b ~country:nl")
	op.expect("MODE #geo +b ~C:NL")
	x.send("JOIN #geo")
	x.expect(" 474 x #geo ")
	op.send("MODE #geo -b+b ~C:NL ~asn:AS64500")
	op.expect("MODE #geo -b+b ~C:NL ~A:64500")
	x.send("JOIN #geo")
	x.expect(" 474 x #geo ")
	op.send("MODE #geo -b+b ~A:64500 ~C:MY")
	op.expect("MODE #geo -b+b ~A:64500 ~C:MY")
	x.send("JOIN #geo")
	x.expect("JOIN #geo")
}

// The Hive Panel: country and network on the People page and in the
// Overview's counts, only for opers who see hosts.
func TestPanelGeoIP(t *testing.T) {
	country, err := geoip.Parse(geoiptest.Build(24, 6, []geoiptest.Net{{P: "127.0.0.0/8", Data: geoiptest.Country("MY", "Malaysia")}}))
	if err != nil {
		t.Fatal(err)
	}
	asn, err := geoip.Parse(geoiptest.Build(24, 6, []geoiptest.Net{{P: "127.0.0.0/8", Data: map[string]any{
		"autonomous_system_number": uint32(4788), "autonomous_system_organization": "TM Technology"}}}))
	if err != nil {
		t.Fatal(err)
	}
	s, _, _ := fileServer(t, "")
	b := boss(t, s)
	registered(t, s, "ember")
	b.send("AS CLASS watch ADD FLAGS g") // no h: no hosts, no GeoIP
	b.expect("watch")
	b.send("AS OPER ember ADD CLASS watch HOST *@127.0.0.0/8 PASSWORD " + emberPW)
	b.expect("ember is an oper now")
	login(t, s, "guesty")
	ask := func(as string, r control.Request) *control.Response {
		r.As, r.IP = as, "127.0.0.1"
		return panelAsk(t, s, r)
	}

	// GeoIP off: no columns, the Where card says so
	if p := ask("Boss", control.Request{Cmd: control.CmdPeople}).People; p.Geo || p.Rows[0].Country != "" {
		t.Fatalf("GeoIP off: %+v", p)
	}
	if r := ask("Boss", control.Request{Cmd: control.CmdWhere}); r.Where != nil || !strings.Contains(r.Error, "GeoIP is off") {
		t.Fatalf("where, GeoIP off: %+v", r)
	}

	on := true
	s.onLoop(func() {
		s.cfg.GeoIP.Enabled = &on
		s.geo.set = geoip.Set{Country: country, ASN: asn}
		for _, u := range s.net.Users() {
			s.geoFill(u)
		}
	})
	p := ask("Boss", control.Request{Cmd: control.CmdPeople}).People
	if !p.Geo || p.Rows[0].Country != "MY" || p.Rows[0].ASN != 4788 || p.Rows[0].ASOrg != "TM Technology" {
		t.Fatalf("people: %+v", p)
	}
	for _, q := range []string{"my", "Malaysia", "AS4788", "4788", "tm tech"} {
		if p := ask("Boss", control.Request{Cmd: control.CmdPeople, Query: q}).People; p.Matched != p.Online {
			t.Errorf("search %q: %d of %d", q, p.Matched, p.Online)
		}
	}
	if p := ask("Boss", control.Request{Cmd: control.CmdPeople, Query: "SG"}).People; p.Matched != 0 {
		t.Errorf("search SG: %d", p.Matched)
	}
	person := ask("Boss", control.Request{Cmd: control.CmdPerson, UID: p.Rows[0].UID}).Person
	if person == nil || person.CountryName != "Malaysia" || person.ASN != 4788 {
		t.Fatalf("person: %+v", person)
	}
	w := ask("Boss", control.Request{Cmd: control.CmdWhere}).Where
	if w == nil || w.Total != p.Online || len(w.Countries) != 1 || w.Countries[0] != (control.Place{Code: "MY", Name: "Malaysia", People: p.Online}) ||
		w.Networks[0].Code != "AS4788" || w.NoCountry != 0 || w.Source != "Test-Country" || w.Built.IsZero() {
		t.Fatalf("where: %+v", w)
	}

	// no h: nothing about where people are, and no search by it
	pe := ask("ember", control.Request{Cmd: control.CmdPeople}).People
	if pe.Geo || pe.Rows[0].Country != "" || pe.Rows[0].ASN != 0 {
		t.Errorf("ember sees GeoIP: %+v", pe.Rows[0])
	}
	if pe := ask("ember", control.Request{Cmd: control.CmdPeople, Query: "malaysia"}).People; pe.Matched != 0 {
		t.Errorf("ember searched by country: %d", pe.Matched)
	}
	if pr := ask("ember", control.Request{Cmd: control.CmdPerson, UID: p.Rows[0].UID}).Person; pr.CountryName != "" || pr.Country != "" {
		t.Errorf("ember person: %+v", pr)
	}
	if r := ask("ember", control.Request{Cmd: control.CmdWhere}); r.Denied != "flag" {
		t.Errorf("ember where: %+v", r)
	}
}

func TestTopPlaces(t *testing.T) {
	m := map[string]*control.Place{}
	for i := range whereMax + 3 {
		code := fmt.Sprintf("C%03d", i)
		m[code] = &control.Place{Code: code, People: 1}
	}
	m["C010"].People = 9
	list, more := topPlaces(m)
	if len(list) != whereMax || more != 3 || list[0].Code != "C010" || list[1].Code != "C000" {
		t.Errorf("top: %d more %d first %v %v", len(list), more, list[0], list[1])
	}
}
