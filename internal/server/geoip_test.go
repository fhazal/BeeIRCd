package server

import (
	"bytes"
	"compress/gzip"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fhazal/beeircd/internal/config"
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
