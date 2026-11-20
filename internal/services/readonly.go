package services

// OperServ SET, as Anope's os_set: READONLY switches. They live in memory
// only, so a restart turns them off again.

import (
	"strings"
)

// readonlyRefusal is what a command that would change data gets while
// READONLY is on.
const readonlyRefusal = "Services are in read-only mode; this can't be changed right now."

// readonlyAllow lists, per service, the commands that still work while
// READONLY is on: those that only look, log you in or out, or act on the
// network without saving anything. "NAME SUB" allows NAME only with that
// subcommand; "NAME -" allows NAME with no subcommand. The subcommand is
// the first word after the command, or the second on ChanServ and for
// BotServ BADWORDS (after the channel).
var readonlyAllow = map[string]map[string]bool{
	"NickServ": readonlySet("IDENTIFY", "LOGOUT", "RECOVER", "STATUS", "ALIST", "INFO", "LIST", "GLIST",
		"GETEMAIL", "UPDATE", "ACCESS LIST", "CERT LIST", "AJOIN LIST", "BUDDY LIST"),
	"ChanServ": readonlySet("INFO", "STATUS", "SEEN", "LIST", "GETKEY", "INVITE", "KICK", "BAN", "UNBAN",
		"OP", "DEOP", "VOICE", "DEVOICE", "HALFOP", "DEHALFOP", "PROTECT", "DEPROTECT",
		"OWNER", "DEOWNER", "UP", "DOWN", "SYNC", "ENFORCE",
		"ACCESS LIST", "ACCESS VIEW", "AKICK LIST", "AKICK VIEW", "LEVELS LIST", "FLAGS LIST", "FLAGS -",
		"ENTRYMSG LIST", "LOG -", "QOP LIST", "SOP LIST", "HOP LIST", "AOP LIST", "VOP LIST", "UOP LIST"),
	"MemoServ": readonlySet("LIST", "READ", "CHECK", "INFO", "BUDDY LIST"),
	"OperServ": readonlySet("SET", "STATS", "SESSION", "DEFCON", "GLOBAL", "KILL", "MODE", "KICK",
		"SVSNICK", "SVSJOIN", "SVSPART", "UMODE", "USERLIST", "CHANLIST", "SEEN STATS",
		"AKILL LIST", "AKILL VIEW", "ZLINE LIST", "ZLINE VIEW", "SNLINE LIST", "SNLINE VIEW",
		"SHUN LIST", "SHUN VIEW", "SQLINE LIST", "SQLINE VIEW", "FORBID LIST",
		"EXCEPTION LIST", "EXCEPTION VIEW", "IGNORE LIST", "LOGONNEWS LIST", "OPERNEWS LIST",
		"RANDOMNEWS LIST", "OPER LIST", "OPER INFO"),
	"HostServ": readonlySet("ON", "OFF", "LIST", "WAITING"),
	"BotServ":  readonlySet("BOTLIST", "INFO", "SAY", "ACT", "BADWORDS LIST"),
	"HelpServ": readonlySet("ON", "OFF", "CANCEL", "LIST", "TAKE", "CLOSE", "TOPIC LIST", "HELPER LIST"),
}

func readonlySet(words ...string) map[string]bool {
	m := make(map[string]bool, len(words))
	for _, w := range words {
		m[w] = true
	}
	return m
}

// readonlyBlocks refuses command name (an alias already turned into its
// real name) on sv while READONLY is on, unless it only looks. Bots the
// server adds (AdminServ) aren't services data and are never blocked.
func (s *Services) readonlyBlocks(c *Client, sv *service, name string, args []string) bool {
	allow, ok := readonlyAllow[sv.user.Nick]
	if !s.readonly || !ok || allow[name] {
		return false
	}
	at := 0
	if sv == s.chanServ || (sv == s.botServ && name == "BADWORDS") {
		at = 1
	}
	sub := strings.ToUpper(argAt(args, at))
	if sub == "" {
		sub = "-"
	}
	if allow[name+" "+sub] {
		return false
	}
	s.say(c, sv, readonlyRefusal)
	return true
}

func osSet(s *Services, c *Client, args []string) {
	what := strings.ToUpper(argAt(args, 0))
	if what == "LIST" {
		s.os(c, "READONLY   %s", onOffWord(s.readonly))
		return
	}
	on, ok := onOff(argAt(args, 1))
	if what != "READONLY" || !ok {
		s.os(c, "Syntax: SET {READONLY} {ON | OFF} | SET LIST")
		return
	}
	if !s.needFlag(c, s.operServ, 'a') {
		return
	}
	word := onOffWord(on)
	s.readonly = on
	s.audit(c, s.operServ, "SET %s %s", what, word)
	s.osLog(c, "to turn %s %s", what, strings.ToLower(word))
	s.snotice('a', "%s turned %s %s", c.user.Nick, what, strings.ToLower(word))
	if on {
		s.os(c, "Services are now in read-only mode: nothing is saved or expires until READONLY is off or services restart.")
	} else {
		s.os(c, "Services are no longer in read-only mode.")
	}
}

func onOffWord(on bool) string {
	if on {
		return "ON"
	}
	return "OFF"
}
