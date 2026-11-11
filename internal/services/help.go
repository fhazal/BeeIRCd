package services

import (
	"fmt"
	"sort"
	"strings"
)

// HELP follows Anope's layout: everyday commands one line each, the rest
// as named groups, HELP ALL for every command with its line. Commands
// someone can't use (oper flags, ranks this server doesn't have) are
// hidden from them, and hidden aliases never show.

// helpGroup is a set of commands HELP lists by name under a title.
type helpGroup struct {
	title string
	cmds  []string
}

// canUse reports whether c may use (and so see) the command name of sv.
func (s *Services) canUse(c *Client, sv *service, name string) bool {
	switch need := sv.cmdNeed[name]; {
	case need == 'o' && !c.isOper():
		return false
	case need != 0 && need != 'o' && !c.can(need):
		return false
	}
	if shown := sv.cmdShown[name]; shown != nil && !shown() {
		return false
	}
	return true
}

func (s *Services) serviceHelp(c *Client, sv *service, args []string) {
	if len(args) > 0 && !strings.EqualFold(args[0], "ALL") {
		cmd, ok := sv.command(args[0])
		name := strings.ToUpper(args[0])
		if real, isAlias := sv.aliases[name]; isAlias {
			name = real
		}
		if !ok || !s.canUse(c, sv, name) {
			s.svcReply(c, sv, fmt.Sprintf("No help for %s. Type /%s help for a list.", args[0], sv.alias))
			return
		}
		s.svcReply(c, sv, "Syntax: "+cmd.syntax)
		for _, l := range strings.Split(cmd.long, "\n") {
			s.svcReply(c, sv, "  "+l)
		}
		return
	}
	all := len(args) > 0
	grouped := map[string]bool{}
	if !all {
		for _, g := range sv.groups {
			for _, n := range g.cmds {
				grouped[n] = true
			}
		}
	}
	var lines []string
	for _, name := range sv.order {
		if !grouped[name] && s.canUse(c, sv, name) {
			lines = append(lines, name)
		}
	}
	if all {
		s.svcReply(c, sv, "All commands of "+sv.user.Nick+":")
	} else {
		s.svcReply(c, sv, sv.intro)
	}
	width := 0
	for _, name := range lines {
		width = max(width, len(name))
	}
	for _, name := range lines {
		s.svcReply(c, sv, fmt.Sprintf("  %-*s  %s", width, name, sv.cmds[name].short))
	}
	shownGroups := 0
	if !all {
		for _, g := range sv.groups {
			var names []string
			for _, n := range g.cmds {
				if s.canUse(c, sv, n) {
					names = append(names, n)
				}
			}
			if len(names) == 0 {
				continue
			}
			shownGroups++
			s.svcReply(c, sv, " ")
			s.svcReply(c, sv, g.title)
			for _, line := range wrapNames(names, 60) {
				s.svcReply(c, sv, "  "+line)
			}
		}
	}
	s.svcReply(c, sv, " ")
	if shownGroups > 0 {
		s.svcReply(c, sv, fmt.Sprintf("Every command with a line each: /%s help all", sv.alias))
	}
	s.svcReply(c, sv, fmt.Sprintf("More on one command: /%s help <command>", sv.alias))
}

// wrapNames joins names with ", " into lines of about width characters.
func wrapNames(names []string, width int) []string {
	var out []string
	line := ""
	for i, n := range names {
		piece := n
		if i < len(names)-1 {
			piece += ","
		}
		if line != "" && len(line)+1+len(piece) > width {
			out = append(out, line)
			line = ""
		}
		if line != "" {
			line += " "
		}
		line += piece
	}
	if line != "" {
		out = append(out, line)
	}
	return out
}

// helpSetup gives each service its groups, oper flags and what to hide.
func (s *Services) helpSetup() {
	full := func() bool { return s.cfg.FullPrefixes() }

	ns := s.nickServ
	ns.groups = []helpGroup{{"Commands for IRC operators", []string{"SASET", "LIST"}}}
	ns.cmdNeed = map[string]byte{"SASET": 's', "LIST": 'o'}

	cs := s.chanServ
	cs.groups = []helpGroup{
		{"Access lists", []string{"QOP", "SOP", "AOP", "HOP", "VOP", "UOP", "ACCESS"}},
		{"Channel status", []string{"OWNER", "DEOWNER", "PROTECT", "DEPROTECT", "OP", "DEOP", "HALFOP", "DEHALFOP", "VOICE", "DEVOICE", "UP", "DOWN"}},
		{"Channel management", []string{"SET", "AKICK", "BAN", "UNBAN", "KICK", "TOPIC", "CLEAR"}},
	}
	cs.cmdShown = map[string]func() bool{}
	for _, n := range []string{"QOP", "HOP", "OWNER", "DEOWNER", "PROTECT", "DEPROTECT", "HALFOP", "DEHALFOP"} {
		cs.cmdShown[n] = full
	}

	s.operServ.cmdNeed = map[string]byte{
		"GLOBAL": 'g', "LOGONNEWS": 'g', "AKILL": 'k', "KILL": 'k', "SESSION": 'k', "EXCEPTION": 'k',
		"MODE": 's', "KICK": 's', "FORBID": 's',
	}
	for _, sv := range s.services {
		sort.Strings(sv.order)
	}
}
