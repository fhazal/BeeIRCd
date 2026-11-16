package link

import (
	"crypto/subtle"
	"fmt"
	"strconv"
	"strings"

	"github.com/fhazal/beeircd/internal/config"
	"github.com/fhazal/beeircd/internal/irc"
)

// InspProtocol is the InspIRCd server protocol we speak: 1206, InspIRCd 4.
const InspProtocol = 1206

// insp is the InspIRCd 4 server handshake, as InspIRCd 4.12 does it
// (src/modules/m_spanningtree/capab.cpp, server.cpp, treesocket2.cpp):
//
//	connecting side: CAPAB START .. CAPAB END, then SERVER once the other
//	                 side's CAPAB END arrived;
//	accepting side:  CAPAB START .. CAPAB END when theirs starts, SERVER
//	                 once their SERVER checks out.
//
// Then the connecting side bursts (BURST .. ENDBURST) and the other side
// answers with its own. The password goes in plain text: we send no
// CHALLENGE, so InspIRCd doesn't use its HMAC either; the TLS certificate
// fingerprint is what proves who links, as with UnrealIRCd.
type insp struct {
	me         Local
	out        bool
	fp         string
	blk        *config.LinkServer
	capSent    bool
	capEnd     bool
	serverSent bool
	done       bool
	peer       Peer
	chanModes  string // their CAPAB CHANMODES
	userModes  string // their CAPAB USERMODES
	extBans    string // their CAPAB EXTBANS
	haveExt    bool
}

// NewInsp starts an InspIRCd 4 handshake (see NewUnreal for the
// arguments).
func NewInsp(me Local, outgoing bool, block *config.LinkServer, fp string) Handshake {
	h := &insp{me: me, out: outgoing, fp: fp, blk: block}
	h.peer.Tokens = map[string]string{}
	return h
}

// LooksInsp reports whether the first line from a server is InspIRCd's.
func LooksInsp(m irc.Message) bool { return m.Command == "CAPAB" }

func (h *insp) Done() bool { return h.done }
func (h *insp) Peer() Peer { return h.peer }

func (h *insp) Start() []string {
	if !h.out {
		return nil
	}
	return h.capab()
}

// capab is our CAPAB block. We send no module or mode lists: InspIRCd
// only compares lists that came, and the bridge maps modes by name.
func (h *insp) capab() []string {
	if h.capSent {
		return nil
	}
	h.capSent = true
	return []string{
		fmt.Sprintf("CAPAB START %d", InspProtocol),
		fmt.Sprintf("CAPAB CAPABILITIES :CASEMAPPING=%s PROTOCOL=%d", h.me.casemapping(), InspProtocol),
		"CAPAB END",
	}
}

func (h *insp) serverLine() string {
	h.serverSent = true
	p := h.blk.Password
	if p == "" {
		p = "*"
	}
	return fmt.Sprintf("SERVER %s %s %s :%s", h.me.Name, p, h.me.SID, h.me.Description)
}

func (h *insp) Handle(m irc.Message) ([]string, error) {
	if h.done {
		return nil, nil
	}
	switch m.Command {
	case "CAPAB":
		return h.capabIn(m.Params)
	case "SERVER":
		return h.server(m.Params)
	case "ERROR":
		text := ""
		if len(m.Params) > 0 {
			text = m.Params[len(m.Params)-1]
		}
		return nil, refuse("they closed the link: " + text)
	case "NOTICE", "PING", "PONG":
		return nil, nil
	}
	return nil, refuse("not an InspIRCd server handshake (got " + m.Command + ")")
}

func (h *insp) capabIn(p []string) ([]string, error) {
	if len(p) == 0 {
		return nil, refuse("Invalid number of parameters for CAPAB - Mismatched version")
	}
	arg := ""
	if len(p) > 1 {
		arg = p[1]
	}
	switch strings.ToUpper(p[0]) {
	case "START":
		proto, _ := strconv.Atoi(arg)
		h.peer.Protocol = proto
		if proto < InspProtocol {
			return nil, refuse(fmt.Sprintf("CAPAB negotiation failed: protocol %d is too old (InspIRCd 4, protocol %d, needed)", proto, InspProtocol))
		}
		return h.capab(), nil
	case "CAPABILITIES":
		for _, kv := range strings.Fields(arg) {
			k, v, _ := strings.Cut(kv, "=")
			h.peer.Tokens[k] = v
		}
	case "CHANMODES":
		h.chanModes = arg
	case "USERMODES":
		h.userModes = arg
	case "EXTBANS":
		h.extBans, h.haveExt = arg, true
	case "END":
		if !h.capSent {
			return nil, refuse("CAPAB END before CAPAB START")
		}
		if cm := h.peer.Tokens["CASEMAPPING"]; cm != "" && cm != h.me.casemapping() {
			return nil, refuse(fmt.Sprintf("CAPAB negotiation failed: casemapping differs (%s here, %s there); set [server] casemapping = %q on %s or <options casemapping=%q> there",
				h.me.casemapping(), cm, cm, h.me.Name, h.me.casemapping()))
		}
		h.capEnd = true
		if h.out && !h.serverSent {
			return []string{h.serverLine()}, nil
		}
	}
	return nil, nil
}

