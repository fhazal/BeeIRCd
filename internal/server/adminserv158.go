package server

import (
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"github.com/fhazal/beeircd/internal/config"
	"github.com/fhazal/beeircd/internal/irc"
	"github.com/fhazal/beeircd/internal/store"
)

// Step 1.5.8: AdminServ CLASS, and HISTORY / DIFF / UNDO of every change
// AdminServ makes.

// record notes a change for HISTORY. Old and New are as UNDO reads them
// for kind (see undo).
func (s *Server) record(c *Client, kind, key, old, new string) {
	if _, err := s.store.AddChange(store.Change{At: time.Now(), By: c.user.Nick, Kind: kind, Key: key, Old: old, New: new}); err != nil {
		s.log.Printf("AdminServ history: %v", err)
	}
}

// operRecord is an oper entry as HISTORY keeps it (never the password).
type operRecord struct {
	Class    string   `json:"class,omitempty"`
	Flags    string   `json:"flags,omitempty"`
	Title    string   `json:"title,omitempty"`
	Vhost    string   `json:"vhost,omitempty"`
	Swhois   []string `json:"swhois,omitempty"`
	Hosts    []string `json:"hosts,omitempty"`
	Disabled string   `json:"disabled,omitempty"`
}

func encodeOper(o *store.Oper) string {
	if o == nil {
		return ""
	}
	b, _ := json.Marshal(operRecord{o.Class, o.Flags, o.Title, o.Vhost, o.Swhois, o.Hosts, o.Disabled})
	return string(b)
}

func encodeClass(oc *config.OperClass) string {
	if oc == nil {
		return ""
	}
	b, _ := json.Marshal(oc)
	return string(b)
}

// describeRecord turns a stored value into words for DIFF.
func describeRecord(kind, v string) string {
	switch {
	case v == "":
		return "(none)"
	case kind == "motd":
		return strconv.Itoa(len(strings.Split(v, "\n"))) + " line(s): " + strings.ReplaceAll(v, "\n", " / ")
	case kind == "oper":
		var r operRecord
		if json.Unmarshal([]byte(v), &r) != nil {
			return v
		}
		out := "class " + orNone(r.Class) + ", flags " + orNone(r.Flags) + ", hosts " + strings.Join(r.Hosts, " ")
		if r.Title != "" {
			out += ", title " + r.Title
		}
		if r.Vhost != "" {
			out += ", vhost " + r.Vhost
		}
		if len(r.Swhois) > 0 {
			out += ", swhois " + strings.Join(r.Swhois, " | ")
		}
		if r.Disabled != "" {
			out += ", DISABLED (" + r.Disabled + ")"
		}
		return out
	case kind == "class":
		var oc config.OperClass
		if json.Unmarshal([]byte(v), &oc) != nil {
			return v
		}
		out := "flags " + oc.Flags
		if oc.Title != "" {
			out += ", title " + oc.Title
		}
		if oc.Vhost != "" {
			out += ", vhost " + oc.Vhost
		}
		if len(oc.Swhois) > 0 {
			out += ", swhois " + strings.Join(oc.Swhois, " | ")
		}
		return out
	}
	return v
}

// validOperVhost checks a vhost for an oper class or entry (the HostServ
// rules: letters, digits, dots and dashes, at least one dot, not starting
// or ending with . or -).
func (s *Server) validOperVhost(h string) bool {
	hs := s.cfg.HostServ
	if h == "" || len(h) > hs.HostLen || strings.ContainsAny(h[:1], hs.StartOrEnd()) || strings.ContainsAny(h[len(h)-1:], hs.StartOrEnd()) {
		return false
	}
	for i := 0; i < len(h); i++ {
		if strings.IndexByte(hs.VhostChars, h[i]) < 0 {
			return false
		}
	}
	return strings.Contains(h, ".") || hs.AllowUndotted
}

// splitSwhois reads "line one | line two" (up to 5 lines of 100 chars).
func splitSwhois(text string) ([]string, bool) {
	var out []string
	for _, p := range strings.Split(text, "|") {
		if p = strings.TrimSpace(p); p != "" {
			if len(p) > 100 {
				return nil, false
			}
			out = append(out, p)
		}
	}
	return out, len(out) > 0 && len(out) <= 5
}

