package server

import (
	"maps"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/fhazal/beeircd/internal/control"
	"github.com/fhazal/beeircd/internal/irc"
	"github.com/fhazal/beeircd/internal/link"
	"github.com/fhazal/beeircd/internal/services"
	"github.com/fhazal/beeircd/internal/spamfilter"
	"github.com/fhazal/beeircd/internal/state"
)

// IRCv3 draft/metadata-2: small key/value pairs on users and channels
// (avatar, colour, status, ...). Clients SUB to the keys they want and
// get METADATA lines when those change on someone they share a channel
// with or MONITOR. A logged-in user's keys live on the account (services
// keep them and give them back on login), a registered channel's with
// ChanServ; the rest are in memory only. BeeIRCd servers pass every
// change on as BEE METADATA.

const metadataCap = "draft/metadata-2"

const (
	mdMaxSubs   = 20  // keys one client may SUB to
	mdMaxKeys   = 20  // keys one user or channel may have
	mdMaxValue  = 300 // bytes in one value
	mdSetLimit  = 10  // SETs (and CLEARs) per mdSetWindow, opers exempt
	mdSetWindow = 30 * time.Second
)

// mdKeys are the keys BeeIRCd takes.
var mdKeys = []string{"avatar", "bot", "color", "display-name", "homepage", "pronouns", "status"}

// mdLabel is how the panel's own words name a key: the key stays "color"
// on IRC, but the panel spells it "colour".
func mdLabel(key string) string {
	if key == "color" {
		return "colour"
	}
	return key
}

var mdColor = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)

func metadataValue() string {
	return metadataCap + "=max-subs=" + strconv.Itoa(mdMaxSubs) + ",max-keys=" + strconv.Itoa(mdMaxKeys) + ",max-value-bytes=" + strconv.Itoa(mdMaxValue)
}

// mdValidKey reports whether k is a key name the spec allows.
func mdValidKey(k string) bool {
	if k == "" {
		return false
	}
	for i := 0; i < len(k); i++ {
		b := k[i]
		if !(b >= 'a' && b <= 'z' || b >= '0' && b <= '9' || b == '_' || b == '.' || b == '/' || b == '-') {
			return false
		}
	}
	return true
}

// mdTarget is a user or a channel that carries metadata.
type mdTarget struct {
	u  *state.User
	ch *state.Channel
}

func (t mdTarget) name() string {
	if t.ch != nil {
		return t.ch.Name
	}
	return t.u.Nick
}

// linkID names the target on a server link: the UID or the channel.
func (t mdTarget) linkID() string {
	if t.ch != nil {
		return t.ch.Name
	}
	return t.u.UID
}

func (t mdTarget) meta() map[string]string {
	if t.ch != nil {
		return t.ch.Metadata
	}
	return t.u.Metadata
}

func (t mdTarget) set(key, value string) {
	var m *map[string]string
	if t.ch != nil {
		m = &t.ch.Metadata
	} else {
		m = &t.u.Metadata
	}
	if value == "" {
		delete(*m, key)
		return
	}
	if *m == nil {
		*m = map[string]string{}
	}
	(*m)[key] = value
}

func (s *Server) mdFail(c *Client, code string, params ...string) {
	c.send(irc.Message{Source: s.cfg.Server.Name, Command: "FAIL", Params: append([]string{"METADATA", code}, params...), Trailing: true}.String())
}

// mdCheck checks value for key; it returns a reason when it is refused.
// The spam filter is not part of it (it needs the client, cmdMetadata).
func (s *Server) mdCheck(key, value string) string {
	if !slices.Contains(mdKeys, key) {
		return ""
	}
	if len(value) > mdMaxValue {
		return "Value is too long (" + strconv.Itoa(mdMaxValue) + " bytes at most)"
	}
	if !utf8.ValidString(value) || strings.ContainsAny(value, "\x00\r\n") {
		return "Value is not valid text"
	}
	switch key {
	case "avatar", "homepage":
		u, err := url.Parse(value)
		if err != nil || u.Scheme != "https" || u.Host == "" || strings.ContainsAny(value, " \t") {
			return "Value must be an https:// address"
		}
	case "color":
		if !mdColor.MatchString(value) {
			return "Value must be a colour like #ffcc00"
		}
	case "status", "display-name":
		if censor(value, s.cfg.Censor.Words, "\x00") != value {
			return "Value contains a word that is not allowed here"
		}
	}
	return ""
}

