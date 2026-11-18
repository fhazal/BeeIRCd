package config

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestAuthPromptDefaults(t *testing.T) {
	var a AuthPrompt
	if !a.On() || !strings.HasPrefix(a.Text(), "This network asks") || !strings.HasPrefix(a.Fail(), "Login failed.") || a.Wait() != time.Minute {
		t.Errorf("defaults: on %v, %q, %q, %s", a.On(), a.Text(), a.Fail(), a.Wait())
	}
	off := false
	a = AuthPrompt{Enabled: &off, Message: "  ", FailMessage: "Nope.", Timeout: Duration{2 * time.Minute}}
	if a.On() || !strings.HasPrefix(a.Text(), "This network asks") || a.Fail() != "Nope." || a.Wait() != 2*time.Minute {
		t.Errorf("set: on %v, %q, %q, %s", a.On(), a.Text(), a.Fail(), a.Wait())
	}
}

func TestSpamReportChecks(t *testing.T) {
	good := SpamReport{Name: "dronebl", Type: "dronebl", RPCKey: "abc123", Class: 6, RateLimit: "10:1m", AutoReport: []string{"spamfilter"}}
	cases := []struct {
		c    Config
		want string
	}{
		{Config{AuthPrompt: AuthPrompt{Message: strings.Repeat("x", 301)}}, "at most 300 characters"},
		{Config{AuthPrompt: AuthPrompt{FailMessage: "two\nlines"}}, "one line each"},
		{Config{AuthPrompt: AuthPrompt{Timeout: Duration{5 * time.Second}}}, "between 10s and 10m"},
		{Config{SpamReports: []SpamReport{{Name: "drone bl", Type: "dronebl"}}}, "name"},
		{Config{SpamReports: []SpamReport{good, {Name: "DroneBL", Type: "dronebl"}}}, "twice"},
		{Config{SpamReports: []SpamReport{{Name: "x", Type: "smtp"}}}, "type"},
		{Config{SpamReports: []SpamReport{{Name: "x", Type: "dronebl", Class: 300}}}, "class"},
		{Config{SpamReports: []SpamReport{{Name: "x", Type: "dronebl", RPCKey: `a"b`}}}, "rpckey"},
		{Config{SpamReports: []SpamReport{{Name: "x", Type: "http", URL: "ftp://example.net/"}}}, "url"},
		{Config{SpamReports: []SpamReport{{Name: "x", Type: "http", URL: "https://example.net/", Method: "PUT"}}}, "method"},
		{Config{SpamReports: []SpamReport{{Name: "x", Type: "dronebl", RateLimit: "lots"}}}, "rate_limit"},
		{Config{SpamReports: []SpamReport{{Name: "x", Type: "dronebl", AutoReport: []string{"dnsbl"}}}}, "auto_report"},
	}
	for _, tc := range cases {
		err := tc.c.checkSpamReport()
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%+v: got %v, want %q", tc.c.SpamReports, err, tc.want)
		}
	}
	c := Config{SpamReports: []SpamReport{good, {Name: "hook", Type: "http", URL: "https://example.net/r?ip=$ip", Method: "POST"}}, AuthPrompt: AuthPrompt{Timeout: Duration{10 * time.Minute}}}
	if err := c.checkSpamReport(); err != nil {
		t.Fatalf("good blocks refused: %v", err)
	}

	c = Config{SpamReports: []SpamReport{{Name: "d", Type: "DroneBL", Method: "post"}}}
	c.setSpamReportDefaults()
	if r := c.SpamReports[0]; r.Type != "dronebl" || r.Class != 6 || r.RateLimit != "10:1m" || r.Method != "POST" {
		t.Errorf("defaults: %+v", r)
	}
	for in, want := range map[string]string{"5:30s": "5 30s", "3:60": "3 1m0s", "0:1m": "10 1m0s", "10:25h": "10 1m0s", "bad": "10 1m0s"} {
		n, per := SpamReport{RateLimit: in}.Rate()
		if got := fmt.Sprintf("%d %s", n, per); got != want {
			t.Errorf("Rate(%s) = %s, want %s", in, got, want)
		}
	}
}
