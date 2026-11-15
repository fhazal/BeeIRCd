package anopedb

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/fhazal/beeircd/internal/irc"
	"github.com/fhazal/beeircd/internal/passwd"
	"github.com/fhazal/beeircd/internal/store"
)

// Options changes how Import treats names that exist on both sides.
type Options struct {
	// Replace: an account or channel that BeeIRCd already has is replaced
	// by Anope's. Without it BeeIRCd's stays and Anope's is skipped.
	Replace bool
}

// Report says what Import did, in the order it shows it.
type Report struct {
	In      []Count  // what came in
	Changed []string // brought in, but not exactly as Anope had it
	Clashes []string // names on both sides
	Skipped []string // left out, with the reason
	Unknown []Count  // object types this import doesn't know
}

// Count is one line of the report: n things of a kind.
type Count struct {
	What string
	N    int
}

type importer struct {
	st          *store.Store
	opt         Options
	r           *Report
	in          map[string]int
	keys        []string
	acct        map[string]*store.Account // folded Anope display -> new account
	nick        map[string]*store.Account // folded Anope nick -> new account
	chans       map[string]*store.Channel
	bots        map[string]*store.Bot
	skippedAcct map[string]bool // folded display of accounts left out
	skippedChan map[string]bool
}

func (im *importer) count(what string, n int) {
	if n == 0 {
		return
	}
	if _, ok := im.in[what]; !ok {
		im.keys = append(im.keys, what)
	}
	im.in[what] += n
}

func (im *importer) skip(format string, a ...any) {
	im.r.Skipped = append(im.r.Skipped, fmt.Sprintf(format, a...))
}
func (im *importer) change(format string, a ...any) {
	im.r.Changed = append(im.r.Changed, fmt.Sprintf(format, a...))
}
func (im *importer) clash(format string, a ...any) {
	im.r.Clashes = append(im.r.Clashes, fmt.Sprintf(format, a...))
}

// serviceBots are the names Anope gives its own service bots; they come
// from Anope's config and BeeIRCd has its own.
var serviceBots = map[string]bool{"nickserv": true, "chanserv": true, "memoserv": true, "operserv": true,
	"hostserv": true, "botserv": true, "global": true, "helpserv": true, "statserv": true, "chanfix": true}

// known are the object types Import reads; anything else is counted as unknown.
var known = map[string]bool{"NickCore": true, "NickAlias": true, "BotInfo": true, "ChannelInfo": true,
	"ChanAccess": true, "AutoKick": true, "ModeLock": true, "EntryMsg": true, "LogSetting": true,
	"BadWord": true, "Memo": true, "AJoinEntry": true, "NSMiscData": true, "CSMiscData": true,
	"NSSuspendInfo": true, "CSSuspendInfo": true, "HostRequest": true, "XLine": true, "ForbidData": true,
	"Exception": true, "NewsItem": true, "OperInfo": true, "IgnoreData": true, "SeenInfo": true,
	"Oper": true, "Stats": true}

// Import brings objs (a parsed anope.db) into st. It is meant for a
// stopped server's database; the caller backs it up first.
func Import(st *store.Store, objs []Object, opt Options) (*Report, error) {
	im := &importer{st: st, opt: opt, r: &Report{}, in: map[string]int{},
		acct: map[string]*store.Account{}, nick: map[string]*store.Account{},
		chans: map[string]*store.Channel{}, bots: map[string]*store.Bot{},
		skippedAcct: map[string]bool{}, skippedChan: map[string]bool{}}
	by := map[string][]Object{}
	unknown := map[string]int{}
	for _, o := range objs {
		if !known[o.Type] {
			unknown[o.Type]++
			continue
		}
		by[o.Type] = append(by[o.Type], o)
	}
	steps := []func([]Object, map[string][]Object) error{
		im.accounts, im.botInfos, im.channels, im.access, im.akicks, im.modeLocks, im.entryMsgs,
		im.logSettings, im.badWords, im.memos, im.ajoins, im.miscData, im.hostRequests, im.xlines,
		im.forbids, im.exceptions, im.news, im.operInfo, im.ignores, im.seen, im.leftOut,
	}
	for _, step := range steps {
		if err := step(nil, by); err != nil {
			return nil, err
		}
	}
	for _, k := range im.keys {
		im.r.In = append(im.r.In, Count{k, im.in[k]})
	}
	types := make([]string, 0, len(unknown))
	for t := range unknown {
		types = append(types, t)
	}
	sort.Strings(types)
	for _, t := range types {
		im.r.Unknown = append(im.r.Unknown, Count{t, unknown[t]})
	}
	return im.r, nil
}

