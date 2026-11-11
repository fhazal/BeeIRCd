package link

import (
	"crypto/subtle"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/fhazal/beeircd/internal/config"
	"github.com/fhazal/beeircd/internal/irc"
)

// UnrealProtocol is the UnrealIRCd server protocol version we speak
// (UnrealIRCd 6.1 and 6.2 send 6100).
const UnrealProtocol = 6100

// minUnrealProtocol is the oldest protocol we link with: UnrealIRCd 6.
const minUnrealProtocol = 6000

// maxClockSkew is how far apart two servers' clocks may be (UnrealIRCd's
// MAX_SERVER_TIME_OFFSET).
const maxClockSkew = 60

var sidRe = regexp.MustCompile(`^[0-9][A-Z0-9]{2}$`)

// ValidSID reports whether s is a server ID like 0BE.
func ValidSID(s string) bool { return sidRe.MatchString(s) }

// unreal is the UnrealIRCd 6 server handshake, as UnrealIRCd 6.2.7 does
// it (src/serv.c start_server_handshake + send_proto, src/modules/
// protoctl.c, src/modules/server.c cmd_server + server_sync):
//
//	outgoing: PASS, PROTOCTL EAUTH, PROTOCTL SERVERS=, three PROTOCTL lines;
//	          SERVER once their PROTOCTL SERVERS=* answer arrived.
//	incoming: PASS as soon as their EAUTH checks out; EAUTH + SERVERS=*
//	          when their SERVERS= arrives; the three PROTOCTL lines and
//	          SERVER when their SERVER arrives.
//
// Then both sides burst (SID, UID, SJOIN, ... NETINFO, EOS).
type unreal struct {
	me         Local
	out        bool
	fp         string // the other side's certificate fingerprint
	blk        *config.LinkServer
	pass       string
	gotPass    bool
	eauth      bool
	serverSent bool
	done       bool
	peer       Peer
	servers    func() []string // SIDs we know behind other links
}

// NewUnreal starts an UnrealIRCd 6 handshake. outgoing links know their
// block already; incoming ones find it from the name in EAUTH. fp is the
// fingerprint of the certificate the other side showed. servers lists the
// SIDs we know besides our own (for PROTOCTL SERVERS=).
func NewUnreal(me Local, outgoing bool, block *config.LinkServer, fp string, servers func() []string) Handshake {
	h := &unreal{me: me, out: outgoing, fp: fp, blk: block, servers: servers}
	h.peer.Tokens = map[string]string{}
	return h
}

func (h *unreal) Done() bool { return h.done }
func (h *unreal) Peer() Peer { return h.peer }

func (h *unreal) Start() []string {
	if !h.out {
		return nil
	}
	lines := []string{h.passLine(), h.eauthLine(), h.serversLine(false)}
	return append(lines, h.protoLines()...)
}

func (h *unreal) passLine() string {
	p := h.blk.Password
	if p == "" {
		p = "*"
	}
	return "PASS :" + p
}

func (h *unreal) eauthLine() string {
	return fmt.Sprintf("PROTOCTL EAUTH=%s,%d,,BeeIRCd-%s", h.me.Name, UnrealProtocol, h.me.Version)
}

func (h *unreal) serversLine(response bool) string {
	l := "PROTOCTL SERVERS="
	if response {
		l += "*"
	}
	return l + strings.Join(h.servers(), ",")
}

// protoLines are send_proto's three lines. BEE=1 tells another BeeIRCd
// that our extras (rename instead of kill on nick collisions, L5) are on.
func (h *unreal) protoLines() []string {
	now := h.me.Now()
	return []string{
		"PROTOCTL NOQUIT NICKv2 SJOIN SJOIN2 UMODE2 VL SJ3 TKLEXT TKLEXT2 NICKIP ESVID NEXTBANS SJSBY MTAGS BEE=1",
		fmt.Sprintf("PROTOCTL CHANMODES=%s USERMODES=%s BOOTED=%d PREFIX=%s SID=%s MLOCK TS=%d EXTSWHOIS",
			h.me.ChanModes, h.me.UserModes, h.me.Booted, h.me.Prefix, h.me.SID, now),
		"PROTOCTL NICKCHARS= CHANNELCHARS=utf8 BIGLINES",
	}
}

