package control

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestPruneKeepsNewestDated(t *testing.T) {
	dir := t.TempDir()
	t0 := time.Date(2026, 11, 1, 4, 0, 0, 0, time.UTC)
	for i := 0; i < 9; i++ {
		os.WriteFile(filepath.Join(dir, BackupName(t0.AddDate(0, 0, i))), []byte("x"), 0o600)
	}
	os.WriteFile(filepath.Join(dir, "my-own.tar.gz"), []byte("x"), 0o600)
	Prune(dir, 7)
	b := Backups(dir)
	if len(b) != 7 || filepath.Base(b[0]) != BackupName(t0.AddDate(0, 0, 8)) || filepath.Base(b[6]) != BackupName(t0.AddDate(0, 0, 2)) {
		t.Fatalf("kept %v", b)
	}
	if _, err := os.Stat(filepath.Join(dir, "my-own.tar.gz")); err != nil {
		t.Error("a backup with its own name was removed")
	}
}

func TestWriteArchiveSkipsMissing(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "a.txt")
	os.WriteFile(a, []byte("hello"), 0o600)
	n, parts, err := WriteArchive(filepath.Join(dir, "out.tar.gz"), []File{{Name: "a.txt", Path: a, Part: "a"}, {Name: "b", Path: filepath.Join(dir, "none"), Part: "b"}})
	if err != nil || n == 0 || len(parts) != 1 || parts[0] != "a" {
		t.Fatalf("%d %v %v", n, parts, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "out.tar.gz.part")); err == nil {
		t.Error("temporary file left behind")
	}
}

func TestFreeBackupName(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 11, 12, 4, 0, 33, 0, time.UTC)
	first := FreeBackupName(dir, now)
	os.WriteFile(first, []byte("x"), 0o600)
	second := FreeBackupName(dir, now)
	if second == first || filepath.Base(second) != "beeircd-2026-11-12-040033.tar.gz" {
		t.Fatalf("second backup of the minute: %s", second)
	}
	os.WriteFile(second, []byte("x"), 0o600)
	if b := Backups(dir); b[0] != second {
		t.Errorf("newest first: %v", b)
	}
}
