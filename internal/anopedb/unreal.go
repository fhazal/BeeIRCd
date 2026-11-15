package anopedb

// UnrealIRCd 6 oper blocks, for networks moving from UnrealIRCd + Anope:
// oper names, password hashes (argon2id, the same format BeeIRCd uses),
// certificate fingerprints, classes, vhosts and swhois lines.

import (
	"fmt"
	"io"
	"strings"
	"time"
	"unicode"

	"github.com/fhazal/beeircd/internal/config"
	"github.com/fhazal/beeircd/internal/irc"
	"github.com/fhazal/beeircd/internal/passwd"
	"github.com/fhazal/beeircd/internal/store"
)

// confEntry is one item of an UnrealIRCd config: `name value;` or
// `name value { ... };`.
type confEntry struct {
	Name, Value string
	Kids        []*confEntry
}

func (e *confEntry) kid(name string) *confEntry {
	for _, k := range e.Kids {
		if strings.EqualFold(k.Name, name) {
			return k
		}
	}
	return nil
}

func (e *confEntry) all(name string) []*confEntry {
	var out []*confEntry
	for _, k := range e.Kids {
		if strings.EqualFold(k.Name, name) {
			out = append(out, k)
		}
	}
	return out
}

// parseUnreal reads UnrealIRCd's config syntax: words and "quoted strings",
// { } blocks, ; ends, comments with //, # and /* */.
func parseUnreal(r io.Reader) ([]*confEntry, error) {
	b, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	toks, err := unrealTokens(string(b))
	if err != nil {
		return nil, err
	}
	pos := 0
	var block func(depth int) ([]*confEntry, error)
	block = func(depth int) ([]*confEntry, error) {
		var out []*confEntry
		for pos < len(toks) {
			t := toks[pos]
			switch {
			case t == "}":
				pos++
				if depth == 0 {
					return nil, fmt.Errorf("a } without its {")
				}
				if pos < len(toks) && toks[pos] == ";" {
					pos++
				}
				return out, nil
			case t == ";":
				pos++
				continue
			}
			e := &confEntry{Name: unquote(t)}
			pos++
			var vals []string
			for pos < len(toks) && toks[pos] != ";" && toks[pos] != "{" && toks[pos] != "}" {
				vals = append(vals, unquote(toks[pos]))
				pos++
			}
			e.Value = strings.Join(vals, " ")
			if pos < len(toks) && toks[pos] == "{" {
				pos++
				kids, err := block(depth + 1)
				if err != nil {
					return nil, err
				}
				e.Kids = kids
			} else if pos < len(toks) && toks[pos] == ";" {
				pos++
			}
			out = append(out, e)
		}
		if depth > 0 {
			return nil, fmt.Errorf("a { is never closed")
		}
		return out, nil
	}
	return block(0)
}

func unquote(t string) string {
	if len(t) >= 2 && t[0] == '"' && t[len(t)-1] == '"' {
		return strings.ReplaceAll(t[1:len(t)-1], `\"`, `"`)
	}
	return t
}

func unrealTokens(s string) ([]string, error) {
	var out []string
	for i := 0; i < len(s); {
		c := s[i]
		switch {
		case unicode.IsSpace(rune(c)):
			i++
		case (c == '#' && lineStart(s, i)) || (c == '/' && i+1 < len(s) && s[i+1] == '/'):
			for i < len(s) && s[i] != '\n' {
				i++
			}
		case c == '/' && i+1 < len(s) && s[i+1] == '*':
			end := strings.Index(s[i+2:], "*/")
			if end < 0 {
				return nil, fmt.Errorf("a /* comment is never closed")
			}
			i += end + 4
		case c == '{' || c == '}' || c == ';':
			out = append(out, string(c))
			i++
		case c == '"':
			j := i + 1
			for j < len(s) && s[j] != '"' {
				if s[j] == '\\' {
					j++
				}
				j++
			}
			if j >= len(s) {
				return nil, fmt.Errorf("a quoted string is never closed")
			}
			out = append(out, s[i:j+1])
			i = j + 1
		default:
			j := i
			for j < len(s) && !unicode.IsSpace(rune(s[j])) && !strings.ContainsRune("{};\"", rune(s[j])) {
				j++
			}
			out = append(out, s[i:j])
			i = j
		}
	}
	return out, nil
}

