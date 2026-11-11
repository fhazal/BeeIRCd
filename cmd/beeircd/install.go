package main

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"os/user"
	"path"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"text/template"
	"time"

	"golang.org/x/term"

	"github.com/fhazal/beeircd/internal/acmecert"
	"github.com/fhazal/beeircd/internal/config"
	"github.com/fhazal/beeircd/internal/control"
	"github.com/fhazal/beeircd/internal/irc"
	"github.com/fhazal/beeircd/internal/passwd"
	"github.com/fhazal/beeircd/internal/services"
	"github.com/fhazal/beeircd/internal/tlsutil"
	"github.com/fhazal/beeircd/internal/version"
)

// supportedOS are the systems the installer is tested on (PLAN-7 D7).
var supportedOS = map[string]bool{"debian 12": true, "debian 13": true, "ubuntu 22.04": true, "ubuntu 24.04": true}

var errStopped = errors.New("stopped, nothing was changed")

// installer asks the questions and writes the files. root is "" on a
// real machine; tests use a temporary folder and a fake command runner.
type installer struct {
	root       string
	in         *bufio.Reader
	out        io.Writer
	c          *checker
	tty        bool
	readSecret func() (string, error)
	run        func(name string, args ...string) (string, error)
	env        func(string) string
	exe        string // the program to copy
	now        time.Time
	uid, gid   int // the beeircd user, -1 until made
	portFree   func(port int) bool
}

// plan is everything the questions decide; the templates read it.
type plan struct {
	Date, Name, Network, Description, SID, Prefixes string
	PlainPort, TLSPort, LinkPort                    int
	Role                                            string // standalone, hub, leaf
	Cert                                            string // "acme", "self" or "own"
	ACMEEmail, CertFile, KeyFile                    string
	OwnCert, OwnKey, LELive                         string // own files as given; LELive = certbot folder
	MOTDFile, CloakSecret, Database                 string
	Admin, AdminHash                                string
	AdminHosts                                      []string
	HistorySecret, Socket, BackupDir, TimeZone      string
}

func cmdInstall(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("install", flag.ContinueOnError)
	fs.SetOutput(stderr)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	in := realInstaller(stdout)
	if err := in.preflight(); err != nil {
		in.c.bad(err.Error())
		return 1
	}
	p, err := in.ask()
	if err != nil {
		fmt.Fprintln(stdout)
		in.c.bad(err.Error())
		return 1
	}
	if err := in.apply(p); err != nil {
		in.c.bad(err.Error())
		return 1
	}
	return in.finish(p)
}

func realInstaller(stdout io.Writer) *installer {
	exe, _ := os.Executable()
	tty := term.IsTerminal(int(os.Stdin.Fd()))
	in := &installer{
		in: bufio.NewReader(os.Stdin), out: stdout, c: &checker{w: stdout, p: colourFor(stdout)},
		tty: tty, env: os.Getenv, exe: exe, now: time.Now(), uid: -1, gid: -1,
		run: func(name string, args ...string) (string, error) {
			out, err := exec.Command(name, args...).CombinedOutput()
			return strings.TrimSpace(string(out)), err
		},
		portFree: func(port int) bool {
			ln, err := net.Listen("tcp", ":"+strconv.Itoa(port))
			if err != nil {
				return false
			}
			ln.Close()
			return true
		},
	}
	in.readSecret = func() (string, error) {
		if !tty {
			return in.line()
		}
		b, err := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Fprintln(stdout)
		return string(b), err
	}
	return in
}

func (in *installer) path(p string) string { return filepath.Join(in.root, p) }

func (in *installer) line() (string, error) {
	s, err := in.in.ReadString('\n')
	if err != nil && s == "" {
		return "", errStopped
	}
	return strings.TrimSpace(s), nil
}

