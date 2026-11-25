package services

import (
	"errors"
	"hash/fnv"
	"math"
	"reflect"
	"time"

	"github.com/fhazal/beeircd/internal/extauth"
	"github.com/fhazal/beeircd/internal/irc"
	"github.com/fhazal/beeircd/internal/store"
)

// Outside logins ([auth]): passwords kept in an LDAP directory or an SQL
// database. The outside source is asked first; a password set here still
// works unless local_passwords is off.

// NoPassword is the password hash of an account that only the outside
// source can log in to. No password matches it.
const NoPassword = "*"

// outside is the source [auth] names, opened again after a rehash changed
// it; nil when off or when it could not be opened.
func (s *Services) outside() extauth.Source {
	if !reflect.DeepEqual(s.cfg.Auth, s.extCfg) {
		if s.ext != nil {
			s.ext.Close()
		}
		s.ext, s.extCfg = nil, s.cfg.Auth
		src, err := extauth.New(s.cfg.Auth)
		if err != nil {
			s.log.Printf("outside logins: %v", err)
		}
		s.ext = src
	}
	return s.ext
}

// CloseOutside lets go of the outside source (when the server stops).
func (s *Services) CloseOutside() {
	if s.ext != nil {
		s.ext.Close()
		s.ext = nil
	}
}

// LocalPasswords reports whether passwords can be set here.
func (s *Services) LocalPasswords() bool {
	return !s.cfg.Auth.On() || s.cfg.Auth.LocalToo()
}

// CheckLogin checks pw for account a like CheckSecret, asking the outside
// source first when there is one.
func (s *Services) CheckLogin(busy *bool, gone func() bool, a *store.Account, src, pw string, result func(PwResult)) {
	s.check(busy, gone, a.ID, src, a.Name, false, a.PassHash, pw, func(r PwResult, _ string) { result(r) })
}

// CheckOper checks an oper password like CheckSecret; with [auth] opers
// the outside source is asked first, under the oper's account name.
func (s *Services) CheckOper(busy *bool, gone func() bool, key int64, src, name, hash, pw string, result func(PwResult)) {
	if !s.cfg.Auth.Opers {
		name = ""
	}
	s.check(busy, gone, key, src, name, true, hash, pw, func(r PwResult, _ string) { result(r) })
}

// OutsideCreates reports whether a login as name, which has no account
// here, may be asked from the outside source and make the account. Not
// while READONLY is on: that would store a new account.
func (s *Services) OutsideCreates(name string) bool {
	return !s.off && !s.readonly && s.cfg.Auth.On() && s.cfg.Auth.Creates() && s.DefconLevel() > 4 &&
		irc.ValidNick(name, s.cfg.Limits.NickLen) && s.registerForbidden(name) == ""
}

// outsideKey is the wrong-password lock of a name that has no account
// yet. Account IDs are positive and oper locks small negatives; these sit
// far below both.
func outsideKey(name string) int64 {
	h := fnv.New32a()
	h.Write([]byte(irc.Fold(name)))
	return math.MinInt64/2 - int64(h.Sum32())
}

// OutsideLogin checks pw for name, which has no account here, against the
// outside source. When it is right the account is made and handed to
// result. Callers ask OutsideCreates first.
func (s *Services) OutsideLogin(busy *bool, gone func() bool, name, src, pw string, result func(*store.Account, PwResult)) {
	s.check(busy, gone, outsideKey(name), src, name, false, "", pw, func(r PwResult, email string) {
		if r != pwOK {
			result(nil, r)
			return
		}
		if !validEmail(email) || s.emailForbidden(email) != "" {
			email = ""
		}
		a, err := s.store.CreateAccount(name, NoPassword, email, time.Now())
		if errors.Is(err, store.ErrExists) {
			a, err = s.store.AccountByNick(name)
		}
		if err != nil || a == nil {
			if err != nil {
				s.log.Printf("services database: %v", err)
			}
			result(nil, pwDown)
			return
		}
		if n := s.cfg.Services.MaxMemos; n != store.DefaultMemoLimit && a.PassHash == NoPassword && a.MemoLimit != n {
			a.MemoLimit = n
			s.store.SaveAccount(a)
		}
		s.log.Printf("registered nick %s (%s): first login from the outside source", a.Name, src)
		result(a, pwOK)
	})
}