func fold(s string) string { return irc.Fold(s) }

func orNow(t time.Time) time.Time {
	if t.IsZero() {
		return time.Now()
	}
	return t
}

// --- accounts and nicks ---

func (im *importer) accounts(_ []Object, by map[string][]Object) error {
	aliases := map[string][]Object{} // folded display -> its nicks
	for _, na := range by["NickAlias"] {
		aliases[fold(na.Get("nc"))] = append(aliases[fold(na.Get("nc"))], na)
	}
	suspend := map[string]Object{}
	for _, s := range by["NSSuspendInfo"] {
		suspend[fold(s.Get("nick"))] = s
	}
	for _, nc := range by["NickCore"] {
		display := nc.Get("display")
		key := fold(display)
		nas := aliases[key]
		if display == "" || len(nas) == 0 {
			im.skip("account %q: it has no nicks", display)
			continue
		}
		// The display nick first, as Anope shows it.
		sort.SliceStable(nas, func(i, j int) bool { return fold(nas[i].Get("nick")) == key && fold(nas[j].Get("nick")) != key })
		if clash := im.accountClash(nas); clash != "" {
			if !im.opt.Replace {
				im.clash("nick %s: BeeIRCd's account stays; Anope's account %s is skipped", clash, display)
				im.skippedAcct[key] = true
				continue
			}
			if err := im.dropExisting(nas); err != nil {
				return err
			}
			im.clash("nick %s: replaced by Anope's account %s", clash, display)
		}
		hash := ""
		if pass := nc.Get("pass"); passwd.AnopeSupported(pass) {
			hash = passwd.AnopePrefix + pass
		} else {
			im.change("account %s: its password kind (%s) can't be checked, so it needs NickServ RESETPASS", display, strings.SplitN(pass, ":", 2)[0])
			hash = "!" // no password matches
		}
		first := nas[0]
		a, err := im.st.CreateAccount(first.Get("nick"), hash, nc.Get("email"), orNow(first.Time("time_registered")))
		if err != nil {
			return fmt.Errorf("account %s: %w", display, err)
		}
		if a.Name != display && fold(a.Name) == key {
			_ = im.st.RenameAccount(a.ID, display)
			a.Name = display
		}
		im.acct[key] = a
		im.nick[fold(first.Get("nick"))] = a
		for _, na := range nas[1:] {
			if err := im.st.AddNick(a, na.Get("nick"), orNow(na.Time("time_registered"))); err != nil {
				im.skip("nick %s (account %s): %v", na.Get("nick"), display, err)
				continue
			}
			im.nick[fold(na.Get("nick"))] = a
		}
		im.count("accounts", 1)
		im.count("nicks", len(nas))
		im.accountSettings(a, nc, nas, suspend[key])
		if err := im.st.SaveAccount(a); err != nil {
			return err
		}
		for _, m := range nc.Words("access") {
			if ok, _ := im.st.AddAccess(a, m); ok {
				im.count("NickServ access masks", 1)
			}
		}
		for _, fp := range nc.Words("cert") {
			if ok, _ := im.st.AddCert(a, strings.ToLower(fp), time.Now()); ok {
				im.count("certificates", 1)
			}
		}
		if g := nc.Get("greet"); g != "" {
			if err := im.st.SetGreet(a.ID, g); err == nil {
				im.count("greets", 1)
			}
		}
		for _, n := range nc.Words("memoignores") {
			if ok, _ := im.st.AddMemoIgnore(a, n); ok {
				im.count("memo ignores", 1)
			}
		}
		for _, na := range nas {
			if h := na.Get("vhost_host"); h != "" {
				if err := im.st.SetVHost(na.Get("nick"), na.Get("vhost_ident"), h, na.Get("vhost_creator"), orNow(na.Time("vhost_time"))); err == nil {
					im.count("vhosts", 1)
				} else {
					im.skip("vhost of %s: %v", na.Get("nick"), err)
				}
			}
		}
	}
	for key, nas := range aliases {
		if im.acct[key] == nil && !im.skippedAcct[key] && len(nas) > 0 {
			found := false
			for _, nc := range by["NickCore"] {
				if fold(nc.Get("display")) == key {
					found = true
				}
			}
			if !found {
				im.skip("%d nick(s) of account %q: Anope has no such account", len(nas), nas[0].Get("nc"))
			}
		}
	}
	return nil
}

