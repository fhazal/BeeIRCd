package server

// Channel modes. Letters follow UnrealIRCd
// 6 where it and InspIRCd 4 disagree; +E +B +J come from InspIRCd.

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/fhazal/beeircd/internal/config"
	"github.com/fhazal/beeircd/internal/irc"
	"github.com/fhazal/beeircd/internal/state"
)

// Flag modes (no argument) and who may set them. O and P are for opers,
// r for ChanServ, Z for the server itself.
const (
	halfopFlagModes = "imnt"
	opFlagModes     = "psRMcCSTNKVQzDGUu"
)

// Channel mode letters for 004 and 005, without the rank letters.
const (
	chanModesList  = "beI"
	chanModesKey   = "k"
	chanModesParam = "BEFHJLWfl"
	chanModesFlags = "CDGKMNOPQRSTUVZcimnprstuz"
)

// maxLinkHops is how many +L channels one JOIN may be sent through.
const maxLinkHops = 3

// floodBootDelay: right after a start everybody reconnects at once, so
// join floods aren't counted (UnrealIRCd boot-delay, 75 s).
const floodBootDelay = 75 * time.Second

// checkParamMode checks the argument of parameter mode m on channel name
// and returns it the way it is stored, or why it can't be used.
func (s *Server) checkParamMode(name string, m byte, arg string) (string, string) {
	switch m {
	case 'f':
		fs, err := parseFlood(arg, false)
		if err != "" {
			return "", err
		}
		return fs.String(), ""
	case 'F':
		p := strings.ToLower(arg)
		if _, ok := config.FloodProfiles[p]; !ok {
			return "", "Unknown flood profile (use " + strings.Join(config.ProfileNames(), ", ") + ")"
		}
		return p, ""
	case 'L':
		if !irc.ValidChannel(arg, s.cfg.Limits.ChannelLen) {
			return "", "Invalid channel name for +L"
		}
		if irc.EqualFold(arg, name) {
			return "", "A channel can't link to itself"
		}
		return arg, ""
	case 'E':
		a, b, ok := twoNumbers(arg)
		if !ok || a < 2 || a > 50 || b < 1 || b > 3600 {
			return "", "Use +E <lines>:<seconds>, like +E 3:60 (lines 2-50)"
		}
		return strconv.Itoa(a) + ":" + strconv.Itoa(b), ""
	case 'B':
		a, b, ok := twoNumbers(arg)
		if !ok || a < 1 || a > 512 || b < 1 || b > 100 {
			return "", "Use +B <min length>:<percent>, like +B 10:70"
		}
		return strconv.Itoa(a) + ":" + strconv.Itoa(b), ""
	case 'H':
		return s.checkHistoryMode(name, arg)
	case 'J':
		n, err := strconv.Atoi(arg)
		if err != nil || n < 1 || n > 3600 {
			return "", "Use +J <seconds> (1-3600)"
		}
		return strconv.Itoa(n), ""
	case 'W':
		n, err := strconv.Atoi(arg)
		if err != nil || n < 1 || n > 3600 {
			return "", "Use +W <seconds> (1-3600)"
		}
		return strconv.Itoa(n), ""
	}
	return "", "Unknown mode"
}

func twoNumbers(arg string) (int, int, bool) {
	x, y, found := strings.Cut(arg, ":")
	if !found {
		return 0, 0, false
	}
	a, err1 := strconv.Atoi(x)
	b, err2 := strconv.Atoi(y)
	return a, b, err1 == nil && err2 == nil
}

// msgExempt reports whether u in ch skips the content modes (+c +S +C +T
// +E +B) and the message parts of +f: halfop and up, opers with flag g,
// and services.
func (s *Server) msgExempt(c *Client, ch *state.Channel, u *state.User) bool {
	if u.Service || (c != nil && c.can('g')) {
		return true
	}
	mem := ch.Member(u)
	return mem != nil && mem.AtLeast(state.RankHalfop)
}

