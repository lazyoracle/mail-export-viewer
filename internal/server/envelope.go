package server

import (
	"bytes"
	"errors"
	"io"
	"io/fs"
	"mime"
	"net"
	"net/http"
	"net/url"
	"path"
	"runtime/debug"
	"strings"
	"time"
)

// uiCSP is sent with the static UI. Message HTML is shown in a sandboxed
// srcdoc iframe, which inherits this policy: inline styles are allowed,
// scripts only from the app itself, images only local.
const uiCSP = "default-src 'self'; img-src 'self' data:; style-src 'self' 'unsafe-inline'; " +
	"object-src 'none'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'"

// bodyCSP is sent on every API response (it matters for those that carry
// message content); raw, attachment and cid responses add "sandbox".
const bodyCSP = "default-src 'none'; img-src 'self' data:; style-src 'unsafe-inline'"

const (
	// maxRawQuery and maxPathLen bound the URL (414 above them).
	maxRawQuery = 8 << 10
	maxPathLen  = 4 << 10
)

// envelope applies what every response needs, in this order: panic
// recovery, base security headers, the Host allow-list (DNS-rebinding
// defence), GET/HEAD only, URL size limits and canonical paths (no dot
// or empty segments, backslashes or NULs, so nothing is ever redirected
// or resolved outside the UI tree). No CORS headers are ever sent.
func (s *Server) envelope(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rw := &trackingWriter{ResponseWriter: w}
		defer func() {
			if v := recover(); v != nil {
				if v == http.ErrAbortHandler {
					panic(v)
				}
				s.log.Printf("server: panic serving %s: %v\n%s", r.URL.Path, v, debug.Stack())
				if !rw.wrote {
					writeError(rw, http.StatusInternalServerError, codeInternal, "internal error")
				}
			}
		}()
		hd := rw.Header()
		hd.Set("X-Content-Type-Options", "nosniff")
		hd.Set("Referrer-Policy", "no-referrer")
		hd.Set("X-Frame-Options", "DENY")
		hd.Set("Cross-Origin-Resource-Policy", "same-origin")
		hd.Set("Cross-Origin-Opener-Policy", "same-origin")
		hd.Set("Content-Security-Policy", bodyCSP)
		if !s.hosts.allowed(r.Host) {
			writeError(rw, http.StatusForbidden, codeForbiddenHost, "forbidden host")
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			hd.Set("Allow", "GET, HEAD")
			writeError(rw, http.StatusMethodNotAllowed, codeMethod, "method not allowed")
			return
		}
		p := r.URL.EscapedPath()
		if len(r.URL.RawQuery) > maxRawQuery || len(p) > maxPathLen {
			writeError(rw, http.StatusRequestURITooLong, codeURITooLong, "request URI too long")
			return
		}
		if !canonicalPath(p) {
			writeError(rw, http.StatusBadRequest, codeBadRequest, "bad path")
			return
		}
		h.ServeHTTP(rw, r)
	})
}

// trackingWriter records whether a response was started, so a panic after
// that does not write a second status.
type trackingWriter struct {
	http.ResponseWriter
	wrote bool
}

func (t *trackingWriter) WriteHeader(code int) { t.wrote = true; t.ResponseWriter.WriteHeader(code) }
func (t *trackingWriter) Write(b []byte) (int, error) {
	t.wrote = true
	return t.ResponseWriter.Write(b)
}

// canonicalPath accepts "/" and paths of non-empty segments that are not
// "." or ".." (also when percent-encoded) and hold no backslash or NUL.
func canonicalPath(escaped string) bool {
	if escaped == "/" {
		return true
	}
	if escaped == "" || escaped[0] != '/' {
		return false
	}
	segs := strings.Split(escaped[1:], "/")
	for i, seg := range segs {
		dec, err := url.PathUnescape(seg)
		if err != nil {
			return false
		}
		if dec == "" && i != len(segs)-1 || dec == "." || dec == ".." ||
			strings.ContainsAny(dec, "\\\x00") {
			return false
		}
	}
	return true
}

// dispatch routes API requests and serves the UI for everything else.
func (s *Server) dispatch(w http.ResponseWriter, r *http.Request) {
	p := r.URL.EscapedPath()
	if p == "/api" || strings.HasPrefix(p, "/api/") {
		w.Header().Set("Cache-Control", "no-store")
		s.route(w, r)
		return
	}
	s.serveStatic(w, r)
}

