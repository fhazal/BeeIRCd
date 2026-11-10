// Command beeircd is the BeeIRCD server and its admin command.
//
//	beeircd start | stop | restart | status | check | update | backup | version
package main

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/fhazal/beeircd/internal/version"
)

const usage = `Usage: beeircd <command>

  start     start the server
  stop      stop the server
  restart   check the config, then restart
  status    show what the server is doing
  check     test config, certificate and database
  update    install the newest release (backup first)
  backup    save config, database and certificates
  version   show the version
`

// planned lists the commands from SPEC.md section 3.2 that later Stage 1
// steps add, so a user gets a clear answer instead of "unknown command".
var planned = map[string]bool{
	"start": true, "stop": true, "restart": true, "status": true,
	"check": true, "update": true, "backup": true,
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run carries out one command and returns the exit code. It writes only to
// the given writers so tests can drive it.
func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stdout, usage)
		return 0
	}
	cmd := strings.ToLower(args[0])
	switch {
	case cmd == "version" || cmd == "--version" || cmd == "-v":
		fmt.Fprintln(stdout, version.String())
		return 0
	case cmd == "help" || cmd == "--help" || cmd == "-h":
		fmt.Fprint(stdout, usage)
		return 0
	case planned[cmd]:
		fmt.Fprintf(stderr, "beeircd %s isn't built yet in %s.\n", cmd, version.String())
		return 2
	default:
		fmt.Fprintf(stderr, "Unknown command %q.\n\n%s", args[0], usage)
		return 2
	}
}
