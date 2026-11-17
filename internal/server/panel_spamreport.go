package server

import (
	"strings"
	"time"

	"github.com/fhazal/beeircd/internal/control"
)

// The Hive Panel side of [[spamreport]]: the places on Settings (flag a,
// the key is written but never shown) and "Report spam" on People (flag k,
// like /SPAMREPORT).

// panelSpamReports lists the places.
func (s *Server) panelSpamReports() *control.SpamReports {
	out := &control.SpamReports{Places: []control.SpamPlace{}}
	for _, r := range s.cfg.SpamReports {
		out.Places = append(out.Places, control.SpamPlace{
			Name:    r.Name,
			Type:    r.Type,
			KeySet:  r.RPCKey != "",
			Staging: r.Staging,
			On:      r.On(),
			Auto:    r.Auto("spamfilter"),
			Line:    spamReportLine(r),
		})
	}
	return out
}

// panelSpamReportKey saves or removes a DroneBL key.
func (s *Server) panelSpamReportKey(po *control.PanelOper, ip string, req control.Request) control.Response {
	name := strings.ToLower(strings.TrimSpace(req.Name))
	key := strings.TrimSpace(req.Value)
	msg, problem := s.setSpamReportKey(po.Account+" (Hive Panel)", name, key)
	if problem != "" {
		return control.Response{Error: problem}
	}
	what := "saved the DroneBL key of " + name
	if key == "" {
		what = "removed the DroneBL key of " + name
	}
	s.panelEvent(po.Account, ip, what)
	return control.Response{Done: &control.Done{Text: msg}, SpamReports: s.panelSpamReports()}
}

// panelSpamReportStaging turns DroneBL's test list on or off.
func (s *Server) panelSpamReportStaging(po *control.PanelOper, ip string, req control.Request) control.Response {
	name := strings.ToLower(strings.TrimSpace(req.Name))
	var on bool
	switch strings.ToLower(req.Value) {
	case "on":
		on = true
	case "off":
	default:
		return control.Response{Error: "Bad request."}
	}
	msg, problem := s.setSpamReportStaging(po.Account+" (Hive Panel)", name, on)
	if problem != "" {
		return control.Response{Error: problem}
	}
	s.panelEvent(po.Account, ip, "turned DroneBL's test list "+strings.ToLower(req.Value)+" for "+name)
	return control.Response{Done: &control.Done{Text: msg}, SpamReports: s.panelSpamReports()}
}

// panelSpamReport reports the person req.UID and waits for the answers.
func (s *Server) panelSpamReport(req control.Request, run func(byte, func(*control.PanelOper)) control.Response) control.Response {
	ch := make(chan []string, 1)
	problem := ""
	r := run('k', func(po *control.PanelOper) {
		u, p := s.panelTarget(req.UID)
		if p != "" {
			problem = p
			return
		}
		t, p := s.spamTargetOf(u.Nick)
		if p != "" {
			problem = p
			return
		}
		s.reportSpam(t, "", "reported by "+po.Account, false, func(lines []string) {
			for _, l := range lines {
				if rest, ok := strings.CutPrefix(l, "Reported "); ok {
					s.panelEvent(po.Account, req.IP, "reported "+strings.TrimSuffix(rest, "."))
				}
			}
			ch <- lines
		})
	})
	switch {
	case r.Error != "":
		return r
	case problem != "":
		return control.Response{Error: problem}
	}
	select {
	case lines := <-ch:
		text := strings.Join(lines, " ")
		for _, l := range lines {
			if strings.HasPrefix(l, "Reported ") {
				return control.Response{Done: &control.Done{Text: text}}
			}
		}
		return control.Response{Error: text}
	case <-time.After(spamReportTimeout + 5*time.Second):
		return control.Response{Error: "The report is still being sent; its answer will be in the server notices."}
	}
}
