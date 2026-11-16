// Package extauth checks passwords against a source kept outside BeeIRCd:
// an LDAP directory or an SQL database.
package extauth

import (
	"context"
	"fmt"

	"github.com/fhazal/beeircd/internal/config"
)

// Result is what the outside source said.
type Result int

const (
	OK      Result = iota // the password is right
	Wrong                 // the name is known, the password is not right
	Unknown               // the source has nobody of that name
	Down                  // the source could not be asked
)

func (r Result) String() string {
	switch r {
	case OK:
		return "ok"
	case Wrong:
		return "wrong"
	case Unknown:
		return "unknown"
	}
	return "down"
}

// Query is one login to check.
type Query struct {
	Name     string
	Password string
	IP       string
	Oper     bool // an oper login (oper_filter, oper_query)
}

// Answer is the source's answer. Email is set when the source gave one.
type Answer struct {
	Result Result
	Email  string
	Err    error // with Down: why
}

// Source is an outside place that knows passwords.
type Source interface {
	Check(ctx context.Context, q Query) Answer
	Close()
}

// New opens the source [auth] names; nil when it is off.
func New(cfg config.Auth) (Source, error) {
	switch cfg.Source {
	case "":
		return nil, nil
	case "ldap":
		return newLDAP(cfg.LDAP)
	case "sql":
		return newSQL(cfg.SQL, cfg.Opers)
	}
	return nil, fmt.Errorf("[auth] source %q is not known", cfg.Source)
}
