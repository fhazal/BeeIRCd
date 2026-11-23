package store

import (
	"testing"
	"time"
)

// RenameChannel moves the registration, read markers, AJOIN entries and
// last-seen lines to the new name in one go.
func TestRenameChannel(t *testing.T) {
	s, _ := open(t)
	now := time.Now()
	bee, _ := s.CreateAccount("Bee", "h", "", now)
	ember, _ := s.CreateAccount("ember", "h", "", now)
	c, err := s.CreateChannel("#Hive", bee, "the hive", now)
	if err != nil {
		t.Fatal(err)
	}
	s.SetLevel(c, ember, LevelAOP, "Bee", "", now)
	s.CreateChannel("#taken", ember, "", now)
	s.SetReadMarker(ReadMarker{Account: "bee", Target: "#hive", At: now})
	s.SetReadMarker(ReadMarker{Account: "bee", Target: "#comb", At: now.Add(-time.Hour)}) // gives way
	s.SetReadMarker(ReadMarker{Account: "ember", Target: "#other", At: now})
	s.AddAjoin(ember, "#HIVE", "")
	s.AddAjoin(ember, "#other", "")
	if err := s.SaveSeen([]Seen{
		{Nick: "ember", Mask: "e@h", Kind: "join", Channel: "#hive", Last: now},
		{Nick: "kes", Mask: "k@h", Kind: "join", Channel: "#other", Last: now},
	}, nil); err != nil {
		t.Fatal(err)
	}

	if err := s.RenameChannel("#hive", "#taken"); err != ErrExists {
		t.Errorf("onto another registered channel: %v", err)
	}
	if err := s.RenameChannel("#hive", "#Comb"); err != nil {
		t.Fatal(err)
	}
	if old, _ := s.ChannelByName("#hive"); old != nil {
		t.Errorf("old name still registered: %+v", old)
	}
	moved, _ := s.ChannelByName("#comb")
	if moved == nil || moved.ID != c.ID || moved.Name != "#Comb" || moved.Description != "the hive" {
		t.Fatalf("moved channel %+v", moved)
	}
	if list, _ := s.AccessList(moved, 0); len(list) != 1 || list[0].Account != "ember" {
		t.Errorf("access list %+v", list)
	}
	ms, _ := s.ReadMarkers()
	got := map[string]time.Time{}
	for _, m := range ms {
		got[m.Account+" "+m.Target] = m.At
	}
	if len(got) != 2 || got["bee #comb"].UnixMilli() != now.UnixMilli() || got["ember #other"].IsZero() {
		t.Errorf("read markers %+v", ms)
	}
	if aj := s.Ajoins(ember); len(aj) != 2 || aj[0].Channel != "#Comb" || aj[1].Channel != "#other" {
		t.Errorf("ajoin %+v", aj)
	}
	seen, _ := s.AllSeen()
	for _, e := range seen {
		if want := map[string]string{"ember": "#Comb", "kes": "#other"}[e.Nick]; e.Channel != want {
			t.Errorf("seen %s in %s, want %s", e.Nick, e.Channel, want)
		}
	}

	// A change of case keeps the keys and sets the new spelling.
	if err := s.RenameChannel("#comb", "#COMB"); err != nil {
		t.Fatal(err)
	}
	if c, _ := s.ChannelByName("#comb"); c == nil || c.Name != "#COMB" {
		t.Errorf("case change: %+v", c)
	}
	// An unregistered channel moves its markers too.
	if err := s.RenameChannel("#other", "#else"); err != nil {
		t.Fatal(err)
	}
	ms, _ = s.ReadMarkers()
	if len(ms) != 2 {
		t.Errorf("markers after an unregistered rename %+v", ms)
	}
	for _, m := range ms {
		if m.Target == "#other" {
			t.Errorf("marker left behind: %+v", m)
		}
	}
	if err := s.VerifyCache(); err != nil {
		t.Error(err)
	}
}
