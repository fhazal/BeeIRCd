package main

import (
	"os"
	"testing"
)

// The hand-over test (handover_linux_test.go) needs real processes: it
// starts this test program again as `beeircd run`, and the gate in it
// starts it once more as the server process behind the gate.
const envAsBeeircd = "BEEIRCD_TEST_RUN_AS_BEEIRCD"

func TestMain(m *testing.M) {
	if os.Getenv(envAsBeeircd) != "" {
		os.Exit(runServer(os.Args[1:], os.Stderr))
	}
	os.Exit(m.Run())
}