// --- CLASS ---

func (s *Server) classUsers(name string) []string {
	var out []string
	list, _ := s.store.Opers()
	for _, o := range list {
		if strings.EqualFold(o.Class, name) {
			out = append(out, o.Account)
		}
	}
	return out
}

// saveClass writes (oc != nil) or removes a class block, records it and
// re-applies the class to whoever is opered with it.
func (s *Server) saveClass(c *Client, name string, oc *config.OperClass, verb string) bool {
	old, _ := s.cfg.Class(name)
	var oldText string
	for _, b := range s.cfg.OperClasses {
		if strings.EqualFold(b.Name, name) {
			oldText = encodeClass(&old)
		}
	}
	if s.cfg.Path == "" {
		s.as(c, "This server wasn't started from a config file, so classes can't be saved.")
		return false
	}
	fresh, err := config.Edit(s.cfg.Path, func(d *config.Doc) error {
		if oc == nil {
			d.DelBlock("operclass", "name", name)
			return nil
		}
		kv := []config.KV{{Key: "name", Value: config.Quote(oc.Name)}, {Key: "flags", Value: config.Quote(oc.Flags)}}
		if oc.Title != "" {
			kv = append(kv, config.KV{Key: "title", Value: config.Quote(oc.Title)})
		}
		if oc.Vhost != "" {
			kv = append(kv, config.KV{Key: "vhost", Value: config.Quote(oc.Vhost)})
		}
		if len(oc.Swhois) > 0 {
			kv = append(kv, config.KV{Key: "swhois", Value: config.QuoteList(oc.Swhois)})
		}
		d.SetBlock("operclass", kv)
		return nil
	})
	if err != nil {
		s.log.Printf("AdminServ: %v", err)
		s.as(c, "Not saved: %v", err)
		return false
	}
	s.cfg.OperClasses = fresh.OperClasses
	s.record(c, "class", strings.ToLower(name), oldText, encodeClass(oc))
	s.audit(c, s.adminServ, "CLASS %s %s", name, verb)
	s.svc.LogAdmin(c.uid, s.adminServ, "to "+verb+" oper class "+name)
	for oc2 := range s.opers {
		if strings.EqualFold(oc2.operClass, name) {
			s.recheckOpers(oc2.operAccount)
		}
	}
	s.recheckRemoteOpers(0, name)
	return true
}

