package link

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/fhazal/beeircd/internal/irc"
)

// inspBridge turns the UnrealIRCd dialect the server speaks into InspIRCd
// 4's protocol (1206) and back, so every part of the server that knows
// links works with InspIRCd unchanged. It keeps what InspIRCd lines need
// and ours don't carry: channel timestamps, nicks, which servers are
// behind the link, and the other side's mode letters (InspIRCd says in
// CAPAB which letter each mode has; we map by the mode's name).
//
// InspIRCd source used: src/modules/m_spanningtree (uid.cpp, fjoin.cpp,
// fmode.cpp, ftopic.cpp, metadata.cpp, addline.cpp, save.cpp, num.cpp,
// ijoin.cpp, netburst.cpp), m_sasl.cpp, m_services.cpp; Anope's
// modules/protocol/inspircd.cpp for what services send.
type inspBridge struct {
	me   Local
	peer Peer

	ours   modeTable // our channel modes, by letter
	theirs modeTable // theirs, from CAPAB CHANMODES
	ourU   modeTable
	theirU modeTable

	started bool
	synced  bool // their burst is over
	// saslAgent is the server on our side that said it takes SASL.
	saslAgent string
	chanTS    map[string]int64  // folded channel name -> TS
	nicks     map[string]string // UID -> nick
	nickTS    map[string]int64  // UID -> nick TS
	behind    map[string]bool   // SIDs on their side of the link
	names     map[string]string // SID -> server name (both sides)
	pending   *inspUID          // a UID held back for its account METADATA
	// operWait is opers whose OPERTYPE waits for MD operclass, so it can
	// carry their rank; operRank is that rank (MD bee-opertype).
	operWait map[string]bool
	operRank map[string]string
	// theirExt is the extbans they have, from CAPAB EXTBANS (name ->
	// "acting" or "matching"); nil when they sent none.
	theirExt map[string]string
}

// parseInspExtbans reads CAPAB EXTBANS: "acting:mute=m matching:account=R".
func parseInspExtbans(list string) map[string]string {
	out := map[string]string{}
	for _, w := range strings.Fields(list) {
		kind, rest, ok := strings.Cut(w, ":")
		if name, _, _ := strings.Cut(rest, "="); ok && name != "" {
			out[name] = kind
		}
	}
	return out
}

// banOut writes our mask m for their ban lists; false for what they can't
// keep. InspIRCd turns an extban it doesn't have into a useless mask, so
// those are left out. A forward ban goes as its extban when they have it
// and as banredirect's mask#channel when not.
func (b *inspBridge) banOut(m string) (string, bool) {
	out, ok := irc.InspBan(m)
	if !ok || b.theirExt == nil {
		return out, ok
	}
	rest := out
	for {
		name, arg, found := strings.Cut(rest, ":")
		if !found || name == "" || strings.Trim(name, "abcdefghijklmnopqrstuvwxyz") != "" {
			return out, true
		}
		kind, have := b.theirExt[name]
		switch {
		case have && kind == "acting" && name == "redirect":
			_, rest, _ = strings.Cut(arg, ":")
		case have && kind == "acting":
			rest = arg
		case have:
			return out, true
		case name == "redirect" && rest == out:
			to, in, _ := strings.Cut(arg, ":")
			if to == "" || in == "" || strings.Contains(in, ":") {
				return "", false
			}
			return in + to, true
		default:
			return "", false
		}
	}
}

// inspUID is a user from their burst, kept until its METADATA lines
// (account) have come.
type inspUID struct {
	src   string
	p     []string
	acc   string
	after []string // lines for the user, sent after its UID
}

// Mode kinds, as InspIRCd's CAPAB CHANMODES names them.
const (
	kindFlag   = 'F' // simple
	kindList   = 'L' // list
	kindParam  = 'P' // param: an argument both ways
	kindSet    = 'S' // param-set: an argument when set
	kindRank   = 'R' // prefix
	kindIgnore = 0
)

// modeTable is one side's modes: letter -> name and kind, and for ranks
// the prefix character.
type modeTable struct {
	name   map[byte]string
	letter map[string]byte
	kind   map[byte]byte
	prefix map[byte]byte // rank letter -> prefix character
}

func newModeTable() modeTable {
	return modeTable{name: map[byte]string{}, letter: map[string]byte{}, kind: map[byte]byte{}, prefix: map[byte]byte{}}
}

func (t modeTable) add(l byte, name string, kind byte) {
	t.name[l], t.letter[name], t.kind[l] = name, l, kind
}

// rankByPrefix finds the rank letter with prefix character p.
func (t modeTable) rankByPrefix(p byte) byte {
	for l, c := range t.prefix {
		if c == p {
			return l
		}
	}
	return 0
}

// Our mode letters and the names InspIRCd 4's modules give the same
// modes. Ours with no InspIRCd twin (or a different value format: f F E B
// V Z, user q z) stay on our side.
var (
	inspChanNames = map[byte]string{
		'b': "ban", 'e': "banexception", 'I': "invex", 'k': "key", 'l': "limit",
		'L': "redirect", 'J': "kicknorejoin", 'H': "history", 'W': "delaymsg",
		'U': "opmoderated", 'u': "auditorium",
		'i': "inviteonly", 'm': "moderated", 'n': "noextmsg", 't': "topiclock",
		'p': "private", 's': "secret", 'R': "reginvite", 'M': "regmoderated",
		'c': "blockcolor", 'S': "stripcolor", 'C': "noctcp", 'T': "nonotice",
		'N': "nonick", 'K': "noknock", 'Q': "nokick", 'z': "sslonly",
		'D': "delayjoin", 'G': "censor", 'O': "operonly", 'P': "permanent",
		'r': "c_registered",
	}
	inspUserNames = map[byte]string{
		'i': "invisible", 'o': "oper", 'w': "wallops", 's': "snomask", 'x': "cloak",
		'r': "u_registered", 'B': "bot", 'd': "deaf", 'D': "privdeaf", 'R': "regdeaf",
		'T': "u_noctcp", 'H': "hideoper", 'W': "showwhois", 'Z': "sslqueryonly",
		'p': "hidechans", 'g': "callerid", 'G': "u_censor",
	}
	// inspRankNames are the usual names; ranks map by prefix character.
	inspRankNames = map[byte]string{'q': "founder", 'a': "admin", 'o': "op", 'h': "halfop", 'v': "voice"}
)

// ourModes builds our table from the ISUPPORT-style groups and PREFIX.
func ourModes(chanModes, prefix string) modeTable {
	t := newModeTable()
	groups := strings.Split(chanModes, ",")
	kinds := []byte{kindList, kindParam, kindSet, kindFlag}
	for i, g := range groups {
		if i >= len(kinds) {
			break
		}
		for j := 0; j < len(g); j++ {
			if n := inspChanNames[g[j]]; n != "" {
				t.add(g[j], n, kinds[i])
			} else {
				t.kind[g[j]] = kinds[i] // known, so arguments parse
			}
		}
	}
	letters, chars := prefixLetters(prefix), prefixChars(prefix)
	for i := 0; i < len(letters) && i < len(chars); i++ {
		t.add(letters[i], inspRankNames[letters[i]], kindRank)
		t.prefix[letters[i]] = chars[i]
	}
	return t
}

// ourUserModes: every letter of ours that InspIRCd has too. Only s
// (snomask) takes an argument, and only when set.
func ourUserModes(letters string) modeTable {
	t := newModeTable()
	for i := 0; i < len(letters); i++ {
		l := letters[i]
		k := byte(kindFlag)
		if l == 's' {
			k = kindSet
		}
		if n := inspUserNames[l]; n != "" {
			t.add(l, n, k)
		} else {
			t.kind[l] = k
		}
	}
	return t
}

// parseInspModes reads CAPAB CHANMODES / USERMODES:
// "list:ban=b param-set:limit=l prefix:30000:op=@o simple:noextmsg=n".
func parseInspModes(list string) modeTable {
	t := newModeTable()
	for _, tok := range strings.Fields(list) {
		spec, value, ok := strings.Cut(tok, "=")
		if !ok || value == "" {
			continue
		}
		parts := strings.Split(spec, ":")
		kind, name := parts[0], parts[len(parts)-1]
		l := value[len(value)-1]
		switch kind {
		case "simple":
			t.add(l, name, kindFlag)
		case "list":
			t.add(l, name, kindList)
		case "param":
			t.add(l, name, kindParam)
		case "param-set":
			t.add(l, name, kindSet)
		case "prefix":
			t.add(l, name, kindRank)
			if len(value) > 1 {
				t.prefix[l] = value[0]
			}
		}
	}
	return t
}