// lineStart reports whether only spaces come before s[i] on its line.
func lineStart(s string, i int) bool {
	for j := i - 1; j >= 0 && s[j] != '\n'; j-- {
		if s[j] != ' ' && s[j] != '\t' && s[j] != '\r' {
			return false
		}
	}
	return true
}

// unrealClasses are UnrealIRCd's own operclasses on BeeIRCd's classes.
var unrealClasses = map[string]string{
	"netadmin": "netadmin", "netadmin-with-override": "netadmin",
	"services-admin": "admin", "services-admin-with-override": "admin",
	"admin": "admin", "admin-with-override": "admin",
	"globop": "globop", "globop-with-override": "globop",
	"locop": "helper",
}

// classFlags gives BeeIRCd flags for an operclass of the config's own,
// from the permissions it grants.
func classFlags(oc *confEntry) string {
	flags := ""
	add := func(f byte) {
		if strings.IndexByte(flags, f) < 0 {
			flags += string(f)
		}
	}
	var walk func(e *confEntry, path string)
	walk = func(e *confEntry, path string) {
		for _, k := range e.Kids {
			p := strings.ToLower(strings.Trim(path+":"+k.Name, ":"))
			switch {
			case strings.Contains(p, "kill"), strings.Contains(p, "server-ban"), strings.Contains(p, "tkl"):
				add('k')
			case strings.Contains(p, "chghost"), strings.Contains(p, "chgident"), strings.Contains(p, "sethost"), strings.Contains(p, "chgname"):
				add('h')
			case strings.Contains(p, "route"), strings.Contains(p, "connect"), strings.Contains(p, "squit"):
				add('c')
			case strings.Contains(p, "sajoin"), strings.Contains(p, "sapart"), strings.Contains(p, "sanick"), strings.Contains(p, "samode"), strings.Contains(p, "server:rehash"):
				add('a')
			case strings.Contains(p, "wallops"), strings.Contains(p, "globops"), strings.Contains(p, "notice:global"):
				add('g')
			}
			walk(k, p)
		}
	}
	if perms := oc.kid("permissions"); perms != nil {
		walk(perms, "")
	}
	return flags
}

