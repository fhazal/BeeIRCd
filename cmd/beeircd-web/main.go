// Command beeircd-web is the Hive Panel on its own, for development. The
// release runs the same panel as `beeircd web`.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/fhazal/beeircd/internal/hivepanel"
	"github.com/fhazal/beeircd/internal/version"
)

func main() {
	path := flag.String("config", "/etc/beeircd/beeircd.conf", "config file")
	listen := flag.String("listen", "", "address to answer on (default: [web] listen)")
	socket := flag.String("socket", "", "control socket (default: [control] socket)")
	showVersion := flag.Bool("version", false, "print the version")
	flag.Parse()
	if *showVersion {
		fmt.Println("beeircd-web " + version.Version)
		return
	}
	log.SetFlags(0)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := hivepanel.Run(ctx, *path, *listen, *socket, log.Printf); err != nil {
		log.Fatal(err)
	}
}