// inspDefaultChanModes is what InspIRCd 4 has without extra modules, for
// a server that sends no CAPAB CHANMODES.
const inspDefaultChanModes = "list:ban=b param:key=k param-set:limit=l prefix:30000:op=@o prefix:10000:voice=+v " +
	"simple:inviteonly=i simple:moderated=m simple:noextmsg=n simple:private=p simple:secret=s simple:topiclock=t"

const inspDefaultUserModes = "simple:invisible=i simple:oper=o param-set:snomask=s simple:wallops=w"

func newInspBridge(me Local, peer Peer, chanModes, userModes string) *inspBridge {
	if chanModes == "" {
		chanModes = inspDefaultChanModes
	}
	if userModes == "" {
		userModes = inspDefaultUserModes
	}
	b := &inspBridge{
		me: me, peer: peer,
		ours: ourModes(me.ChanModes, me.Prefix), theirs: parseInspModes(chanModes),
		ourU: ourUserModes(me.UserModes), theirU: parseInspModes(userModes),
		chanTS: map[string]int64{}, nicks: map[string]string{}, nickTS: map[string]int64{},
		operWait: map[string]bool{}, operRank: map[string]string{},
		behind: map[string]bool{peer.SID: true}, names: map[string]string{peer.SID: peer.Name, me.SID: me.Name},
	}
	return b
}

func (b *inspBridge) fold(ch string) string { return irc.Fold(ch) }

// line builds a wire line; text marks the last parameter as free text.
func line(tags map[string]string, src, cmd string, text bool, params ...string) string {
	return irc.Message{Tags: tags, Source: src, Command: cmd, Params: params, Trailing: text}.String()
}

func (b *inspBridge) now() int64 { return b.me.Now() }

// sourceSID is the server a source is on: a SID, or a UID's first three.
func sourceSID(src string) string {
	if len(src) >= 3 {
		return src[:3]
	}
	return src
}

// ---- our lines out ----

func (b *inspBridge) Out(raw string) []string {
	m, err := irc.Parse(raw)
	if err != nil {
		return nil
	}
	var out []string
	if !b.started {
		b.started = true
		ver := "BeeIRCd-" + b.me.Version
		out = append(out,
			line(nil, b.me.SID, "BURST", false, strconv.FormatInt(b.now(), 10)),
			line(nil, b.me.SID, "SINFO", true, "rawversion", ver),
			line(nil, b.me.SID, "SINFO", true, "rawbranch", ver),
			line(nil, b.me.SID, "SINFO", true, "customversion", b.me.Network))
	}
	return sound(append(out, b.out(m)...))
}

// badSource are bytes no source may hold.
const badSource = "\r\n\x00 "

// sound keeps the lines that stay one valid line once framed: a broken
// line in (from a bad peer, or fuzzing) is dropped, not passed on.
func sound(lines []string) []string {
	out := lines[:0]
	for _, l := range lines {
		if m, err := irc.Parse(irc.CleanFraming(l)); err == nil && m.Command != "" && !strings.ContainsAny(m.Source, badSource) {
			out = append(out, l)
		}
	}
	return out
}