// server is their "SERVER name password sid :description".
func (h *insp) server(p []string) ([]string, error) {
	if len(p) < 4 {
		return nil, refuse("Protocol error - Not enough parameters for SERVER command")
	}
	if !h.capEnd {
		return nil, refuse("SERVER before CAPAB END")
	}
	name, pass, sid, desc := p[0], p[1], p[2], p[len(p)-1]
	if !config.ValidServerName(name) {
		return nil, refuse("Bogus server name")
	}
	h.peer.Name = name
	switch {
	case h.out && !strings.EqualFold(name, h.blk.Name):
		return nil, refuse(fmt.Sprintf("Link denied: expected %s, this is %s", h.blk.Name, name))
	case !h.out:
		h.blk = h.me.Block(name)
		if h.blk == nil {
			return nil, refuse("Mismatched server name or password (no [[link.server]] block for " + name + ")")
		}
	}
	if h.fp == "" || subtle.ConstantTimeCompare([]byte(h.fp), []byte(h.blk.Fingerprint)) != 1 {
		return nil, refuse("Link denied: certificate fingerprint does not match the link block")
	}
	if strings.HasPrefix(pass, "AUTH:") {
		return nil, refuse("Link denied: HMAC password sent, but no CHALLENGE was offered")
	}
	if h.blk.Password != "" && subtle.ConstantTimeCompare([]byte(pass), []byte(h.blk.Password)) != 1 {
		return nil, refuse("Mismatched server name or password")
	}
	switch {
	case !ValidSID(sid):
		return nil, refuse("Invalid format server ID: " + sid + "!")
	case h.me.Known(sid):
		return nil, refuse("Server ID " + sid + " already exists!")
	case h.me.Known(name):
		return nil, refuse("Server " + name + " already exists!")
	}
	h.peer.SID, h.peer.Description, h.peer.Block = sid, desc, *h.blk
	h.peer.Software = "InspIRCd"
	h.done = true
	if h.out {
		return nil, nil
	}
	return []string{h.serverLine()}, nil
}

// Burst is written in the UnrealIRCd dialect like every other line; the
// bridge turns it into InspIRCd's.
func (h *insp) Burst(servers []ServerInfo, body []string) []string {
	var out []string
	for _, s := range servers {
		out = append(out, fmt.Sprintf(":%s SID %s %d %s :%s", s.Uplink, s.Name, s.Hops+1, s.SID, s.Description))
		if s.Synced {
			out = append(out, ":"+s.SID+" EOS")
		}
	}
	out = append(out, body...)
	return append(out, ":"+h.me.SID+" EOS")
}

// Codec reads and writes the UnrealIRCd dialect with our own mode
// letters: the bridge does the rest.
func (h *insp) Codec() Codec {
	groups := strings.Split(h.me.ChanModes, ",")
	c := &unrealCodec{}
	if len(groups) >= 3 {
		c.paramModes = groups[1] + groups[2]
		c.listModes, c.alwaysModes, c.setModes = groups[0], groups[1], groups[2]
	}
	c.rankModes = prefixLetters(h.me.Prefix)
	return &inspCodec{c}
}

// inspCodec settles nick collisions like InspIRCd: the loser is renamed
// to its UID (SAVE), and we tell the other side about its own losers.
type inspCodec struct{ *unrealCodec }

func (c *inspCodec) Rename() bool { return true }

// TellsRenames reports that the other side wants to hear about the
// collision losers on its side (InspIRCd's SAVE).
func (c *inspCodec) TellsRenames() bool { return true }

// prefixLetters is "qaohv" from "(qaohv)~&@%+".
func prefixLetters(prefix string) string {
	if strings.HasPrefix(prefix, "(") {
		if end := strings.IndexByte(prefix, ')'); end > 0 {
			return prefix[1:end]
		}
	}
	return "qaohv"
}

// prefixChars is "~&@%+" from "(qaohv)~&@%+".
func prefixChars(prefix string) string {
	if end := strings.IndexByte(prefix, ')'); end > 0 {
		return prefix[end+1:]
	}
	return "~&@%+"
}

// Bridger is a handshake whose link speaks another protocol than the
// UnrealIRCd dialect the server writes: every line after the handshake
// goes through its Bridge.
type Bridger interface {
	Bridge() Bridge
}

// Bridge turns lines between the UnrealIRCd dialect and the wire.
type Bridge interface {
	// Out turns one of our lines into wire lines (none: not sent).
	Out(line string) []string
	// In turns one wire line into lines for the server to run, and
	// answers that go straight back on the link.
	In(line string) (run, reply []string)
}

func (h *insp) Bridge() Bridge {
	b := newInspBridge(h.me, h.peer, h.chanModes, h.userModes)
	if h.haveExt {
		b.theirExt = parseInspExtbans(h.extBans)
	}
	return b
}
