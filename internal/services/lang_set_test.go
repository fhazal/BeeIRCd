package services

import (
	"strings"
	"testing"
)

// NickServ SET LANGUAGE: replies come in the chosen language, the choice
// is kept with the account, and default_language covers everyone else.
func TestSetLanguage(t *testing.T) {
	svc, h := newServices(t)
	ns, cs := h.net.UserByNick("NickServ"), h.net.UserByNick("ChanServ")
	bee := helpUser(t, h, "1LFAAAAAA", "Bee")
	svc.Message(bee.UID, ns, "REGISTER honeypot42")
	h.waitFor("Bee", "registered")

	svc.Message(bee.UID, ns, "SET LANGUAGE")
	if got := noticeTo(h, "Bee"); !strings.Contains(got, "Syntax: SET LANGUAGE <language>") || !strings.Contains(got, "ms  Bahasa Melayu") {
		t.Errorf("SET LANGUAGE alone: %q", got)
	}
	svc.Message(bee.UID, ns, "SET LANGUAGE ms")
	if got := noticeTo(h, "Bee"); got != "Bahasa ditukar kepada Bahasa Melayu." {
		t.Errorf("SET LANGUAGE ms: %q", got)
	}
	if a, _ := svc.store.AccountByNick("Bee"); a == nil || a.Language != "ms" {
		t.Fatalf("account after SET LANGUAGE ms: %+v", a)
	}
	svc.Message(bee.UID, ns, "FROBNICATE")
	if got := noticeTo(h, "Bee"); !strings.Contains(got, "Arahan FROBNICATE tidak dikenali.") {
		t.Errorf("unknown command in Malay: %q", got)
	}
	svc.Message(bee.UID, cs, "HELP")
	if got := noticeTo(h, "Bee"); !strings.Contains(got, "ChanServ memastikan saluran kekal milik anda.") || !strings.Contains(got, "daftar saluran yang anda op di dalamnya") {
		t.Errorf("ChanServ HELP in Malay: %q", got)
	}
	svc.Message(bee.UID, ns, "SET LANGUAGE xx")
	if got := noticeTo(h, "Bee"); !strings.Contains(got, "Bahasa xx tidak dikenali.") {
		t.Errorf("SET LANGUAGE xx: %q", got)
	}
	svc.Message(bee.UID, ns, "SET LANGUAGE en")
	if got := noticeTo(h, "Bee"); got != "Language changed to English." {
		t.Errorf("SET LANGUAGE en: %q", got)
	}

	// default_language: someone without an account gets Malay; Bee keeps English.
	svc.cfg.Services.DefaultLanguage = "ms"
	guest := helpUser(t, h, "1LFAAAAAB", "Kumbang")
	svc.Message(guest.UID, ns, "INFO Nobody")
	if got := noticeTo(h, "Kumbang"); !strings.Contains(got, "Nobody tidak didaftarkan.") {
		t.Errorf("default_language ms: %q", got)
	}
	svc.Message(bee.UID, ns, "INFO Nobody")
	if got := noticeTo(h, "Bee"); !strings.Contains(got, "Nobody isn't registered.") {
		t.Errorf("Bee chose English: %q", got)
	}
}