// preflight refuses machines the installer can't handle.
func (in *installer) preflight() error {
	switch {
	case runtime.GOOS != "linux":
		return errors.New("beeircd install is for Linux; elsewhere run `beeircd run -config <file>` under your own service manager")
	case os.Geteuid() != 0:
		return errors.New("run the installer as root (sudo beeircd install)")
	}
	if _, err := os.Stat("/run/systemd/system"); err != nil {
		return errors.New("this machine doesn't run systemd; install by hand: see INSTALL.md \"Without systemd\"")
	}
	if _, err := os.Stat(instConf); err == nil {
		return errors.New("BeeIRCd is already installed (" + instConf + "). Use `beeircd update`, or `beeircd uninstall` first")
	}
	return nil
}

// osName is "debian 13" and the like, from /etc/os-release.
func (in *installer) osName() string {
	b, err := os.ReadFile(in.path("/etc/os-release"))
	if err != nil {
		return ""
	}
	var id, ver string
	for _, l := range strings.Split(string(b), "\n") {
		k, v, _ := strings.Cut(l, "=")
		v = strings.Trim(v, `"`)
		switch k {
		case "ID":
			id = v
		case "VERSION_ID":
			ver = v
		}
	}
	return strings.TrimSpace(id + " " + ver)
}

// question asks until check accepts the answer (check returns "" for OK).
func (in *installer) question(n int, label, def string, check func(string) string) (string, error) {
	for {
		hint := ""
		if def != "" {
			hint = " [" + def + "]"
		}
		fmt.Fprintf(in.out, "  %d/9  %-22s%s  ", n, label, hint)
		a, err := in.line()
		if err != nil {
			return "", err
		}
		if !in.tty {
			fmt.Fprintln(in.out, a)
		}
		if a == "" {
			a = def
		}
		why := check(a)
		if why == "" {
			return a, nil
		}
		fmt.Fprintln(in.out, "        "+in.c.p.warn("! "+why))
	}
}

func (in *installer) yesNo(q string, def bool) (bool, error) {
	hint := "[Y/n]"
	if !def {
		hint = "[y/N]"
	}
	for {
		fmt.Fprintf(in.out, "  %s %s ", q, hint)
		a, err := in.line()
		if err != nil {
			return false, err
		}
		if !in.tty {
			fmt.Fprintln(in.out, a)
		}
		switch strings.ToLower(a) {
		case "":
			return def, nil
		case "y", "yes":
			return true, nil
		case "n", "no":
			return false, nil
		}
	}
}

