package link

import (
	"strings"
	"testing"

	"github.com/fhazal/beeircd/internal/config"
	"github.com/fhazal/beeircd/internal/irc"
)

const (
	fpHub  = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="
	fpLeaf = "BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBA="
)

func local(name, sid string, blocks ...config.LinkServer) Local {
	known := map[string]bool{strings.ToLower(name): true, sid: true}
	return Local{
		Name: name, SID: sid, Description: "test " + name, Network: "BeeNet", Version: "1.1.0-test",
		ChanModes: "beI,k,l,imnpst", UserModes: "iowx", Prefix: "(ov)@+", CloakHash: "abc", Booted: 1, Now: func() int64 { return 1_800_000_000 },
		Known: func(s string) bool { return known[strings.ToLower(s)] || known[s] },
		Block: func(n string) *config.LinkServer {
			for i := range blocks {
				if strings.EqualFold(blocks[i].Name, n) {
					return &blocks[i]
				}
			}
			return nil
		},
	}
}

func none() []string { return nil }

// pump runs two handshakes against each other until both are done or one
// refuses; it returns the refusal (nil = linked) and which side refused.
func pump(t *testing.T, a, b Handshake) (string, error) {
	t.Helper()
	qa, qb := a.Start(), b.Start() // qa: lines a sends to b
	for i := 0; i < 100 && (len(qa) > 0 || len(qb) > 0); i++ {
		if len(qa) > 0 {
			l := qa[0]
			qa = qa[1:]
			m, err := irc.Parse(l)
			if err != nil {
				t.Fatalf("bad line %q: %v", l, err)
			}
			out, err := b.Handle(m)
			if err != nil {
				return "b", err
			}
			qb = append(qb, out...)
			if b.Done() && len(qb) == 0 {
				qb = append(qb, b.Burst(nil)...)
			}
			continue
		}
		l := qb[0]
		qb = qb[1:]
		m, _ := irc.Parse(l)
		if a.Done() {
			continue // burst lines: the server package handles those
		}
		out, err := a.Handle(m)
		if err != nil {
			return "a", err
		}
		qa = append(qa, out...)
	}
	if !a.Done() || !b.Done() {
		t.Fatalf("handshake stalled: a %v b %v", a.Done(), b.Done())
	}
	return "", nil
}

func TestBeeToBee(t *testing.T) {
	hubBlock := config.LinkServer{Name: "leaf.bee.net", Fingerprint: fpLeaf, Password: "s3cret"}
	leafBlock := config.LinkServer{Name: "hub.bee.net", Host: "10.0.0.1", Port: 7000, Fingerprint: fpHub, Password: "s3cret"}
	leaf := NewUnreal(local("leaf.bee.net", "0LF"), true, &leafBlock, fpHub, none)
	hub := NewUnreal(local("hub.bee.net", "0HB", hubBlock), false, nil, fpLeaf, none)
	if side, err := pump(t, leaf, hub); err != nil {
		t.Fatalf("%s refused: %v", side, err)
	}
	p := hub.Peer()
	if p.Name != "leaf.bee.net" || p.SID != "0LF" || p.Description != "test leaf.bee.net" || !p.Bee || p.Software != "BeeIRCd-1.1.0-test" || p.Protocol != UnrealProtocol {
		t.Errorf("hub sees %+v", p)
	}
	p = leaf.Peer()
	if p.Name != "hub.bee.net" || p.SID != "0HB" || !p.Bee {
		t.Errorf("leaf sees %+v", p)
	}
}