// channelText checks a PRIVMSG or NOTICE to ch against the content modes
// and the flood guard. It returns the text to send (+S strips colours),
// why it is refused ("" = not refused), and whether to drop it quietly
// (a flood kick already dealt with the sender).
func (s *Server) channelText(c *Client, ch *state.Channel, verb, text string) (string, string, bool) {
	u := c.user
	if s.msgExempt(c, ch, u) {
		return text, "", false
	}
	ctcp := len(text) > 1 && text[0] == '\x01'
	action := ctcp && strings.HasPrefix(text, "\x01ACTION")
	colourOK := ch.Bypasses(u, "color")
	switch {
	case verb == "NOTICE" && ch.HasMode('T') && !ch.Bypasses(u, "notice"):
		return "", "NOTICEs are not permitted in this channel", false
	case verb == "NOTICE" && ch.Stops(u, state.StopNotice):
		return "", "You may not send NOTICEs to this channel (you're banned from it)", false
	case ctcp && !action && ch.HasMode('C'):
		return "", "CTCPs are not permitted in this channel", false
	case ctcp && !action && ch.Stops(u, state.StopCTCP):
		return "", "You may not send CTCPs to this channel (you're banned from it)", false
	case ch.HasMode('c') && hasColour(text) && !colourOK:
		return "", "Color is not permitted in this channel", false
	case hasColour(text) && ch.Stops(u, state.StopColor):
		return "", "You may not send colours to this channel (you're banned from it)", false
	}
	if (ch.HasMode('S') && !colourOK) || ch.Stops(u, state.StripColor) {
		text = stripColour(text)
	}
	if ch.HasMode('G') && !ch.Bypasses(u, "censor") {
		text = censor(text, s.cfg.Censor.Words, s.cfg.CensorReplace())
	}
	out, blocked := ch.TextBan(text, s.cfg.CensorReplace())
	if blocked {
		return "", "Message blocked by a text ban (~T) of this channel", false
	}
	text = out
	if p := ch.Param('B'); p != "" {
		minLen, pct, _ := twoNumbers(p)
		if capsOver(text, minLen, pct) {
			return "", fmt.Sprintf("Your message cannot be %d%% or more capital letters if it's %d or more letters long (+B)", pct, minLen), false
		}
	}
	now := time.Now()
	fl := s.chanFlood(ch, true)
	uf := fl.user(u.UID)
	if p := ch.Param('E'); p != "" {
		lines, secs, _ := twoNumbers(p)
		if uf.repeat(&uf.eLine, &uf.eCount, &uf.eStart, text, secs, now) >= lines {
			return "", "Your message repeats what you sent before (+E)", false
		}
	}
	if ch.FloodExempt(u) {
		return text, "", false // +e ~F: the +f limits don't count them
	}
	rules := s.floodRules(ch)
	if ctcp && !action {
		s.floodCount(ch, fl, 'c', rules, now)
	}
	s.floodCount(ch, fl, 'm', rules, now)
	if r, ok := rules['t']; ok {
		if uf.text.hit(r, now) {
			s.floodKick(ch, u, r, "Flooding (limit is "+strconv.Itoa(r.limit)+" lines per "+strconv.Itoa(r.secs)+" seconds)")
			return "", "", true
		}
	}
	if r, ok := rules['r']; ok {
		if uf.repeat(&uf.rLine, &uf.rCount, &uf.rStart, text, r.secs, now) > r.limit {
			s.floodKick(ch, u, r, "Flooding (Your last message is too similar to previous ones)")
			return "", "", true
		}
	}
	return text, "", false
}

// hasColour reports mIRC colour codes (^C or the hex ^D).
func hasColour(text string) bool { return strings.ContainsAny(text, "\x03\x04") }

// stripColour removes colour codes and their numbers, like UnrealIRCd +S.
func stripColour(text string) string {
	if !hasColour(text) {
		return text
	}
	var b strings.Builder
	for i := 0; i < len(text); i++ {
		switch text[i] {
		case '\x03':
			i = skipColour(text, i+1, isDigit, 2)
		case '\x04':
			i = skipColour(text, i+1, isHex, 6)
		default:
			b.WriteByte(text[i])
		}
	}
	return b.String()
}

// skipColour skips "fg[,bg]" after a colour code at i and returns the index
// of the last byte it ate.
func skipColour(t string, i int, ok func(byte) bool, n int) int {
	j := i
	for j < len(t) && j-i < n && ok(t[j]) {
		j++
	}
	if j > i && j+1 < len(t) && t[j] == ',' && ok(t[j+1]) {
		k := j + 1
		for k < len(t) && k-(j+1) < n && ok(t[k]) {
			k++
		}
		j = k
	}
	return j - 1
}

