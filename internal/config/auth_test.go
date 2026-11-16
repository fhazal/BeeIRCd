package config

import "testing"

func TestAuthCheck(t *testing.T) {
	ldap := AuthLDAP{URL: "ldaps://ldap.example.org", BaseDN: "ou=people,dc=example,dc=org"}
	sql := AuthSQL{Driver: "mysql", DSN: "x", Query: "SELECT p FROM u WHERE n = $name"}
	good := []Auth{
		{},
		{Source: "ldap", LDAP: ldap},
		{Source: "ldap", LDAP: AuthLDAP{URL: "ldap://h", StartTLS: true, UserDN: "uid=%s,dc=x"}},
		{Source: "sql", SQL: sql},
		{Source: "sql", SQL: AuthSQL{Driver: "postgres", DSN: "x", Hash: "query", Query: "SELECT 1 FROM u WHERE n = $name AND p = crypt($password, p)"}},
	}
	for i, a := range good {
		if err := a.check(); err != nil {
			t.Errorf("good %d: %v", i, err)
		}
	}
	bad := map[string]Auth{
		"source":        {Source: "radius"},
		"no url":        {Source: "ldap", LDAP: AuthLDAP{BaseDN: "dc=x"}},
		"http url":      {Source: "ldap", LDAP: AuthLDAP{URL: "https://h", BaseDN: "dc=x"}},
		"no base":       {Source: "ldap", LDAP: AuthLDAP{URL: "ldaps://h"}},
		"ldaps+tls":     {Source: "ldap", LDAP: AuthLDAP{URL: "ldaps://h", BaseDN: "dc=x", StartTLS: true}},
		"filter":        {Source: "ldap", LDAP: AuthLDAP{URL: "ldaps://h", BaseDN: "dc=x", Filter: "uid=%s"}},
		"filter no %s":  {Source: "ldap", LDAP: AuthLDAP{URL: "ldaps://h", BaseDN: "dc=x", Filter: "(uid=ann)"}},
		"half bind":     {Source: "ldap", LDAP: AuthLDAP{URL: "ldaps://h", BaseDN: "dc=x", BindDN: "cn=irc"}},
		"driver":        {Source: "sql", SQL: AuthSQL{Driver: "oracle", DSN: "x", Query: "$name"}},
		"no dsn":        {Source: "sql", SQL: AuthSQL{Driver: "mysql", Query: "$name"}},
		"no $name":      {Source: "sql", SQL: AuthSQL{Driver: "mysql", DSN: "x", Query: "SELECT 1"}},
		"hash":          {Source: "sql", SQL: AuthSQL{Driver: "mysql", DSN: "x", Query: "$name", Hash: "rot13"}},
		"query no pass": {Source: "sql", SQL: AuthSQL{Driver: "mysql", DSN: "x", Query: "$name", Hash: "query"}},
	}
	for name, a := range bad {
		if err := a.check(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}
