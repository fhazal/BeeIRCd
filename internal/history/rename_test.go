package history

import (
	"strings"
	"testing"
	"time"
)

// Rename moves a channel's history to the new name, lines and all.
func TestRename(t *testing.T) {
	s := New(0, strings.ToLower)
	lim := Limit{Lines: 100, Age: 24 * time.Hour}
	fill(s, "#old", 3, lim)
	fill(s, "#new", 5, lim) // a stale history under the new name gives way
	before := s.bytes
	s.Rename("#OLD", "#New")
	if got := texts(s.Latest("#old", Ref{Star: true}, 10)); got != "" {
		t.Errorf("old name still answers: %q", got)
	}
	got := s.Latest("#new", Ref{Star: true}, 10)
	if texts(got) != "0,1,2" {
		t.Fatalf("new name: %q", texts(got))
	}
	for _, l := range got {
		if l.Target != "#New" {
			t.Errorf("line target %q", l.Target)
		}
	}
	if s.bytes >= before {
		t.Errorf("bytes %d not below %d after the stale history went", s.bytes, before)
	}
	s.Rename("#new", "#NEW") // case only
	if got := s.Latest("#new", Ref{Star: true}, 10); len(got) != 3 || got[0].Target != "#NEW" {
		t.Errorf("case change: %+v", got)
	}
	s.Rename("#nothing", "#else") // no history: nothing happens
}
