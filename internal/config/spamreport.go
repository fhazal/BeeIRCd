package config

import (
	"fmt"
	"net/url"
	"slices"
	"strings"
	"time"
)

// AuthPrompt is [authprompt], like UnrealIRCd's authprompt module: someone
// a soft ban stops who isn't logged in is held at connect and asked to
// log in with /AUTH instead of being refused at once.
type AuthPrompt struct {
	// Enabled is on by default.
	Enabled *bool `toml:"enabled"`
	// Message is the first notice they get; the how-to line follows it.
	Message string `toml:"message"`
	// FailMessage is the notice after a wrong password.
	FailMessage string `toml:"fail_message"`
	// Timeout is how long they have to log in (60s by default).
	Timeout Duration `toml:"timeout"`
}

// Default texts of [authprompt].
const (
	AuthPromptMessage = "This network asks people from your address to log in to a registered nick."
	AuthPromptFail    = "Login failed. Check the nick and password and try /AUTH again."
)

// On reports whether the prompt is used.
func (a AuthPrompt) On() bool { return a.Enabled == nil || *a.Enabled }

// Text is the first notice.
func (a AuthPrompt) Text() string { return orDefault(a.Message, AuthPromptMessage) }

// Fail is the notice after a wrong password.
func (a AuthPrompt) Fail() string { return orDefault(a.FailMessage, AuthPromptFail) }

// Wait is how long they have to log in.
func (a AuthPrompt) Wait() time.Duration {
	if a.Timeout.Duration <= 0 {
		return 60 * time.Second
	}
	return a.Timeout.Duration
}

func orDefault(s, def string) string {
	if strings.TrimSpace(s) == "" {
		return def
	}
	return s
}

// SpamReport is one [[spamreport]] block: a place spammers' IPs are
// reported to, like UnrealIRCd's spamreport block. type "dronebl" uses
// DroneBL's RPC2 with your own rpckey; type "http" calls url.
type SpamReport struct {
	Name string `toml:"name"`
	Type string `toml:"type"`
	// RPCKey is your DroneBL key (dronebl.org/rpckey_signup).
	RPCKey string `toml:"rpckey"`
	// Class is the DroneBL listing type (6 = unknown spambot or drone).
	Class int `toml:"class"`
	// Staging sends to DroneBL's test list instead of the real one.
	Staging bool `toml:"staging"`
	// URL is for type "http": $ip, $nick and $reason are filled in. For
	// type "dronebl" it replaces DroneBL's RPC2 address (for testing).
	URL string `toml:"url"`
	// Method is GET (the default) or POST for type "http".
	Method string `toml:"method"`
	// RateLimit is "reports:time", like "10:1m" (the default).
	RateLimit string `toml:"rate_limit"`
	// AutoReport says what reports on its own: "spamfilter" (a spam
	// filter rule that removes or bans someone).
	AutoReport []string `toml:"auto_report"`
	// Enabled is on by default.
	Enabled *bool `toml:"enabled"`
}

// SpamReportTypes and SpamReportAuto are the values type and auto_report take.
var (
	SpamReportTypes = []string{"dronebl", "http"}
	SpamReportAuto  = []string{"spamfilter"}
)

// On reports whether the block is used.
func (r SpamReport) On() bool { return r.Enabled == nil || *r.Enabled }

// Auto reports whether what (like "spamfilter") reports on its own.
func (r SpamReport) Auto(what string) bool { return slices.Contains(r.AutoReport, what) }

// Rate is the rate limit: at most n reports per per.
func (r SpamReport) Rate() (int, time.Duration) {
	n, per, ok := parseCountPer(r.RateLimit)
	if !ok {
		return 10, time.Minute
	}
	return n, per
}

