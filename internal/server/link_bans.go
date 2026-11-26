package server

import (
	"encoding/base64"
	"strings"
	"time"

	"github.com/fhazal/beeircd/internal/irc"
	"github.com/fhazal/beeircd/internal/link"
	"github.com/fhazal/beeircd/internal/store"
)

// Network bans over links: AKILL = G-line, ZLINE = GZ-line,
// SHUN = shun, ELINE = ban exception, as UnrealIRCd's TKL. One list per
// server (the services database), kept the same everywhere: each change
// goes to every link, the burst sends the whole list, and every server
// enforces it on its own users.

var banTypes = map[string]byte{
	store.BanAkill:  link.BanGline,
	store.BanZline:  link.BanGZline,
	store.BanShun:   link.BanShun,
	store.BanEline:  link.BanException,
	store.BanSNLine: link.BanRealname, // UnrealIRCd and InspIRCd get it as ~realname: / R-line
}

func banKind(t byte) string {
	for k, v := range banTypes {
		if v == t {
			return k
		}
	}
	return ""
}

// banItem writes a ban for the wire.
func (s *Server) banItem(b store.NetBan, add bool) *link.Ban {
	it := &link.Ban{Source: s.cfg.Server.SID, Add: add, Type: banTypes[b.Kind], SetBy: orStr(b.SetBy, s.cfg.Server.Name),
		SetAt: b.SetAt.Unix(), Reason: orStr(b.Reason, "No reason")}
	if !b.Expires.IsZero() {
		it.Expires = b.Expires.Unix()
	}
	switch {
	case irc.IsPlaceMask(b.Mask): // ~C:NL travels as ~C: NL
		it.User, it.Host = irc.SplitServerExtban(b.Mask, false)
	case b.Kind == store.BanZline:
		it.User, it.Host = "*", b.Mask
	case b.Kind == store.BanSNLine:
		it.User, it.Host = "*", base64.RawURLEncoding.EncodeToString([]byte(b.Mask))
	default:
		it.User, it.Host, _ = strings.Cut(b.Mask, "@")
		if it.Host == "" {
			it.User, it.Host = "*", b.Mask
		}
	}
	if b.Kind == store.BanEline {
		it.Exempt = "GZs" // what an ELINE lifts here: AKILL, ZLINE, SHUN
	}
	return it
}

// linkBanChanged sends a ban change to every link but the one it came
// from (services call it, see OnBanChange).
func (s *Server) linkBanChanged(b store.NetBan, add bool) {
	it := s.banItem(b, add)
	for _, o := range s.upLinks(s.banFrom) {
		for _, line := range o.codecOf().Encode(it) {
			o.send(line)
		}
	}
	if add && s.banFrom == nil {
		return // our own: services already removed whoever it hits
	}
	if add {
		s.enforceBan(b)
	}
}

// banBurst is the ban list for a new link.
func (s *Server) banBurst(l *linkConn) []string {
	var out []string
	cd := l.codecOf()
	for _, b := range s.svc.LinkedBans() {
		out = append(out, cd.Encode(s.banItem(b, true))...)
	}
	out = append(out, s.qlineBurst(l)...)
	out = append(out, s.spamBurst(l)...)
	return out
}

// remoteBan is a TKL from l.
func (s *Server) remoteBan(l *linkConn, raw irc.Message, b *link.Ban) {
	if b.Type == link.BanQline {
		s.remoteQline(l, raw, b)
		return
	}
	if b.Type == link.BanRealname {
		if m, err := base64.RawURLEncoding.DecodeString(b.Host); err == nil {
			b.User, b.Host = "", string(m)
		} else {
			return
		}
	}
	kind := banKind(b.Type)
	ext := ""
	if irc.IsPlaceUser(b.User) {
		// UnrealIRCd's extended server ban: ~country: NL. The ones we
		// don't know pass on like the kinds we don't keep.
		var ok bool
		if ext, ok = irc.ServerExtban(b.User + b.Host); !ok {
			b.Unsupported = true
		}
	}
	if b.Unsupported || kind == "" {
		// Nick bans, spamfilters ...: not ours to keep, but the servers
		// further on may be UnrealIRCd.
		for _, o := range s.upLinks(l) {
			o.send(raw.String())
		}
		return
	}
	mask := b.Host
	if kind != store.BanZline && kind != store.BanSNLine {
		mask = orStr(b.User, "*") + "@" + b.Host
	}
	if kind != store.BanSNLine {
		// An SNLINE keeps its case: lowercasing a /regex/ changes it.
		mask = strings.ToLower(mask)
	}
	if ext != "" {
		mask = ext
	}
	nb := store.NetBan{Kind: kind, Mask: mask, Reason: irc.Truncate(b.Reason, 200), SetBy: b.SetBy, SetAt: time.Unix(b.SetAt, 0)}
	if b.SetAt == 0 {
		nb.SetAt = time.Now()
	}
	if b.Expires > 0 {
		nb.Expires = time.Unix(b.Expires, 0)
	}
	if !b.Add && nb.Reason == "" {
		// InspIRCd's DELLINE has no reason: the ban we keep has it.
		for _, have := range s.svc.LinkedBans() {
			if have.Kind == kind && strings.EqualFold(have.Mask, mask) {
				nb.Reason = have.Reason
				break
			}
		}
	}
	s.banFrom = l
	changed, stale := s.svc.RemoteBan(nb, b.Add)
	s.banFrom = nil
	if stale {
		// We removed this ban while l was split off, and l (or a server
		// behind it) still has it: tell l to remove it too, rather than
		// take it back. Only l: everyone else already had the removal.
		// Each side drops its copy, so nothing comes back here.
		s.log.Printf("link %s: it still has the %s on %s, removed here while it was split off; asked it to remove it too", l.peer.Name, store.BanWord(kind), nb.Mask)
		del := nb
		del.SetBy = s.cfg.Server.Name
		for _, line := range l.codecOf().Encode(s.banItem(del, false)) {
			l.send(line)
		}
		return
	}
	if changed && l.eos {
		// UnrealIRCd names the setter nick!user@host; the notice, like
		// InspIRCd's, names the nick.
		by, _, _ := strings.Cut(nb.SetBy, "!")
		switch {
		case !b.Add:
			s.snoBanRemoved('X', by, kind, nb.Mask, nb.Reason)
		default:
			s.snoBanAdded('X', by, kind, nb.Mask, nb.Reason, nb.SetAt, nb.Expires)
		}
	}
}

// enforceBan removes our users a ban from another server hits.
func (s *Server) enforceBan(b store.NetBan) {
	switch b.Kind {
	case store.BanAkill, store.BanZline, store.BanSNLine:
	default:
		return
	}
	var hit []*Client
	var bans []*store.NetBan
	for _, c := range s.clients {
		if c.user == nil || c.closed || c.user.Service {
			continue
		}
		u := c.user
		if k := s.svc.ConnectBan(u.TrueIdent(), u.RealHost, u.IP, u.Realname, u.Account != ""); k != nil {
			hit = append(hit, c)
			bans = append(bans, k)
		}
	}
	for i, c := range hit {
		k := bans[i]
		s.svc.LogBanHit(k.Kind, k.Mask, c.logMask(c.nick), k.SetBy, k.Reason)
		s.reply(c, irc.ErrYoureBannedCreep, s.cfg.Messages.BanText("You are banned from this network: "+b.Reason))
		s.banQuit(c, "Banned", b.Reason)
	}
}
