package services

import (
	"strconv"
	"strings"
	"time"

	"github.com/fhazal/beeircd/internal/irc"
	"github.com/fhazal/beeircd/internal/state"
	"github.com/fhazal/beeircd/internal/store"
)

// BotServ kickers and BADWORDS (Anope 2.0.20 bs_kick, bs_badwords). The
// server hands every plain channel PRIVMSG to ChannelText; only channels
// in s.bsWatch (a bot and a kicker on) cost more than one map lookup.

// Anope's botserv settings.
const (
	badwordsMax  = 32               // botserv:badwordsmax
	kickKeepData = 10 * time.Minute // bs_kick:keepdata
	kickPurge    = 5 * time.Minute  // BanDataPurger
)

// kickerNames are the KICK options, in store order.
var kickerNames = [store.KickerCount]string{"BOLDS", "COLORS", "REVERSES", "UNDERLINES", "BADWORDS", "CAPS", "FLOOD", "REPEAT", "ITALICS", "AMSG"}

// kickerWords are the names in Anope's replies ("Bot will now kick for bolds").
var kickerWords = [store.KickerCount]string{"bolds", "colors", "reverses", "underlines", "badwords", "caps", "flood", "repeats", "italics", "AMSG"}

// kickerHelp is the one-line help of each kicker.
var kickerHelp = [store.KickerCount]string{
	"KICK BOLDS <#channel> {ON|OFF} [ttb]   kick for bold text",
	"KICK COLORS <#channel> {ON|OFF} [ttb]   kick for colours",
	"KICK REVERSES <#channel> {ON|OFF} [ttb]   kick for reverse text",
	"KICK UNDERLINES <#channel> {ON|OFF} [ttb]   kick for underlined text",
	"KICK BADWORDS <#channel> {ON|OFF} [ttb]   kick for words on the BADWORDS list",
	"KICK CAPS <#channel> {ON|OFF} [ttb [min [percent]]]   kick for caps: at least\n    min capitals (10) making up percent (25) of the letters",
	"KICK FLOOD <#channel> {ON|OFF} [ttb [lines [seconds]]]   kick for lines (6) in\n    seconds (10)",
	"KICK REPEAT <#channel> {ON|OFF} [ttb [times]]   kick for the same line said\n    times (3) more",
	"KICK ITALICS <#channel> {ON|OFF} [ttb]   kick for italics",
	"KICK AMSG <#channel> {ON|OFF} [ttb]   kick for the same line sent to several channels",
}

// kickRun is what the kickers remember about one channel (Anope's BanData
// and UserData), kept for kickKeepData after last use.
type kickRun struct {
	bans  map[string]*kickBan  // by nick!user@host
	users map[string]*kickUser // by UID
}

type kickBan struct {
	lastUse time.Time
	kicks   [store.KickerCount]int
}

type kickUser struct {
	lastUse   time.Time
	lines     int // flood kicker
	lastStart time.Time
	times     int // repeat kicker
	lastLine  string
}

// amsgLast is the last channel line of one person, for the AMSG kicker.
type amsgLast struct {
	line    string
	channel string // folded
	at      time.Time
}

// bsWatchUpdate decides whether reg's messages go to BotServ: a bot, and
// a kicker or FANTASY on.
func (s *Services) bsWatchUpdate(reg *store.Channel) {
	s.bsWatchOne(reg)
	s.bsAmsgRecount()
}

func (s *Services) bsWatchOne(reg *store.Channel) {
	if s.bsWatch == nil {
		s.bsWatch = map[string]bool{}
	}
	key := irc.Fold(reg.Name)
	cb := s.store.ChanBot(reg)
	if cb.BotID != 0 && (cb.Fantasy || s.store.ChanKickers(reg).Any()) {
		s.bsWatch[key] = true
	} else {
		delete(s.bsWatch, key)
		delete(s.kickRuns, key)
	}
}