func isDigit(b byte) bool { return b >= '0' && b <= '9' }
func isHex(b byte) bool   { return isDigit(b) || (b|0x20 >= 'a' && b|0x20 <= 'f') }

// capsOver reports whether text has at least minLen letters and pct% or
// more of them are capitals (InspIRCd anticaps).
func capsOver(text string, minLen, pct int) bool {
	letters, caps := 0, 0
	for _, r := range text {
		switch {
		case r >= 'A' && r <= 'Z':
			letters++
			caps++
		case r >= 'a' && r <= 'z':
			letters++
		}
	}
	return letters >= minLen && letters > 0 && caps*100 >= pct*letters
}

// ---- joins ----

// joinRefusal is why u can't join ch, as numeric and text, or "" when it
// may, checked in the order BeeIRCd always used. link reports whether +L
// may send u on instead (UnrealIRCd: +z, +R, bans, +i, +l and a wrong +k).
func (s *Server) joinRefusal(c *Client, ch *state.Channel, key string, uop bool) (code, text string, link bool) {
	u := c.user
	invited := ch.Invited[u.UID]
	switch {
	case ch.HasMode('O') && !c.isOper():
		return irc.ErrOperOnly, "Cannot join channel (+O)", false
	case ch.HasMode('z') && !u.TLS && !c.isOper():
		return irc.ErrSecureOnlyChan, "Cannot join channel (Secure connection is required)", true
	case ch.HasMode('i') && !invited && !uop && !ch.Matches('I', u):
		return irc.ErrInviteOnlyChan, "Cannot join channel (+i)", true
	case ch.HasMode('k') && key != ch.Key && !invited && !uop:
		return irc.ErrBadChannelKey, "Cannot join channel (+k)", true
	case ch.HasMode('l') && len(ch.Members) >= ch.Limit && !invited:
		return irc.ErrChannelIsFull, "Cannot join channel (+l)", true
	case ch.IsBanned(u) && !invited:
		return irc.ErrBannedFromChan, "Cannot join channel (+b)", true
	case ch.HasMode('R') && u.Account == "":
		return irc.ErrNeedReggedNick, "You need to identify to a registered nick to join that channel", true
	}
	if until, ok := s.rejoinBlock[rejoinKey(ch.Name, u.UID)]; ok && !invited && !uop {
		if left := time.Until(until); left > 0 {
			secs := int((left + time.Second - 1) / time.Second)
			return irc.ErrDelayRejoin, fmt.Sprintf("You must wait %d seconds after being kicked to rejoin (+J)", secs), false
		}
	}
	return "", "", false
}

// linkReason is the [Link] text for a refusal code.
func linkReason(code string) string {
	switch code {
	case irc.ErrChannelIsFull:
		return "has become full"
	case irc.ErrInviteOnlyChan:
		return "is invite only"
	case irc.ErrBannedFromChan:
		return "has you banned"
	case irc.ErrBadChannelKey:
		return "needs the right key"
	case irc.ErrNeedReggedNick:
		return "is for registered users"
	case irc.ErrSecureOnlyChan:
		return "is for secure (TLS) connections"
	}
	return "can't be joined"
}

func rejoinKey(channel, uid string) string { return irc.Fold(channel) + " " + uid }

// ---- delay join (+D) ----

// revealForModes shows hidden members who are about to get a status mode.
func (s *Server) revealForModes(e *state.ChannelMode) {
	ch := s.net.Channel(e.Channel)
	if ch == nil {
		return
	}
	for _, m := range e.Changes {
		if m.Add && state.RankForMode(m.Mode) != 0 && m.UID != "" {
			if mem := ch.Members[m.UID]; mem != nil && mem.Hidden {
				s.emit(&state.Reveal{UID: m.UID, Channel: ch.Name})
			}
		}
	}
}

// revealAll shows every hidden member (the channel lost +D).
func (s *Server) revealAll(name string) {
	ch := s.net.Channel(name)
	if ch == nil {
		return
	}
	var hidden []string
	for uid, m := range ch.Members {
		if m.Hidden {
			hidden = append(hidden, uid)
		}
	}
	for _, uid := range hidden {
		s.emit(&state.Reveal{UID: uid, Channel: ch.Name})
	}
}

