package config

import (
	"fmt"
	"net/url"
	"strings"
	"time"
)

// Auth is [auth]: passwords checked against a directory (LDAP) or a
// database (SQL) kept outside BeeIRCd. Off until source is set.
type Auth struct {
	// Source is "ldap", "sql" or "" (off).
	Source string `toml:"source"`
	// LocalPasswords: a NickServ password set here still works when the
	// outside source says no or can't be reached. Default on.
	LocalPasswords *bool `toml:"local_passwords"`
	// Create makes the NickServ account the first time someone the
	// outside source knows logs in. Default on.
	Create *bool `toml:"create"`
	// Opers: oper passwords are asked from the outside source too, under
	// the oper's account name.
	Opers bool `toml:"opers"`
	// Timeout is how long one question to the outside source may take.
	Timeout Duration `toml:"timeout"`

	LDAP AuthLDAP `toml:"ldap"`
	SQL  AuthSQL  `toml:"sql"`
}

// AuthLDAP is [auth.ldap].
type AuthLDAP struct {
	URL          string `toml:"url"` // ldaps://host[:636] or ldap://host[:389]
	StartTLS     bool   `toml:"starttls"`
	Verify       *bool  `toml:"verify"`  // check the directory's certificate; default on
	CAFile       string `toml:"ca_file"` // its own CA, when not a public one
	BindDN       string `toml:"bind_dn"`
	BindPassword string `toml:"bind_password"`
	BaseDN       string `toml:"base_dn"`
	Filter       string `toml:"filter"`      // "(uid=%s)"
	UserDN       string `toml:"user_dn"`     // instead of a search: "uid=%s,ou=people,dc=example,dc=org"
	EmailAttr    string `toml:"email_attr"`  // "mail"
	OperFilter   string `toml:"oper_filter"` // with opers: who may be asked for as an oper
}

// AuthSQL is [auth.sql].
type AuthSQL struct {
	Driver    string `toml:"driver"` // mysql, postgres or sqlite
	DSN       string `toml:"dsn"`
	Query     string `toml:"query"`      // gives the password (hash) and, as a second column, the email
	Hash      string `toml:"hash"`       // auto, bcrypt, argon2, sha256, sha512, md5, plain or query
	OperQuery string `toml:"oper_query"` // with opers: the same for oper logins
	LogQuery  string `toml:"log_query"`  // run after every login try
}

// On reports whether an outside source is set.
func (a *Auth) On() bool { return a.Source != "" }

// LocalToo reports whether passwords set here still count.
func (a *Auth) LocalToo() bool { return a.LocalPasswords == nil || *a.LocalPasswords }

// Creates reports whether a first login makes the account.
func (a *Auth) Creates() bool { return a.Create == nil || *a.Create }

// TimeoutOr is Timeout, 5 seconds when not set.
func (a *Auth) TimeoutOr() time.Duration {
	if a.Timeout.Duration > 0 {
		return a.Timeout.Duration
	}
	return 5 * time.Second
}

// Verifies reports whether the directory's certificate is checked.
func (l *AuthLDAP) Verifies() bool { return l.Verify == nil || *l.Verify }

// FilterOr is Filter, "(uid=%s)" when not set.
func (l *AuthLDAP) FilterOr() string {
	if l.Filter != "" {
		return l.Filter
	}
	return "(uid=%s)"
}

// EmailAttrOr is EmailAttr, "mail" when not set.
func (l *AuthLDAP) EmailAttrOr() string {
	if l.EmailAttr != "" {
		return l.EmailAttr
	}
	return "mail"
}

// HashOr is Hash, "auto" when not set.
func (q *AuthSQL) HashOr() string {
	if q.Hash != "" {
		return strings.ToLower(q.Hash)
	}
	return "auto"
}

func (a *Auth) check() error {
	if t := a.Timeout.Duration; t < 0 || t > time.Minute {
		return fmt.Errorf("[auth] timeout: use up to 1m")
	}
	switch a.Source {
	case "":
		return nil
	case "ldap":
		return a.LDAP.check(a.Opers)
	case "sql":
		return a.SQL.check(a.Opers)
	}
	return fmt.Errorf("[auth] source %q: use \"ldap\" or \"sql\"", a.Source)
}

func (l *AuthLDAP) check(opers bool) error {
	u, err := url.Parse(l.URL)
	if err != nil || u.Host == "" || (u.Scheme != "ldap" && u.Scheme != "ldaps") {
		return fmt.Errorf("[auth.ldap] url %q: use ldaps://host or ldap://host", l.URL)
	}
	if u.Scheme == "ldaps" && l.StartTLS {
		return fmt.Errorf("[auth.ldap] starttls is for ldap:// addresses; ldaps:// is encrypted already")
	}
	if l.UserDN != "" {
		if strings.Count(l.UserDN, "%s") != 1 {
			return fmt.Errorf("[auth.ldap] user_dn needs one %%s for the name, like \"uid=%%s,ou=people,dc=example,dc=org\"")
		}
	} else if l.BaseDN == "" {
		return fmt.Errorf("[auth.ldap] needs base_dn (where to search) or user_dn")
	}
	for name, f := range map[string]string{"filter": l.FilterOr(), "oper_filter": l.OperFilter} {
		if f == "" {
			continue
		}
		if strings.Count(f, "%s") < 1 || !strings.HasPrefix(f, "(") || !strings.HasSuffix(f, ")") {
			return fmt.Errorf("[auth.ldap] %s %q: write it like \"(uid=%%s)\"", name, f)
		}
	}
	if (l.BindDN == "") != (l.BindPassword == "") {
		return fmt.Errorf("[auth.ldap] bind_dn and bind_password go together")
	}
	if opers && l.OperFilter != "" && l.BaseDN == "" {
		return fmt.Errorf("[auth.ldap] oper_filter needs base_dn")
	}
	return nil
}

func (q *AuthSQL) check(opers bool) error {
	switch q.Driver {
	case "mysql", "postgres", "sqlite":
	default:
		return fmt.Errorf("[auth.sql] driver %q: use \"mysql\", \"postgres\" or \"sqlite\"", q.Driver)
	}
	if q.DSN == "" {
		return fmt.Errorf("[auth.sql] needs dsn (how to reach the database)")
	}
	switch q.HashOr() {
	case "auto", "bcrypt", "argon2", "sha256", "sha512", "md5", "plain", "query":
	default:
		return fmt.Errorf("[auth.sql] hash %q: use auto, bcrypt, argon2, sha256, sha512, md5, plain or query", q.Hash)
	}
	if !strings.Contains(q.Query, "$name") {
		return fmt.Errorf("[auth.sql] query needs $name, like \"SELECT password, email FROM users WHERE name = $name\"")
	}
	if q.HashOr() == "query" && !strings.Contains(q.Query, "$password") {
		return fmt.Errorf("[auth.sql] hash = \"query\" needs $password in query: the database compares the password itself")
	}
	if opers && q.OperQuery != "" && !strings.Contains(q.OperQuery, "$name") {
		return fmt.Errorf("[auth.sql] oper_query needs $name")
	}
	return nil
}