// bsAmsgRecount notes whether any watched channel has the AMSG kicker on:
// then every channel line is remembered, so a line sent first to a
// channel without a bot still counts.
func (s *Services) bsAmsgRecount() {
	s.bsAmsgOn = false
	for key := range s.bsWatch {
		if reg, err := s.store.ChannelByName(key); err == nil && reg != nil && s.store.ChanKickers(reg).On[store.KickAmsgs] {
			s.bsAmsgOn = true
			return
		}
	}
	s.amsg = nil
}

// bsWatchAll rebuilds the watch list from the store.
func (s *Services) bsWatchAll() {
	s.bsWatch = map[string]bool{}
	for _, a := range s.store.BotAssignments() {
		if reg, err := s.store.ChannelByName(a.Name); err == nil && reg != nil {
			s.bsWatchOne(reg)
		}
	}
	s.bsAmsgRecount()
}

// ChannelText runs the BotServ kickers and fantasy commands on a PRIVMSG
// uid sent to ch.
func (s *Services) ChannelText(uid string, ch *state.Channel, text string) {
	if len(s.bsWatch) == 0 {
		return
	}
	key := irc.Fold(ch.Name)
	if !s.bsWatch[key] {
		if s.bsAmsgOn {
			s.amsgNote(uid, key, actionText(text), time.Now())
		}
		return
	}
	c := s.clients[uid]
	if c == nil || c.user == nil || c.user.Service {
		return
	}
	reg, err := s.store.ChannelByName(ch.Name)
	if err != nil || reg == nil {
		return
	}
	bot, cb := s.chanBot(reg)
	if bot == nil || ch.Member(bot.user) == nil {
		return
	}
	if k := s.store.ChanKickers(reg); k.Any() {
		s.bsKickers(c, ch, reg, bot, cb, k, key, text)
	}
	if cb.Fantasy && ch.Member(c.user) != nil {
		s.bsFantasy(c, ch, reg, bot, text)
	}
	s.Flush()
}

func (s *Services) bsKickers(c *Client, ch *state.Channel, reg *store.Channel, bot *service, cb store.ChanBot, k store.Kickers, key, text string) {
	u := c.user
	now := time.Now()
	s.kickPurgeOld(now)
	if !s.bsMayKick(reg, cb, ch, u) {
		return
	}

	text = actionText(text)
	if text == "" {
		return
	}
	kick := func(kind int, reason string) {
		s.bsKick(reg, cb, k, ch, bot, u, kind, reason, now)
	}
	codes := []struct {
		kind int
		code string
		why  string
	}{
		{store.KickBolds, "\x02", "Don't use bolds on this channel!"},
		{store.KickColors, "\x03", "Don't use colors on this channel!"},
		{store.KickReverses, "\x16", "Don't use reverses on this channel!"},
		{store.KickItalics, "\x1d", "Don't use italics on this channel!"},
		{store.KickUnderlines, "\x1f", "Don't use underlines on this channel!"},
	}
	for _, x := range codes {
		if k.On[x.kind] && strings.Contains(text, x.code) {
			kick(x.kind, x.why)
			return
		}
	}
	if k.On[store.KickCaps] && len(text) >= k.CapsMin {
		up, low := 0, 0
		for i := 0; i < len(text); i++ {
			switch b := text[i]; {
			case b >= 'A' && b <= 'Z':
				up++
			case b >= 'a' && b <= 'z':
				low++
			}
		}
		if up+low > 0 && up >= k.CapsMin && up*100/(up+low) >= k.CapsPercent {
			kick(store.KickCaps, "Turn caps lock OFF!")
			return
		}
	}
	if k.On[store.KickBadwords] {
		if bufHasBadWord(normalizeBuffer(text), s.store.BadWords(reg)) {
			kick(store.KickBadwords, "Watch your language!") // gentlebadwordreason = yes
			return
		}
	}

	run := s.kickRun(key)
	ud := run.users[u.UID]
	if ud == nil {
		ud = &kickUser{lastStart: now}
		run.users[u.UID] = ud
	}
	ud.lastUse = now
	if k.On[store.KickFlood] {
		if now.Sub(ud.lastStart) > time.Duration(k.FloodSecs)*time.Second {
			ud.lastStart, ud.lines = now, 0
		}
		ud.lines++
		if ud.lines >= k.FloodLines {
			delete(run.users, u.UID)
			kick(store.KickFlood, "Stop flooding!")
			return
		}
	}
	if k.On[store.KickRepeat] {
		if strings.EqualFold(ud.lastLine, text) {
			ud.times++
		} else {
			ud.times = 0
		}
		if ud.times >= k.RepeatTimes {
			delete(run.users, u.UID)
			kick(store.KickRepeat, "Stop repeating yourself!")
			return
		}
	}
	ud.lastLine = text

	// AMSG: the same line just went to another channel. Anope 2.0 keeps
	// the last target per channel, so its check never fires; this keeps it
	// per person, and kicks in every channel of theirs with the kicker on.
	if !s.bsAmsgOn {
		return
	}
	last := s.amsgNote(u.UID, key, text, now)
	if last.line == "" || last.channel == key || !strings.EqualFold(last.line, text) || now.Sub(last.at) > kickKeepData {
		return
	}
	for _, joined := range u.Channels {
		ch2 := s.net.Channel(joined.Name)
		if ch2 == nil || !s.bsWatch[irc.Fold(ch2.Name)] {
			continue
		}
		reg2, err := s.store.ChannelByName(ch2.Name)
		if err != nil || reg2 == nil {
			continue
		}
		bot2, cb2 := s.chanBot(reg2)
		k2 := s.store.ChanKickers(reg2)
		if bot2 == nil || ch2.Member(bot2.user) == nil || !k2.On[store.KickAmsgs] || !s.bsMayKick(reg2, cb2, ch2, u) {
			continue
		}
		s.bsKick(reg2, cb2, k2, ch2, bot2, u, store.KickAmsgs, "Don't use AMSGs!", now)
	}
}