// mdResolve finds target for c ("*" is c itself); ok false when there is
// no such user or channel c may see.
func (s *Server) mdResolve(c *Client, target string) (mdTarget, bool) {
	if target == "*" {
		return mdTarget{u: c.user}, true
	}
	if strings.HasPrefix(target, "#") {
		ch := s.net.Channel(target)
		if ch == nil || (ch.Member(c.user) == nil && (ch.HasMode('s') || ch.HasMode('p')) && !c.isOper()) {
			return mdTarget{}, false
		}
		return mdTarget{ch: ch}, true
	}
	u := s.net.UserByNick(target)
	if u == nil {
		return mdTarget{}, false
	}
	return mdTarget{u: u}, true
}

// mdOwns reports whether c may change t's keys as its own: itself, or a
// channel it is an op (or more) in. Opers may delete anyone's keys too.
func (s *Server) mdOwns(c *Client, t mdTarget) bool {
	if t.ch != nil {
		m := t.ch.Member(c.user)
		return m != nil && m.AtLeast(state.RankOp)
	}
	return t.u == c.user
}

// mdRated reports the seconds c has to wait when it is over the SET/CLEAR
// limit. It does not count: only a change that went through does (mdCount),
// so refused ones cost nothing.
func (s *Server) mdRated(c *Client) (int, bool) {
	if c.isOper() {
		return 0, false
	}
	now := time.Now()
	kept := c.mdSets[:0]
	for _, t := range c.mdSets {
		if now.Sub(t) < mdSetWindow {
			kept = append(kept, t)
		}
	}
	c.mdSets = kept
	if len(c.mdSets) >= mdSetLimit {
		wait := int(mdSetWindow-now.Sub(c.mdSets[0]))/int(time.Second) + 1
		return wait, true
	}
	return 0, false
}

// mdCount counts one accepted SET or CLEAR for c.
func (s *Server) mdCount(c *Client) {
	if !c.isOper() {
		c.mdSets = append(c.mdSets, time.Now())
	}
}

// cmdMetadata is METADATA <target> <subcommand> [params...].
func (s *Server) cmdMetadata(c *Client, m irc.Message) {
	if !c.caps[metadataCap] {
		s.reply(c, irc.ErrUnknownCommand, "METADATA", "Unknown command")
		return
	}
	if len(m.Params) < 2 {
		s.reply(c, irc.ErrNeedMoreParams, "METADATA", "Not enough parameters")
		return
	}
	target, sub, args := m.Params[0], strings.ToUpper(m.Params[1]), m.Params[2:]
	switch sub {
	case "SUB":
		s.mdSub(c, args)
		return
	case "UNSUB":
		s.mdUnsub(c, args)
		return
	case "SUBS":
		s.mdSubs(c)
		return
	case "GET", "LIST", "SET", "CLEAR", "SYNC":
	default:
		s.mdFail(c, "SUBCOMMAND_INVALID", m.Params[1], "Invalid subcommand")
		return
	}
	t, ok := s.mdResolve(c, target)
	if !ok {
		s.mdFail(c, "INVALID_TARGET", target, "Invalid target")
		return
	}
	switch sub {
	case "GET":
		if len(args) == 0 {
			s.reply(c, irc.ErrNeedMoreParams, "METADATA", "Not enough parameters")
			return
		}
		var lines []string
		for _, k := range args {
			if !mdValidKey(k) {
				s.mdFail(c, "KEY_INVALID", k, "Invalid key")
				continue
			}
			lines = append(lines, s.mdKeyLine(c, t, k))
		}
		s.mdBatch(c, "metadata", t.name(), lines)
	case "LIST":
		var lines []string
		for _, k := range sortedMeta(t.meta()) {
			lines = append(lines, s.mdKeyLine(c, t, k))
		}
		s.mdBatch(c, "metadata", t.name(), lines)
	case "SET":
		s.mdSet(c, t, args)
	case "CLEAR":
		s.mdClearCmd(c, t, args)
	case "SYNC":
		s.mdBatch(c, "metadata", t.name(), s.mdSyncLines(c, t))
	}
}