// accountClash returns a nick of nas that BeeIRCd already has, or "".
func (im *importer) accountClash(nas []Object) string {
	for _, na := range nas {
		if a, _ := im.st.AccountByNick(na.Get("nick")); a != nil {
			return na.Get("nick")
		}
	}
	return ""
}

// dropExisting removes the BeeIRCd accounts holding any of nas's nicks.
func (im *importer) dropExisting(nas []Object) error {
	for _, na := range nas {
		if a, _ := im.st.AccountByNick(na.Get("nick")); a != nil {
			if _, err := im.st.DeleteAccount(a.ID); err != nil {
				return fmt.Errorf("replacing %s: %w", a.Name, err)
			}
		}
	}
	return nil
}

func (im *importer) accountSettings(a *store.Account, nc Object, nas []Object, sus Object) {
	a.Enforce = nc.Bool("KILLPROTECT") || nc.Bool("KILL_QUICK") || nc.Bool("KILL_IMMED")
	switch {
	case nc.Bool("KILL_IMMED"):
		a.Protect = store.KillImmed
	case nc.Bool("KILL_QUICK"):
		a.Protect = store.KillQuick
	default:
		a.Protect = store.KillOn
	}
	a.Secure = nc.Bool("NS_SECURE")
	a.Private = nc.Bool("NS_PRIVATE")
	a.HideEmail = nc.Bool("HIDE_EMAIL")
	a.HideHost = nc.Bool("HIDE_MASK")
	a.HideStatus = nc.Bool("HIDE_STATUS")
	a.HideQuit = nc.Bool("HIDE_QUIT")
	a.AutoOp = nc.Bool("AUTOOP")
	a.KeepModes = nc.Bool("NS_KEEP_MODES")
	a.MemoNotify = nc.Bool("MEMO_RECEIVE")
	a.MemoSignon = nc.Bool("MEMO_SIGNON")
	a.MemoMail = nc.Bool("MEMO_MAIL")
	if n := nc.Int("memomax"); n > 0 {
		a.MemoLimit = int(n)
	}
	for _, na := range nas {
		if na.Bool("NS_NO_EXPIRE") {
			a.NoExpire = true
		}
		if t := na.Time("last_seen"); t.After(a.LastSeen) {
			a.LastSeen = t
			if h := na.Get("last_realhost"); h != "" {
				a.LastHost = h
			} else {
				a.LastHost = na.Get("last_usermask")
			}
			a.LastQuit = na.Get("last_quit")
		}
	}
	if nc.Bool("NS_SUSPENDED") || sus.Data != nil { // 2.0.20 keeps only the NSSuspendInfo record
		a.SuspendedBy = sus.Get("by")
		if a.SuspendedBy == "" {
			a.SuspendedBy = "Anope"
		}
		a.SuspendReason = sus.Get("reason")
		a.SuspendedAt = orNow(sus.Time("time"))
		a.SuspendUntil = time.Unix(sus.Int("expires"), 0)
		im.count("suspended accounts", 1)
	}
	if nc.Bool("UNCONFIRMED") {
		im.change("account %s was not confirmed by email in Anope; in BeeIRCd it is a normal account", a.Name)
	}
}

// accountFor finds the new account behind an Anope nick or display.
func (im *importer) accountFor(name string) *store.Account {
	if a := im.nick[fold(name)]; a != nil {
		return a
	}
	return im.acct[fold(name)]
}

// --- bots ---

func (im *importer) botInfos(_ []Object, by map[string][]Object) error {
	for _, o := range by["BotInfo"] {
		nick := o.Get("nick")
		if serviceBots[strings.ToLower(nick)] {
			continue // BeeIRCd has its own
		}
		if b := im.st.BotByNick(nick); b != nil {
			im.clash("bot %s: BeeIRCd's bot stays", nick)
			im.bots[fold(nick)] = b
			continue
		}
		b := &store.Bot{Nick: nick, Ident: o.Get("user"), Host: o.Get("host"), Realname: o.Get("realname"),
			Created: orNow(o.Time("created")), Private: o.Bool("oper_only")}
		if err := im.st.AddBot(b); err != nil {
			im.skip("bot %s: %v", nick, err)
			continue
		}
		im.bots[fold(nick)] = b
		im.count("bots", 1)
	}
	return nil
}

// --- channels ---

