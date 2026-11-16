package extauth

import (
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/fhazal/beeircd/internal/config"
	"github.com/fhazal/beeircd/internal/passwd"
)

func TestFilterEncoding(t *testing.T) {
	eq := func(attr, val string) []byte {
		return tlv(0xa3, berString(tagOctets, attr), berString(tagOctets, val))
	}
	for _, tc := range []struct {
		in   string
		want []byte
	}{
		{"(uid=ann)", eq("uid", "ann")},
		{"(&(uid=ann)(objectClass=person))", tlv(0xa0, eq("uid", "ann"), eq("objectClass", "person"))},
		{"(|(uid=a)(!(cn=b)))", tlv(0xa1, eq("uid", "a"), tlv(0xa2, eq("cn", "b")))},
		{"(mail=*)", berString(0x87, "mail")},
		{`(cn=a\2ab)`, eq("cn", "a*b")},
		{"(cn=a*b*c)", tlv(0xa4, berString(tagOctets, "cn"), tlv(tagSequence, berString(0x80, "a"), berString(0x81, "b"), berString(0x82, "c")))},
		{"(age>=5)", tlv(0xa5, berString(tagOctets, "age"), berString(tagOctets, "5"))},
	} {
		got, err := encodeFilter(tc.in)
		if err != nil || !bytes.Equal(got, tc.want) {
			t.Errorf("%s: got % x (%v), want % x", tc.in, got, err, tc.want)
		}
	}
	for _, bad := range []string{"uid=ann", "(uid=ann", "(uid)", "(&(uid=a)", "(uid=a)(cn=b)", "(=x)", `(cn=a\2)`, "(cn:dn:=x)"} {
		if _, err := encodeFilter(bad); err == nil {
			t.Errorf("%s: no error", bad)
		}
	}
}

func TestEscapes(t *testing.T) {
	if got := escapeFilter(`a*(b)\c`); got != `a\2a\28b\29\5cc` {
		t.Errorf("escapeFilter: %s", got)
	}
	if got := escapeDN(` a,b=c#`); got != `\ a\,b\=c#` {
		t.Errorf("escapeDN: %s", got)
	}
	// What someone types as a name can never widen the search.
	f, err := encodeFilter(strings.ReplaceAll("(uid=%s)", "%s", escapeFilter("*)(uid=*")))
	want := tlv(0xa3, berString(tagOctets, "uid"), berString(tagOctets, "*)(uid=*"))
	if err != nil || !bytes.Equal(f, want) {
		t.Errorf("injected filter: % x (%v)", f, err)
	}
}

// fakeDirectory is a tiny LDAP server: it knows the service account and
// the people in users (uid -> password), each with a mail address.
type fakeDirectory struct {
	ln      net.Listener
	bindDN  string
	bindPW  string
	users   map[string]string
	filters chan []byte
}

func startDirectory(t *testing.T) *fakeDirectory {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	d := &fakeDirectory{ln: ln, bindDN: "cn=irc,dc=test", bindPW: "service-secret",
		users: map[string]string{"ann": "correct horse", "bob": "battery staple"}, filters: make(chan []byte, 16)}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go d.serve(c)
		}
	}()
	return d
}

