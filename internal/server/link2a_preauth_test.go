package server

import (
	"crypto/tls"
	"io"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/fhazal/beeircd/internal/link"
)

// strangerTLS opens a TLS connection to n's link port with a certificate
// no link block knows (anyone on the internet can do this much).
func strangerTLS(t *testing.T, n *linkNode) *tls.Conn {
	t.Helper()
	dir := t.TempDir()
	c, k := filepath.Join(dir, "c.pem"), filepath.Join(dir, "k.pem")
	if _, _, err := link.LoadOrCreate(c, k, "stranger.test.local"); err != nil {
		t.Fatal(err)
	}
	pair, err := tls.LoadX509KeyPair(c, k)
	if err != nil {
		t.Fatal(err)
	}
	tc, err := tls.Dial("tcp", "127.0.0.1:"+strconv.Itoa(n.port), &tls.Config{Certificates: []tls.Certificate{pair}, InsecureSkipVerify: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { tc.Close() })
	return tc
}

// TestLinkPreAuthLimits: before the handshake is done a stranger on the
// link port may send only a handful of lines, and only a few strangers
// may wait in a handshake at once; else one address could keep the
// server's loop busy with junk for the whole handshake time.
func TestLinkPreAuthLimits(t *testing.T) {
	hub := newLinkNode(t, "hub.test.local", "0HB", "hub")
	hub.start(t)

	tc := strangerTLS(t, hub)
	go tc.Write([]byte(strings.Repeat("PING :x\r\n", 2000)))
	tc.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := io.Copy(io.Discard, tc); err != nil {
		t.Fatalf("a stranger sending 2000 lines before the handshake was not dropped: %v", err)
	}
	waitLink(t, "the reason in the log", func() bool { return hub.logHas("before the handshake") })

	for i := 0; i < linkMaxPending+10; i++ {
		c := strangerTLS(t, hub)
		c.Write([]byte("PING :x\r\n")) // TLS done; the server has the link now
	}
	time.Sleep(300 * time.Millisecond)
	waiting := 0
	hub.s.onLoop(func() {
		for l := range hub.s.links.conns {
			if !l.up && !l.out {
				waiting++
			}
		}
	})
	if waiting > linkMaxPending {
		t.Errorf("%d strangers wait in a handshake (at most %d)", waiting, linkMaxPending)
	}
}