func (b *inspBridge) out(m irc.Message) []string {
	p := m.Params
	arg := func(i int) string {
		if i < len(p) {
			return p[i]
		}
		return ""
	}
	last := func() string {
		if len(p) == 0 {
			return ""
		}
		return p[len(p)-1]
	}
	src := m.Source
	if src == "" {
		src = b.me.SID
	}
	switch m.Command {
	case "SID": // :up SID name hops sid :desc
		if len(p) < 4 {
			return nil
		}
		b.names[p[2]] = p[0]
		// InspIRCd 4 starts the burst timer of a server behind a link only
		// from SERVER's burst=<ms> property (a BURST line is dropped once
		// connected), else ENDBURST (EOS) reports "burst time: <epoch> secs".
		return []string{line(nil, src, "SERVER", true, p[0], p[2], "burst="+strconv.FormatInt(b.now()*1000, 10), last())}
	case "EOS":
		return []string{line(nil, src, "ENDBURST", false)}
	case "NETINFO", "PROTOCTL", "PASS", "BEESESS", "BEEOPER", "BEEOPERR", "BEEADMIN", "BEEPLACE", "REPUTATION",
		"SVSNOOP", "VERSION", "BATCH", "SMOD", "SINFO":
		// VERSION: InspIRCd 4 answers it only for its own users (no
		// remote form), so our side answers instead.
		return nil
	case "MOTD", "ADMIN", "TIME": // :uid CMD :server -> :uid CMD <server name>
		// InspIRCd 4 routes these only by a server name (one with a dot),
		// and the server named checks it is its own name.
		if len(p) == 0 {
			return nil
		}
		name := b.theirServer(last())
		if name == "" {
			return nil
		}
		return []string{line(nil, src, m.Command, false, name)}
	case "REHASH": // :uid REHASH :server [-flags] -> :uid REHASH <server name>
		// InspIRCd 4 passes REHASH <mask> (a name has a dot) to every
		// server; each one it matches rehashes.
		if len(p) == 0 {
			return nil
		}
		name := b.theirServer(p[0])
		if name == "" {
			return nil
		}
		return []string{line(nil, src, "REHASH", false, name)}
	case "CONNECT": // :uid CONNECT target port :server -> :uid RCONNECT <server name> <target>
		// InspIRCd 4 has no port here: the server uses its own link block.
		if len(p) < 3 {
			return nil
		}
		name := b.theirServer(last())
		if name == "" {
			return nil
		}
		return []string{line(nil, src, "RCONNECT", false, name, p[0])}
	case "STATS": // :uid STATS letter :server -> :uid STATS letter <server name>
		if len(p) < 2 || p[0] == "" {
			return nil
		}
		name := b.theirServer(last())
		if name == "" {
			return nil
		}
		return []string{line(nil, src, "STATS", false, p[0][:1], name)}
	case "WHOIS": // :uid WHOIS server :nick -> :uid IDLE <their uid>
		// InspIRCd 4 never sends WHOIS between servers: the asker's server
		// asks the user's own server for the idle time (IDLE) and builds
		// the WHOIS itself from the answer.
		if len(p) < 2 {
			return nil
		}
		uid := b.theirUID(strings.SplitN(last(), ",", 2)[0])
		if uid == "" {
			return nil
		}
		return []string{line(nil, src, "IDLE", false, uid)}
	case "UID":
		return b.outUID(src, p)
	case "AWAY":
		if last() == "" {
			return []string{line(nil, src, "AWAY", false)}
		}
		return []string{line(nil, src, "AWAY", true, strconv.FormatInt(b.now(), 10), last())}
	case "SJOIN":
		return b.outSJoin(src, p)
	case "MODE", "SVSMODE", "SVS2MODE":
		if len(p) < 2 {
			return nil
		}
		if irc.IsChannel(p[0]) {
			return b.outChanMode(src, p[0], p[1], p[2:])
		}
		return b.outUserMode(src, p[0], p[1])
	case "UMODE2":
		if len(p) < 1 {
			return nil
		}
		return b.outUserMode(src, src, p[0])
	case "NICK": // :uid NICK new ts
		if len(p) < 1 {
			return nil
		}
		b.nicks[src] = p[0]
		ts := b.validTS(arg(1))
		b.nickTS[src], _ = strconv.ParseInt(ts, 10, 64)
		return []string{line(nil, src, "NICK", false, p[0], ts)}
	case "QUIT":
		delete(b.nicks, src)
		delete(b.nickTS, src)
		b.forgetOper(src)
		return []string{line(nil, src, "QUIT", true, last())}
	case "KILL":
		if len(p) < 1 {
			return nil
		}
		b.gone(p[0])
		return []string{line(nil, src, "KILL", true, p[0], last())}
	case "SVSKILL": // InspIRCd has no SVSKILL: a KILL from services
		if len(p) < 1 {
			return nil
		}
		b.gone(p[0])
		return []string{line(nil, src, "KILL", true, p[0], last())}
	case "PART", "KICK", "PRIVMSG", "NOTICE", "TAGMSG", "WALLOPS", "SVSJOIN", "SVSPART", "ERROR", "SQUIT":
		if m.Command == "SQUIT" && len(p) > 0 {
			delete(b.behind, b.sidOf(p[0]))
		}
		m.Source = src
		return []string{m.String()}
	case "INVITE": // :src INVITE uid #chan 0
		if len(p) < 2 {
			return nil
		}
		ts, ok := b.chanTS[b.fold(p[1])]
		if !ok {
			return nil
		}
		return []string{line(nil, src, "INVITE", false, p[0], p[1], strconv.FormatInt(ts, 10))}
	case "TOPIC": // :src TOPIC #chan setter ts :text
		if len(p) < 4 {
			return nil
		}
		cts, ok := b.chanTS[b.fold(p[0])]
		if !ok {
			return nil
		}
		return []string{line(nil, src, "FTOPIC", true, p[0], strconv.FormatInt(cts, 10), p[2], p[1], last())}
	case "SVSNICK": // :src SVSNICK uid new ts
		if len(p) < 2 {
			return nil
		}
		if p[0] == p[1] {
			// A collision loser renamed to its UID: InspIRCd's SAVE,
			// with the nick time it has for the user.
			ts, ok := b.nickTS[p[0]]
			if !ok {
				ts, _ = strconv.ParseInt(arg(2), 10, 64)
			}
			return []string{line(nil, b.me.SID, "SAVE", false, p[0], strconv.FormatInt(ts, 10))}
		}
		ts := arg(2)
		if ts == "" {
			ts = strconv.FormatInt(b.now(), 10)
		}
		return []string{line(nil, src, "SVSNICK", false, p[0], p[1], ts)}
	case "SVSLOGIN": // :src SVSLOGIN server uid account
		if len(p) < 3 {
			return nil
		}
		acc := p[2]
		if acc == "0" || acc == "*" {
			acc = ""
		}
		return []string{line(nil, src, "METADATA", true, p[1], "accountname", acc)}
	case "SETHOST", "CHGHOST":
		return b.outChange(src, m.Command, p, "FHOST", "CHGHOST")
	case "SETIDENT", "CHGIDENT":
		return b.outChange(src, m.Command, p, "FIDENT", "CHGIDENT")
	case "SETNAME", "CHGNAME":
		return b.outChange(src, m.Command, p, "FNAME", "CHGNAME")
	case "TKL":
		return b.outTKL(src, p)
	case "SAJOIN", "SAPART", "SANICK", "ACCEPT":
		// InspIRCd has the same commands, sent to the person's own server.
		m.Source = src
		return []string{m.String()}
	case "TSCTL": // :uid TSCTL alltime -> InspIRCd's ALLTIME
		if strings.EqualFold(arg(0), "alltime") {
			return []string{line(nil, src, "ALLTIME", false)}
		}
		return nil
	case "PING":
		return []string{line(nil, b.me.SID, "PING", false, b.peer.SID)}
	case "PONG": // :us PONG ourname :theirsid
		return []string{line(nil, b.me.SID, "PONG", false, last())}
	case "MD": // :sid MD client <name|uid> key [:value]
		if len(p) >= 4 && p[0] == "client" && p[2] == "saslmechlist" {
			b.saslAgent = sourceSID(src)
			return []string{line(nil, src, "METADATA", true, "*", "saslmechlist", last())}
		}
		if len(p) >= 3 && p[0] == "client" {
			return b.outOperMD(p[1], p[2], arg(3))
		}
		return nil
	case "SASL": // :src SASL target uid mode data...
		return b.outSASL(src, p)
	case "SWHOIS": // :src SWHOIS uid +|- tag prio :text -> METADATA uid swhois [:text]
		// InspIRCd keeps one extra WHOIS line; a delete clears it.
		if len(p) < 5 || len(p[0]) != 9 {
			return nil
		}
		if p[1] == "+" && last() != "" {
			return []string{line(nil, sourceSID(src), "METADATA", true, p[0], "swhois", last())}
		}
		return []string{line(nil, sourceSID(src), "METADATA", false, p[0], "swhois")}
	case "GLOBOPS": // :uid GLOBOPS :text -> :sid SNONOTICE G :From nick: text
		if len(p) == 0 || last() == "" {
			return nil
		}
		text := last()
		if n := b.nicks[src]; n != "" {
			text = "From " + n + ": " + text
		}
		return []string{line(nil, sourceSID(src), "SNONOTICE", true, "G", text)}
	case "KNOCK": // :uid KNOCK #chan :reason -> :uid ENCAP * KNOCK #chan :reason
		// InspIRCd 4's m_knock is an optional broadcast command, so it
		// goes as ENCAP * (a server without m_knock ignores it).
		if len(p) < 2 || len(src) != 9 {
			return nil
		}
		return []string{line(nil, src, "ENCAP", true, "*", "KNOCK", p[0], last())}
	case "SDESC": // :uid SDESC :text -> :sid SINFO desc :text
		// InspIRCd 4 keeps a server's description as its SINFO desc, sent
		// by that server (here: the server of the oper who changed it).
		sid := sourceSID(src)
		if last() == "" || b.behind[sid] {
			return nil
		}
		return []string{line(nil, sid, "SINFO", true, "desc", last())}
	case "UNINVITE": // :src UNINVITE <uid> #chan -> :src ENCAP <sid> UNINVITE <uid> #chan
		// InspIRCd 4's m_uninvite goes to the target's server as ENCAP.
		if len(p) < 2 || len(p[0]) != 9 || !b.behind[sourceSID(p[0])] {
			return nil
		}
		return []string{line(nil, src, "ENCAP", false, sourceSID(p[0]), "UNINVITE", p[0], p[1])}
	}
	if len(m.Command) == 3 && m.Command[0] >= '0' && m.Command[0] <= '9' && len(p) > 0 {
		// :src 401 uid params -> NUM src uid 401 params
		ps := append([]string{sourceSID(src), p[0], m.Command}, p[1:]...)
		return []string{line(nil, sourceSID(src), "NUM", true, ps...)}
	}
	return nil
}

// validTS is a timestamp InspIRCd takes: it refuses 0 (our services bots
// have nick time 0), so those get our start time.
func (b *inspBridge) validTS(ts string) string {
	if t, err := strconv.ParseInt(ts, 10, 64); err == nil && t > 0 {
		return ts
	}
	if b.me.Booted > 0 {
		return strconv.FormatInt(b.me.Booted, 10)
	}
	return strconv.FormatInt(b.now(), 10)
}

// gone forgets a user that left.
func (b *inspBridge) gone(uid string) {
	delete(b.nicks, uid)
	delete(b.nickTS, uid)
	b.forgetOper(uid)
}

// sidOf is a server's SID by name or SID.
func (b *inspBridge) sidOf(x string) string {
	for sid, n := range b.names {
		if strings.EqualFold(n, x) || sid == x {
			return sid
		}
	}
	return x
}

// outUID: nick hops ts ident realhost uid account umodes vhost cloak ip :real
// -> UID uid ts nick realhost dhost ident ident ip signon +modes :real
func (b *inspBridge) outUID(src string, p []string) []string {
	if len(p) < 12 {
		return nil
	}
	nick, ts, ident, realhost, uid, account, modes, vhost, cloak := p[0], b.validTS(p[2]), p[3], p[4], p[5], p[6], strings.TrimPrefix(p[7], "+"), p[8], p[9]
	dhost := realhost
	if strings.IndexByte(modes, 'x') >= 0 {
		switch {
		case vhost != "*" && vhost != "":
			dhost = vhost
		case cloak != "*" && cloak != "":
			dhost = cloak
		}
	}
	ip := decodeIP(p[10])
	if ip == "" {
		ip = "0.0.0.0"
	}
	b.nicks[uid] = nick
	b.nickTS[uid], _ = strconv.ParseInt(ts, 10, 64)
	oper := strings.IndexByte(modes, 'o') >= 0
	var um []byte
	for i := 0; i < len(modes); i++ {
		if modes[i] == 'o' || modes[i] == 's' {
			continue // OPERTYPE makes the oper; snomasks need an argument
		}
		if l, ok := b.theirUser(modes[i]); ok {
			um = append(um, l)
		}
	}
	out := []string{line(nil, sourceSID(uid), "UID", true, uid, ts, nick, realhost, dhost, ident, ident, ip, ts, "+"+string(um), p[11])}
	if account != "0" && account != "*" && account != "" {
		out = append(out, line(nil, sourceSID(uid), "METADATA", true, uid, "accountname", account))
	}
	if oper {
		b.operWait[uid] = true // OPERTYPE goes out with MD operclass
	}
	return out
}

