package server

import (
	"errors"
	"strings"
	"time"

	"github.com/fhazal/beeircd/internal/control"
	"github.com/fhazal/beeircd/internal/services"
	"github.com/fhazal/beeircd/internal/spamfilter"
)

// The Hive Panel's Spam filter page (after 1.0): the same list and checks
// as /SPAMFILTER, flag k.

// panelSpam is the page.
func (s *Server) panelSpam() *control.Response {
	now := time.Now()
	p := &control.Spam{Rules: []control.SpamRule{}, Caught24h: s.svc.SpamCaught24h(now), Except: append([]string{}, s.cfg.SpamFilter.Except...)}
	for _, f := range s.svc.SpamFilters() {
		h := s.svc.SpamHitsOf(f.ID)
		p.Rules = append(p.Rules, control.SpamRule{ID: f.ID, Type: f.Type, Match: f.Match, Targets: f.Targets, Words: f.Words(),
			Action: f.Action, Does: services.ActionWords(f), Reason: f.Reason, SetBy: f.SetBy, SetAt: f.SetAt, Expires: f.Expires,
			Hits: h.Count, Last: h.Last, Unusable: s.svc.SpamUnusable(f.ID)})
	}
	return &control.Response{Spam: p}
}

// spamFromReq reads a rule from the page's fields.
func spamFromReq(req control.Request) (spamfilter.Filter, string) {
	f := spamfilter.Filter{Type: strings.ToLower(req.Kind), Match: strings.TrimSpace(req.Mask), Targets: req.Value,
		Action: strings.ToLower(req.Name), Reason: strings.TrimSpace(req.Reason)}
	if f.Type == "" {
		f.Type = spamfilter.Simple
	}
	if spamfilter.Bans(f.Action) && req.Life != "" && req.Life != "0" {
		d, err := spamDuration(req.Life)
		if err != nil {
			return f, err.Error()
		}
		f.BanTime = d
	}
	return f, ""
}

// panelSpamCheck is what adding the rule would do.
func (s *Server) panelSpamCheck(req control.Request) *control.Response {
	f, problem := spamFromReq(req)
	if problem != "" {
		return &control.Response{SpamCheck: &control.SpamCheck{Problem: problem}}
	}
	problem, unsafe, lines := s.svc.CheckSpamFilter(f)
	return &control.Response{SpamCheck: &control.SpamCheck{Problem: problem, Unsafe: unsafe, Lines: lines}}
}

// panelSpamAdd adds a rule for po (req.New = add anyway).
func (s *Server) panelSpamAdd(po *control.PanelOper, req control.Request) *control.Response {
	f, problem := spamFromReq(req)
	if problem != "" {
		return &control.Response{Error: problem}
	}
	got, err := s.svc.AddSpamFilter(f, po.Account, req.New)
	var safe *services.SafetyError
	switch {
	case errors.As(err, &safe):
		return &control.Response{Error: "It would also catch normal chat. Narrow the match, or add it anyway.",
			SpamCheck: &control.SpamCheck{Unsafe: safe.Lines}}
	case errors.Is(err, services.ErrSpamDuplicate):
		return &control.Response{Error: "That rule is there already."}
	case err != nil:
		return &control.Response{Error: upperFirst(err.Error()) + "."}
	}
	s.panelEvent(po.Account, req.IP, "added spam filter "+services.Describe(got))
	return &control.Response{Done: &control.Done{Text: "Added: " + services.Describe(got)}}
}

// panelSpamDel removes rule req.ID for po.
func (s *Server) panelSpamDel(po *control.PanelOper, req control.Request) *control.Response {
	f, err := s.svc.DelSpamFilter(req.ID, po.Account)
	if err != nil {
		return &control.Response{Error: upperFirst(err.Error()) + "."}
	}
	s.panelEvent(po.Account, req.IP, "removed spam filter "+services.Describe(f))
	return &control.Response{Done: &control.Done{Text: "Removed: " + services.Describe(f)}}
}

// panelSpamTest is which rules would catch a line.
func (s *Server) panelSpamTest(req control.Request) *control.Response {
	targets, err := spamfilter.NormTargets(req.Value)
	if err != nil {
		return &control.Response{Error: upperFirst(err.Error()) + "."}
	}
	t := &control.SpamTest{Hits: []control.SpamTestHit{}}
	for _, h := range s.spamTest(targets, req.Mask) {
		t.Hits = append(t.Hits, control.SpamTestHit{ID: h.ID, Match: h.Match, Where: spamfilter.TargetWords[h.target],
			Does: services.ActionWords(h.Filter), Reason: h.Reason})
	}
	return &control.Response{SpamTest: t}
}