func (d *fakeDirectory) serve(c net.Conn) {
	defer c.Close()
	r := bufio.NewReader(c)
	bound := false
	reply := func(id []byte, op []byte) { c.Write(tlv(tagSequence, tlv(tagInt, id), op)) }
	result := func(tag byte, code int) []byte {
		return tlv(tag, berInt(tagEnum, code), berString(tagOctets, ""), berString(tagOctets, ""))
	}
	for {
		e, err := readElement(r)
		if err != nil {
			return
		}
		kids, _ := children(e.body)
		if len(kids) < 2 {
			return
		}
		id, op := kids[0].body, kids[1]
		switch op.tag {
		case appBindRequest:
			p, _ := children(op.body)
			dn, pw := string(p[1].body), string(p[2].body)
			code := ldapInvalidCredentials
			switch {
			case pw == "": // an anonymous bind "works" on real directories
				code = ldapSuccess
			case dn == d.bindDN && pw == d.bindPW:
				code, bound = ldapSuccess, true
			case strings.HasPrefix(dn, "uid=") && strings.HasSuffix(dn, ",ou=people,dc=test"):
				uid := strings.TrimSuffix(strings.TrimPrefix(dn, "uid="), ",ou=people,dc=test")
				if want, ok := d.users[uid]; ok && want == pw {
					code = ldapSuccess
				}
			}
			reply(id, result(appBindResponse, code))
		case appSearchRequest:
			p, _ := children(op.body)
			filter := tlv(p[6].tag, p[6].body)
			d.filters <- filter
			if !bound {
				reply(id, result(appSearchDone, 50)) // insufficient access
				continue
			}
			for uid := range d.users {
				if bytes.Equal(filter, tlv(0xa3, berString(tagOctets, "uid"), berString(tagOctets, uid))) {
					reply(id, tlv(appSearchEntry,
						berString(tagOctets, "uid="+uid+",ou=people,dc=test"),
						tlv(tagSequence, tlv(tagSequence, berString(tagOctets, "mail"), tlv(tagSet, berString(tagOctets, uid+"@example.org"))))))
				}
			}
			reply(id, result(appSearchDone, ldapSuccess))
		case appUnbindRequest:
			return
		}
	}
}

func TestLDAPLogin(t *testing.T) {
	d := startDirectory(t)
	cfg := config.AuthLDAP{URL: "ldap://" + d.ln.Addr().String(), BindDN: d.bindDN, BindPassword: d.bindPW, BaseDN: "ou=people,dc=test"}
	src, err := newLDAP(cfg)
	if err != nil {
		t.Fatal(err)
	}
	ask := func(name, pw string) Answer {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return src.Check(ctx, Query{Name: name, Password: pw})
	}
	if a := ask("ann", "correct horse"); a.Result != OK || a.Email != "ann@example.org" {
		t.Fatalf("right password: %+v", a)
	}
	if a := ask("ann", "wrong"); a.Result != Wrong {
		t.Fatalf("wrong password: %+v", a)
	}
	if a := ask("ann", ""); a.Result != Wrong {
		t.Fatalf("an empty password must never log in: %+v", a)
	}
	if a := ask("nobody", "x"); a.Result != Unknown {
		t.Fatalf("unknown name: %+v", a)
	}
	if a := ask("*", "correct horse"); a.Result != Unknown {
		t.Fatalf("a * as the name matched someone: %+v", a)
	}

	// Without a search: the name goes straight into user_dn.
	direct, _ := newLDAP(config.AuthLDAP{URL: cfg.URL, UserDN: "uid=%s,ou=people,dc=test"})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if a := direct.Check(ctx, Query{Name: "bob", Password: "battery staple"}); a.Result != OK {
		t.Fatalf("user_dn login: %+v", a)
	}
	if a := direct.Check(ctx, Query{Name: "bob", Password: "nope"}); a.Result != Wrong {
		t.Fatalf("user_dn wrong password: %+v", a)
	}

	// A wrong service password is the directory's problem, not the user's.
	bad := cfg
	bad.BindPassword = "not it"
	broken, _ := newLDAP(bad)
	if a := broken.Check(ctx, Query{Name: "ann", Password: "correct horse"}); a.Result != Down {
		t.Fatalf("refused bind_dn: %+v", a)
	}
	d.ln.Close()
	if a := ask("ann", "correct horse"); a.Result != Down {
		t.Fatalf("directory gone: %+v", a)
	}
}

