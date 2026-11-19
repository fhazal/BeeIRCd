package main

// The CLI side of server links: `beeircd link-info`, the link
// lines of `beeircd check` and the Links row of `beeircd status`.

import (
	"errors"
	"fmt"
	"io"
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/fhazal/beeircd/internal/config"
	"github.com/fhazal/beeircd/internal/control"
	"github.com/fhazal/beeircd/internal/link"
)

// cmdLinkInfo prints what another server needs to link with this one.
func cmdLinkInfo(args []string, stdout, stderr io.Writer) int {
	path, _, ok := adminFlags("link-info", args, stderr)
	if !ok {
		return 2
	}
	cfg, err := config.Load(path)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if !cfg.Link.Linked() {
		fmt.Fprintln(stderr, "This server is standalone. Set [link] role = \"hub\" or \"leaf\" in beeircd.conf, restart, then run beeircd link-info again.")
		return 1
	}
	fp, err := link.FileFingerprint(cfg.Link.Cert)
	if errors.Is(err, os.ErrNotExist) {
		fmt.Fprintln(stderr, "No link certificate yet: it is made when the server starts as a hub or leaf. Restart it, then run beeircd link-info again.")
		return 1
	}
	if err != nil {
		fmt.Fprintln(stderr, "link certificate:", err)
		return 1
	}
	svc, _, _ := cfg.ServicesServer()
	fmt.Fprint(stdout, linkInfoText(cfg.Server.Name, cfg.Server.SID, cfg.Link.Role, cfg.Link.Port, fp, cfg.Link.Servers, svc))
	if hash, err := link.FileCertHash(cfg.Link.Cert); err == nil {
		fmt.Fprint(stdout, inspInfoText(cfg.Server.Name, cfg.Link.Port, hash, svc))
	}
	return 0
}

// inspInfoText is the block for an InspIRCd 4 server on the other end.
// services is our services server's name ("" when services run elsewhere).
// Only the services server is U-lined: a U-line on this server itself
// would hide our users' connects and quits on InspIRCd and skip its
// permission checks for everyone here.
func inspInfoText(name string, port int, hash, services string) string {
	var b strings.Builder
	fmt.Fprintf(&b, `  On an InspIRCd 4 server, add to inspircd.conf, then: /REHASH
  (with the ssl_openssl, spanningtree and services modules loaded and an
  <sslprofile name="Servers"> for links)

    <link name="%s" ipaddr="%s" port="%d"
          allowmask="*" sslprofile="Servers"
          sendpass="PASSWORD" recvpass="PASSWORD"
          fingerprint="%s">
`, name, name, port, hash)
	if services != "" {
		fmt.Fprintf(&b, `    <uline server="%s" silent="yes">
    <module name="sasl">
    <sasl target="%s">
`, services, services)
	}
	b.WriteString(`
  And here, a [[link.server]] block for it with protocol = "inspircd",
  password = "PASSWORD" (the same one) and its fingerprint (see "Linking
  an InspIRCd 4 server" in docs/ADMIN.md).

`)
	return b.String()
}

// linkInfoText is what `beeircd link-info` and the installer print: this
// server's fingerprint and the block the other side pastes. A hub's block
// tells leaves where to connect; a leaf's block tells its hub to wait for
// it. servers are the [[link.server]] blocks this server already has.
func linkInfoText(name, sid, role string, port int, fp string, servers []config.LinkServer, services string) string {
	var b strings.Builder
	ps := strconv.Itoa(port)
	fmt.Fprintf(&b, "\n  This server   %s (SID %s), %s, link port %s (TLS)\n  Fingerprint   %s\n", name, sid, role, ps, fp)
	for _, s := range servers {
		state := "fingerprint set"
		if s.Fingerprint == "" {
			state = "fingerprint MISSING: add it to beeircd.conf, then beeircd rehash"
		}
		where := "it connects here"
		if s.Host != "" {
			where = fmt.Sprintf("connects to %s:%d", s.Host, s.Port)
		}
		fmt.Fprintf(&b, "  Links to      %s (%s; %s)\n", s.Name, where, state)
	}
	if role == "leaf" {
		fmt.Fprintf(&b, `
  On the hub (BeeIRCd), add to beeircd.conf, then: beeircd rehash

    [[link.server]]
    name = "%s"
    fingerprint = "%s"
    # no host: this leaf connects to the hub

  On an UnrealIRCd 6 hub, add to unrealircd.conf, then: ./unrealircd rehash

    link %s {
        incoming { mask *; }
        password "%s" { spkifp; }
        class servers;
    }
`, name, fp, name, fp)
	} else {
		fmt.Fprintf(&b, `
  On each leaf (BeeIRCd), add to beeircd.conf, then: beeircd rehash

    [[link.server]]
    name = "%s"
    host = "%s"   # or this server's IP address
    port = %s
    fingerprint = "%s"
    autoconnect = true

  On an UnrealIRCd 6 leaf, add to unrealircd.conf, then: ./unrealircd rehash

    link %s {
        incoming { mask *; }
        outgoing { hostname %s; port %s; options { tls; autoconnect; } }
        password "%s" { spkifp; }
        class servers;
    }
`, name, name, ps, fp, name, name, ps, fp)
	}
	if services != "" {
		// Without these UnrealIRCd keeps SASL off and takes no SVSNICK
		// (SANICK) from us; it may find SASL by itself, but not the rest.
		fmt.Fprintf(&b, `
  And on any UnrealIRCd server, U-line our services (unrealircd.conf):

    ulines { %s; }
    set { services-server "%s"; }
`, services, services)
	}
	b.WriteString(`
  The fingerprint is the lock: only a server holding this server's private
  key can link as it. No password is needed (add password = "..." on both
  sides only if the other side wants one). Every server on a network needs
  its own SID. And here, add a [[link.server]] block for the other server
  with its fingerprint (beeircd link-info there, or ./unrealircd spkifp).

`)
	return b.String()
}

