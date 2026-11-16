package server

import (
	"database/sql"
	"path/filepath"
	"testing"

	"golang.org/x/crypto/bcrypt"

	"github.com/fhazal/beeircd/internal/config"
	"github.com/fhazal/beeircd/internal/services"
	"github.com/fhazal/beeircd/internal/store"
)

// outsideDB makes a user table as an outside login source would keep it:
// ann and bob, both with the password in pwOutside.
const pwOutside = "from-the-directory"

func outsideDB(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "users.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	hash, _ := bcrypt.GenerateFromPassword([]byte(pwOutside), bcrypt.MinCost)
	if _, err := db.Exec("CREATE TABLE users (name TEXT PRIMARY KEY, password TEXT, email TEXT)"); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"ann", "bob", "Bee"} {
		if _, err := db.Exec("INSERT INTO users VALUES (?, ?, ?)", n, string(hash), n+"@example.org"); err != nil {
			t.Fatal(err)
		}
	}
	return path
}

func outsideSQL(path string, more func(*config.Auth)) func(*config.Config) {
	return func(c *config.Config) {
		c.Auth = config.Auth{Source: "sql", SQL: config.AuthSQL{Driver: "sqlite", DSN: path,
			Query: "SELECT password, email FROM users WHERE name = $name COLLATE NOCASE"}}
		if more != nil {
			more(&c.Auth)
		}
	}
}

func accountOf(t *testing.T, s *Server, nick string) *store.Account {
	t.Helper()
	var a *store.Account
	s.onLoop(func() { a, _ = s.store.AccountByNick(nick) })
	return a
}

func TestOutsideLoginMakesTheAccount(t *testing.T) {
	s := testServer(t, outsideSQL(outsideDB(t), nil))

	// Someone the outside source knows, with no account here yet.
	c := login(t, s, "ann")
	c.send("NS IDENTIFY nope-not-this")
	c.expect(nsLine("ann", "Wrong password for ann."))
	if accountOf(t, s, "ann") != nil {
		t.Fatal("a wrong password made an account")
	}
	c.send("NS IDENTIFY " + pwOutside)
	c.expect(nsLine("ann", "Password accepted - you are now recognized."))
	c.expect(" 900 ann ")
	a := accountOf(t, s, "ann")
	if a == nil || a.PassHash != services.NoPassword || a.Email != "ann@example.org" {
		t.Fatalf("account after the first login: %+v", a)
	}

	// The same over SASL, before registration.
	if got := saslWith(t, s, "bob", "bob", pwOutside); got != "903" {
		t.Fatalf("SASL with the outside password: %s", got)
	}
	if got := saslWith(t, s, "bob2", "bob", "wrong-one"); got != "904" {
		t.Fatalf("SASL with a wrong password: %s", got)
	}
	// Nobody the source knows, and no account: as without [auth].
	if got := saslWith(t, s, "zed", "zed", pwOutside); got != "904" {
		t.Fatalf("SASL as someone unknown: %s", got)
	}
	if accountOf(t, s, "zed") != nil {
		t.Fatal("an unknown name got an account")
	}
	// No password matches the account's own (empty) hash.
	if got := saslWith(t, s, "ann2", "ann", services.NoPassword); got != "904" {
		t.Fatalf("SASL with the placeholder as the password: %s", got)
	}
}

func TestOutsideAndLocalPasswords(t *testing.T) {
	s := testServer(t, outsideSQL(outsideDB(t), nil))
	c := registered(t, s, "Bee") // a password set here
	c.send("QUIT")
	c.expect("ERROR :Closing Link")
	// Both the outside password and the one set here work.
	if got := saslWith(t, s, "Bee2", "Bee", pwOutside); got != "903" {
		t.Fatalf("outside password for a local account: %s", got)
	}
	if got := saslWith(t, s, "Bee3", "Bee", pw); got != "903" {
		t.Fatalf("local password with [auth] on: %s", got)
	}
	// A name only known here.
	d := registered(t, s, "Wasp")
	d.send("QUIT")
	d.expect("ERROR :Closing Link")
	if got := saslWith(t, s, "Wasp2", "Wasp", pw); got != "903" {
		t.Fatalf("local-only account: %s", got)
	}
}

