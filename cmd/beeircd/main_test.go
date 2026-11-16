package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRun(t *testing.T) {
	cases := []struct {
		name   string
		args   []string
		code   int
		outHas string
		errHas string
	}{
		{"no args shows usage", nil, 0, "Usage: beeircd", ""},
		{"help", []string{"help"}, 0, "Usage: beeircd", ""},
		{"version", []string{"version"}, 0, "BeeIRCd ", ""},
		{"version is case blind", []string{"VERSION"}, 0, "BeeIRCd ", ""},
		{"unknown command", []string{"fly"}, 2, "", `Unknown command "fly"`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var out, errb bytes.Buffer
			code := run(c.args, &out, &errb)
			if code != c.code {
				t.Errorf("exit code %d, want %d", code, c.code)
			}
			if c.outHas != "" && !strings.Contains(out.String(), c.outHas) {
				t.Errorf("stdout %q lacks %q", out.String(), c.outHas)
			}
			if c.errHas != "" && !strings.Contains(errb.String(), c.errHas) {
				t.Errorf("stderr %q lacks %q", errb.String(), c.errHas)
			}
			if c.errHas == "" && errb.Len() > 0 {
				t.Errorf("unexpected stderr %q", errb.String())
			}
		})
	}
}

func TestMkpasswd(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := mkpasswd(strings.NewReader("Hive-2026-secret\n"), &out, &errOut); code != 0 {
		t.Fatalf("code %d: %s", code, errOut.String())
	}
	if !strings.HasPrefix(out.String(), "$argon2id$") {
		t.Fatalf("hash = %q", out.String())
	}
	if code := mkpasswd(strings.NewReader("short\n"), &out, &errOut); code != 2 {
		t.Fatalf("short password accepted")
	}
}

func TestModuleTools(t *testing.T) {
	dir := t.TempDir()
	var out, errOut bytes.Buffer
	if code := run([]string{"module", "new", "greet", "-dir", dir}, &out, &errOut); code != 0 {
		t.Fatalf("module new: %d %s", code, errOut.String())
	}
	path := filepath.Join(dir, "greet.lua")
	out.Reset()
	if code := run([]string{"module", "check", path}, &out, &errOut); code != 0 || !strings.Contains(out.String(), "no mistakes found") {
		t.Fatalf("module check of the starter: %d %s %s", code, out.String(), errOut.String())
	}
	if code := run([]string{"module", "new", "greet", "-dir", dir}, &out, &errOut); code != 1 || !strings.Contains(errOut.String(), "is there already") {
		t.Fatalf("writing over a module: %d %s", code, errOut.String())
	}
	bad := filepath.Join(dir, "bad.lua")
	os.WriteFile(bad, []byte("module { name = \"bad\", version = \"1\" }\non(\"joined\", function() end)\n"), 0o600)
	out.Reset()
	if code := run([]string{"module", "check", bad}, &out, &errOut); code != 1 || !strings.Contains(out.String(), "there is no such hook") {
		t.Fatalf("module check of a bad module: %d %s", code, out.String())
	}
	out.Reset()
	if code := run([]string{"module", "hooks"}, &out, &errOut); code != 0 || !strings.Contains(out.String(), "check_message") {
		t.Fatalf("module hooks: %d %s", code, out.String())
	}
	if code := run([]string{"module", "new", "Bad Name", "-dir", dir}, &out, &errOut); code != 2 {
		t.Fatalf("a bad name: %d", code)
	}
}
