package server

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/fhazal/beeircd/internal/irc"
)

// The unrealircd.org/json-log tag, as UnrealIRCd's json-log-tag module:
// BeeIRCd's own server notices carry the notice as a small JSON object
// (UnrealIRCd's timestamp, level, subsystem, event_id, log_source and msg)
// for opers whose client has message-tags. A notice an UnrealIRCd server
// sent with its own tag (SLOG) keeps that one.

const jsonLogTagName = "unrealircd.org/json-log"

// snoEvent is the subsystem and event id of a server notice with letter,
// UnrealIRCd's names where it has one for that kind of notice.
func snoEvent(letter byte, text string) (subsystem, eventID string) {
	switch letter {
	case 'c':
		return "connect", "LOCAL_CLIENT_CONNECT"
	case 'C':
		return "connect", "REMOTE_CLIENT_CONNECT"
	case 'q':
		return "connect", "LOCAL_CLIENT_DISCONNECT"
	case 'Q':
		return "connect", "REMOTE_CLIENT_DISCONNECT"
	case 'k', 'K':
		return "kill", "KILL_COMMAND"
	case 'o', 'O':
		return "oper", "OPER"
	case 'x', 'X':
		return "tkl", "TKL"
	case 'f':
		return "tkl", "SPAMFILTER_MATCH"
	case 'd':
		return "blacklist", "BLACKLIST_HIT"
	case 'r':
		return "config", "CONFIG_RELOAD"
	case 'l', 'L':
		return "link", "LINK"
	case 'n':
		return "nick", "LOCAL_NICK_CHANGE"
	case 'N':
		return "nick", "REMOTE_NICK_CHANGE"
	case 't':
		return "stats", "STATS_COMMAND"
	case 'g', 'G':
		return "globops", "GLOBOPS"
	case 'v':
		return "operoverride", "OPEROVERRIDE"
	}
	if strings.HasPrefix(text, "RPC") {
		return "rpc", "RPC_CALL"
	}
	return "server", "NOTICE"
}

// jsonLogTag is the tag value for one of our server notices.
func (s *Server) jsonLogTag(letter byte, text string) string {
	ev := rpcLogEvent{Timestamp: rpcTime(time.Now()), Level: "info", LogSource: s.cfg.Server.Name,
		Msg: strings.ReplaceAll(text, "\x02", "")}
	ev.Subsystem, ev.EventID = snoEvent(letter, text)
	b, err := json.Marshal(ev)
	if err != nil {
		return ""
	}
	return string(b)
}

// snoNotice sends one server notice line to oper c, with jsonTag when c
// has message-tags.
func (s *Server) snoNotice(c *Client, line, jsonTag string) {
	if jsonTag == "" || !c.caps["message-tags"] || c.greetWait {
		s.notice(c, line)
		return
	}
	c.send(irc.Message{Tags: map[string]string{jsonLogTagName: jsonTag}, Source: s.cfg.Server.Name, Command: "NOTICE",
		Params: []string{c.target(), line}, Trailing: true}.String())
}
