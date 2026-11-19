package server

import (
	"context"
	"errors"
	"fmt"
	"net"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/fhazal/beeircd/internal/config"
	"github.com/fhazal/beeircd/internal/irc"
	"github.com/fhazal/beeircd/internal/services"
	"github.com/fhazal/beeircd/internal/store"
)

// DNSBL: new connections are looked up in the [[dnsbl]] lists of
// beeircd.conf (like UnrealIRCd's blacklist{} and Anope's
// m_dnsbl). Lookups run off the loop; registration waits for them at most
// dnsblWait, then goes on. LAN and loopback IPs are never looked up, and
// ELINEs and session exceptions skip the check.

const (
	dnsblWait     = 2 * time.Second
	dnsblHitFor   = time.Hour        // a listed IP is remembered this long
	dnsblCleanFor = 10 * time.Minute // and a clean one this long
	dnsblCacheMax = 50000
)

// dnsblResult is what the lists said about one IP.
type dnsblResult struct {
	list  string // "" = clean
	reply int
	until time.Time
}

// dnsblState is the server's DNSBL bookkeeping; it lives on the loop.
type dnsblState struct {
	cache map[string]dnsblResult
	hits  map[string]int  // by list name
	off   map[string]bool // lists switched off with AdminServ DNSBL DISABLE
}

// lookupA resolves a DNSBL query name; replaceable in tests.
func lookupA(ctx context.Context, name string) ([]string, error) {
	return net.DefaultResolver.LookupHost(ctx, name)
}

// dnsblQuery is the name to look ip up as in zone: reversed octets for
// IPv4, reversed nibbles for IPv6. "" for something that isn't an IP.
func dnsblQuery(ip, zone string) string {
	p := net.ParseIP(ip)
	if p == nil {
		return ""
	}
	if v4 := p.To4(); v4 != nil {
		return fmt.Sprintf("%d.%d.%d.%d.%s", v4[3], v4[2], v4[1], v4[0], zone)
	}
	const hex = "0123456789abcdef"
	var b strings.Builder
	for i := len(p) - 1; i >= 0; i-- {
		b.WriteByte(hex[p[i]&0xf])
		b.WriteByte('.')
		b.WriteByte(hex[p[i]>>4])
		b.WriteByte('.')
	}
	return b.String() + zone
}

// dnsblSkipped reports whether ip is never looked up: LAN, loopback,
// link-local or not an IP at all.
func dnsblSkipped(ip string) bool {
	p := net.ParseIP(ip)
	return p == nil || p.IsPrivate() || p.IsLoopback() || p.IsLinkLocalUnicast() || p.IsUnspecified()
}

// dnsblLists are the lists in use now.
func (s *Server) dnsblLists() []config.DNSBL {
	var out []config.DNSBL
	for _, d := range s.cfg.DNSBL {
		if (d.Enabled == nil || *d.Enabled) && !s.dnsbl.off[strings.ToLower(d.Name)] {
			out = append(out, d)
		}
	}
	return out
}

// dnsblAsk looks ip up in every list at once and returns the first hit
// (in the file's order), or a clean result. It runs off the loop.
func (s *Server) dnsblAsk(ctx context.Context, ip string, lists []config.DNSBL) dnsblResult {
	type answer struct {
		i     int
		reply int
	}
	ctx, cancel := context.WithTimeout(ctx, dnsblWait)
	defer cancel()
	ch := make(chan answer, len(lists))
	for i, d := range lists {
		go func(i int, d config.DNSBL) {
			ch <- answer{i, dnsblReply(ctx, s.lookupA, ip, d)}
		}(i, d)
	}
	best := -1
	replies := make([]int, len(lists))
	for range lists {
		a := <-ch
		replies[a.i] = a.reply
		if a.reply > 0 && (best < 0 || a.i < best) {
			best = a.i
		}
	}
	if best < 0 {
		return dnsblResult{until: time.Now().Add(dnsblCleanFor)}
	}
	return dnsblResult{list: lists[best].Name, reply: replies[best], until: time.Now().Add(dnsblHitFor)}
}

// dnsblReply asks one list: the answer N of 127.0.0.N when it counts, else 0.
func dnsblReply(ctx context.Context, lookup func(context.Context, string) ([]string, error), ip string, d config.DNSBL) int {
	n, _, _ := dnsblAnswers(ctx, lookup, ip, d)
	return n
}

