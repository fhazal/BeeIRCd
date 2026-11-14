package link

import (
	"fmt"
	"strconv"
	"strings"
)

// SpamFilter is a spam filter rule going on (Add) or off: UnrealIRCd's
// TKL F, the same on BeeIRCd links. Reason is decoded (spaces, not _).
type SpamFilter struct {
	Source    string
	Add       bool
	Targets   string // letters, like "cpn"
	Action    byte   // UnrealIRCd action letter, like 'g'
	SetBy     string
	Expires   int64 // unix, 0 = never
	SetAt     int64
	BanTime   int64 // seconds
	Reason    string
	MatchType string // "simple" or "regex"
	Match     string
}

// encodeSpam writes TKL F (UnrealIRCd 6, TKLEXT2):
//
//	:sid TKL +|- F targets action set_by expire_at set_at ban_secs reason match_type :match
func (c *unrealCodec) encodeSpam(f *SpamFilter) string {
	sign := "-"
	if f.Add {
		sign = "+"
	}
	return fmt.Sprintf(":%s TKL %s F %s %c %s %d %d %d %s %s :%s", f.Source, sign, f.Targets, f.Action, f.SetBy,
		f.Expires, f.SetAt, f.BanTime, spamReason(f.Reason), f.MatchType, f.Match)
}

// spamReason writes a reason the UnrealIRCd way: _ for space, __ for _.
func spamReason(r string) string {
	if r == "" {
		return "No_reason"
	}
	return strings.ReplaceAll(strings.ReplaceAll(r, "_", "__"), " ", "_")
}

// decodeSpam reads TKL F; nil when the line is short or broken.
func decodeSpam(src string, p []string) any {
	if len(p) < 11 || p[2] == "" || len(p[3]) != 1 || p[10] == "" {
		return nil
	}
	f := &SpamFilter{Source: src, Add: p[0] == "+", Targets: p[2], Action: p[3][0], SetBy: p[4], MatchType: p[9], Match: p[10]}
	f.Expires, _ = strconv.ParseInt(p[5], 10, 64)
	f.SetAt, _ = strconv.ParseInt(p[6], 10, 64)
	f.BanTime, _ = strconv.ParseInt(p[7], 10, 64)
	var b strings.Builder
	r := p[8]
	for i := 0; i < len(r); i++ {
		switch {
		case r[i] == '_' && i+1 < len(r) && r[i+1] == '_':
			b.WriteByte('_')
			i++
		case r[i] == '_':
			b.WriteByte(' ')
		default:
			b.WriteByte(r[i])
		}
	}
	f.Reason = b.String()
	return f
}