func (im *importer) channels(_ []Object, by map[string][]Object) error {
	suspend := map[string]Object{}
	for _, s := range by["CSSuspendInfo"] {
		suspend[fold(s.Get("chan"))] = s
	}
	for _, ci := range by["ChannelInfo"] {
		name := ci.Get("name")
		key := fold(name)
		founder := im.accountFor(ci.Get("founder"))
		if founder == nil {
			im.skip("channel %s: its founder %q didn't come in", name, ci.Get("founder"))
			im.skippedChan[key] = true
			continue
		}
		if c, _ := im.st.ChannelByName(name); c != nil {
			if !im.opt.Replace {
				im.clash("channel %s: BeeIRCd's registration stays; Anope's is skipped", name)
				im.skippedChan[key] = true
				continue
			}
			if err := im.st.DeleteChannel(c.ID); err != nil {
				return fmt.Errorf("replacing %s: %w", name, err)
			}
			im.clash("channel %s: replaced by Anope's", name)
		}
		c, err := im.st.CreateChannel(name, founder, ci.Get("description"), orNow(ci.Time("time_registered")))
		if err != nil {
			return fmt.Errorf("channel %s: %w", name, err)
		}
		im.chans[key] = c
		if s := im.accountFor(ci.Get("successor")); s != nil {
			c.SuccessorID, c.Successor = s.ID, s.Name
		}
		c.LastUsed = orNow(ci.Time("last_used"))
		c.Topic, c.TopicBy, c.TopicAt = ci.Get("last_topic"), ci.Get("last_topic_setter"), ci.Time("last_topic_time")
		c.KeepTopic = ci.Bool("KEEPTOPIC")
		c.TopicLock = ci.Bool("TOPICLOCK")
		c.SecureOps = ci.Bool("SECUREOPS")
		c.Secure = ci.Bool("CS_SECURE")
		c.Private = ci.Bool("CS_PRIVATE")
		c.Restricted = ci.Bool("RESTRICTED")
		c.Peace = ci.Bool("PEACE")
		c.SecureFounder = ci.Bool("SECUREFOUNDER")
		c.SignKick = ci.Bool("SIGNKICK") || ci.Bool("SIGNKICK_LEVEL")
		c.KeepModes = ci.Bool("CS_KEEP_MODES")
		c.Persist = ci.Bool("PERSIST")
		c.AutoOp = !ci.Bool("NOAUTOOP")
		c.NoExpire = ci.Bool("CS_NO_EXPIRE")
		c.BanType = int(ci.Int("bantype"))
		c.LastModes = lastModes(ci.Words("last_modes"))
		if n := ci.Int("memomax"); n > 0 {
			c.MemoLimit = int(n)
		}
		if s, ok := suspend[key]; ok || ci.Bool("CS_SUSPENDED") {
			c.SuspendedBy = s.Get("by")
			if c.SuspendedBy == "" {
				c.SuspendedBy = "Anope"
			}
			c.SuspendReason = s.Get("reason")
			c.SuspendedAt = orNow(s.Time("time"))
			c.SuspendUntil = time.Unix(s.Int("expires"), 0)
			im.count("suspended channels", 1)
		}
		if err := im.st.SaveChannel(c); err != nil {
			return err
		}
		im.count("channels", 1)
		// LEVELS: "PRIV level PRIV level ..."; Anope's disabled is -10000 too.
		w := ci.Words("levels")
		for i := 0; i+1 < len(w); i += 2 {
			var lvl int
			if _, err := fmt.Sscan(w[i+1], &lvl); err == nil {
				_ = im.st.SetChanLevel(c, strings.ToUpper(w[i]), lvl)
			}
		}
		for _, n := range ci.Words("memoignores") {
			if ok, _ := im.st.AddChanMemoIgnore(c, n); ok {
				im.count("channel memo ignores", 1)
			}
		}
		// BotServ: the bot, its settings and the kickers.
		cb := store.ChanBot{Greet: ci.Bool("BS_GREET"), Fantasy: ci.Bool("BS_FANTASY"), NoBot: ci.Bool("BS_NOBOT"),
			DontKickOps:    ci.Bool("BS_DONTKICKOPS") || ci.Bool("dontkickops"),
			DontKickVoices: ci.Bool("BS_DONTKICKVOICES") || ci.Bool("dontkickvoices"),
			BanExpire:      time.Duration(ci.Int("banexpire")) * time.Second}
		if bn := ci.Get("bi"); bn != "" {
			if b := im.bots[fold(bn)]; b != nil {
				cb.BotID = b.ID
				im.count("channels with a bot", 1)
			} else if !serviceBots[strings.ToLower(bn)] {
				im.skip("bot %s on %s: the bot didn't come in", bn, name)
			}
		}
		if err := im.st.SetChanBot(c, cb); err != nil {
			return err
		}
		var k store.Kickers
		any := false
		for i, f := range []string{"bolds", "colors", "reverses", "underlines", "badwords", "caps", "flood", "repeat", "italics", "amsgs"} {
			if ci.Bool("kickerdata:" + f) {
				k.On[i], any = true, true
			}
		}
		for i, t := range ci.Words("ttb") {
			if i < store.KickerCount {
				fmt.Sscan(t, &k.TTB[i])
			}
		}
		k.CapsMin, k.CapsPercent = int(ci.Int("capsmin")), int(ci.Int("capspercent"))
		k.FloodLines, k.FloodSecs, k.RepeatTimes = int(ci.Int("floodlines")), int(ci.Int("floodsecs")), int(ci.Int("repeattimes"))
		if any {
			if err := im.st.SetChanKickers(c, k); err != nil {
				return err
			}
			im.count("channels with kickers", 1)
		}
	}
	return nil
}

