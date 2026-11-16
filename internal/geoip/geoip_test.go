package geoip

import (
	"bytes"
	"compress/gzip"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fhazal/beeircd/internal/geoip/geoiptest"
)

func TestLookup(t *testing.T) {
	nets := []geoiptest.Net{
		{P: "192.0.2.0/24", Data: geoiptest.Country("NL", "Netherlands")},
		{P: "198.51.100.0/25", Data: geoiptest.Country("MY", "Malaysia")},
		{P: "2001:db8::/32", Data: geoiptest.Country("DE", "Germany")},
	}
	for _, rs := range []int{24, 28, 32} {
		for _, v := range []int{4, 6} {
			db, err := Parse(geoiptest.Build(rs, v, nets))
			if err != nil {
				t.Fatalf("record size %d, IPv%d: %v", rs, v, err)
			}
			set := &Set{Country: db}
			for ip, want := range map[string]string{
				"192.0.2.77": "NL", "198.51.100.1": "MY", "198.51.100.200": "", "203.0.113.1": "",
				"::ffff:192.0.2.1": "NL", "2001:db8::1": "DE", "not an ip": "",
			} {
				if v == 4 && strings.HasPrefix(ip, "2001") {
					want = ""
				}
				if got := set.Lookup(ip).Country; got != want {
					t.Errorf("record size %d, IPv%d: %s = %q, want %q", rs, v, ip, got, want)
				}
			}
			if got := set.Lookup("198.51.100.1").Name; got != "Malaysia" {
				t.Errorf("name %q", got)
			}
		}
	}
	asn, err := Parse(geoiptest.Build(24, 6, []geoiptest.Net{{P: "192.0.2.0/24", Data: map[string]any{
		"autonomous_system_number": uint32(64500), "autonomous_system_organization": "Example Net"}}}))
	if err != nil {
		t.Fatal(err)
	}
	if got := (&Set{ASN: asn}).Lookup("192.0.2.5"); got.ASN != 64500 || got.ASOrg != "Example Net" {
		t.Errorf("ASN = %+v", got)
	}
	if got := (*Set)(nil).Lookup("192.0.2.5"); got != (Info{}) {
		t.Errorf("nil set = %+v", got)
	}
}

func TestDecoderPointersAndBroken(t *testing.T) {
	// a map whose value is a pointer back to the string at 0
	buf := append(geoiptest.Enc("hello"), geoiptest.Enc(map[string]any{})...)
	start := len(geoiptest.Enc("hello"))
	buf = buf[:start]
	buf = append(buf, 0xe1)                  // map, 1 entry
	buf = append(buf, geoiptest.Enc("k")...) // key
	buf = append(buf, 0x20, 0x00)            // pointer, 11 bits: offset 0
	d := &decoder{buf: buf}
	v, _, err := d.decode(uint(start), 0)
	if err != nil || v.(map[string]any)["k"] != "hello" {
		t.Fatalf("pointer: %v %v", v, err)
	}
	for _, bad := range [][]byte{{0x45}, {0xe2, 0x41, 'a'}, {0x20}, {0x20, 0x05}} {
		if _, _, err := (&decoder{buf: bad}).decode(0, 0); err == nil {
			t.Errorf("% x: no error", bad)
		}
	}
	for _, bad := range [][]byte{nil, []byte("junk"), append([]byte("x"), metaMarker...)} {
		if _, err := Parse(bad); err == nil {
			t.Errorf("Parse(%q) took it", bad)
		}
	}
}

func TestDownload(t *testing.T) {
	file := geoiptest.Build(24, 6, []geoiptest.Net{{P: "192.0.2.0/24", Data: geoiptest.Country("NL", "Netherlands")}})
	var gz bytes.Buffer
	zw := gzip.NewWriter(&gz)
	zw.Write(file)
	zw.Close()
	var asked []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked = append(asked, r.URL.Path)
		if strings.Contains(r.URL.Path, "2026-11") { // not out yet this month
			http.NotFound(w, r)
			return
		}
		w.Write(gz.Bytes())
	}))
	defer srv.Close()
	old := DBIPURL
	DBIPURL = func(kind, month string) string { return srv.URL + "/dbip-" + kind + "-lite-" + month + ".mmdb.gz" }
	defer func() { DBIPURL = old }()

	path := filepath.Join(t.TempDir(), "geoip", "country.mmdb")
	now := time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC)
	db, err := Download(context.Background(), srv.Client(), "country", path, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(asked) != 2 || !strings.HasSuffix(asked[1], "dbip-country-lite-2026-10.mmdb.gz") {
		t.Errorf("asked %q", asked)
	}
	if (&Set{Country: db}).Lookup("192.0.2.1").Country != "NL" {
		t.Error("downloaded file doesn't work")
	}
	if on, err := Open(path); err != nil || on.Type != "Test-Country" {
		t.Errorf("saved file: %v", err)
	}
	if Stale(db, now) || !Stale(db, now.AddDate(0, 2, 0)) || !Stale(nil, now) {
		t.Error("Stale")
	}
	// a broken download leaves the working file alone
	gz.Reset()
	gz.WriteString("not gzip")
	if _, err := Download(context.Background(), srv.Client(), "country", path, now); err == nil {
		t.Error("a broken file was taken")
	}
	if b, _ := os.ReadFile(path); !bytes.Equal(b, file) {
		t.Error("the working file was replaced")
	}
}

func FuzzParse(f *testing.F) {
	f.Add(geoiptest.Build(24, 6, []geoiptest.Net{{P: "192.0.2.0/24", Data: geoiptest.Country("NL", "Netherlands")}}))
	f.Add(geoiptest.Build(28, 4, []geoiptest.Net{{P: "192.0.2.0/24", Data: geoiptest.Country("MY", "Malaysia")}}))
	f.Fuzz(func(t *testing.T, b []byte) {
		db, err := Parse(b)
		if err != nil {
			return
		}
		set := &Set{Country: db, ASN: db}
		for _, ip := range []string{"192.0.2.1", "10.0.0.1", "2001:db8::1", "::"} {
			set.Lookup(ip)
		}
	})
}
