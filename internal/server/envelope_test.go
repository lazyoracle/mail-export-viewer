package server

import (
	"bytes"
	"errors"
	"io/fs"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
	"time"
)

const (
	wantUICSP = "default-src 'self'; img-src 'self' data:; style-src 'self' 'unsafe-inline'; " +
		"object-src 'none'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'"
	wantBodyCSP = "default-src 'none'; img-src 'self' data:; style-src 'unsafe-inline'"
)

func TestHostCheck(t *testing.T) {
	f := newFixture(t)
	for _, host := range []string{"localhost", "localhost:8080", "127.0.0.1", "127.0.0.1:1234", "[::1]", "[::1]:80", "LOCALHOST:9"} {
		for _, p := range []string{"/api/status", "/"} {
			if rec := serve(f.h, "GET", p, host); rec.Code != http.StatusOK {
				t.Errorf("host %q %s: status %d", host, p, rec.Code)
			}
		}
	}
	for _, host := range []string{"", "evil.example", "evil.example:8080", "localhost.evil.example", "127.0.0.1.nip.io",
		"0.0.0.0:8080", "192.168.1.5", "[::2]", "::1", "localhost:abc", "localhost:", "127.0.0.2"} {
		for _, p := range []string{"/api/status", "/", "/app.js"} {
			f.wantError(serve(f.h, "GET", p, host), http.StatusForbidden)
		}
	}
}

func TestAllowedHosts(t *testing.T) {
	f := newFixture(t, func(c *Config) { c.AllowedHosts = []string{"MyBox.lan", "10.0.0.5:80", "fe80::1", "[fe80::2]"} })
	for _, host := range []string{"mybox.lan", "mybox.lan:8080", "10.0.0.5", "10.0.0.5:80", "[fe80::1]:8080", "[fe80::2]", "localhost:1"} {
		if rec := serve(f.h, "GET", "/api/status", host); rec.Code != 200 {
			t.Errorf("host %q: %d", host, rec.Code)
		}
	}
	for _, host := range []string{"evil.example", "mybox.lan.evil", "10.0.0.6"} {
		if rec := serve(f.h, "GET", "/", host); rec.Code != http.StatusForbidden {
			t.Errorf("host %q: %d", host, rec.Code)
		}
	}
}

func TestOnlyGetAndHead(t *testing.T) {
	f := newFixture(t)
	id := f.id("Plain hello")
	for _, m := range []string{"POST", "PUT", "DELETE", "PATCH", "OPTIONS", "TRACE", "CONNECT", "PROPFIND"} {
		for _, p := range []string{"/api/status", "/api/messages/" + itoa(id), "/api/nope", "/", "/app.js"} {
			rec := f.do(m, p)
			f.wantError(rec, http.StatusMethodNotAllowed)
			if a := rec.Header().Get("Allow"); a != "GET, HEAD" {
				t.Errorf("%s %s: Allow %q", m, p, a)
			}
		}
	}
	for _, p := range []string{"/api/status", "/"} {
		if rec := f.do("HEAD", p); rec.Code != http.StatusOK {
			t.Errorf("HEAD %s: %d", p, rec.Code)
		}
	}
}

func TestSecurityHeadersEverywhere(t *testing.T) {
	f := newFixture(t)
	id := f.id("Fancy newsletter")
	paths := []string{"/", "/app.js", "/missing", "/api/status", "/api/facets", "/api/messages", "/api/nope",
		"/api/messages/" + itoa(id), "/api/messages/" + itoa(id) + "/raw", "/api/messages/" + itoa(id) + "/attachments/1"}
	for _, p := range paths {
		req := httptest.NewRequest("GET", p, nil)
		req.Host = "localhost"
		req.Header.Set("Origin", "http://evil.example")
		req.Header.Set("Access-Control-Request-Method", "GET")
		rec := httptest.NewRecorder()
		f.h.ServeHTTP(rec, req)
		for k, v := range map[string]string{
			"X-Content-Type-Options":       "nosniff",
			"Referrer-Policy":              "no-referrer",
			"X-Frame-Options":              "DENY",
			"Cross-Origin-Opener-Policy":   "same-origin",
			"Cross-Origin-Resource-Policy": "same-origin",
		} {
			if got := rec.Header().Get(k); got != v {
				t.Errorf("%s: %s = %q, want %q", p, k, got, v)
			}
		}
		for k := range rec.Header() {
			if strings.HasPrefix(strings.ToLower(k), "access-control-") {
				t.Errorf("%s: CORS header %s sent", p, k)
			}
		}
		if len(rec.Header().Values("Content-Security-Policy")) == 0 {
			t.Errorf("%s: no CSP", p)
		}
	}
}

