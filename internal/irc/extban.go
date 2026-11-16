package irc

import (
	"sort"
	"strconv"
	"strings"
)

// Extbans in +b, +e and +I. BeeIRCd writes them the UnrealIRCd way
// (~letter:arg, or ~name:arg for the ones only InspIRCd has) and reads
// UnrealIRCd's names (~account:) and InspIRCd's names (mute:) too: one
// extban per job, whichever spelling it came in.

// ExtKind is how an extban works.
type ExtKind uint8

const (
	// ExtMatch says who: ~a:account matches people logged in to account.
	ExtMatch ExtKind = iota
	// ExtAct says what a matching person may not do: ~q:mask may not talk.
	ExtAct
	// ExtSpecial has its own rules: ~t ~f ~T ~F ~i and the msgbypass ~m.
	ExtSpecial
)

// Extban is one kind of extban.
type Extban struct {
	Letter     byte    // BeeIRCd/UnrealIRCd letter, 0 = written by name
	Name       string  // its name in BeeIRCd (UnrealIRCd's where it has one)
	Unreal     bool    // UnrealIRCd 6 has it, under Name
	Insp       string  // InspIRCd 4's name, "" = InspIRCd has none
	InspLetter byte    // InspIRCd 4's letter
	Kind       ExtKind //
	Lists      string  // the lists it may be in
}

// Extbans is every extban BeeIRCd knows.
var Extbans = []*Extban{
	// Who
	{Letter: 'a', Name: "account", Unreal: true, Insp: "account", InspLetter: 'R', Kind: ExtMatch, Lists: "beI"},
	{Letter: 'c', Name: "channel", Unreal: true, Insp: "channel", InspLetter: 'j', Kind: ExtMatch, Lists: "beI"},
	{Letter: 'O', Name: "operclass", Unreal: true, Insp: "opertype", InspLetter: 'O', Kind: ExtMatch, Lists: "beI"},
	{Letter: 'r', Name: "realname", Unreal: true, Insp: "realname", InspLetter: 'r', Kind: ExtMatch, Lists: "beI"},
	{Letter: 'S', Name: "certfp", Unreal: true, Insp: "fingerprint", InspLetter: 'z', Kind: ExtMatch, Lists: "beI"},
	{Letter: 'C', Name: "country", Unreal: true, Insp: "country", InspLetter: 'G', Kind: ExtMatch, Lists: "beI"},
	{Letter: 'A', Name: "asn", Unreal: true, Kind: ExtMatch, Lists: "beI"},
	{Name: "unauthed", Insp: "unauthed", InspLetter: 'U', Kind: ExtMatch, Lists: "beI"},
	{Name: "bot", Insp: "bot", InspLetter: 'B', Kind: ExtMatch, Lists: "beI"},
	{Name: "gateway", Insp: "gateway", InspLetter: 'w', Kind: ExtMatch, Lists: "beI"},
	{Name: "oper", Insp: "oper", InspLetter: 'o', Kind: ExtMatch, Lists: "beI"},
	{Name: "realmask", Insp: "realmask", InspLetter: 'a', Kind: ExtMatch, Lists: "beI"},
	{Name: "server", Insp: "server", InspLetter: 's', Kind: ExtMatch, Lists: "beI"},
	// What they may not do
	{Letter: 'q', Name: "quiet", Unreal: true, Insp: "mute", InspLetter: 'm', Kind: ExtAct, Lists: "be"},
	{Letter: 'j', Name: "join", Unreal: true, Kind: ExtAct, Lists: "b"},
	{Letter: 'n', Name: "nickchange", Unreal: true, Insp: "nonick", InspLetter: 'N', Kind: ExtAct, Lists: "be"},
	{Letter: 'p', Name: "partmsg", Unreal: true, Kind: ExtAct, Lists: "be"},
	{Name: "noctcp", Insp: "noctcp", InspLetter: 'C', Kind: ExtAct, Lists: "be"},
	{Name: "nonotice", Insp: "nonotice", InspLetter: 'T', Kind: ExtAct, Lists: "be"},
	{Name: "blockcolor", Insp: "blockcolor", InspLetter: 'c', Kind: ExtAct, Lists: "be"},
	{Name: "stripcolor", Insp: "stripcolor", InspLetter: 'S', Kind: ExtAct, Lists: "be"},
	{Name: "nokick", Insp: "nokick", InspLetter: 'Q', Kind: ExtAct, Lists: "be"},
	{Name: "blockinvite", Insp: "blockinvite", InspLetter: 'A', Kind: ExtAct, Lists: "be"},
	// Their own rules
	{Letter: 't', Name: "time", Unreal: true, Kind: ExtSpecial, Lists: "beI"},
	{Letter: 'f', Name: "forward", Unreal: true, Insp: "redirect", InspLetter: 'd', Kind: ExtSpecial, Lists: "b"},
	{Letter: 'T', Name: "text", Unreal: true, Kind: ExtSpecial, Lists: "b"},
	{Letter: 'm', Name: "msgbypass", Unreal: true, Kind: ExtSpecial, Lists: "e"},
	{Letter: 'F', Name: "flood", Unreal: true, Kind: ExtSpecial, Lists: "e"},
	{Letter: 'i', Name: "inherit", Unreal: true, Kind: ExtSpecial, Lists: "bI"},
}

