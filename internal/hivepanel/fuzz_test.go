package hivepanel

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/fhazal/beeircd/internal/control"
)

// FuzzPanel throws random requests at the panel: no panic, no 500, and a
// POST without the X-Hive header never reaches the server.
func FuzzPanel(f *testing.F) {
	for _, s := range []string{"/api/login", "/api/set", "/api/people?q=x&offset=-5", "/api/ban-add", "/api/oper-save", "/api/link-save", "/fonts/../app.js", "/api/live", "/"} {
		f.Add("POST", s, `{"name":"x","value":"1"}`, true, true)
		f.Add("GET", s, "", false, true)
	}
	f.Add("POST", "/api/oper-save", `{"name":`, true, true)
	f.Add("PUT", "/api/set", `[]`, true, false)
	f.Fuzz(func(t *testing.T, method, path, body string, hive, signedIn bool) {
		if !strings.HasPrefix(path, "/") || strings.ContainsAny(path, " \r\n\t#") || len(path) > 2000 || strings.Contains(path, "/api/live") {
			return
		}
		if method == "" || strings.ContainsAny(method, " \r\n\t()<>@,;:\\\"/[]?={}") {
			return
		}
		fs := &fakeServer{}
		asked := 0
		p := New(Options{Ask: func(r control.Request, d time.Duration) (*control.Response, error) {
			if r.Cmd != control.CmdPanelLogin {
				asked++
			}
			return fs.ask(r, d)
		}, Idle: time.Hour})
		cookie := ""
		if signedIn {
			r := httptest.NewRequest("POST", "http://127.0.0.1:8787/api/login", strings.NewReader(`{"nick":"Bee","password":"pw"}`))
			r.RemoteAddr = "127.0.0.1:1"
			r.Header.Set("X-Hive", "1")
			w := httptest.NewRecorder()
			p.ServeHTTP(w, r)
			for _, c := range w.Result().Cookies() {
				cookie = c.Value
			}
		}
		asked = 0
		r, err := http.NewRequest(method, "http://127.0.0.1:8787"+path, strings.NewReader(body))
		if err != nil {
			return
		}
		r.RemoteAddr = "127.0.0.1:2"
		if hive {
			r.Header.Set("X-Hive", "1")
		}
		if cookie != "" {
			r.AddCookie(&http.Cookie{Name: cookieName, Value: cookie})
		}
		w := httptest.NewRecorder()
		p.ServeHTTP(w, r)
		if w.Code >= 500 && w.Code != http.StatusServiceUnavailable && w.Code != http.StatusBadGateway {
			t.Fatalf("%s %s %q: %d %s", method, path, body, w.Code, w.Body)
		}
		if method == "POST" && !hive && asked > 0 {
			t.Fatalf("POST without X-Hive reached the server: %s", path)
		}
	})
}