// channelFor finds a channel that came in, quietly nil for one skipped on purpose.
func (im *importer) channelFor(name, what string) *store.Channel {
	if c := im.chans[fold(name)]; c != nil {
		return c
	}
	if !im.skippedChan[fold(name)] {
		im.skip("%s on %s: the channel isn't registered in Anope", what, name)
	}
	return nil
}

// xopLevels are Anope's xOP names on BeeIRCd's (and Anope's) level scale.
var xopLevels = map[string]int{"QOP": 10000, "SOP": 10, "AOP": 5, "HOP": 4, "VOP": 3}

func (im *importer) access(_ []Object, by map[string][]Object) error {
	for _, o := range by["ChanAccess"] {
		c := im.channelFor(o.Get("ci"), "access entry")
		if c == nil {
			continue
		}
		mask, data := o.Get("mask"), o.Get("data")
		var lvl int
		switch o.Get("provider") {
		case "access/access":
			fmt.Sscan(data, &lvl)
		case "access/xop":
			lvl = xopLevels[strings.ToUpper(data)]
		case "access/flags":
			lvl = flagsLevel(data)
			im.change("%s: %s FLAGS +%s became level %d", c.Name, mask, data, lvl)
		}
		if lvl == 0 {
			im.skip("%s: access entry %s (%s %q) has no level BeeIRCd knows", c.Name, mask, o.Get("provider"), data)
			continue
		}
		a := im.accountFor(mask)
		if a == nil {
			im.skip("%s: access for %s (level %d): BeeIRCd keeps access for registered nicks only", c.Name, mask, lvl)
			continue
		}
		if a.ID == c.FounderID {
			continue
		}
		if _, err := im.st.SetLevel(c, a, lvl, o.Get("creator"), "", orNow(o.Time("created"))); err != nil {
			im.skip("%s: access for %s: %v", c.Name, mask, err)
			continue
		}
		im.count("access entries", 1)
	}
	return nil
}

// flagsLevel turns Anope FLAGS (BeeIRCd has no FLAGS yet) into the nearest
// level: changing access or founder flags = SOP, op = AOP, halfop = HOP,
// voice = VOP, anything else = 1.
func flagsLevel(flags string) int {
	switch {
	case strings.ContainsAny(flags, "Ffas"):
		return xopLevels["SOP"]
	case strings.ContainsAny(flags, "oO"):
		return xopLevels["AOP"]
	case strings.ContainsAny(flags, "hH"):
		return xopLevels["HOP"]
	case strings.ContainsAny(flags, "vV"):
		return xopLevels["VOP"]
	case flags != "":
		return 1
	}
	return 0
}

func (im *importer) akicks(_ []Object, by map[string][]Object) error {
	for _, o := range by["AutoKick"] {
		c := im.channelFor(o.Get("ci"), "akick")
		if c == nil {
			continue
		}
		mask := o.Get("mask")
		if nc := o.Get("nc"); nc != "" {
			mask = "~a:" + nc // an akick on an account
		}
		if ok, err := im.st.AddAkick(c, mask, o.Get("reason"), o.Get("creator"), orNow(o.Time("addtime"))); err != nil {
			return err
		} else if ok {
			im.count("akicks", 1)
		}
	}
	return nil
}

// modeLetters are Anope's channel mode names (UnrealIRCd protocol module).
var modeLetters = map[string]string{"ALLSSL": "Z", "BLOCKCOLOR": "c", "CENSOR": "G", "DELAYEDJOIN": "D",
	"INVITE": "i", "KEY": "k", "LIMIT": "l", "MODERATED": "m", "NOCTCP": "C", "NOEXTERNAL": "n",
	"NOINVITE": "V", "NOKICK": "Q", "NOKNOCK": "K", "NONICK": "N", "NONOTICE": "T", "OPERONLY": "O",
	"PERM": "P", "PRIVATE": "p", "REDIRECT": "L", "REGISTEREDONLY": "R", "REGISTERED": "r",
	"REGMODERATED": "M", "SECRET": "s", "SSL": "z", "STRIPCOLOR": "S", "TOPIC": "t", "FLOOD": "f",
	"HISTORY": "H"}

