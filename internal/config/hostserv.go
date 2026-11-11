package config

// HostServ is the [hostserv] block: Anope 2.0.20's hostserv module, hs_request
// and the networkinfo vhost rules. Defaults are Anope's (and the owner's
// live config).
type HostServ struct {
	// VhostChars are the characters a vhost may use (Anope's vhost_chars).
	VhostChars string `toml:"vhost_chars"`
	// AllowUndotted lets a vhost have no dot (allow_undotted_vhosts).
	AllowUndotted bool `toml:"allow_undotted"`
	// DisallowStartOrEnd are characters a vhost may not start or end with.
	DisallowStartOrEnd *string `toml:"disallow_start_or_end"`
	// HostLen and UserLen are the longest vhost and vident.
	HostLen int `toml:"host_length"`
	UserLen int `toml:"ident_length"`
	// ActivateOnSet shows a vhost an oper sets right away
	// (activate_on_set); off: at the next identify or /hs on.
	ActivateOnSet bool `toml:"activate_on_set"`
	// MemoUser sends the user a memo when a request is approved or
	// rejected; MemoOpers memos opers about new requests (hs_request's
	// memouser / memooper).
	MemoUser  bool `toml:"memo_user"`
	MemoOpers bool `toml:"memo_opers"`
}

// StartOrEnd is DisallowStartOrEnd with its default ".-".
func (h HostServ) StartOrEnd() string {
	if h.DisallowStartOrEnd == nil {
		return ".-"
	}
	return *h.DisallowStartOrEnd
}

func (h *HostServ) setDefaults() {
	if h.VhostChars == "" {
		h.VhostChars = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789.-"
	}
	setInt(&h.HostLen, 64)
	setInt(&h.UserLen, 10)
}
