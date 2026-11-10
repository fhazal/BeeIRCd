// Package version holds the BeeIRCd version and build details.
package version

import "fmt"

// These are set at build time with -ldflags "-X ...". The defaults are what a
// plain `go build` or `go test` gets.
var (
	Version = "0.0.0-dev"
	Commit  = "unknown"
)

// Name is the product name as people see it.
const Name = "BeeIRCd"

// String is the one-line version shown by `beeircd version` and in the
// server's welcome lines, for example "BeeIRCd 0.1.0 (8e0aa2f)".
func String() string {
	if Commit == "" || Commit == "unknown" {
		return fmt.Sprintf("%s %s", Name, Version)
	}
	return fmt.Sprintf("%s %s (%s)", Name, Version, Commit)
}
