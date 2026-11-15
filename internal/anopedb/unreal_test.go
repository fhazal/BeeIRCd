package anopedb

import (
	"io"
	"strings"
	"testing"
	"time"

	"github.com/fhazal/beeircd/internal/passwd"
)

func TestImportUnrealOpers(t *testing.T) {
	st := newStore(t)
	hash, _ := passwd.Hash("oper-secret-1")
	for _, n := range []string{"Belon", "Psycho", "Claude", "Guard", "Plain"} {
		if _, err := st.CreateAccount(n, "!", "", time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	conf := `/* Test config, shaped like a real network's */
# a comment line
operclass guard {
	permissions {
		channel { operonly; }
		immune;
		kill;
		server-ban { shun; zline; gline; }
	}
}
set { help-channel "#Help"; oper-auto-join #opers; }
oper Belon {
	class opers;
	mask *@*;
	password "` + hash + `";
	operclass netadmin;
	swhois "is a Network Administrator";
	swhois "looks very helpful";
	vhost netadmin.example.org;
	snomask bBcCdfkqsSoO;
};
oper Psycho { class opers; mask { *@203.0.113.*; *@198.51.100.7; }; password "` + hash + `"; operclass globop; swhois "IRC Operator"; };
oper Claude {
	class opers;
	mask { certfp "AA:BB:` + strings.Repeat("cc", 30) + `"; };
	operclass admin;
};
oper Guard { class opers; mask { certfp "` + strings.Repeat("ab", 32) + `"; }; operclass guard; vhost botnet.example.org; };
oper Plain { mask *@*; password "plaintext-pw"; operclass locop; };
oper Ghost { mask *@*; password "` + hash + `"; operclass netadmin; };
`
	r := &Report{}
	if err := ImportUnrealOpers(st, []io.Reader{strings.NewReader(conf)}, Options{}, r); err != nil {
		t.Fatal(err)
	}
	get := func(n string) (cls, flags string, hosts, fps, swhois []string, vhost, pass string) {
		a, _ := st.AccountByNick(n)
		o, _ := st.OperByAccount(a.ID)
		if o == nil {
			t.Fatalf("no oper entry for %s", n)
		}
		return o.Class, o.Flags, o.Hosts, o.CertFPs, o.Swhois, o.Vhost, o.PassHash
	}
	if cls, _, hosts, _, sw, vh, pass := get("Belon"); cls != "netadmin" || hosts[0] != "*@*" || len(sw) != 1 || sw[0] != "looks very helpful" || vh != "netadmin.example.org" || !passwd.Check("oper-secret-1", pass) {
		t.Errorf("Belon: %s %v %v %s", cls, hosts, sw, vh)
	}
	// a swhois with the rank but without "is a"/"is an" is the same line
	if cls, _, hosts, _, sw, _, _ := get("Psycho"); cls != "globop" || len(hosts) != 2 || hosts[1] != "*@198.51.100.7" || len(sw) != 0 {
		t.Errorf("Psycho: %s %v swhois %v", cls, hosts, sw)
	}
	if cls, _, _, fps, _, _, pass := get("Claude"); cls != "admin" || len(fps) != 1 || fps[0] != "aabb"+strings.Repeat("cc", 30) || pass != "" {
		t.Errorf("Claude: %s %v %q", cls, fps, pass)
	}
	if cls, flags, _, fps, _, vh, _ := get("Guard"); cls != "" || flags != "k" || len(fps) != 1 || vh != "botnet.example.org" {
		t.Errorf("Guard: class %q flags %q fps %v vhost %q", cls, flags, fps, vh)
	}
	if cls, _, _, _, _, _, pass := get("Plain"); cls != "helper" || !passwd.Check("plaintext-pw", pass) {
		t.Errorf("Plain: %s", cls)
	}
	if len(r.In) != 1 || r.In[0].N != 5 {
		t.Errorf("report in %v", r.In)
	}
	found := false
	for _, s := range r.Skipped {
		if strings.Contains(s, "Ghost") && strings.Contains(s, "no account") {
			found = true
		}
	}
	if !found {
		t.Errorf("Ghost (no account) not reported: %v", r.Skipped)
	}

	// Again: the entries are there now, BeeIRCd's stay.
	r2 := &Report{}
	if err := ImportUnrealOpers(st, []io.Reader{strings.NewReader(conf)}, Options{}, r2); err != nil {
		t.Fatal(err)
	}
	if len(r2.Clashes) != 5 {
		t.Errorf("second run clashes %v", r2.Clashes)
	}
}

func TestParseUnrealErrors(t *testing.T) {
	for _, bad := range []string{"oper x {", "oper x }", `oper "x;`, "/* open"} {
		if _, err := parseUnreal(strings.NewReader(bad)); err == nil {
			t.Errorf("parseUnreal(%q) took it", bad)
		}
	}
}