// Mute is BeeIRCd's own ~m:mask: the same as ~q:mask (InspIRCd's m:).
// ~m with a bypass type (~m:moderated:mask) is UnrealIRCd's msgbypass.
var Mute = extByName0("quiet")

func extByName0(name string) *Extban {
	for _, e := range Extbans {
		if e.Name == name {
			return e
		}
	}
	panic("no extban " + name)
}

// BypassTypes are what an UnrealIRCd msgbypass (+e ~m:type:mask) lets
// someone skip.
var BypassTypes = []string{"external", "censor", "moderated", "color", "notice"}

var extByLetter, extByName, extByInsp = func() (map[byte]*Extban, map[string]*Extban, map[string]*Extban) {
	l, n, i := map[byte]*Extban{}, map[string]*Extban{}, map[string]*Extban{}
	for _, e := range Extbans {
		if e.Letter != 0 {
			l[e.Letter] = e
		}
		n[e.Name] = e
		if e.Insp != "" {
			i[e.Insp] = e
			i[string(e.InspLetter)] = e
		}
	}
	n["mute"] = Mute // InspIRCd's name, typed by people used to it
	return l, n, i
}()

// ExtbanLetters is the EXTBAN ISUPPORT letter list.
var ExtbanLetters = func() string {
	var b []byte
	for _, e := range Extbans {
		if e.Letter != 0 {
			b = append(b, e.Letter)
		}
	}
	sort.Slice(b, func(i, j int) bool { return b[i] < b[j] })
	return string(b)
}()

// MaxTimedBan is the longest ~t ban, in minutes (one year).
const MaxTimedBan = 525600

func isBypass(arg string) bool {
	typ, _, ok := strings.Cut(arg, ":")
	if !ok {
		return false
	}
	for _, t := range BypassTypes {
		if strings.EqualFold(typ, t) {
			return true
		}
	}
	return false
}

// SplitExtban splits a normalised list entry into its extban and argument;
// nil for a plain nick!user@host mask. ~m comes back as Mute or as
// msgbypass, by its argument.
func SplitExtban(m string) (*Extban, string) {
	if len(m) < 3 || m[0] != '~' {
		return nil, ""
	}
	if m[2] == ':' {
		e := extByLetter[m[1]]
		if e == nil {
			return nil, ""
		}
		if e.Letter == 'm' && !isBypass(m[3:]) {
			return Mute, m[3:]
		}
		return e, m[3:]
	}
	name, arg, ok := strings.Cut(m[1:], ":")
	if !ok {
		return nil, ""
	}
	return extByName[name], arg
}

// NormalizeBan is NormalizeMask that also takes extbans; false when an
// extban is malformed or unknown.
func NormalizeBan(m string) (string, bool) {
	if m == "" || strings.ContainsAny(m, " \r\n\x00") {
		return "", false // a mask with a space would break list replies (367)
	}
	if !strings.HasPrefix(m, "~") {
		return NormalizeMask(m), true
	}
	return normExt(m[1:], 0)
}

// normExt normalises "x:arg" or "name:arg" (the part after ~). depth
// guards nesting: ~t may hold one extban, which may hold one matching
// extban.
func normExt(s string, depth int) (string, bool) {
	if depth > 2 {
		return "", false
	}
	name, arg, ok := strings.Cut(s, ":")
	if !ok || name == "" {
		return "", false
	}
	var e *Extban
	switch {
	case len(name) == 1:
		e = extByLetter[name[0]]
		if e != nil && e.Letter == 'm' && !isBypass(arg) {
			e = Mute
		}
	default:
		e = extByName[strings.ToLower(name)]
		if e == nil {
			e = extByInsp[strings.ToLower(name)]
		}
	}
	if e == nil {
		return "", false
	}
	out, ok := normArg(e, arg, depth)
	if !ok {
		return "", false
	}
	head := "~" + e.Name + ":"
	switch {
	case e == Mute && (name == "m" || strings.EqualFold(name, "mute")):
		head = "~m:" // BeeIRCd's own mute keeps its letter
	case e.Letter != 0:
		head = "~" + string(e.Letter) + ":"
	}
	return head + out, true
}