// dnsblAnswers asks one list and returns the answer that counts (0 for
// none), every 127.0.0.N answer it gave, and whether the lookup itself
// failed (as opposed to "not listed").
func dnsblAnswers(ctx context.Context, lookup func(context.Context, string) ([]string, error), ip string, d config.DNSBL) (int, []int, error) {
	q := dnsblQuery(ip, strings.Trim(d.Name, "."))
	if q == "" {
		return 0, nil, nil
	}
	addrs, err := lookup(ctx, q)
	if err != nil {
		var dnsErr *net.DNSError
		if errors.As(err, &dnsErr) && dnsErr.IsNotFound {
			return 0, nil, nil // not listed
		}
		return 0, nil, err
	}
	var all []int
	hit := 0
	for _, a := range addrs {
		p := net.ParseIP(a).To4()
		if p == nil || p[0] != 127 {
			continue
		}
		n := int(p[3])
		all = append(all, n)
		if hit == 0 && (len(d.Replies) == 0 || slices.Contains(d.Replies, n)) {
			hit = n
		}
	}
	return hit, all, nil
}

// dnsblStart begins the check for a new connection. c.dnsblBusy holds
// registration until the answer is in (at most dnsblWait).
func (s *Server) dnsblStart(c *Client) {
	lists := s.dnsblLists()
	if len(lists) == 0 || s.dnsblSkip(c.ip) || s.svc.DnsblExempt(c.ip) {
		return
	}
	if r, ok := s.dnsbl.cache[c.ip]; ok && time.Now().Before(r.until) {
		if r.list != "" {
			c.dnsblListed = &r
		}
		return
	}
	c.dnsblBusy = true
	ip, gen := c.ip, c.lookupGen
	go func() {
		r := s.dnsblAsk(context.Background(), ip, lists)
		s.post(func() {
			if len(s.dnsbl.cache) >= dnsblCacheMax {
				s.dnsbl.cache = make(map[string]dnsblResult)
			}
			s.dnsbl.cache[ip] = r
			if c.closed || c.lookupGen != gen {
				return
			}
			c.dnsblBusy = false
			if r.list != "" {
				c.dnsblListed = &r
			}
			s.tryRegister(c)
		})
	}()
}

// dnsblHit acts on a listed connection as its list says, once its nick
// and ident are known (from tryRegister), so the log shows who it was. It reports
// whether the connection was closed.
func (s *Server) dnsblHit(c *Client, r dnsblResult) bool {
	var d config.DNSBL
	for _, l := range s.cfg.DNSBL {
		if strings.EqualFold(l.Name, r.list) {
			d = l
		}
	}
	if d.Name == "" {
		return false
	}
	s.dnsbl.hits[strings.ToLower(d.Name)]++
	s.safety().DNSBLHits++
	reason := dnsblReason(d.Reason, c, r, s.cfg.Server.Network)
	nick := c.nick
	if nick == "" {
		nick = "*"
	}
	mask := nick + "!" + orStar(c.ident) + "@" + c.host
	s.svc.LogDNSBL(mask, c.ip, d.Name)
	s.sno('d', "%s (%s) detected as being on the '%s' DNSBL: %s (%d)", mask, c.ip, d.Name, reason, r.reply)
	switch d.Action {
	case "soft-reject":
		c.softReason = reason // in only once logged in (tryRegister)
		return false
	case "zline":
		s.svc.DnsblBan(store.BanZline, c.ip, reason, d.BanTime.Duration)
	case "akill":
		s.svc.DnsblBan(store.BanAkill, c.ip, reason, d.BanTime.Duration)
	case "shun":
		s.svc.DnsblBan(store.BanShun, c.ip, reason, d.BanTime.Duration)
		return false // they may connect, but can't say anything
	}
	s.reply(c, irc.ErrYoureBannedCreep, "You are banned from this network: "+reason)
	s.quit(c, "Banned: "+reason)
	return true
}

func orStar(s string) string {
	if s == "" {
		return "*"
	}
	return s
}

// dnsblReason fills in a list's reason (Anope's %-codes, plus %b for the
// list's name).
func dnsblReason(tmpl string, c *Client, r dnsblResult, network string) string {
	return strings.NewReplacer(
		"%i", c.ip, "%b", r.list, "%r", "reply "+strconv.Itoa(r.reply), "%n", c.nick,
		"%u", c.ident, "%h", c.host, "%g", c.realname, "%N", network,
	).Replace(tmpl)
}

// dnsblSummary is one line for OperServ STATS ALL.
func (s *Server) dnsblSummary() string {
	if len(s.cfg.DNSBL) == 0 {
		return ""
	}
	var parts []string
	for _, d := range s.cfg.DNSBL {
		name := strings.ToLower(d.Name)
		state := ""
		if d.Enabled != nil && !*d.Enabled || s.dnsbl.off[name] {
			state = " (off)"
		}
		parts = append(parts, fmt.Sprintf("%s %d%s", d.Name, s.dnsbl.hits[name], state))
	}
	return strings.Join(parts, ", ")
}

