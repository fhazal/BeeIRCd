package server

import (
	"encoding/json"
	"errors"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/fhazal/beeircd/internal/irc"
	"github.com/fhazal/beeircd/internal/link"
	"github.com/fhazal/beeircd/internal/services"
	"github.com/fhazal/beeircd/internal/spamfilter"
	"github.com/fhazal/beeircd/internal/state"
	"github.com/fhazal/beeircd/internal/store"
	"github.com/fhazal/beeircd/internal/version"
)

// The API's methods, with UnrealIRCd's names, parameters and answers.
// Each runs on the loop.

type rpcMethod struct {
	changes bool // changes something: opers with +s see the call
	run     func(s *Server, rc *rpcConn, params json.RawMessage) (any, *rpcError)
}

var rpcMethods map[string]rpcMethod

func init() {
	read := func(f func(*Server, *rpcConn, json.RawMessage) (any, *rpcError)) rpcMethod {
		return rpcMethod{false, f}
	}
	change := func(f func(*Server, *rpcConn, json.RawMessage) (any, *rpcError)) rpcMethod { return rpcMethod{true, f} }
	rpcMethods = map[string]rpcMethod{
		"rpc.info":       read((*Server).rpcInfo),
		"rpc.set_issuer": read((*Server).rpcSetIssuer),
		"stats.get":      read((*Server).rpcStats),

		"user.list":         read((*Server).rpcUserList),
		"user.get":          read((*Server).rpcUserGet),
		"user.set_nick":     change((*Server).rpcUserSetNick),
		"user.set_username": change((*Server).rpcUserSetUsername),
		"user.set_realname": change((*Server).rpcUserSetRealname),
		"user.set_vhost":    change((*Server).rpcUserSetVhost),
		"user.set_mode":     change((*Server).rpcUserSetMode),
		"user.join":         change((*Server).rpcUserJoin),
		"user.part":         change((*Server).rpcUserPart),
		"user.kill":         change((*Server).rpcUserKill),
		"user.quit":         change((*Server).rpcUserQuit),

		"channel.list":      read((*Server).rpcChannelList),
		"channel.get":       read((*Server).rpcChannelGet),
		"channel.set_mode":  change((*Server).rpcChannelSetMode),
		"channel.set_topic": change((*Server).rpcChannelSetTopic),
		"channel.kick":      change((*Server).rpcChannelKick),

		"server.list":        read((*Server).rpcServerList),
		"server.get":         read((*Server).rpcServerGet),
		"server.rehash":      change((*Server).rpcServerRehash),
		"server.module_list": read((*Server).rpcModuleList),

		"server_ban.list": read((*Server).rpcBanList),
		"server_ban.get":  read((*Server).rpcBanGet),
		"server_ban.add":  change((*Server).rpcBanAdd),
		"server_ban.del":  change((*Server).rpcBanDel),

		"server_ban_exception.list": read((*Server).rpcExceptList),
		"server_ban_exception.get":  read((*Server).rpcExceptGet),
		"server_ban_exception.add":  change((*Server).rpcExceptAdd),
		"server_ban_exception.del":  change((*Server).rpcExceptDel),

		"name_ban.list": read((*Server).rpcNameBanList),
		"name_ban.get":  read((*Server).rpcNameBanGet),
		"name_ban.add":  change((*Server).rpcNameBanAdd),
		"name_ban.del":  change((*Server).rpcNameBanDel),

		"spamfilter.list": read((*Server).rpcSpamList),
		"spamfilter.get":  read((*Server).rpcSpamGet),
		"spamfilter.add":  change((*Server).rpcSpamAdd),
		"spamfilter.del":  change((*Server).rpcSpamDel),

		"whowas.get": read((*Server).rpcWhowas),

		"log.subscribe":   read((*Server).rpcLogSubscribe),
		"log.unsubscribe": read((*Server).rpcLogUnsubscribe),
		"log.list":        read((*Server).rpcLogList),
	}
}

type obj = map[string]any

// ---- rpc.* and stats.* ----

func (s *Server) rpcInfo(rc *rpcConn, _ json.RawMessage) (any, *rpcError) {
	methods := obj{}
	for name := range rpcMethods {
		group, _, _ := strings.Cut(name, ".")
		methods[name] = obj{"name": name, "module": "rpc/" + group, "version": version.Version}
	}
	return obj{"methods": methods}, nil
}

func (s *Server) rpcSetIssuer(rc *rpcConn, params json.RawMessage) (any, *rpcError) {
	var p struct {
		Name string `json:"name"`
	}
	if e := rpcParams(params, &p); e != nil {
		return nil, e
	}
	if p.Name == "" || len(p.Name) > 32 || strings.ContainsAny(p.Name, " \t\r\n:,!@") {
		return nil, rpcErr(rpcInvalidName, "The issuer is a name of at most 32 characters without spaces")
	}
	rc.issuer = p.Name
	return true, nil
}

