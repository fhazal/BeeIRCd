//go:build unix

package main

import (
	"os"
	"syscall"
)

// installPath is where this program was started from, read once at start.
// Asked later, Linux names the running file wherever it went: after
// `beeircd update` moved it aside, that is beeircd.old, and a restart
// would start the old version again instead of the new one.
var installPath, installPathErr = os.Executable()

// reexecPath is the program a restart in place starts: the one at the
// install path.
func reexecPath() (string, error) { return installPath, installPathErr }

// reexec replaces this process with a fresh copy of the program, keeping
// the same process ID, so systemd and Docker see one long-running server.
func reexec() error {
	exe, err := reexecPath()
	if err != nil {
		return err
	}
	return syscall.Exec(exe, os.Args, os.Environ())
}
