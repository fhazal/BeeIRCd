package server

import (
	"testing"

	"github.com/fhazal/beeircd/internal/config"
)

// operRank is the InspIRCd oper type: WHOIS's rank without "is a".
func TestOperRank(t *testing.T) {
	s := testServer(t, func(c *config.Config) {
		c.OperClasses = append(c.OperClasses, config.OperClass{Name: "keeper", Flags: "h", Title: "is the Hive Keeper"})
	})
	for _, c := range []struct{ class, title, want string }{
		{"netadmin", "", "Network Administrator"},
		{"admin", "", "Server Administrator"},
		{"globop", "", "IRC Operator"},
		{"keeper", "", "Hive Keeper"},
		{"netadmin", "is a Queen Bee", "Queen Bee"},
		{"nosuch", "", "IRC Operator"},
	} {
		if got := s.operRank(c.class, c.title); got != c.want {
			t.Errorf("operRank(%q, %q) = %q, want %q", c.class, c.title, got, c.want)
		}
	}
}