func TestRefusals(t *testing.T) {
	good := func() (Handshake, Handshake) {
		lb := config.LinkServer{Name: "hub.bee.net", Host: "h", Fingerprint: fpHub, Password: "pw"}
		hb := config.LinkServer{Name: "leaf.bee.net", Fingerprint: fpLeaf, Password: "pw"}
		return NewUnreal(local("leaf.bee.net", "0LF"), true, &lb, fpHub, none),
			NewUnreal(local("hub.bee.net", "0HB", hb), false, nil, fpLeaf, none)
	}
	cases := []struct {
		name, want string
		make       func() (Handshake, Handshake)
	}{
		{"wrong fingerprint at hub", "fingerprint does not match", func() (Handshake, Handshake) {
			l, _ := good()
			hb := config.LinkServer{Name: "leaf.bee.net", Fingerprint: fpHub, Password: "pw"}
			return l, NewUnreal(local("hub.bee.net", "0HB", hb), false, nil, fpLeaf, none)
		}},
		{"no link block", "No link block", func() (Handshake, Handshake) {
			l, _ := good()
			return l, NewUnreal(local("hub.bee.net", "0HB"), false, nil, fpLeaf, none)
		}},
		{"wrong password", "wrong password", func() (Handshake, Handshake) {
			lb := config.LinkServer{Name: "hub.bee.net", Host: "h", Fingerprint: fpHub, Password: "nope"}
			_, h := good()
			return NewUnreal(local("leaf.bee.net", "0LF"), true, &lb, fpHub, none), h
		}},
		{"SID collision", "SID collision", func() (Handshake, Handshake) {
			lb := config.LinkServer{Name: "hub.bee.net", Host: "h", Fingerprint: fpHub, Password: "pw"}
			_, h := good()
			return NewUnreal(local("leaf.bee.net", "0HB"), true, &lb, fpHub, none), h
		}},
		{"leaf talks to the wrong server", "expected", func() (Handshake, Handshake) {
			lb := config.LinkServer{Name: "other.bee.net", Host: "h", Fingerprint: fpHub}
			hb := config.LinkServer{Name: "leaf.bee.net", Fingerprint: fpLeaf}
			return NewUnreal(local("leaf.bee.net", "0LF"), true, &lb, fpHub, none),
				NewUnreal(local("hub.bee.net", "0HB", hb), false, nil, fpLeaf, none)
		}},
		{"server already linked (loop)", "Server Exists", func() (Handshake, Handshake) {
			l, _ := good()
			hb := config.LinkServer{Name: "leaf.bee.net", Fingerprint: fpLeaf, Password: "pw"}
			me := local("hub.bee.net", "0HB", hb)
			me.Known = func(s string) bool { return s == "0HB" || strings.EqualFold(s, "hub.bee.net") || s == "0XX" }
			return l, NewUnreal(me, false, nil, fpLeaf, none)
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			a, b := c.make()
			if c.name == "server already linked (loop)" {
				// the leaf already has 0XX behind it, which the hub knows
				lb := config.LinkServer{Name: "hub.bee.net", Host: "h", Fingerprint: fpHub, Password: "pw"}
				a = NewUnreal(local("leaf.bee.net", "0LF"), true, &lb, fpHub, func() []string { return []string{"0XX"} })
			}
			_, err := pump(t, a, b)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("want %q, got %v", c.want, err)
			}
		})
	}
}

func TestClockAndCharsets(t *testing.T) {
	hb := config.LinkServer{Name: "u.example.net", Fingerprint: fpLeaf}
	for want, line := range map[string]string{
		"Incorrect clock":      "PROTOCTL TS=1799999000",
		"allowed-channelchars": "PROTOCTL CHANNELCHARS=any",
		"allowed-nickchars":    "PROTOCTL NICKCHARS=latin-utf8,utf8",
		"old protocol":         "SERVER u.example.net 1 :x",
		"too old":              "PROTOCTL EAUTH=u.example.net,5002,,UnrealIRCd-5.2.4",
	} {
		h := NewUnreal(local("hub.bee.net", "0HB", hb), false, nil, fpLeaf, none)
		if !strings.Contains(want, "too old") && !strings.Contains(want, "old protocol") {
			h.Handle(irc.Message{Command: "PROTOCTL", Params: []string{"EAUTH=u.example.net,6100,,UnrealIRCd-6.2.7"}})
		}
		m, _ := irc.Parse(line)
		_, err := h.Handle(m)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: want %q, got %v", line, want, err)
		}
	}
}

func TestVLInfo(t *testing.T) {
	for in, want := range map[string]string{
		"U6100-Fhin6XeOoE-001 My server": "My server",
		"U6100-B-0BE":                    "U6100-B-0BE",
		"Plain description":              "Plain description",
		"U6100-B-0BE ":                   "server",
	} {
		if got := vlInfo(in); got != want {
			t.Errorf("vlInfo(%q) = %q, want %q", in, got, want)
		}
	}
}
