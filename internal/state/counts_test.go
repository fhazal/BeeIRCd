package state

import (
	"fmt"
	"math/rand/v2"
	"testing"
)

// recount is PeopleCounts done the slow way.
func recount(n *Network) (total, invisible, opers int) {
	for _, u := range n.users {
		if u.Service {
			continue
		}
		total++
		if u.Modes['i'] {
			invisible++
		}
		if u.Modes['o'] {
			opers++
		}
	}
	return
}

// The LUSERS counts kept by Apply always match a full recount.
func TestPeopleCountsStayRight(t *testing.T) {
	n := NewNetwork("0BE")
	r := rand.New(rand.NewPCG(1, 2))
	var uids []string
	for step := 0; step < 5000; step++ {
		switch op := r.IntN(4); {
		case op == 0 || len(uids) == 0:
			u := &User{UID: n.NextUID(), Nick: fmt.Sprintf("n%d", step), Service: r.IntN(10) == 0, Modes: map[byte]bool{'i': r.IntN(2) == 0}}
			if err := n.Apply(&UserAdd{User: u}); err != nil {
				t.Fatal(err)
			}
			uids = append(uids, u.UID)
		case op == 1:
			i := r.IntN(len(uids))
			n.Apply(&UserQuit{UID: uids[i]})
			uids = append(uids[:i], uids[i+1:]...)
		default:
			m := []byte("iow")[r.IntN(3)]
			n.Apply(&UserMode{UID: uids[r.IntN(len(uids))], Changes: []ModeChange{{Add: r.IntN(2) == 0, Mode: m}, {Add: r.IntN(2) == 0, Mode: m}}})
		}
		a, b, c := n.PeopleCounts()
		x, y, z := recount(n)
		if a != x || b != y || c != z {
			t.Fatalf("step %d: counts %d/%d/%d, recount %d/%d/%d", step, a, b, c, x, y, z)
		}
	}
	cl := n.Clone()
	if ok, why := Equal(n, cl); !ok {
		t.Fatalf("clone differs: %s", why)
	}
}
