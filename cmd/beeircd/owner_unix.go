//go:build unix

package main

import (
	"os"
	"syscall"
)

// copyOwner gives file the owner and group fi has (the config belongs to
// the beeircd user; an update runs as root).
func copyOwner(fi os.FileInfo, file string) {
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		os.Chown(file, int(st.Uid), int(st.Gid))
	}
}