// actionText is a /me's text, or text as it is: only the text counts.
func actionText(text string) string {
	if len(text) > 8 && strings.EqualFold(text[:8], "\x01ACTION ") && text[len(text)-1] == '\x01' {
		return text[8 : len(text)-1]
	}
	return text
}

// amsgNote remembers uid's channel line and returns the one before.
func (s *Services) amsgNote(uid, key, text string, now time.Time) amsgLast {
	if s.amsg == nil {
		s.amsg = map[string]*amsgLast{}
	}
	a := s.amsg[uid]
	if a == nil {
		a = &amsgLast{}
		s.amsg[uid] = a
	}
	prev := *a
	a.line, a.channel, a.at = text, key, now
	return prev
}

// bsMayKick reports whether the kickers may act on u in ch: not with the
// NOKICK privilege, not an op or voice when DONTKICKOPS / DONTKICKVOICES
// say so, and never someone protected (a service, user mode +q).
func (s *Services) bsMayKick(reg *store.Channel, cb store.ChanBot, ch *state.Channel, u *state.User) bool {
	if u.Service || u.HasMode('q') || s.userHas(reg, u, "NOKICK") {
		return false
	}
	m := ch.Member(u)
	if m == nil {
		return false
	}
	if cb.DontKickOps && m.AtLeast(state.RankHalfop) {
		return false
	}
	if cb.DontKickVoices && m.Has(state.RankVoice) {
		return false
	}
	return true
}

// bsKick is Anope's check_ban + bot_kick: count the kick, ban after TTB
// kicks (lifted after BANEXPIRE), then kick.
func (s *Services) bsKick(reg *store.Channel, cb store.ChanBot, k store.Kickers, ch *state.Channel, bot *service, u *state.User, kind int, reason string, now time.Time) {
	if ch.Member(u) == nil {
		return
	}
	run := s.kickRun(irc.Fold(ch.Name))
	mask := u.Nick + "!" + u.Ident + "@" + u.Host
	bd := run.bans[mask]
	if bd == nil {
		bd = &kickBan{}
		run.bans[mask] = bd
	}
	bd.lastUse = now
	bd.kicks[kind]++
	if k.TTB[kind] > 0 && bd.kicks[kind] >= k.TTB[kind] {
		bd.kicks[kind] = 0
		ban := banMask(reg, u)
		if cb.BanExpire > 0 {
			minutes := min(int((cb.BanExpire+time.Minute-1)/time.Minute), irc.MaxTimedBan)
			ban = "~t:" + strconv.Itoa(minutes) + ":" + ban
		}
		s.svcMode(bot, ch, []modeChange{{true, 'b', ban}})
	}
	s.svcKick(bot, ch, u, reason)
}

