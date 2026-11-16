package config

import "path/filepath"

// GeoIP finds each user's country and network (ASN): for ~C: and ~A:
// bans and the WHOIS line opers see. Lookups happen on the server; the
// only thing that goes out is the monthly download of the free DB-IP
// Lite files.
type GeoIP struct {
	// Enabled: on by default.
	Enabled *bool `toml:"enabled"`
	// Download fetches DB-IP Lite (free, CC BY 4.0, "IP data by
	// DB-IP") when the files are missing or over 35 days old. Set false
	// to use your own files, like MaxMind GeoLite2, at country_db and
	// asn_db.
	Download *bool `toml:"download"`
	// CountryDB and ASNDB are the MaxMind DB files (default: geoip/ next
	// to the services database).
	CountryDB string `toml:"country_db"`
	ASNDB     string `toml:"asn_db"`
}

// On reports whether GeoIP lookups run.
func (g GeoIP) On() bool { return g.Enabled == nil || *g.Enabled }

// DownloadOn reports whether the DB-IP Lite files are fetched.
func (g GeoIP) DownloadOn() bool { return g.Download == nil || *g.Download }

// GeoIPFiles are the country and ASN database paths.
func (c *Config) GeoIPFiles() (country, asn string) {
	dir := filepath.Join(filepath.Dir(c.Services.Database), "geoip")
	country, asn = c.GeoIP.CountryDB, c.GeoIP.ASNDB
	if country == "" {
		country = filepath.Join(dir, "country.mmdb")
	}
	if asn == "" {
		asn = filepath.Join(dir, "asn.mmdb")
	}
	return country, asn
}
