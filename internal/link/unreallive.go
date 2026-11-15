package link

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/fhazal/beeircd/internal/irc"
)

// Live lines in UnrealIRCd 6's wire format, as its modules
// send them: nick.c, part.c, quit.c, kick.c, mode.c, umode2/svsmode.c,
// svslogin.c, sethost/chghost.c, chgident.c, setname/chgname.c,
// invite.c, svsnick.c, message.c, tkl.c.

// isSID reports whether src names a server (a SID) rather than a user.
func isSID(src string) bool { return len(src) == 3 }

// modesPerLine keeps MODE lines short enough for every server.
const modesPerLine = 12

func (c *unrealCodec) Encode(item any) []string {
	switch it := item.(type) {
	case *Nick:
		return []string{fmt.Sprintf(":%s NICK %s %d", it.UID, it.Nick, it.TS)}
	case *Quit:
		return []string{":" + it.UID + " QUIT :" + it.Reason}
	case *Part:
		if it.Reason == "" {
			return []string{":" + it.UID + " PART " + it.Channel}
		}
		return []string{":" + it.UID + " PART " + it.Channel + " :" + it.Reason}
	case *Kick:
		return []string{":" + it.Source + " KICK " + it.Channel + " " + it.UID + " :" + it.Reason}
	case *Mode:
		return c.encodeMode(it)
	case *UMode:
		m := c.ourModes(strings.ReplaceAll(it.Modes, "s", "")) // snomasks need a parameter there
		if strings.Trim(m, "+-") == "" {
			return nil
		}
		if it.Source == it.UID {
			return []string{":" + it.UID + " UMODE2 " + m}
		}
		return []string{":" + it.Source + " SVS2MODE " + it.UID + " " + m}
	case *Login:
		acc := it.Account
		if acc == "" {
			acc = "0"
		}
		return []string{":" + it.Source + " SVSLOGIN * " + it.UID + " " + acc}
	case *Host:
		if it.Source == it.UID {
			return []string{":" + it.UID + " SETHOST " + it.Host}
		}
		return []string{":" + it.Source + " CHGHOST " + it.UID + " " + it.Host}
	case *Ident:
		return []string{":" + it.Source + " CHGIDENT " + it.UID + " " + it.Ident}
	case *Realname:
		if it.Source == it.UID {
			return []string{":" + it.UID + " SETNAME :" + it.Realname}
		}
		return []string{":" + it.Source + " CHGNAME " + it.UID + " :" + it.Realname}
	case *Invite:
		return []string{":" + it.Source + " INVITE " + it.UID + " " + it.Channel + " 0"}
	case *ForceNick:
		return []string{fmt.Sprintf(":%s SVSNICK %s %s %d", it.Source, it.UID, it.Nick, it.TS)}
	case *Message:
		line := ":" + it.Source + " " + it.Verb + " " + it.Target
		if it.Verb != "TAGMSG" {
			line += " :" + it.Text
		}
		return []string{withTags(it.Tags) + line}
	case *Numeric:
		if len(it.Params) == 0 {
			return nil
		}
		ps := append([]string(nil), it.Params...)
		last := len(ps) - 1
		ps[last] = ":" + ps[last]
		return []string{":" + it.Source + " " + it.Code + " " + strings.Join(ps, " ")}
	case *Ban:
		return []string{c.encodeBan(it)}
	case *SpamFilter:
		return []string{c.encodeSpam(it)}
	}
	return nil
}

