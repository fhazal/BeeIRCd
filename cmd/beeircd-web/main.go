// Command beeircd-web is the Hive Panel, BeeIRCd's web admin. It reads the
// [web] and [control] parts of beeircd.conf, answers only on this machine
// and talks to the running server through its control socket. Opers sign
// in with their oper nick and oper password.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/fhazal/beeircd/internal/config"
	"github.com/fhazal/beeircd/internal/control"
	"github.com/fhazal/beeircd/internal/hivepanel"
	"github.com/fhazal/beeircd/internal/version"
)

func main() {
	path := flag.String("config", "/etc/beeircd/beeircd.conf", "config file")
	listen := flag.String("listen", "", "address to answer on (default: [web] listen, "+config.DefaultWebListen+")")
	socket := flag.String("socket", "", "control socket (default: [control] socket)")
	showVersion := flag.Bool("version", false, "print the version")
	flag.Parse()
	if *showVersion {
		fmt.Println("beeircd-web " + version.Version)
		return
	}
	log.SetFlags(0)
	cfg, err := config.Load(*path)
	if err != nil {
		log.Fatal(err)
	}
	if *listen != "" {
		cfg.Web.Listen = *listen
		if err := cfg.Check(); err != nil {
			log.Fatal(err)
		}
	}
	sock := cfg.Control.Socket
	if *socket != "" {
		sock = *socket
	}
	if sock == "" {
		log.Fatal("no control socket: set [control] socket in " + *path)
	}
	panel := hivepanel.New(hivepanel.Options{
		Ask:          func(r control.Request, t time.Duration) (*control.Response, error) { return control.Ask(sock, r, t) },
		TrustedProxy: cfg.Web.TrustedProxy,
		Idle:         cfg.Web.SessionIdle.Duration,
	})
	ln, err := net.Listen("tcp", cfg.Web.Listen)
	if err != nil {
		log.Fatal(err)
	}
	srv := &http.Server{Handler: panel, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 2 * time.Minute, MaxHeaderBytes: 32 << 10}
	log.Printf("Hive Panel %s on http://%s (server: %s)", version.Version, ln.Addr(), sock)
	go func() {
		ch := make(chan os.Signal, 1)
		signal.Notify(ch, os.Interrupt, syscall.SIGTERM)
		<-ch
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		srv.Shutdown(ctx)
	}()
	if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}
