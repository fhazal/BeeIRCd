package geoip

import (
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"time"
)

// Info is what is known about an address.
type Info struct {
	Country string // ISO 3166 code, upper case ("NL"), "" = unknown
	Name    string // the country's English name
	ASN     uint32 // network number, 0 = unknown
	ASOrg   string // the network's owner
}

// Set is the country and ASN databases in use (either may be nil).
type Set struct {
	Country, ASN *DB
}

// Lookup finds ip in the databases; unknown parts stay empty.
func (s *Set) Lookup(ip string) Info {
	var out Info
	if s == nil {
		return out
	}
	a, err := netip.ParseAddr(ip)
	if err != nil {
		return out
	}
	if s.Country != nil {
		if r, _ := s.Country.Lookup(a); r != nil {
			c, _ := r["country"].(map[string]any)
			if c == nil {
				c, _ = r["registered_country"].(map[string]any)
			}
			if c != nil {
				out.Country, _ = c["iso_code"].(string)
				if names, ok := c["names"].(map[string]any); ok {
					out.Name, _ = names["en"].(string)
				}
			}
		}
	}
	if s.ASN != nil {
		if r, _ := s.ASN.Lookup(a); r != nil {
			out.ASN = uint32(num(r["autonomous_system_number"]))
			out.ASOrg, _ = r["autonomous_system_organization"].(string)
		}
	}
	return out
}

// Kinds are the two DB-IP Lite files: what Set field they fill.
var Kinds = []string{"country", "asn"}

// DBIPURL is where DB-IP Lite's file of kind for month ("2026-11") is.
var DBIPURL = func(kind, month string) string {
	return "https://download.db-ip.com/free/dbip-" + kind + "-lite-" + month + ".mmdb.gz"
}

// Download fetches the newest DB-IP Lite file of kind (this month's, or
// last month's early in the month), checks it opens, and puts it at
// path. Only a working file replaces the old one.
func Download(ctx context.Context, client *http.Client, kind, path string, now time.Time) (*DB, error) {
	var lastErr error
	for _, m := range []time.Time{now, now.AddDate(0, -1, 0)} {
		db, err := fetch(ctx, client, DBIPURL(kind, m.Format("2006-01")), path)
		if err == nil {
			return db, nil
		}
		lastErr = err
		if ctx.Err() != nil {
			break
		}
	}
	return nil, lastErr
}

// maxFile is the largest database we take (the Lite files are ~10 MB).
const maxFile = 256 << 20

func fetch(ctx context.Context, client *http.Client, url, path string) (*DB, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "BeeIRCd")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: %s", url, resp.Status)
	}
	zr, err := gzip.NewReader(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", url, err)
	}
	buf, err := io.ReadAll(io.LimitReader(zr, maxFile+1))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", url, err)
	}
	if len(buf) > maxFile {
		return nil, errors.New(url + ": file too large")
	}
	db, err := Parse(buf)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", url, err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	tmp := path + ".new"
	if err := os.WriteFile(tmp, buf, 0o644); err != nil {
		return nil, err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return nil, err
	}
	return db, nil
}

// Stale reports whether a database built at built (seconds since 1970)
// should be downloaded again: older than 35 days.
func Stale(db *DB, now time.Time) bool {
	return db == nil || now.Sub(time.Unix(int64(db.Built), 0)) > 35*24*time.Hour
}