// theirUser is their letter for our user mode letter l.
func (b *inspBridge) theirUser(l byte) (byte, bool) {
	n := b.ourU.name[l]
	if n == "" {
		return 0, false
	}
	t, ok := b.theirU.letter[n]
	return t, ok
}

// theirChan is their letter for our channel mode l (ranks by prefix
// character, a missing rank falls back to the next one down that they
// have: owner and admin become op).
func (b *inspBridge) theirChan(l byte) (byte, bool) {
	if b.ours.kind[l] == kindRank {
		chain := map[byte]string{'q': "~&@", 'a': "&@", 'o': "@", 'h': "%", 'v': "+"}[l]
		if chain == "" {
			chain = string(b.ours.prefix[l])
		}
		for i := 0; i < len(chain); i++ {
			if t := b.theirs.rankByPrefix(chain[i]); t != 0 {
				return t, true
			}
		}
		return 0, false
	}
	n := b.ours.name[l]
	if n == "" {
		return 0, false
	}
	t, ok := b.theirs.letter[n]
	return t, ok
}

// ourChan is our letter for their channel mode l.
func (b *inspBridge) ourChan(l byte) (byte, bool) {
	if b.theirs.kind[l] == kindRank {
		r := b.ours.rankByPrefix(b.theirs.prefix[l])
		return r, r != 0
	}
	n := b.theirs.name[l]
	if n == "" {
		return 0, false
	}
	o, ok := b.ours.letter[n]
	return o, ok
}

func (b *inspBridge) ourUser(l byte) (byte, bool) {
	n := b.theirU.name[l]
	if n == "" {
		return 0, false
	}
	o, ok := b.ourU.letter[n]
	return o, ok
}

// takesArg says whether a mode of kind k takes an argument going on/off.
func takesArg(k byte, add bool) bool {
	return k == kindList || k == kindParam || k == kindRank || (k == kindSet && add)
}

// modeChange is one parsed mode letter.
type modeChange struct {
	add bool
	l   byte
	arg string
}

// parseModes reads "+nt-k key" with a table; letters it doesn't know are
// kept without an argument (and dropped by the mapping).
func parseModes(t modeTable, modes string, args []string) []modeChange {
	var out []modeChange
	add := true
	for i := 0; i < len(modes); i++ {
		switch modes[i] {
		case '+':
			add = true
			continue
		case '-':
			add = false
			continue
		}
		c := modeChange{add: add, l: modes[i]}
		if takesArg(t.kind[c.l], add) {
			if len(args) == 0 {
				continue
			}
			c.arg, args = args[0], args[1:]
		}
		out = append(out, c)
	}
	return out
}

// writeModes is "+nt-k" and its arguments.
func writeModes(cs []modeChange) (string, []string) {
	var b strings.Builder
	var args []string
	sign := byte(0)
	for _, c := range cs {
		s := byte('-')
		if c.add {
			s = '+'
		}
		if s != sign {
			b.WriteByte(s)
			sign = s
		}
		b.WriteByte(c.l)
		if c.arg != "" {
			args = append(args, c.arg)
		}
	}
	return b.String(), args
}

func (b *inspBridge) outChanMode(src, ch, modes string, args []string) []string {
	ts, ok := b.chanTS[b.fold(ch)]
	if !ok {
		return nil
	}
	var cs []modeChange
	for _, c := range parseModes(b.ours, modes, args) {
		if b.ours.kind[c.l] == kindList {
			mask, ok := b.banOut(c.arg)
			if !ok {
				continue // an extban InspIRCd doesn't have
			}
			c.arg = mask
		}
		if t, ok := b.theirChan(c.l); ok {
			c.l = t
			cs = append(cs, c)
		}
	}
	if len(cs) == 0 {
		return nil
	}
	var out []string
	for i := 0; i < len(cs); i += modesPerLine {
		ms, as := writeModes(cs[i:min(i+modesPerLine, len(cs))])
		out = append(out, line(nil, src, "FMODE", false, append([]string{ch, strconv.FormatInt(ts, 10), ms}, as...)...))
	}
	return out
}

// outOperMD turns MD operclass into InspIRCd's OPERTYPE for an oper
// waiting for it. The type is their rank as BeeIRCd's WHOIS words it
// ("Network Administrator", from MD bee-opertype just before); InspIRCd
// shows it in its oper-up notice and WHOIS. Only a label: no rights.
func (b *inspBridge) outOperMD(uid, key, value string) []string {
	switch key {
	case "bee-opertype":
		if value != "" {
			b.operRank[uid] = value
		}
	case "operclass":
		rank, wait := b.operRank[uid], b.operWait[uid]
		b.forgetOper(uid)
		if wait && value != "" {
			if rank == "" {
				rank = "IRC Operator"
			}
			return []string{line(nil, uid, "OPERTYPE", true, rank)}
		}
	}
	return nil
}

func (b *inspBridge) forgetOper(uid string) {
	delete(b.operWait, uid)
	delete(b.operRank, uid)
}

func (b *inspBridge) outUserMode(src, uid, modes string) []string {
	var cs []modeChange
	var out []string
	for _, c := range parseModes(b.ourU, modes, nil) {
		if c.l == 's' {
			continue
		}
		if c.l == 'o' && c.add {
			b.operWait[uid] = true // OPERTYPE goes out with MD operclass
			continue
		}
		if c.l == 'o' {
			b.forgetOper(uid)
		}
		if c.l == 'o' && src != uid {
			// Deoper goes from the user, or InspIRCd says "<server> logged
			// <nick> out of their server operator account".
			if t, ok := b.theirUser(c.l); ok {
				c.l = t
				ms, _ := writeModes([]modeChange{c})
				out = append(out, line(nil, uid, "MODE", false, uid, ms))
			}
			continue
		}
		if t, ok := b.theirUser(c.l); ok {
			c.l = t
			cs = append(cs, c)
		}
	}
	if len(cs) > 0 {
		ms, _ := writeModes(cs)
		out = append([]string{line(nil, src, "MODE", false, uid, ms)}, out...)
	}
	return out
}

