package control

// Spam report places ([[spamreport]], after 1.2.0).
const (
	CmdSpamReports       = "spam-reports"        // -> SpamReports (flag a)
	CmdSpamReportKey     = "spam-report-key"     // Name + Value (the DroneBL key, "" removes it) -> Done (flag a)
	CmdSpamReportStaging = "spam-report-staging" // Name + Value ("on" or "off") -> Done (flag a)
	CmdSpamReport        = "spam-report"         // UID -> Done (flag k): report that person
)

// SpamReports lists the [[spamreport]] blocks; a key is never sent, only
// whether one is set.
type SpamReports struct {
	Places []SpamPlace `json:"places"`
}

// SpamPlace is one [[spamreport]] block.
type SpamPlace struct {
	Name    string `json:"name"`
	Type    string `json:"type"` // dronebl or http
	KeySet  bool   `json:"keySet,omitempty"`
	Staging bool   `json:"staging,omitempty"`
	On      bool   `json:"on"`
	Auto    bool   `json:"auto,omitempty"` // the spam filter reports on its own
	Line    string `json:"line"`           // as AdminServ SPAMREPORT LIST says it
}