func sortedMeta(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

// mdKeyLine is RPL_KEYVALUE for key of t, or RPL_KEYNOTSET.
func (s *Server) mdKeyLine(c *Client, t mdTarget, key string) string {
	if v, ok := t.meta()[key]; ok {
		return irc.Message{Source: s.cfg.Server.Name, Command: "761", Params: []string{c.target(), t.name(), key, "*", v}, Trailing: true}.String()
	}
	return irc.Message{Source: s.cfg.Server.Name, Command: "766", Params: []string{c.target(), t.name(), key, "key not set"}, Trailing: true}.String()
}

// mdBatch sends lines to c in one batch of kind for target (plain when c
// has no batch). An empty batch is still sent: it says "nothing".
func (s *Server) mdBatch(c *Client, kind, target string, lines []string) {
	id := ""
	if c.caps["batch"] {
		s.batchSeq++
		id = "m" + strconv.FormatUint(s.batchSeq, 36)
		head := ":" + s.cfg.Server.Name + " BATCH +" + id + " " + kind
		if target != "" {
			head += " " + target
		}
		c.send(head)
	}
	for _, line := range lines {
		if id != "" {
			line = addTag(line, "batch", id)
		}
		c.send(line)
	}
	if id != "" {
		c.send(":" + s.cfg.Server.Name + " BATCH -" + id)
	}
}

// mdLine is the METADATA line telling a subscriber key of t changed.
func (s *Server) mdLine(name, key, value string) string {
	if value == "" {
		return irc.Message{Source: s.cfg.Server.Name, Command: "METADATA", Params: []string{name, key, "*"}}.String()
	}
	return irc.Message{Source: s.cfg.Server.Name, Command: "METADATA", Params: []string{name, key, "*", value}, Trailing: true}.String()
}

// mdLinesFor are METADATA lines for t's keys c subscribed to.
func (s *Server) mdLinesFor(c *Client, t mdTarget) []string {
	var out []string
	meta := t.meta()
	for _, k := range sortedMeta(meta) {
		if c.mdSubs[k] {
			out = append(out, s.mdLine(t.name(), k, meta[k]))
		}
	}
	return out
}

// mdSyncLines is what SYNC (and a JOIN) sends: t's subscribed keys and,
// for a channel, those of every other member too.
func (s *Server) mdSyncLines(c *Client, t mdTarget) []string {
	lines := s.mdLinesFor(c, t)
	if t.ch == nil {
		return lines
	}
	uids := make([]string, 0, len(t.ch.Members))
	for uid := range t.ch.Members {
		uids = append(uids, uid)
	}
	slices.Sort(uids)
	for _, uid := range uids {
		if u := t.ch.Members[uid].User; u != c.user {
			lines = append(lines, s.mdLinesFor(c, mdTarget{u: u})...)
		}
	}
	return lines
}

func (s *Server) mdSet(c *Client, t mdTarget, args []string) {
	if len(args) == 0 {
		s.reply(c, irc.ErrNeedMoreParams, "METADATA", "Not enough parameters")
		return
	}
	key := args[0]
	value := ""
	if len(args) > 1 {
		value = args[1]
	}
	if !mdValidKey(key) {
		s.mdFail(c, "KEY_INVALID", key, "Invalid key")
		return
	}
	owns := s.mdOwns(c, t)
	if !owns && !(value == "" && c.isOper()) {
		s.mdFail(c, "KEY_NO_PERMISSION", t.name(), key, "You may not change this target's metadata")
		return
	}
	if !slices.Contains(mdKeys, key) {
		s.mdFail(c, "KEY_NO_PERMISSION", t.name(), key, "This server does not take that key ("+strings.Join(mdKeys, ", ")+")")
		return
	}
	if s.readOnly() {
		s.mdFail(c, "KEY_NO_PERMISSION", t.name(), key, services.ReadonlyRefusal)
		return
	}
	if wait, limited := s.mdRated(c); limited {
		s.mdFail(c, "RATE_LIMITED", t.name(), key, strconv.Itoa(wait), "Too many changes, wait a moment")
		return
	}
	if value == "" {
		if _, ok := t.meta()[key]; !ok {
			s.mdFail(c, "KEY_NOT_SET", t.name(), key, "Key not set")
			return
		}
	} else {
		if why := s.mdCheck(key, value); why != "" {
			s.mdFail(c, "VALUE_INVALID", why)
			return
		}
		if (key == "status" || key == "display-name") && !s.spamSkip(c) {
			if r := s.svc.SpamSet().First(spamfilter.Away, value); r != nil && spamfilter.Known(r.Action) {
				if s.spamDoAs(c, r, spamfilter.Away, "METADATA", t.name(), value) != spamPass {
					return
				}
			}
		}
		if _, ok := t.meta()[key]; !ok && len(t.meta()) >= mdMaxKeys {
			s.mdFail(c, "LIMIT_REACHED", t.name(), "Metadata limit reached")
			return
		}
	}
	s.mdCount(c) // only a change that went through counts toward the limit
	if !owns {
		s.sno('a', "%s deleted metadata key %s of %s", c.nick, key, t.name())
	}
	s.mdApply(nil, c, t, key, value, true)
	s.mdBatch(c, "metadata", t.name(), []string{s.mdKeyLine(c, t, key)})
}

// mdClearCmd is CLEAR [key...]: the named keys of t, or all of them.
func (s *Server) mdClearCmd(c *Client, t mdTarget, keys []string) {
	owns := s.mdOwns(c, t)
	if !owns && !c.isOper() {
		s.mdFail(c, "KEY_NO_PERMISSION", t.name(), "*", "You may not change this target's metadata")
		return
	}
	if s.readOnly() {
		s.mdFail(c, "KEY_NO_PERMISSION", t.name(), "*", services.ReadonlyRefusal)
		return
	}
	if wait, limited := s.mdRated(c); limited {
		s.mdFail(c, "RATE_LIMITED", t.name(), "*", strconv.Itoa(wait), "Too many changes, wait a moment")
		return
	}
	if len(keys) == 0 {
		keys = sortedMeta(t.meta())
	}
	var lines, valid, done []string
	for _, k := range keys {
		if !mdValidKey(k) {
			s.mdFail(c, "KEY_INVALID", k, "Invalid key")
			continue
		}
		valid = append(valid, k)
		if _, ok := t.meta()[k]; ok {
			done = append(done, k)
		}
	}
	if len(done) > 0 {
		s.mdCount(c)
	}
	if len(done) > 0 && !owns {
		s.sno('a', "%s cleared the metadata of %s", c.nick, t.name())
	}
	for _, k := range done {
		s.mdApply(nil, c, t, k, "", true)
	}
	for _, k := range valid {
		lines = append(lines, s.mdKeyLine(c, t, k))
	}
	if len(lines) == 0 && !c.caps["batch"] {
		// Nothing to clear and no batch to say so: don't leave c waiting.
		s.mdFail(c, "KEY_NOT_SET", t.name(), "*", "Key not set")
		return
	}
	s.mdBatch(c, "metadata", t.name(), lines)
}

// mdClearAll deletes all of t's keys (panel, opers); who is for the log.
func (s *Server) mdClearAll(t mdTarget, who string) int {
	keys := sortedMeta(t.meta())
	for _, k := range keys {
		s.mdApply(nil, nil, t, k, "", true)
	}
	if len(keys) > 0 {
		s.sno('a', "%s cleared the metadata of %s", who, t.name())
	}
	return len(keys)
}

func (s *Server) mdSub(c *Client, keys []string) {
	if len(keys) == 0 {
		s.reply(c, irc.ErrNeedMoreParams, "METADATA", "Not enough parameters")
		return
	}
	var ok []string
	for _, k := range keys {
		if !mdValidKey(k) {
			s.mdFail(c, "KEY_INVALID", k, "Invalid key")
			continue
		}
		if c.mdSubs[k] {
			continue
		}
		if len(c.mdSubs) >= mdMaxSubs {
			s.mdFail(c, "TOO_MANY_SUBS", k, "Too many subscriptions")
			break
		}
		if c.mdSubs == nil {
			c.mdSubs = map[string]bool{}
		}
		c.mdSubs[k] = true
		ok = append(ok, k)
	}
	if len(ok) > 0 {
		s.replyBare(c, "770", ok...)
	}
}

func (s *Server) mdUnsub(c *Client, keys []string) {
	if len(keys) == 0 {
		s.reply(c, irc.ErrNeedMoreParams, "METADATA", "Not enough parameters")
		return
	}
	var ok []string
	for _, k := range keys {
		if !mdValidKey(k) {
			s.mdFail(c, "KEY_INVALID", k, "Invalid key")
			continue
		}
		if c.mdSubs[k] {
			delete(c.mdSubs, k)
			ok = append(ok, k)
		}
	}
	if len(ok) > 0 {
		s.replyBare(c, "771", ok...)
	}
}

func (s *Server) mdSubs(c *Client) {
	keys := make([]string, 0, len(c.mdSubs))
	for k := range c.mdSubs {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	var lines []string
	for len(keys) > 0 {
		n := min(len(keys), 10)
		lines = append(lines, irc.Message{Source: s.cfg.Server.Name, Command: "772", Params: append([]string{c.target()}, keys[:n]...)}.String())
		keys = keys[n:]
	}
	s.mdBatch(c, "metadata-subs", "", lines)
}

// mdApply changes key of t to value ("" deletes it): saves it when it
// belongs to an account or registered channel (save), tells the
// subscribers here (but not skip, who got a reply) and the BeeIRCd
// servers (but not from).
func (s *Server) mdApply(from *linkConn, skip *Client, t mdTarget, key, value string, save bool) {
	if old, ok := t.meta()[key]; ok == (value != "") && old == value {
		return
	}
	t.set(key, value)
	if save {
		s.mdSave(t, key, value)
	}
	s.mdPush(skip, t, key, value)
	s.linkMetadata(from, t, key, value)
}

// mdSave keeps a change on the account or registered channel, on the
// server that runs services.
func (s *Server) mdSave(t mdTarget, key, value string) {
	if s.svc.Off() || s.svc.ReadOnly() {
		return
	}
	var err error
	if t.ch != nil {
		rc, e := s.store.ChannelByName(t.ch.Name)
		if e != nil || rc == nil {
			return
		}
		err = s.store.SetChannelMetadata(rc.ID, key, value)
	} else {
		if t.u.Account == "" {
			return
		}
		a, e := s.store.AccountByNick(t.u.Account)
		if e != nil || a == nil {
			return
		}
		err = s.store.SetAccountMetadata(a.ID, key, value)
	}
	if err != nil {
		s.log.Printf("services database: metadata: %v", err)
	}
}

// mdPush tells the local subscribers of key that it changed on t.
func (s *Server) mdPush(skip *Client, t mdTarget, key, value string) {
	line := s.mdLine(t.name(), key, value)
	sent := map[*Client]bool{}
	to := func(c *Client) {
		if c == nil || c == skip || sent[c] || !c.registered || c.user == nil || !c.caps[metadataCap] || !c.mdSubs[key] {
			return
		}
		sent[c] = true
		c.send(line)
	}
	if t.ch != nil {
		for uid := range t.ch.Members {
			to(s.clients[uid])
		}
		return
	}
	to(s.clients[t.u.UID])
	for _, n := range s.net.Neighbours(t.u) {
		to(s.clients[n.UID])
	}
	for c := range s.watchers[irc.Fold(t.u.Nick)] {
		to(c)
	}
}

// linkMetadata passes a change on to the BeeIRCd servers (all but from).
func (s *Server) linkMetadata(from *linkConn, t mdTarget, key, value string) {
	args := []string{t.linkID(), key}
	if value != "" {
		args = append(args, value)
	}
	it := &link.Bee{Source: s.cfg.Server.SID, Sub: "METADATA", Args: args}
	for _, o := range s.upLinks(from) {
		for _, line := range o.codecOf().Encode(it) {
			o.send(line)
		}
	}
}

// remoteMetadata is BEE METADATA <uid|#channel> <key> [value].
func (s *Server) remoteMetadata(l *linkConn, it *link.Bee) {
	if len(it.Args) < 2 || !mdValidKey(it.Args[1]) {
		return
	}
	var t mdTarget
	if strings.HasPrefix(it.Args[0], "#") {
		if t.ch = s.net.Channel(it.Args[0]); t.ch == nil {
			return
		}
	} else if t.u = s.net.UserByUID(it.Args[0]); t.u == nil {
		return
	}
	value := ""
	if len(it.Args) > 2 {
		value = it.Args[2]
	}
	if len(value) > mdMaxValue {
		return
	}
	s.mdApply(l, nil, t, it.Args[1], value, true)
}

// metadataBurst are the BEE METADATA lines of a burst: every user's and
// channel's keys.
func (s *Server) metadataBurst(t mdTarget) []*link.Bee {
	var out []*link.Bee
	meta := t.meta()
	for _, k := range sortedMeta(meta) {
		out = append(out, &link.Bee{Source: s.cfg.Server.SID, Sub: "METADATA", Args: []string{t.linkID(), k, meta[k]}})
	}
	return out
}

// metadataPrepare notes the account a Login leaves, which is gone once
// it is applied, and returns what moves the user's keys afterwards.
func (s *Server) metadataPrepare(ev state.Event) func() {
	e, ok := ev.(*state.Login)
	if !ok {
		return nil
	}
	prev := ""
	if u := s.net.UserByUID(e.UID); u != nil {
		prev = u.Account
	}
	return func() { s.mdLogin(e.UID, prev) }
}

// metadataAfter keeps metadata in step with what ev did: a channel's keys
// are loaded on its first join (logins go through metadataPrepare).
func (s *Server) metadataAfter(ev state.Event) {
	switch e := ev.(type) {
	case *state.UserAdd:
		// Logged in with SASL before registering: no Login event comes.
		// Not for a server's burst: those users bring their own keys.
		r := s.links.servers[uidServer(e.User.UID)]
		if e.User.Account != "" && !e.User.Service && (s.ours(e.User.UID) || r != nil && r.synced) {
			s.mdLogin(e.User.UID, "")
		}
	case *state.Join:
		ch := s.net.Channel(e.Channel)
		u := s.net.UserByUID(e.UID)
		if ch == nil || u == nil {
			return
		}
		if ch.Metadata == nil {
			ch.Metadata = map[string]string{}
			if !s.svc.Off() {
				if rc, err := s.store.ChannelByName(ch.Name); err == nil && rc != nil {
					t, stored := mdTarget{ch: ch}, s.store.ChannelMetadata(rc.ID)
					for _, k := range sortedMeta(stored) {
						s.mdApply(nil, s.clients[u.UID], t, k, stored[k], false)
					}
				}
			}
		}
		if e.Burst {
			return
		}
		if c := s.clients[u.UID]; c != nil && c.caps[metadataCap] && len(c.mdSubs) > 0 {
			s.mdBatch(c, "metadata", ch.Name, s.mdSyncLines(c, mdTarget{ch: ch}))
		}
		if len(u.Metadata) == 0 {
			return
		}
		for uid := range ch.Members {
			c := s.clients[uid]
			if c == nil || c.user == u || !c.caps[metadataCap] || len(c.mdSubs) == 0 || sharedChannels(c.user, u) > 1 || s.watching(c, u) {
				continue // they knew u already
			}
			for _, line := range s.mdLinesFor(c, mdTarget{u: u}) {
				c.send(line)
			}
		}
	}
}

// mdLogin moves a user's keys when their account changes from prev: an
// account's keys come back on login and go again on logout.
func (s *Server) mdLogin(uid, prev string) {
	u := s.net.UserByUID(uid)
	if u == nil || u.Account == prev || s.svc.Off() {
		return
	}
	t := mdTarget{u: u}
	clear := func() {
		for _, k := range sortedMeta(u.Metadata) {
			s.mdApply(nil, nil, t, k, "", false)
		}
	}
	if u.Account == "" {
		clear() // logged out: the keys were the account's
		return
	}
	a, err := s.store.AccountByNick(u.Account)
	if err != nil || a == nil {
		return
	}
	stored := s.store.AccountMetadata(a.ID)
	if len(stored) == 0 {
		if prev != "" {
			clear() // switched accounts: the keys are the old account's
			return
		}
		// Nothing kept yet: what they set before logging in is kept now.
		if s.svc.ReadOnly() {
			return
		}
		for _, k := range sortedMeta(u.Metadata) {
			if err := s.store.SetAccountMetadata(a.ID, k, u.Metadata[k]); err != nil {
				s.log.Printf("services database: metadata: %v", err)
			}
		}
		return
	}
	for _, k := range sortedMeta(u.Metadata) {
		if _, ok := stored[k]; !ok {
			s.mdApply(nil, nil, t, k, "", false)
		}
	}
	for _, k := range sortedMeta(stored) {
		s.mdApply(nil, nil, t, k, stored[k], false)
	}
}

func sharedChannels(a, b *state.User) int {
	n := 0
	for key := range a.Channels {
		if _, ok := b.Channels[key]; ok {
			n++
		}
	}
	return n
}

func (s *Server) watching(c *Client, u *state.User) bool {
	return s.watchers[irc.Fold(u.Nick)][c]
}

// metadataWelcome is the batch of c's own keys a metadata client gets on
// registering (empty when none).
func (s *Server) metadataWelcome(c *Client) {
	if !c.caps[metadataCap] {
		return
	}
	t := mdTarget{u: c.user}
	var lines []string
	for _, k := range sortedMeta(t.meta()) {
		lines = append(lines, s.mdKeyLine(c, t, k))
	}
	s.mdBatch(c, "metadata", c.user.Nick, lines)
}

// ---------- Hive Panel ----------

// panelMyMeta sets (or with value "" removes) key on po's own account and
// on everyone logged in to it now, with the same checks as METADATA SET.
func (s *Server) panelMyMeta(po *control.PanelOper, ip, key, value string) *control.Response {
	a, err := s.store.AccountByNick(po.Account)
	if err != nil || a == nil {
		return &control.Response{Error: "Your account no longer exists.", Denied: "signin"}
	}
	if !s.cfg.Server.IRCv3Drafts {
		return &control.Response{Error: "Metadata is off on this server (ircv3_drafts)."}
	}
	if !slices.Contains(mdKeys, key) {
		return &control.Response{Error: "That key isn't one this server takes (" + strings.Join(mdKeys, ", ") + ")."}
	}
	if s.svc.ReadOnly() {
		return &control.Response{Error: services.ReadonlyRefusal}
	}
	value = strings.TrimSpace(value)
	kept := s.store.AccountMetadata(a.ID)
	if value != "" {
		if why := s.mdCheck(key, value); why != "" {
			return &control.Response{Error: why + "."}
		}
		if key == "status" || key == "display-name" {
			if r := s.svc.SpamSet().First(spamfilter.Away, value); r != nil && spamfilter.Known(r.Action) {
				return &control.Response{Error: "A spam filter of this network does not allow that text."}
			}
		}
		if _, ok := kept[key]; !ok && len(kept) >= mdMaxKeys {
			return &control.Response{Error: "You have the most keys you may have."}
		}
	} else if _, ok := kept[key]; !ok {
		return &control.Response{Done: &control.Done{Text: "Your " + mdLabel(key) + " is not set."}}
	}
	if err := s.store.SetAccountMetadata(a.ID, key, value); err != nil {
		s.log.Printf("services database: metadata: %v", err)
		return &control.Response{Error: "Something went wrong on the server."}
	}
	for _, u := range s.net.Users() {
		if u.Account != "" && irc.Fold(u.Account) == irc.Fold(a.Name) {
			s.mdApply(nil, nil, mdTarget{u: u}, key, value, false)
		}
	}
	if value == "" {
		s.panelEvent(po.Account, ip, "removed their "+mdLabel(key))
		return &control.Response{Done: &control.Done{Text: "Your " + mdLabel(key) + " is removed."}}
	}
	s.panelEvent(po.Account, ip, "set their "+mdLabel(key))
	return &control.Response{Done: &control.Done{Text: "Your " + mdLabel(key) + " is saved."}}
}

// panelMetaClear deletes all metadata of a person (uid) or a channel
// (name) for an oper; it is logged like METADATA CLEAR.
func (s *Server) panelMetaClear(po *control.PanelOper, ip, uid, name string) control.Response {
	if s.svc.ReadOnly() {
		return control.Response{Error: services.ReadonlyRefusal}
	}
	who := po.Account + " (Hive Panel)"
	n, what := 0, ""
	if uid != "" {
		u, problem := s.panelTarget(uid)
		if problem != "" {
			return control.Response{Error: problem}
		}
		n, what = s.mdClearAll(mdTarget{u: u}, who), u.Nick
	} else {
		ch := s.net.Channel(name)
		if ch != nil {
			n, what = s.mdClearAll(mdTarget{ch: ch}, who), ch.Name
		}
		if !s.svc.Off() { // a registered channel no one is in keeps its keys too
			if rc, _ := s.store.ChannelByName(name); rc != nil {
				if kept := len(s.store.ChannelMetadata(rc.ID)); kept > 0 {
					if err := s.store.ClearChannelMetadata(rc.ID); err != nil {
						s.log.Printf("services database: metadata: %v", err)
						return control.Response{Error: "Something went wrong on the server."}
					}
					if n == 0 {
						s.sno('a', "%s cleared the metadata of %s", who, rc.Name)
					}
					n = max(n, kept)
				}
				what = rc.Name
			}
		}
		if what == "" {
			return control.Response{Error: "No such channel."}
		}
	}
	if n == 0 {
		return control.Response{Done: &control.Done{Text: what + " has no metadata."}}
	}
	s.panelEvent(po.Account, ip, "cleared the metadata of "+what)
	return control.Response{Done: &control.Done{Text: "The metadata of " + what + " is cleared."}}
}

// panelChanMeta is a channel's keys for the panel: live, or kept by
// ChanServ when no one is in it.
func (s *Server) panelChanMeta(ch *state.Channel, name string) map[string]string {
	if ch != nil && ch.Metadata != nil {
		return maps.Clone(ch.Metadata)
	}
	if s.svc.Off() {
		return nil
	}
	if rc, _ := s.store.ChannelByName(name); rc != nil {
		return s.store.ChannelMetadata(rc.ID)
	}
	return nil
}
