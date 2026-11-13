package config

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// OperClass is an [[operclass]] block (step 1.5.8): what an oper may do and
// how they look once opered. BeeIRCd is IRCd and services in one, so a
// class is an UnrealIRCd operclass and an Anope opertype together.
type OperClass struct {
	Name string `toml:"name"`
	// Flags: a admin, b bots, c links, g global, h hosts, k kill, s services.
	Flags string `toml:"flags"`
	// Title shows in WHOIS (313), like "is a Network Administrator".
	Title string `toml:"title"`
	// Vhost becomes the shown host at /oper (UnrealIRCd's oper vhost).
	Vhost string `toml:"vhost"`
	// Swhois are extra WHOIS lines (320), like "looks very helpful".
	Swhois []string `toml:"swhois"`
}

// OperFlagLetters are every oper flag, in order.
const OperFlagLetters = "abcghks"

// BuiltinClasses are there without any [[operclass]] block: UnrealIRCd's
// names with Anope's opertypes inside (Helper, Services Operator,
// Services Administrator, Services Root). A block with the same name
// replaces one.
var BuiltinClasses = []OperClass{
	{Name: "helper", Flags: "h", Title: "is a Network Helper"},
	{Name: "globop", Flags: "hk", Title: "is an IRC Operator"},
	{Name: "admin", Flags: "ghks", Title: "is a Server Administrator"},
	{Name: "netadmin", Flags: "abcghks", Title: "is a Network Administrator"},
}

// Builtin reports whether name is one of BuiltinClasses.
func Builtin(name string) bool {
	for _, b := range BuiltinClasses {
		if strings.EqualFold(b.Name, name) {
			return true
		}
	}
	return false
}

// Classes returns every class: the built-in ones (or their replacements)
// and the configured ones, by name.
func (c *Config) Classes() []OperClass {
	by := map[string]OperClass{}
	for _, b := range BuiltinClasses {
		by[b.Name] = b
	}
	for _, oc := range c.OperClasses {
		by[strings.ToLower(oc.Name)] = oc
	}
	out := make([]OperClass, 0, len(by))
	for _, oc := range by {
		out = append(out, oc)
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name) })
	return out
}

// Class finds a class by name (case doesn't matter).
func (c *Config) Class(name string) (OperClass, bool) {
	for _, oc := range c.Classes() {
		if strings.EqualFold(oc.Name, name) {
			return oc, true
		}
	}
	return OperClass{}, false
}

var classNameRe = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_-]{0,29}$`)

// ValidClassName reports whether name can be a class name.
func ValidClassName(name string) bool { return classNameRe.MatchString(name) }

// checkClasses reports the first thing wrong with the [[operclass]] blocks.
func (c *Config) checkClasses() error {
	seen := map[string]bool{}
	for _, oc := range c.OperClasses {
		key := strings.ToLower(oc.Name)
		switch {
		case !ValidClassName(oc.Name):
			return fmt.Errorf("operclass name %q must be a word of up to 30 letters, digits, _ or -", oc.Name)
		case seen[key]:
			return fmt.Errorf("operclass %q is there twice", oc.Name)
		case strings.Trim(oc.Flags, OperFlagLetters) != "":
			return fmt.Errorf("operclass %q flags %q: only a c g h k s", oc.Name, oc.Flags)
		case len(oc.Title) > 50 || len(oc.Vhost) > 64 || len(oc.Swhois) > 5:
			return fmt.Errorf("operclass %q: title up to 50, vhost up to 64 characters, up to 5 swhois lines", oc.Name)
		}
		seen[key] = true
	}
	return nil
}