// ask runs the eight questions and the summary; nothing is written yet.
func (in *installer) ask() (*plan, error) {
	fmt.Fprintf(in.out, "\n  %s setup\n  Answer each question. Enter keeps the [default]. Ctrl+C stops without changes.\n\n", version.String())
	if osn := in.osName(); !supportedOS[osn] {
		fmt.Fprintln(in.out, "  "+in.c.p.warn("! "+or(osn, "this system")+" isn't one the installer is tested on (Debian 12/13, Ubuntu 22.04/24.04)."))
		if ok, err := in.yesNo("Continue anyway?", false); err != nil || !ok {
			return nil, errStopped
		}
	}
	p := &plan{Date: in.now.Format("2 Jan 2006"), Prefixes: "@+", MOTDFile: instMOTD,
		Database: path.Join(instData, "services.db"), Socket: instSocket,
		BackupDir: path.Join(instData, "backups"), TimeZone: in.timeZone()}
	var err error
	host, _ := os.Hostname()
	if !config.ValidServerName(host) || !strings.Contains(host, ".") {
		host = ""
	}
	if p.Name, err = in.question(1, "Server name", host, func(a string) string {
		if !config.ValidServerName(a) {
			return "a DNS name like irc.example.org"
		}
		return ""
	}); err != nil {
		return nil, err
	}
	if p.Network, err = in.question(2, "Network name", guessNetwork(p.Name), func(a string) string {
		if !config.ValidNetwork(a) {
			return "letters, digits, - _ . (up to 32)"
		}
		return ""
	}); err != nil {
		return nil, err
	}
	p.Description = p.Network + " IRC"
	if p.Admin, err = in.question(3, "Your admin nick", "", func(a string) string {
		switch {
		case a == "":
			return "the nick you will use on IRC"
		case !irc.ValidNick(a, 30):
			return "not a valid IRC nick"
		case services.IsReserved(a):
			return "that nick belongs to the services"
		}
		return ""
	}); err != nil {
		return nil, err
	}
	if p.AdminHash, err = in.password(); err != nil {
		return nil, err
	}
	from := ""
	if ip, _, _ := strings.Cut(in.env("SSH_CONNECTION"), " "); net.ParseIP(ip) != nil {
		from = "*@" + ip
	}
	hosts, err := in.question(5, "Admin may /oper from", from, func(a string) string {
		for _, m := range splitList(a) {
			if strings.ContainsAny(m, "! ") || strings.Count(m, "@") > 1 {
				return "user@host masks or IPs, comma separated (empty = anywhere)"
			}
		}
		return ""
	})
	if err != nil {
		return nil, err
	}
	p.AdminHosts = splitList(hosts)
	if _, err = in.question(6, "Ports", "6667 plain, 6697 TLS", func(a string) string {
		plain, tls, why := parsePorts(a)
		if why != "" {
			return why
		}
		for _, port := range []int{plain, tls} {
			if port != 0 && !in.portFree(port) {
				return fmt.Sprintf("port %d is in use by another program", port)
			}
		}
		p.PlainPort, p.TLSPort = plain, tls
		return ""
	}); err != nil {
		return nil, err
	}
	if err := in.askCert(p); err != nil {
		return nil, err
	}
	style, err := in.question(8, "Channel prefixes", "1", func(a string) string {
		if a != "1" && a != "2" {
			return "1) @+ op and voice   2) ~&@%+ owner, admin, op, halfop, voice"
		}
		return ""
	})
	if err != nil {
		return nil, err
	}
	if style == "2" {
		p.Prefixes = "~&@%+"
	}
	role, err := in.question(9, "This server is", "1", func(a string) string {
		if a != "1" && a != "2" && a != "3" {
			return "1) on its own  2) a hub  3) a leaf"
		}
		return ""
	})
	if err != nil {
		return nil, err
	}
	p.Role = map[string]string{"1": "standalone", "2": "hub", "3": "leaf"}[role]
	if p.Role != "standalone" {
		lp, err := in.question(9, "Link port (TLS only)", "7000", func(a string) string {
			n, err := strconv.Atoi(a)
			switch {
			case err != nil || n < 1 || n > 65535:
				return "a port number"
			case n == p.PlainPort || n == p.TLSPort:
				return "that port is already for clients"
			case !in.portFree(n):
				return fmt.Sprintf("port %d is in use by another program", n)
			}
			return ""
		})
		if err != nil {
			return nil, err
		}
		p.LinkPort, _ = strconv.Atoi(lp)
	}
	p.SID = makeSID(p.Name)
	p.CloakSecret, p.HistorySecret = randomSecret(), randomSecret()
	in.summary(p)
	if ok, err := in.yesNo("Install with these settings?", true); err != nil || !ok {
		return nil, errStopped
	}
	fmt.Fprintln(in.out)
	return p, nil
}

func (in *installer) password() (string, error) {
	for {
		fmt.Fprintf(in.out, "  %d/9  %-22s  ", 4, "Admin password")
		a, err := in.readSecret()
		if err != nil {
			return "", errStopped
		}
		if len(a) < 8 {
			fmt.Fprintln(in.out, "        "+in.c.p.warn("! at least 8 characters (this is your /oper password)"))
			continue
		}
		fmt.Fprintf(in.out, "       %-22s  ", "Again")
		b, err := in.readSecret()
		if err != nil {
			return "", errStopped
		}
		if a != b {
			fmt.Fprintln(in.out, "        "+in.c.p.warn("! the two didn't match"))
			continue
		}
		if !in.tty {
			fmt.Fprintln(in.out, "••••••••")
		}
		return passwd.Hash(a)
	}
}