// TestCSPExact pins the policies documented in the security model.
func TestCSPExact(t *testing.T) {
	f := newFixture(t)
	id := itoa(f.id("Fancy newsletter"))
	for p, want := range map[string][]string{
		"/":                                      {wantUICSP},
		"/app.js":                                {wantUICSP},
		"/api/status":                            {wantBodyCSP},
		"/api/messages":                          {wantBodyCSP},
		"/api/messages/" + id:                    {wantBodyCSP},
		"/api/messages/" + id + "/raw":           {wantBodyCSP, "sandbox"},
		"/api/messages/" + id + "/attachments/0": {wantBodyCSP, "sandbox"},
		"/api/messages/" + id + "/cid/logo@example.com": {wantBodyCSP, "sandbox"},
	} {
		rec := f.do("GET", p)
		if rec.Code != 200 {
			t.Errorf("%s: status %d", p, rec.Code)
			continue
		}
		if got := rec.Header().Values("Content-Security-Policy"); strings.Join(got, "|") != strings.Join(want, "|") {
			t.Errorf("%s: CSP %q, want %q", p, got, want)
		}
	}
}

func TestStaticServing(t *testing.T) {
	f := newFixture(t)
	rec := f.do("GET", "/")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "<title>ui</title>") ||
		!strings.HasPrefix(rec.Header().Get("Content-Type"), "text/html") {
		t.Fatalf("index: %d %q %s", rec.Code, rec.Header().Get("Content-Type"), rec.Body)
	}
	if rec.Header().Get("Cache-Control") != "no-cache" {
		t.Errorf("index Cache-Control %q", rec.Header().Get("Cache-Control"))
	}
	for p, ct := range map[string]string{"/app.js": "text/javascript; charset=utf-8", "/css/style.css": "text/css; charset=utf-8",
		"/img/sub/logo.svg": "image/svg+xml", "/index.html": "text/html; charset=utf-8"} {
		rec := f.do("GET", p)
		if rec.Code != 200 || rec.Header().Get("Content-Type") != ct {
			t.Errorf("%s: %d %q", p, rec.Code, rec.Header().Get("Content-Type"))
		}
	}
	// No directory listings, no hidden files, nothing outside the FS.
	for _, p := range []string{"/css/", "/css", "/img/", "/img/sub/", "/.hidden", "/missing.js", "/api.js/x"} {
		rec := f.do("GET", p)
		if rec.Code != http.StatusNotFound || strings.Contains(rec.Body.String(), "style.css") ||
			strings.Contains(rec.Body.String(), "secret") {
			t.Errorf("%s: %d %s", p, rec.Code, rec.Body)
		}
		f.wantError(rec, http.StatusNotFound)
	}
	for _, p := range []string{"/../index.html", "/%2e%2e/index.html", "/a%5c..%5capp.js", "/app.js%00", "/css//style.css"} {
		rec := f.do("GET", p)
		if rec.Code != http.StatusBadRequest && rec.Code != http.StatusNotFound {
			t.Errorf("%s: status %d", p, rec.Code)
		}
		if rec.Code == 200 || strings.Contains(rec.Body.String(), "console.log") {
			t.Errorf("%s served content: %s", p, rec.Body)
		}
	}
	// Without a UI only the API is served.
	g := newFixture(t, func(c *Config) { c.UI = nil })
	g.wantError(g.do("GET", "/"), http.StatusNotFound)
	if rec := g.do("GET", "/api/status"); rec.Code != 200 {
		t.Errorf("api without UI: %d", rec.Code)
	}
}

