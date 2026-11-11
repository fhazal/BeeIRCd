// Command installcheck tests a fresh `beeircd install` from inside the
// machine: both ports answer, a nick registers with NickServ, and the
// admin can /oper. Used by scripts/cleanmachine.sh.
//
//	installcheck -plain 127.0.0.1:6667 -tls 127.0.0.1:6697 -admin Bee -pass <oper password>
package main

import (
	"bufio"
	"crypto/tls"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"time"
)

func main() {
	plain := flag.String("plain", "", "plain host:port (empty = none)")
	tlsAddr := flag.String("tls", "127.0.0.1:6697", "TLS host:port")
	admin := flag.String("admin", "", "admin nick")
	pass := flag.String("pass", "", "admin oper password")
	skipReg := flag.Bool("again", false, "the nick is registered already (after a restart): IDENTIFY instead")
	serve := flag.String("serve", "", "serve this folder over HTTP on -addr instead (a release folder for the update test)")
	addr := flag.String("addr", "127.0.0.1:8099", "address for -serve")
	watch := flag.String("watch", "", "stay connected to this plain host:port and print the server's goodbye (ERROR line)")
	flag.Parse()
	if *watch != "" {
		c, err := dial(*watch, false)
		if err == nil {
			err = c.register("watcher")
		}
		if err != nil {
			fmt.Println("watch:", err)
			os.Exit(1)
		}
		for {
			c.SetReadDeadline(time.Now().Add(10 * time.Minute))
			l, err := c.r.ReadString('\n')
			if err != nil {
				return
			}
			if strings.HasPrefix(l, "PING ") {
				c.send("PONG " + strings.TrimPrefix(strings.TrimSpace(l), "PING "))
			}
			if strings.HasPrefix(l, "ERROR") {
				fmt.Print(l)
			}
		}
	}
	if *serve != "" {
		fmt.Println(http.ListenAndServe(*addr, http.FileServer(http.Dir(*serve))))
		os.Exit(1)
	}
	fails := 0
	check := func(name string, err error) {
		if err != nil {
			fails++
			fmt.Printf("FAIL %s: %v\n", name, err)
			return
		}
		fmt.Printf("OK   %s\n", name)
	}
	if *plain != "" {
		c, err := dial(*plain, false)
		if err == nil {
			err = c.register("plain1")
			c.Close()
		}
		check("plain port "+*plain+" registers", err)
	}
	// The admin's first time, as the installer explains it: register the
	// nick with NickServ (30 s online first, as in Anope), then /oper.
	a, err := dial(*tlsAddr, true)
	if err == nil {
		err = a.register(*admin)
	}
	check("TLS port "+*tlsAddr+" registers", err)
	if err != nil {
		os.Exit(1)
	}
	defer a.Close()
	if !*skipReg {
		time.Sleep(31 * time.Second)
		a.send("PRIVMSG NickServ :REGISTER " + *admin + "-ns-password " + strings.ToLower(*admin) + "@example.org")
		_, err = a.wait("registered", "NickServ REGISTER")
		check("NickServ REGISTER "+*admin, err)
	} else {
		a.send("PRIVMSG NickServ :IDENTIFY " + *admin + "-ns-password")
		_, err = a.wait("recognized", "NickServ IDENTIFY")
		check("NickServ IDENTIFY "+*admin, err)
	}
	a.send("OPER " + *admin + " wrong-password")
	_, err = a.wait(" 464 ", "wrong /oper refused")
	check("wrong /oper refused (464)", err)
	a.send("OPER " + *admin + " " + *pass)
	_, err = a.wait(" 381 ", "/oper")
	check("admin /oper "+*admin+" (381)", err)
	if fails > 0 {
		fmt.Printf("%d check(s) failed\n", fails)
		os.Exit(1)
	}
	fmt.Println("all checks passed")
}

type conn struct {
	net.Conn
	r *bufio.Reader
}

func dial(addr string, useTLS bool) (*conn, error) {
	var nc net.Conn
	var err error
	if useTLS {
		// A fresh install is often self-signed: only the connection matters here.
		nc, err = tls.DialWithDialer(&net.Dialer{Timeout: 5 * time.Second}, "tcp", addr, &tls.Config{InsecureSkipVerify: true})
	} else {
		nc, err = net.DialTimeout("tcp", addr, 5*time.Second)
	}
	if err != nil {
		return nil, err
	}
	return &conn{Conn: nc, r: bufio.NewReader(nc)}, nil
}

func (c *conn) send(line string) { fmt.Fprintf(c, "%s\r\n", line) }

func (c *conn) wait(want, what string) (string, error) {
	c.SetReadDeadline(time.Now().Add(10 * time.Second))
	for {
		l, err := c.r.ReadString('\n')
		if err != nil {
			return "", fmt.Errorf("%s: %v", what, err)
		}
		if strings.HasPrefix(l, "PING ") {
			c.send("PONG " + strings.TrimPrefix(strings.TrimSpace(l), "PING "))
			continue
		}
		if strings.Contains(l, want) {
			return l, nil
		}
		if strings.Contains(l, " 464 ") && want != " 464 " || strings.Contains(l, " 491 ") {
			return "", fmt.Errorf("%s: %s", what, strings.TrimSpace(l))
		}
	}
}

func (c *conn) register(nick string) error {
	c.send("NICK " + nick)
	c.send("USER " + nick + " 0 * :installcheck")
	_, err := c.wait(" 001 ", "registration")
	return err
}