func (s *Services) kickRun(key string) *kickRun {
	if s.kickRuns == nil {
		s.kickRuns = map[string]*kickRun{}
	}
	r := s.kickRuns[key]
	if r == nil {
		r = &kickRun{bans: map[string]*kickBan{}, users: map[string]*kickUser{}}
		s.kickRuns[key] = r
	}
	return r
}

// kickPurgeOld forgets what wasn't used for kickKeepData, at most every
// kickPurge.
func (s *Services) kickPurgeOld(now time.Time) {
	if now.Sub(s.kickPurged) < kickPurge {
		return
	}
	s.kickPurged = now
	for key, r := range s.kickRuns {
		for m, b := range r.bans {
			if now.Sub(b.lastUse) > kickKeepData {
				delete(r.bans, m)
			}
		}
		for uid, ud := range r.users {
			if now.Sub(ud.lastUse) > kickKeepData || s.net.UserByUID(uid) == nil {
				delete(r.users, uid)
			}
		}
		if len(r.bans) == 0 && len(r.users) == 0 {
			delete(s.kickRuns, key)
		}
	}
	for uid, a := range s.amsg {
		if now.Sub(a.at) > kickKeepData || s.net.UserByUID(uid) == nil {
			delete(s.amsg, uid)
		}
	}
}

// bsForget drops what the kickers remember of uid in channel (all
// channels when channel is "").
func (s *Services) bsForget(uid, channel string) {
	if len(s.kickRuns) == 0 && len(s.amsg) == 0 {
		return
	}
	if channel == "" {
		for _, r := range s.kickRuns {
			delete(r.users, uid)
		}
		delete(s.amsg, uid)
		return
	}
	if r := s.kickRuns[irc.Fold(channel)]; r != nil {
		delete(r.users, uid)
	}
}

// normalizeBuffer is Anope's NormalizeBuffer: the text without bold,
// colour (with its numbers), reverse, underline, italics and the like.
func normalizeBuffer(t string) string {
	if strings.IndexFunc(t, func(r rune) bool { return r < ' ' }) < 0 {
		return t
	}
	var b strings.Builder
	for i := 0; i < len(t); i++ {
		switch t[i] {
		case '\x03':
			// up to two digits, then ",<up to two digits>"
			j := i + 1
			for n := 0; n < 2 && j < len(t) && t[j] >= '0' && t[j] <= '9'; n++ {
				j++
			}
			if j > i+1 && j+1 < len(t) && t[j] == ',' && t[j+1] >= '0' && t[j+1] <= '9' {
				j++
				for n := 0; n < 2 && j < len(t) && t[j] >= '0' && t[j] <= '9'; n++ {
					j++
				}
			}
			i = j - 1
		case '\x02', '\x0f', '\x16', '\x1d', '\x1e', '\x1f', '\x11', '\x01':
		default:
			b.WriteByte(t[i])
		}
	}
	return b.String()
}

// bufHasBadWord is the badwords check of Anope's bs_kick (not case
// sensitive: botserv:casesensitive is off).
func bufHasBadWord(buf string, words []store.BadWord) bool {
	if buf == "" {
		return false
	}
	lb := strings.ToLower(buf)
	for _, bw := range words {
		w := strings.ToLower(bw.Word)
		if w == "" || len(w) > len(lb) {
			continue
		}
		switch bw.Type {
		case store.BadWordAny:
			if strings.Contains(lb, w) {
				return true
			}
		case store.BadWordSingle:
			if lb == w || strings.HasPrefix(lb, w+" ") || strings.HasSuffix(lb, " "+w) || strings.Contains(lb, " "+w+" ") {
				return true
			}
		case store.BadWordStart:
			if strings.HasPrefix(lb, w) || strings.Contains(lb, " "+w) {
				return true
			}
		case store.BadWordEnd:
			if strings.HasSuffix(lb, w) || strings.Contains(lb, w+" ") {
				return true
			}
		}
	}
	return false
}

