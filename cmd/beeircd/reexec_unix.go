//go:build unix

package main

import (
	"os"
	"syscall"
)

// reexec replaces this process with a fresh copy of the program, keeping
// the same process ID, so systemd and Docker see one long-running server.
func reexec() error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	return syscall.Exec(exe, os.Args, os.Environ())
}
