package config

import (
	"fmt"
	"strings"
)

// CreateMode is [channels] create in lower case, "everyone" when not set.
func (c *Channels) CreateMode() string {
	if c.Create == "" {
		return "everyone"
	}
	return strings.ToLower(c.Create)
}

// neverOff are commands [limits] disabled_commands can't switch off:
// without them nobody could connect, stay connected or leave.
var neverOff = map[string]bool{
	"NICK": true, "USER": true, "PASS": true, "CAP": true, "AUTHENTICATE": true,
	"PING": true, "PONG": true, "QUIT": true, "OPER": true, "WEBIRC": true, "STARTTLS": true,
}

// checkSmall checks the small options spread over [server], [limits] and
// [channels].
func (c *Config) checkSmall() error {
	switch c.Channels.CreateMode() {
	case "everyone", "registered", "opers":
	default:
		return fmt.Errorf("[channels] create %q: use \"everyone\", \"registered\" or \"opers\"", c.Channels.Create)
	}
	for _, name := range c.Limits.DisabledCommands {
		if neverOff[strings.ToUpper(name)] {
			return fmt.Errorf("[limits] disabled_commands: %s can't be switched off", strings.ToUpper(name))
		}
		if name == "" || strings.ContainsAny(name, " \t,") {
			return fmt.Errorf("[limits] disabled_commands: %q is not one command name", name)
		}
	}
	for what, m := range map[string]string{"connect_modes": c.Server.ConnectModes, "oper_modes": c.Server.OperModes} {
		if m == "" {
			continue
		}
		if !strings.HasPrefix(m, "+") || strings.ContainsAny(m[1:], "+- ") {
			return fmt.Errorf("[server] %s %q: write it like \"+RT\"", what, m)
		}
		for _, l := range m[1:] {
			// o, r and z are given by /oper, NickServ and TLS; s comes with +o.
			if strings.ContainsRune("orz", l) {
				return fmt.Errorf("[server] %s: +%c can't be given this way", what, l)
			}
		}
	}
	for _, ch := range c.Server.OperJoin {
		if !strings.HasPrefix(ch, "#") || strings.ContainsAny(ch, " ,") {
			return fmt.Errorf("[server] oper_join: %q is not a channel name", ch)
		}
	}
	return nil
}
