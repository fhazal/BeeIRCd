package config

import (
	"fmt"
	"net"
	"time"
)

// Web is the [web] section: the Hive Panel (beeircd-web). The server
// itself never reads it; it is here so one beeircd.conf holds everything
// and `beeircd check` checks it.
type Web struct {
	// Listen is where the panel answers. Only a loopback address: reach
	// it over an SSH tunnel or through a reverse proxy on this machine.
	Listen string `toml:"listen"`
	// TrustedProxy are reverse proxies (on this machine) whose
	// X-Forwarded-For and X-Forwarded-Proto the panel believes.
	TrustedProxy []string `toml:"trusted_proxy"`
	// SessionIdle signs an oper out after this long without a click.
	SessionIdle Duration `toml:"session_idle"`
	// Members lets everyone with a registered nick sign in with their
	// NickServ password, to the member pages only (Chat, My nick, My
	// channels). Off: only opers sign in, as before.
	Members bool `toml:"members"`
}

// InContainer is set in the Docker image: the panel then listens inside
// its own container, and compose.yaml publishes it on 127.0.0.1 only.
var InContainer bool

// DefaultWebListen is where the panel answers when [web] listen is empty.
const DefaultWebListen = "127.0.0.1:8787"

func (w *Web) setDefaults() {
	if w.Listen == "" {
		w.Listen = DefaultWebListen
	}
	setDur(&w.SessionIdle, 30*time.Minute)
}

func (w *Web) check() error {
	host, _, err := net.SplitHostPort(w.Listen)
	if err != nil {
		return fmt.Errorf("[web] listen %q: %v", w.Listen, err)
	}
	if ip := net.ParseIP(host); !InContainer && host != "localhost" && (ip == nil || !ip.IsLoopback()) {
		return fmt.Errorf("[web] listen %q: the Hive Panel only answers on this machine (127.0.0.1 or ::1); reach it with an SSH tunnel or a reverse proxy", w.Listen)
	}
	for _, p := range w.TrustedProxy {
		if net.ParseIP(p) == nil {
			return fmt.Errorf("[web] trusted_proxy %q is not an IP address", p)
		}
	}
	if d := w.SessionIdle.Duration; d < time.Minute || d > 24*time.Hour {
		return fmt.Errorf("[web] session_idle %s: use 1m to 24h", FormatDuration(d))
	}
	return nil
}
