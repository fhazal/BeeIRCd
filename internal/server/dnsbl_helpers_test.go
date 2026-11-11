package server

import "github.com/fhazal/beeircd/internal/config"

// testDNSBL is a reject list with the defaults SetDefaults gives.
func testDNSBL(name string) config.DNSBL {
	c := &config.Config{DNSBL: []config.DNSBL{{Name: name}}}
	c.SetDefaults()
	return c.DNSBL[0]
}