// ---- KICK ----

func bsKickCmd(s *Services, c *Client, args []string) {
	syntax := func() {
		s.bs(c, "Syntax: KICK <option> <#channel> {ON|OFF} [settings]   (see /bs help kick)")
	}
	if len(args) < 3 {
		syntax()
		return
	}
	// Anope 2.0 is KICK <option> <#channel>; 1.8 had the channel first.
	opt, name := strings.ToUpper(args[0]), args[1]
	if irc.IsChannel(args[0]) && !irc.IsChannel(args[1]) {
		opt, name = strings.ToUpper(args[1]), args[0]
	}
	kind := -1
	for i, n := range kickerNames {
		if n == opt || opt == "AMSGS" && n == "AMSG" {
			kind = i
		}
	}
	if kind < 0 {
		syntax()
		return
	}
	c.cmdName = "KICK " + kickerNames[kind]
	val := strings.ToUpper(args[2])
	rest := args[3:]
	reg := s.bsReg(c, name)
	if reg == nil {
		return
	}
	if val != "ON" && val != "OFF" {
		s.bs(c, "Syntax: %s", strings.SplitN(kickerHelp[kind], "   ", 2)[0])
		return
	}
	has := s.bsChanPriv(c, reg, "SET")
	if !has && !bsAdmin(c) {
		s.bs(c, msgAccessDenied)
		return
	}
	if cb := s.store.ChanBot(reg); cb.BotID == 0 {
		s.bs(c, msgBotNotAssign)
		return
	}
	k := s.store.ChanKickers(reg)
	word := kickerWords[kind]
	if val == "OFF" {
		k.On[kind] = false
		if err := s.store.SetChanKickers(reg, k); err != nil {
			s.dbErr(c, s.botServ, err)
			return
		}
		s.bsWatchUpdate(reg)
		s.bsLog(c, !has, reg.Name, "to disable the %s kicker", word)
		s.bs(c, "Bot won't kick for %s anymore.", word)
		return
	}
	ttb := 0
	if t := argAt(rest, 0); t != "" {
		n, err := strconv.Atoi(t)
		if err != nil || n < 0 || n > 32767 {
			s.bs(c, "%s cannot be taken as times to ban.", t)
			return
		}
		ttb = n
	}
	num := func(i, def, lo, hi int) int {
		n, err := strconv.Atoi(argAt(rest, i))
		if err != nil || n < lo || n > hi {
			return def
		}
		return n
	}
	k.On[kind], k.TTB[kind] = true, ttb
	switch kind {
	case store.KickCaps:
		k.CapsMin, k.CapsPercent = num(1, 10, 1, 32767), num(2, 25, 1, 100)
	case store.KickFlood:
		k.FloodLines, k.FloodSecs = num(1, 6, 2, 32767), num(2, 10, 1, int(kickKeepData/time.Second))
	case store.KickRepeat:
		k.RepeatTimes = num(1, 3, 1, 32767)
	}
	if err := s.store.SetChanKickers(reg, k); err != nil {
		s.dbErr(c, s.botServ, err)
		return
	}
	s.bsWatchUpdate(reg)
	s.bsLog(c, !has, reg.Name, "to enable the %s kicker", word)
	ban := ""
	if ttb > 0 {
		ban = ", and will place a ban after " + strconv.Itoa(ttb) + " kicks for the same user"
	}
	switch kind {
	case store.KickCaps:
		s.bs(c, "Bot will now kick for caps (they must constitute at least %d characters and %d%% of the entire message)%s.", k.CapsMin, k.CapsPercent, ban)
	case store.KickFlood:
		s.bs(c, "Bot will now kick for flood (%d lines in %d seconds)%s.", k.FloodLines, k.FloodSecs, ban)
	case store.KickRepeat:
		times := "times"
		if k.RepeatTimes == 1 {
			times = "time"
		}
		s.bs(c, "Bot will now kick for repeats (users that repeat the same message %d %s)%s.", k.RepeatTimes, times, ban)
	default:
		s.bs(c, "Bot will now kick for %s%s.", word, ban)
	}
}