func (s *Server) rpcStats(rc *rpcConn, _ json.RawMessage) (any, *rpcError) {
	total, _, opers := s.net.PeopleCounts()
	bots := 0
	countries := map[string]int{}
	for _, u := range s.net.Users() {
		switch {
		case u.Service:
			bots++
		case u.Country != "":
			countries[u.Country]++
		}
	}
	var top []obj
	for cc, n := range countries {
		top = append(top, obj{"country": cc, "count": n})
	}
	sort.Slice(top, func(i, k int) bool {
		if top[i]["count"].(int) != top[k]["count"].(int) {
			return top[i]["count"].(int) > top[k]["count"].(int)
		}
		return top[i]["country"].(string) < top[k]["country"].(string)
	})
	if len(top) > 20 {
		top = top[:20]
	}
	counts := s.svc.BanCounts()
	bans := counts[store.BanAkill] + counts[store.BanZline] + counts[store.BanShun] + len(s.svc.Klines())
	spam := len(s.svc.SpamFilters())
	return obj{
		"server":     obj{"total": len(s.netServers()), "ulined": boolInt(s.hasServices())},
		"user":       obj{"total": total + bots, "ulined": bots, "oper": opers, "record": s.maxGlobal, "countries": top},
		"channel":    obj{"total": len(s.net.Channels())},
		"server_ban": obj{"total": bans + spam + counts[store.BanEline], "server_ban": bans, "spamfilter": spam, "name_ban": len(s.rpcNameBans()), "server_ban_exception": counts[store.BanEline]},
	}, nil
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func (s *Server) hasServices() bool {
	_, ok := s.servicesInfo()
	return ok
}

// ---- user.* ----

// rpcUser is u as UnrealIRCd's API shows a client; full adds the channels.
func (s *Server) rpcUser(u *state.User, full bool) obj {
	o := obj{
		"name":            u.Nick,
		"id":              u.UID,
		"hostname":        u.RealHost,
		"ip":              u.IP,
		"details":         u.Nick + "!" + u.TrueIdent() + "@" + u.RealHost,
		"connected_since": rpcTime(u.SignOn),
	}
	usr := obj{
		"username":   u.Ident,
		"realname":   u.Realname,
		"servername": u.Server,
		"modes":      strings.TrimPrefix(u.ModeString(), "+"),
	}
	if u.Host != u.RealHost {
		usr["vhost"] = u.Host
	}
	// Always there, as on UnrealIRCd: unrealircd-webpanel's kickban bans
	// *!*@cloakedhost, which without it would be *!*@* (everyone).
	usr["cloakedhost"] = s.userCloak(u)
	if usr["cloakedhost"] == "" {
		usr["cloakedhost"] = u.Host
	}
	if u.Account != "" {
		usr["account"] = u.Account
	}
	if u.Away != "" {
		usr["away_reason"] = u.Away
	}
	if u.IP != "" && s.repOn() {
		usr["reputation"] = s.repScore(u.IP)
	}
	if len(u.Groups) > 0 {
		usr["security-groups"] = u.Groups
	}
	if u.Modes['o'] {
		class, login := u.OperClass, u.OperName
		if tc := s.clientOf(u); tc != nil {
			class = tc.operClass
		}
		usr["operlogin"], usr["operclass"] = login, class
	}
	if tc := s.clientOf(u); tc != nil {
		o["server_port"] = tc.port
		o["idle_since"] = rpcTime(tc.lastMsg)
	}
	if full {
		chans := []obj{}
		for _, ch := range u.Channels {
			if mem := ch.Member(u); mem != nil {
				chans = append(chans, obj{"name": ch.Name, "level": rankLetters(mem.Ranks)})
			}
		}
		sort.Slice(chans, func(i, k int) bool { return chans[i]["name"].(string) < chans[k]["name"].(string) })
		usr["channels"] = chans
	}
	o["user"] = usr
	if u.TLS {
		t := obj{}
		if u.CertFP != "" {
			t["certfp"] = u.CertFP
		}
		o["tls"] = t
	}
	if u.Country != "" || u.ASN != 0 {
		g := obj{}
		if u.Country != "" {
			g["country_code"] = u.Country
		}
		if u.ASN != 0 {
			g["asn"], g["asname"] = u.ASN, u.ASOrg
		}
		o["geoip"] = g
	}
	return o
}

func (s *Server) rpcUserList(rc *rpcConn, _ json.RawMessage) (any, *rpcError) {
	users := s.net.Users()
	sort.Slice(users, func(i, k int) bool { return users[i].UID < users[k].UID })
	list := make([]obj, 0, len(users))
	for _, u := range users {
		list = append(list, s.rpcUser(u, false))
	}
	return obj{"list": list}, nil
}

// rpcNick is the "nick" parameter every user method takes.
type rpcNick struct {
	Nick string `json:"nick"`
}

// rpcFindUser finds the person a method is about, by nick or UID.
func (s *Server) rpcFindUser(nick string) (*state.User, *rpcError) {
	if nick == "" {
		return nil, rpcErr(rpcInvalidParams, "Missing parameter: 'nick'")
	}
	if u := s.userByUIDOrNick(nick); u != nil {
		return u, nil
	}
	return nil, rpcErr(rpcNotFound, "Nickname not found")
}

// rpcPerson is rpcFindUser for methods that change someone: never a
// service.
func (s *Server) rpcPerson(nick string) (*state.User, *rpcError) {
	u, e := s.rpcFindUser(nick)
	if e == nil && u.Service {
		return nil, rpcErr(rpcRefused, "%s is a network service", u.Nick)
	}
	return u, e
}

func (s *Server) rpcUserGet(rc *rpcConn, params json.RawMessage) (any, *rpcError) {
	var p rpcNick
	if e := rpcParams(params, &p); e != nil {
		return nil, e
	}
	u, e := s.rpcFindUser(p.Nick)
	if e != nil {
		return nil, e
	}
	return obj{"client": s.rpcUser(u, true)}, nil
}

func (s *Server) rpcUserSetNick(rc *rpcConn, params json.RawMessage) (any, *rpcError) {
	var p struct {
		rpcNick
		NewNick string `json:"newnick"`
	}
	if e := rpcParams(params, &p); e != nil {
		return nil, e
	}
	u, e := s.rpcPerson(p.Nick)
	switch {
	case e != nil:
		return nil, e
	case !irc.ValidNick(p.NewNick, s.cfg.Limits.NickLen) || services.IsReserved(p.NewNick):
		return nil, rpcErr(rpcInvalidName, "New nickname contains forbidden character(s) or is too long")
	case p.NewNick == u.Nick:
		return nil, rpcErr(rpcExists, "Old nickname and new nickname are identical")
	case !irc.EqualFold(p.NewNick, u.Nick) && s.net.NickInUse(p.NewNick):
		return nil, rpcErr(rpcExists, "New nickname is already taken by another user")
	}
	if s.ours(u.UID) {
		if err := s.emit(&state.NickChange{UID: u.UID, Nick: p.NewNick, TS: time.Now().Unix()}); err != nil {
			return nil, rpcErr(rpcExists, "New nickname is already taken by another user")
		}
		return true, nil
	}
	if !s.sendRemote(u.UID, &link.ForceNick{Source: s.svcSource(), UID: u.UID, Nick: p.NewNick, TS: time.Now().Unix()}) {
		return nil, rpcErr(rpcInternal, "%s's server can't be reached", u.Nick)
	}
	return true, nil
}

func (s *Server) rpcUserSetUsername(rc *rpcConn, params json.RawMessage) (any, *rpcError) {
	var p struct {
		rpcNick
		Username string `json:"username"`
	}
	if e := rpcParams(params, &p); e != nil {
		return nil, e
	}
	u, e := s.rpcPerson(p.Nick)
	switch {
	case e != nil:
		return nil, e
	case !validSetIdent(p.Username):
		return nil, rpcErr(rpcInvalidName, "New username contains forbidden character(s) or is too long")
	case p.Username == u.Ident:
		return nil, rpcErr(rpcExists, "Old username and new username are identical")
	}
	if tc := s.clientOf(u); tc != nil {
		s.changeHost(tc, "", p.Username)
	} else {
		s.emit(&state.HostChange{UID: u.UID, Host: u.Host, Ident: p.Username})
	}
	return true, nil
}

func (s *Server) rpcUserSetRealname(rc *rpcConn, params json.RawMessage) (any, *rpcError) {
	var p struct {
		rpcNick
		Realname string `json:"realname"`
	}
	if e := rpcParams(params, &p); e != nil {
		return nil, e
	}
	u, e := s.rpcPerson(p.Nick)
	switch {
	case e != nil:
		return nil, e
	case p.Realname == "" || len(p.Realname) > maxRealname:
		return nil, rpcErr(rpcInvalidName, "New real name is empty or longer than %d characters", maxRealname)
	case p.Realname == u.Realname:
		return nil, rpcErr(rpcExists, "Old realname and new realname are identical")
	}
	s.emit(&state.RealnameChange{UID: u.UID, Realname: p.Realname})
	return true, nil
}

func (s *Server) rpcUserSetVhost(rc *rpcConn, params json.RawMessage) (any, *rpcError) {
	var p struct {
		rpcNick
		Vhost string `json:"vhost"`
	}
	if e := rpcParams(params, &p); e != nil {
		return nil, e
	}
	u, e := s.rpcPerson(p.Nick)
	switch {
	case e != nil:
		return nil, e
	case !validVhost(p.Vhost):
		return nil, rpcErr(rpcInvalidName, "New vhost contains forbidden character(s) or is too long")
	case p.Vhost == u.Host:
		return nil, rpcErr(rpcExists, "Old vhost and new vhost are identical")
	}
	if tc := s.clientOf(u); tc != nil {
		s.changeHost(tc, p.Vhost, "")
	} else {
		s.emit(&state.HostChange{UID: u.UID, Host: p.Vhost})
	}
	return true, nil
}

func (s *Server) rpcUserSetMode(rc *rpcConn, params json.RawMessage) (any, *rpcError) {
	var p struct {
		rpcNick
		Modes string `json:"modes"`
	}
	if e := rpcParams(params, &p); e != nil {
		return nil, e
	}
	u, e := s.rpcPerson(p.Nick)
	switch {
	case e != nil:
		return nil, e
	case p.Modes == "" || strings.ContainsAny(p.Modes, " \r\n"):
		return nil, rpcErr(rpcInvalidParams, "Missing parameter: 'modes'")
	case strings.Contains(p.Modes, "o") && strings.HasPrefix(strings.TrimLeft(p.Modes, "-"), "+"):
		// Nobody becomes an oper through a mode: that is /OPER.
		if add := modeAdds(p.Modes, 'o'); add {
			return nil, rpcErr(rpcRefused, "User mode +o is given by /OPER only")
		}
	}
	if tc := s.clientOf(u); tc != nil {
		if s.setUserModes(tc, s.svcBotUID(), p.Modes) {
			return nil, rpcErr(rpcInvalidParams, "Unknown user mode in '%s' (the known ones were applied)", p.Modes)
		}
		return true, nil
	}
	if !s.sendRemote(u.UID, &link.UMode{Source: s.svcSource(), UID: u.UID, Modes: p.Modes}) {
		return nil, rpcErr(rpcInternal, "%s's server can't be reached", u.Nick)
	}
	return true, nil
}

// modeAdds reports whether a mode string like "+i-o+w" adds letter.
func modeAdds(modes string, letter byte) bool {
	add := true
	for i := 0; i < len(modes); i++ {
		switch modes[i] {
		case '+':
			add = true
		case '-':
			add = false
		case letter:
			if add {
				return true
			}
		}
	}
	return false
}

// svcBotUID is a services bot to name as the source of a change, "" when
// services are off.
func (s *Server) svcBotUID() string {
	for _, u := range s.net.Users() {
		if u.Service && s.ours(u.UID) {
			return u.UID
		}
	}
	return ""
}

func (s *Server) rpcUserJoin(rc *rpcConn, params json.RawMessage) (any, *rpcError) {
	var p struct {
		rpcNick
		Channel string `json:"channel"`
	}
	if e := rpcParams(params, &p); e != nil {
		return nil, e
	}
	u, e := s.rpcPerson(p.Nick)
	if e != nil {
		return nil, e
	}
	names := strings.Split(p.Channel, ",")
	for _, name := range names {
		if !irc.ValidChannel(name, s.cfg.Limits.ChannelLen) {
			return nil, rpcErr(rpcInvalidName, "'%s' is not a channel name", name)
		}
	}
	for _, name := range names {
		if ch := s.net.Channel(name); ch == nil || ch.Member(u) == nil {
			host{s}.ForceJoin(u.UID, name)
		}
	}
	return true, nil
}

func (s *Server) rpcUserPart(rc *rpcConn, params json.RawMessage) (any, *rpcError) {
	var p struct {
		rpcNick
		Channel string `json:"channel"`
	}
	if e := rpcParams(params, &p); e != nil {
		return nil, e
	}
	u, e := s.rpcPerson(p.Nick)
	if e != nil {
		return nil, e
	}
	if p.Channel == "" {
		return nil, rpcErr(rpcInvalidParams, "Missing parameter: 'channel'")
	}
	for _, name := range strings.Split(p.Channel, ",") {
		ch := s.net.Channel(name)
		if ch == nil || ch.Member(u) == nil {
			continue
		}
		if s.ours(u.UID) {
			s.emit(&state.Part{UID: u.UID, Channel: ch.Name})
		} else {
			s.sendRemote(u.UID, &link.SvsPart{Source: s.svcSource(), UID: u.UID, Channel: ch.Name})
		}
	}
	return true, nil
}

func (s *Server) rpcUserKill(rc *rpcConn, params json.RawMessage) (any, *rpcError) {
	var p struct {
		rpcNick
		Reason string `json:"reason"`
	}
	if e := rpcParams(params, &p); e != nil {
		return nil, e
	}
	u, e := s.rpcPerson(p.Nick)
	if e != nil {
		return nil, e
	}
	if p.Reason == "" {
		return nil, rpcErr(rpcInvalidParams, "Missing parameter: 'reason'")
	}
	s.killUser(u, rc.by(), p.Reason)
	return true, nil
}

func (s *Server) rpcUserQuit(rc *rpcConn, params json.RawMessage) (any, *rpcError) {
	var p struct {
		rpcNick
		Reason string `json:"reason"`
	}
	if e := rpcParams(params, &p); e != nil {
		return nil, e
	}
	u, e := s.rpcPerson(p.Nick)
	if e != nil {
		return nil, e
	}
	if p.Reason == "" {
		return nil, rpcErr(rpcInvalidParams, "Missing parameter: 'reason'")
	}
	reason := irc.Truncate(p.Reason, s.cfg.Limits.KickLen)
	if tc := s.clientOf(u); tc != nil {
		s.quit(tc, reason)
	} else {
		s.killUser(u, s.cfg.Server.Name, reason)
	}
	return true, nil
}

// ---- channel.* ----

func rpcListEntries(list []state.ListEntry) []obj {
	out := make([]obj, 0, len(list))
	for _, e := range list {
		out = append(out, obj{"name": e.Mask, "set_by": e.SetBy, "set_at": rpcTime(e.SetAt)})
	}
	return out
}

// rpcChannel is ch as UnrealIRCd's API shows it: full adds the lists and
// the members, and users (object_detail_level 4) makes each member a whole
// user, as unrealircd-webpanel shows them.
func (s *Server) rpcChannel(ch *state.Channel, full, users bool) obj {
	modes, args := ch.ModeString(true)
	o := obj{
		"name":          ch.Name,
		"creation_time": rpcTime(time.Unix(ch.TS, 0)),
		"num_users":     len(ch.Members),
		"modes":         strings.TrimSpace(strings.TrimPrefix(modes, "+") + " " + strings.Join(args, " ")),
	}
	if ch.Topic != "" {
		o["topic"], o["topic_set_by"], o["topic_set_at"] = ch.Topic, ch.TopicBy, rpcTime(ch.TopicAt)
	}
	if full {
		o["bans"] = rpcListEntries(ch.Lists['b'])
		o["ban_exemptions"] = rpcListEntries(ch.Lists['e'])
		o["invite_exceptions"] = rpcListEntries(ch.Lists['I'])
		members := make([]obj, 0, len(ch.Members))
		for _, mem := range ch.Members {
			m := obj{"name": mem.User.Nick, "id": mem.User.UID}
			if users {
				m = s.rpcUser(mem.User, false)
			}
			if l := rankLetters(mem.Ranks); l != "" {
				m["level"] = l
			}
			members = append(members, m)
		}
		sort.Slice(members, func(i, k int) bool { return members[i]["id"].(string) < members[k]["id"].(string) })
		o["members"] = members
	}
	return o
}

func (s *Server) rpcChannelList(rc *rpcConn, _ json.RawMessage) (any, *rpcError) {
	chans := s.net.Channels()
	sort.Slice(chans, func(i, k int) bool { return irc.Fold(chans[i].Name) < irc.Fold(chans[k].Name) })
	list := make([]obj, 0, len(chans))
	for _, ch := range chans {
		list = append(list, s.rpcChannel(ch, false, false))
	}
	return obj{"list": list}, nil
}

func (s *Server) rpcFindChannel(name string) (*state.Channel, *rpcError) {
	if name == "" {
		return nil, rpcErr(rpcInvalidParams, "Missing parameter: 'channel'")
	}
	if ch := s.net.Channel(name); ch != nil {
		return ch, nil
	}
	return nil, rpcErr(rpcNotFound, "Channel not found")
}

func (s *Server) rpcChannelGet(rc *rpcConn, params json.RawMessage) (any, *rpcError) {
	var p struct {
		Channel string `json:"channel"`
		Detail  *int   `json:"object_detail_level"`
	}
	if e := rpcParams(params, &p); e != nil {
		return nil, e
	}
	ch, e := s.rpcFindChannel(p.Channel)
	if e != nil {
		return nil, e
	}
	return obj{"channel": s.rpcChannel(ch, true, p.Detail != nil && *p.Detail >= 4)}, nil
}

func (s *Server) rpcChannelSetMode(rc *rpcConn, params json.RawMessage) (any, *rpcError) {
	var p struct {
		Channel    string `json:"channel"`
		Modes      string `json:"modes"`
		Parameters string `json:"parameters"`
	}
	if e := rpcParams(params, &p); e != nil {
		return nil, e
	}
	ch, e := s.rpcFindChannel(p.Channel)
	if e != nil {
		return nil, e
	}
	if p.Modes == "" {
		return nil, rpcErr(rpcInvalidParams, "Missing parameter: 'modes'")
	}
	// Carried out as the server, with an oper's rights; what an IRC client
	// would be told comes back as the error.
	var told []string
	c := &Client{s: s, registered: true, nick: "*", caps: map[string]bool{}, operFlags: allFlags, capture: &told}
	src := &state.User{Nick: s.cfg.Server.Name, Service: true}
	s.applyChannelModes(c, src, ch, servicesRank, p.Modes, strings.Fields(p.Parameters))
	for _, line := range told {
		if m, err := irc.Parse(line); err == nil && len(m.Params) > 1 && len(m.Command) == 3 && m.Command[0] >= '4' {
			return nil, rpcErr(rpcInvalidParams, "%s", strings.Join(m.Params[1:], " "))
		}
	}
	return true, nil
}

func (s *Server) rpcChannelSetTopic(rc *rpcConn, params json.RawMessage) (any, *rpcError) {
	var p struct {
		Channel string `json:"channel"`
		Topic   string `json:"topic"`
		SetBy   string `json:"set_by"`
		SetAt   string `json:"set_at"`
	}
	if e := rpcParams(params, &p); e != nil {
		return nil, e
	}
	ch, e := s.rpcFindChannel(p.Channel)
	if e != nil {
		return nil, e
	}
	at := time.Now()
	if p.SetAt != "" {
		t, err := time.Parse(time.RFC3339, p.SetAt)
		if err != nil {
			return nil, rpcErr(rpcInvalidParams, "'set_at' is a time like 2026-01-31T12:00:00.000Z")
		}
		at = t
	}
	by := p.SetBy
	if by == "" || strings.ContainsAny(by, " \r\n") {
		by = rc.by()
	}
	s.emit(&state.Topic{Channel: ch.Name, Text: irc.Truncate(p.Topic, s.cfg.Limits.TopicLen), By: by, At: at})
	return true, nil
}

func (s *Server) rpcChannelKick(rc *rpcConn, params json.RawMessage) (any, *rpcError) {
	var p struct {
		Channel string `json:"channel"`
		Nick    string `json:"nick"`
		Reason  string `json:"reason"`
	}
	if e := rpcParams(params, &p); e != nil {
		return nil, e
	}
	ch, e := s.rpcFindChannel(p.Channel)
	if e != nil {
		return nil, e
	}
	u, e := s.rpcPerson(p.Nick)
	switch {
	case e != nil:
		return nil, e
	case ch.Member(u) == nil:
		return nil, rpcErr(rpcNotFound, "%s is not on %s", u.Nick, ch.Name)
	}
	reason := irc.Truncate(p.Reason, s.cfg.Limits.KickLen)
	if reason == "" {
		reason = rc.by()
	}
	s.emit(&state.Kick{UID: u.UID, Channel: ch.Name, Reason: reason})
	return true, nil
}

// ---- server.* ----

func (s *Server) rpcServer(n netServer, people map[string]int) obj {
	srv := obj{"info": n.desc, "num_users": people[n.sid]}
	if n.uplink != "" {
		srv["uplink"] = s.sidName(n.uplink)
	}
	if n.sid == s.cfg.Server.SID {
		srv["boot_time"] = rpcTime(s.created)
		// UnrealIRCd's features: unrealircd-webpanel builds its channel mode
		// editor from chanmodes (the four CHANMODES groups).
		srv["features"] = obj{
			"software":  version.String(),
			"protocol":  link.UnrealProtocol,
			"usermodes": userModes,
			"chanmodes": []string{chanModesList, chanModesKey, chanModesParam, chanModesFlags},
		}
	}
	// "ip" is null as UnrealIRCd sends it for its own server (we do not
	// keep link addresses); without the key unrealircd-webpanel warns.
	return obj{"name": n.name, "id": n.sid, "ip": nil, "server": srv}
}

func (s *Server) rpcPeoplePerServer() map[string]int {
	out := map[string]int{}
	for _, u := range s.net.Users() {
		out[uidServer(u.UID)]++
	}
	return out
}

func (s *Server) rpcServerList(rc *rpcConn, _ json.RawMessage) (any, *rpcError) {
	people := s.rpcPeoplePerServer()
	list := []obj{}
	for _, n := range s.netServers() {
		list = append(list, s.rpcServer(n, people))
	}
	return obj{"list": list}, nil
}

func (s *Server) rpcServerGet(rc *rpcConn, params json.RawMessage) (any, *rpcError) {
	var p struct {
		Server string `json:"server"`
	}
	if e := rpcParams(params, &p); e != nil {
		return nil, e
	}
	for _, n := range s.netServers() {
		if p.Server == "" || strings.EqualFold(p.Server, n.name) || p.Server == n.sid {
			return obj{"server": s.rpcServer(n, s.rpcPeoplePerServer())}, nil
		}
	}
	return nil, rpcErr(rpcNotFound, "Server not found")
}

func (s *Server) rpcServerRehash(rc *rpcConn, params json.RawMessage) (any, *rpcError) {
	var p struct {
		Server string `json:"server"`
	}
	if e := rpcParams(params, &p); e != nil {
		return nil, e
	}
	if p.Server != "" && !strings.EqualFold(p.Server, s.cfg.Server.Name) && p.Server != s.cfg.Server.SID {
		return nil, rpcErr(rpcRefused, "Only the server you are calling can be rehashed: ask %s itself", p.Server)
	}
	r, err := s.rehash("RPC " + rc.by())
	if err != nil {
		return obj{"success": false, "log": []string{err.Error()}}, nil
	}
	return obj{"success": true, "log": []string{rehashSummary(r)}}, nil
}

func (s *Server) rpcModuleList(rc *rpcConn, _ json.RawMessage) (any, *rpcError) {
	// Everything is built in, so the list is the server itself.
	return obj{"list": []obj{{"name": "beeircd", "description": "BeeIRCd: every feature is built in, there are no loadable modules",
		"author": "BajuBesi Labs", "version": version.String(), "third_party": false, "permanent": true}}}, nil
}

// ---- server_ban.* and server_ban_exception.* ----

// rpcBanTypes are UnrealIRCd's ban types and ours. A Z-line is always
// network-wide here.
var rpcBanTypes = map[string]string{"gline": store.BanAkill, "gzline": store.BanZline, "zline": store.BanZline, "shun": store.BanShun, "kline": store.BanKline}

var rpcBanNames = map[string][2]string{
	store.BanAkill: {"gline", "G-Line"}, store.BanZline: {"gzline", "Global Z-Line"}, store.BanShun: {"shun", "Shun"},
	store.BanKline: {"kline", "K-Line"}, store.BanEline: {"except", "Exception"},
}

func durShortWords(d time.Duration) string {
	if d <= 0 {
		return "permanent"
	}
	return durShort(d)
}

// rpcStamps adds set_at_string and expire_at_string as UnrealIRCd writes
// them (asctime in UTC, "Never" for no end): unrealircd-webpanel shows
// these in its Set On and Expires columns.
func rpcStamps(o obj, setAt, expires time.Time) {
	o["set_at_string"] = setAt.UTC().Format(time.ANSIC)
	o["expire_at_string"] = "Never"
	if !expires.IsZero() {
		o["expire_at_string"] = expires.UTC().Format(time.ANSIC)
	}
}

func rpcBan(b store.NetBan) obj {
	n := rpcBanNames[b.Kind]
	o := obj{
		"type":            n[0],
		"type_string":     n[1],
		"name":            b.Mask,
		"reason":          b.Reason,
		"set_by":          b.SetBy,
		"set_at":          rpcTime(b.SetAt),
		"set_in_config":   false,
		"duration_string": "permanent",
	}
	if !b.Expires.IsZero() {
		o["expire_at"] = rpcTime(b.Expires)
		o["duration_string"] = durShortWords(b.Expires.Sub(b.SetAt))
	}
	if b.Kind == store.BanEline {
		o["exception_types"] = "kGzZs"
	}
	rpcStamps(o, b.SetAt, b.Expires)
	return o
}

// rpcBans are the live bans of kind.
func (s *Server) rpcBans(kind string) []store.NetBan {
	if kind == store.BanKline {
		return s.svc.Klines()
	}
	return s.svc.PanelBans(kind)
}

func (s *Server) rpcBanList(rc *rpcConn, _ json.RawMessage) (any, *rpcError) {
	list := []obj{}
	for _, kind := range []string{store.BanAkill, store.BanKline, store.BanZline, store.BanShun} {
		for _, b := range s.rpcBans(kind) {
			list = append(list, rpcBan(b))
		}
	}
	return obj{"list": list}, nil
}

type rpcBanParams struct {
	Name           string `json:"name"`
	Type           string `json:"type"`
	Reason         string `json:"reason"`
	DurationString string `json:"duration_string"`
	ExpireAt       string `json:"expire_at"`
	SetBy          string `json:"set_by"`
}

// kind reads p.Type; exception: the exception methods have no type.
func (p rpcBanParams) kind(exception bool) (string, *rpcError) {
	switch {
	case p.Name == "":
		return "", rpcErr(rpcInvalidParams, "Missing parameter: 'name'")
	case exception:
		return store.BanEline, nil
	case p.Type == "":
		return "", rpcErr(rpcInvalidParams, "Missing parameter: 'type'")
	}
	kind, ok := rpcBanTypes[strings.ToLower(p.Type)]
	if !ok {
		return "", rpcErr(rpcInvalidParams, "Invalid type: '%s' (gline, kline, gzline, zline or shun)", p.Type)
	}
	return kind, nil
}

// life is how long the ban holds: 0 = until removed.
func (p rpcBanParams) life() (time.Duration, *rpcError) {
	switch {
	case p.ExpireAt != "":
		t, err := time.Parse(time.RFC3339, p.ExpireAt)
		if err != nil || !t.After(time.Now()) {
			return 0, rpcErr(rpcInvalidParams, "'expire_at' is a time in the future like 2026-01-31T12:00:00.000Z")
		}
		return time.Until(t), nil
	case p.DurationString == "" || p.DurationString == "0" || strings.EqualFold(p.DurationString, "permanent"):
		return 0, nil
	}
	if n, err := strconv.Atoi(p.DurationString); err == nil && n > 0 {
		return time.Duration(n) * time.Second, nil
	}
	// UnrealIRCd's form joins units: unrealircd-webpanel sends 0w1d0h, and
	// 0w0d0h for until removed.
	if d, err := parseShortDuration(p.DurationString); err == nil {
		return d, nil
	}
	if strings.Trim(strings.ToLower(p.DurationString), "0wdhms") == "" && strings.Contains(p.DurationString, "0") {
		return 0, nil
	}
	d, err := parseLife(p.DurationString)
	if err != nil {
		return 0, rpcErr(rpcInvalidParams, "Invalid duration_string: use 30m, 12h, 7d, seconds, or 0 for until removed")
	}
	return d, nil
}

// rpcFindBan finds the live ban of kind on name.
func (s *Server) rpcFindBan(kind, name string) (store.NetBan, bool) {
	for _, b := range s.rpcBans(kind) {
		if strings.EqualFold(b.Mask, name) || strings.EqualFold(b.Mask, "*@"+name) {
			return b, true
		}
	}
	return store.NetBan{}, false
}

func (s *Server) rpcBanGetKind(params json.RawMessage, exception bool) (any, *rpcError) {
	var p rpcBanParams
	if e := rpcParams(params, &p); e != nil {
		return nil, e
	}
	kind, e := p.kind(exception)
	if e != nil {
		return nil, e
	}
	b, ok := s.rpcFindBan(kind, p.Name)
	if !ok {
		return nil, rpcErr(rpcNotFound, "Ban not found")
	}
	return obj{"tkl": rpcBan(b)}, nil
}

func (s *Server) rpcBanAddKind(rc *rpcConn, params json.RawMessage, exception bool) (any, *rpcError) {
	var p rpcBanParams
	if e := rpcParams(params, &p); e != nil {
		return nil, e
	}
	kind, e := p.kind(exception)
	if e != nil {
		return nil, e
	}
	life, e := p.life()
	if e != nil {
		return nil, e
	}
	if p.Reason == "" {
		return nil, rpcErr(rpcInvalidParams, "Missing parameter: 'reason'")
	}
	by := rc.by()
	if p.SetBy != "" && !strings.ContainsAny(p.SetBy, " \r\n") && len(p.SetBy) <= 64 {
		by = p.SetBy
	}
	if _, there := s.rpcFindBan(kind, p.Name); there {
		return nil, rpcErr(rpcExists, "A ban with that mask already exists")
	}
	if kind == store.BanKline {
		mask := strings.ToLower(p.Name)
		user, hostPart, ok := strings.Cut(mask, "@")
		if !ok || user == "" || hostPart == "" || strings.Trim(hostPart, "*?.") == "" {
			return nil, rpcErr(rpcInvalidName, "A K-line is on user@host with a real host part")
		}
		if err := s.svc.AddKline(mask, p.Reason, by, life); err != nil {
			s.log.Printf("services database: %v", err)
			return nil, rpcErr(rpcInternal, "The K-line couldn't be saved")
		}
		for _, o := range s.clients {
			if u := o.user; u != nil && !o.closed && !u.Service && !o.isOper() && irc.MatchUserHost(mask, u.TrueIdent(), u.RealHost, u.IP) {
				s.reply(o, irc.ErrYoureBannedCreep, "You are banned from this server: "+p.Reason)
				s.quit(o, "K-Lined: "+p.Reason)
			}
		}
	} else if _, _, problem := s.svc.BanVia(by, "RPC", kind, p.Name, p.Reason, life); problem != "" {
		return nil, rpcErr(rpcInvalidName, "%s", problem)
	}
	b, ok := s.rpcFindBan(kind, p.Name)
	if !ok {
		// Stored under the mask the server made of the name.
		if list := s.rpcBans(kind); len(list) > 0 {
			sort.SliceStable(list, func(i, k int) bool { return list[i].SetAt.After(list[k].SetAt) })
			b = list[0]
		}
	}
	return obj{"tkl": rpcBan(b)}, nil
}

func (s *Server) rpcBanDelKind(rc *rpcConn, params json.RawMessage, exception bool) (any, *rpcError) {
	var p rpcBanParams
	if e := rpcParams(params, &p); e != nil {
		return nil, e
	}
	kind, e := p.kind(exception)
	if e != nil {
		return nil, e
	}
	b, ok := s.rpcFindBan(kind, p.Name)
	if !ok {
		return nil, rpcErr(rpcNotFound, "Ban not found")
	}
	if kind == store.BanKline {
		if gone, err := s.svc.DelKline(b.Mask); err != nil || !gone {
			return nil, rpcErr(rpcInternal, "The K-line couldn't be removed")
		}
	} else if _, problem := s.svc.BanDelVia(rc.by(), "RPC", kind, b.Mask); problem != "" {
		return nil, rpcErr(rpcInternal, "%s", problem)
	}
	return obj{"tkl": rpcBan(b)}, nil
}

func (s *Server) rpcBanGet(rc *rpcConn, p json.RawMessage) (any, *rpcError) {
	return s.rpcBanGetKind(p, false)
}
func (s *Server) rpcBanAdd(rc *rpcConn, p json.RawMessage) (any, *rpcError) {
	return s.rpcBanAddKind(rc, p, false)
}
func (s *Server) rpcBanDel(rc *rpcConn, p json.RawMessage) (any, *rpcError) {
	return s.rpcBanDelKind(rc, p, false)
}

func (s *Server) rpcExceptList(rc *rpcConn, _ json.RawMessage) (any, *rpcError) {
	list := []obj{}
	for _, b := range s.rpcBans(store.BanEline) {
		list = append(list, rpcBan(b))
	}
	return obj{"list": list}, nil
}
func (s *Server) rpcExceptGet(rc *rpcConn, p json.RawMessage) (any, *rpcError) {
	return s.rpcBanGetKind(p, true)
}
func (s *Server) rpcExceptAdd(rc *rpcConn, p json.RawMessage) (any, *rpcError) {
	return s.rpcBanAddKind(rc, p, true)
}
func (s *Server) rpcExceptDel(rc *rpcConn, p json.RawMessage) (any, *rpcError) {
	return s.rpcBanDelKind(rc, p, true)
}

// ---- name_ban.* ----
// UnrealIRCd's name bans (Q-lines) are our nick and channel forbids
// (/os sqline, /os forbid chan, /qline). Q-lines that came over a link
// from an UnrealIRCd server are listed too, but are removed there.

func rpcNameBan(name, reason, setBy string, setAt, expires time.Time) obj {
	o := obj{
		"type":            "qline",
		"type_string":     "Q-Line",
		"name":            name,
		"reason":          reason,
		"set_by":          setBy,
		"set_at":          rpcTime(setAt),
		"set_in_config":   false,
		"duration_string": "permanent",
	}
	if !expires.IsZero() {
		o["expire_at"] = rpcTime(expires)
		o["duration_string"] = durShortWords(expires.Sub(setAt))
	}
	rpcStamps(o, setAt, expires)
	return o
}

// rpcNameBans are ours, then the live ones from links.
func (s *Server) rpcNameBans() []obj {
	list := []obj{}
	for _, f := range s.svc.NameBans() {
		list = append(list, rpcNameBan(f.Name, f.Reason, f.SetBy, f.SetAt, f.Expires))
	}
	now := time.Now().Unix()
	for _, q := range s.links.qlines {
		if q.expires == 0 || q.expires > now {
			var exp time.Time
			if q.expires > 0 {
				exp = time.Unix(q.expires, 0)
			}
			list = append(list, rpcNameBan(q.mask, q.reason, q.setBy, time.Unix(q.setAt, 0), exp))
		}
	}
	return list
}

func (s *Server) rpcFindNameBan(name string) (obj, bool) {
	for _, o := range s.rpcNameBans() {
		if irc.Fold(o["name"].(string)) == irc.Fold(name) {
			return o, true
		}
	}
	return nil, false
}

func (s *Server) rpcNameBanList(rc *rpcConn, _ json.RawMessage) (any, *rpcError) {
	return obj{"list": s.rpcNameBans()}, nil
}

func (s *Server) rpcNameBanGet(rc *rpcConn, params json.RawMessage) (any, *rpcError) {
	var p rpcBanParams
	if e := rpcParams(params, &p); e != nil {
		return nil, e
	}
	if p.Name == "" {
		return nil, rpcErr(rpcInvalidParams, "Missing parameter: 'name'")
	}
	o, ok := s.rpcFindNameBan(p.Name)
	if !ok {
		return nil, rpcErr(rpcNotFound, "Ban not found")
	}
	return obj{"tkl": o}, nil
}

func (s *Server) rpcNameBanAdd(rc *rpcConn, params json.RawMessage) (any, *rpcError) {
	var p rpcBanParams
	if e := rpcParams(params, &p); e != nil {
		return nil, e
	}
	if p.Name == "" {
		return nil, rpcErr(rpcInvalidParams, "Missing parameter: 'name'")
	}
	life, e := p.life()
	if e != nil {
		return nil, e
	}
	if p.Reason == "" {
		return nil, rpcErr(rpcInvalidParams, "Missing parameter: 'reason'")
	}
	if s.svc.Off() {
		return nil, rpcErr(rpcInternal, "Name bans are kept by the services, which are off on this server")
	}
	if _, there := s.rpcFindNameBan(p.Name); there {
		return nil, rpcErr(rpcExists, "A ban with that name already exists")
	}
	by := rc.by()
	if p.SetBy != "" && !strings.ContainsAny(p.SetBy, " \r\n") && len(p.SetBy) <= 64 {
		by = p.SetBy
	}
	if _, problem := s.svc.NameBanVia(by, "RPC", p.Name, p.Reason, life); problem != "" {
		return nil, rpcErr(rpcInvalidName, "%s", problem)
	}
	o, ok := s.rpcFindNameBan(p.Name)
	if !ok {
		return nil, rpcErr(rpcInternal, "The name ban couldn't be saved")
	}
	return obj{"tkl": o}, nil
}

func (s *Server) rpcNameBanDel(rc *rpcConn, params json.RawMessage) (any, *rpcError) {
	var p rpcBanParams
	if e := rpcParams(params, &p); e != nil {
		return nil, e
	}
	if p.Name == "" {
		return nil, rpcErr(rpcInvalidParams, "Missing parameter: 'name'")
	}
	o, ok := s.rpcFindNameBan(p.Name)
	if !ok {
		return nil, rpcErr(rpcNotFound, "Ban not found")
	}
	if problem := s.svc.NameBanDelVia(rc.by(), "RPC", o["name"].(string)); problem != "" {
		// Not ours: a Q-line from a linked UnrealIRCd server.
		return nil, rpcErr(rpcDenied, "%s was set on another server: remove it there", o["name"])
	}
	return obj{"tkl": o}, nil
}

// ---- spamfilter.* ----

func (s *Server) rpcSpam(f spamfilter.Filter) obj {
	h := s.svc.SpamHitsOf(f.ID)
	o := obj{
		"type":                "spamfilter",
		"type_string":         "Spamfilter",
		"name":                f.Match,
		"match_type":          f.Type,
		"ban_action":          f.Action,
		"ban_duration":        int64(f.BanTime / time.Second),
		"ban_duration_string": durShortWords(f.BanTime),
		"spamfilter_targets":  f.Targets,
		"reason":              f.Reason,
		"set_by":              f.SetBy,
		"set_at":              rpcTime(f.SetAt),
		"set_in_config":       false,
		"hits":                h.Count,
	}
	if !f.Expires.IsZero() {
		o["expire_at"] = rpcTime(f.Expires)
	}
	rpcStamps(o, f.SetAt, f.Expires)
	return o
}

type rpcSpamParams struct {
	Name        string `json:"name"`
	MatchType   string `json:"match_type"`
	Targets     string `json:"spamfilter_targets"`
	BanAction   string `json:"ban_action"`
	BanDuration string `json:"ban_duration"`
	Reason      string `json:"reason"`
	SetBy       string `json:"set_by"`
}

// find is the rule p names: the same match, type, targets and action.
func (s *Server) rpcFindSpam(p rpcSpamParams) (spamfilter.Filter, *rpcError) {
	for _, name := range []struct{ v, n string }{{p.Name, "name"}, {p.MatchType, "match_type"}, {p.Targets, "spamfilter_targets"}, {p.BanAction, "ban_action"}} {
		if name.v == "" {
			return spamfilter.Filter{}, rpcErr(rpcInvalidParams, "Missing parameter: '%s'", name.n)
		}
	}
	targets, err := spamfilter.NormTargets(p.Targets)
	if err != nil {
		return spamfilter.Filter{}, rpcErr(rpcInvalidParams, "Invalid spamfilter_targets: %v", err)
	}
	for _, f := range s.svc.SpamFilters() {
		if f.Match == p.Name && strings.EqualFold(f.Type, p.MatchType) && f.Targets == targets && strings.EqualFold(f.Action, p.BanAction) {
			return f, nil
		}
	}
	return spamfilter.Filter{}, rpcErr(rpcNotFound, "Spamfilter not found")
}

func (s *Server) rpcSpamList(rc *rpcConn, _ json.RawMessage) (any, *rpcError) {
	list := []obj{}
	for _, f := range s.svc.SpamFilters() {
		list = append(list, s.rpcSpam(f))
	}
	return obj{"list": list}, nil
}

func (s *Server) rpcSpamGet(rc *rpcConn, params json.RawMessage) (any, *rpcError) {
	var p rpcSpamParams
	if e := rpcParams(params, &p); e != nil {
		return nil, e
	}
	f, e := s.rpcFindSpam(p)
	if e != nil {
		return nil, e
	}
	return obj{"tkl": s.rpcSpam(f)}, nil
}

func (s *Server) rpcSpamAdd(rc *rpcConn, params json.RawMessage) (any, *rpcError) {
	var p rpcSpamParams
	if e := rpcParams(params, &p); e != nil {
		return nil, e
	}
	if _, e := s.rpcFindSpam(p); e == nil {
		return nil, rpcErr(rpcExists, "A spamfilter with that regex+action+target already exists")
	} else if e.Code != rpcNotFound {
		return nil, e
	}
	if p.Reason == "" {
		return nil, rpcErr(rpcInvalidParams, "Missing parameter: 'reason'")
	}
	f := spamfilter.Filter{Type: strings.ToLower(p.MatchType), Match: p.Name, Targets: p.Targets, Action: strings.ToLower(p.BanAction), Reason: p.Reason}
	if spamfilter.Bans(f.Action) && p.BanDuration != "" && p.BanDuration != "0" {
		d, err := spamDuration(p.BanDuration)
		if err != nil {
			return nil, rpcErr(rpcInvalidParams, "Invalid ban_duration: %v", err)
		}
		f.BanTime = d
	}
	by := rc.by()
	if p.SetBy != "" && !strings.ContainsAny(p.SetBy, " \r\n") && len(p.SetBy) <= 64 {
		by = p.SetBy
	}
	// A tool can't be asked "add it anyway?": a rule that would also catch
	// normal chat is refused, as for an oper who says no.
	got, err := s.svc.AddSpamFilter(f, by, false)
	var unsafe *services.SafetyError
	switch {
	case errors.As(err, &unsafe):
		return nil, rpcErr(rpcRefused, "That rule would also catch normal chat (like '%s'); narrow the match", strings.Join(unsafe.Lines, "', '"))
	case errors.Is(err, services.ErrSpamDuplicate):
		return nil, rpcErr(rpcExists, "A spamfilter with that regex+action+target already exists")
	case err != nil:
		return nil, rpcErr(rpcInvalidParams, "%s", upperFirst(err.Error()))
	}
	return obj{"tkl": s.rpcSpam(got)}, nil
}

func (s *Server) rpcSpamDel(rc *rpcConn, params json.RawMessage) (any, *rpcError) {
	var p rpcSpamParams
	if e := rpcParams(params, &p); e != nil {
		return nil, e
	}
	f, e := s.rpcFindSpam(p)
	if e != nil {
		return nil, e
	}
	o := s.rpcSpam(f)
	if _, err := s.svc.DelSpamFilter(f.ID, rc.by()); err != nil {
		return nil, rpcErr(rpcInternal, "%s", upperFirst(err.Error()))
	}
	return obj{"tkl": o}, nil
}

// ---- whowas.get ----

func (s *Server) rpcWhowas(rc *rpcConn, params json.RawMessage) (any, *rpcError) {
	var p struct {
		Nick string `json:"nick"`
		IP   string `json:"ip"`
	}
	if e := rpcParams(params, &p); e != nil {
		return nil, e
	}
	if p.Nick == "" && p.IP == "" {
		return nil, rpcErr(rpcInvalidParams, "Missing parameter: 'nick' or 'ip'")
	}
	list := []obj{}
	for i := len(s.whowas) - 1; i >= 0 && len(list) < 100; i-- {
		e := s.whowas[i]
		if p.Nick != "" && !irc.Match(p.Nick, e.nick) || p.IP != "" && !irc.Match(p.IP, e.host) {
			continue
		}
		list = append(list, obj{"name": e.nick, "event": "quit", "logoff_time": rpcTime(e.at),
			"user": obj{"username": e.ident, "realname": e.realname, "vhost": e.host, "servername": e.server}})
	}
	return obj{"list": list}, nil
}

// ---- log.* ----

type rpcLogParams struct {
	Sources []string `json:"sources"`
}

func (s *Server) rpcLogSubscribe(rc *rpcConn, params json.RawMessage) (any, *rpcError) {
	var p rpcLogParams
	if e := rpcParams(params, &p); e != nil {
		return nil, e
	}
	if rc.out == nil {
		return nil, rpcErr(rpcInvalidRequest, "The live log needs a connection that stays open: a WebSocket or the socket file")
	}
	if len(p.Sources) == 0 {
		return nil, rpcErr(rpcInvalidParams, "Missing parameter: 'sources' (like [\"all\"])")
	}
	rc.logSources = p.Sources
	s.rpc.subs[rc] = true
	return true, nil
}

func (s *Server) rpcLogUnsubscribe(rc *rpcConn, _ json.RawMessage) (any, *rpcError) {
	delete(s.rpc.subs, rc)
	rc.logSources = nil
	return true, nil
}

func (s *Server) rpcLogList(rc *rpcConn, params json.RawMessage) (any, *rpcError) {
	var p rpcLogParams
	if e := rpcParams(params, &p); e != nil {
		return nil, e
	}
	list := []rpcLogEvent{}
	for _, ev := range s.rpc.recent {
		if rpcLogWanted(p.Sources, ev) {
			list = append(list, ev)
		}
	}
	return obj{"list": list}, nil
}
