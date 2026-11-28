package server

import (
	"strings"

	"github.com/fhazal/beeircd/internal/irc"
)

// [log] oper_commands (like InspIRCd's operlog): every command an oper
// uses that needs oper rights goes to the server log and the log channel
// (event opercmd). runCommand notes the running command in c.operCmd;
// needOper and needFlag log it here once it passes their check.

// secretParams are the parameters (by index) never written to a log.
var secretParams = map[string][]int{"OPER": {1}, "PASS": {0}, "AUTHENTICATE": {0}, "WEBIRC": {0}}

// operCmdText is the command m as logged: secret parameters as *****.
func operCmdText(m irc.Message) string {
	parts := []string{m.Command}
	for i, p := range m.Params {
		for _, j := range secretParams[m.Command] {
			if i == j {
				p = "*****"
			}
		}
		if i == len(m.Params)-1 && (p == "" || strings.ContainsAny(p, " :")) {
			p = ":" + p
		}
		parts = append(parts, p)
	}
	return strings.Join(parts, " ")
}

// logOperCmd logs the command c is running, once, when [log]
// oper_commands is on.
func (s *Server) logOperCmd(c *Client) {
	m := c.operCmd
	if m == nil || c.user == nil {
		return
	}
	c.operCmd = nil
	who := c.logMask(c.nick)
	var about []string
	if c.user.Account != "" {
		about = append(about, "account "+c.user.Account)
	}
	if c.user.OperName != "" {
		about = append(about, "oper "+c.user.OperName)
	}
	if len(about) > 0 {
		who += " (" + strings.Join(about, ", ") + ")"
	}
	text := "OPERCMD: " + who + " used " + operCmdText(*m)
	s.log.Printf("%s", text)
	s.svc.LogOperCommand(text)
}
