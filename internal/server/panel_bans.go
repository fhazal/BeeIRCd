package server

// The Hive Panel's Bans page: the network bans OperServ keeps
// (AKILL, ZLINE, SHUN, SNLINE, exceptions), added and removed with the same checks,
// and a "matches N people" preview while a mask is typed. Flag k, like
// OperServ's ban commands.

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/fhazal/beeircd/internal/config"
	"github.com/fhazal/beeircd/internal/control"
	"github.com/fhazal/beeircd/internal/services"
)

func (s *Server) panelBans(kind string) control.Response {
	if kind == "" {
		kind = services.PanelKinds[0]
	}
	out := &control.Bans{Counts: map[string]int{}, Kind: kind, Default: config.FormatDuration(s.cfg.Services.BanExpiry.Duration)}
	found := false
	for _, k := range services.PanelKinds {
		list := s.svc.PanelBans(k)
		out.Counts[k] = len(list)
		if k != kind {
			continue
		}
		found = true
		out.Rows = make([]control.Ban, 0, len(list))
		for _, b := range list {
			out.Rows = append(out.Rows, control.Ban{Kind: b.Kind, Mask: b.Mask, Reason: b.Reason, SetBy: b.SetBy, SetAt: b.SetAt, Expires: b.Expires})
		}
	}
	if !found {
		return control.Response{Error: "That isn't a kind of ban."}
	}
	return control.Response{Bans: out}
}

func (s *Server) panelBanAdd(po *control.PanelOper, ip string, req control.Request) control.Response {
	d, ok := panelLife(req.Life)
	if !ok {
		return control.Response{Error: "That ban length isn't one I know. Use 1h, 1d, 7d, 30d or 0 for until removed."}
	}
	mask, hit, problem := s.svc.PanelBan(po.Account, req.Kind, req.Mask, req.Reason, d)
	if problem != "" {
		return control.Response{Error: problem}
	}
	name := services.PanelKindName(req.Kind)
	how := "until removed"
	if d < 0 && req.Kind != "eline" {
		d = s.cfg.Services.BanExpiry.Duration
	}
	if d > 0 {
		how = "for " + lengthWords(d)
	}
	s.panelEvent(po.Account, ip, fmt.Sprintf("added a%s %s %s", an(name), name, how))
	what := map[string]string{"akill": "removed", "zline": "removed", "shun": "shunned", "snline": "removed", "eline": "now exempt"}[req.Kind]
	return control.Response{Done: &control.Done{Text: fmt.Sprintf("%s added on %s %s. %d %s.", name, mask, how, hit, what)}}
}

func (s *Server) panelBanDel(po *control.PanelOper, ip string, req control.Request) control.Response {
	mask, problem := s.svc.PanelBanDel(po.Account, req.Kind, req.Mask)
	if problem != "" {
		return control.Response{Error: problem}
	}
	name := services.PanelKindName(req.Kind)
	s.panelEvent(po.Account, ip, "removed a"+an(name)+" "+name)
	return control.Response{Done: &control.Done{Text: name + " on " + mask + " removed."}}
}

func (s *Server) panelBanMatch(req control.Request) control.Response {
	mask, n, problem := s.svc.PanelBanMatch(req.Kind, req.Mask)
	if problem != "" {
		return control.Response{Error: problem}
	}
	return control.Response{Match: &control.Match{Mask: mask, People: n}}
}

// lengthWords says a ban length the way people do: "7 days", "1 hour".
func lengthWords(d time.Duration) string {
	unit := func(n int64, one string) string {
		if n == 1 {
			return "1 " + one
		}
		return strconv.FormatInt(n, 10) + " " + one + "s"
	}
	switch {
	case d%(24*time.Hour) == 0:
		return unit(int64(d/(24*time.Hour)), "day")
	case d%time.Hour == 0:
		return unit(int64(d/time.Hour), "hour")
	default:
		return unit(int64((d+time.Minute-1)/time.Minute), "minute")
	}
}

// an is "n" before a word starting with a vowel, for "a"/"an".
func an(word string) string {
	if word != "" && strings.ContainsRune("AEIOUaeiou", rune(word[0])) {
		return "n"
	}
	return ""
}
