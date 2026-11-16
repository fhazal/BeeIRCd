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

// ShowFile is one [[showfile]] block: a command that shows a text.
type ShowFile struct {
	// Command is the command name, like "FAQ".
	Command string `toml:"command"`
	// File is the text file to show; Text is used when there is no file.
	File string `toml:"file"`
	Text string `toml:"text"`
	// Title is the first line shown. Default: the command name.
	Title string `toml:"title"`
	// Opers shows it to opers only.
	Opers bool `toml:"opers"`
}

// ExtJWT is the [extjwt] block: tokens that tell another service (a
// video call, a file upload) who someone is on IRC.
type ExtJWT struct {
	// Secret signs the tokens of the default service. Empty: EXTJWT
	// without a service name is off.
	Secret string `toml:"secret"`
	// Method is "HS256" (the default), "HS384" or "HS512".
	Method string `toml:"method"`
	// Expire is how long a token is good for. Default 30 seconds.
	Expire   Duration        `toml:"expire"`
	Services []ExtJWTService `toml:"service"`
}

// ExtJWTService is one [[extjwt.service]] block: a named service with
// its own secret.
type ExtJWTService struct {
	Name   string   `toml:"name"`
	Secret string   `toml:"secret"`
	Method string   `toml:"method"`
	Expire Duration `toml:"expire"`
}

// On reports whether any EXTJWT service is set up.
func (e *ExtJWT) On() bool { return e.Secret != "" || len(e.Services) > 0 }

func jwtMethod(m string) bool {
	switch strings.ToUpper(m) {
	case "", "HS256", "HS384", "HS512":
		return true
	}
	return false
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
	if w := c.Server.WebRedirect; w != "" && !strings.HasPrefix(w, "https://") && !strings.HasPrefix(w, "http://") {
		return fmt.Errorf("[server] web_redirect %q: write a web address that starts with https://", w)
	}
	if t := c.Server.OperTag; len(t) > 30 || strings.ContainsAny(t, "\r\n\x00") || strings.TrimSpace(t) != t {
		return fmt.Errorf("[server] oper_tag %q: a short text of at most 30 characters, like \"(IRCop)\"", t)
	}
	seen := map[string]bool{}
	for _, f := range c.ShowFiles {
		name := strings.ToUpper(f.Command)
		switch {
		case name == "" || strings.ContainsAny(name, " 	,.:"):
			return fmt.Errorf("[[showfile]] command %q: use one word, like \"FAQ\"", f.Command)
		case seen[name]:
			return fmt.Errorf("[[showfile]] command %s is there twice", name)
		case f.File == "" && f.Text == "":
			return fmt.Errorf("[[showfile]] %s: set file or text", name)
		}
		seen[name] = true
	}
	if !jwtMethod(c.ExtJWT.Method) {
		return fmt.Errorf("[extjwt] method %q: use HS256, HS384 or HS512", c.ExtJWT.Method)
	}
	seen = map[string]bool{}
	for _, v := range c.ExtJWT.Services {
		switch {
		case v.Name == "" || v.Name == "*" || strings.ContainsAny(v.Name, " 	,"):
			return fmt.Errorf("[[extjwt.service]] name %q: use one word", v.Name)
		case seen[strings.ToLower(v.Name)]:
			return fmt.Errorf("[[extjwt.service]] %s is there twice", v.Name)
		case v.Secret == "":
			return fmt.Errorf("[[extjwt.service]] %s: set secret", v.Name)
		case !jwtMethod(v.Method):
			return fmt.Errorf("[[extjwt.service]] %s: method %q: use HS256, HS384 or HS512", v.Name, v.Method)
		}
		seen[strings.ToLower(v.Name)] = true
	}
	for _, ch := range c.Server.OperJoin {
		if !strings.HasPrefix(ch, "#") || strings.ContainsAny(ch, " ,") {
			return fmt.Errorf("[server] oper_join: %q is not a channel name", ch)
		}
	}
	return nil
}