func (in *installer) askCert(p *plan) error {
	def := "1"
	if acmecert.ValidDomain(p.Name) != "" {
		def = "2" // Let's Encrypt can't check a private name
	}
	for {
		choice, err := in.question(7, "TLS certificate", def, func(a string) string {
			if a != "1" && a != "2" && a != "3" {
				return "1) Let's Encrypt  2) self-signed  3) my own files"
			}
			if a == "1" {
				if why := acmecert.ValidDomain(p.Name); why != "" {
					return p.Name + " " + why + ", so Let's Encrypt can't check it: pick 2 or 3"
				}
				if !in.portFree(80) {
					return "port 80 is in use (a web server?); Let's Encrypt needs it while asking. Pick 2 now and switch later, or free port 80"
				}
			}
			return ""
		})
		if err != nil {
			return err
		}
		p.CertFile, p.KeyFile = path.Join(instTLS, "cert.pem"), path.Join(instTLS, "key.pem")
		switch choice {
		case "1":
			p.Cert = "acme"
			fmt.Fprintf(in.out, "       %-22s  ", "Email for expiry mails")
			email, err := in.line()
			if err != nil {
				return err
			}
			if !in.tty {
				fmt.Fprintln(in.out, email)
			}
			if email != "" && !strings.Contains(email, "@") {
				fmt.Fprintln(in.out, "        "+in.c.p.warn("! not an email address; left out"))
				email = ""
			}
			p.ACMEEmail = email
			return nil
		case "2":
			p.Cert = "self"
			return nil
		}
		p.Cert = "own"
		fmt.Fprintf(in.out, "       %-22s  ", "Certificate file")
		cert, err := in.line()
		if err != nil {
			return err
		}
		fmt.Fprintf(in.out, "       %-22s  ", "Key file")
		key, err := in.line()
		if err != nil {
			return err
		}
		if !in.tty {
			fmt.Fprintf(in.out, "%s\n", key)
		}
		leaf, err := tlsutil.Describe(in.path(cert), in.path(key), p.Name, in.now)
		if err != nil && !errors.Is(err, tlsutil.ErrOtherName) {
			fmt.Fprintln(in.out, "        "+in.c.p.warn("! "+err.Error()))
			continue
		}
		if errors.Is(err, tlsutil.ErrOtherName) {
			fmt.Fprintln(in.out, "        "+in.c.p.warn("! that certificate is for "+strings.Join(leaf.DNSNames, ", ")+", not "+p.Name))
		}
		p.OwnCert, p.OwnKey = cert, key
		if dir := path.Dir(cert); strings.HasPrefix(dir, "/etc/letsencrypt/live/") && path.Dir(key) == dir {
			p.LELive = dir // copied for the beeircd user, renewals through a hook
		} else {
			p.CertFile, p.KeyFile = cert, key
		}
		return nil
	}
}

func (in *installer) summary(p *plan) {
	w := in.out
	ports := fmt.Sprintf("%d TLS", p.TLSPort)
	if p.PlainPort != 0 {
		ports = fmt.Sprintf("%d plain · ", p.PlainPort) + ports
	}
	cert := map[string]string{
		"acme": "Let's Encrypt for " + p.Name + ", renews by itself",
		"self": "self-signed (clients will warn; switch to Let's Encrypt later)",
		"own":  p.OwnCert,
	}[p.Cert]
	if p.LELive != "" {
		cert += " (copied for BeeIRCd, renewals copied by a certbot hook)"
	}
	from := "anywhere"
	if len(p.AdminHosts) > 0 {
		from = strings.Join(p.AdminHosts, ", ")
	}
	fmt.Fprintf(w, "\n  Summary\n")
	fmt.Fprintf(w, "  %-11s %s   on network %s\n", "Server", p.Name, p.Network)
	fmt.Fprintf(w, "  %-11s %s   (all flags; register the nick with NickServ, then /oper, from %s)\n", "Admin", p.Admin, from)
	fmt.Fprintf(w, "  %-11s %s\n", "Ports", ports)
	fmt.Fprintf(w, "  %-11s %s\n", "Prefixes", p.Prefixes)
	fmt.Fprintf(w, "  %-11s %s\n", "Role", p.roleText())
	fmt.Fprintf(w, "  %-11s %s\n", "Certificate", cert)
	fmt.Fprintf(w, "  %-11s daily 04:00 %s, 7 kept\n", "Backup", p.TimeZone)
	fmt.Fprintf(w, "  %-11s /opt/beeircd   config /etc/beeircd   data /var/lib/beeircd   logs journalctl -u beeircd\n\n", "Files")
}