// asDnsbl is AdminServ DNSBL: LIST, CHECK <ip>, ENABLE|DISABLE <name>.
func asDnsbl(s *Server, c *Client, args []string) {
	if !s.needFlag(c, s.adminServ, 'a') {
		return
	}
	sub, arg := strings.ToUpper(services.ArgAt(args, 0)), services.ArgAt(args, 1)
	find := func(name string) (config.DNSBL, bool) {
		for _, d := range s.cfg.DNSBL {
			if strings.EqualFold(strings.Trim(d.Name, "."), strings.Trim(name, ".")) {
				return d, true
			}
		}
		return config.DNSBL{}, false
	}
	switch sub {
	case "LIST", "":
		if len(s.cfg.DNSBL) == 0 {
			s.as(c, "No DNSBL lists. Add [[dnsbl]] blocks to beeircd.conf.")
			return
		}
		s.as(c, "DNSBL lists:")
		for _, d := range s.cfg.DNSBL {
			name := strings.ToLower(d.Name)
			on := "on"
			if d.Enabled != nil && !*d.Enabled {
				on = "off in beeircd.conf"
			} else if s.dnsbl.off[name] {
				on = "off until restart"
			}
			replies := "every answer"
			if len(d.Replies) > 0 {
				var r []string
				for _, n := range d.Replies {
					r = append(r, strconv.Itoa(n))
				}
				replies = "answers " + strings.Join(r, ",")
			}
			act := d.Action
			if act != "reject" {
				act += " " + config.FormatDuration(d.BanTime.Duration)
			}
			s.as(c, "  %-24s %s · %s · %s · %s", d.Name, on, replies, act, services.Plural(s.dnsbl.hits[name], "hit"))
		}
	case "CHECK":
		if net.ParseIP(arg) == nil {
			s.as(c, "Syntax: DNSBL CHECK <ip>   (127.0.0.2 gets each list's test answer)")
			return
		}
		lists := s.dnsblLists()
		if len(lists) == 0 {
			s.as(c, "No DNSBL list is on.")
			return
		}
		uid := c.uid
		s.as(c, "Looking %s up in %s...", arg, services.Plural(len(lists), "list"))
		go func() {
			type line struct{ name, text string }
			out := make([]line, len(lists))
			done := make(chan struct{}, len(lists))
			ctx, cancel := context.WithTimeout(context.Background(), dnsblWait)
			defer cancel()
			for i, d := range lists {
				go func(i int, d config.DNSBL) {
					n, all, err := dnsblAnswers(ctx, s.lookupA, arg, d)
					var text string
					switch {
					case err != nil:
						text = "lookup failed: " + err.Error()
					case n > 0:
						text = fmt.Sprintf("LISTED (answer %d): %s", n, d.Action)
					case len(all) > 0:
						text = fmt.Sprintf("answers %v, which this list doesn't count (see its replies)", all)
					default:
						text = "not listed"
					}
					out[i] = line{d.Name, text}
					done <- struct{}{}
				}(i, d)
			}
			for range lists {
				<-done
			}
			s.post(func() {
				if c := s.clients[uid]; c != nil && c.user != nil {
					for _, l := range out {
						s.as(c, "  %-24s %s", l.name, l.text)
					}
				}
			})
		}()
	case "ENABLE", "DISABLE":
		d, ok := find(arg)
		if !ok {
			s.as(c, "There is no DNSBL list %s. See /as dnsbl list.", arg)
			return
		}
		name := strings.ToLower(d.Name)
		if sub == "DISABLE" {
			s.dnsbl.off[name] = true
			s.as(c, "%s is off until the server restarts. To keep it off, set enabled = false in its [[dnsbl]] block.", d.Name)
		} else {
			delete(s.dnsbl.off, name)
			if d.Enabled != nil && !*d.Enabled {
				s.as(c, "%s is switched off in beeircd.conf (enabled = false); change it there.", d.Name)
				return
			}
			s.as(c, "%s is on.", d.Name)
		}
		s.audit(c, s.adminServ, "DNSBL %s %s", sub, d.Name)
		s.svc.LogAdmin(c.uid, s.adminServ, sub+" "+d.Name)
	default:
		s.as(c, "Syntax: DNSBL LIST | DNSBL CHECK <ip> | DNSBL ENABLE|DISABLE <name>")
	}
}
