package services

import (
	"testing"
	"time"

	"github.com/fhazal/beeircd/internal/store"
)

// The text helpers match Anope 2.0.20 (misc.cpp Duration, os_akill, the
// expiry lines in operserv.cpp / os_forbid / os_session).
func TestAnopeLogText(t *testing.T) {
	for d, want := range map[time.Duration]string{
		45 * time.Second:                 "45 seconds",
		time.Second:                      "1 second",
		time.Minute:                      "1 minute",
		time.Hour:                        "1 hour",
		26*time.Hour + 5*time.Minute:     "1 day, 2 hours, 5 minutes",
		366 * 24 * time.Hour:             "1 year, 1 day",
		2*time.Hour + 30*time.Second:     "2 hours",
		730*24*time.Hour + 3*time.Minute: "2 years, 3 minutes",
	} {
		if got := anopeDuration(d); got != want {
			t.Errorf("anopeDuration(%v) = %q, want %q", d, got, want)
		}
	}
	if got := anopeLineAdd("*@1.2.3.4", "spam", 0, 1, 4); got != "on *@1.2.3.4 (spam), expires in never [affects 1 user(s) (25%)]" {
		t.Errorf("anopeLineAdd = %q", got)
	}
	for e, want := range map[store.Expired]string{
		{What: "netban", Kind: store.BanAkill, Mask: "*@x"}:    "AKILL on \x02*@x\x02 has expired",
		{What: "forbid", Kind: store.ForbidNick, Mask: "Bad*"}: "Expiring forbid for Bad* type NICK",
		{What: "exception", Mask: "10.0.0.0/8"}:                "Session exception for 10.0.0.0/8 has expired.",
	} {
		if got := anopeExpired(e); got != want {
			t.Errorf("anopeExpired(%+v) = %q, want %q", e, got, want)
		}
	}
}