// parseCountPer reads "10:1m" (or "10:60", seconds).
func parseCountPer(v string) (int, time.Duration, bool) {
	a, b, ok := strings.Cut(strings.TrimSpace(v), ":")
	if !ok {
		return 0, 0, false
	}
	var n int
	if _, err := fmt.Sscan(a, &n); err != nil || n < 1 || n > 10000 {
		return 0, 0, false
	}
	var d Duration
	if err := d.UnmarshalText([]byte(b)); err != nil {
		var sec int
		if _, err := fmt.Sscan(b, &sec); err != nil {
			return 0, 0, false
		}
		d.Duration = time.Duration(sec) * time.Second
	}
	if d.Duration < time.Second || d.Duration > 24*time.Hour {
		return 0, 0, false
	}
	return n, d.Duration, true
}

func (c *Config) setSpamReportDefaults() {
	for i := range c.SpamReports {
		r := &c.SpamReports[i]
		r.Type = strings.ToLower(r.Type)
		if r.Type == "" {
			r.Type = "dronebl"
		}
		if r.Class == 0 {
			r.Class = 6
		}
		if r.RateLimit == "" {
			r.RateLimit = "10:1m"
		}
		r.Method = strings.ToUpper(r.Method)
		if r.Method == "" {
			r.Method = "GET"
		}
	}
}

func (c *Config) checkSpamReport() error {
	a := c.AuthPrompt
	switch {
	case len(a.Message) > 300 || len(a.FailMessage) > 300:
		return fmt.Errorf("[authprompt] message and fail_message are at most 300 characters")
	case strings.ContainsAny(a.Message+a.FailMessage, "\r\n\x00"):
		return fmt.Errorf("[authprompt] message and fail_message are one line each")
	case a.Timeout.Duration != 0 && (a.Timeout.Duration < 10*time.Second || a.Timeout.Duration > 10*time.Minute):
		return fmt.Errorf("[authprompt] timeout must be between 10s and 10m")
	}
	seen := map[string]bool{}
	for _, r := range c.SpamReports {
		name := strings.ToLower(r.Name)
		switch {
		case !nameRe(name):
			return fmt.Errorf("[[spamreport]] name %q: use letters, digits, - and _ (like \"dronebl\")", r.Name)
		case seen[name]:
			return fmt.Errorf("[[spamreport]] %s is in the file twice", r.Name)
		case !slices.Contains(SpamReportTypes, r.Type):
			return fmt.Errorf("[[spamreport]] %s: type %q must be dronebl or http", r.Name, r.Type)
		case r.Type == "dronebl" && r.Class != 0 && (r.Class < 1 || r.Class > 255):
			return fmt.Errorf("[[spamreport]] %s: class must be 1 to 255", r.Name)
		case r.Type == "dronebl" && strings.ContainsAny(r.RPCKey, "\"'<>& \t"):
			return fmt.Errorf("[[spamreport]] %s: rpckey has characters a DroneBL key doesn't have", r.Name)
		case (r.Type == "http" || r.URL != "") && !httpURL(r.URL):
			return fmt.Errorf("[[spamreport]] %s: url must be an http:// or https:// address", r.Name)
		case r.Method != "" && r.Method != "GET" && r.Method != "POST":
			return fmt.Errorf("[[spamreport]] %s: method must be GET or POST", r.Name)
		}
		if _, _, ok := parseCountPer(r.RateLimit); r.RateLimit != "" && !ok {
			return fmt.Errorf("[[spamreport]] %s: rate_limit %q must be reports:time like 10:1m", r.Name, r.RateLimit)
		}
		for _, w := range r.AutoReport {
			if !slices.Contains(SpamReportAuto, w) {
				return fmt.Errorf("[[spamreport]] %s: auto_report %q: the only one is \"spamfilter\"", r.Name, w)
			}
		}
		seen[name] = true
	}
	return nil
}

func nameRe(s string) bool {
	if s == "" || len(s) > 30 {
		return false
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
			return false
		}
	}
	return true
}

func httpURL(s string) bool {
	u, err := url.Parse(s)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != ""
}
