package server

import (
	"context"
	"crypto/x509"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/fhazal/beeircd/internal/acmecert"
	"github.com/fhazal/beeircd/internal/version"
)

// acmeState is the Let's Encrypt renewal as the loop sees it (7c).
type acmeState struct {
	fails   int    // renewals that failed in a row
	lastErr string // why the last one failed
	retryAt time.Time
	// obtain asks for the certificate; replaceable in tests.
	obtain func(ctx context.Context, cfg acmecert.Config) (*x509.Certificate, error)
	// first is how long after start the first check runs.
	first time.Duration
}

// acmeRetry is how long to wait after a failed renewal: Let's Encrypt
// allows only 5 failed checks per name per hour.
var acmeRetry = []time.Duration{time.Hour, 3 * time.Hour, 12 * time.Hour}

const acmeCheckEvery = 12 * time.Hour

// acmeConfig is what the renewal goroutine needs, copied on the loop.
func (s *Server) acmeConfig() acmecert.Config {
	t := s.cfg.TLS
	return acmecert.Config{
		Directory:      t.ACMEDirectory,
		Email:          t.ACMEEmail,
		Domains:        t.Domains(s.cfg.Server.Name),
		CertFile:       t.Cert,
		KeyFile:        t.Key,
		AccountKeyFile: t.AccountKey(),
		HTTPAddr:       t.ACMEHTTP,
		UserAgent:      "BeeIRCd/" + version.Version,
	}
}

// acmeLoop checks twice a day whether the certificate needs renewing and
// asks Let's Encrypt when it does. A failure keeps the current certificate
// and tells opers; the next try waits per acmeRetry.
func (s *Server) acmeLoop(cfg acmecert.Config) {
	defer s.wg.Done()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		<-s.done
		cancel()
	}()
	obtain := s.acme.obtain
	if obtain == nil {
		obtain = acmecert.Obtain
	}
	wait := s.acme.first
	fails := 0
	for {
		select {
		case <-s.done:
			return
		case <-time.After(wait):
		}
		due, why := acmecert.Due(s.certs.Leaf(), cfg.Domains, time.Now())
		if !due {
			wait = acmeCheckEvery
			continue
		}
		names := strings.Join(cfg.Domains, ", ")
		s.post(func() { s.log.Printf("Let's Encrypt: asking for a certificate for %s (%s)", names, why) })
		octx, done := context.WithTimeout(ctx, 5*time.Minute)
		leaf, err := obtain(octx, cfg)
		done()
		if ctx.Err() != nil {
			return
		}
		if err == nil {
			err = s.certs.Reload()
		}
		if err != nil {
			fails++
			wait = acmeRetry[min(fails, len(acmeRetry))-1]
			n, msg, retry := fails, err.Error(), time.Now().Add(wait)
			s.post(func() {
				s.acme.fails, s.acme.lastErr, s.acme.retryAt = n, msg, retry
				keep := "the server keeps its current certificate"
				if l := s.certs.Leaf(); l != nil {
					keep += ", valid until " + l.NotAfter.Format("2 Jan 2006")
				}
				s.snotice("Let's Encrypt failed for %s: %s; %s, next try in %s", names, msg, keep, fmtWait(wait))
			})
			continue
		}
		fails, wait = 0, acmeCheckEvery
		s.post(func() {
			s.acme.fails, s.acme.lastErr, s.acme.retryAt = 0, "", time.Time{}
			s.certErr = ""
			s.snotice("Let's Encrypt: new certificate for %s, valid until %s", names, leaf.NotAfter.Format("2 Jan 2006"))
		})
	}
}

func fmtWait(d time.Duration) string {
	if d >= time.Hour {
		h := int(d / time.Hour)
		if h == 1 {
			return "1 hour"
		}
		return fmt.Sprintf("%d hours", h)
	}
	return d.Round(time.Second).String()
}

// realCert says whether the certificate in use is from a CA (not
// self-signed, and not the stand-in used until Let's Encrypt answers).
func (s *Server) realCert() bool {
	if s.certs == nil {
		return false
	}
	l := s.certs.Leaf()
	return l != nil && !acmecert.SelfSigned(l)
}

// stsValue is the IRCv3 sts value for c, or "" when not offered: only with
// a real certificate, and never to clients that can't read cap values.
// Plain connections are sent to the TLS port; TLS ones get the duration.
func (s *Server) stsValue(c *Client, v302 bool) string {
	if !v302 || !s.cfg.STSOn() || !s.realCert() {
		return ""
	}
	const duration = "duration=2592000" // 30 days
	if c.tls {
		return duration
	}
	port := s.stsPort()
	if port == 0 {
		return ""
	}
	return "port=" + strconv.Itoa(port) + "," + duration
}

// stsPort is [tls] sts_port, or the first TLS port as opened.
func (s *Server) stsPort() int {
	if s.cfg.TLS.STSPort != 0 {
		return s.cfg.TLS.STSPort
	}
	s.lmu.Lock()
	defer s.lmu.Unlock()
	for _, l := range s.listeners {
		if a, ok := l.Addr().(*net.TCPAddr); ok && l.tls {
			return a.Port
		}
	}
	return 0
}
