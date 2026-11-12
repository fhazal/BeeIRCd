// Command livecheck tests a running BeeIRCd over the network, the way a
// person would: it connects test clients, sends real IRC lines and checks
// the replies. It replaces the Python live scripts (live157/158/159a).
//
// It is non-destructive: it only uses its own zt* nicks and channels,
// never opers up, and drops what it registered at the end. Point it at a
// test server only.
//
//	go run ./tools/livecheck -addr 192.168.50.76:26667 -tls 192.168.50.76:26697 \
//	    -cert zt-cert.pem -key zt-key.pem [-suites core,opers,modes]
package main

import (
	"flag"
	"fmt"
	"os"
	"strings"
	"time"
)

type liveCheck struct {
	addr, tlsAddr string
	cert, key     string
	pw            string
	admin         string // an oper account to try one wrong /oper password on ("" = skip)
	passed, total int
	failed        []string
	all           []*client
}

func (lc *liveCheck) check(name string, ok bool) {
	lc.total++
	if ok {
		lc.passed++
		fmt.Println("OK   " + name)
		return
	}
	lc.failed = append(lc.failed, name)
	fmt.Println("FAIL " + name)
}

// ok is check for a wait() result.
func (lc *liveCheck) ok(name, line string) { lc.check(name, line != "") }

func (lc *liveCheck) fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "livecheck: "+format+"\n", args...)
	os.Exit(2)
}

func main() {
	lc := &liveCheck{}
	flag.StringVar(&lc.addr, "addr", "", "plain host:port of the test server (required)")
	flag.StringVar(&lc.tlsAddr, "tls", "", "TLS host:port (for the CERT, SASL EXTERNAL and +z checks)")
	flag.StringVar(&lc.cert, "cert", "zt-cert.pem", "test client certificate")
	flag.StringVar(&lc.key, "key", "zt-key.pem", "its key")
	flag.StringVar(&lc.pw, "pw", "zt9-pass-word", "password for the zt* test accounts")
	flag.StringVar(&lc.admin, "admin", "", "oper account for one wrong-password /oper check (counts toward its lockout)")
	suites := flag.String("suites", "core,opers,modes", "which checks: core (1.5.x services and IRCv3), opers (1.5.8), modes (1.5.9a), net (2f, needs -net)")
	netList := flag.String("net", "", "net suite: hub=host:port,leaf1=host:port,leaf2=host:port (server names; the hub runs services)")
	cut := flag.String("cut", "", "net suite: shell command that cuts leaf2's link")
	heal := flag.String("heal", "", "net suite: shell command that gives it back")
	flag.Parse()
	var nc netCheck
	for _, kv := range strings.Split(*netList, ",") {
		if name, addr, ok := strings.Cut(strings.TrimSpace(kv), "="); ok {
			nc.names, nc.addrs = append(nc.names, name), append(nc.addrs, addr)
		}
	}
	nc.cut, nc.heal = *cut, *heal
	if lc.addr == "" && len(nc.addrs) > 0 {
		lc.addr = nc.addrs[0]
	}
	if lc.addr == "" {
		flag.Usage()
		os.Exit(2)
	}
	for _, s := range strings.Split(*suites, ",") {
		switch strings.TrimSpace(s) {
		case "core":
			lc.core()
		case "opers":
			lc.opers()
		case "modes":
			lc.modes()
		case "net":
			if nc.cut == "" || nc.heal == "" {
				lc.fatal("the net suite needs -net, -cut and -heal")
			}
			lc.network(nc)
		default:
			lc.fatal("unknown suite %q", s)
		}
	}
	for _, c := range lc.all {
		c.send("QUIT :done")
	}
	time.Sleep(500 * time.Millisecond)
	fmt.Printf("%d/%d OK\n", lc.passed, lc.total)
	if len(lc.failed) > 0 {
		os.Exit(1)
	}
}
