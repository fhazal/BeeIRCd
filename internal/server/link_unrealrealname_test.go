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

// Through a real InspIRCd bridge: our realname ban goes out as an R-line
// (a regex over the realname part only), theirs of that shape comes back
// as our glob, one we cannot enforce gives the opers one notice, and the
// DELLINE removes what was added.
func TestInspRlineBan(t *testing.T) {
	hub, hop, _, _ := globopsTree(t)
	b := inspBridgeTo(t, "a.test.local", "0AA")
	hub.s.onLoop(func() {
		for l := range hub.s.links.conns {
			if l.peer.Name == "a.test.local" {
				l.codec = link.NewUnrealCodec()
				l.bridge = b
			}
		}
	})
	hop.send("MODE hubop +s +G")
	hop.send("PING :umode")
	hop.expect(" :umode")
	const bot = `^[^[:space:]]+[[:space:]].*[bB][oO][tT].*$`
	const in = `^[^[:space:]]+[[:space:]].*[sS][pP][aA][mM].*$`

	b.Out(":0HB EOS") // the bridge's first line out carries its burst
	hop.send("OS SNLINE ADD +1h *bot* spam")
	waitLink(t, "R-line to the InspIRCd link", func() bool { return hub.logHas("ADDLINE R " + bot + " ") })
	if hub.logHas("ADDLINE R *bot*") {
		t.Errorf("the glob went out as the regex")
	}
	hop.send("OS SNLINE DEL *bot*")
	waitLink(t, "DELLINE", func() bool { return hub.logHas("DELLINE R " + bot) })

	feed := func(line string) { // an InspIRCd line, as the link reads it
		hub.s.onLoop(func() {
			for l := range hub.s.links.conns {
				if l.peer.Name == "a.test.local" {
					hub.s.handleLinkLine(l, line)
				}
			}
		})
	}
	feed(":0AA ADDLINE R " + in + " ann!a@h 1700000000 0 :spam")
	waitLink(t, "R-line in as our glob", func() bool {
		m := snlines(hub)
		return len(m) == 1 && m[0] == "*spam*"
	})
	feed(":0AA DELLINE R " + in)
	waitLink(t, "R-line removed", func() bool { return len(snlines(hub)) == 0 })

	feed(":0AA ADDLINE R bot ann!a@h 1700000000 0 :spam")
	hop.expect("InspIRCd R-line bot not copied")
	hop.quiet("InspIRCd R-line bot not copied")
	if len(snlines(hub)) != 0 {
		t.Errorf("an R-line over the whole text was kept: %v", snlines(hub))
	}
}