func TestQueryAndPathLimits(t *testing.T) {
	f := newFixture(t)
	f.wantError(f.do("GET", "/api/messages?q="+strings.Repeat("a", maxRawQuery+1)), http.StatusRequestURITooLong)
	f.wantError(f.do("GET", "/"+strings.Repeat("a", maxPathLen+1)), http.StatusRequestURITooLong)
	f.wantError(f.do("GET", "/api/messages?q=%zz"), http.StatusBadRequest)
}

func TestPanicRecovery(t *testing.T) {
	f := newFixture(t)
	var logs bytes.Buffer
	f.srv.log = log.New(&logs, "", 0)
	h := f.srv.envelope(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		panic("boom at " + f.dir)
	}))
	rec := serve(h, "GET", "/api/status", "localhost")
	f.wantError(rec, http.StatusInternalServerError)
	if !strings.Contains(logs.String(), "boom") {
		t.Errorf("panic not logged: %q", logs.String())
	}
	// http.ErrAbortHandler is re-raised so net/http aborts the connection.
	h = f.srv.envelope(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { panic(http.ErrAbortHandler) }))
	func() {
		defer func() {
			if r := recover(); r != http.ErrAbortHandler {
				t.Errorf("recovered %v, want ErrAbortHandler", r)
			}
		}()
		serve(h, "GET", "/", "localhost")
	}()
}

func TestHTTPServerLimits(t *testing.T) {
	f := newFixture(t)
	hs := f.srv.HTTPServer()
	if hs.Handler == nil || hs.ReadHeaderTimeout <= 0 || hs.ReadTimeout <= 0 || hs.WriteTimeout <= 0 ||
		hs.IdleTimeout <= 0 || hs.MaxHeaderBytes <= 0 || hs.MaxHeaderBytes > 1<<20 {
		t.Fatalf("server limits: %+v", hs)
	}
	if hs.ErrorLog == nil {
		t.Error("no ErrorLog")
	}
}

func TestListenLoopbackOnly(t *testing.T) {
	for _, addr := range []string{"127.0.0.1:0", "localhost:0"} {
		l, err := Listen(addr, false)
		if err != nil {
			t.Errorf("Listen(%q): %v", addr, err)
			continue
		}
		if ip := l.Addr().(*net.TCPAddr).IP; !ip.IsLoopback() {
			t.Errorf("Listen(%q) bound %v", addr, ip)
		}
		l.Close()
	}
	if l, err := Listen("[::1]:0", false); err == nil {
		l.Close()
	} else if !errors.Is(err, ErrNotLoopback) {
		t.Logf("no IPv6 loopback here: %v", err)
	}
	for _, addr := range []string{":0", "0.0.0.0:0", "[::]:0", "192.0.2.1:0", "example.com:0", "127.0.0.1", ""} {
		l, err := Listen(addr, false)
		if err == nil {
			l.Close()
			t.Errorf("Listen(%q) succeeded", addr)
		}
	}
	for _, addr := range []string{":0", "0.0.0.0:0"} {
		if _, err := Listen(addr, false); !errors.Is(err, ErrNotLoopback) {
			t.Errorf("Listen(%q) err = %v, want ErrNotLoopback", addr, err)
		}
	}
	l, err := Listen("0.0.0.0:0", true)
	if err != nil {
		t.Fatalf("AllowRemote: %v", err)
	}
	l.Close()
}

