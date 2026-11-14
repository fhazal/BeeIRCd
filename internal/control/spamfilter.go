package control

import "time"

// Spam filter page (after 1.0), all flag k like /SPAMFILTER.
const (
	CmdSpam      = "spam"       // -> Spam
	CmdSpamCheck = "spam-check" // Spam rule fields -> SpamCheck (nothing is added)
	CmdSpamAdd   = "spam-add"   // Kind (simple/regex) + Value (targets) + Name (action) + Life (ban time) + Reason + Mask (match) + New (add anyway) -> Done
	CmdSpamDel   = "spam-del"   // ID -> Done
	CmdSpamTest  = "spam-test"  // Value (targets) + Mask (a line of text) -> SpamTest
)

// Spam is the Spam filter page.
type Spam struct {
	Rules     []SpamRule `json:"rules"`
	Caught24h int        `json:"caught24h"`
	Except    []string   `json:"except"` // [spamfilter] except channels
}

// SpamRule is one rule as the page shows it.
type SpamRule struct {
	ID       int64     `json:"id"`
	Type     string    `json:"type"` // simple, regex
	Match    string    `json:"match"`
	Targets  string    `json:"targets"`
	Words    []string  `json:"words"` // the targets in plain words
	Action   string    `json:"action"`
	Does     string    `json:"does"` // "Block", "AKILL 1d" ...
	Reason   string    `json:"reason"`
	SetBy    string    `json:"set_by"`
	SetAt    time.Time `json:"set_at"`
	Expires  time.Time `json:"expires,omitzero"`
	Hits     int       `json:"hits"`
	Last     time.Time `json:"last,omitzero"`
	Unusable bool      `json:"unusable,omitempty"` // a regex from another server this one can't read
}

// SpamCheck is what adding a rule would do.
type SpamCheck struct {
	Problem string   `json:"problem,omitempty"` // why it can't be added
	Unsafe  []string `json:"unsafe,omitempty"`  // normal lines it would catch
	Lines   int      `json:"lines"`             // how many normal lines were tried
}

// SpamTest is which rules would catch a line.
type SpamTest struct {
	Hits []SpamTestHit `json:"hits"`
}

// SpamTestHit is one rule that would catch the line.
type SpamTestHit struct {
	ID     int64  `json:"id"`
	Match  string `json:"match"`
	Where  string `json:"where"`
	Does   string `json:"does"`
	Reason string `json:"reason"`
}
