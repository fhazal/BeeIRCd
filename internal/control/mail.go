package control

// The Mail card on the Settings page: owner only.
const (
	CmdMail     = "mail"      // -> Mail
	CmdMailSave = "mail-save" // MailForm -> Mail
	CmdMailTest = "mail-test" // MailForm (unsaved is fine) + Value (send to) -> MailTest
	CmdMailDNS  = "mail-dns"  // -> MailTest (Checks only, for the saved settings)
	CmdMailReg  = "mail-reg"  // Value (none, mail, admin) -> Mail
)

// MailForm is the Mail card as the owner filled it in. Password "" keeps
// the saved one.
type MailForm struct {
	Provider string `json:"provider"` // off, gmail, outlook, zoho, own
	From     string `json:"from"`
	Name     string `json:"name"`
	Server   string `json:"server"` // own: host:port
	User     string `json:"user"`   // own: the login
	Password string `json:"password"`
}

// Mail is how services send mail now.
type Mail struct {
	Provider     string `json:"provider"` // off, sendmail, gmail, outlook, zoho, own
	From         string `json:"from"`
	Name         string `json:"name"`
	Server       string `json:"server"`
	User         string `json:"user"`
	HasPassword  bool   `json:"hasPassword"`
	Registration string `json:"registration"` // none, mail, admin
	Expire       string `json:"expire"`       // unconfirmed accounts go after
	// Tested is when a test mail with exactly these settings last went
	// through (Unix seconds, 0 never); Confirm by email needs one.
	Tested   int64    `json:"tested,omitempty"`
	Presets  []Preset `json:"presets"`
	Problems []string `json:"problems,omitempty"`
}

// Preset is a mail service the card offers.
type Preset struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Server string `json:"server"`
}

// MailTest is what a test mail did, and the spam check.
type MailTest struct {
	OK      bool        `json:"ok"`
	To      string      `json:"to,omitempty"`
	Took    int64       `json:"took,omitempty"` // milliseconds
	Explain string      `json:"explain,omitempty"`
	Raw     string      `json:"raw,omitempty"` // the mail server's own words
	Domain  string      `json:"domain,omitempty"`
	Checks  []MailCheck `json:"checks,omitempty"`
}

// MailCheck is one line of "Will it land in the inbox?".
type MailCheck struct {
	Name  string `json:"name"`
	State string `json:"state"` // ok, warn, bad
	Text  string `json:"text"`
	Fix   string `json:"fix,omitempty"`
}
