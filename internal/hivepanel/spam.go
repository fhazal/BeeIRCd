package hivepanel

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/fhazal/beeircd/internal/control"
)

// The Spam filter page (after 1.0): flag k, checked by the server.

// spamIn is a rule as the page sends it.
type spamIn struct {
	Type, Targets, Action, Life, Reason, Match, Text string
	ID                                               int64
	Force                                            bool
}

func readSpam(w http.ResponseWriter, r *http.Request) (spamIn, bool) {
	var in spamIn
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10)).Decode(&in) != nil || len(in.Match) > 500 || len(in.Reason) > 400 || len(in.Text) > 1000 {
		writeErr(w, http.StatusBadRequest, "Bad request.")
		return in, false
	}
	return in, true
}

// spamRule is spam-check or spam-add.
func (p *Panel) spamRule(cmd string) func(http.ResponseWriter, *http.Request, operCtx) {
	return func(w http.ResponseWriter, r *http.Request, oc operCtx) {
		in, ok := readSpam(w, r)
		if !ok {
			return
		}
		p.send(w, oc, control.Request{Cmd: cmd, Kind: in.Type, Value: in.Targets, Name: in.Action, Life: in.Life,
			Reason: in.Reason, Mask: in.Match, New: in.Force}, 10*time.Second)
	}
}

func (p *Panel) spamDel(w http.ResponseWriter, r *http.Request, oc operCtx) {
	in, ok := readSpam(w, r)
	if !ok || in.ID <= 0 {
		if ok {
			writeErr(w, http.StatusBadRequest, "Bad request.")
		}
		return
	}
	p.send(w, oc, control.Request{Cmd: control.CmdSpamDel, ID: in.ID}, 10*time.Second)
}

func (p *Panel) spamTest(w http.ResponseWriter, r *http.Request, oc operCtx) {
	in, ok := readSpam(w, r)
	if !ok {
		return
	}
	p.send(w, oc, control.Request{Cmd: control.CmdSpamTest, Value: in.Targets, Mask: in.Text}, 10*time.Second)
}