// kickHelp is the long help of KICK.
func kickHelp() string {
	var b strings.Builder
	b.WriteString("Kickers make the channel's bot kick people for what they say. ttb = kicks\nbefore the bot also bans them (0 = never); BANEXPIRE lifts those bans.\n")
	for _, h := range kickerHelp {
		b.WriteString(h + "\n")
	}
	b.WriteString("Needs the SET privilege. People with NOKICK are never kicked; see also\nSET DONTKICKOPS and SET DONTKICKVOICES.")
	return b.String()
}

// bsKickInfo adds the kicker lines to INFO #channel.
func (s *Services) bsKickInfo(c *Client, reg *store.Channel) {
	k := s.store.ChanKickers(reg)
	labels := [store.KickerCount]string{"Bolds kicker", "Colors kicker", "Reverses kicker", "Underlines kicker", "Bad words kicker", "Caps kicker", "Flood kicker", "Repeat kicker", "Italics kicker", "AMSG kicker"}
	order := []int{store.KickBadwords, store.KickBolds, store.KickCaps, store.KickColors, store.KickFlood, store.KickRepeat, store.KickReverses, store.KickUnderlines, store.KickItalics, store.KickAmsgs}
	for _, i := range order {
		v := "Disabled"
		if k.On[i] {
			var extra []string
			if k.TTB[i] > 0 {
				extra = append(extra, strconv.Itoa(k.TTB[i])+" kick(s) to ban")
			}
			switch i {
			case store.KickCaps:
				extra = append(extra, "minimum "+strconv.Itoa(k.CapsMin)+"/"+strconv.Itoa(k.CapsPercent)+"%")
			case store.KickFlood:
				extra = append(extra, strconv.Itoa(k.FloodLines)+" lines in "+strconv.Itoa(k.FloodSecs)+"s")
			case store.KickRepeat:
				extra = append(extra, strconv.Itoa(k.RepeatTimes)+" times")
			}
			v = "Enabled"
			if len(extra) > 0 {
				v += " (" + strings.Join(extra, "; ") + ")"
			}
		}
		s.bs(c, "%18s %s", labels[i]+":", v)
	}
}

// ---- BADWORDS ----