// inner normalises the mask inside an acting or special extban: a plain
// mask or a matching extban.
func inner(arg string, depth int) (string, bool) {
	if arg == "" {
		return "", false
	}
	if !strings.HasPrefix(arg, "~") {
		return NormalizeMask(arg), true
	}
	n, ok := normExt(arg[1:], depth+1)
	if !ok {
		return "", false
	}
	if e, _ := SplitExtban(n); e == nil || e.Kind != ExtMatch {
		return "", false
	}
	return n, true
}

func word(arg string) (string, bool) {
	if arg == "" {
		return "", false
	}
	out := Truncate(arg, 100)
	return out, out != "" // the cut can leave nothing (bad UTF-8)
}

func normArg(e *Extban, arg string, depth int) (string, bool) {
	if e.Kind == ExtAct {
		return inner(arg, depth)
	}
	switch e.Name {
	case "account":
		return word(arg)
	case "channel":
		ch := strings.TrimLeft(arg, "~&@%+")
		if len(arg)-len(ch) > 1 || !ValidChannel(ch, 64) {
			return "", false
		}
		return arg, true
	case "operclass", "realname", "oper", "server", "gateway", "realmask":
		return word(arg)
	case "country":
		arg = strings.ToUpper(arg)
		if arg == "" || len(arg) > 3 || strings.Trim(arg, "ABCDEFGHIJKLMNOPQRSTUVWXYZ*?") != "" {
			return "", false
		}
		return arg, true
	case "asn":
		n, err := strconv.ParseUint(strings.TrimPrefix(strings.ToUpper(arg), "AS"), 10, 32)
		if err != nil || n == 0 {
			return "", false
		}
		return strconv.FormatUint(n, 10), true
	case "certfp":
		arg = strings.ToLower(strings.ReplaceAll(arg, ":", ""))
		if arg == "" || len(arg) > 128 || strings.Trim(arg, "0123456789abcdef*?") != "" {
			return "", false
		}
		return arg, true
	case "unauthed", "bot":
		if arg == "" || strings.HasPrefix(arg, "~") {
			return "", false
		}
		return NormalizeMask(arg), true
	case "time":
		mins, rest, ok := strings.Cut(arg, ":")
		n, err := strconv.Atoi(mins)
		if !ok || err != nil || n < 1 || n > MaxTimedBan || rest == "" {
			return "", false
		}
		if !strings.HasPrefix(rest, "~") {
			return strconv.Itoa(n) + ":" + NormalizeMask(rest), true
		}
		in, ok := normExt(rest[1:], depth+1)
		if !ok || strings.HasPrefix(in, "~t:") {
			return "", false
		}
		return strconv.Itoa(n) + ":" + in, true
	case "forward":
		to, rest, ok := strings.Cut(arg, ":")
		if !ok || !ValidChannel(to, 64) {
			return "", false
		}
		in, ok := inner(rest, depth)
		return to + ":" + in, ok
	case "text":
		act, pat, ok := strings.Cut(arg, ":")
		act = strings.ToLower(act)
		if !ok || (act != "block" && act != "censor") || pat == "" {
			return "", false
		}
		p, ok := word(pat)
		return act + ":" + p, ok
	case "msgbypass":
		if !isBypass(arg) {
			return "", false
		}
		typ, rest, _ := strings.Cut(arg, ":")
		in, ok := inner(rest, depth)
		return strings.ToLower(typ) + ":" + in, ok
	case "flood":
		return inner(arg, depth)
	case "inherit":
		if !ValidChannel(arg, 64) {
			return "", false
		}
		return arg, true
	}
	return "", false
}

// ExtbanAllowed reports whether normalised mask m may be in list (b, e or
// I): ~j only bans, ~m:type:mask only lets through, and so on.
func ExtbanAllowed(list byte, m string) bool {
	e, arg := SplitExtban(m)
	if e == nil {
		return true
	}
	if e.Letter == 't' {
		_, in, _ := strings.Cut(arg, ":")
		return ExtbanAllowed(list, in)
	}
	return strings.IndexByte(e.Lists, list) >= 0
}

// TimedBan reads "~t:30:mask" as 30 minutes; false for other masks.
func TimedBan(m string) (minutes int, ok bool) {
	if !strings.HasPrefix(m, "~t:") {
		return 0, false
	}
	mins, _, found := strings.Cut(m[3:], ":")
	n, err := strconv.Atoi(mins)
	return n, found && err == nil
}

