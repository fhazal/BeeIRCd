package config

import (
	"os"
	"reflect"
	"regexp"
	"strings"
	"testing"
)

// Every setting beeircd.conf takes is described in docs/ADMIN.md, in the
// table of its section. beeircd.org/docs is built from that file, so a new
// setting without a row there fails here instead of going unnoticed.
func TestEverySettingIsDocumented(t *testing.T) {
	raw, err := os.ReadFile("../../docs/ADMIN.md")
	if err != nil {
		t.Fatal(err)
	}
	documented := docSettings(string(raw))
	var missing []string
	ct := reflect.TypeOf(Config{})
	for i := 0; i < ct.NumField(); i++ {
		f := ct.Field(i)
		sec := docKey(f)
		if sec == "" {
			continue
		}
		have, ok := documented[sec]
		if !ok {
			missing = append(missing, "["+sec+"] (no section)")
			continue
		}
		for _, k := range settingKeys(elem(f.Type)) {
			if !have[k] {
				missing = append(missing, "["+sec+"] "+k)
			}
		}
	}
	if len(missing) > 0 {
		t.Errorf("settings missing from the beeircd.conf tables in docs/ADMIN.md:\n  %s", strings.Join(missing, "\n  "))
	}
}

// docSettings reads the "## beeircd.conf" part of ADMIN.md: for each
// "### [section]" heading, the setting names in its tables' first column.
func docSettings(md string) map[string]map[string]bool {
	out := map[string]map[string]bool{}
	head := regexp.MustCompile("^### `?\\[\\[?([a-z0-9_.]+)\\]\\]?`?\\s*$")
	key := regexp.MustCompile("`([a-z0-9_]+)`")
	in, sec := false, ""
	for _, l := range strings.Split(strings.ReplaceAll(md, "\r\n", "\n"), "\n") {
		switch {
		case strings.HasPrefix(l, "## "):
			in = strings.TrimSpace(l[3:]) == "beeircd.conf"
			sec = ""
		case in && strings.HasPrefix(l, "### "):
			if m := head.FindStringSubmatch(l); m != nil {
				sec = m[1]
				out[sec] = map[string]bool{}
			} else {
				sec = ""
			}
		case in && sec != "" && strings.HasPrefix(l, "|"):
			cells := strings.Split(l, "|")
			if len(cells) > 2 {
				for _, m := range key.FindAllStringSubmatch(cells[1], -1) {
					out[sec][m[1]] = true
				}
			}
		}
	}
	return out
}

// settingKeys lists the toml names a section takes. A list of blocks inside
// a section (like [[link.server]]) counts as part of it.
func settingKeys(t reflect.Type) []string {
	var keys []string
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		name := docKey(f)
		if name == "" {
			continue
		}
		if e := elem(f.Type); f.Type.Kind() == reflect.Slice && e.Kind() == reflect.Struct && e != reflect.TypeOf(Duration{}) {
			keys = append(keys, settingKeys(e)...)
			continue
		}
		keys = append(keys, name)
	}
	return keys
}

func docKey(f reflect.StructField) string {
	if !f.IsExported() {
		return ""
	}
	tag := strings.Split(f.Tag.Get("toml"), ",")[0]
	if tag == "-" || tag == "" {
		return ""
	}
	return tag
}

// elem is the struct a field holds: itself, or what a slice or pointer holds.
func elem(t reflect.Type) reflect.Type {
	for t.Kind() == reflect.Slice || t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	return t
}