func TestOutsideOnly(t *testing.T) {
	no := false
	s := testServer(t, outsideSQL(outsideDB(t), func(a *config.Auth) { a.LocalPasswords = &no }))
	c := login(t, s, "Wasp")
	c.send("NS REGISTER " + pw)
	c.expect(nsLine("Wasp", "Passwords on this network are kept somewhere else and can't be set here."))
	if accountOf(t, s, "Wasp") != nil {
		t.Fatal("REGISTER made an account with local passwords off")
	}
	b := login(t, s, "bob")
	b.send("NS IDENTIFY " + pwOutside)
	b.expect(nsLine("bob", "Password accepted - you are now recognized."))
}

func TestOutsideNoCreate(t *testing.T) {
	no := false
	s := testServer(t, outsideSQL(outsideDB(t), func(a *config.Auth) { a.Create = &no }))
	c := login(t, s, "ann")
	c.send("NS IDENTIFY " + pwOutside)
	c.expect(nsLine("ann", "ann isn't registered. Register it: /ns register <password> [email]"))
}

// Nobody can register here: the "register it" hint would send people
// to a command that refuses them.
func TestOutsideNoCreateNoLocalPasswords(t *testing.T) {
	no := false
	s := testServer(t, outsideSQL(outsideDB(t), func(a *config.Auth) { a.Create, a.LocalPasswords = &no, &no }))
	c := login(t, s, "ann")
	c.send("NS IDENTIFY " + pwOutside)
	c.expect(nsLine("ann", "ann has no account here. Ask an admin of this network."))
}

// /oper while the outside source is down says so, not "Password incorrect".
func TestOutsideOperDown(t *testing.T) {
	gone := filepath.ToSlash(filepath.Join(t.TempDir(), "no", "such", "dir", "users.db"))
	s, _, _ := fileServer(t, "\n[auth]\nsource = \"sql\"\nopers = true\n[auth.sql]\ndriver = \"sqlite\"\ndsn = \""+gone+"\"\nquery = \"SELECT password FROM users WHERE name = $name\"\n")
	b := registered(t, s, "Boss")
	b.send("OPER Boss " + pwOutside)
	b.expect(" 464 Boss :The login server can't be reached right now. Try again in a moment.")
}

func TestOutsideDown(t *testing.T) {
	// A database that can't be opened: the source is down.
	gone := filepath.Join(t.TempDir(), "no", "such", "dir", "users.db")
	s := testServer(t, outsideSQL(gone, nil))
	c := registered(t, s, "Bee")
	c.send("QUIT")
	c.expect("ERROR :Closing Link")
	// A password set here still works.
	if got := saslWith(t, s, "Bee2", "Bee", pw); got != "903" {
		t.Fatalf("local password while the source is down: %s", got)
	}
	a := login(t, s, "ann")
	a.send("NS IDENTIFY " + pwOutside)
	a.expect(nsLine("ann", "The login server can't be reached right now. Try again in a moment."))
	// Not counted as wrong passwords: no lock after many tries.
	for i := 0; i < 6; i++ {
		a.send("NS IDENTIFY " + pwOutside)
		a.expect(nsLine("ann", "The login server can't be reached right now. Try again in a moment."))
	}
}

// With [auth] opers the oper password is the outside one, and the first
// /oper never sets a password here.
func TestOutsideOperPassword(t *testing.T) {
	path := filepath.ToSlash(outsideDB(t))
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	hash, _ := bcrypt.GenerateFromPassword([]byte(pwOutside), bcrypt.MinCost)
	db.Exec("INSERT INTO users VALUES (?, ?, ?)", "Boss", string(hash), "")
	db.Close()
	s, _, _ := fileServer(t, "\n[auth]\nsource = \"sql\"\nopers = true\n[auth.sql]\ndriver = \"sqlite\"\ndsn = \""+path+"\"\nquery = \"SELECT password FROM users WHERE name = $name\"\n")
	b := registered(t, s, "Boss")
	b.send("OPER Boss some-other-password")
	b.expect(" 464 Boss ")
	b.quiet("Your oper password is set")
	b.send("OPER Boss " + pwOutside)
	b.expect(" 381 Boss :You are now an IRC operator")
}
