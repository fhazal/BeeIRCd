package main

import (
	"bufio"
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fhazal/beeircd/internal/store"
)

func importConf(t *testing.T) (conf, db string) {
	t.Helper()
	dir := t.TempDir()
	db = filepath.Join(dir, "services.db")
	conf = filepath.Join(dir, "beeircd.conf")
	body := `[server]
name = "irc.import.test"
network = "ImportNet"
sid = "0IM"

[[listen]]
address = "127.0.0.1:0"

[cloak]
secret = "0123456789abcdef-import"

[services]
database = "` + filepath.ToSlash(db) + `"

[control]
socket = "` + filepath.ToSlash(filepath.Join(dir, "beeircd.sock")) + `"

[backup]
dir = "` + filepath.ToSlash(filepath.Join(dir, "backups")) + `"

[history]
file = "` + filepath.ToSlash(filepath.Join(dir, "history.db")) + `"
`
	if err := os.WriteFile(conf, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return conf, db
}

const sampleDB = "../../internal/anopedb/testdata/anope-sample.db"

func TestImportAnopeCommand(t *testing.T) {
	conf, db := importConf(t)
	var out, errb bytes.Buffer
	// Dry run: the report, nothing written.
	if code := importAnope([]string{"-config", conf, sampleDB, "-dry-run"}, &out, &errb, importRun{}); code != 0 {
		t.Fatalf("dry run exit %d: %s%s", code, out.String(), errb.String())
	}
	if !strings.Contains(out.String(), "5 accounts") || !strings.Contains(out.String(), "Dry run") {
		t.Errorf("dry run report:\n%s", out.String())
	}
	if _, err := os.Stat(db); err == nil {
		t.Error("the dry run made services.db")
	}

	// No terminal and no -yes: refused.
	out.Reset()
	if code := importAnope([]string{"-config", conf, sampleDB}, &out, &errb, importRun{}); code != 1 || !strings.Contains(out.String(), "-yes") {
		t.Errorf("without a terminal: exit %d\n%s", code, out.String())
	}

	// A "no" on the terminal changes nothing; "y" imports.
	out.Reset()
	if code := importAnope([]string{"-config", conf, sampleDB}, &out, &errb, importRun{in: bufio.NewReader(strings.NewReader("n\n")), tty: true}); code != 0 || !strings.Contains(out.String(), "Nothing changed") {
		t.Errorf("answer n: exit %d\n%s", code, out.String())
	}
	out.Reset()
	if code := importAnope([]string{"-config", conf, sampleDB}, &out, &errb, importRun{in: bufio.NewReader(strings.NewReader("y\n")), tty: true}); code != 0 {
		t.Fatalf("import exit %d: %s%s", code, out.String(), errb.String())
	}
	for _, want := range []string{"Backup first:", "imported in", "Undo (while stopped):  rm "} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, out.String())
		}
	}
	st, err := store.Open(db)
	if err != nil {
		t.Fatal(err)
	}
	if a, _ := st.AccountByNick("alice"); a == nil {
		t.Error("alice didn't come in")
	}
	st.Close()

	// Again: now every name is there already, BeeIRCd's stays; the undo
	// line points at the copy made before.
	out.Reset()
	if code := importAnope([]string{"-config", conf, "-yes", sampleDB}, &out, &errb, importRun{}); code != 0 {
		t.Fatalf("second import exit %d: %s", code, out.String())
	}
	if !strings.Contains(out.String(), "Already in BeeIRCd") || !strings.Contains(out.String(), "services.db.before-anope-") {
		t.Errorf("second import:\n%s", out.String())
	}
}

func TestImportAnopeRefusals(t *testing.T) {
	conf, _ := importConf(t)
	var out, errb bytes.Buffer
	if code := importAnope([]string{"-config", conf}, &out, &errb, importRun{}); code != 2 {
		t.Errorf("no file: exit %d", code)
	}
	bad := filepath.Join(t.TempDir(), "bad.db")
	os.WriteFile(bad, []byte("not anope\n"), 0o600)
	out.Reset()
	if code := importAnope([]string{"-config", conf, bad}, &out, &errb, importRun{}); code != 1 || !strings.Contains(out.String(), "not an Anope 2.0 database line") {
		t.Errorf("bad file: exit %d\n%s", code, out.String())
	}
}
