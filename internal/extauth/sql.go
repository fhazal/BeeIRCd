package extauth

import (
	"context"
	"crypto/md5"
	"crypto/sha256"
	"crypto/sha512"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	_ "github.com/go-sql-driver/mysql"
	_ "github.com/lib/pq"
	"golang.org/x/crypto/bcrypt"
	_ "modernc.org/sqlite"

	"github.com/fhazal/beeircd/internal/config"
	"github.com/fhazal/beeircd/internal/passwd"
)

type sqlSource struct {
	db    *sql.DB
	hash  string
	query stmt
	oper  stmt
	log   stmt
}

// stmt is a query with its $name-style values in the order the driver
// wants them.
type stmt struct {
	text string
	args []string
}

var placeholder = regexp.MustCompile(`\$(name|password|ip|result|time)\b`)

// prepare swaps $name, $password, $ip, $result and $time for the driver's
// own placeholders. The values travel apart from the query text, so
// nothing a user types can change the query.
func prepare(driver, q string) stmt {
	var st stmt
	st.text = placeholder.ReplaceAllStringFunc(q, func(m string) string {
		st.args = append(st.args, m[1:])
		if driver == "postgres" {
			return "$" + strconv.Itoa(len(st.args))
		}
		return "?"
	})
	return st
}

func (st stmt) values(q Query, result string) []any {
	out := make([]any, len(st.args))
	for i, a := range st.args {
		switch a {
		case "name":
			out[i] = q.Name
		case "password":
			out[i] = q.Password
		case "ip":
			out[i] = q.IP
		case "result":
			out[i] = result
		case "time":
			out[i] = time.Now().Unix()
		}
	}
	return out
}

func newSQL(cfg config.AuthSQL, opers bool) (*sqlSource, error) {
	db, err := sql.Open(cfg.Driver, cfg.DSN)
	if err != nil {
		return nil, fmt.Errorf("[auth.sql] %w", err)
	}
	db.SetMaxOpenConns(4)
	db.SetMaxIdleConns(2)
	db.SetConnMaxLifetime(5 * time.Minute)
	s := &sqlSource{db: db, hash: cfg.HashOr(), query: prepare(cfg.Driver, cfg.Query)}
	if opers && cfg.OperQuery != "" {
		s.oper = prepare(cfg.Driver, cfg.OperQuery)
	}
	if cfg.LogQuery != "" {
		s.log = prepare(cfg.Driver, cfg.LogQuery)
	}
	return s, nil
}

func (s *sqlSource) Close() { s.db.Close() }

func (s *sqlSource) Check(ctx context.Context, q Query) Answer {
	a := s.check(ctx, q)
	if s.log.text != "" && a.Result != Down {
		// The log must never keep a password.
		logged := q
		logged.Password = ""
		if _, err := s.db.ExecContext(ctx, s.log.text, s.log.values(logged, a.Result.String())...); err != nil && a.Err == nil {
			a.Err = fmt.Errorf("log_query: %w", err)
		}
	}
	return a
}

func (s *sqlSource) check(ctx context.Context, q Query) Answer {
	if q.Password == "" {
		return Answer{Result: Wrong}
	}
	st := s.query
	if q.Oper && s.oper.text != "" {
		st = s.oper
	}
	rows, err := s.db.QueryContext(ctx, st.text, st.values(q, "")...)
	if err != nil {
		return Answer{Result: Down, Err: err}
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return Answer{Result: Down, Err: err}
		}
		if s.hash == "query" {
			return Answer{Result: Wrong} // the database compared, and said no
		}
		return Answer{Result: Unknown}
	}
	cols, err := rows.Columns()
	if err != nil || len(cols) == 0 {
		return Answer{Result: Down, Err: errors.New("the query gives no column")}
	}
	vals := make([]sql.NullString, len(cols))
	ptrs := make([]any, len(cols))
	for i := range vals {
		ptrs[i] = &vals[i]
	}
	if err := rows.Scan(ptrs...); err != nil {
		return Answer{Result: Down, Err: err}
	}
	email := ""
	if len(vals) > 1 {
		email = strings.TrimSpace(vals[1].String)
	}
	if s.hash == "query" {
		return Answer{Result: OK, Email: email}
	}
	if !vals[0].Valid || vals[0].String == "" {
		return Answer{Result: Wrong}
	}
	if checkHash(s.hash, q.Password, strings.TrimSpace(vals[0].String)) {
		return Answer{Result: OK, Email: email}
	}
	return Answer{Result: Wrong}
}

// checkHash compares pw with what the database keeps, in the format kind
// names. "auto" reads bcrypt and argon2id by their prefix and refuses the
// rest: plain or unsalted passwords are only compared when asked for.
func checkHash(kind, pw, stored string) bool {
	switch kind {
	case "auto":
		switch {
		case strings.HasPrefix(stored, "$2a$"), strings.HasPrefix(stored, "$2b$"), strings.HasPrefix(stored, "$2y$"):
			return checkHash("bcrypt", pw, stored)
		case strings.HasPrefix(stored, "$argon2id$"):
			return checkHash("argon2", pw, stored)
		}
		return false
	case "bcrypt":
		if strings.HasPrefix(stored, "$2y$") { // PHP's name for the same thing
			stored = "$2a$" + stored[4:]
		}
		return bcrypt.CompareHashAndPassword([]byte(stored), []byte(pw)) == nil
	case "argon2":
		return strings.HasPrefix(stored, "$argon2id$") && passwd.Check(pw, stored)
	case "sha256":
		sum := sha256.Sum256([]byte(pw))
		return sameHex(stored, sum[:])
	case "sha512":
		sum := sha512.Sum512([]byte(pw))
		return sameHex(stored, sum[:])
	case "md5":
		sum := md5.Sum([]byte(pw))
		return sameHex(stored, sum[:])
	case "plain":
		return subtle.ConstantTimeCompare([]byte(stored), []byte(pw)) == 1
	}
	return false
}

func sameHex(stored string, sum []byte) bool {
	want, err := hex.DecodeString(stored)
	return err == nil && subtle.ConstantTimeCompare(want, sum) == 1
}