func (h *unreal) serverLine() string {
	h.serverSent = true
	return fmt.Sprintf("SERVER %s 1 :U%d-B-%s %s", h.me.Name, UnrealProtocol, h.me.SID, h.me.Description)
}

func (h *unreal) Handle(m irc.Message) ([]string, error) {
	if h.done {
		return nil, nil
	}
	switch m.Command {
	case "PASS":
		if len(m.Params) < 1 {
			return nil, refuse("PASS needs a password")
		}
		h.pass, h.gotPass = m.Params[len(m.Params)-1], true
		return nil, nil
	case "PROTOCTL":
		var out []string
		for _, tok := range m.Params {
			o, err := h.protoctl(tok)
			if err != nil {
				return nil, err
			}
			out = append(out, o...)
		}
		return out, nil
	case "SERVER":
		return h.server(m)
	case "ERROR":
		text := ""
		if len(m.Params) > 0 {
			text = m.Params[len(m.Params)-1]
		}
		return nil, refuse("they closed the link: " + text)
	case "NOTICE", "PING", "PONG":
		return nil, nil
	}
	return nil, refuse("not a server handshake (got " + m.Command + ")")
}

func (h *unreal) protoctl(tok string) ([]string, error) {
	name, value, hasValue := strings.Cut(tok, "=")
	h.peer.Tokens[name] = value
	switch name {
	case "EAUTH":
		if !hasValue {
			return nil, nil
		}
		return h.eauthIn(value)
	case "SID":
		switch {
		case !h.eauth:
			return nil, refuse("Got PROTOCTL SID before EAUTH, that's the wrong order!")
		case h.peer.SID != "":
			return nil, refuse("Got PROTOCTL SID twice")
		case !ValidSID(value):
			return nil, refuse("Invalid SID. The first character must be a digit and the other two characters must be A-Z0-9. Eg: 0AA.")
		case h.me.Known(value):
			return nil, refuse("SID collision")
		}
		h.peer.SID = value
	case "SERVERS":
		if !h.eauth || !hasValue {
			return nil, nil
		}
		response := strings.HasPrefix(value, "*")
		for _, sid := range strings.Split(strings.TrimPrefix(value, "*"), ",") {
			if sid != "" && h.me.Known(sid) {
				return nil, refuse("Server Exists (or non-unique me::sid)")
			}
		}
		var out []string
		if !response {
			out = append(out, h.eauthLine(), h.serversLine(true))
		}
		if h.out && !h.serverSent {
			out = append(out, h.serverLine())
		}
		return out, nil
	case "TS":
		t, err := strconv.ParseInt(value, 10, 64)
		if err != nil || t < 10000 {
			return nil, nil
		}
		if d := h.me.Now() - t; d > maxClockSkew || d < -maxClockSkew {
			if d < 0 {
				d = -d
			}
			return nil, refuse(fmt.Sprintf("Incorrect clock. Our clocks are %d seconds apart.", d))
		}
	case "CHANNELCHARS":
		if hasValue && value != "utf8" {
			return nil, refuse("Incompatible set::allowed-channelchars setting (BeeIRCd uses utf8)")
		}
	case "NICKCHARS":
		if strings.Contains(value, "utf8") {
			return nil, refuse("Incompatible set::allowed-nickchars setting (BeeIRCd nicks are ASCII)")
		}
	case "BEE":
		h.peer.Bee = true
	}
	return nil, nil
}