// lastModes turns Anope's "KEY,honey LIMIT,50 NOEXTERNAL" into "+kln honey 50".
func lastModes(words []string) string {
	letters, params := "", ""
	for _, w := range words {
		name, param, _ := strings.Cut(w, ",")
		l := modeLetters[name]
		if l == "" || strings.Contains(letters, l) {
			continue
		}
		letters += l
		if param != "" {
			params += " " + param
		}
	}
	if letters == "" {
		return ""
	}
	return "+" + letters + params
}

func (im *importer) modeLocks(_ []Object, by map[string][]Object) error {
	for _, o := range by["ModeLock"] {
		c := im.channelFor(o.Get("ci"), "mode lock")
		if c == nil {
			continue
		}
		name, param := o.Get("name"), o.Get("param")
		l := modeLetters[name]
		if l == "" {
			im.skip("%s: mode lock %s (no such mode in BeeIRCd)", c.Name, name)
			continue
		}
		if !o.Bool("set") {
			if !strings.Contains(c.MlockOff, l) {
				c.MlockOff += l
			}
		} else {
			switch l {
			case "k":
				c.MlockKey = param
			case "l":
				fmt.Sscan(param, &c.MlockLimit)
			default:
				if param != "" {
					c.MlockParams = strings.TrimSpace(c.MlockParams + " " + l + " " + param)
				} else if !strings.Contains(c.MlockOn, l) {
					c.MlockOn += l
				}
			}
		}
		if err := im.st.SaveChannel(c); err != nil {
			return err
		}
		im.count("mode locks", 1)
	}
	return nil
}

func (im *importer) entryMsgs(_ []Object, by map[string][]Object) error {
	for _, o := range by["EntryMsg"] {
		if c := im.channelFor(o.Get("ci"), "entry message"); c != nil {
			if err := im.st.AddEntryMsg(c, o.Get("message"), o.Get("creator"), orNow(o.Time("when"))); err != nil {
				return err
			}
			im.count("entry messages", 1)
		}
	}
	return nil
}

func (im *importer) logSettings(_ []Object, by map[string][]Object) error {
	for _, o := range by["LogSetting"] {
		if c := im.channelFor(o.Get("ci"), "log setting"); c != nil {
			cmd := o.Get("command_service") + "/" + strings.ToUpper(o.Get("command_name"))
			if err := im.st.SetLog(c, store.LogSetting{Command: cmd, Method: strings.ToUpper(o.Get("method")), Status: o.Get("extra")}); err != nil {
				return err
			}
			im.count("channel log settings", 1)
		}
	}
	return nil
}

func (im *importer) badWords(_ []Object, by map[string][]Object) error {
	for _, o := range by["BadWord"] {
		if c := im.channelFor(o.Get("ci"), "bad word"); c != nil {
			if err := im.st.AddBadWord(c, o.Get("word"), int(o.Int("type"))); err != nil {
				return err
			}
			im.count("bad words", 1)
		}
	}
	return nil
}

func (im *importer) memos(_ []Object, by map[string][]Object) error {
	for _, o := range by["Memo"] {
		owner, from, text, when := o.Get("owner"), o.Get("sender"), o.Get("text"), orNow(o.Time("time"))
		if strings.HasPrefix(owner, "#") || strings.HasPrefix(owner, "&") {
			c := im.channelFor(owner, "memo")
			if c == nil {
				continue
			}
			if err := im.st.AddChanMemo(c, from, text, when); err != nil {
				return err
			}
			if !o.Bool("unread") {
				if ms := im.st.ChanMemos(c); len(ms) > 0 {
					_ = im.st.MarkChanMemoRead(ms[len(ms)-1].ID)
				}
			}
			im.count("channel memos", 1)
			continue
		}
		a := im.accountFor(owner)
		if a == nil {
			im.skip("memo to %s: the account didn't come in", owner)
			continue
		}
		if err := im.st.AddMemo(a, from, text, when); err != nil {
			return err
		}
		if !o.Bool("unread") {
			if ms, _ := im.st.Memos(a); len(ms) > 0 {
				_ = im.st.MarkRead(ms[len(ms)-1].ID)
			}
		}
		im.count("memos", 1)
	}
	return nil
}

