package link

import (
	"encoding/base64"
	"fmt"
	"net"
	"sort"
	"strconv"
	"strings"

	"github.com/fhazal/beeircd/internal/irc"
)

// unrealCodec reads and writes the UnrealIRCd 6 burst lines (src/modules/
// server.c introduce_user + send_channel_modes_sjoin3, src/modules/
// sjoin.c, src/modules/nick.c cmd_uid).
type unrealCodec struct {
	bee bool // the other side is BeeIRCd (BEE=1)
	// paramModes are the other side's channel modes that take a
	// parameter when set (CHANMODES groups B and C).
	paramModes string
	// For live MODE lines (2c): CHANMODES groups A (lists), B (always an
	// argument), C (an argument when set), and the PREFIX rank letters.
	listModes, alwaysModes, setModes, rankModes string
}

// Codec returns the codec for the protocol the handshake agreed on.
func (h *unreal) Codec() Codec {
	c := &unrealCodec{bee: h.peer.Bee}
	groups := strings.Split(h.peer.Tokens["CHANMODES"], ",")
	if len(groups) >= 3 {
		c.paramModes = groups[1] + groups[2]
		c.listModes, c.alwaysModes, c.setModes = groups[0], groups[1], groups[2]
	} else {
		c.paramModes = "kfLlFH" // UnrealIRCd 6 defaults
		c.listModes, c.alwaysModes, c.setModes = "beI", "kLf", "lFH"
	}
	c.rankModes = "qaohv"
	if p := h.peer.Tokens["PREFIX"]; strings.HasPrefix(p, "(") {
		if end := strings.IndexByte(p, ')'); end > 0 {
			c.rankModes = p[1:end]
		}
	}
	return c
}

func (c *unrealCodec) Rename() bool { return c.bee }

// SJOIN prefixes for member ranks (UnrealIRCd's chanmodes/*.c
// sjoin_prefix) and list modes.
var (
	sjoinRank = map[byte]byte{'q': '*', 'a': '~', 'o': '@', 'h': '%', 'v': '+'}
	sjoinList = map[byte]byte{'b': '&', 'e': '"', 'I': '\''}
)

func rankFromSJoin(p byte) byte {
	for m, q := range sjoinRank {
		if q == p {
			return m
		}
	}
	return 0
}

func listFromSJoin(p byte) byte {
	for m, q := range sjoinList {
		if q == p {
			return m
		}
	}
	return 0
}

// encodeIP is NICKIP: the address bytes in base64, "*" when unknown.
func encodeIP(ip string) string {
	p := net.ParseIP(ip)
	if p == nil {
		return "*"
	}
	if v4 := p.To4(); v4 != nil {
		return base64.StdEncoding.EncodeToString(v4)
	}
	return base64.StdEncoding.EncodeToString(p.To16())
}

func decodeIP(s string) string {
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil || (len(b) != 4 && len(b) != 16) {
		return ""
	}
	return net.IP(b).String()
}

// sendUserModes drops modes another server may not take from a UID line:
// s needs a snomask parameter there.
func sendUserModes(m string) string {
	return "+" + strings.ReplaceAll(m, "s", "")
}

func (c *unrealCodec) User(u User, sid string) []string {
	account := u.Account
	if account == "" {
		account = "0"
	}
	modes := u.Modes
	if u.Host != u.RealHost && !strings.Contains(modes, "x") {
		modes += "x" // the host others see is a cloak or vhost
	}
	cloak, vhost := orStar(u.Cloak), "*"
	switch {
	case u.Host == u.RealHost:
		if u.Cloak == "" {
			cloak = u.RealHost
		}
	case u.Cloak == "":
		cloak = u.Host
	case u.Host != u.Cloak:
		vhost = u.Host
	}
	hops := u.Hops
	if hops < 1 {
		hops = 1
	}
	return []string{fmt.Sprintf(":%s UID %s %d %d %s %s %s %s %s %s %s %s :%s",
		sid, u.Nick, hops, u.NickTS, u.Ident, u.RealHost, u.UID, account, sendUserModes(modes), vhost, cloak, encodeIP(u.IP), u.Realname)}
}

func (c *unrealCodec) Away(uid, message string) string {
	return ":" + uid + " AWAY :" + message
}

// sjoinLineMax keeps SJOIN lines within the 510 bytes every server takes.
const sjoinLineMax = 480

func (c *unrealCodec) Channel(ch Channel, sid string) []string {
	head := fmt.Sprintf(":%s SJOIN %d %s", sid, ch.TS, ch.Name)
	first := head
	if ch.Modes != "" {
		first += " +" + ch.Modes
		for i := 0; i < len(ch.Modes); i++ {
			if a, ok := ch.Args[ch.Modes[i]]; ok {
				first += " " + a
			}
		}
	}
	var items []string
	for _, m := range ch.Members {
		var pre []byte
		for i := 0; i < len(m.Ranks); i++ {
			if p := sjoinRank[m.Ranks[i]]; p != 0 {
				pre = append(pre, p)
			}
		}
		items = append(items, string(pre)+m.UID)
	}
	for _, l := range ch.Lists {
		p := sjoinList[l.Mode]
		if p == 0 {
			continue
		}
		items = append(items, fmt.Sprintf("<%d,%s>%c%s", l.SetAt, l.SetBy, p, l.Mask))
	}
	var out []string
	cur, body := first, ""
	for _, it := range items {
		if body != "" && len(cur)+len(body)+len(it)+3 > sjoinLineMax {
			out = append(out, cur+" :"+body)
			cur, body = head, ""
		}
		if body != "" {
			body += " "
		}
		body += it
	}
	return append(out, cur+" :"+body)
}