// reveal shows u in ch when it is hidden there (it talks or sets a topic).
func (s *Server) reveal(u *state.User, ch *state.Channel) {
	if mem := ch.Member(u); mem != nil && mem.Hidden {
		s.emit(&state.Reveal{UID: u.UID, Channel: ch.Name})
	}
}

// seers are the members of ch who may see u there: everyone, unless +D or
// +u (auditorium) hides u.
func seers(ch *state.Channel, u *state.User) []*state.User {
	mem := ch.Member(u)
	if mem == nil {
		return members(ch)
	}
	out := make([]*state.User, 0, len(ch.Members))
	for _, m := range ch.Members {
		if ch.Sees(m.User, mem) {
			out = append(out, m.User)
		}
	}
	return out
}

// ---- auditorium (+u) ----

// auditoriumBefore runs before a mode change: someone about to be opped
// in a +u channel joins, as far as the audience can tell.
func (s *Server) auditoriumBefore(e *state.ChannelMode) {
	ch := s.net.Channel(e.Channel)
	if ch == nil || !ch.HasMode('u') {
		return
	}
	for _, m := range e.Changes {
		mem := ch.Members[m.UID]
		if !m.Add || m.Mode != 'o' || mem == nil || mem.Hidden || mem.AtLeast(state.RankOp) {
			continue
		}
		join := s.out(mem.User, "JOIN", ch.Name)
		join.extParams = []string{ch.Name, accountOrStar(mem.User), mem.User.Realname}
		for _, other := range ch.Members {
			if other != mem && !other.AtLeast(state.RankOp) {
				s.deliverTo(other.User, join)
			}
		}
	}
}

// auditoriumAfter runs after a mode change. Whoever became or stopped
// being an op gets the list of names again, because what they may see
// changed; an op who stepped down leaves, as far as the audience can
// tell. Setting or taking +u gives everyone a new list.
func (s *Server) auditoriumAfter(e *state.ChannelMode) {
	ch := s.net.Channel(e.Channel)
	if ch == nil {
		return
	}
	names := func(u *state.User) {
		if c := s.clients[u.UID]; c != nil {
			s.sendNames(c, ch)
		}
	}
	for _, m := range e.Changes {
		if m.Mode == 'u' {
			for _, mem := range ch.Members {
				if !mem.AtLeast(state.RankOp) {
					names(mem.User)
				}
			}
			return
		}
	}
	if !ch.HasMode('u') {
		return
	}
	for _, m := range e.Changes {
		mem := ch.Members[m.UID]
		if m.Mode != 'o' || mem == nil || mem.Hidden {
			continue
		}
		if !m.Add && !mem.AtLeast(state.RankOp) {
			part := s.out(mem.User, "PART", ch.Name)
			for _, other := range ch.Members {
				if other != mem && !other.AtLeast(state.RankOp) {
					s.deliverTo(other.User, part)
				}
			}
		}
		names(mem.User)
	}
}

// ---- secure channel (+Z) ----

// syncSecure sets +Z on a +z channel when everyone in it is on TLS, and
// takes it off when not (UnrealIRCd issecure).
func (s *Server) syncSecure(name string) {
	ch := s.net.Channel(name)
	if ch == nil {
		return
	}
	want := ch.HasMode('z')
	if want {
		for _, m := range ch.Members {
			if !m.User.TLS && !m.User.Service {
				want = false
				break
			}
		}
	}
	if want != ch.HasMode('Z') {
		s.emit(&state.ChannelMode{Channel: ch.Name, By: s.cfg.Server.Name, At: time.Now(),
			Changes: []state.ModeChange{{Add: want, Mode: 'Z'}}})
	}
}

// ---- flood guard (+f / +F) ----

// floodRule is one part of +f: more than limit events in secs seconds
// sets mode action (or kicks, for t and r) for mins minutes (0 = stays).
type floodRule struct {
	limit, secs int
	action      byte
	mins        int
}

// floodSet is a parsed +f value.
type floodSet struct {
	rules map[byte]floodRule
	secs  int
}

