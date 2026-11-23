// Command hovcheck is the bot crowd for scripts/handovertest.sh: it proves
// that people stay connected while the door keeper hands them to a new
// server process (beeircd update, --rollback, /as restart).
//
//	hovcheck run -n 50 [-plain 127.0.0.1:6667] [-tls 127.0.0.1:6697] [-ctl 127.0.0.1:8098]
//	hovcheck ask ready | check?label=update | restart | quit
//	hovcheck status [-config /etc/beeircd/beeircd.conf]
//	hovcheck cert -out /root/zt
//
// run connects the bots and stays; ask tells it what to do and prints the
// answer (exit 1 when a line starts with FAIL). The bots never reconnect:
// a bot that loses its connection stays lost and every later check fails.
// status prints the server's own view (version, pid, gate, hand-over) in
// one line; cert makes a client certificate for tools/livecheck.
package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"flag"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/fhazal/beeircd/internal/config"
	"github.com/fhazal/beeircd/internal/control"
)

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	args := os.Args[2:]
	switch os.Args[1] {
	case "run":
		os.Exit(cmdRun(args))
	case "ask":
		os.Exit(cmdAsk(args))
	case "status":
		os.Exit(cmdStatus(args))
	case "cert":
		os.Exit(cmdCert(args))
	}
	usage()
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: hovcheck run|ask|status|cert ... (see the top of tools/hovcheck/main.go)")
	os.Exit(2)
}

// cmdAsk sends one command to a running `hovcheck run`.
func cmdAsk(args []string) int {
	fs := flag.NewFlagSet("ask", flag.ExitOnError)
	ctl := fs.String("ctl", "127.0.0.1:8098", "where hovcheck run listens")
	fs.Parse(args)
	if fs.NArg() != 1 {
		usage()
	}
	cl := &http.Client{Timeout: 10 * time.Minute}
	resp, err := cl.Get("http://" + *ctl + "/" + strings.TrimPrefix(fs.Arg(0), "/"))
	if err != nil {
		fmt.Println("FAIL hovcheck run doesn't answer:", err)
		return 1
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	fmt.Print(string(body))
	for _, l := range strings.Split(string(body), "\n") {
		if strings.HasPrefix(l, "FAIL") {
			return 1
		}
	}
	return 0
}

// cmdStatus prints the server's status in one line the script can read.
func cmdStatus(args []string) int {
	fs := flag.NewFlagSet("status", flag.ExitOnError)
	path := fs.String("config", "/etc/beeircd/beeircd.conf", "beeircd.conf")
	fs.Parse(args)
	cfg, err := config.Load(*path)
	if err != nil {
		fmt.Println("error=config", err)
		return 1
	}
	resp, err := control.Ask(cfg.Control.Socket, control.Request{Cmd: control.CmdStatus}, 5*time.Second)
	if err != nil || resp.Status == nil {
		fmt.Println("error=not-running", err)
		return 1
	}
	st := resp.Status
	gate := st.Gate
	if gate == "" {
		gate = "none"
	}
	h := st.Handover
	if h == nil {
		h = &control.Handover{Kept: -1, Of: -1}
	}
	fmt.Printf("version=%s pid=%d gate=%s local=%d online=%d kept=%d of=%d problem=%q\n",
		st.Version, st.PID, gate, st.Local, st.Online, h.Kept, h.Of, h.Problem)
	return 0
}

// cmdCert writes a self-signed client certificate and its key.
func cmdCert(args []string) int {
	fs := flag.NewFlagSet("cert", flag.ExitOnError)
	out := fs.String("out", "zt", "file prefix: <out>-cert.pem and <out>-key.pem")
	fs.Parse(args)
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: "hovcheck test client"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(30 * 24 * time.Hour),
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	kder, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if err := os.WriteFile(*out+"-cert.pem", pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if err := os.WriteFile(*out+"-key.pem", pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kder}), 0o600); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
}
