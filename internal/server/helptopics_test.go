package server

import (
	"sort"
	"strings"
	"testing"
)

// Every IRC command a client can send has a HELP topic, so /HELP <command>
// answers and beeircd.org/docs lists it. Service aliases (/NS, /CS ...) are
// covered by each service's own HELP.
func TestEveryCommandHasHelpTopic(t *testing.T) {
	skip := map[string]bool{"HELP": true, "HELPOP": true, "WEBIRC": true}
	for _, sv := range []string{"NICKSERV", "NS", "CHANSERV", "CS", "MEMOSERV", "MS", "OPERSERV", "OS",
		"HOSTSERV", "HS", "BOTSERV", "BS", "ADMINSERV", "AS"} {
		skip[sv] = true
	}
	var missing []string
	for name := range commands {
		if !skip[name] && helpTopics[name] == nil {
			missing = append(missing, name)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Errorf("IRC commands without a HELP topic in helpTopics (info.go): %s", strings.Join(missing, " "))
	}
}
