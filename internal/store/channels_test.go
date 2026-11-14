package store

import (
	"testing"
	"time"
)

func TestChannelsAndAccess(t *testing.T) {
	s, _ := open(t)
	now := time.Now()
	bee, _ := s.CreateAccount("Bee", "h", "", now)
	ember, _ := s.CreateAccount("ember", "h", "", now)
	kes, _ := s.CreateAccount("Kestrel", "h", "", now)

	c, err := s.CreateChannel("#BChat", bee, "Be kind", now)
	if err != nil || c.Founder != "Bee" || c.MlockOn != "nt" || !c.KeepTopic || c.SecureOps {
		t.Fatalf("CreateChannel = %+v, %v", c, err)
	}
	if _, err := s.CreateChannel("#bchat", ember, "", now); err != ErrExists {
		t.Errorf("same channel again: %v", err)
	}
	if l, _ := s.Level(c, bee.ID); l != LevelFounder {
		t.Errorf("founder level = %d", l)
	}
	if old, _ := s.SetLevel(c, ember, LevelVOP, "Bee", "", now); old != LevelNone {
		t.Errorf("first SetLevel old = %d", old)
	}
	if old, _ := s.SetLevel(c, ember, LevelAOP, "Bee", "", now); old != LevelVOP {
		t.Errorf("move old = %d", old)
	}
	s.SetLevel(c, kes, LevelSOP, "Bee", "", now)
	list, _ := s.AccessList(c, 0)
	if len(list) != 2 || list[0].Account != "Kestrel" || list[1].Level != LevelAOP {
		t.Errorf("AccessList = %+v", list)
	}
	if n, _ := s.ClearLevel(c, LevelAOP); n != 1 {
		t.Errorf("ClearLevel = %d", n)
	}
	s.AddAkick(c, "*!*@bad.example", "spam", "Bee", now)
	if added, _ := s.AddAkick(c, "*!*@bad.example", "spam", "Bee", now); added {
		t.Error("duplicate akick")
	}

	c.Successor, c.SuccessorID, c.SecureOps, c.Topic = "Kestrel", kes.ID, true, "hello"
	if err := s.SaveChannel(c); err != nil {
		t.Fatal(err)
	}
	again, _ := s.ChannelByName("#bchat")
	if again.Successor != "Kestrel" || !again.SecureOps || again.Topic != "hello" {
		t.Errorf("after save: %+v", again)
	}

	// The founder's account goes: the successor takes over.
	changes, err := s.DeleteAccount(bee.ID)
	if err != nil || len(changes) != 1 || changes[0].NewOwner != "Kestrel" {
		t.Fatalf("DeleteAccount changes = %+v, %v", changes, err)
	}
	again, _ = s.ChannelByName("#bchat")
	if again.Founder != "Kestrel" || again.SuccessorID != 0 {
		t.Errorf("after hand-over: %+v", again)
	}
	if l, _ := s.Level(again, kes.ID); l != LevelFounder {
		t.Errorf("new founder level = %d", l)
	}
	// No successor: the channel is dropped with its lists.
	changes, _ = s.DeleteAccount(kes.ID)
	if len(changes) != 1 || changes[0].NewOwner != "" {
		t.Errorf("drop changes = %+v", changes)
	}
	if gone, _ := s.ChannelByName("#bchat"); gone != nil {
		t.Error("channel still there")
	}
	if n, _ := s.ChannelCount(); n != 0 {
		t.Errorf("ChannelCount = %d", n)
	}
}

func TestEntryMsgMigration(t *testing.T) {
	s, path := open(t)
	bee, _ := s.CreateAccount("Bee", "x", "", time.Now())
	c, _ := s.CreateChannel("#bchat", bee, "", time.Now())
	// An older database kept one line in channels.entrymsg.
	if _, err := s.db.Exec("UPDATE channels SET entrymsg = 'Welcome!' WHERE id = ?", c.ID); err != nil {
		t.Fatal(err)
	}
	s.Close()
	for i := 0; i < 2; i++ { // a second start must not copy it again
		s2, err := Open(path)
		if err != nil {
			t.Fatal(err)
		}
		got := s2.EntryMsgs(c)
		if len(got) != 1 || got[0].Text != "Welcome!" || got[0].AddedBy != "Bee" {
			t.Fatalf("start %d: entry messages = %+v", i, got)
		}
		reg, _ := s2.ChannelByName("#bchat")
		if !reg.Peace || !reg.SecureFounder || !reg.AutoOp || reg.BanType != 2 || reg.MemoLimit != DefaultChanMemoLimit {
			t.Fatalf("defaults of an old channel: %+v", reg)
		}
		s2.Close()
	}
}