// TestServeOverTCP runs the real http.Server on a loopback listener.
func TestServeOverTCP(t *testing.T) {
	f := newFixture(t)
	l, err := Listen("127.0.0.1:0", false)
	if err != nil {
		t.Fatal(err)
	}
	hs := f.srv.HTTPServer()
	go hs.Serve(l)
	defer hs.Close()
	c := &http.Client{Timeout: 5 * time.Second}
	resp, err := c.Get("http://" + l.Addr().String() + "/api/status")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status %d", resp.StatusCode)
	}
	req, _ := http.NewRequest("GET", "http://"+l.Addr().String()+"/api/status", nil)
	req.Host = "rebind.example"
	resp, err = c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("rebinding host: %d", resp.StatusCode)
	}
	// Oversized headers are refused by net/http.
	req, _ = http.NewRequest("GET", "http://"+l.Addr().String()+"/", nil)
	req.Header.Set("X-Big", strings.Repeat("a", maxHeaderBytes+4096))
	resp, err = c.Do(req)
	if err == nil {
		resp.Body.Close()
		if resp.StatusCode != http.StatusRequestHeaderFieldsTooLarge {
			t.Fatalf("big header: %d", resp.StatusCode)
		}
	}
}

func TestNewNeedsStore(t *testing.T) {
	if _, err := New(Config{UI: fstest.MapFS{}}); err == nil {
		t.Fatal("New without a Store succeeded")
	}
}

// Dot and empty segments are refused outright (never resolved or
// redirected), for the API and the UI alike.
func TestNonCanonicalPathsRefused(t *testing.T) {
	f := newFixture(t)
	for _, p := range []string{"/api/./status", "/api//status", "/api/messages/%2e", "/api/messages/%2E%2E/raw",
		"/./app.js", "/css/../app.js", "//app.js", "/css/%2e%2e/app.js", "/a%5cb", "/a%00"} {
		f.wantError(f.do("GET", p), http.StatusBadRequest)
	}
}

// dirListFS is a file system whose directories read as listings, to check
// that the UI never serves a directory's content.
type dirListFS struct{ fstest.MapFS }

func (d dirListFS) Open(name string) (fs.File, error) {
	f, err := d.MapFS.Open(name)
	if err != nil {
		return nil, err
	}
	if fi, err := f.Stat(); err == nil && fi.IsDir() {
		return &listingFile{File: f, r: strings.NewReader("LISTING app.js")}, nil
	}
	return f, nil
}

type listingFile struct {
	fs.File
	r *strings.Reader
}

func (l *listingFile) Read(b []byte) (int, error) { return l.r.Read(b) }

func TestStaticNeverServesDirectories(t *testing.T) {
	f := newFixture(t, func(c *Config) { c.UI = dirListFS{testUI} })
	for _, p := range []string{"/css", "/img/sub"} {
		rec := f.do("GET", p)
		f.wantError(rec, http.StatusNotFound)
		if strings.Contains(rec.Body.String(), "LISTING") {
			t.Errorf("%s served a listing", p)
		}
	}
	if rec := f.do("GET", "/app.js"); rec.Code != 200 {
		t.Errorf("file: %d", rec.Code)
	}
}

func TestLoopbackHost(t *testing.T) {
	for host, want := range map[string]bool{
		"localhost": true, "LocalHost": true, "127.0.0.1": true, "127.5.6.7": true, "::1": true,
		"": false, "0.0.0.0": false, "::": false, "192.0.2.1": false, "example.com": false, "localhost.example.com": false,
	} {
		if got := loopbackHost(host); got != want {
			t.Errorf("loopbackHost(%q) = %v, want %v", host, got, want)
		}
	}
}

func TestScrubPaths(t *testing.T) {
	s := &Server{root: "/data/exports"}
	for in, want := range map[string]string{
		"open /data/exports/Mail/Inbox: bad":        "open Mail/Inbox: bad",
		"read /data/exports failed":                 "read exports failed",
		"cannot open /etc/profile.d/x.msf (gone)":   "cannot open x.msf (gone)",
		`prefs at "/somewhere/else/prefs.js"`:       `prefs at "prefs.js"`,
		"keep relative a/b/c and x/y alone":         "keep relative a/b/c and x/y alone",
		"/var/tmp/z.olm: zip: not a valid zip file": "z.olm: zip: not a valid zip file",
	} {
		if got := s.scrub(in); got != want {
			t.Errorf("scrub(%q) = %q, want %q", in, got, want)
		}
	}
}