// checkLinks adds the link lines to beeircd check.
func checkLinks(c *checker, cfg *config.Config) {
	if !cfg.Link.Linked() {
		return
	}
	_, fp, err := link.Load(cfg.Link.Cert, cfg.Link.Key)
	switch {
	case err != nil && errors.Is(err, os.ErrNotExist):
		c.ok("link certificate: made at the next start")
	case err != nil:
		c.bad(err.Error())
	default:
		c.ok("link certificate OK", "(fingerprint "+fp+")")
	}
	for _, w := range linkFileWarnings(cfg, fileMode) {
		c.warn(w)
	}
	if len(cfg.Link.Servers) == 0 {
		c.ok("links: none set up yet (beeircd link-info shows how)")
		return
	}
	var names []string
	for _, s := range cfg.Link.Servers {
		names = append(names, s.Name)
	}
	c.ok("links: " + strings.Join(names, ", "))
}

// fileMode returns a file's permission bits (ok false when it can't be
// read, or on Windows, where they mean nothing).
func fileMode(path string) (os.FileMode, bool) {
	if runtime.GOOS == "windows" {
		return 0, false
	}
	fi, err := os.Stat(path)
	if err != nil {
		return 0, false
	}
	return fi.Mode().Perm(), true
}

// linkFileWarnings flags link secrets other users on the machine could
// read: beeircd.conf holding a link password, or the link key.
func linkFileWarnings(cfg *config.Config, mode func(string) (os.FileMode, bool)) []string {
	var out []string
	hasPassword := false
	for _, s := range cfg.Link.Servers {
		hasPassword = hasPassword || s.Password != ""
	}
	if m, ok := mode(cfg.Path); ok && hasPassword && m&0o004 != 0 {
		out = append(out, fmt.Sprintf("%s holds a link password and every user on this machine can read it (chmod o-r %s)", cfg.Path, cfg.Path))
	}
	plainWebIRC := false
	for _, w := range cfg.WebIRC {
		plainWebIRC = plainWebIRC || !w.Hashed()
	}
	if m, ok := mode(cfg.Path); ok && plainWebIRC && m&0o004 != 0 {
		out = append(out, fmt.Sprintf("%s holds a WEBIRC gateway password and every user on this machine can read it (chmod o-r %s)", cfg.Path, cfg.Path))
	}
	if m, ok := mode(cfg.Link.Key); ok && m&0o077 != 0 {
		out = append(out, fmt.Sprintf("the link key %s can be read by other users; it proves this server's identity (chmod 600 %s)", cfg.Link.Key, cfg.Link.Key))
	}
	return out
}

// linksRow is the Links row of the status screen.
func linksRow(st *control.Status, p paint, now time.Time) []string {
	var out []string
	for _, l := range st.Links {
		switch l.State {
		case "up":
			lag := "lag " + ms(l.LagMS)
			if l.LagMS == 0 {
				lag = "lag not measured yet"
			}
			text := fmt.Sprintf("%s %s for %s · %s", l.Name, p.ok("● up"), since(now.Sub(l.Since)), lag)
			if l.Behind > 0 {
				text += fmt.Sprintf(" · %s behind it", plural(l.Behind, "server"))
			}
			if l.Software != "" {
				text += " " + p.dim("("+l.Software+")")
			}
			out = append(out, text)
		case "linking", "connecting":
			out = append(out, l.Name+" "+p.dim("connecting…"))
		default:
			text := l.Name + " " + p.warn("● down")
			switch {
			case l.Error != "" && !l.Retry.IsZero():
				text += p.warn(" · " + l.Error + " · next try in " + since(l.Retry.Sub(now)))
			case !l.Out:
				text += p.dim(" · waiting for it to connect")
			}
			out = append(out, text)
		}
	}
	return out
}