// floodTypes are the +f kinds in the order they are shown, with the
// default action and the other actions allowed (UnrealIRCd 6 floodtypes;
// 0 = kick).
var floodTypes = []struct {
	t       byte
	name    string
	def     byte
	allowed string
}{
	{'c', "CTCPflood", 'C', "CmM"},
	{'j', "joinflood", 'i', "iR"},
	{'k', "knockflood", 'K', "K"},
	{'m', "msg/noticeflood", 'm', "mM"},
	{'n', "nickflood", 'N', "N"},
	{'t', "msg/noticeflood", 0, "b"},
	{'r', "repeating", 0, "b"},
}

func floodType(t byte) int {
	for i, ft := range floodTypes {
		if ft.t == t {
			return i
		}
	}
	return -1
}

// parseFlood reads "[5j#R10,10m]:15". profile is true for the built-in +F
// profiles (they may not hold t or r, like UnrealIRCd).
func parseFlood(arg string, profile bool) (floodSet, string) {
	const usage = "Use +f [<number><type>{#<action>{<minutes>}},...]:<seconds>, like +f [10j#R10,40m]:15 (types c j k m n t r)"
	fs := floodSet{rules: map[byte]floodRule{}}
	if !strings.HasPrefix(arg, "[") {
		return fs, usage
	}
	body, secStr, found := strings.Cut(arg[1:], "]:")
	if !found {
		return fs, usage
	}
	secs, err := strconv.Atoi(secStr)
	if err != nil || secs < 1 || secs > 999 {
		return fs, "The seconds after ]: must be 1-999"
	}
	fs.secs = secs
	for _, part := range strings.Split(body, ",") {
		i := 0
		for i < len(part) && isDigit(part[i]) {
			i++
		}
		if i == 0 || i == len(part) {
			return fs, usage
		}
		limit, _ := strconv.Atoi(part[:i])
		t := part[i]
		ti := floodType(t)
		if ti < 0 || limit < 1 || limit > 999 {
			return fs, usage
		}
		if profile && (t == 't' || t == 'r') {
			return fs, "Flood profiles can't hold t or r"
		}
		r := floodRule{limit: limit, secs: secs, action: floodTypes[ti].def}
		rest := part[i+1:]
		if rest != "" {
			if rest[0] != '#' || len(rest) < 2 || strings.IndexByte(floodTypes[ti].allowed, rest[1]) < 0 {
				return fs, fmt.Sprintf("Type %c can take the actions: %s", t, floodTypes[ti].allowed)
			}
			r.action = rest[1]
			if len(rest) > 2 {
				mins, err := strconv.Atoi(rest[2:])
				if err != nil || mins < 0 || mins > 60 {
					return fs, "The minutes after an action must be 0-60"
				}
				r.mins = mins
			}
		}
		fs.rules[t] = r
	}
	if len(fs.rules) == 0 {
		return fs, usage
	}
	return fs, ""
}

// String writes fs back in a fixed order.
func (fs floodSet) String() string {
	var parts []string
	for _, ft := range floodTypes {
		r, ok := fs.rules[ft.t]
		if !ok {
			continue
		}
		p := strconv.Itoa(r.limit) + string(ft.t)
		if r.action != ft.def {
			p += "#" + string(r.action)
		}
		if r.mins > 0 {
			if r.action == ft.def {
				p += "#" + string(r.action)
			}
			p += strconv.Itoa(r.mins)
		}
		parts = append(parts, p)
	}
	return "[" + strings.Join(parts, ",") + "]:" + strconv.Itoa(fs.secs)
}

// floodSetFor parses (and caches) a +f value or a profile.
func (s *Server) floodSetFor(val string, profile bool) floodSet {
	if fs, ok := s.floodCache[val]; ok {
		return fs
	}
	fs, _ := parseFlood(val, profile)
	if len(s.floodCache) > 1000 {
		clear(s.floodCache)
	}
	s.floodCache[val] = fs
	return fs
}