func asClass(s *Server, c *Client, args []string) {
	if len(args) == 0 || strings.EqualFold(args[0], "LIST") {
		s.as(c, "  %-12s %-7s %-28s %s", "Class", "Flags", "Title", "Vhost")
		for _, oc := range s.cfg.Classes() {
			name := oc.Name
			if config.Builtin(oc.Name) {
				name += "*"
			}
			s.as(c, "  %-12s %-7s %-28s %s", name, oc.Flags, orNone(oc.Title), orNone(oc.Vhost))
		}
		n := len(s.cfg.Classes())
		word := "classes"
		if n == 1 {
			word = "class"
		}
		s.as(c, "%d %s (* = built in; SET changes it, DEL puts it back).", n, word)
		return
	}
	name, sub := args[0], strings.ToUpper(argAt(args, 1))
	if !config.ValidClassName(name) {
		s.as(c, "A class name is a word of up to 30 letters, digits, _ or -.")
		return
	}
	cur, exists := s.cfg.Class(name)
	rest := args[min(2, len(args)):]
	switch sub {
	case "ADD", "SET":
		if sub == "ADD" && exists {
			s.as(c, "Class %s exists. Change it: /as class %s set flags|title|vhost|swhois <value>", cur.Name, cur.Name)
			return
		}
		if sub == "SET" && !exists {
			s.as(c, "There is no class %s. See them: /as class list", name)
			return
		}
		oc := cur
		if !exists {
			oc = config.OperClass{Name: name}
		}
		what := strings.ToUpper(argAt(rest, 0))
		val := strings.Join(rest[min(1, len(rest)):], " ")
		if sub == "ADD" && what != "FLAGS" {
			s.as(c, "Syntax: CLASS <name> ADD FLAGS <letters>")
			return
		}
		switch what {
		case "FLAGS":
			flags, bad := cleanFlags(val)
			if bad != 0 || flags == "" {
				s.as(c, "Flags: a admin, c links, g global, h hosts, k kill, s services.")
				return
			}
			for i := 0; i < len(flags); i++ {
				if !c.can(flags[i]) {
					s.as(c, "You can't give flag %c: you don't have it yourself.", flags[i])
					return
				}
			}
			oc.Flags = flags
		case "TITLE":
			if strings.EqualFold(val, "OFF") {
				val = ""
			}
			oc.Title = irc.Truncate(val, 50)
		case "VHOST":
			if strings.EqualFold(val, "OFF") {
				val = ""
			} else if !s.validOperVhost(val) {
				s.as(c, "A vhost is a hostname like staff.example.org.")
				return
			}
			oc.Vhost = val
		case "SWHOIS":
			if strings.EqualFold(val, "OFF") {
				oc.Swhois = nil
			} else if lines, ok := splitSwhois(val); ok {
				oc.Swhois = lines
			} else {
				s.as(c, "Up to 5 swhois lines of 100 characters, split with |.")
				return
			}
		default:
			s.as(c, "Syntax: CLASS <name> SET FLAGS|TITLE|VHOST|SWHOIS <value>")
			return
		}
		if s.saveClass(c, oc.Name, &oc, strings.ToLower(sub)) {
			s.as(c, "Class %s: flags %s, title %s, vhost %s.", oc.Name, oc.Flags, orNone(oc.Title), orNone(oc.Vhost))
		}
	case "DEL":
		if !exists {
			s.as(c, "There is no class %s.", name)
			return
		}
		inBlock := false
		for _, b := range s.cfg.OperClasses {
			if strings.EqualFold(b.Name, name) {
				inBlock = true
			}
		}
		if !inBlock {
			s.as(c, "%s is built in and not changed; there is nothing to delete.", cur.Name)
			return
		}
		if users := s.classUsers(name); len(users) > 0 && !config.Builtin(name) {
			s.as(c, "%s is in use by %s. Move them to another class first.", cur.Name, strings.Join(users, ", "))
			return
		}
		if s.saveClass(c, cur.Name, nil, "delete") {
			if config.Builtin(name) {
				s.as(c, "Class %s is back to the built-in one.", cur.Name)
			} else {
				s.as(c, "Class %s is deleted.", cur.Name)
			}
		}
	default:
		s.as(c, "Syntax: CLASS LIST | CLASS <name> ADD|SET|DEL ...")
	}
}

// --- HISTORY / DIFF / UNDO ---

func asHistory(s *Server, c *Client, args []string) {
	n := 20
	if v, err := strconv.Atoi(argAt(args, 0)); err == nil && v > 0 {
		n = min(v, store.HistoryKeep)
	}
	list, err := s.store.Changes(n)
	if err != nil {
		s.dbErr(c, s.adminServ, err)
		return
	}
	if len(list) == 0 {
		s.as(c, "No changes yet.")
		return
	}
	for _, ch := range list {
		s.as(c, "  #%-4d %s  %-10s %-6s %s", ch.ID, ch.At.UTC().Format("2 Jan 15:04"), ch.By, strings.ToUpper(ch.Kind), ch.Key)
	}
	s.as(c, "%s. Details: /as diff <number>  Undo: /as undo <number>", plural(len(list), "change"))
}

func (s *Server) changeArg(c *Client, args []string, cmd string) (store.Change, bool) {
	id, err := strconv.ParseInt(strings.TrimPrefix(argAt(args, 0), "#"), 10, 64)
	if err != nil {
		s.as(c, "Syntax: %s <number> (see /as history)", cmd)
		return store.Change{}, false
	}
	ch, ok, err := s.store.ChangeByID(id)
	if err != nil {
		s.dbErr(c, s.adminServ, err)
		return store.Change{}, false
	}
	if !ok {
		s.as(c, "There is no change #%d (the last %d are kept).", id, store.HistoryKeep)
	}
	return ch, ok
}

