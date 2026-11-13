// Command netagree checks that every server of a network sees the same
// network: the user and server counts (LUSERS) and, for every channel,
// its topic, modes and members with their prefixes (LIST, MODE, NAMES).
// It looks a few times, some seconds apart, and reports only what differs
// every time: a nick change still on its way doesn't count, a desync does.
//
//	netagree -addrs 127.0.0.1:27011,127.0.0.1:27012,127.0.0.1:27013
//
// Exit status 0 when they agree, 1 when they don't, 2 when it can't look.
package main

import (
	"bufio"
	"flag"
	"fmt"
	"net"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

var (
	addrs  = flag.String("addrs", "", "the servers' client addresses, comma separated")
	looks  = flag.Int("looks", 3, "how many times to look")
	gap    = flag.Duration("gap", 5*time.Second, "time between looks")
	prefix = flag.String("prefix", "#", "only channels starting with this")
	show   = flag.Int("show", 15, "differences to print at most")
)

type conn struct {
	c net.Conn
	r *bufio.Reader
}

func dial(addr, nick string) (*conn, error) {
	c, err := net.DialTimeout("tcp", addr, 10*time.Second)
	if err != nil {
		return nil, err
	}
	k := &conn{c: c, r: bufio.NewReaderSize(c, 1<<16)}
	k.send("NICK "+nick, "USER agree 0 * :netagree")
	err = k.until("001", func([]string, string) {})
	if err != nil {
		c.Close()
		return nil, fmt.Errorf("%s: no welcome: %v", addr, err)
	}
	return k, nil
}

func (k *conn) send(lines ...string) {
	for _, l := range lines {
		k.c.Write([]byte(l + "\r\n"))
	}
}

// until reads lines until one with numeric end; f sees the others as
// fields before " :" and the trailing text.
func (k *conn) until(end string, f func(fields []string, trail string)) error {
	for {
		k.c.SetReadDeadline(time.Now().Add(60 * time.Second))
		l, err := k.r.ReadString('\n')
		if err != nil {
			return err
		}
		l = strings.TrimRight(l, "\r\n")
		if strings.HasPrefix(l, "PING ") {
			k.send("PONG " + l[5:])
			continue
		}
		if strings.HasPrefix(l, "ERROR ") {
			return fmt.Errorf("%s", l)
		}
		head, trail, _ := strings.Cut(strings.TrimPrefix(l, ":"), " :")
		fields := strings.Fields(head)
		if len(fields) > 1 && fields[1] == end {
			return nil
		}
		if len(fields) > 1 && fields[1] == "433" && end == "001" {
			return fmt.Errorf("nick taken")
		}
		f(fields, trail)
	}
}

var lusersRe = regexp.MustCompile(`There are (\d+) users and (\d+) invisible on (\d+) servers`)

// look is one picture of the network as this server sees it.
func (k *conn) look() (map[string]string, error) {
	v := map[string]string{}
	k.send("LUSERS")
	if err := k.until("266", func(f []string, t string) {
		if len(f) > 1 && f[1] == "251" {
			if m := lusersRe.FindStringSubmatch(t); m != nil {
				a, _ := strconv.Atoi(m[1])
				b, _ := strconv.Atoi(m[2])
				v["users on the network"] = strconv.Itoa(a + b)
				v["servers on the network"] = m[3]
			}
		}
	}); err != nil {
		return nil, err
	}
	var chans []string
	k.send("LIST")
	if err := k.until("323", func(f []string, t string) {
		if len(f) > 4 && f[1] == "322" && strings.HasPrefix(f[3], *prefix) {
			chans = append(chans, f[3])
			v[strings.ToLower(f[3])+" topic"] = t
		}
	}); err != nil {
		return nil, err
	}
	for _, ch := range chans {
		key := strings.ToLower(ch)
		var mode string
		k.send("MODE "+ch, "NAMES "+ch) // MODE's 324 comes before NAMES's 366
		var names []string
		got324 := false
		if err := k.until("366", func(f []string, t string) {
			switch {
			case len(f) > 3 && f[1] == "324" && !got324:
				got324 = true
				mode = strings.TrimSpace(strings.Join(f[4:], " ") + " " + t)
			case len(f) > 1 && f[1] == "353":
				names = append(names, strings.Fields(t)...)
			}
		}); err != nil {
			return nil, err
		}
		sort.Strings(names)
		v[key+" modes"] = mode
		v[key+" members"] = strconv.Itoa(len(names)) + ": " + strings.Join(names, " ")
	}
	return v, nil
}

func main() {
	flag.Parse()
	list := strings.Split(*addrs, ",")
	if len(list) < 2 {
		fmt.Fprintln(os.Stderr, "netagree: give at least two -addrs")
		os.Exit(2)
	}
	cs := make([]*conn, len(list))
	stamp := strconv.FormatInt(time.Now().Unix()%100000, 36)
	for i, a := range list {
		c, err := dial(a, fmt.Sprintf("agree%s%d", stamp, i))
		if err != nil {
			fmt.Fprintln(os.Stderr, "netagree:", err)
			os.Exit(2)
		}
		cs[i] = c
	}
	time.Sleep(2 * time.Second) // our own three users reach every server
	var always map[string]bool  // keys that differed in every look so far
	var last []map[string]string
	for n := 0; n < *looks; n++ {
		if n > 0 {
			time.Sleep(*gap)
		}
		views := make([]map[string]string, len(cs))
		errs := make([]error, len(cs))
		var wg sync.WaitGroup
		for i, c := range cs {
			wg.Add(1)
			go func() { defer wg.Done(); views[i], errs[i] = c.look() }()
		}
		wg.Wait()
		for i, err := range errs {
			if err != nil {
				fmt.Fprintf(os.Stderr, "netagree: %s: %v\n", list[i], err)
				os.Exit(2)
			}
		}
		diff := map[string]bool{}
		keys := map[string]bool{}
		for _, v := range views {
			for k := range v {
				keys[k] = true
			}
		}
		for k := range keys {
			for _, v := range views[1:] {
				if v[k] != views[0][k] {
					diff[k] = true
				}
			}
		}
		if always == nil {
			always = diff
		} else {
			for k := range always {
				if !diff[k] {
					delete(always, k)
				}
			}
		}
		last = views
		fmt.Printf("look %d: %d things checked, %d differ\n", n+1, len(keys), len(diff))
	}
	if len(always) == 0 {
		fmt.Printf("AGREE: %d servers see the same network (%s users, %s servers)\n", len(cs), last[0]["users on the network"], last[0]["servers on the network"])
		os.Exit(0)
	}
	var ks []string
	for k := range always {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	fmt.Printf("DISAGREE: %d things differ in every look\n", len(ks))
	for i, k := range ks {
		if i == *show {
			fmt.Printf("  ... and %d more\n", len(ks)-i)
			break
		}
		fmt.Println("  " + k)
		for j, v := range last {
			val := v[k]
			if len(val) > 300 {
				val = val[:300] + "..."
			}
			fmt.Printf("    %s: %s\n", list[j], val)
		}
	}
	os.Exit(1)
}
