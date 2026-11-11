package control

import (
	"archive/tar"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// File is one file going into a backup: Name inside the archive, Path on
// disk. A Path that doesn't exist is skipped.
type File struct {
	Name, Path, Part string
}

const backupPrefix, backupSuffix = "beeircd-", ".tar.gz"

// BackupName is the dated name of a backup made at t.
func BackupName(t time.Time) string {
	return backupPrefix + t.Format("2006-01-02-1504") + backupSuffix
}

// FreeBackupName is BackupName in dir, with the seconds added when a
// backup of the same minute is there already.
func FreeBackupName(dir string, t time.Time) string {
	p := filepath.Join(dir, BackupName(t))
	if _, err := os.Stat(p); err == nil {
		p = filepath.Join(dir, backupPrefix+t.Format("2006-01-02-150405")+backupSuffix)
	}
	return p
}

// WriteArchive writes files into a new .tar.gz at dst (mode 0600, written
// next to it first so a failed backup never leaves half a file). It
// returns the size and the parts that went in.
func WriteArchive(dst string, files []File) (int64, []string, error) {
	tmp := dst + ".part"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return 0, nil, err
	}
	fail := func(err error) (int64, []string, error) {
		f.Close()
		os.Remove(tmp)
		return 0, nil, err
	}
	zw := gzip.NewWriter(f)
	tw := tar.NewWriter(zw)
	var parts []string
	seen := map[string]bool{}
	for _, fl := range files {
		src, err := os.Open(fl.Path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return fail(err)
		}
		fi, err := src.Stat()
		if err != nil {
			src.Close()
			return fail(err)
		}
		hdr := &tar.Header{Name: fl.Name, Mode: 0o600, Size: fi.Size(), ModTime: fi.ModTime()}
		if err := tw.WriteHeader(hdr); err != nil {
			src.Close()
			return fail(err)
		}
		_, err = io.Copy(tw, src)
		src.Close()
		if err != nil {
			return fail(err)
		}
		if fl.Part != "" && !seen[fl.Part] {
			seen[fl.Part] = true
			parts = append(parts, fl.Part)
		}
	}
	if err := tw.Close(); err != nil {
		return fail(err)
	}
	if err := zw.Close(); err != nil {
		return fail(err)
	}
	if err := f.Sync(); err != nil {
		return fail(err)
	}
	fi, _ := f.Stat()
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return 0, nil, err
	}
	if err := os.Rename(tmp, dst); err != nil {
		os.Remove(tmp)
		return 0, nil, err
	}
	return fi.Size(), parts, nil
}

// Backups lists the dated backups in dir, newest first.
func Backups(dir string) []string {
	entries, _ := os.ReadDir(dir)
	var out []string
	for _, e := range entries {
		n := e.Name()
		if !e.IsDir() && strings.HasPrefix(n, backupPrefix) && strings.HasSuffix(n, backupSuffix) {
			out = append(out, filepath.Join(dir, n))
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(out)))
	return out
}

// Prune removes dated backups beyond the newest keep. Files with other
// names (backups saved under a name of your own) are never touched.
func Prune(dir string, keep int) {
	b := Backups(dir)
	if len(b) <= keep {
		return
	}
	for _, old := range b[keep:] {
		os.Remove(old)
	}
}

// LastBackup is when the newest dated backup in dir was written.
func LastBackup(dir string) (time.Time, bool) {
	b := Backups(dir)
	if len(b) == 0 {
		return time.Time{}, false
	}
	fi, err := os.Stat(b[0])
	if err != nil {
		return time.Time{}, false
	}
	return fi.ModTime(), true
}
