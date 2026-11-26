package services

import (
	"strings"
)

// Anope's OperServ OPER (os_oper) over BeeIRCd's one oper list:
// LIST and INFO for opers, ADD and DEL for flag a (they run AdminServ OPER,
// which answers).

func osOper(s *Services, c *Client, args []string) {
	switch sub := strings.ToUpper(argAt(args, 0)); sub {
	case "", "LIST":
		opers, err := s.store.Opers()
		if err != nil {
			s.dbErr(c, s.operServ, err)
			return
		}
		s.os(c, "Name                 Class")
		if admin := s.cfg.Services.Admin; admin != "" {
			s.os(c, "%-20s netadmin (server admin from beeircd.conf)", admin)
		}
		for _, o := range opers {
			class := o.Class
			if class == "" {
				class = "(flags " + o.Flags + ")"
			}
			if o.Disabled != "" {
				class += " DISABLED"
			}
			s.os(c, "%-20s %s", o.Account, class)
		}
	case "INFO":
		want := argAt(args, 1)
		found := false
		for _, oc := range s.cfg.Classes() {
			if want != "" && !strings.EqualFold(oc.Name, want) {
				continue
			}
			found = true
			s.os(c, "Class %s: %s", oc.Name, describeOperFlags(oc.Flags))
			if want != "" {
				if oc.Title != "" {
					s.os(c, "  WHOIS: %s", oc.Title)
				}
				if oc.Vhost != "" {
					s.os(c, "  vHost: %s", oc.Vhost)
				}
				for _, l := range oc.Swhois {
					s.os(c, "  swhois: %s", l)
				}
			}
		}
		if !found {
			s.os(c, "Oper class %s doesn't exist.", want)
		}
	case "ADD", "DEL":
		if !s.needFlag(c, s.operServ, 'a') {
			return
		}
		nick := argAt(args, 1)
		if nick == "" || sub == "ADD" && argAt(args, 2) == "" {
			s.os(c, "Syntax: OPER ADD <nick> <class> | OPER DEL <nick>")
			return
		}
		line := "OPER " + nick + " DEL"
		if sub == "ADD" {
			line = "OPER " + nick + " ADD CLASS " + args[2] + " HOST *@*"
		}
		s.MessageAlias(c.user.UID, "as", line)
	default:
		s.os(c, "Syntax: OPER LIST | INFO [class] | ADD <nick> <class> | DEL <nick>")
	}
}

// describeOperFlags names oper flags: "ghks (global, hosts, kill, services)".
func describeOperFlags(flags string) string {
	var names []string
	for i := 0; i < len(flags); i++ {
		if n, ok := flagNames[flags[i]]; ok {
			names = append(names, n)
		}
	}
	return flags + " (" + strings.Join(names, ", ") + ")"
}