func (im *importer) ajoins(_ []Object, by map[string][]Object) error {
	for _, o := range by["AJoinEntry"] {
		a := im.accountFor(o.Get("owner"))
		if a == nil {
			continue
		}
		if err := im.st.AddAjoin(a, o.Get("channel"), o.Get("key")); err != nil {
			im.skip("auto-join %s for %s: %v", o.Get("channel"), a.Name, err)
			continue
		}
		im.count("auto-joins", 1)
	}
	return nil
}

// miscData: SET URL (and Anope's other free-text settings).
func (im *importer) miscData(_ []Object, by map[string][]Object) error {
	for _, o := range by["NSMiscData"] {
		a := im.accountFor(o.Get("nc"))
		if a == nil {
			continue
		}
		switch n := strings.ToLower(strings.TrimPrefix(o.Get("name"), "ns_set_misc:")); n {
		case "url":
			a.URL = o.Get("data")
			if err := im.st.SaveAccount(a); err != nil {
				return err
			}
			im.count("URLs", 1)
		default:
			im.skip("account %s: SET %s (BeeIRCd has no such setting)", a.Name, strings.ToUpper(n))
		}
	}
	for _, o := range by["CSMiscData"] {
		c := im.channelFor(o.Get("ci"), "setting")
		if c == nil {
			continue
		}
		switch n := strings.ToLower(strings.TrimPrefix(o.Get("name"), "cs_set_misc:")); n {
		case "url":
			c.URL = o.Get("data")
			if err := im.st.SaveChannel(c); err != nil {
				return err
			}
			im.count("URLs", 1)
		default:
			im.skip("%s: SET %s (BeeIRCd has no such setting)", c.Name, strings.ToUpper(n))
		}
	}
	return nil
}

func (im *importer) hostRequests(_ []Object, by map[string][]Object) error {
	for _, o := range by["HostRequest"] {
		if im.accountFor(o.Get("nick")) == nil {
			continue
		}
		if err := im.st.RequestVHost(o.Get("nick"), o.Get("ident"), o.Get("host"), orNow(o.Time("time"))); err != nil {
			im.skip("vhost request of %s: %v", o.Get("nick"), err)
			continue
		}
		im.count("vhost requests", 1)
	}
	return nil
}

// --- network-wide lists ---

func (im *importer) xlines(_ []Object, by map[string][]Object) error {
	for _, o := range by["XLine"] {
		mask, reason, setBy := o.Get("mask"), o.Get("reason"), o.Get("by")
		setAt, exp := orNow(o.Time("created")), o.Time("expires")
		switch strings.TrimPrefix(o.Get("manager"), "xlinemanager/") {
		case "sgline":
			kind := store.BanAkill
			if !strings.Contains(mask, "@") { // a bare IP
				kind = store.BanZline
			}
			if err := im.st.AddNetBan(store.NetBan{Kind: kind, Mask: mask, Reason: reason, SetBy: setBy, SetAt: setAt, Expires: exp}); err != nil {
				return err
			}
			im.count("AKILLs", 1)
		case "szline":
			if err := im.st.AddNetBan(store.NetBan{Kind: store.BanZline, Mask: mask, Reason: reason, SetBy: setBy, SetAt: setAt, Expires: exp}); err != nil {
				return err
			}
			im.count("Z-lines", 1)
		case "snline":
			if err := im.st.AddNetBan(store.NetBan{Kind: store.BanSNLine, Mask: mask, Reason: reason, SetBy: setBy, SetAt: setAt, Expires: exp}); err != nil {
				return err
			}
			im.count("SNLINEs", 1)
		case "sqline": // nick bans; in BeeIRCd they are FORBID NICK / CHAN
			kind := store.ForbidNick
			if strings.HasPrefix(mask, "#") {
				kind = store.ForbidChan
			}
			if _, err := im.st.AddForbid(kind, store.Forbid{Name: mask, Reason: reason, SetBy: setBy, SetAt: setAt, Expires: exp}); err != nil {
				return err
			}
			im.count("SQLINEs", 1)
		default:
			im.skip("network ban %s (%s)", mask, o.Get("manager"))
		}
	}
	return nil
}

// forbidKinds are Anope's ForbidType numbers.
var forbidKinds = map[int64]string{1: store.ForbidNick, 2: store.ForbidChan, 3: store.ForbidEmail, 4: store.ForbidRegister}