// serveStatic serves a file of the UI tree: "/" is index.html, a
// directory is never listed (404), and dot-files are not served.
func (s *Server) serveStatic(w http.ResponseWriter, r *http.Request) {
	if s.ui == nil {
		writeError(w, http.StatusNotFound, codeNotFound, "not found")
		return
	}
	name := strings.TrimPrefix(r.URL.Path, "/")
	if name == "" {
		name = "index.html"
	}
	if !fs.ValidPath(name) || strings.HasSuffix(r.URL.Path, "/") && r.URL.Path != "/" {
		writeError(w, http.StatusNotFound, codeNotFound, "not found")
		return
	}
	for _, seg := range strings.Split(name, "/") {
		if strings.HasPrefix(seg, ".") {
			writeError(w, http.StatusNotFound, codeNotFound, "not found")
			return
		}
	}
	data, err := readRegular(s.ui, name)
	if err != nil {
		writeError(w, http.StatusNotFound, codeNotFound, "not found")
		return
	}
	h := w.Header()
	h.Set("Content-Security-Policy", uiCSP)
	h.Set("Cache-Control", "no-cache")
	h.Set("Content-Type", staticType(name))
	http.ServeContent(w, r, "", time.Time{}, bytes.NewReader(data))
}

// readRegular reads name from fsys if it is a regular file.
func readRegular(fsys fs.FS, name string) ([]byte, error) {
	f, err := fsys.Open(name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() {
		return nil, errors.New("not a regular file")
	}
	return io.ReadAll(f)
}

// staticTypes are the UI file types; anything else is served as
// application/octet-stream (never sniffed).
var staticTypes = map[string]string{
	".html": "text/html; charset=utf-8", ".js": "text/javascript; charset=utf-8", ".mjs": "text/javascript; charset=utf-8",
	".css": "text/css; charset=utf-8", ".json": "application/json; charset=utf-8", ".svg": "image/svg+xml",
	".png": "image/png", ".ico": "image/x-icon", ".webp": "image/webp", ".woff2": "font/woff2",
	".txt": "text/plain; charset=utf-8", ".map": "application/json; charset=utf-8",
}

func staticType(name string) string {
	if t, ok := staticTypes[strings.ToLower(path.Ext(name))]; ok {
		return t
	}
	if t := mime.TypeByExtension(path.Ext(name)); strings.HasPrefix(t, "image/") && !strings.Contains(t, "svg") {
		return t
	}
	return "application/octet-stream"
}

// hostSet is the Host-header allow-list: loopback plus extra (lower-case,
// IPv6 in brackets, no port).
type hostSet struct{ extra map[string]bool }

func newHostSet(hosts []string) hostSet {
	hs := hostSet{extra: map[string]bool{}}
	for _, h := range hosts {
		if h = normHost(h); h != "" {
			hs.extra[h] = true
		}
	}
	return hs
}

// normHost lower-cases h, strips a port ("example.org:8080", "[::1]:80") and
// puts bare IPv6 addresses in brackets.
func normHost(h string) string {
	h = strings.ToLower(strings.TrimSpace(h))
	if host, _, err := net.SplitHostPort(h); err == nil {
		h = host
	}
	if strings.Contains(h, ":") && !strings.HasPrefix(h, "[") {
		h = "[" + h + "]"
	}
	return h
}

// allowed accepts localhost, 127.0.0.1, [::1] and the extra hosts, with or
// without a numeric port.
func (hs hostSet) allowed(host string) bool {
	h := host
	if i := strings.LastIndexByte(host, ':'); i >= 0 && !strings.HasSuffix(host, "]") {
		h = host[:i]
		port := host[i+1:]
		if port == "" || len(port) > 5 || strings.Trim(port, "0123456789") != "" {
			return false
		}
		if strings.Contains(h, ":") && !strings.HasPrefix(h, "[") {
			return false // a bare IPv6 address is not a valid Host
		}
	}
	h = strings.ToLower(h)
	switch h {
	case "localhost", "127.0.0.1", "[::1]":
		return true
	}
	return h != "" && hs.extra[h]
}
