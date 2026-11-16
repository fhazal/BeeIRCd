package server

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"hash"
	"sort"
	"strings"
	"time"

	"github.com/fhazal/beeircd/internal/config"
	"github.com/fhazal/beeircd/internal/irc"
	"github.com/fhazal/beeircd/internal/state"
)

// More small options: commands of your own that show a text
// ([[showfile]]), shortened commands, a redirect for browsers, a PING
// that new connections must answer, and EXTJWT tokens.

func init() {
	commands["EXTJWT"] = command{1, false, (*Server).cmdExtJWT}
	helpTopics["EXTJWT"] = []string{"EXTJWT <#channel|*> [service]", "Gives a signed token that tells another service who you are here."}
}

// ---- [[showfile]] ----

// showFile returns the [[showfile]] block for command name, or nil.
func (s *Server) showFile(name string) *config.ShowFile {
	for i := range s.cfg.ShowFiles {
		if strings.EqualFold(s.cfg.ShowFiles[i].Command, name) {
			return &s.cfg.ShowFiles[i]
		}
	}
	return nil
}

func (s *Server) runShowFile(c *Client, f *config.ShowFile) {
	name := strings.ToUpper(f.Command)
	if f.Opers && !s.needOper(c) {
		return
	}
	lines := config.TextLines(f.File, f.Text)
	if len(lines) == 0 {
		s.notice(c, name+": the text is missing")
		return
	}
	title := f.Title
	if title == "" {
		title = name
	}
	s.motdLike(c, title, name, lines)
}

// ---- shortened commands ----

// abbreviated turns "WHOW." into the one command that starts with WHOW.
// done is true when the answer was already sent (more than one fits).
func (s *Server) abbreviated(c *Client, name string) (full string, done bool) {
	if !s.cfg.Limits.Abbreviations || len(name) < 2 || !strings.HasSuffix(name, ".") {
		return "", false
	}
	start := strings.ToUpper(name[:len(name)-1])
	var fits []string
	for cmd := range commands {
		if strings.HasPrefix(cmd, start) {
			fits = append(fits, cmd)
		}
	}
	switch len(fits) {
	case 0:
		return "", false
	case 1:
		return fits[0], false
	}
	sort.Strings(fits)
	if len(fits) > 12 {
		fits = append(fits[:12], "...")
	}
	s.reply(c, irc.ErrUnknownCommand, name, "More than one command starts like that: "+strings.Join(fits, ", "))
	return "", true
}

// ---- browsers on an IRC port ----

// webRedirect answers a web browser that opened an IRC port by mistake.
func (s *Server) webRedirect(c *Client, m irc.Message) bool {
	to := s.cfg.Server.WebRedirect
	if to == "" {
		return false
	}
	switch m.Command {
	case "GET", "POST", "HEAD", "PUT", "OPTIONS":
	default:
		return false
	}
	for _, l := range []string{"HTTP/1.1 301 Moved Permanently", "Location: " + to, "Content-Length: 0", "Connection: close", ""} {
		c.queueLine(l + crlf)
	}
	s.quit(c, "This is an IRC server, not a website")
	return true
}

// ---- ping cookie ----

// pingCookie reports whether c still owes the answer to its first PING,
// and sends that PING the first time.
func (s *Server) pingCookie(c *Client) bool {
	if !s.cfg.Server.PingCookie || c.cookieOK {
		return false
	}
	if c.cookie == "" {
		b := make([]byte, 4)
		rand.Read(b)
		c.cookie = strings.ToUpper(hex.EncodeToString(b))
		c.send("PING :" + c.cookie)
		c.send(":" + s.cfg.Server.Name + " NOTICE * :*** If your connection stops here, type /quote PONG " + c.cookie)
	}
	return true
}

// ---- EXTJWT ----

func (s *Server) cmdExtJWT(c *Client, m irc.Message) {
	cfg := &s.cfg.ExtJWT
	if !cfg.On() {
		s.reply(c, irc.ErrUnknownCommand, "EXTJWT", "Unknown command")
		return
	}
	fail := func(code, about, text string) {
		c.send(irc.Message{Source: s.cfg.Server.Name, Command: "FAIL", Params: []string{"EXTJWT", code, about, text}, Trailing: true}.String())
	}
	secret, method, expire, service := cfg.Secret, cfg.Method, cfg.Expire.Duration, "*"
	if name := m.Param(1); name != "" && name != "*" {
		secret = ""
		for _, v := range cfg.Services {
			if strings.EqualFold(v.Name, name) {
				secret, service = v.Secret, v.Name
				if v.Method != "" {
					method = v.Method
				}
				if v.Expire.Duration > 0 {
					expire = v.Expire.Duration
				}
			}
		}
		if secret == "" {
			fail("NO_SUCH_SERVICE", name, "No such service")
			return
		}
	} else if secret == "" {
		fail("NO_SUCH_SERVICE", "*", "Give the name of a service")
		return
	}
	if expire <= 0 {
		expire = 30 * time.Second
	}
	u := c.user
	umodes := []string{}
	for _, l := range userModes {
		if u.HasMode(byte(l)) {
			umodes = append(umodes, string(l))
		}
	}
	claims := map[string]any{
		"exp":     time.Now().Add(expire).Unix(),
		"iss":     s.cfg.Server.Name,
		"sub":     u.Nick,
		"account": u.Account,
		"umodes":  umodes,
	}
	target := m.Param(0)
	if target != "*" {
		ch := s.net.Channel(target)
		var mem *state.Member
		if ch != nil {
			mem = ch.Member(u)
		}
		if ch == nil || (mem == nil && (ch.HasMode('s') || ch.HasMode('p'))) {
			fail("NO_SUCH_CHANNEL", target, "No such channel")
			return
		}
		target = ch.Name
		joined, cmodes := int64(0), []string{}
		if mem != nil {
			joined = mem.Joined.Unix()
			for _, l := range s.rankModes() {
				if mem.Ranks&state.RankForMode(byte(l)) != 0 {
					cmodes = append(cmodes, string(l))
				}
			}
		}
		claims["channel"], claims["joined"], claims["cmodes"] = ch.Name, joined, cmodes
	}
	token := signJWT(method, secret, claims)
	for len(token) > 200 {
		c.send(irc.Message{Source: s.cfg.Server.Name, Command: "EXTJWT", Params: []string{target, service, "*", token[:200]}}.String())
		token = token[200:]
	}
	c.send(irc.Message{Source: s.cfg.Server.Name, Command: "EXTJWT", Params: []string{target, service, token}}.String())
}

// signJWT makes a JSON Web Token signed with HMAC and a shared secret.
func signJWT(method, secret string, claims map[string]any) string {
	method = strings.ToUpper(method)
	var h func() hash.Hash
	switch method {
	case "HS384":
		h = sha512.New384
	case "HS512":
		h = sha512.New
	default:
		method, h = "HS256", sha256.New
	}
	enc := base64.RawURLEncoding
	head := enc.EncodeToString([]byte(`{"alg":"` + method + `","typ":"JWT"}`))
	body, _ := json.Marshal(claims)
	signed := head + "." + enc.EncodeToString(body)
	mac := hmac.New(h, []byte(secret))
	mac.Write([]byte(signed))
	return signed + "." + enc.EncodeToString(mac.Sum(nil))
}
