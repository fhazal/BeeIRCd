package mail

import (
	"context"
	"errors"
	"net"
	"slices"
	"strconv"
	"strings"

	"github.com/fhazal/beeircd/internal/config"
)

// A Check is one line of "Will it land in the inbox?".
type Check struct {
	Name  string `json:"name"`  // SPF, DKIM, DMARC, Sender
	State string `json:"state"` // ok, warn, bad
	Text  string `json:"text"`
	Fix   string `json:"fix,omitempty"` // the DNS record to add, ready to copy
}

// Lookup reads the TXT records of a name (net.DefaultResolver.LookupTXT).
type Lookup func(ctx context.Context, name string) ([]string, error)

// dkimSelectors are the names mail servers usually publish their DKIM key
// under (name._domainkey.domain).
var dkimSelectors = []string{"default", "mail", "dkim", "s1", "s2", "k1", "selector1", "selector2", "google", "zmail", "x", "smtp"}

// CheckDomain looks up what decides whether mails from m.SendFrom land in
// the inbox: SPF (who may send for the domain), DKIM (a signing key) and
// DMARC (the domain's policy). lookup nil uses DNS.
func CheckDomain(ctx context.Context, m config.Mail, lookup Lookup) []Check {
	if lookup == nil {
		lookup = net.DefaultResolver.LookupTXT
	}
	dom := Domain(m.SendFrom)
	if dom == "" {
		return nil
	}
	p := PresetFor(m.SMTPServer)
	free := p != nil && slices.Contains(p.Domains, dom)
	var out []Check
	// A free address of one service sent through another fails that
	// service's DMARC: almost always spam.
	for i := range Presets {
		if q := &Presets[i]; slices.Contains(q.Domains, dom) && q != p {
			out = append(out, Check{Name: "Sender", State: "bad", Text: dom + " addresses only reach the inbox when sent through " + q.Name + ". Pick " + q.Name + " above."})
		}
	}
	txt := func(name, prefix string) (found []string, err error) {
		recs, err := lookup(ctx, name)
		var de *net.DNSError
		if errors.As(err, &de) && de.IsNotFound {
			return nil, nil
		}
		for _, r := range recs {
			if strings.HasPrefix(strings.ToLower(strings.TrimSpace(r)), prefix) {
				found = append(found, strings.TrimSpace(r))
			}
		}
		return found, err
	}
	who := "Your mail server"
	if p != nil {
		who = p.Name
	}

	// SPF
	switch spf, err := txt(dom, "v=spf1"); {
	case free:
		out = append(out, Check{Name: "SPF", State: "ok", Text: who + "'s servers may send for " + dom + "."})
	case err != nil && len(spf) == 0:
		out = append(out, Check{Name: "SPF", State: "warn", Text: "Couldn't look up " + dom + ": " + err.Error()})
	case len(spf) == 0:
		out = append(out, Check{Name: "SPF", State: "bad", Text: "No SPF record: other mail servers can't tell who may send for " + dom + ". Add this TXT record at your DNS host:",
			Fix: dom + "  TXT  \"" + spfFor(m, p) + "\""})
	case len(spf) > 1:
		out = append(out, Check{Name: "SPF", State: "bad", Text: dom + " has " + strconv.Itoa(len(spf)) + " SPF records; mail servers ignore them all. Join them into one."})
	case strings.Contains(spf[0], "+all"):
		out = append(out, Check{Name: "SPF", State: "warn", Text: "The SPF record of " + dom + " says anyone may send for it (+all). Use ~all."})
	case !spfAllows(spf[0]):
		out = append(out, Check{Name: "SPF", State: "bad", Text: "The SPF record of " + dom + " says no server may send for it. Change it to:",
			Fix: dom + "  TXT  \"" + spfFor(m, p) + "\""})
	case p != nil && !strings.Contains(strings.ToLower(spf[0]), "include:"+p.SPF):
		out = append(out, Check{Name: "SPF", State: "bad", Text: "The SPF record of " + dom + " doesn't let " + p.Name + " send for it. Change it to:",
			Fix: dom + "  TXT  \"" + addInclude(spf[0], p.SPF) + "\""})
	default:
		out = append(out, Check{Name: "SPF", State: "ok", Text: "Found: " + spf[0]})
	}

	// DKIM
	if free {
		out = append(out, Check{Name: "DKIM", State: "ok", Text: who + " signs every mail."})
	} else {
		sels := dkimSelectors
		if p != nil {
			sels = p.DKIM
		}
		var found string
		for _, sel := range sels {
			if ctx.Err() != nil {
				break
			}
			recs, _ := lookup(ctx, sel+"._domainkey."+dom)
			for _, r := range recs {
				if strings.Contains(strings.ReplaceAll(r, " ", ""), "p=") {
					found = sel
				}
			}
			if found != "" {
				break
			}
		}
		switch {
		case found != "":
			out = append(out, Check{Name: "DKIM", State: "ok", Text: "Signing key found at " + found + "._domainkey." + dom + "."})
		case p != nil:
			out = append(out, Check{Name: "DKIM", State: "bad", Text: "No DKIM key for " + dom + ". Turn on DKIM for your domain in " + p.Name + "'s admin page; it gives you the TXT record to add."})
		default:
			out = append(out, Check{Name: "DKIM", State: "bad", Text: "No DKIM key found under the usual names (default, mail, dkim ...). Turn on DKIM signing in your mail server; it shows you the TXT record to add. (If it already signs under another name, this is fine.)"})
		}
	}

	// DMARC
	switch dm, err := txt("_dmarc."+dom, "v=dmarc1"); {
	case len(dm) > 0:
		pol := "none"
		for _, part := range strings.Split(dm[0], ";") {
			if k, v, ok := strings.Cut(strings.TrimSpace(part), "="); ok && strings.EqualFold(k, "p") {
				pol = strings.ToLower(strings.TrimSpace(v))
			}
		}
		out = append(out, Check{Name: "DMARC", State: "ok", Text: dom + " has a policy (p=" + pol + ")."})
	case err != nil:
		out = append(out, Check{Name: "DMARC", State: "warn", Text: "Couldn't look up _dmarc." + dom + ": " + err.Error()})
	default:
		out = append(out, Check{Name: "DMARC", State: "bad", Text: "No DMARC record. Add this TXT record at your DNS host:",
			Fix: "_dmarc." + dom + "  TXT  \"v=DMARC1; p=none; rua=mailto:postmaster@" + dom + "\""})
	}
	return out
}

// spfAllows reports whether an SPF record lets any server send: it has a
// mechanism besides "all", or a redirect.
func spfAllows(rec string) bool {
	for _, f := range strings.Fields(strings.ToLower(rec))[1:] {
		f = strings.TrimLeft(f, "+-~?")
		if f != "all" && !strings.HasPrefix(f, "exp=") {
			return true
		}
	}
	return false
}

// spfFor is a first SPF record for the way m sends.
func spfFor(m config.Mail, p *Preset) string {
	switch {
	case p != nil:
		return "v=spf1 include:" + p.SPF + " ~all"
	case m.SMTPServer != "":
		return "v=spf1 mx a:" + hostOf(m.SMTPServer) + " ~all"
	}
	return "v=spf1 mx a ~all"
}

// addInclude puts include:x into an SPF record before its last part.
func addInclude(rec, x string) string {
	f := strings.Fields(rec)
	if n := len(f); n > 1 && strings.HasSuffix(f[n-1], "all") {
		return strings.Join(append(f[:n-1:n-1], "include:"+x, f[n-1]), " ")
	}
	return rec + " include:" + x
}