func (c *unrealCodec) Topic(t Topic, sid string) string {
	return fmt.Sprintf(":%s TOPIC %s %s %d :%s", sid, t.Channel, t.By, t.At, t.Text)
}

func (c *unrealCodec) Kill(sid, uid, reason string) string {
	return ":" + sid + " KILL " + uid + " :" + reason
}

func (c *unrealCodec) Decode(m irc.Message) any {
	p := m.Params
	switch m.Command {
	case "UID":
		// nick hops ts ident realhost uid account umodes vhost cloakhost ip :gecos
		if len(p) < 12 {
			return nil
		}
		hops, _ := strconv.Atoi(p[1])
		ts, _ := strconv.ParseInt(p[2], 10, 64)
		u := &User{UID: p[5], Nick: p[0], NickTS: ts, Hops: hops, Ident: p[3], RealHost: p[4],
			Host: p[4], IP: decodeIP(p[10]), Realname: p[11], Modes: strings.TrimPrefix(p[7], "+")}
		if a := p[6]; a != "0" && a != "*" {
			u.Account = a
		}
		if p[9] != "*" && p[9] != "" {
			u.Cloak = p[9]
		}
		if strings.Contains(u.Modes, "x") {
			switch {
			case p[8] != "*" && p[8] != "":
				u.Host = p[8]
			case p[9] != "*" && p[9] != "":
				u.Host = p[9]
			}
		}
		return u
	case "SJOIN":
		return c.decodeSJoin(p)
	case "TOPIC":
		// TOPIC #chan setter ts :text (from a server)
		if len(p) < 4 {
			return nil
		}
		at, err := strconv.ParseInt(p[2], 10, 64)
		if err != nil {
			return nil
		}
		return &Topic{Channel: p[0], By: p[1], At: at, Text: p[3]}
	case "AWAY":
		msg := ""
		if len(p) > 0 {
			msg = p[len(p)-1]
		}
		return &Away{UID: m.Source, Message: msg}
	case "KILL":
		if len(p) < 1 {
			return nil
		}
		reason := ""
		if len(p) > 1 {
			reason = p[len(p)-1]
		}
		return &Kill{Source: m.Source, UID: p[0], Reason: reason}
	case "SVSKILL":
		if len(p) < 1 {
			return nil
		}
		reason := ""
		if len(p) > 1 {
			reason = p[len(p)-1]
		}
		return &SvsKill{Source: m.Source, UID: p[0], Reason: reason}
	}
	return c.decodeLive(m)
}

// decodeSJoin reads "SJOIN ts #chan [+modes params...] :items".
func (c *unrealCodec) decodeSJoin(p []string) any {
	if len(p) < 3 {
		return nil
	}
	ts, err := strconv.ParseInt(p[0], 10, 64)
	if err != nil {
		return nil
	}
	ch := &Channel{Name: p[1], TS: ts, Args: map[byte]string{}}
	rest := p[2 : len(p)-1]
	if len(rest) > 0 && strings.HasPrefix(rest[0], "+") {
		letters := rest[0][1:]
		args := rest[1:]
		for i := 0; i < len(letters); i++ {
			l := letters[i]
			ch.Modes += string(l)
			if strings.IndexByte(c.paramModes, l) >= 0 && len(args) > 0 {
				ch.Args[l] = args[0]
				args = args[1:]
			}
		}
	}
	for _, it := range strings.Fields(p[len(p)-1]) {
		var setAt int64
		setBy := ""
		if strings.HasPrefix(it, "<") {
			end := strings.IndexByte(it, '>')
			if end < 0 {
				continue
			}
			at, by, _ := strings.Cut(it[1:end], ",")
			setAt, _ = strconv.ParseInt(at, 10, 64)
			setBy, it = by, it[end+1:]
		}
		if it == "" {
			continue
		}
		if mode := listFromSJoin(it[0]); mode != 0 {
			ch.Lists = append(ch.Lists, ListEntry{Mode: mode, Mask: it[1:], SetBy: setBy, SetAt: setAt})
			continue
		}
		var ranks []byte
		for len(it) > 0 {
			r := rankFromSJoin(it[0])
			if r == 0 {
				break
			}
			ranks = append(ranks, r)
			it = it[1:]
		}
		if it != "" {
			ch.Members = append(ch.Members, Member{UID: it, Ranks: string(ranks)})
		}
	}
	return ch
}

// SortedModes writes mode letters in a stable order (for tests and
// bursts).
func SortedModes(m map[byte]bool) string {
	var b []byte
	for k, on := range m {
		if on {
			b = append(b, k)
		}
	}
	sort.Slice(b, func(i, j int) bool { return b[i] < b[j] })
	return string(b)
}