// floodRules is what guards ch: its +F profile (or the server default)
// with +f on top, kind by kind.
func (s *Server) floodRules(ch *state.Channel) map[byte]floodRule {
	prof := ch.Param('F')
	if prof == "" {
		prof = s.cfg.Channels.FloodProfile
	}
	f := ch.Param('f')
	if f == "" {
		if v := config.FloodProfiles[prof]; v != "" {
			return s.floodSetFor(v, true).rules
		}
		return nil
	}
	own := s.floodSetFor(f, false).rules
	v := config.FloodProfiles[prof]
	if v == "" {
		return own
	}
	key := "merged " + f + " " + prof // the merged set is cached too
	if fs, ok := s.floodCache[key]; ok {
		return fs.rules
	}
	out := map[byte]floodRule{}
	for t, r := range s.floodSetFor(v, true).rules {
		out[t] = r
	}
	for t, r := range own {
		out[t] = r
	}
	s.floodCache[key] = floodSet{rules: out}
	return out
}

// counter counts events in a window.
type counter struct {
	n     int
	start time.Time
}

// hit counts one event and reports whether it went over r's limit (the
// count starts again then).
func (k *counter) hit(r floodRule, now time.Time) bool {
	if now.Sub(k.start) > time.Duration(r.secs)*time.Second {
		k.n, k.start = 0, now
	}
	k.n++
	if k.n > r.limit {
		k.n, k.start = 0, now
		return true
	}
	return false
}

// chanFloodState is one channel's flood counters. It only exists while
// something is being counted there.
type chanFloodState struct {
	kinds [7]counter // by floodType index
	users map[string]*userFlood
	last  time.Time
}

type userFlood struct {
	text   counter
	rLine  string
	rCount int
	rStart time.Time
	eLine  string
	eCount int
	eStart time.Time
	last   time.Time
}

func (f *chanFloodState) user(uid string) *userFlood {
	uf := f.users[uid]
	if uf == nil {
		if f.users == nil {
			f.users = make(map[string]*userFlood)
		}
		uf = &userFlood{}
		f.users[uid] = uf
	}
	uf.last = time.Now()
	return uf
}

// repeat counts how often text came in a row within secs and returns it.
func (uf *userFlood) repeat(line *string, n *int, start *time.Time, text string, secs int, now time.Time) int {
	if text != *line || now.Sub(*start) > time.Duration(secs)*time.Second {
		*line, *n, *start = text, 0, now
	}
	*n++
	return *n
}

func (s *Server) chanFlood(ch *state.Channel, make_ bool) *chanFloodState {
	key := irc.Fold(ch.Name)
	f := s.floods[key]
	if f == nil && make_ {
		f = &chanFloodState{}
		s.floods[key] = f
	}
	if f != nil {
		f.last = time.Now()
	}
	return f
}

// floodCount counts one event of kind t in ch and acts when it floods.
func (s *Server) floodCount(ch *state.Channel, f *chanFloodState, t byte, rules map[byte]floodRule, now time.Time) {
	r, ok := rules[t]
	if !ok {
		return
	}
	if f == nil {
		f = s.chanFlood(ch, true)
	}
	i := floodType(t)
	if !f.kinds[i].hit(r, now) {
		return
	}
	if ch.HasMode(r.action) {
		return
	}
	s.noticeOps(ch, fmt.Sprintf("*** Channel %s detected (limit is %d per %d seconds), setting mode +%c", floodTypes[i].name, r.limit, r.secs, r.action))
	s.emit(&state.ChannelMode{Channel: ch.Name, By: s.cfg.Server.Name, At: now,
		Changes: []state.ModeChange{{Add: true, Mode: r.action}}})
	if r.mins > 0 {
		s.floodUnsets = append(s.floodUnsets, floodUnset{ch.Name, r.action, now.Add(time.Duration(r.mins) * time.Minute)})
	}
}

// floodEvent counts a join, nick change or knock by c's user in ch.
func (s *Server) floodEvent(c *Client, ch *state.Channel, t byte) {
	if c == nil || c.user.Service || c.can('g') {
		return
	}
	if t == 'j' && time.Since(s.created) < s.floodBoot {
		return
	}
	if t == 'n' {
		if mem := ch.Member(c.user); mem != nil && mem.AtLeast(state.RankHalfop) {
			return
		}
	}
	rules := s.floodRules(ch)
	if _, ok := rules[t]; !ok {
		return
	}
	s.floodCount(ch, nil, t, rules, time.Now())
}