// withTags writes message tags in front of a line ("" for none).
func withTags(tags map[string]string) string {
	if len(tags) == 0 {
		return ""
	}
	keys := make([]string, 0, len(tags))
	for k := range tags {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	b.WriteByte('@')
	for i, k := range keys {
		if i > 0 {
			b.WriteByte(';')
		}
		b.WriteString(k)
		if v := tags[k]; v != "" {
			b.WriteByte('=')
			b.WriteString(irc.EscapeTag(v))
		}
	}
	b.WriteByte(' ')
	return b.String()
}

func (c *unrealCodec) encodeMode(m *Mode) []string {
	var out []string
	for i := 0; i < len(m.Items); i += modesPerLine {
		part := m.Items[i:min(i+modesPerLine, len(m.Items))]
		var modes strings.Builder
		var args []string
		sign := byte(0)
		for _, it := range part {
			s := byte('-')
			if it.Add {
				s = '+'
			}
			if s != sign {
				modes.WriteByte(s)
				sign = s
			}
			modes.WriteByte(it.Mode)
			if it.Arg != "" {
				args = append(args, it.Arg)
			}
		}
		line := ":" + m.Source + " MODE " + m.Channel + " " + modes.String()
		if len(args) > 0 {
			line += " " + strings.Join(args, " ")
		}
		if isSID(m.Source) {
			line += " 0" // from a server: no timestamp check (like SVSMODE)
		}
		out = append(out, line)
	}
	return out
}

func orStar(s string) string {
	if s == "" {
		return "*"
	}
	return s
}

func (c *unrealCodec) encodeBan(b *Ban) string {
	sign := "-"
	if b.Add {
		sign = "+"
	}
	head := fmt.Sprintf(":%s TKL %s %c %s %s %s %d %d", b.Source, sign, b.Type, orStar(b.User), b.Host, b.SetBy, b.Expires, b.SetAt)
	if b.Type == BanException {
		return head + " " + orStar(b.Exempt) + " :" + b.Reason
	}
	return head + " :" + b.Reason
}

// decodeLive reads a live line, or returns nil.
func (c *unrealCodec) decodeLive(m irc.Message) any {
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
	switch m.Command {
	case "NICK":
		if len(p) < 1 {
			return nil
		}
		ts, _ := strconv.ParseInt(arg(1), 10, 64)
		return &Nick{UID: m.Source, Nick: p[0], TS: ts}
	case "QUIT":
		return &Quit{UID: m.Source, Reason: last()}
	case "PART":
		if len(p) < 1 {
			return nil
		}
		reason := ""
		if len(p) > 1 {
			reason = last()
		}
		return &Part{UID: m.Source, Channel: p[0], Reason: reason}
	case "KICK":
		if len(p) < 2 {
			return nil
		}
		reason := ""
		if len(p) > 2 {
			reason = last()
		}
		return &Kick{Source: m.Source, Channel: p[0], UID: p[1], Reason: reason}
	case "MODE":
		if len(p) < 2 {
			return nil
		}
		if !strings.HasPrefix(p[0], "#") {
			return &UMode{Source: m.Source, UID: p[0], Modes: p[1]}
		}
		return c.decodeMode(m.Source, p[0], p[1], p[2:])
	case "UMODE2":
		if len(p) < 1 {
			return nil
		}
		return &UMode{Source: m.Source, UID: m.Source, Modes: p[0]}
	case "SVSMODE", "SVS2MODE":
		if len(p) < 2 {
			return nil
		}
		if strings.HasPrefix(p[0], "#") {
			return c.decodeMode(m.Source, p[0], p[1], p[2:])
		}
		um := &UMode{Source: m.Source, UID: p[0], Modes: p[1]}
		if modes, ok := svidMode(p[1]); ok && len(p) >= 3 {
			// +d with a value is the services account, not deaf: "0" logs
			// out, digits only are an old services stamp (no change).
			um.Modes = modes
			switch acc := p[2]; {
			case acc == "0" || acc == "*":
				um.Login = &Login{Source: m.Source, Target: "*", UID: p[0]}
			case strings.Trim(acc, "0123456789") != "":
				um.Login = &Login{Source: m.Source, Target: "*", UID: p[0], Account: acc}
			}
		}
		return um
	case "SVSLOGIN":
		if len(p) < 3 {
			return nil
		}
		acc := p[2]
		if acc == "0" || acc == "*" {
			acc = ""
		}
		return &Login{Source: m.Source, Target: p[0], UID: p[1], Account: acc}
	case "SETHOST":
		if len(p) < 1 {
			return nil
		}
		return &Host{Source: m.Source, UID: m.Source, Host: last()}
	case "CHGHOST":
		if len(p) < 2 {
			return nil
		}
		return &Host{Source: m.Source, UID: p[0], Host: last()}
	case "SETIDENT":
		if len(p) < 1 {
			return nil
		}
		return &Ident{Source: m.Source, UID: m.Source, Ident: last()}
	case "CHGIDENT":
		if len(p) < 2 {
			return nil
		}
		return &Ident{Source: m.Source, UID: p[0], Ident: last()}
	case "SETNAME":
		if len(p) < 1 {
			return nil
		}
		return &Realname{Source: m.Source, UID: m.Source, Realname: last()}
	case "CHGNAME":
		if len(p) < 2 {
			return nil
		}
		return &Realname{Source: m.Source, UID: p[0], Realname: last()}
	case "INVITE":
		if len(p) < 2 {
			return nil
		}
		return &Invite{Source: m.Source, UID: p[0], Channel: p[1]}
	case "SVSNICK":
		if len(p) < 2 {
			return nil
		}
		ts, _ := strconv.ParseInt(arg(2), 10, 64)
		return &ForceNick{Source: m.Source, UID: p[0], Nick: p[1], TS: ts}
	case "PRIVMSG", "NOTICE", "TAGMSG":
		if len(p) < 1 || (m.Command != "TAGMSG" && len(p) < 2) {
			return nil
		}
		return &Message{Source: m.Source, Verb: m.Command, Target: p[0], Text: arg(1), Tags: m.Tags}
	case "TKL":
		return c.decodeBan(m.Source, p)
	}
	if len(m.Command) == 3 && m.Command[0] >= '0' && m.Command[0] <= '9' && len(p) > 0 {
		return &Numeric{Source: m.Source, Code: m.Command, Params: p}
	}
	return nil
}

// decodeMode reads channel modes with the other side's CHANMODES groups:
// list modes and group B always take an argument, group C only when set,
// ranks always.
func (c *unrealCodec) decodeMode(src, channel, modes string, args []string) *Mode {
	m := &Mode{Source: src, Channel: channel}
	add := true
	for i := 0; i < len(modes); i++ {
		l := modes[i]
		switch l {
		case '+':
			add = true
			continue
		case '-':
			add = false
			continue
		}
		takes := strings.IndexByte(c.listModes+c.alwaysModes+c.rankModes, l) >= 0 ||
			(add && strings.IndexByte(c.setModes, l) >= 0)
		it := ModeItem{Add: add, Mode: l}
		if takes {
			if len(args) == 0 {
				continue // missing argument: skip it, like UnrealIRCd
			}
			it.Arg, args = args[0], args[1:]
		}
		m.Items = append(m.Items, it)
	}
	return m
}

// decodeBan reads TKL. Server bans: + G user host setby expire setat
// :reason; ban exceptions add a kinds field before the reason; nick bans
// (Q) and spamfilters are kinds we don't keep.
func (c *unrealCodec) decodeBan(src string, p []string) any {
	if len(p) < 2 || len(p[1]) != 1 || (p[0] != "+" && p[0] != "-") {
		return nil // (an empty type crashed here before 2g's fuzzing)
	}
	if p[1] == "F" {
		return decodeSpam(src, p)
	}
	b := &Ban{Source: src, Add: p[0] == "+", Type: p[1][0]}
	switch {
	case b.Type == BanGline, b.Type == BanGZline, b.Type == BanShun, b.Type == BanException, b.Type == BanQline:
	case b.Type == BanRealname && c.bee:
	default:
		b.Unsupported = true
		return b
	}
	if len(p) < 5 {
		return nil
	}
	b.User, b.Host, b.SetBy = strings.TrimPrefix(p[2], "%"), p[3], p[4]
	if len(p) > 6 {
		b.Expires, _ = strconv.ParseInt(p[5], 10, 64)
		b.SetAt, _ = strconv.ParseInt(p[6], 10, 64)
	}
	if b.Type == BanException && len(p) > 7 {
		b.Exempt = p[7]
	}
	if len(p) > 7 {
		b.Reason = p[len(p)-1]
	}
	return b
}

// svidMode reports whether modes sets +d (with SVSMODE: the services
// account), and returns modes without it.
func svidMode(modes string) (string, bool) {
	add, found := true, false
	var out strings.Builder
	for i := 0; i < len(modes); i++ {
		switch ch := modes[i]; {
		case ch == '+' || ch == '-':
			add = ch == '+'
			out.WriteByte(ch)
		case ch == 'd' && add:
			found = true
		default:
			out.WriteByte(ch)
		}
	}
	if !found {
		return modes, false
	}
	return strings.Trim(out.String(), "+-"), true
}
