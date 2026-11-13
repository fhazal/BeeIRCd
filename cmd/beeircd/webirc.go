package main

// `beeircd webirc add <name> <ip>`: adds a [[webirc]] gateway (a web chat
// such as BeeRoom) to beeircd.conf, makes its password and re-reads the
// config, so nobody has to edit the file by hand.

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/fhazal/beeircd/internal/config"
	"github.com/fhazal/beeircd/internal/control"
)

const webircUsage = `Usage: beeircd webirc add [-config file] [-q] <name> <ip>[,<ip>...]

  Lets a web chat (like BeeRoom) on <ip> tell BeeIRCd each person's real IP.
  Adds a [[webirc]] block to beeircd.conf, makes its password, re-reads the
  config and prints the password to put in the web chat's settings.
  -q prints only the password (for install scripts).
`

func cmdWebirc(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || strings.ToLower(args[0]) != "add" {
		fmt.Fprint(stderr, webircUsage)
		return 2
	}
	fs := flag.NewFlagSet("webirc add", flag.ContinueOnError)
	fs.SetOutput(stderr)
	path := fs.String("config", defaultConfig, "config file")
	quiet := fs.Bool("q", false, "print only the password")
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	if fs.NArg() != 2 {
		fmt.Fprint(stderr, webircUsage)
		return 2
	}
	name, hosts := fs.Arg(0), splitList(strings.ReplaceAll(fs.Arg(1), " ", ","))
	c := &checker{w: stdout, p: colourFor(stdout)}
	if *quiet {
		c.w = io.Discard
	}
	secret := webircSecret()
	if err := addWebIRC(*path, name, hosts, secret, time.Now()); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	c.ok(fmt.Sprintf("[[webirc]] %s added to %s (from %s)", name, *path, strings.Join(hosts, ", ")))
	cfg, err := config.Load(*path)
	if err == nil {
		_, err = ask(cfg, control.CmdRehash, "", 30*time.Second)
	}
	if err != nil {
		c.ok("BeeIRCd isn't running: it uses the gateway when it starts")
	} else {
		c.ok("rehashed: people coming through " + name + " now show their real IP")
	}
	if *quiet {
		fmt.Fprintln(stdout, secret)
		return 0
	}
	fmt.Fprintf(stdout, "  Paste this into %s's settings (it is also in %s):\n    %s\n", name, filepath.Base(*path), secret)
	return 0
}

// webircSecret is 20 random letters and digits in groups of four, easy to
// copy by hand: b7Qm-2xR9-kT4p-Wv8e-Lc3n.
func webircSecret() string {
	s := randomSecret()[:20]
	return s[0:4] + "-" + s[4:8] + "-" + s[8:12] + "-" + s[12:16] + "-" + s[16:20]
}

// addWebIRC appends a [[webirc]] block to the config file. The new file is
// checked like a start would check it before it replaces the old one, so a
// bad name or host changes nothing.
func addWebIRC(path, name string, hosts []string, secret string, now time.Time) error {
	cfg, err := config.Load(path)
	if err != nil {
		return fmt.Errorf("%s doesn't load, fix it first: %w", path, err)
	}
	for _, w := range cfg.WebIRC {
		if strings.EqualFold(w.Name, name) {
			return fmt.Errorf("%s already has a [[webirc]] %q; remove that block first to make a new one", path, w.Name)
		}
	}
	if len(hosts) == 0 {
		return errors.New("give the web chat's IP, like 127.0.0.1")
	}
	old, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	fi, err := os.Stat(path)
	if err != nil {
		return err
	}
	text := string(old)
	if text != "" && !strings.HasSuffix(text, "\n") {
		text += "\n"
	}
	text += fmt.Sprintf("\n# Web chat gateway, added by `beeircd webirc add` on %s.\n[[webirc]]\nname = %s\npassword = %s\nhosts = %s\n",
		now.Format("2 Jan 2006"), tomlString(name), tomlString(secret), tomlList(hosts))
	tmp := filepath.Join(filepath.Dir(path), "."+filepath.Base(path)+".webirc")
	if err := os.WriteFile(tmp, []byte(text), fi.Mode().Perm()); err != nil {
		return err
	}
	defer os.Remove(tmp)
	if _, err := config.Load(tmp); err != nil {
		return fmt.Errorf("not added: %v", strings.Replace(err.Error(), tmp, path, 1))
	}
	copyOwner(fi, tmp)
	return os.Rename(tmp, path)
}