func bsBadwords(s *Services, c *Client, args []string) {
	if len(args) < 2 {
		s.bs(c, "Syntax: BADWORDS <#channel> {ADD | DEL | LIST | CLEAR} [word | entry-num | list]")
		return
	}
	cmd := strings.ToUpper(args[1])
	word := strings.Join(args[2:], " ")
	if word == "" && cmd != "LIST" && cmd != "CLEAR" {
		s.bs(c, "Syntax: BADWORDS <#channel> %s <word>", cmd)
		return
	}
	reg := s.bsReg(c, args[0])
	if reg == nil {
		return
	}
	has := s.bsChanPriv(c, reg, "BADWORDS")
	if !has && !bsAdmin(c) {
		s.bs(c, msgAccessDenied)
		return
	}
	c.cmdName = "BADWORDS"
	list := s.store.BadWords(reg)
	typeWord := func(t int) string {
		switch t {
		case store.BadWordSingle:
			return "(SINGLE)"
		case store.BadWordStart:
			return "(START)"
		case store.BadWordEnd:
			return "(END)"
		}
		return ""
	}
	switch cmd {
	case "ADD":
		typ, real := store.BadWordAny, word
		if i := strings.LastIndexByte(word, ' '); i >= 0 {
			switch strings.ToUpper(word[i+1:]) {
			case "SINGLE":
				typ = store.BadWordSingle
			case "START":
				typ = store.BadWordStart
			case "END":
				typ = store.BadWordEnd
			}
			real = word[:i]
		}
		real = irc.Truncate(strings.TrimSpace(real), 100)
		if real == "" {
			s.bs(c, "Syntax: BADWORDS <#channel> ADD <word> [SINGLE | START | END]")
			return
		}
		if len(list) >= badwordsMax {
			s.bs(c, "Sorry, you can only have %d bad words entries on a channel.", badwordsMax)
			return
		}
		for _, bw := range list {
			if strings.EqualFold(bw.Word, real) {
				s.bs(c, "%s already exists in %s bad words list.", bw.Word, reg.Name)
				return
			}
		}
		if err := s.store.AddBadWord(reg, real, typ); err != nil {
			s.dbErr(c, s.botServ, err)
			return
		}
		s.bsLog(c, !has, reg.Name, "ADD %s", real)
		s.bs(c, "%s added to %s bad words list.", real, reg.Name)
	case "DEL":
		if len(list) == 0 {
			s.bs(c, "%s bad words list is empty.", reg.Name)
			return
		}
		if nums, ok := numList(word, len(list)); ok && word[0] >= '0' && word[0] <= '9' {
			var ids []int64
			for _, n := range nums {
				if n >= 1 {
					ids = append(ids, list[n-1].ID)
					s.bsLog(c, !has, reg.Name, "DEL %s", list[n-1].Word)
				}
			}
			if len(ids) > 0 {
				if err := s.store.DeleteBadWords(reg, ids); err != nil {
					s.dbErr(c, s.botServ, err)
					return
				}
			}
			switch len(ids) {
			case 0:
				s.bs(c, "No matching entries on %s bad words list.", reg.Name)
			case 1:
				s.bs(c, "Deleted 1 entry from %s bad words list.", reg.Name)
			default:
				s.bs(c, "Deleted %d entries from %s bad words list.", len(ids), reg.Name)
			}
			return
		}
		for _, bw := range list {
			if strings.EqualFold(bw.Word, word) {
				if err := s.store.DeleteBadWords(reg, []int64{bw.ID}); err != nil {
					s.dbErr(c, s.botServ, err)
					return
				}
				s.bsLog(c, !has, reg.Name, "DEL %s", bw.Word)
				s.bs(c, "%s deleted from %s bad words list.", bw.Word, reg.Name)
				return
			}
		}
		s.bs(c, "%s not found on %s bad words list.", word, reg.Name)
	case "LIST":
		s.bsLog(c, !has, reg.Name, "LIST")
		if len(list) == 0 {
			s.bs(c, "%s bad words list is empty.", reg.Name)
			return
		}
		type row struct {
			n    int
			word string
			typ  string
		}
		var rows []row
		nums, isNums := numList(word, len(list))
		switch {
		case word != "" && isNums:
			for _, n := range nums {
				if n >= 1 {
					rows = append(rows, row{n, list[n-1].Word, typeWord(list[n-1].Type)})
				}
			}
		default:
			for i, bw := range list {
				if word == "" || irc.Match(word, bw.Word) {
					rows = append(rows, row{i + 1, bw.Word, typeWord(bw.Type)})
				}
			}
		}
		if len(rows) == 0 {
			s.bs(c, "No matching entries on %s bad words list.", reg.Name)
			return
		}
		w := len("Word")
		for _, r := range rows {
			w = max(w, len(r.word))
		}
		s.bs(c, "Bad words list for %s:", reg.Name)
		s.bs(c, "  %-6s  %-*s  %s", "Number", w, "Word", "Type")
		for _, r := range rows {
			s.bs(c, "  %-6d  %-*s  %s", r.n, w, r.word, r.typ)
		}
		s.bs(c, "End of bad words list.")
	case "CLEAR":
		if err := s.store.ClearBadWords(reg); err != nil {
			s.dbErr(c, s.botServ, err)
			return
		}
		s.bsLog(c, !has, reg.Name, "CLEAR")
		s.bs(c, "Bad words list is now empty.")
	default:
		s.bs(c, "Syntax: BADWORDS <#channel> {ADD | DEL | LIST | CLEAR} [word | entry-num | list]")
	}
}
