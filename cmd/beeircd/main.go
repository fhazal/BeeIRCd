// Command beeircd is the BeeIRCd server and its admin command.
//
//	beeircd start | stop | restart | status | check | update | backup | version
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/fhazal/beeircd/internal/config"
	"github.com/fhazal/beeircd/internal/server"
	"github.com/fhazal/beeircd/internal/version"
)

const defaultConfig = "/etc/beeircd/beeircd.conf"

const usage = `Usage: beeircd <command>

  start     start the server
  stop      stop the server
  restart   check the config, then restart
  status    show what the server is doing
  check     test config, certificate and database
  update    install the newest release (backup first)
  backup    save config, database and certificates
  version   show the version
  run       run in the foreground (used by the service)
            beeircd run [-config /etc/beeircd/beeircd.conf]
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
	case cmd == "run":
		return runServer(args[1:], stderr)
	case planned[cmd]:
		fmt.Fprintf(stderr, "beeircd %s isn't built yet in %s.\n", cmd, version.String())
		return 2
	default:
		fmt.Fprintf(stderr, "Unknown command %q.\n\n%s", args[0], usage)
		return 2
	}
}

// runServer runs the server until SIGINT or SIGTERM.
func runServer(args []string, stderr io.Writer) int {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.SetOutput(stderr)
	path := fs.String("config", defaultConfig, "config file")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	logger := log.New(stderr, "", log.LstdFlags)
	cfg, err := config.Load(*path)
	if err != nil {
		logger.Print(err)
		return 1
	}
	srv, err := server.New(cfg, logger)
	if err != nil {
		logger.Print(err)
		return 1
	}
	logger.Printf("%s starting as %s", version.String(), cfg.Server.Name)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := srv.Serve(ctx); err != nil {
		logger.Print(err)
		return 1
	}
	return 0
}
