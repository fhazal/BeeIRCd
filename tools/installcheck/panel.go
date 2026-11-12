package main

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"strings"
	"time"
)

// panelCheck signs in to the Hive Panel at addr as the admin and walks
// the Backups card: list, Back up now, a wrong password refused, then a
// real download that must be a .tar.gz with beeircd.conf in it. This is
// the part only a real install tests: beeircd-web, locked down by
// systemd, reading the backup folder the server wrote.
func panelCheck(addr, admin, pass string) int {
	jar, _ := cookiejar.New(nil)
	cl := &http.Client{Jar: jar, Timeout: 60 * time.Second}
	base := "http://" + addr
	call := func(method, path, body string) (*http.Response, []byte, error) {
		req, err := http.NewRequest(method, base+path, strings.NewReader(body))
		if err != nil {
			return nil, nil, err
		}
		if method == http.MethodPost {
			req.Header.Set("X-Hive", "1")
			req.Header.Set("Content-Type", "application/json")
		}
		res, err := cl.Do(req)
		if err != nil {
			return nil, nil, err
		}
		defer res.Body.Close()
		b, err := io.ReadAll(res.Body)
		return res, b, err
	}
	fails := 0
	check := func(name string, err error) bool {
		if err != nil {
			fails++
			fmt.Printf("FAIL %s: %v\n", name, err)
			return false
		}
		fmt.Printf("OK   %s\n", name)
		return true
	}
	want := func(res *http.Response, b []byte, err error, code int) error {
		if err != nil {
			return err
		}
		if res.StatusCode != code {
			return fmt.Errorf("status %d, want %d: %s", res.StatusCode, code, strings.TrimSpace(string(b)))
		}
		return nil
	}
	js, _ := json.Marshal(map[string]string{"nick": admin, "password": pass})
	res, b, err := call("POST", "/api/login", string(js))
	if !check("panel sign-in as "+admin, want(res, b, err, 200)) {
		return 1
	}
	var list struct {
		Backups struct {
			Dir   string
			Files []struct {
				Name  string
				Bytes int64
				By    string
			}
		}
	}
	res, b, err = call("POST", "/api/backup", "")
	check("panel Back up now", want(res, b, err, 200))
	res, b, err = call("GET", "/api/backups", "")
	if err = want(res, b, err, 200); err == nil {
		err = json.Unmarshal(b, &list)
	}
	if err == nil && (len(list.Backups.Files) == 0 || list.Backups.Files[0].By != admin) {
		err = fmt.Errorf("newest backup not marked as made by %s: %s", admin, b)
	}
	if !check(fmt.Sprintf("panel lists %d backup(s) in %s", len(list.Backups.Files), list.Backups.Dir), err) {
		return 1
	}
	newest := list.Backups.Files[0]
	get := func(pw string) (*http.Response, []byte, error) {
		js, _ := json.Marshal(map[string]string{"name": newest.Name, "password": pw})
		return call("POST", "/api/backup-get", string(js))
	}
	res, b, err = get("wrong-password")
	if err = want(res, b, err, 422); err == nil && !strings.Contains(string(b), "Wrong password.") {
		err = fmt.Errorf("answer %s", b)
	}
	check("panel download with a wrong password refused", err)
	res, b, err = get(pass)
	if err = want(res, b, err, 200); err == nil {
		err = tarHas(b, res.Header.Get("Content-Type"), newest.Bytes)
	}
	check(fmt.Sprintf("panel download %s (%d bytes, beeircd.conf + services.db inside)", newest.Name, len(b)), err)
	if fails > 0 {
		fmt.Printf("%d panel check(s) failed\n", fails)
		return 1
	}
	fmt.Println("all panel checks passed")
	return 0
}

// tarHas checks a downloaded backup: the right type and size, and a
// readable .tar.gz holding the config and the services database.
func tarHas(b []byte, ctype string, size int64) error {
	if ctype != "application/gzip" {
		return fmt.Errorf("content type %q", ctype)
	}
	if int64(len(b)) != size {
		return fmt.Errorf("%d bytes, the list said %d", len(b), size)
	}
	zr, err := gzip.NewReader(strings.NewReader(string(b)))
	if err != nil {
		return err
	}
	tr := tar.NewReader(zr)
	seen := map[string]bool{}
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		if _, err := io.Copy(io.Discard, tr); err != nil {
			return err
		}
		seen[h.Name] = true
	}
	for _, n := range []string{"beeircd.conf", "services.db"} {
		if !seen[n] {
			return fmt.Errorf("%s missing from the backup (has %v)", n, seen)
		}
	}
	return nil
}
