package extauth

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/fhazal/beeircd/internal/config"
)

// LDAP result codes used here.
const (
	ldapSuccess            = 0
	ldapNoSuchObject       = 32
	ldapInvalidCredentials = 49
)

type ldapSource struct {
	cfg  config.AuthLDAP
	addr string
	tls  *tls.Config // nil for plain ldap:// without starttls
	ldps bool
}

func newLDAP(cfg config.AuthLDAP) (*ldapSource, error) {
	u, err := url.Parse(cfg.URL)
	if err != nil {
		return nil, err
	}
	l := &ldapSource{cfg: cfg, ldps: u.Scheme == "ldaps"}
	port := u.Port()
	if port == "" {
		port = "389"
		if l.ldps {
			port = "636"
		}
	}
	l.addr = net.JoinHostPort(u.Hostname(), port)
	if l.ldps || cfg.StartTLS {
		l.tls = &tls.Config{ServerName: u.Hostname(), MinVersion: tls.VersionTLS12, InsecureSkipVerify: !cfg.Verifies()}
		if cfg.CAFile != "" {
			pem, err := os.ReadFile(cfg.CAFile)
			if err != nil {
				return nil, fmt.Errorf("[auth.ldap] ca_file: %w", err)
			}
			pool := x509.NewCertPool()
			if !pool.AppendCertsFromPEM(pem) {
				return nil, fmt.Errorf("[auth.ldap] ca_file %s holds no certificate", cfg.CAFile)
			}
			l.tls.RootCAs = pool
		}
	}
	return l, nil
}

func (l *ldapSource) Close() {}

// ldapConn is one connection to the directory.
type ldapConn struct {
	c   net.Conn
	r   *bufio.Reader
	msg int
}

func (l *ldapSource) dial(ctx context.Context) (*ldapConn, error) {
	d := net.Dialer{}
	c, err := d.DialContext(ctx, "tcp", l.addr)
	if err != nil {
		return nil, err
	}
	if dl, ok := ctx.Deadline(); ok {
		c.SetDeadline(dl)
	} else {
		c.SetDeadline(time.Now().Add(10 * time.Second))
	}
	lc := &ldapConn{c: c, r: bufio.NewReader(c)}
	if l.ldps {
		t := tls.Client(c, l.tls)
		if err := t.HandshakeContext(ctx); err != nil {
			c.Close()
			return nil, err
		}
		lc.c, lc.r = t, bufio.NewReader(t)
	} else if l.cfg.StartTLS {
		// RFC 4511 4.14: the StartTLS extended operation.
		resp, err := lc.round(tlv(appExtendedReq, berString(0x80, "1.3.6.1.4.1.1466.20037")), appExtendedResp)
		if err != nil {
			c.Close()
			return nil, err
		}
		if code, text := resultOf(resp); code != ldapSuccess {
			c.Close()
			return nil, fmt.Errorf("ldap: StartTLS refused (%d) %s", code, text)
		}
		t := tls.Client(c, l.tls)
		if err := t.HandshakeContext(ctx); err != nil {
			c.Close()
			return nil, err
		}
		lc.c, lc.r = t, bufio.NewReader(t)
	}
	return lc, nil
}

func (lc *ldapConn) close() {
	lc.msg++
	lc.c.Write(tlv(tagSequence, berInt(tagInt, lc.msg), tlv(appUnbindRequest)))
	lc.c.Close()
}

func (lc *ldapConn) send(op []byte) error {
	lc.msg++
	_, err := lc.c.Write(tlv(tagSequence, berInt(tagInt, lc.msg), op))
	return err
}

// next reads the next answer to the last request.
func (lc *ldapConn) next() (element, error) {
	for {
		e, err := readElement(lc.r)
		if err != nil {
			return element{}, err
		}
		if e.tag != tagSequence {
			return element{}, errors.New("ldap: unexpected answer")
		}
		kids, err := children(e.body)
		if err != nil || len(kids) < 2 {
			return element{}, errors.New("ldap: unexpected answer")
		}
		if id := kids[0].int(); id != lc.msg {
			if id == 0 { // a notice of disconnection
				return element{}, errors.New("ldap: the directory closed the connection")
			}
			continue
		}
		return kids[1], nil
	}
}

// round sends op and reads the one answer of kind want.
func (lc *ldapConn) round(op []byte, want byte) (element, error) {
	if err := lc.send(op); err != nil {
		return element{}, err
	}
	e, err := lc.next()
	if err != nil {
		return element{}, err
	}
	if e.tag != want {
		return element{}, fmt.Errorf("ldap: unexpected answer 0x%02x", e.tag)
	}
	return e, nil
}

// resultOf reads the result code and message of an LDAPResult.
func resultOf(e element) (int, string) {
	kids, err := children(e.body)
	if err != nil || len(kids) < 3 {
		return -1, "unreadable answer"
	}
	return kids[0].int(), string(kids[2].body)
}

