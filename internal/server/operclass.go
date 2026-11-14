package server

import (
	"strings"

	"github.com/fhazal/beeircd/internal/irc"
	"github.com/fhazal/beeircd/internal/state"
	"github.com/fhazal/beeircd/internal/store"
)

// Oper classes. What an oper may do and how they look is their
// class (an [[operclass]] or a built-in one) with the entry's own extras on
// top: more flags, and a title, vhost or swhois lines of its own.

// operView is an oper entry as it applies: class plus entry.
type operView struct {
	class  string
	flags  string
	title  string
	vhost  string
	swhois []string
}

// viewOf works out what entry o gives.
func (s *Server) viewOf(o *store.Oper) operView {
	v := operView{class: o.Class, flags: o.Flags, title: o.Title, vhost: o.Vhost, swhois: o.Swhois}
	if o.Class != "" {
		if oc, ok := s.cfg.Class(o.Class); ok {
			v.class = oc.Name
			v.flags = oc.Flags + o.Flags
			if v.title == "" {
				v.title = oc.Title
			}
			if v.vhost == "" {
				v.vhost = oc.Vhost
			}
			if len(v.swhois) == 0 {
				v.swhois = oc.Swhois
			}
		}
	}
	v.flags, _ = cleanFlags(v.flags)
	return v
}

// operHostOK reports whether u connects from a host on the oper entry:
// user@host masks with globs or CIDR, against the real host, the IP and
// the shown host.
func operHostOK(o *store.Oper, u *state.User) bool {
	for _, m := range o.Hosts {
		if irc.MatchUserHost(m, u.TrueIdent(), u.RealHost, u.IP, u.Host) {
			return true
		}
	}
	return false
}

// adminHosts are where the config admin may /oper from ("*@*" when
// admin_hosts is empty).
func (s *Server) adminHosts() []string {
	var out []string
	for _, h := range s.cfg.Services.AdminHosts {
		if m, ok := operMask(h); ok {
			out = append(out, m)
		}
	}
	if len(out) == 0 {
		return []string{"*@*"}
	}
	return out
}

// whoisOperText is the 313 line: a title in UnrealIRCd's form ("is a
// Network Administrator") as it is, an older short title in brackets.
func whoisOperText(title string) string {
	switch {
	case title == "":
		return "is an IRC Operator"
	case strings.HasPrefix(strings.ToLower(title), "is "):
		return title
	}
	return "is an IRC Operator (" + title + ")"
}

// showOperVhost puts the oper vhost on c, or takes an old one off.
func (s *Server) showOperVhost(c *Client, vhost string) {
	u := c.user
	switch {
	case vhost != "" && vhost != u.Host:
		c.operVhost = vhost
		s.emit(&state.HostChange{UID: u.UID, Host: vhost})
		s.reply(c, irc.RplHostHidden, vhost, "is now your displayed host")
	case vhost != "":
		c.operVhost = vhost
	case c.operVhost != "":
		c.operVhost = ""
		s.svc.RestoreHost(c.uid)
	}
}

// coversOper reports whether c holds every flag oper entry o gives, so it
// may change or remove the entry (or put it back with UNDO). Otherwise c
// is told why not.
func (s *Server) coversOper(c *Client, o *store.Oper) bool {
	eff := s.viewOf(o).flags
	for i := 0; i < len(eff); i++ {
		if !c.can(eff[i]) {
			s.as(c, "You can't change %s: they have flag %c and you don't.", o.Account, eff[i])
			return false
		}
	}
	return true
}