// ImportUnrealOpers adds UnrealIRCd's oper blocks (from unrealircd.conf
// and the files it includes, as given) to st as BeeIRCd oper entries. An
// oper's name must be a registered nick: entries belong to accounts here.
func ImportUnrealOpers(st *store.Store, confs []io.Reader, opt Options, r *Report) error {
	var entries []*confEntry
	for _, c := range confs {
		e, err := parseUnreal(c)
		if err != nil {
			return err
		}
		entries = append(entries, e...)
	}
	classes := map[string]*confEntry{}
	for _, e := range entries {
		if strings.EqualFold(e.Name, "operclass") {
			classes[strings.ToLower(e.Value)] = e
		}
	}
	add := func(line string) { r.Skipped = append(r.Skipped, line) }
	n := 0
	for _, e := range entries {
		if !strings.EqualFold(e.Name, "oper") {
			continue
		}
		name := e.Value
		a, _ := st.AccountByNick(name)
		if a == nil {
			add(fmt.Sprintf("Unreal oper %s: no account with that nick (register it, then /as oper %s add ...)", name, name))
			continue
		}
		if have, _ := st.OperByAccount(a.ID); have != nil && !opt.Replace {
			r.Clashes = append(r.Clashes, fmt.Sprintf("oper %s: BeeIRCd's oper entry stays; Unreal's is skipped", name))
			continue
		}
		o := &store.Oper{AccountID: a.ID, Account: a.Name, AddedBy: "UnrealIRCd import", AddedAt: time.Now()}
		// Where from: a plain mask, or a mask block with masks and certfps.
		if m := e.kid("mask"); m != nil {
			if m.Value != "" {
				o.Hosts = append(o.Hosts, strings.Fields(m.Value)...)
			}
			for _, k := range m.Kids {
				switch strings.ToLower(k.Name) {
				case "certfp":
					o.CertFPs = append(o.CertFPs, strings.ToLower(strings.ReplaceAll(k.Value, ":", "")))
				case "mask", "ip":
					if k.Value != "" {
						h := k.Value
						if !strings.Contains(h, "@") {
							h = "*@" + h
						}
						o.Hosts = append(o.Hosts, h)
					}
					for _, kk := range k.Kids {
						h := kk.Name
						if !strings.Contains(h, "@") {
							h = "*@" + h
						}
						o.Hosts = append(o.Hosts, h)
					}
				default:
					if k.Value == "" && len(k.Kids) == 0 { // a bare host mask in the list
						h := k.Name
						if !strings.Contains(h, "@") {
							h = "*@" + h
						}
						o.Hosts = append(o.Hosts, h)
						continue
					}
					add(fmt.Sprintf("Unreal oper %s: mask %s %q (BeeIRCd matches hosts and certificates only)", name, k.Name, k.Value))
				}
			}
		}
		if len(o.Hosts) == 0 {
			o.Hosts = []string{"*@*"}
		}
		// Password: argon2id hashes as they are; plain text gets hashed.
		if p := e.kid("password"); p != nil && p.Value != "" {
			switch {
			case strings.HasPrefix(p.Value, "$argon2id$"):
				o.PassHash = p.Value
			case strings.HasPrefix(p.Value, "$"):
				add(fmt.Sprintf("Unreal oper %s: password hash kind %s can't be checked here; set one: /as oper %s set password ...", name, strings.SplitN(p.Value[1:], "$", 2)[0], name))
			default:
				h, err := passwd.Hash(p.Value)
				if err != nil {
					return err
				}
				o.PassHash = h
			}
		}
		if o.PassHash == "" && len(o.CertFPs) == 0 {
			add(fmt.Sprintf("Unreal oper %s: neither a usable password nor a certificate: set one with AdminServ", name))
		}
		// Class.
		cls := strings.ToLower(valueOf(e.kid("operclass")))
		if c, ok := unrealClasses[cls]; ok {
			o.Class = c
		} else if oc := classes[cls]; oc != nil {
			if parent := strings.ToLower(valueOf(oc.kid("parent"))); unrealClasses[parent] != "" {
				o.Class = unrealClasses[parent]
			}
			o.Flags = classFlags(oc)
			if o.Class == "" && o.Flags == "" {
				add(fmt.Sprintf("Unreal oper %s: operclass %s grants nothing BeeIRCd knows", name, cls))
				continue
			}
			r.Changed = append(r.Changed, fmt.Sprintf("oper %s: operclass %s became %s", name, cls, describeOperRights(o)))
		} else {
			o.Class = "globop"
			r.Changed = append(r.Changed, fmt.Sprintf("oper %s: operclass %q unknown, given class globop", name, cls))
		}
		o.Vhost = valueOf(e.kid("vhost"))
		title := ""
		for _, b := range config.BuiltinClasses {
			if b.Name == o.Class {
				title = b.Title
			}
		}
		for _, s := range e.all("swhois") {
			// UnrealIRCd networks often repeat the oper's rank as a swhois
			// line; BeeIRCd already shows it (313), so it would show twice.
			if title != "" && sameRank(s.Value, title) {
				continue
			}
			if s.Value != "" && len(o.Swhois) < 5 {
				o.Swhois = append(o.Swhois, irc.Truncate(s.Value, 100))
			}
		}
		if err := st.SaveOper(o); err != nil {
			return fmt.Errorf("oper %s: %w", name, err)
		}
		n++
	}
	if n > 0 {
		r.In = append(r.In, Count{"opers (from UnrealIRCd)", n})
	}
	return nil
}

func valueOf(e *confEntry) string {
	if e == nil {
		return ""
	}
	return e.Value
}

func describeOperRights(o *store.Oper) string {
	switch {
	case o.Class != "" && o.Flags != "":
		return "class " + o.Class + " + flags " + o.Flags
	case o.Class != "":
		return "class " + o.Class
	}
	return "flags " + o.Flags
}

// sameRank says whether a swhois line only repeats a class title, with or
// without its "is a"/"is an": "Network Administrator" and "is a Network
// Administrator" both match "is a Network Administrator".
func sameRank(swhois, title string) bool {
	bare := func(s string) string {
		s = strings.ToLower(strings.Join(strings.Fields(s), " "))
		for _, p := range []string{"is an ", "is a "} {
			if strings.HasPrefix(s, p) {
				return s[len(p):]
			}
		}
		return s
	}
	return bare(swhois) == bare(title)
}
