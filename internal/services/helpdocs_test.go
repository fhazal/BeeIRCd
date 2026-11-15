package services

import (
	"sort"
	"strings"
	"testing"
)

// Every services command has its syntax, a line for HELP and a HELP text.
// beeircd.org/docs prints these, so a command added without them fails here.
func TestEveryCommandHasHelp(t *testing.T) {
	s, _ := newServices(t)
	var missing []string
	for _, sv := range s.services {
		if sv.botID != 0 {
			continue
		}
		for name, cmd := range sv.cmds {
			var no []string
			if strings.TrimSpace(cmd.syntax) == "" {
				no = append(no, "syntax")
			}
			if strings.TrimSpace(cmd.short) == "" {
				no = append(no, "HELP line")
			}
			if strings.TrimSpace(cmd.long) == "" {
				no = append(no, "HELP text")
			}
			if len(no) > 0 {
				missing = append(missing, sv.user.Nick+" "+name+": no "+strings.Join(no, ", "))
			}
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Errorf("services commands without help:\n  %s", strings.Join(missing, "\n  "))
	}
}