// apply writes everything. Each step prints a ✓ line.
func (in *installer) apply(p *plan) error {
	if err := in.makeUser(); err != nil {
		return err
	}
	if err := in.copyProgram(); err != nil {
		return err
	}
	if err := in.writeConfig(p); err != nil {
		return err
	}
	if err := in.certFiles(p); err != nil {
		return err
	}
	if err := in.writeUnits(p); err != nil {
		return err
	}
	in.firewall(p)
	return nil
}

func (in *installer) makeUser() error {
	if in.root == "" {
		if _, err := user.Lookup(instUser); err != nil {
			if out, err := in.run("useradd", "--system", "--user-group", "--home-dir", instData, "--no-create-home",
				"--shell", "/usr/sbin/nologin", "--comment", "BeeIRCd", instUser); err != nil {
				return fmt.Errorf("useradd: %s", or(out, err.Error()))
			}
			in.c.ok("system user beeircd created")
		} else {
			in.c.ok("system user beeircd already there")
		}
		u, err := user.Lookup(instUser)
		if err != nil {
			return err
		}
		in.uid, _ = strconv.Atoi(u.Uid)
		in.gid, _ = strconv.Atoi(u.Gid)
		return nil
	}
	in.c.ok("system user beeircd created")
	return nil
}

// write puts a file in place with its mode, owned by beeircd when mine.
func (in *installer) write(p string, b []byte, mode os.FileMode, mine bool) error {
	f := in.path(p)
	if err := os.MkdirAll(filepath.Dir(f), 0o755); err != nil {
		return err
	}
	tmp := f + ".new"
	if err := os.WriteFile(tmp, b, mode); err != nil {
		return err
	}
	if err := os.Chmod(tmp, mode); err != nil {
		return err
	}
	if mine {
		in.chown(tmp)
	}
	return os.Rename(tmp, f)
}

func (in *installer) mkdir(p string, mode os.FileMode, mine bool) error {
	f := in.path(p)
	if err := os.MkdirAll(f, mode); err != nil {
		return err
	}
	if err := os.Chmod(f, mode); err != nil {
		return err
	}
	if mine {
		in.chown(f)
	}
	return nil
}

func (in *installer) chown(f string) {
	if in.uid >= 0 {
		os.Chown(f, in.uid, in.gid)
	}
}

func (in *installer) copyProgram() error {
	b, err := os.ReadFile(in.exe)
	if err != nil {
		return fmt.Errorf("read the program: %w", err)
	}
	if err := in.write(instBin, b, 0o755, false); err != nil {
		return err
	}
	link := in.path(instLink)
	os.Remove(link)
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		return err
	}
	if err := os.Symlink(instBin, link); err != nil && in.root == "" {
		return err
	}
	return nil
}

func render(t *template.Template, data any) ([]byte, error) {
	var b bytes.Buffer
	err := t.Execute(&b, data)
	return b.Bytes(), err
}

