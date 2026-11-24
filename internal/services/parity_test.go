package services

import (
	"strings"
	"testing"
)

// [services] max_accounts_per_email (Anope's ns_maxemail).
func TestMaxAccountsPerEmail(t *testing.T) {
	svc, h := newServices(t)
	svc.cfg.Services.MaxAccountsPerEmail = 1
	ns := h.net.UserByNick("NickServ")
	bee := helpUser(t, h, "1LFAAAAAA", "Bee")
	wasp := helpUser(t, h, "1LFAAAAAB", "Wasp")

	svc.Message(bee.UID, ns, "REGISTER honeypot42 bee@example.org")
	h.waitFor("Bee", "registered")
	svc.Message(wasp.UID, ns, "REGISTER honeypot42 BEE@Example.ORG")
	svc.Flush()
	if got := noticeTo(h, "Wasp"); !strings.Contains(got, "reached its limit of accounts here (1)") {
		t.Fatalf("second REGISTER on the same email: %q", got)
	}
	svc.Message(wasp.UID, ns, "REGISTER honeypot42 wasp@example.org")
	h.waitFor("Wasp", "registered")

	svc.Message(wasp.UID, ns, "SET EMAIL Bee@example.org")
	svc.Flush()
	if got := noticeTo(h, "Wasp"); !strings.Contains(got, "reached its limit") {
		t.Fatalf("SET EMAIL to a full address: %q", got)
	}
	// Setting your own address again isn't refused.
	svc.Message(bee.UID, ns, "SET EMAIL BEE@example.org")
	svc.Flush()
	if got := noticeTo(h, "Bee"); strings.Contains(got, "reached its limit") {
		t.Fatalf("SET EMAIL to own address: %q", got)
	}

	// An oper's SASET isn't held to the limit.
	h.opers = map[string]string{bee.UID: "s"}
	svc.Message(bee.UID, ns, "SASET EMAIL Wasp bee@example.org")
	svc.Flush()
	if got := noticeTo(h, "Bee"); strings.Contains(got, "reached its limit") {
		t.Fatalf("SASET EMAIL: %q", got)
	}
	if a, _ := svc.store.AccountByNick("Wasp"); a == nil || a.Email != "bee@example.org" {
		t.Fatalf("Wasp after SASET: %+v", a)
	}

	// 0 is no limit.
	svc.cfg.Services.MaxAccountsPerEmail = 0
	drone := helpUser(t, h, "1LFAAAAAC", "Drone")
	svc.Message(drone.UID, ns, "REGISTER honeypot42 bee@example.org")
	h.waitFor("Drone", "registered")
}