// UnrealBan writes normalised mask m the way UnrealIRCd 6 servers send
// it (NEXTBANS: ~quiet:mask). Extbans only InspIRCd has keep their name;
// UnrealIRCd drops what it doesn't know.
func UnrealBan(m string) string {
	e, arg := SplitExtban(m)
	if e == nil {
		return m
	}
	switch e.Name {
	case "time":
		n, in, _ := strings.Cut(arg, ":")
		return "~time:" + n + ":" + UnrealBan(in)
	case "forward":
		to, in, _ := strings.Cut(arg, ":")
		return "~forward:" + to + ":" + UnrealBan(in)
	case "msgbypass":
		typ, in, _ := strings.Cut(arg, ":")
		return "~msgbypass:" + typ + ":" + UnrealBan(in)
	}
	if e.Kind == ExtAct || e.Name == "flood" {
		return "~" + e.Name + ":" + UnrealBan(arg)
	}
	return "~" + e.Name + ":" + arg
}

// InspBan writes normalised mask m the way InspIRCd 4 does (mute:mask);
// false when InspIRCd has no such extban (~j, ~p, ~T...). A timed ban
// goes as its mask: our side lifts it.
func InspBan(m string) (string, bool) {
	e, arg := SplitExtban(m)
	switch {
	case e == nil:
		return m, true
	case e.Name == "time":
		_, in, _ := strings.Cut(arg, ":")
		return InspBan(in)
	case e.Name == "account" && arg == "0":
		return "unauthed:*!*@*", true
	case e.Insp == "":
		return "", false
	case e.Name == "forward":
		to, in, _ := strings.Cut(arg, ":")
		in, ok := InspBan(in)
		return "redirect:" + to + ":" + in, ok
	case e.Kind == ExtAct:
		in, ok := InspBan(arg)
		return e.Insp + ":" + in, ok
	}
	return e.Insp + ":" + arg, true
}

// FromInspBan reads a ban InspIRCd 4 sent ([!]name:value or
// [!]letter:value, or a plain mask) as BeeIRCd writes it; false for what
// BeeIRCd can't keep (inverted bans, unknown extbans).
func FromInspBan(m string) (string, bool) {
	if strings.HasPrefix(m, "!") {
		return "", false
	}
	name, arg, ok := strings.Cut(m, ":")
	if !ok || strings.ContainsAny(name, "!@") {
		return NormalizeBan(m)
	}
	e := extByInsp[name]
	if e == nil {
		if len(name) == 1 {
			return "", false
		}
		e = extByInsp[strings.ToLower(name)]
	}
	if e == nil {
		return "", false
	}
	if e.Kind == ExtAct || e.Name == "forward" {
		pre := ""
		if e.Name == "forward" {
			to, rest, ok := strings.Cut(arg, ":")
			if !ok {
				return "", false
			}
			pre, arg = to+":", rest
		}
		in, ok := FromInspBan(arg)
		if !ok {
			return "", false
		}
		arg = pre + in
	}
	return NormalizeBan("~" + e.Name + ":" + arg)
}

// serverExtbans are the extbans a network ban (AKILL, SHUN, ELINE) can
// be: what is known when someone connects, from their address alone.
const serverExtbans = "CA"

// ServerExtban reads m as an extended network ban (UnrealIRCd's GLINE
// ~country:NL), in its short form ("~C:NL"); ok is false for anything
// else, plain masks included.
func ServerExtban(m string) (string, bool) {
	if !strings.HasPrefix(m, "~") {
		return "", false
	}
	n, ok := NormalizeBan(m)
	if !ok || len(n) < 4 || n[2] != ':' || strings.IndexByte(serverExtbans, n[1]) < 0 {
		return "", false
	}
	return n, true
}

// SplitServerExtban splits an extended network ban for the TKL line:
// "~C:" and "NL", or UnrealIRCd's "~country:" and "NL".
func SplitServerExtban(m string, unreal bool) (user, host string) {
	if unreal {
		m = UnrealBan(m)
	}
	i := strings.IndexByte(m, ':')
	if i < 0 {
		return "*", m
	}
	return m[:i+1], m[i+1:]
}

// IsPlaceMask reports whether a network ban mask is a ban by place
// (~C:NL), not a user@host one like ~bot@*.example.net.
func IsPlaceMask(m string) bool {
	return strings.HasPrefix(m, "~") && !strings.Contains(m, "@")
}

// IsPlaceUser reports whether the user field of a TKL line starts an
// extended server ban (~country:, ~C:), not an ident like ~bot.
func IsPlaceUser(u string) bool {
	return strings.HasPrefix(u, "~") && strings.HasSuffix(u, ":")
}
