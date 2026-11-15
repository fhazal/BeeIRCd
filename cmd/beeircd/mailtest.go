package main

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/fhazal/beeircd/internal/config"
	"github.com/fhazal/beeircd/internal/mail"
)

// cmdMailTest sends a test mail with the [mail] settings of beeircd.conf
// and checks the sender domain's SPF, DKIM and DMARC: the Hive Panel's
// Mail card, for people without the panel.
func cmdMailTest(args []string, stdout, stderr io.Writer) int {
	path, rest, ok := adminFlags("mail-test", args, stderr)
	if !ok {
		return 2
	}
	if len(rest) != 1 || !mail.ValidAddress(rest[0]) {
		fmt.Fprintln(stderr, "Usage: beeircd mail-test [-config file] you@example.org")
		return 2
	}
	cfg, err := config.Load(path)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	c := &checker{w: stdout, p: colourFor(stdout)}
	m := cfg.Mail
	if !m.On() {
		c.bad("mail is off: set use_mail = true and send_from in [mail] (or use the Mail card in the Hive Panel)")
		return 1
	}
	how := m.SMTPServer
	if how == "" {
		how = "sendmail (" + m.SendmailPath + ")"
	}
	fmt.Fprintf(stdout, "Sending a test mail to %s from %s through %s ...\n", rest[0], m.SendFrom, how)
	start := time.Now()
	err = mail.Send(m, rest[0], rest[0], "Test mail from "+cfg.Server.Network,
		"Hi,\n\nThis is a test mail from beeircd mail-test on "+cfg.Server.Name+".\nIf you can read it, services can send mail.\n\n"+cfg.Server.Network+" administrators.")
	if err != nil {
		c.bad(mail.Explain(m, err))
		fmt.Fprintln(stdout, "    "+c.p.dim(err.Error()))
	} else {
		c.ok(fmt.Sprintf("sent in %.1f s: look in the inbox (and in spam)", time.Since(start).Seconds()))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	fmt.Fprintf(stdout, "Will it land in the inbox? (%s)\n", mail.Domain(m.SendFrom))
	for _, k := range mail.CheckDomain(ctx, m, nil) {
		switch k.State {
		case "ok":
			c.ok(k.Name + ": " + k.Text)
		case "warn":
			c.warn(k.Name + ": " + k.Text)
		default:
			c.bad(k.Name + ": " + k.Text)
		}
		if k.Fix != "" {
			fmt.Fprintln(stdout, "      "+k.Fix)
		}
	}
	if err != nil {
		return 1
	}
	return 0
}