// bind does a simple bind and returns the result code.
func (lc *ldapConn) bind(dn, pw string) (int, string, error) {
	e, err := lc.round(tlv(appBindRequest, berInt(tagInt, 3), berString(tagOctets, dn), berString(ctxSimplePassword, pw)), appBindResponse)
	if err != nil {
		return 0, "", err
	}
	code, text := resultOf(e)
	return code, text, nil
}

type ldapEntry struct {
	dn    string
	attrs map[string]string // lower-case name -> first value
}

// search finds up to two entries under base matching filter.
func (lc *ldapConn) search(base, filter string, attrs ...string) ([]ldapEntry, error) {
	f, err := encodeFilter(filter)
	if err != nil {
		return nil, err
	}
	var want [][]byte
	for _, a := range attrs {
		want = append(want, berString(tagOctets, a))
	}
	if len(want) == 0 {
		want = append(want, berString(tagOctets, "1.1")) // no attributes
	}
	req := tlv(appSearchRequest,
		berString(tagOctets, base),
		berInt(tagEnum, 2), // the whole subtree
		berInt(tagEnum, 0), // never follow aliases
		berInt(tagInt, 2),  // two entries tell "one" from "several"
		berInt(tagInt, 10), // seconds
		berBool(false),
		f,
		tlv(tagSequence, want...))
	if err := lc.send(req); err != nil {
		return nil, err
	}
	var out []ldapEntry
	for {
		e, err := lc.next()
		if err != nil {
			return nil, err
		}
		switch e.tag {
		case appSearchEntry:
			kids, err := children(e.body)
			if err != nil || len(kids) < 1 {
				return nil, errors.New("ldap: unreadable entry")
			}
			ent := ldapEntry{dn: string(kids[0].body), attrs: map[string]string{}}
			if len(kids) > 1 {
				list, _ := children(kids[1].body)
				for _, a := range list {
					p, _ := children(a.body)
					if len(p) < 2 {
						continue
					}
					vals, _ := children(p[1].body)
					if len(vals) > 0 {
						ent.attrs[strings.ToLower(string(p[0].body))] = string(vals[0].body)
					}
				}
			}
			out = append(out, ent)
		case appSearchDone:
			code, text := resultOf(e)
			// 4 = more entries than asked for: the two we have are enough.
			if code != ldapSuccess && code != 4 && code != ldapNoSuchObject {
				return nil, fmt.Errorf("ldap: search failed (%d) %s", code, text)
			}
			return out, nil
		}
	}
}

// Check asks the directory whether pw is name's password.
func (l *ldapSource) Check(ctx context.Context, q Query) Answer {
	if q.Password == "" {
		// A bind with an empty password is an anonymous bind and
		// "succeeds" for anyone.
		return Answer{Result: Wrong}
	}
	lc, err := l.dial(ctx)
	if err != nil {
		return Answer{Result: Down, Err: err}
	}
	defer lc.close()

	filter := l.cfg.FilterOr()
	if q.Oper && l.cfg.OperFilter != "" {
		filter = l.cfg.OperFilter
	}
	search := l.cfg.UserDN == "" || (q.Oper && l.cfg.OperFilter != "")
	dn, email := "", ""
	if search || l.cfg.BaseDN != "" {
		if l.cfg.BindDN != "" {
			code, text, err := lc.bind(l.cfg.BindDN, l.cfg.BindPassword)
			if err != nil {
				return Answer{Result: Down, Err: err}
			}
			if code != ldapSuccess {
				return Answer{Result: Down, Err: fmt.Errorf("ldap: bind_dn was refused (%d) %s", code, text)}
			}
		}
	}
	if search {
		ents, err := lc.search(l.cfg.BaseDN, strings.ReplaceAll(filter, "%s", escapeFilter(q.Name)), l.cfg.EmailAttrOr())
		if err != nil {
			return Answer{Result: Down, Err: err}
		}
		switch len(ents) {
		case 0:
			return Answer{Result: Unknown}
		case 1:
			dn, email = ents[0].dn, ents[0].attrs[strings.ToLower(l.cfg.EmailAttrOr())]
		default:
			return Answer{Result: Down, Err: fmt.Errorf("ldap: more than one entry matches %q", q.Name)}
		}
	}
	if l.cfg.UserDN != "" && dn == "" {
		dn = strings.Replace(l.cfg.UserDN, "%s", escapeDN(q.Name), 1)
	}
	if dn == "" {
		return Answer{Result: Unknown}
	}
	code, text, err := lc.bind(dn, q.Password)
	if err != nil {
		return Answer{Result: Down, Err: err}
	}
	switch code {
	case ldapSuccess:
		return Answer{Result: OK, Email: email}
	case ldapInvalidCredentials:
		return Answer{Result: Wrong}
	case ldapNoSuchObject:
		return Answer{Result: Unknown}
	}
	return Answer{Result: Down, Err: fmt.Errorf("ldap: login as %s failed (%d) %s", dn, code, text)}
}