func TestSQLLogin(t *testing.T) {
	path := filepath.Join(t.TempDir(), "users.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	bc, _ := bcrypt.GenerateFromPassword([]byte("correct horse"), bcrypt.MinCost)
	ar, _ := passwd.Hash("battery staple")
	for _, q := range []string{
		"CREATE TABLE users (name TEXT PRIMARY KEY, password TEXT, email TEXT)",
		"CREATE TABLE logins (at INTEGER, name TEXT, ip TEXT, result TEXT)",
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	db.Exec("INSERT INTO users VALUES (?, ?, ?)", "ann", string(bc), "ann@example.org")
	db.Exec("INSERT INTO users VALUES (?, ?, ?)", "bob", ar, nil)
	db.Exec("INSERT INTO users VALUES (?, ?, ?)", "carl", "plainpass", "")

	cfg := config.AuthSQL{Driver: "sqlite", DSN: path,
		Query:    "SELECT password, email FROM users WHERE name = $name",
		LogQuery: "INSERT INTO logins VALUES ($time, $name, $ip, $result)"}
	src, err := newSQL(cfg, false)
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	ctx := context.Background()
	ask := func(name, pw string) Answer { return src.Check(ctx, Query{Name: name, Password: pw, IP: "192.0.2.7"}) }
	if a := ask("ann", "correct horse"); a.Result != OK || a.Email != "ann@example.org" {
		t.Fatalf("bcrypt: %+v", a)
	}
	if a := ask("bob", "battery staple"); a.Result != OK {
		t.Fatalf("argon2id: %+v", a)
	}
	if a := ask("ann", "nope"); a.Result != Wrong {
		t.Fatalf("wrong: %+v", a)
	}
	if a := ask("carl", "plainpass"); a.Result != Wrong {
		t.Fatalf("auto must not compare plain passwords: %+v", a)
	}
	if a := ask("x' OR '1'='1", "x"); a.Result != Unknown {
		t.Fatalf("injection: %+v", a)
	}
	var n int
	var leaked int
	db.QueryRow("SELECT COUNT(*) FROM logins WHERE ip = '192.0.2.7'").Scan(&n)
	db.QueryRow("SELECT COUNT(*) FROM logins WHERE result NOT IN ('ok','wrong','unknown')").Scan(&leaked)
	if n != 5 || leaked != 0 {
		t.Fatalf("login log: %d rows, %d odd", n, leaked)
	}

	// The database compares the password itself.
	cfg.Hash, cfg.LogQuery = "query", ""
	cfg.Query = "SELECT name, email FROM users WHERE name = $name AND password = $password"
	byQuery, _ := newSQL(cfg, false)
	defer byQuery.Close()
	if a := byQuery.Check(ctx, Query{Name: "carl", Password: "plainpass"}); a.Result != OK {
		t.Fatalf("hash = query: %+v", a)
	}
	if a := byQuery.Check(ctx, Query{Name: "carl", Password: "other"}); a.Result != Wrong {
		t.Fatalf("hash = query, wrong: %+v", a)
	}
	if a := byQuery.Check(ctx, Query{Name: "carl", Password: ""}); a.Result != Wrong {
		t.Fatalf("empty password: %+v", a)
	}
}

func TestPlaceholders(t *testing.T) {
	st := prepare("postgres", "SELECT p FROM u WHERE n = $name AND ip <> $ip AND n2 = $name")
	if st.text != "SELECT p FROM u WHERE n = $1 AND ip <> $2 AND n2 = $3" || strings.Join(st.args, ",") != "name,ip,name" {
		t.Fatalf("postgres: %q %v", st.text, st.args)
	}
	st = prepare("mysql", "SELECT p FROM u WHERE n = $name")
	if st.text != "SELECT p FROM u WHERE n = ?" {
		t.Fatalf("mysql: %q", st.text)
	}
}

func TestHashKinds(t *testing.T) {
	for _, tc := range []struct{ kind, stored string }{
		{"sha256", "2c26b46b68ffc68ff99b453c1d30413413422d706483bfa0f98a5e886266e7ae"},
		{"md5", "acbd18db4cc2f85cedef654fccc4a4d8"},
		{"plain", "foo"},
	} {
		if !checkHash(tc.kind, "foo", tc.stored) || checkHash(tc.kind, "bar", tc.stored) {
			t.Errorf("%s does not check right", tc.kind)
		}
	}
}