func (im *importer) forbids(_ []Object, by map[string][]Object) error {
	for _, o := range by["ForbidData"] {
		kind := forbidKinds[o.Int("type")]
		if kind == "" {
			im.skip("forbid %s (type %d)", o.Get("mask"), o.Int("type"))
			continue
		}
		if _, err := im.st.AddForbid(kind, store.Forbid{Name: o.Get("mask"), Reason: o.Get("reason"), SetBy: o.Get("creator"),
			SetAt: orNow(o.Time("created")), Expires: o.Time("expires")}); err != nil {
			return err
		}
		im.count("forbids", 1)
	}
	return nil
}

func (im *importer) exceptions(_ []Object, by map[string][]Object) error {
	for _, o := range by["Exception"] {
		if err := im.st.SaveSessionExcept(store.SessionExcept{Mask: o.Get("mask"), Max: int(o.Int("limit")), Reason: o.Get("reason"),
			SetBy: o.Get("who"), SetAt: orNow(o.Time("time")), Expires: o.Time("expires")}); err != nil {
			return err
		}
		im.count("session exceptions", 1)
	}
	return nil
}

// newsKinds are Anope's NewsType numbers.
var newsKinds = map[int64]string{0: store.NewsLogon, 1: store.NewsRandom, 2: store.NewsOper}

func (im *importer) news(_ []Object, by map[string][]Object) error {
	// A second import of the same file must not show every item twice.
	have, err := im.st.AllNews()
	if err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, n := range have {
		seen[n.Kind+"\x00"+n.Text] = true
	}
	for _, o := range by["NewsItem"] {
		kind := newsKinds[o.Int("type")]
		if kind == "" {
			im.skip("news %q (type %d)", o.Get("text"), o.Int("type"))
			continue
		}
		key := kind + "\x00" + o.Get("text")
		if seen[key] {
			continue
		}
		seen[key] = true
		if err := im.st.AddNews(kind, o.Get("text"), o.Get("who"), orNow(o.Time("time"))); err != nil {
			return err
		}
		im.count("news", 1)
	}
	return nil
}

func (im *importer) operInfo(_ []Object, by map[string][]Object) error {
	for _, o := range by["OperInfo"] {
		t := o.Get("target")
		var (
			channel bool
			id      int64
		)
		if strings.HasPrefix(t, "#") {
			c := im.channelFor(t, "oper note")
			if c == nil {
				continue
			}
			channel, id = true, c.ID
		} else if a := im.accountFor(t); a != nil {
			id = a.ID
		} else {
			im.skip("oper note on %s: the account didn't come in", t)
			continue
		}
		if err := im.st.AddOperInfo(channel, id, o.Get("info"), o.Get("adder"), orNow(o.Time("created"))); err != nil {
			return err
		}
		im.count("oper notes", 1)
	}
	return nil
}

func (im *importer) ignores(_ []Object, by map[string][]Object) error {
	for _, o := range by["IgnoreData"] {
		if err := im.st.SetSvcIgnore(store.Ignore{Mask: o.Get("mask"), Reason: o.Get("reason"), SetBy: o.Get("creator"),
			SetAt: time.Now(), Expires: time.Unix(o.Int("time"), 0)}); err != nil {
			return err
		}
		im.count("services ignores", 1)
	}
	return nil
}

// seenKinds are Anope's cs_seen TypeInfo numbers.
var seenKinds = []string{store.SeenConnect, store.SeenNickTo, store.SeenNickFrom, store.SeenJoin, store.SeenPart, store.SeenQuit, store.SeenKick}

func (im *importer) seen(_ []Object, by map[string][]Object) error {
	var all []store.Seen
	for _, o := range by["SeenInfo"] {
		k := o.Int("type")
		if k < 0 || int(k) >= len(seenKinds) {
			continue
		}
		all = append(all, store.Seen{Nick: o.Get("nick"), Mask: o.Get("vhost"), Kind: seenKinds[k], Other: o.Get("nick2"),
			Channel: o.Get("channel"), Message: o.Get("message"), Last: o.Time("last")})
	}
	if len(all) > 0 {
		if err := im.st.SaveSeen(all, nil); err != nil {
			return err
		}
		im.count("seen entries", len(all))
	}
	return nil
}

// leftOut lists what is read but not brought over on purpose.
func (im *importer) leftOut(_ []Object, by map[string][]Object) error {
	for _, o := range by["Oper"] {
		im.skip("services oper %s (%s): set opers in beeircd.conf or with AdminServ OPER", o.Get("name"), o.Get("type"))
	}
	if n := len(by["Stats"]); n > 0 {
		im.skip("%d Stats record (max users: BeeIRCd counts its own)", n)
	}
	return nil
}
