package hivepanel

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"time"

	"github.com/fhazal/beeircd/internal/config"
	"github.com/fhazal/beeircd/internal/control"
	"github.com/fhazal/beeircd/internal/version"
)

// Run serves the Hive Panel for the server whose beeircd.conf is cfgPath
// until ctx ends. listen and socket override [web] listen and [control]
// socket when not empty. logf gets the start line.
func Run(ctx context.Context, cfgPath, listen, socket string, logf func(format string, a ...any)) error {
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return err
	}
	if listen != "" {
		cfg.Web.Listen = listen
		if err := cfg.Check(); err != nil {
			return err
		}
	}
	sock := cfg.Control.Socket
	if socket != "" {
		sock = socket
	}
	if sock == "" {
		return fmt.Errorf("no control socket: set [control] socket in %s", cfgPath)
	}
	panel := New(Options{
		Ask:          func(r control.Request, t time.Duration) (*control.Response, error) { return control.Ask(sock, r, t) },
		TrustedProxy: cfg.Web.TrustedProxy,
		Idle:         cfg.Web.SessionIdle.Duration,
		ChatURL:      cfg.Web.ChatURL,
	})
	ln, err := net.Listen("tcp", cfg.Web.Listen)
	if err != nil {
		return err
	}
	srv := &http.Server{Handler: panel, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 2 * time.Minute, MaxHeaderBytes: 32 << 10}
	logf("Hive Panel %s on http://%s (server: %s)", version.Version, ln.Addr(), sock)
	go func() {
		<-ctx.Done()
		c, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		srv.Shutdown(c)
	}()
	if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
