package config

import (
	"fmt"
	"strings"
)

// Censor is [censor]: the words channel mode +G hides, like UnrealIRCd's
// badword blocks. Empty by default, so +G does nothing until words are
// added here.
type Censor struct {
	// Words are single words, matched whole and without regard to case; a
	// * stands for any letters, so "*spam*" also hides "spammer".
	Words []string `toml:"words"`
	// Replace is what each hidden word becomes ("<censored>" when empty).
	Replace string `toml:"replace"`
}

// CensorReplace is the text a censored word becomes.
func (c *Config) CensorReplace() string {
	if c.Censor.Replace == "" {
		return "<censored>"
	}
	return c.Censor.Replace
}

func (c *Config) checkCensor() error {
	if len(c.Censor.Words) > 500 {
		return fmt.Errorf("[censor] words: %d words, at most 500", len(c.Censor.Words))
	}
	for _, w := range c.Censor.Words {
		if strings.Trim(w, "*") == "" || strings.ContainsAny(w, " \t,") {
			return fmt.Errorf("[censor] words: %q is not a single word", w)
		}
	}
	if len(c.Censor.Replace) > 50 || strings.ContainsAny(c.Censor.Replace, "\r\n") {
		return fmt.Errorf("[censor] replace: at most 50 characters on one line")
	}
	return nil
}
