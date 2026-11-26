//go:build unix

package config

import (
	"os"
	"syscall"
	"testing"
)

// The server's config often belongs to its own user: writing it as root
// (beeircd update, the Hive Panel) must not take it away from that user.
func TestReplaceFileKeepsOwner(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("only root can give a file to another user")
	}
	p := writeSample(t)
	if err := os.Chown(p, 1234, 4321); err != nil {
		t.Fatal(err)
	}
	if err := ReplaceFile(p, []byte(sample+"# changed\n")); err != nil {
		t.Fatal(err)
	}
	fi, _ := os.Stat(p)
	if st := fi.Sys().(*syscall.Stat_t); st.Uid != 1234 || st.Gid != 4321 {
		t.Errorf("owner %d:%d, want 1234:4321", st.Uid, st.Gid)
	}
}
