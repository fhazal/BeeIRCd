package store

import (
	"testing"
	"time"
)

// Kicker settings and bad words survive a reopen and go with the channel.
func TestKickersKept(t *testing.T) {
	s, path := open(t)
	now := time.Now()
	a, err := s.CreateAccount("Bee", "x", "", now)
	if err != nil {
		t.Fatal(err)
	}
	ch, err := s.CreateChannel("#hive", a, "", now)
	if err != nil {
		t.Fatal(err)
	}
	k := DefaultKickers()
	k.On[KickCaps], k.TTB[KickCaps], k.CapsMin = true, 3, 12
	k.On[KickAmsgs] = true
	if err := s.SetChanKickers(ch, k); err != nil {
		t.Fatal(err)
	}
	for _, w := range []string{"wasp", "hornet", "moth"} {
		if err := s.AddBadWord(ch, w, BadWordSingle); err != nil {
			t.Fatal(err)
		}
	}
	list := s.BadWords(ch)
	if err := s.DeleteBadWords(ch, []int64{list[1].ID}); err != nil {
		t.Fatal(err)
	}
	s.Close()

	s2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	ch2, _ := s2.ChannelByName("#hive")
	if got := s2.ChanKickers(ch2); got != k {
		t.Errorf("kickers = %+v, want %+v", got, k)
	}
	got := s2.BadWords(ch2)
	if len(got) != 2 || got[0].Word != "wasp" || got[1].Word != "moth" || got[1].Type != BadWordSingle {
		t.Errorf("bad words = %+v", got)
	}
	if err := s2.DeleteChannel(ch2.ID); err != nil {
		t.Fatal(err)
	}
	ch3, err := s2.CreateChannel("#hive", a, "", now)
	if err != nil {
		t.Fatal(err)
	}
	if s2.ChanKickers(ch3).Any() || len(s2.BadWords(ch3)) != 0 {
		t.Error("a dropped channel's kickers came back")
	}
}