// floodKick kicks u from ch for a t or r flood, banning it too with #b.
func (s *Server) floodKick(ch *state.Channel, u *state.User, r floodRule, reason string) {
	now := time.Now()
	if r.action == 'b' {
		mask := "*!*@" + u.Host
		if r.mins > 0 {
			mask = "~t:" + strconv.Itoa(r.mins) + ":" + mask
		}
		s.emit(&state.ChannelMode{Channel: ch.Name, By: s.cfg.Server.Name, At: now,
			Changes: []state.ModeChange{{Add: true, Mode: 'b', Arg: mask}}})
	}
	s.emit(&state.Kick{UID: u.UID, Channel: ch.Name, Reason: reason})
}

// floodUnset is a mode the flood guard set that lifts at until.
type floodUnset struct {
	channel string
	mode    byte
	until   time.Time
}

// floodTick lifts flood modes whose time is up and forgets idle counters.
func (s *Server) floodTick(now time.Time) {
	keep := s.floodUnsets[:0]
	var due []floodUnset
	for _, f := range s.floodUnsets {
		if now.Before(f.until) {
			keep = append(keep, f)
		} else {
			due = append(due, f)
		}
	}
	s.floodUnsets = keep
	for _, f := range due {
		if ch := s.net.Channel(f.channel); ch != nil && ch.HasMode(f.mode) {
			s.emit(&state.ChannelMode{Channel: ch.Name, By: s.cfg.Server.Name, At: now,
				Changes: []state.ModeChange{{Add: false, Mode: f.mode}}})
		}
	}
	if now.Sub(s.floodSwept) < time.Minute {
		return
	}
	s.floodSwept = now
	for key, f := range s.floods {
		if now.Sub(f.last) > 20*time.Minute || s.net.Channel(key) == nil {
			delete(s.floods, key)
			continue
		}
		for uid, uf := range f.users {
			if now.Sub(uf.last) > 20*time.Minute || s.net.UserByUID(uid) == nil {
				delete(f.users, uid)
			}
		}
	}
	for k, until := range s.rejoinBlock {
		if now.After(until) {
			delete(s.rejoinBlock, k)
		}
	}
}

// noticeOps sends a server notice to the halfops and up of ch.
func (s *Server) noticeOps(ch *state.Channel, text string) {
	o := s.outFrom("", "NOTICE", "@"+ch.Name, text).text()
	for _, m := range ch.Members {
		if m.AtLeast(state.RankHalfop) {
			s.deliverTo(m.User, o)
		}
	}
}

// ---- emit hooks ----

// beforeModeEvent runs before ev is applied: reveals for +D, and which
// +z channels to look at afterwards.
func (s *Server) beforeModeEvent(ev state.Event) (secure []string) {
	switch e := ev.(type) {
	case *state.ChannelMode:
		s.revealForModes(e)
		s.auditoriumBefore(e)
		for _, m := range e.Changes {
			if m.Mode == 'z' {
				return []string{e.Channel}
			}
		}
	case *state.UserQuit:
		if u := s.net.UserByUID(e.UID); u != nil {
			for _, ch := range u.Channels {
				if ch.HasMode('z') {
					secure = append(secure, ch.Name)
				}
			}
		}
	case *state.Join:
		return []string{e.Channel}
	case *state.Part:
		return []string{e.Channel}
	case *state.Kick:
		return []string{e.Channel}
	}
	return secure
}

// afterModeEvent runs after ev was applied and delivered.
func (s *Server) afterModeEvent(ev state.Event, secure []string) {
	switch e := ev.(type) {
	case *state.ChannelMode:
		for _, m := range e.Changes {
			if m.Mode == 'D' && !m.Add {
				s.revealAll(e.Channel)
			}
		}
		s.auditoriumAfter(e)
	case *state.Kick:
		if ch := s.net.Channel(e.Channel); ch != nil {
			if n, err := strconv.Atoi(ch.Param('J')); err == nil && n > 0 {
				s.rejoinBlock[rejoinKey(ch.Name, e.UID)] = time.Now().Add(time.Duration(n) * time.Second)
			}
		}
	}
	for _, name := range secure {
		if ch := s.net.Channel(name); ch != nil && (ch.HasMode('z') || ch.HasMode('Z')) {
			s.syncSecure(name)
		}
	}
}

// ---- HELPOP CHMODES ----

