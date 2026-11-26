//go:build linux

package main

import (
	"os"
	"testing"
)

// `beeircd update` moves the running program aside to beeircd.old and puts
// the new one at the install path. A restart in place (-no-gate, no
// systemd) must start the one at the install path: Linux names the moved
// file when asked after the move.
func TestReexecStartsInstallPathAfterMove(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	moved := exe + ".old"
	if err := os.Rename(exe, moved); err != nil {
		t.Skipf("can't move the test program: %v", err)
	}
	defer os.Rename(moved, exe)
	if now, _ := os.Executable(); now != moved {
		t.Skipf("this system names %q after the move, not the moved file", now)
	}
	got, err := reexecPath()
	if err != nil {
		t.Fatal(err)
	}
	if got != exe {
		t.Errorf("restart would start %q, want the install path %q", got, exe)
	}
}
