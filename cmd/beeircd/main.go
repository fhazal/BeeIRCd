// Command beeircd is the BeeIRCd server and its admin command.
//
//	beeircd start | stop | restart | status | check | update | backup | version
package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/fhazal/beeircd/internal/config"
	"github.com/fhazal/beeircd/internal/passwd"
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
  backup    save config, database and certificates [to file]
  rehash    read beeircd.conf again, apply what is safe live
  version   show the version
  mkpasswd  read a password on standard input, print its hash
            (for admin_password in beeircd.conf)
  run       run in the foreground (used by the service)
            beeircd run [-config /etc/beeircd/beeircd.conf]
`

// planned lists the commands from SPEC.md section 3.2 that later Stage 1
// steps add, so a user gets a clear answer instead of "unknown command".
var planned = map[string]bool{
	"update": true,
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
	case cmd == "mkpasswd":
		return mkpasswd(os.Stdin, stdout, stderr)
	case cmd == "status":
		return cmdStatus(args[1:], stdout, stderr)
	case cmd == "check":
		return cmdCheck(args[1:], stdout, stderr)
	case cmd == "backup":
		return cmdBackup(args[1:], stdout, stderr)
	case cmd == "rehash":
		return cmdRehash(args[1:], stdout, stderr)
	case cmd == "start" || cmd == "stop" || cmd == "restart":
		return cmdService(cmd, args[1:], stdout, stderr)
	case planned[cmd]:
		fmt.Fprintf(stderr, "beeircd %s isn't built yet in %s.\n", cmd, version.String())
		return 2
	default:
		fmt.Fprintf(stderr, "Unknown command %q.\n\n%s", args[0], usage)
		return 2
	}
}

// mkpasswd hashes the first line of in for beeircd.conf.
func mkpasswd(in io.Reader, stdout, stderr io.Writer) int {
	line, err := bufio.NewReader(in).ReadString('\n')
	if err != nil && line == "" {
		fmt.Fprintln(stderr, "Type the password and press Enter.")
		return 2
	}
	pw := strings.TrimRight(line, "\r\n")
	if len(pw) < 8 {
		fmt.Fprintln(stderr, "Use at least 8 characters.")
		return 2
	}
	h, err := passwd.Hash(pw)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	fmt.Fprintln(stdout, h)
	return 0
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
	startProfile(logger)
	startMemStats(logger)
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
	stopHUP := onHangup(srv.Rehash) // SIGHUP re-reads beeircd.conf
	defer stopHUP()
	err = srv.Serve(ctx)
	if errors.Is(err, server.ErrRestart) {
		stop()
		logger.Printf("restarting")
		if err := reexec(); err != nil {
			logger.Printf("restart failed: %v", err)
			return 3
		}
	}
	if err != nil {
		logger.Print(err)
		return 1
	}
	return 0
}