// outSJoin: SJOIN ts #chan [+modes args] :items -> FJOIN (members and
// modes) and LMODE (bans, exceptions, invite exceptions).
func (b *inspBridge) outSJoin(src string, p []string) []string {
	if len(p) < 3 {
		return nil
	}
	ts, err := strconv.ParseInt(p[0], 10, 64)
	if err != nil {
		return nil
	}
	ch := p[1]
	b.chanTS[b.fold(ch)] = ts
	tss := p[0]
	var modes []modeChange
	if rest := p[2 : len(p)-1]; len(rest) > 0 && strings.HasPrefix(rest[0], "+") {
		for _, c := range parseModes(b.ours, rest[0], rest[1:]) {
			if t, ok := b.theirChan(c.l); ok && b.ours.kind[c.l] != kindRank && b.ours.kind[c.l] != kindList {
				c.l = t
				modes = append(modes, c)
			}
		}
	}
	var members []string
	lists := map[byte][]string{}
	for _, it := range strings.Fields(p[len(p)-1]) {
		setAt, setBy := "0", src
		if strings.HasPrefix(it, "<") {
			end := strings.IndexByte(it, '>')
			if end < 0 {
				continue
			}
			at, by, _ := strings.Cut(it[1:end], ",")
			setAt, setBy, it = at, by, it[end+1:]
		}
		if it == "" {
			continue
		}
		if l := listFromSJoin(it[0]); l != 0 {
			t, ok := b.theirChan(l)
			if mask, known := b.banOut(it[1:]); ok && known {
				lists[t] = append(lists[t], mask, setBy, setAt)
			}
			continue
		}
		var ranks []byte
		for len(it) > 0 {
			r := rankFromSJoin(it[0])
			if r == 0 {
				break
			}
			if t, ok := b.theirChan(r); ok && strings.IndexByte(string(ranks), t) < 0 {
				ranks = append(ranks, t)
			}
			it = it[1:]
		}
		if it != "" {
			members = append(members, string(ranks)+","+it)
		}
	}
	var out []string
	if len(members) > 0 || len(modes) > 0 || len(lists) == 0 {
		ms, as := writeModes(modes)
		if ms == "" {
			ms = "+"
		}
		ps := append([]string{ch, tss, ms}, as...)
		ps = append(ps, strings.Join(members, " "))
		out = append(out, line(nil, sourceSID(src), "FJOIN", true, ps...))
	}
	keys := make([]byte, 0, len(lists))
	for k := range lists {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	for _, k := range keys {
		items := lists[k]
		for i := 0; i < len(items); i += 3 * 10 {
			part := items[i:min(i+30, len(items))]
			out = append(out, line(nil, sourceSID(src), "LMODE", false, append([]string{ch, tss, string(k)}, part...)...))
		}
	}
	return out
}

// outChange: our own users' host/ident/name changes go as FHOST/FIDENT/
// FNAME from the user; someone else's (services, an oper) as ENCAP to
// the user's server.
func (b *inspBridge) outChange(src, cmd string, p []string, self, other string) []string {
	var uid, value string
	if strings.HasPrefix(cmd, "SET") {
		if len(p) < 1 {
			return nil
		}
		uid, value = src, p[len(p)-1]
	} else {
		if len(p) < 2 {
			return nil
		}
		uid, value = p[0], p[len(p)-1]
	}
	if !b.behind[sourceSID(uid)] {
		if self == "FNAME" {
			return []string{line(nil, uid, "FNAME", true, value)}
		}
		return []string{line(nil, uid, self, false, value, "*")}
	}
	return []string{line(nil, src, "ENCAP", other == "CHGNAME", sourceSID(uid), other, uid, value)}
}

// banTypes are UnrealIRCd TKL letters and InspIRCd X-line types.
var banTypes = map[byte]string{BanGline: "G", BanGZline: "Z", BanShun: "SHUN", BanException: "E", BanQline: "Q"}

// outTKL: TKL +|- type user host setby expire setat [exempt] :reason
func (b *inspBridge) outTKL(src string, p []string) []string {
	if len(p) < 5 || len(p[1]) != 1 {
		return nil
	}
	typ := banTypes[p[1][0]]
	realname := p[1][0] == BanGline && p[2] == realnameUser
	if realname {
		typ = "R" // an R-line: a regex over "nick!user@host realname"
	}
	if typ == "" || (!realname && irc.IsPlaceUser(p[2])) || strings.HasPrefix(p[2], "%") {
		return nil // InspIRCd has no X-line by country or network, and no soft ones
	}
	mask := p[2] + "@" + p[3]
	switch p[1][0] {
	case BanGZline, BanQline:
		mask = p[3]
	}
	if realname {
		// Unreal writes a space '_'; InspIRCd wants a regex (inspraline.go).
		mask = rlineFromGlob(strings.ReplaceAll(p[3], "_", " "))
	}
	// InspIRCd names the line's source in its notice ("<nick> added a
	// timed G-line ..."), so a ban set or removed by someone online goes
	// from them, not from the server.
	nick, _, _ := strings.Cut(p[4], "!")
	if uid := b.ourUID(nick); uid != "" {
		src = uid
	}
	if p[0] == "-" {
		return []string{line(nil, src, "DELLINE", false, typ, mask)}
	}
	if len(p) < 8 {
		return nil
	}
	expires, _ := strconv.ParseInt(p[5], 10, 64)
	setAt, _ := strconv.ParseInt(p[6], 10, 64)
	dur := int64(0)
	if expires > 0 {
		dur = max(expires-setAt, 1)
	}
	return []string{line(nil, src, "ADDLINE", true, typ, mask, p[4], strconv.FormatInt(setAt, 10), strconv.FormatInt(dur, 10), p[len(p)-1])}
}

// outSASL: from the client's server to services it is
//
//	ENCAP <services> SASL <uid> * <mode> <data...>
//
// and from services to the client's server
//
//	ENCAP <its SID> SASL <agent> <uid> <mode> <data...>
func (b *inspBridge) outSASL(src string, p []string) []string {
	if len(p) < 4 {
		return nil
	}
	target, uid, rest := p[0], p[1], p[2:]
	home := sourceSID(uid)
	if target == home || strings.EqualFold(target, b.names[home]) {
		ps := append([]string{home, "SASL", src, uid}, rest...)
		return []string{line(nil, src, "ENCAP", false, ps...)}
	}
	ps := append([]string{target, "SASL", uid, "*"}, rest...)
	return []string{line(nil, src, "ENCAP", false, ps...)}
}

// ---- their lines in ----

func (b *inspBridge) In(raw string) (run, reply []string) {
	m, err := irc.Parse(raw)
	if err != nil {
		return nil, nil
	}
	if m.Source == "" {
		m.Source = b.peer.SID
	}
	if strings.ContainsAny(m.Source, "\r\n\x00 ") {
		return nil, nil // nobody: the server would refuse it anyway
	}
	// A held UID waits only for its own METADATA lines.
	if b.pending != nil && !(m.Command == "METADATA" && len(m.Params) >= 2 && m.Params[0] == b.pending.p[0]) {
		after := b.pending.after
		run = append(append(run, b.flushUID()), after...)
	}
	r, rep := b.in(m)
	return sound(append(run, r...)), sound(rep)
}

func (b *inspBridge) flushUID() string {
	u := b.pending
	b.pending = nil
	p := u.p
	// UID uid ts nick realhost dhost realuser duser ip signon +modes [args] :real
	uid, ts, nick, realhost, dhost, duser, ip := p[0], p[1], p[2], p[3], p[4], p[6], p[7]
	b.nicks[uid] = nick
	b.nickTS[uid], _ = strconv.ParseInt(ts, 10, 64)
	var modes []byte
	for _, c := range parseModes(b.theirU, p[9], p[10:len(p)-1]) {
		if l, ok := b.ourUser(c.l); ok && c.add && l != 's' {
			modes = append(modes, l)
		}
	}
	cloak := "*"
	if dhost != realhost {
		if strings.IndexByte(string(modes), 'x') < 0 {
			modes = append(modes, 'x')
		}
		cloak = dhost
	}
	acc := u.acc
	if acc == "" {
		acc = "0"
	}
	return line(nil, u.src, "UID", true, nick, "1", ts, duser, realhost, uid, acc, "+"+string(modes), "*", cloak, encodeIP(ip), p[len(p)-1])
}

func (b *inspBridge) in(m irc.Message) (run, reply []string) {
	p := m.Params
	src := m.Source
	last := func() string {
		if len(p) == 0 {
			return ""
		}
		return p[len(p)-1]
	}
	one := func(l string) ([]string, []string) { return []string{l}, nil }
	switch m.Command {
	case "SNONOTICE": // :sid SNONOTICE <letter> :text
		if len(p) < 2 || p[0] == "" {
			return nil, nil
		}
		if p[0] != "G" && p[0] != "g" {
			// Any other letter: a server notice for our opers, under our
			// letter for the same news (see inspSno).
			if l, ok := inspSno[p[0][0]|0x20]; ok && len(p[0]) == 1 {
				return one(line(nil, src, "SENDSNO", true, string(l), p[1]))
			}
			return nil, nil
		}
		// G: their GLOBOPS.
		// "From nick: text" from one of their opers goes on from that
		// person, so UnrealIRCd shows "from nick: text", not the server.
		if rest, ok := strings.CutPrefix(p[1], "From "); ok {
			if nick, text, ok := strings.Cut(rest, ": "); ok {
				if uid := b.theirUID(nick); uid != "" {
					return one(line(nil, uid, "GLOBOPS", true, text))
				}
			}
		}
		return one(line(nil, src, "GLOBOPS", true, p[1]))
	case "SINFO": // :sid SINFO desc :text -> :sid SDESC :text
		// Their server's new description; the other keys (version and
		// such) are only shown by InspIRCd itself.
		if len(p) >= 2 && p[0] == "desc" && b.behind[src] {
			return one(line(nil, src, "SDESC", true, last()))
		}
		return nil, nil
	case "BURST", "RSQUIT", "RESYNC", "SVSHOLD", "SVSOPER", "SVSCMODE", "MAP":
		return nil, nil
	case "SERVER": // :up SERVER name sid [key=value...] :desc
		if len(p) < 3 {
			return nil, nil
		}
		b.behind[p[1]], b.names[p[1]] = true, p[0]
		return one(line(nil, src, "SID", true, p[0], "2", p[1], last()))
	case "ENDBURST":
		if src == b.peer.SID {
			b.synced = true
		}
		return one(":" + src + " EOS")
	case "UID":
		if len(p) < 11 || !strings.HasPrefix(p[9], "+") {
			return nil, nil
		}
		u := &inspUID{src: src, p: p}
		if b.synced {
			// After the burst a user must show up at once: its account
			// (a SASL login) follows as its own line.
			b.pending = u
			return one(b.flushUID())
		}
		b.pending = u // in the burst: its METADATA lines come right after
		return nil, nil
	case "METADATA":
		return b.inMetadata(src, p), nil
	case "OPERTYPE": // :uid OPERTYPE :type
		// The type goes on as their oper class, which ranks them here
		// (a type with our class's name gets that class's flags).
		out := []string{line(nil, src, "UMODE2", false, "+o")}
		if t := strings.TrimSpace(last()); t != "" {
			out = append(out, line(nil, sourceSID(src), "MD", true, "client", src, "operclass", t))
		}
		return out, nil
	case "FJOIN":
		return b.inFJoin(src, p), nil
	case "IJOIN": // :uid IJOIN #chan membid [ts modes]
		if len(p) < 2 {
			return nil, nil
		}
		k := b.fold(p[0])
		ts, ok := b.chanTS[k]
		pre := ""
		if len(p) > 2 {
			if t, err := strconv.ParseInt(p[2], 10, 64); err == nil {
				if !ok || t <= ts {
					if len(p) > 3 {
						pre = b.sjoinRanks(p[3]) // their ranks count: not a losing TS
					}
					ts, ok = t, true
					b.chanTS[k] = t
				}
			}
		}
		if !ok {
			ts = b.now()
			b.chanTS[k] = ts
		}
		return one(line(nil, sourceSID(src), "SJOIN", true, strconv.FormatInt(ts, 10), p[0], pre+src))
	case "LMODE":
		return b.inLMode(src, p), nil
	case "FMODE": // :src FMODE #chan ts modes args
		if len(p) < 3 {
			return nil, nil
		}
		ts, _ := strconv.ParseInt(p[1], 10, 64)
		if cur, ok := b.chanTS[b.fold(p[0])]; ok && ts > cur {
			return nil, nil // a channel that lost its TS fight
		}
		return b.inChanMode(src, p[0], p[2], p[3:]), nil
	case "MODE": // :src MODE target modes [args]
		if len(p) < 2 {
			return nil, nil
		}
		if irc.IsChannel(p[0]) {
			return b.inChanMode(src, p[0], p[1], p[2:]), nil
		}
		ms := b.inUserModes(p[1], p[2:])
		if ms == "" {
			return nil, nil
		}
		out := []string{line(nil, src, "SVS2MODE", false, p[0], ms)}
		if src == p[0] {
			out[0] = line(nil, src, "UMODE2", false, ms)
		}
		if dropsMode(ms, 'o') { // no longer an oper: their class goes too
			out = append(out, line(nil, sourceSID(p[0]), "MD", false, "client", p[0], "operclass"))
		}
		return out, nil
	case "NICK": // :uid NICK new ts
		if len(p) < 1 {
			return nil, nil
		}
		b.nicks[src] = p[0]
		if len(p) > 1 {
			b.nickTS[src], _ = strconv.ParseInt(p[1], 10, 64)
		}
		m.Source = src
		return one(m.String())
	case "SAVE": // SAVE uid ts: rename to the UID
		if len(p) < 2 {
			return nil, nil
		}
		if ts, ok := b.nickTS[p[0]]; ok && strconv.FormatInt(ts, 10) != p[1] {
			return nil, nil // an old SAVE: the nick changed since
		}
		b.nicks[p[0]], b.nickTS[p[0]] = p[0], 100
		return one(line(nil, sourceSID(src), "SVSNICK", false, p[0], p[0], "100"))
	case "QUIT":
		b.gone(src)
		return one(line(nil, src, "QUIT", true, last()))
	case "KILL":
		if len(p) > 0 {
			b.gone(p[0])
		}
		m.Source = src
		return one(m.String())
	case "KICK": // :src KICK #chan uid [membid] :reason
		if len(p) < 2 {
			return nil, nil
		}
		reason := ""
		if len(p) > 2 {
			reason = last()
		}
		return one(line(nil, src, "KICK", true, p[0], p[1], reason))
	case "INVITE": // :src INVITE uid #chan ts [expiry]
		if len(p) < 2 {
			return nil, nil
		}
		return one(line(nil, src, "INVITE", false, p[0], p[1]))
	case "FTOPIC", "SVSTOPIC":
		return b.inTopic(src, m.Command, p), nil
	case "AWAY": // :uid AWAY [ts :msg]
		if len(p) < 2 {
			return one(":" + src + " AWAY")
		}
		return one(line(nil, src, "AWAY", true, last()))
	case "FHOST": // :uid FHOST dhost realhost
		if len(p) < 1 || p[0] == "*" {
			return nil, nil
		}
		return one(line(nil, src, "SETHOST", false, p[0]))
	case "FIDENT":
		if len(p) < 1 || p[0] == "*" {
			return nil, nil
		}
		return one(line(nil, src, "SETIDENT", false, p[0]))
	case "FNAME":
		if len(p) < 1 {
			return nil, nil
		}
		return one(line(nil, src, "SETNAME", true, last()))
	case "CHGHOST", "CHGIDENT", "CHGNAME":
		if len(p) < 2 {
			return nil, nil
		}
		return one(line(nil, src, m.Command, m.Command == "CHGNAME", p[0], last()))
	case "ENCAP": // :src ENCAP target cmd args...
		if len(p) < 2 {
			return nil, nil
		}
		cmd := strings.ToUpper(p[1])
		run := b.inEncap(src, p[0], cmd, p[2:])
		if len(run) > 0 && len(p) >= 4 && !b.behind[sourceSID(p[2])] {
			// A change to one of our users: InspIRCd waits for the
			// user's server to announce it (FHOST, FIDENT, FNAME).
			switch cmd {
			case "CHGHOST":
				reply = []string{line(nil, p[2], "FHOST", false, p[len(p)-1], "*")}
			case "CHGIDENT":
				reply = []string{line(nil, p[2], "FIDENT", false, p[len(p)-1], "*")}
			case "CHGNAME":
				reply = []string{line(nil, p[2], "FNAME", true, p[len(p)-1])}
			}
		}
		return run, reply
	case "ADDLINE": // :src ADDLINE type mask setter settime duration :reason
		return b.inAddLine(src, p), nil
	case "DELLINE": // :src DELLINE type mask
		if len(p) < 2 {
			return nil, nil
		}
		t, user, host, ok := b.banOf(p[0], p[1])
		if !ok {
			return nil, nil
		}
		return one(line(nil, src, "TKL", false, "-", t, user, host, b.who(src)))
	case "SQUIT": // :src SQUIT name|sid :reason
		if len(p) < 1 {
			return nil, nil
		}
		name := p[0]
		if n, ok := b.names[name]; ok {
			name = n
		}
		delete(b.behind, b.sidOf(name))
		return one(line(nil, src, "SQUIT", true, name, last()))
	case "PING": // :src PING target
		if len(p) >= 1 && p[0] == b.me.SID {
			return nil, []string{line(nil, b.me.SID, "PONG", false, src)}
		}
		if len(p) >= 1 && b.names[p[0]] != "" && !b.behind[p[0]] {
			// InspIRCd measures the lag of every server with PING. A server
			// behind us can't be reached through this bridge, so we answer
			// for it, or InspIRCd warns of a ping timeout and splits it.
			return nil, []string{line(nil, p[0], "PONG", false, src)}
		}
		return nil, nil
	case "PONG": // :src PONG us
		return one(line(nil, src, "PONG", true, b.names[src], b.me.Name))
	case "NUM": // :sid NUM source target numeric params...
		if len(p) < 3 || !b.behind[p[0]] || !isNumeric(p[2]) {
			return nil, nil
		}
		return one(line(nil, p[0], p[2], true, append([]string{p[1]}, p[3:]...)...))
	case "IDLE": // :uid IDLE target -> :target IDLE uid signon idle
		if len(p) == 1 && !b.behind[sourceSID(p[0])] {
			signon, idle := b.nickTS[p[0]], int64(0)
			if b.me.Idle != nil {
				if on, i, ok := b.me.Idle(p[0]); ok {
					signon, idle = on, i
				}
			}
			return nil, []string{line(nil, p[0], "IDLE", false, src, strconv.FormatInt(signon, 10), strconv.FormatInt(idle, 10))}
		}
		if len(p) >= 3 && b.behind[sourceSID(src)] && !b.behind[sourceSID(p[0])] {
			// :their uid IDLE our asker signon idle: the answer to a WHOIS
			// we sent on; our server builds the WHOIS from it.
			return one(line(nil, src, "IDLE", false, p[0], p[1], p[2]))
		}
		return nil, nil
	case "MOTD", "ADMIN", "TIME", "VERSION": // :uid CMD server -> :uid CMD :server
		if len(p) < 1 || !b.behind[sourceSID(src)] {
			return nil, nil
		}
		return one(line(nil, src, m.Command, true, p[0]))
	case "STATS": // :uid STATS letter server -> :uid STATS letter :server
		if len(p) < 2 || !b.behind[sourceSID(src)] {
			return nil, nil
		}
		return one(line(nil, src, "STATS", true, p[0], p[1]))
	case "REHASH": // :uid REHASH <mask> -> :uid REHASH :<server> for each of ours it matches
		// InspIRCd 4 sends REHASH on to every server; a word without a dot
		// or * (REHASH ssl) is for its own modules.
		if len(p) < 1 || !b.behind[sourceSID(src)] || !strings.ContainsAny(p[0], "*.") {
			return nil, nil
		}
		for _, n := range b.ourServers(p[0]) {
			run = append(run, line(nil, src, "REHASH", true, n))
		}
		return run, nil
	case "RCONNECT": // :uid RCONNECT <server mask> <target> -> :uid CONNECT <target> 0 :<server>
		if len(p) < 2 || !b.behind[sourceSID(src)] {
			return nil, nil
		}
		ns := b.ourServers(p[0])
		if len(ns) == 0 {
			return nil, nil
		}
		return one(line(nil, src, "CONNECT", true, p[1], "0", ns[0]))
	case "PRIVMSG", "NOTICE", "TAGMSG", "WALLOPS", "PART", "SVSJOIN", "SVSPART", "SVSNICK", "ERROR",
		"SAJOIN", "SAPART", "SANICK", "ACCEPT":
		m.Source = src
		return one(m.String())
	case "ALLTIME": // an oper asks every server for its clock
		return one(line(nil, src, "TSCTL", false, "alltime"))
	}
	return nil, nil
}

// inspSno maps the letter of an InspIRCd 4 server notice from another
// server (WriteGlobalSno sends it uppercase; looked up lowercase) to our
// snomask letter for the same news. Left out on purpose, so dropped:
// k (their "Local kill by" - we announce their kills from the KILL line
// as K), c, q and n (we make C, Q and N from their UID, QUIT and NICK
// lines ourselves), w (WebIRC gateways) and j (channel creation), which
// have no letter here, and any letter we don't know. g is GLOBOPS.
var inspSno = map[byte]byte{
	'a': 'a', // announcements, SA* commands, module loads
	'd': 'd', // DNSBL hits
	'f': 'f', // spam filter hits (and the bans it sets)
	'r': 'r', // rehash
	'v': 'v', // oper override
	'l': 'L', // their links and splits
	'o': 'O', // failed oper logins, oper commands (m_operlog)
	'x': 'X', // Q-lined nick attempts
}

// theirServer is the name of the server on their side that x names (a
// server name or SID), "" when it is not one of theirs.
func (b *inspBridge) theirServer(x string) string {
	for sid, n := range b.names {
		if b.behind[sid] && (sid == x || strings.EqualFold(n, x)) {
			return n
		}
	}
	return ""
}

// ourServers are the names of the servers on our side that mask matches,
// ours first.
func (b *inspBridge) ourServers(mask string) []string {
	mask = irc.Fold(mask)
	var out, more []string
	if irc.Match(mask, irc.Fold(b.me.Name)) {
		out = append(out, b.me.Name)
	}
	for sid, n := range b.names {
		if !b.behind[sid] && sid != b.me.SID && irc.Match(mask, irc.Fold(n)) {
			more = append(more, n)
		}
	}
	sort.Strings(more)
	return append(out, more...)
}

// theirUID is the UID of the person on their side with nick, "" when
// there is none.
func (b *inspBridge) theirUID(nick string) string { return b.uidByNick(nick, true) }

// ourUID is the UID of the person on our side with nick, "" when there is
// none.
func (b *inspBridge) ourUID(nick string) string { return b.uidByNick(nick, false) }

func (b *inspBridge) uidByNick(nick string, theirs bool) string {
	if nick == "" {
		return ""
	}
	for uid, n := range b.nicks {
		if len(uid) == 9 && b.behind[sourceSID(uid)] == theirs && irc.Fold(n) == irc.Fold(nick) {
			return uid
		}
	}
	return ""
}

// who names a source for people: a nick or a server name.
func (b *inspBridge) who(src string) string {
	if n := b.nicks[src]; n != "" {
		return n
	}
	if n := b.names[src]; n != "" {
		return n
	}
	return src
}

// sjoinRanks turns their rank letters ("ov") into our SJOIN prefixes.
func (b *inspBridge) sjoinRanks(letters string) string {
	var out []byte
	for i := 0; i < len(letters); i++ {
		if r, ok := b.ourChan(letters[i]); ok && b.ours.kind[r] == kindRank {
			if q := sjoinRank[r]; q != 0 {
				out = append(out, q)
			}
		}
	}
	return string(out)
}

// inFJoin: FJOIN #chan ts +modes [args] :[ranks,]uid[:membid] ...
func (b *inspBridge) inFJoin(src string, p []string) []string {
	if len(p) < 4 {
		return nil
	}
	ts, err := strconv.ParseInt(p[1], 10, 64)
	if err != nil {
		return nil
	}
	k := b.fold(p[0])
	if cur, ok := b.chanTS[k]; !ok || ts < cur {
		b.chanTS[k] = ts
	}
	var modes []modeChange
	for _, c := range parseModes(b.theirs, p[2], p[3:len(p)-1]) {
		if l, ok := b.ourChan(c.l); ok && c.add && b.ours.kind[l] != kindRank && b.ours.kind[l] != kindList {
			c.l = l
			modes = append(modes, c)
		}
	}
	var items []string
	for _, it := range strings.Fields(p[len(p)-1]) {
		ranks, uid, ok := strings.Cut(it, ",")
		if !ok {
			ranks, uid = "", it
		}
		uid, _, _ = strings.Cut(uid, ":")
		if uid == "" {
			continue
		}
		items = append(items, b.sjoinRanks(ranks)+uid)
	}
	ps := []string{p[1], p[0]}
	if len(modes) > 0 {
		ms, as := writeModes(modes)
		ps = append(append(ps, ms), as...)
	}
	ps = append(ps, strings.Join(items, " "))
	return []string{line(nil, sourceSID(src), "SJOIN", true, ps...)}
}

// inLMode: LMODE #chan ts letter [mask setter setat]...
func (b *inspBridge) inLMode(src string, p []string) []string {
	if len(p) < 3 || len(p[2]) != 1 {
		return nil
	}
	l, ok := b.ourChan(p[2][0])
	if !ok {
		return nil
	}
	q := sjoinList[l]
	if q == 0 {
		return nil
	}
	var items []string
	for i := 3; i+2 < len(p); i += 3 {
		if mask, ok := irc.FromInspBan(p[i]); ok {
			items = append(items, fmt.Sprintf("<%s,%s>%c%s", p[i+2], p[i+1], q, mask))
		}
	}
	if len(items) == 0 {
		return nil
	}
	var out []string
	for i := 0; i < len(items); i += 10 {
		out = append(out, line(nil, sourceSID(src), "SJOIN", true, p[1], p[0], strings.Join(items[i:min(i+10, len(items))], " ")))
	}
	return out
}

func (b *inspBridge) inChanMode(src, ch, modes string, args []string) []string {
	var cs []modeChange
	for _, c := range parseModes(b.theirs, modes, args) {
		if l, ok := b.ourChan(c.l); ok {
			if b.ours.kind[l] == kindList {
				mask, ok := irc.FromInspBan(c.arg)
				if !ok {
					continue // inverted or unknown: we can't keep it
				}
				c.arg = mask
			}
			c.l = l
			cs = append(cs, c)
		}
	}
	if len(cs) == 0 {
		return nil
	}
	ms, as := writeModes(cs)
	return []string{line(nil, src, "MODE", false, append([]string{ch, ms}, as...)...)}
}

func (b *inspBridge) inUserModes(modes string, args []string) string {
	var cs []modeChange
	for _, c := range parseModes(b.theirU, modes, args) {
		if l, ok := b.ourUser(c.l); ok && l != 's' {
			c.l, c.arg = l, ""
			cs = append(cs, c)
		}
	}
	ms, _ := writeModes(cs)
	return ms
}

// dropsMode tells whether a mode string like "+i-o" takes mode l off.
func dropsMode(ms string, l byte) bool {
	minus := false
	for i := 0; i < len(ms); i++ {
		switch ms[i] {
		case '+':
			minus = false
		case '-':
			minus = true
		case l:
			if minus {
				return true
			}
		}
	}
	return false
}

// inTopic: FTOPIC #chan chants topicts [setter] :text
func (b *inspBridge) inTopic(src, cmd string, p []string) []string {
	if len(p) < 4 {
		return nil
	}
	if cmd == "SVSTOPIC" { // SVSTOPIC #chan topicts setter :text
		return []string{line(nil, src, "TOPIC", true, p[0], p[2], p[1], p[len(p)-1])}
	}
	cts, _ := strconv.ParseInt(p[1], 10, 64)
	if cur, ok := b.chanTS[b.fold(p[0])]; ok && cts > cur {
		return nil
	}
	setter := b.nicks[src]
	if len(p) > 4 {
		setter = p[3]
	} else if setter == "" {
		setter = b.names[src]
		if setter == "" {
			setter = src
		}
	}
	return []string{line(nil, src, "TOPIC", true, p[0], setter, p[2], p[len(p)-1])}
}

func (b *inspBridge) inMetadata(src string, p []string) []string {
	if len(p) < 2 {
		return nil
	}
	value := ""
	if irc.IsChannel(p[0]) || p[0] == "@" {
		return nil // channel and membership data: mlock, topiclock ...
	}
	if len(p) > 2 {
		value = p[len(p)-1]
	}
	key := strings.ToLower(p[1])
	if p[0] == "*" {
		if key == "saslmechlist" && len(p) > 2 {
			return []string{line(nil, src, "MD", true, "client", b.names[sourceSID(src)], "saslmechlist", value)}
		}
		return nil
	}
	if key == "callerid_data" && (b.pending == nil || b.pending.p[0] != p[0]) {
		// "<time>,<uid>,<uid>": who this person lets through their +g.
		var out []string
		for i, w := range strings.Split(value, ",") {
			if i > 0 && w != "" && len(out) < 100 {
				out = append(out, line(nil, p[0], "ACCEPT", false, w))
			}
		}
		return out
	}
	if key == "swhois" {
		// m_swhois keeps one extra WHOIS line: here a SWHOIS line of tag
		// "swhois" that takes the place of the one before.
		out := []string{line(nil, sourceSID(src), "SWHOIS", true, p[0], "-", "swhois", "0", "*")}
		if value != "" {
			out = append(out, line(nil, sourceSID(src), "SWHOIS", true, p[0], "+", "swhois", "0", value))
		}
		if b.pending != nil && b.pending.p[0] == p[0] {
			b.pending.after = append(b.pending.after, out...)
			return nil
		}
		return out
	}
	if key != "accountname" {
		return nil
	}
	uid := p[0]
	if b.pending != nil && b.pending.p[0] == uid {
		b.pending.acc = value
		return nil
	}
	acc := value
	if acc == "" {
		acc = "0"
	}
	return []string{line(nil, src, "SVSLOGIN", false, sourceSID(uid), uid, acc)}
}

// inEncap runs the ENCAP'd commands we know.
func (b *inspBridge) inEncap(src, target, cmd string, p []string) []string {
	if b.theirTarget(target) {
		return nil // for a server on their side
	}
	switch cmd {
	case "CHGHOST", "CHGIDENT", "CHGNAME":
		if len(p) < 2 {
			return nil
		}
		return []string{line(nil, src, cmd, cmd == "CHGNAME", p[0], p[len(p)-1])}
	case "SASL":
		// client's server -> services: <uid> <agent> <mode> <data...>
		// services -> client's server: <agent> <uid> <mode> <data...>
		if len(p) < 4 {
			return nil
		}
		if target == "*" {
			target = b.saslAgent // "any services": the server that took SASL here
			if target == "" {
				target = b.me.SID
			}
		}
		if len(p[1]) == 9 && !b.behind[sourceSID(p[1])] {
			// services answering one of our clients: <agent> <uid> ...
			return []string{line(nil, src, "SASL", false, append([]string{target, p[1]}, p[2:]...)...)}
		}
		return []string{line(nil, src, "SASL", false, append([]string{target, p[0]}, p[2:]...)...)}
	case "KNOCK": // :uid ENCAP * KNOCK #chan :reason -> :uid KNOCK #chan :reason
		// Our server shows it to the channel's ops here and passes it on
		// in this form, as UnrealIRCd 6 sends it.
		if len(p) < 2 || len(src) != 9 {
			return nil
		}
		return []string{line(nil, src, "KNOCK", true, p[0], p[len(p)-1])}
	case "UNINVITE": // :src ENCAP <sid> UNINVITE <uid> #chan -> :src UNINVITE <uid> #chan
		if len(p) < 2 {
			return nil
		}
		return []string{line(nil, src, "UNINVITE", false, p[0], p[1])}
	case "SAJOIN", "SAPART", "SANICK":
		// :oper ENCAP <sid> SAJOIN <uid> <#channel>
		return []string{line(nil, src, cmd, len(p) > 2, p...)}
	case "ALLTIME":
		return []string{line(nil, src, "TSCTL", false, "alltime")}
	}
	return nil
}

// theirTarget reports whether an ENCAP target (a SID or a server name
// mask) names only servers on their side; "*" and anything on ours (our
// services server too) is for us.
func (b *inspBridge) theirTarget(target string) bool {
	if target == "*" {
		return false
	}
	if ValidSID(target) {
		return b.behind[target]
	}
	if irc.Match(irc.Fold(target), irc.Fold(b.me.Name)) {
		return false
	}
	for sid, n := range b.names {
		if b.behind[sid] && strings.EqualFold(n, target) {
			return true
		}
	}
	return false
}

// banOf turns an X-line into TKL type, user and host.
func (b *inspBridge) banOf(typ, mask string) (t, user, host string, ok bool) {
	switch strings.ToUpper(typ) {
	case "G", "E", "SHUN":
		user, host, ok = strings.Cut(mask, "@")
		if !ok {
			user, host = "*", mask
		}
		if i := strings.IndexByte(user, '!'); i >= 0 {
			user = user[i+1:]
		}
		t = map[string]string{"G": "G", "E": "E", "SHUN": "s"}[strings.ToUpper(typ)]
		return t, user, host, true
	case "Z":
		return "Z", "*", mask, true
	case "R":
		m, why := rlineIn(mask)
		if why != "" {
			return "", "", "", false
		}
		return "G", rlineUser, rlineHost(m), true
	case "Q":
		return "Q", "*", mask, true
	}
	return "", "", "", false
}

func (b *inspBridge) inAddLine(src string, p []string) []string {
	if len(p) < 6 {
		return nil
	}
	t, user, host, ok := b.banOf(p[0], p[1])
	if !ok {
		if strings.EqualFold(p[0], "R") {
			// Not one we can enforce: say so once, to the opers (as a
			// server notice from the peer, which the server shows).
			_, why := rlineIn(p[1])
			return []string{line(nil, b.peer.SID, "SENDSNO", true, "G", "InspIRCd R-line "+p[1]+" not copied: "+why)}
		}
		return nil
	}
	setAt, _ := strconv.ParseInt(p[3], 10, 64)
	dur, _ := strconv.ParseInt(p[4], 10, 64)
	expires := int64(0)
	if dur > 0 {
		expires = setAt + dur
	}
	ps := []string{"+", t, user, host, p[2], strconv.FormatInt(expires, 10), strconv.FormatInt(setAt, 10)}
	if t == "E" {
		ps = append(ps, "GZs")
	}
	return []string{line(nil, src, "TKL", true, append(ps, p[len(p)-1])...)}
}

// isNumeric is a three-digit reply code.
func isNumeric(s string) bool {
	return len(s) == 3 && s[0] >= '0' && s[0] <= '9' && s[1] >= '0' && s[1] <= '9' && s[2] >= '0' && s[2] <= '9'
}