func asDiff(s *Server, c *Client, args []string) {
	ch, ok := s.changeArg(c, args, "DIFF")
	if !ok {
		return
	}
	s.as(c, "Change #%d by %s, %s: %s %s", ch.ID, ch.By, ch.At.UTC().Format("2 Jan 2006 15:04 UTC"), strings.ToUpper(ch.Kind), ch.Key)
	s.as(c, "  before: %s", describeRecord(ch.Kind, ch.Old))
	s.as(c, "  after:  %s", describeRecord(ch.Kind, ch.New))
}

func asUndo(s *Server, c *Client, args []string) {
	ch, ok := s.changeArg(c, args, "UNDO")
	if !ok {
		return
	}
	switch ch.Kind {
	case "set":
		s.applySetting(c, s.adminServ, ch.Key, ch.Old)
	case "motd":
		var lines []string
		if ch.Old != "" {
			lines = strings.Split(ch.Old, "\n")
		}
		now := strings.Join(s.cfg.MOTDLines(), "\n")
		if err := s.writeMotd(lines); err != nil {
			s.as(c, "Not saved: %v", err)
			return
		}
		s.record(c, "motd", "motd", now, ch.Old)
		s.audit(c, s.adminServ, "UNDO #%d (MOTD)", ch.ID)
		s.as(c, "The MOTD is back to how it was before #%d.", ch.ID)
	case "class":
		if ch.Old == "" {
			if s.saveClass(c, ch.Key, nil, "undo") {
				s.as(c, "Class %s is gone again (undo #%d).", ch.Key, ch.ID)
			}
			return
		}
		var oc config.OperClass
		if json.Unmarshal([]byte(ch.Old), &oc) != nil {
			s.as(c, "Change #%d can't be read.", ch.ID)
			return
		}
		if s.saveClass(c, oc.Name, &oc, "undo") {
			s.as(c, "Class %s is back to how it was before #%d.", oc.Name, ch.ID)
		}
	case "oper":
		if ch.Old == "password" || ch.New == "password" {
			s.as(c, "Password changes can't be undone. Set a new one: /as oper %s set password <password>", ch.Key)
			return
		}
		a, err := s.store.AccountByNick(ch.Key)
		if err != nil || a == nil {
			s.as(c, "%s isn't a registered nick any more.", ch.Key)
			return
		}
		cur, _ := s.store.OperByAccount(a.ID)
		if ch.Old == "" {
			if cur != nil {
				if _, err := s.store.DeleteOper(a.ID); err != nil {
					s.dbErr(c, s.adminServ, err)
					return
				}
			}
		} else {
			var r operRecord
			if json.Unmarshal([]byte(ch.Old), &r) != nil {
				s.as(c, "Change #%d can't be read.", ch.ID)
				return
			}
			o := &store.Oper{AccountID: a.ID, Account: a.Name, Class: r.Class, Flags: r.Flags, Title: r.Title, Vhost: r.Vhost,
				Swhois: r.Swhois, Hosts: r.Hosts, Disabled: r.Disabled, AddedBy: c.user.Nick, AddedAt: time.Now()}
			if cur != nil {
				o.PassHash, o.DisabledBy = cur.PassHash, cur.DisabledBy
			}
			if err := s.store.SaveOper(o); err != nil {
				s.dbErr(c, s.adminServ, err)
				return
			}
		}
		after, _ := s.store.OperByAccount(a.ID)
		s.record(c, "oper", a.Name, encodeOper(cur), encodeOper(after))
		s.audit(c, s.adminServ, "UNDO #%d (OPER %s)", ch.ID, a.Name)
		s.recheckOpers(a.ID)
		s.as(c, "The oper entry of %s is back to how it was before #%d.", a.Name, ch.ID)
	case "port":
		s.as(c, "Port changes are undone with PORT ADD or PORT DEL (#%d: %s).", ch.ID, ch.Key)
	default:
		s.as(c, "Change #%d can't be undone.", ch.ID)
	}
}

// describeClass is one line about an oper's class for OPER LIST.
func describeClass(o store.Oper) string {
	if o.Class == "" {
		return "-"
	}
	return o.Class
}
