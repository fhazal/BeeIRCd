package server

import (
	"testing"
	"time"

	"github.com/fhazal/beeircd/internal/control"
	"github.com/fhazal/beeircd/internal/store"
)

// The channel page's Stats tab and the My nick tick box.
func TestPanelChanStats(t *testing.T) {
	s, _, _ := fileServer(t, membersOn)
	e := registered(t, s, "ember")
	registered(t, s, "other")
	e.send("JOIN #kampung")
	e.expect(" 366 ember #kampung ")
	e.send("PRIVMSG ChanServ :REGISTER #kampung")
	e.expect("#kampung is registered")
	syncWith(e)
	m := memberSession(t, s, "ember", pw)

	c := myChan(t, s, m, "#kampung")
	if c.Stats == nil || c.Stats.On || len(c.Stats.Periods) != 0 {
		t.Fatalf("stats before SET: %+v", c.Stats)
	}
	q := chanDo(m, "#kampung", "SET", "CHANSTATS", "", "ON")
	if r := panelAsk(t, s, q); r.Done == nil {
		t.Fatalf("SET CHANSTATS ON: %+v", r)
	}
	c = myChan(t, s, m, "#kampung")
	if !c.Toggles["CHANSTATS"] || c.Stats == nil || !c.Stats.On || len(c.Stats.Periods) != 4 {
		t.Fatalf("stats after SET: %+v %+v", c.Toggles, c.Stats)
	}

	reg, _ := s.store.ChannelByName("#kampung")
	a, _ := s.store.AccountByNick("ember")
	s.store.AddChanStats(reg.ID, a.ID, store.ChanStats{Letters: 10, Lines: 2}, time.Now())
	c = myChan(t, s, m, "#kampung")
	for _, p := range c.Stats.Periods {
		if len(p.Top) != 1 || p.Top[0].Nick != "ember" || !p.Top[0].You || p.Top[0].Letters != 10 || p.Totals.Lines != 2 {
			t.Errorf("%s: %+v", p.Key, p)
		}
	}

	// My nick: the tick box is NickServ SET CHANSTATS.
	q = as(m, control.CmdMySet)
	q.Name, q.Value = "CHANSTATS", "ON"
	if r := panelAsk(t, s, q); r.Done == nil {
		t.Fatalf("my-set CHANSTATS ON: %+v", r)
	}
	if !panelAsk(t, s, as(m, control.CmdMyNick)).MyNick.ChanStats || !s.store.AccountChanStats(a.ID) {
		t.Error("CHANSTATS not on after my-set")
	}

	// Someone off the list gets no page at all, so no stats.
	om := memberSession(t, s, "other", pw)
	q = as(om, control.CmdMyChan)
	q.Name = "#kampung"
	if r := panelAsk(t, s, q); r.Chan != nil {
		t.Errorf("stats for someone off the list: %+v", r.Chan)
	}
}