// eauthIn checks EAUTH=servername,protocol,flags,software: the link block,
// the certificate fingerprint and the protocol version.
func (h *unreal) eauthIn(value string) ([]string, error) {
	if h.eauth {
		return nil, refuse("PROTOCTL EAUTH received twice")
	}
	parts := strings.Split(value, ",")
	name := parts[0]
	if !config.ValidServerName(name) {
		return nil, refuse("Bogus server name")
	}
	h.peer.Name = name // for notices, even when refused below
	proto := 0
	if len(parts) > 1 {
		proto, _ = strconv.Atoi(parts[1])
	}
	if len(parts) > 3 {
		h.peer.Software = parts[3]
	}
	switch {
	case h.out && !strings.EqualFold(name, h.blk.Name):
		return nil, refuse(fmt.Sprintf("Link denied: expected %s, this is %s", h.blk.Name, name))
	case !h.out:
		h.blk = h.me.Block(name)
		if h.blk == nil {
			return nil, refuse("Link denied (No link block found with your server name or link::incoming::mask did not match)")
		}
	}
	if h.fp == "" || subtle.ConstantTimeCompare([]byte(h.fp), []byte(h.blk.Fingerprint)) != 1 {
		return nil, refuse("Link denied: certificate fingerprint does not match the link block")
	}
	if h.me.Known(name) {
		return nil, refuse("Server Exists")
	}
	if proto < minUnrealProtocol {
		return nil, refuse(fmt.Sprintf("Link denied: protocol %d is too old (UnrealIRCd 6 or BeeIRCd needed)", proto))
	}
	h.eauth = true
	h.peer.Name, h.peer.Protocol, h.peer.Block = name, proto, *h.blk
	if !h.out {
		return []string{h.passLine()}, nil // UnrealIRCd sends PASS early
	}
	return nil, nil
}

func (h *unreal) server(m irc.Message) ([]string, error) {
	if len(m.Params) < 3 || m.Params[2] == "" {
		return nil, refuse("Not enough SERVER parameters")
	}
	name := m.Params[0]
	switch {
	case !h.eauth:
		return nil, refuse("Server using an old protocol (no PROTOCTL EAUTH); UnrealIRCd 6 or BeeIRCd needed")
	case !strings.EqualFold(name, h.peer.Name):
		return nil, refuse("SERVER name does not match EAUTH")
	case !h.gotPass:
		return nil, refuse("Missing password")
	case h.peer.SID == "":
		return nil, refuse("Server using old unsupported protocol (no SID)")
	case m.Params[1] != "1":
		return nil, refuse("Invalid SERVER message, hop count must be 1")
	}
	if h.blk.Password != "" && subtle.ConstantTimeCompare([]byte(h.pass), []byte(h.blk.Password)) != 1 {
		return nil, refuse("Link denied: wrong password")
	}
	h.peer.Description = vlInfo(m.Params[len(m.Params)-1])
	h.done = true
	if h.out {
		return nil, nil
	}
	out := h.protoLines()
	if !h.serverSent {
		out = append(out, h.serverLine())
	}
	return out, nil
}

// vlInfo strips the "U6100-flags-SID " version part UnrealIRCd puts in
// front of a server description (PROTOCTL VL).
func vlInfo(desc string) string {
	if !strings.HasPrefix(desc, "U") {
		return desc
	}
	head, rest, ok := strings.Cut(desc, " ")
	if !ok || strings.Count(head, "-") < 2 {
		return desc
	}
	if rest == "" {
		return "server"
	}
	return rest
}

func (h *unreal) Burst(servers []ServerInfo) []string {
	var out []string
	for _, s := range servers {
		out = append(out, fmt.Sprintf(":%s SID %s %d %s :%s", s.Uplink, s.Name, s.Hops+1, s.SID, s.Description))
		if s.Synced {
			out = append(out, ":"+s.SID+" EOS")
		}
	}
	out = append(out,
		fmt.Sprintf("NETINFO %d %d %d %s 0 0 0 :%s", h.me.MaxGlobal, h.me.Now(), UnrealProtocol, h.me.CloakHash, h.me.Network),
		":"+h.me.SID+" EOS")
	return out
}