// chmodesHelp is the channel mode table, like UnrealIRCd's
// /helpop chmodes. %s lines with ranks only show on ~&@%+ servers.
var chmodesHelp = []string{
	"***** Channel modes *****",
	"Status (who may set them in brackets):",
	"~q owner (~)       &a admin (~)        @o op (@)",
	"%h halfop (@)      +v voice (%)",
	"Lists:",
	" b <mask>   ban (and extbans: HELP EXTBANS)  e <mask>   ban exception",
	" I <mask>   invite exception",
	" Only ops see the e and I lists; a server may hide b from others too.",
	"With a value (ops):",
	" k <key>    key needed to join                l <n>      at most n people",
	" f [..]:<s> flood guard, like +f [10j#R10,40m#M10,5n#N15]:15",
	"            j joins, m messages, c CTCPs, n nick changes, k knocks:",
	"            sets #<mode><minutes>; t lines and r repeats per person: kick",
	"            (#b kick + ban)",
	" F <name>   flood profile: very-strict strict normal relaxed very-relaxed off",
	" L <#chan>  send people who can't get in (+l +i +k +b +R +z) to #chan",
	" E <n>:<s>  block the same line sent n times in s seconds",
	" B <len>:<pct> block lines of len+ letters with pct% capitals",
	" J <s>      no rejoin for s seconds after a kick",
	" W <s>      newcomers wait s seconds before they can talk",
	" H <n>:<t>  keep history: the last n lines, up to t old (+H 50:1d)",
	"On or off (ops; i m n t for halfops too):",
	" i invite only     m moderated        n no outside messages",
	" t topic by ops    p private          s secret",
	" R registered only M registered to talk c no colours",
	" S strip colours   C no CTCPs         T no NOTICEs",
	" N no nick changes K no KNOCK         V no INVITE",
	" Q no KICK         z TLS only         D delay join (hidden until they talk)",
	" G hide the words in [censor] of beeircd.conf (they become <censored>)",
	" U what +m or a ban stops is shown to the ops instead of refused",
	" u auditorium: only the ops are seen in the list of names, and only the ops see who joins and leaves",
	"Set by others:",
	" O opers only, P permanent (opers)    r registered (ChanServ)",
	" Z everyone is on TLS (the server, with +z)",
	"Halfops and up, and opers with flag g, skip c S G C T E B and the message",
	"parts of +f. Channels without +F use the server's flood profile.",
	"A server may show changes of some modes to the ops only.",
}

var extbansHelp = []string{
	"***** Extbans (in +b, +e, +I) *****",
	"Written ~x:value. UnrealIRCd names (~account:) and InspIRCd names",
	"(~mute:, ~nonick:...) work too and become the short form.",
	"Who (in +b, +e or +I, alone or inside the ones below):",
	" ~a:<account>   logged in to account (~a:0 = not logged in)",
	" ~c:[+%@&~]#ch  is in #ch (with at least that status)",
	" ~r:<realname>  real name    ~S:<fingerprint>  TLS client certificate",
	" ~O:<class>     oper class   ~oper:<name>      oper login",
	" ~unauthed:<mask> not logged in   ~bot:<mask>  bot (+B)",
	" ~gateway:<name> web gateway  ~server:<name>   their server",
	" ~realmask:<nick!user@host+realname>",
	" ~C:<country>   country, like NL   ~A:<number>  network (ASN)",
	"What they may not do (in +b; in +e only that is allowed again):",
	" ~q:<mask> or ~m:<mask> talk         ~j:<mask> join (+b only)",
	" ~n:<mask> change nick               ~p:<mask> show a part message",
	" ~noctcp ~nonotice ~blockcolor ~stripcolor ~nokick ~blockinvite :<mask>",
	"Special:",
	" ~t:<minutes>:<ban>  lifts itself    ~f:#ch:<mask> send them to #ch (+b)",
	" ~T:block:<text> / ~T:censor:<word>  messages matching text (+b)",
	" ~m:<type>:<mask> skip external, censor, moderated, color or notice (+e)",
	" ~F:<mask> skip the +f flood guard (+e)",
	" ~i:#ch  use #ch's bans (+b) or invite exceptions (+I)",
	"A plain ban also stops talking and nick changes; voice skips both.",
	"Examples: +b ~q:~a:0   +e ~m:moderated:~a:Bee   +b ~T:block:*http*",
}
