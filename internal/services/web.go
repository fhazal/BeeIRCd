package services

import (
	"strings"

	"github.com/fhazal/beeircd/internal/state"
)

// The Hive Panel's My nick page runs the real NickServ SET: the same
// checks, answers and log lines as /ns set on IRC. A stand-in client takes
// the answers instead of a notice.

// webSetOptions are the SET options My nick offers. The others (PASSWORD,
// EMAIL, DISPLAY ...) mail, hash or rename and stay on IRC for now.
var webSetOptions = map[string]bool{"KILL": true, "HIDE": true, "AUTOOP": true, "PRIVATE": true, "GREET": true, "CHANSTATS": true}

// WebSet runs NickServ SET option args for account id, for the Hive Panel
// at ip. It returns whether something changed and NickServ's answers.
func (s *Services) WebSet(id int64, ip, option string, args []string) (bool, []string) {
	if s.readonly {
		return false, []string{readonlyRefusal}
	}
	option = strings.ToUpper(option)
	if !webSetOptions[option] {
		return false, []string{"That can't be changed here."}
	}
	a, err := s.store.AccountByID(id)
	if err != nil {
		s.log.Printf("services database: %v", err)
		return false, []string{"Something went wrong on the server."}
	}
	if a == nil {
		return false, []string{"Your account no longer exists."}
	}
	if a.Suspended(s.KeepNow()) {
		return false, []string{a.Name + " is suspended."}
	}
	var out []string
	c := &Client{s: s, ip: ip, accountID: a.ID, cmdName: "SET " + option,
		user:  &state.User{Nick: a.Name, Ident: "hive", Host: "hive-panel", RealHost: ip, IP: ip, Account: a.Name},
		reply: func(text string) { out = append(out, text) }}
	changed := s.setOption(c, a, "SET", option, args)
	return changed, out
}
