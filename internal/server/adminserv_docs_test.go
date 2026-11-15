package server

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// docs/ADMIN.md lists every setting AdminServ SET changes ("Settings for
// SET"), and only those. beeircd.org/docs is built from that file.
func TestAdminServSettingsDocumented(t *testing.T) {
	raw, err := os.ReadFile("../../docs/ADMIN.md")
	if err != nil {
		t.Fatal(err)
	}
	md := strings.ReplaceAll(string(raw), "\r\n", "\n")
	start := strings.Index(md, "**Settings for `SET`:**")
	if start < 0 {
		t.Fatal("docs/ADMIN.md has no \"Settings for `SET`\" list")
	}
	list := md[start:]
	if end := strings.Index(list, "\n\n"); end > 0 {
		list = list[:end]
	}
	doc := map[string]bool{}
	for _, m := range regexp.MustCompile("`([a-z0-9-]+)`").FindAllStringSubmatch(list, -1) {
		doc[m[1]] = true
	}
	code := map[string]bool{}
	for _, st := range settings() {
		code[st.name] = true
		if !doc[st.name] {
			t.Errorf("AdminServ SET %s is missing from the list in docs/ADMIN.md", st.name)
		}
	}
	for name := range doc {
		if !code[name] && name != "set" {
			t.Errorf("docs/ADMIN.md lists SET %s, which AdminServ doesn't have", name)
		}
	}
}
