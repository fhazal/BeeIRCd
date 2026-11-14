package server

import (
	"encoding/base64"
	"strings"
	"time"

	"github.com/fhazal/beeircd/internal/irc"
	"github.com/fhazal/beeircd/internal/link"
	"github.com/fhazal/beeircd/internal/store"
)

// Network bans over links (Stage 2c): AKILL = G-line, ZLINE = GZ-line,
// SHUN = shun, ELINE = ban exception, as UnrealIRCd's TKL. One list per
// server (the services database), kept the same everywhere: each change
// goes to every link, the burst sends the whole list, and every server
// enforces it on its own users.

var banTypes = map[string]byte{
	store.BanAkill:  link.BanGline,
	store.BanZline:  link.BanGZline,
	store.BanShun:   link.BanShun,
	store.BanEline:  link.BanException,
	store.BanSNLine: link.BanRealname, // BeeIRCd links only
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
	switch b.Kind {
	case store.BanZline:
		it.User, it.Host = "*", b.Mask
	case store.BanSNLine:
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
		if it.Type == link.BanRealname && !o.peer.Bee {
			continue // UnrealIRCd has no realname TKL
		}
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
		if b.Kind == store.BanSNLine && !l.peer.Bee {
			continue
		}
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
	nb := store.NetBan{Kind: kind, Mask: strings.ToLower(mask), Reason: irc.Truncate(b.Reason, 200), SetBy: b.SetBy, SetAt: time.Unix(b.SetAt, 0)}
	if b.SetAt == 0 {
		nb.SetAt = time.Now()
	}
	if b.Expires > 0 {
		nb.Expires = time.Unix(b.Expires, 0)
	}
	s.banFrom = l
	changed := s.svc.RemoteBan(nb, b.Add)
	s.banFrom = nil
	if changed && l.eos {
		how := "added"
		if !b.Add {
			how = "removed"
		}
		s.snotice("%s %s %s by %s (from %s): %s", strings.ToUpper(kind), nb.Mask, how, nb.SetBy, l.peer.Name, nb.Reason)
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
	for _, c := range s.clients {
		if c.user == nil || c.closed || c.user.Service {
			continue
		}
		u := c.user
		if k := s.svc.ConnectBan(u.TrueIdent(), u.RealHost, u.IP, u.Realname); k != nil {
			hit = append(hit, c)
		}
	}
	for _, c := range hit {
		s.reply(c, irc.ErrYoureBannedCreep, "You are banned from this network: "+b.Reason)
		s.quit(c, "Banned: "+b.Reason)
	}
}
