package link

import "testing"

// InspIRCd's m_swhois keeps one extra WHOIS line per user, sent as
// METADATA <uid> swhois. Both ways it was dropped; it now comes in as a
// SWHOIS line of tag "swhois" (one at a time, also in the burst, after the
// UID) and our SWHOIS goes out as that METADATA.
func TestInspBridgeSwhois(t *testing.T) {
	b := testBridge(t)
	run := func(l string) []string {
		r, _ := b.In(l)
		return r
	}
	run(":1IN UID 1INAAAAAA 1700000000 insp real.host cloak.host ~i ~i 192.0.2.1 1700000000 +i :Insp User")
	if r := run(":1IN METADATA 1INAAAAAA swhois :is in the burst"); len(r) != 0 {
		t.Fatalf("burst swhois before the UID: %q", r)
	}
	r := run(":1IN ENDBURST")
	if len(r) < 3 || !has(r[:1], " UID insp ") || !has(r[1:2], ":1IN SWHOIS 1INAAAAAA - swhois 0 :*") || !has(r[2:3], ":1IN SWHOIS 1INAAAAAA + swhois 0 :is in the burst") {
		t.Fatalf("burst UID + swhois: %q", r)
	}
	r = run(":1IN METADATA 1INAAAAAA swhois :a new line")
	if len(r) != 2 || !has(r[1:], ":1IN SWHOIS 1INAAAAAA + swhois 0 :a new line") {
		t.Errorf("swhois set: %q", r)
	}
	r = run(":1IN METADATA 1INAAAAAA swhois")
	if len(r) != 1 || !has(r, ":1IN SWHOIS 1INAAAAAA - swhois 0 :*") {
		t.Errorf("swhois cleared: %q", r)
	}

	if got := b.Out(":0BE SWHOIS 0BEAAAAAA + beeoper 0 :is a bee"); !has(got, ":0BE METADATA 0BEAAAAAA swhois :is a bee") {
		t.Errorf("SWHOIS out: %q", got)
	}
	if got := b.Out(":0BE SWHOIS 0BEAAAAAA - beeoper 0 :*"); len(got) != 1 || got[0] != ":0BE METADATA 0BEAAAAAA swhois" {
		t.Errorf("SWHOIS delete out: %q", got)
	}
}
