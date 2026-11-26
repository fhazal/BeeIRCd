//go:build unix

package config

import (
	"os"
	"syscall"
)

// keepOwner gives f the owner and group of the file it replaces. Only a
// process allowed to (root, or the owner keeping its own group) can; the
// file keeps this process's owner otherwise.
func keepOwner(f *os.File, info os.FileInfo) {
	if st, ok := info.Sys().(*syscall.Stat_t); ok {
		f.Chown(int(st.Uid), int(st.Gid))
	}
}