func (in *installer) writeConfig(p *plan) error {
	for _, d := range []string{instEtc, instTLS, instData} {
		if err := in.mkdir(d, 0o750, true); err != nil {
			return err
		}
	}
	conf, err := render(confTemplate, p)
	if err != nil {
		return err
	}
	if err := in.write(instConf, conf, 0o640, true); err != nil {
		return err
	}
	motd, err := render(template.Must(template.New("motd").Parse(motdText)), p)
	if err != nil {
		return err
	}
	if err := in.write(instMOTD, motd, 0o640, true); err != nil {
		return err
	}
	// The file must read back cleanly; anything else is a bug here.
	if _, err := config.Load(in.path(instConf)); err != nil {
		return fmt.Errorf("the written config doesn't load (please report this): %w", err)
	}
	in.c.ok("program in /opt/beeircd, config written to " + instConf)
	return nil
}

func (in *installer) certFiles(p *plan) error {
	switch {
	case p.Cert == "acme":
		in.c.ok("Let's Encrypt: the certificate is asked for at the first start")
	case p.Cert == "self":
		in.c.ok("self-signed certificate: made at the first start")
	case p.LELive != "":
		for _, f := range []struct {
			from, to string
			mode     os.FileMode
		}{{p.OwnCert, p.CertFile, 0o644}, {p.OwnKey, p.KeyFile, 0o600}} {
			b, err := os.ReadFile(in.path(f.from))
			if err != nil {
				return err
			}
			if err := in.write(f.to, b, f.mode, true); err != nil {
				return err
			}
		}
		hook, err := render(leHookTemplate, map[string]string{"Live": p.LELive, "Cert": p.CertFile, "Key": p.KeyFile})
		if err != nil {
			return err
		}
		if err := in.write(instLEHook, hook, 0o755, false); err != nil {
			return err
		}
		in.c.ok("certificate copied from " + p.LELive + "; certbot renewals are copied by " + instLEHook)
	default:
		if in.root == "" {
			if out, err := in.run("runuser", "-u", instUser, "--", "test", "-r", p.KeyFile); err != nil {
				return fmt.Errorf("the beeircd user can't read %s (%s): make it readable for group beeircd, or copy the files to %s", p.KeyFile, or(out, err.Error()), instTLS)
			}
		}
		in.c.ok("certificate: your files " + p.CertFile)
	}
	return nil
}

// roleText is the summary's Role line.
func (p *plan) roleText() string {
	if p.Role == "standalone" {
		return "on its own"
	}
	return fmt.Sprintf("%s, link port %d (TLS; server linking arrives in Stage 2)", p.Role, p.LinkPort)
}

// lowPorts says whether the server needs CAP_NET_BIND_SERVICE.
func (p *plan) lowPorts() bool {
	return p.Cert == "acme" || (p.PlainPort != 0 && p.PlainPort < 1024) || p.TLSPort < 1024 || (p.LinkPort != 0 && p.LinkPort < 1024)
}

func (in *installer) writeUnits(p *plan) error {
	unit, err := render(unitTemplate, map[string]bool{"LowPorts": p.lowPorts()})
	if err != nil {
		return err
	}
	for _, f := range []struct {
		path string
		body []byte
	}{{instUnit, unit}, {instBkUnit, []byte(backupUnit)}, {instBkTimer, []byte(backupTimer)}} {
		if err := in.write(f.path, f.body, 0o644, false); err != nil {
			return err
		}
	}
	for _, args := range [][]string{{"daemon-reload"}, {"enable", "beeircd.service"}, {"enable", "--now", "beeircd-backup.timer"}} {
		if out, err := in.run("systemctl", args...); err != nil {
			return fmt.Errorf("systemctl %s: %s", strings.Join(args, " "), or(out, err.Error()))
		}
	}
	in.c.ok("service beeircd enabled, starts with the machine, restarts if it stops")
	in.c.ok("daily backup at 04:00 (beeircd-backup.timer)")
	return nil
}

