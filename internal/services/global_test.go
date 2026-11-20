package services

import (
	"errors"
	"strings"
	"testing"
)

func TestOperServReload(t *testing.T) {
	svc, h := newServices(t)
	os := h.net.UserByNick("OperServ")
	boss := helpUser(t, h, "1LFAAAAAB", "Boss")
	small := helpUser(t, h, "1LFAAAAAC", "Small")
	h.opers = map[string]string{boss.UID: "oa", small.UID: "og"}

	svc.Message(small.UID, os, "RELOAD")
	if got := noticeTo(h, "Small"); !strings.Contains(got, "You need oper flag a") || len(h.rehashes) != 0 {
		t.Fatalf("RELOAD without flag a: %q, rehashes %v", got, h.rehashes)
	}
	svc.Message(boss.UID, os, "RELOAD")
	if got := noticeTo(h, "Boss"); !strings.Contains(got, "Reloaded the config.") || len(h.rehashes) != 1 || h.rehashes[0] != "Boss" {
		t.Fatalf("RELOAD: %q, rehashes %v", got, h.rehashes)
	}
	h.rehashErr = errors.New("beeircd.conf not used: bad port")
	svc.Message(boss.UID, os, "RELOAD")
	if got := noticeTo(h, "Boss"); !strings.Contains(got, "beeircd.conf not used: bad port") || strings.Contains(got, "Reloaded") {
		t.Fatalf("RELOAD with a bad file: %q", got)
	}

	// RELOAD is config, not services data: READONLY lets it through.
	h.rehashErr = nil
	svc.Message(boss.UID, os, "SET READONLY ON")
	noticeTo(h, "Boss")
	svc.Message(boss.UID, os, "RELOAD")
	if got := noticeTo(h, "Boss"); !strings.Contains(got, "Reloaded the config.") {
		t.Errorf("RELOAD in read-only mode: %q", got)
	}
}
