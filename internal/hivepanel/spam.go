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

// The Mail card on Settings: owner only, checked by the server.

type mailIn struct {
	Mail  control.MailForm `json:"mail"`
	To    string           `json:"to"`
	Value string           `json:"value"`
}

func readMail(w http.ResponseWriter, r *http.Request) (mailIn, bool) {
	var in mailIn
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&in) != nil {
		writeErr(w, http.StatusBadRequest, "Bad request.")
		return in, false
	}
	return in, true
}

func (p *Panel) mailSave(w http.ResponseWriter, r *http.Request, oc operCtx) {
	if in, ok := readMail(w, r); ok {
		p.send(w, oc, control.Request{Cmd: control.CmdMailSave, Mail: &in.Mail}, 10*time.Second)
	}
}

func (p *Panel) mailTest(w http.ResponseWriter, r *http.Request, oc operCtx) {
	if in, ok := readMail(w, r); ok {
		p.send(w, oc, control.Request{Cmd: control.CmdMailTest, Mail: &in.Mail, Value: in.To}, 80*time.Second)
	}
}

func (p *Panel) mailReg(w http.ResponseWriter, r *http.Request, oc operCtx) {
	if in, ok := readMail(w, r); ok {
		p.send(w, oc, control.Request{Cmd: control.CmdMailReg, Value: in.Value}, 10*time.Second)
	}
}

func (p *Panel) mailDNS(w http.ResponseWriter, r *http.Request, oc operCtx) {
	p.send(w, oc, control.Request{Cmd: control.CmdMailDNS}, 35*time.Second)
}

// spamReportSet is spam-report-key or spam-report-staging: {"name", "value"}.
func (p *Panel) spamReportSet(cmd string) func(http.ResponseWriter, *http.Request, operCtx) {
	return func(w http.ResponseWriter, r *http.Request, oc operCtx) {
		var in struct{ Name, Value string }
		if json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&in) != nil || in.Name == "" {
			writeErr(w, http.StatusBadRequest, "Bad request.")
			return
		}
		p.send(w, oc, control.Request{Cmd: cmd, Name: in.Name, Value: in.Value}, 10*time.Second)
	}
}

// spamReport is "Report spam" on People: {"uid"}.
func (p *Panel) spamReport(w http.ResponseWriter, r *http.Request, oc operCtx) {
	var in struct{ UID string }
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&in) != nil || in.UID == "" {
		writeErr(w, http.StatusBadRequest, "Bad request.")
		return
	}
	p.send(w, oc, control.Request{Cmd: control.CmdSpamReport, UID: in.UID}, 40*time.Second)
}