// firewall opens the ports in ufw or firewalld when one is active.
func (in *installer) firewall(p *plan) {
	var ports []string
	for _, port := range []int{p.PlainPort, p.TLSPort} {
		if port != 0 {
			ports = append(ports, strconv.Itoa(port))
		}
	}
	if p.LinkPort != 0 {
		ports = append(ports, strconv.Itoa(p.LinkPort))
	}
	if p.Cert == "acme" {
		ports = append(ports, "80")
	}
	list := strings.Join(ports, ", ")
	if out, err := in.run("ufw", "status"); err == nil && strings.Contains(out, "Status: active") {
		for _, port := range ports {
			if out, err := in.run("ufw", "allow", port+"/tcp"); err != nil {
				in.c.warn("ufw allow " + port + "/tcp: " + or(out, err.Error()))
				return
			}
		}
		in.c.ok("firewall (ufw): " + list + " open")
		return
	}
	if out, err := in.run("firewall-cmd", "--state"); err == nil && strings.TrimSpace(out) == "running" {
		for _, port := range ports {
			if out, err := in.run("firewall-cmd", "--permanent", "--add-port="+port+"/tcp"); err != nil {
				in.c.warn("firewall-cmd: " + or(out, err.Error()))
				return
			}
		}
		in.run("firewall-cmd", "--reload")
		in.c.ok("firewall (firewalld): " + list + " open")
		return
	}
	in.c.ok("firewall: none active here; open " + list + " in your provider's firewall if it has one")
}

// finish offers to start the server and shows where to connect.
func (in *installer) finish(p *plan) int {
	start, err := in.yesNo("\n  Start BeeIRCd now?", true)
	if err != nil || !start {
		fmt.Fprintf(in.out, "\n  Start it later with:  beeircd start\n%s\n", firstSteps(p.Admin))
		return 0
	}
	if code := cmdService("start", []string{"-config", instConf}, in.out, in.out); code != 0 {
		return code
	}
	if p.Cert == "acme" {
		in.waitACME()
	}
	fmt.Fprintf(in.out, "\n  Connect with:  %s  port %d (TLS)\n%s  beeircd status shows how it runs.\n\n", p.Name, p.TLSPort, firstSteps(p.Admin))
	return 0
}

// firstSteps tells the admin how to get opered on a new server: the oper
// entry belongs to the NickServ account, so the nick is registered first.
func firstSteps(admin string) string {
	return "  First time on IRC as " + admin + ":\n" +
		"    /msg NickServ REGISTER <a NickServ password> <your email>   (after 30 seconds online)\n" +
		"    /oper " + admin + " <the admin password you typed here>\n"
}

// waitACME waits up to 90 s for the first Let's Encrypt certificate.
func (in *installer) waitACME() {
	cfg, err := config.Load(instConf)
	if err != nil {
		return
	}
	for i := 0; i < 90; i++ {
		resp, err := ask(cfg, control.CmdStatus, "", 2*time.Second)
		if err == nil && resp.Status != nil && resp.Status.Cert != nil {
			c := resp.Status.Cert
			if !strings.Contains(c.Source, "stand-in") {
				in.c.ok("certificate issued for " + strings.Join(c.Names, ", ") + ", valid until " + c.NotAfter.Format("2 Jan 2006"))
				return
			}
			if c.RenewFailures > 0 {
				in.c.warn("Let's Encrypt failed: " + c.RenewError)
				in.c.warn("BeeIRCd runs on a self-signed stand-in and tries again in an hour; beeircd status shows how it goes")
				return
			}
		}
		time.Sleep(time.Second)
	}
	in.c.warn("no answer from Let's Encrypt yet; beeircd status shows how it goes")
}

func (in *installer) timeZone() string {
	if b, err := os.ReadFile(in.path("/etc/timezone")); err == nil && strings.TrimSpace(string(b)) != "" {
		return strings.TrimSpace(string(b))
	}
	if l, err := os.Readlink(in.path("/etc/localtime")); err == nil {
		if _, z, ok := strings.Cut(l, "zoneinfo/"); ok {
			return z
		}
	}
	return "server time"
}

