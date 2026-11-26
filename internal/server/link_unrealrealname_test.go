package server

import (
	"github.com/fhazal/beeircd/internal/link"
	"testing"

	"github.com/fhazal/beeircd/internal/store"
)

func snlines(hub *linkNode) (masks []string) {
	hub.s.onLoop(func() {
		for _, b := range hub.s.svc.LinkedBans() {
			if b.Kind == store.BanSNLine {
				masks = append(masks, b.Mask)
			}
		}
	})
	return masks
}

// A realname ban (RLINE) was kept off non-BeeIRCd links. Now ours goes to
// UnrealIRCd as G ~realname:, theirs lands in our list and goes on to the
// other links, and the removal works both ways.
func TestUnrealRealnameBan(t *testing.T) {
	hub, hop, _, _ := globopsTree(t)
	// The tree's links came up as BeeIRCd; make them talk plain UnrealIRCd.
	hub.s.onLoop(func() {
		for l := range hub.s.links.conns {
			l.codec = link.NewUnrealCodec()
		}
	})

	// Ours out.
	hop.send("OS SNLINE ADD +1h *free?bitcoin* spam")
	waitLink(t, "ban to a", func() bool { return hub.logHas("link a.test.local -> :0HB TKL + G ~realname: *free?bitcoin* ") })
	waitLink(t, "ban to b", func() bool { return hub.logHas("link b.test.local -> :0HB TKL + G ~realname: *free?bitcoin* ") })
	hop.send("OS SNLINE DEL *free?bitcoin*")
	waitLink(t, "removal to a", func() bool { return hub.logHas("link a.test.local -> :0HB TKL - G ~realname: *free?bitcoin* ") })

	// Theirs in (a space is written _), and on to b but not back to a.
	hub.fromLinkTo(t, "a.test.local", ":0AA TKL + G ~realname: *pay_now* ann!a@h 0 1700000000 :scam")
	waitLink(t, "in our list", func() bool {
		m := snlines(hub)
		return len(m) == 1 && m[0] == "*pay now*"
	})
	waitLink(t, "on to b", func() bool { return hub.logHas("link b.test.local -> :0HB TKL + G ~realname: *pay_now* ") })
	if hub.logHas("link a.test.local -> :0HB TKL + G ~realname: *pay_now*") {
		t.Errorf("ban sent back to where it came from")
	}
	// Removal from them.
	hub.fromLinkTo(t, "a.test.local", ":0AA TKL - G ~realname: *pay_now* ann!a@h")
	waitLink(t, "gone from our list", func() bool { return len(snlines(hub)) == 0 })
	waitLink(t, "removal on to b", func() bool { return hub.logHas("link b.test.local -> :0HB TKL - G ~realname: *pay_now* ") })
}