// guessNetwork makes "BeeNet" from irc.beenet.org.
func guessNetwork(name string) string {
	parts := strings.Split(name, ".")
	if len(parts) < 2 {
		return ""
	}
	n := parts[len(parts)-2]
	if len(parts) == 2 {
		n = parts[0]
	}
	if n == "" || !config.ValidNetwork(n) {
		return ""
	}
	return strings.ToUpper(n[:1]) + n[1:]
}

// parsePorts reads "6667 plain, 6697 TLS", "6667 6697" or "6697" (TLS only).
func parsePorts(a string) (plain, tls int, why string) {
	var nums []int
	for _, f := range strings.FieldsFunc(a, func(r rune) bool { return r == ',' || r == ' ' || r == '/' }) {
		if n, err := strconv.Atoi(f); err == nil {
			if n < 1 || n > 65535 {
				return 0, 0, "ports are 1-65535"
			}
			nums = append(nums, n)
		}
	}
	switch len(nums) {
	case 1:
		return 0, nums[0], ""
	case 2:
		if nums[0] == nums[1] {
			return 0, 0, "two different ports"
		}
		return nums[0], nums[1], ""
	}
	return 0, 0, "\"6667 6697\" (plain and TLS) or \"6697\" (TLS only)"
}

func splitList(a string) []string {
	return strings.FieldsFunc(a, func(r rune) bool { return r == ',' || r == ' ' })
}

// makeSID turns the server name into a stable server ID like 4QK.
func makeSID(name string) string {
	const chars = "ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	h := sha256.Sum256([]byte(strings.ToLower(name)))
	return string([]byte{'0' + h[0]%10, chars[int(h[1])%len(chars)], chars[int(h[2])%len(chars)]})
}

func randomSecret() string {
	const chars = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	b := make([]byte, 40)
	rand.Read(b)
	for i := range b {
		b[i] = chars[int(b[i])%len(chars)]
	}
	return string(b)
}

// ---- uninstall ----

func cmdUninstall(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("uninstall", flag.ContinueOnError)
	fs.SetOutput(stderr)
	purge := fs.Bool("purge", false, "also delete the config, data, backups and the beeircd user")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	in := realInstaller(stdout)
	if os.Geteuid() != 0 {
		in.c.bad("run it as root (sudo beeircd uninstall)")
		return 1
	}
	return in.uninstall(*purge)
}

func (in *installer) uninstall(purge bool) int {
	what := "the program and the service; config and data stay in " + instEtc + " and " + instData
	if purge {
		what = "the program, the service, AND the config, data, backups and the beeircd user (cannot be undone)"
	}
	fmt.Fprintf(in.out, "\n  This removes %s.\n", what)
	if ok, err := in.yesNo("Go ahead?", false); err != nil || !ok {
		fmt.Fprintln(in.out, "  Nothing changed.")
		return 0
	}
	in.run("systemctl", "disable", "--now", "beeircd.service", "beeircd-backup.timer")
	for _, f := range []string{instUnit, instBkUnit, instBkTimer, instLink, instLEHook} {
		os.Remove(in.path(f))
	}
	os.RemoveAll(in.path(path.Dir(instBin)))
	in.run("systemctl", "daemon-reload")
	in.c.ok("service stopped and removed, program removed")
	if !purge {
		in.c.ok("kept " + instEtc + " and " + instData + " (beeircd uninstall --purge deletes them)")
		return 0
	}
	os.RemoveAll(in.path(instEtc))
	os.RemoveAll(in.path(instData))
	if in.root == "" {
		if out, err := in.run("userdel", instUser); err != nil {
			in.c.warn("userdel beeircd: " + or(out, err.Error()))
		}
	}
	in.c.ok("config, data, backups and the beeircd user deleted")
	in.c.ok("firewall rules were left as they are")
	return 0
}
