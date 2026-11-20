package server

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net"
	"net/http"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/lazyoracle/mail-export-viewer/internal/index"
	"github.com/lazyoracle/mail-export-viewer/internal/source"
)

// Config is what New needs.
type Config struct {
	// Store is the open index (required). Reads work while it indexes.
	Store *index.Store
	// Sources are the opened inputs; messages are re-read from the one
	// whose Path() equals the indexed source path.
	Sources []source.Source
	// Root is the absolute input path (the CLI passes it symlink-resolved,
	// as Source.Path values are absolute). Sources are shown by their path
	// relative to it (or their base name), never by full path.
	Root string
	// UI is the embedded web UI, served at "/" (index.html) and below.
	// Nil serves only the API.
	UI fs.FS
	// AllowedHosts are extra Host header values (names or IPs, any port)
	// accepted besides localhost, 127.0.0.1 and [::1]. Only for a
	// deliberately remote listener (see Listen): the Host check is the
	// DNS-rebinding defence.
	AllowedHosts []string
	// Version is reported by /api/status.
	Version string
	// InputWarnings are reported by /api/status (bounded and scrubbed of
	// paths): problems found before indexing, such as inputs that were
	// skipped or could not be opened.
	InputWarnings []string
	// IncludeDeleted mirrors index.Options.IncludeDeleted (reported by
	// /api/status so the UI can explain what it shows).
	IncludeDeleted bool
	// Logger receives internal errors (which may mention paths; they never
	// reach clients). Nil means log.Default().
	Logger *log.Logger

	// Test hooks: cid inlining limits and the load-budget wait (0 means
	// the defaults).
	inlinePart, inlineTotal int
	loadWait                time.Duration
}

// Server is the HTTP handler of the viewer: the JSON API under /api/ and
// the UI everywhere else, behind one security envelope.
type Server struct {
	st             *index.Store
	srcs           map[string]source.Source
	root           string
	ui             fs.FS
	hosts          hostSet
	version        string
	inputWarnings  []string
	includeDeleted bool
	log            *log.Logger
	cidKey         []byte // per-process secret for cid URL tokens
	inlinePart     int
	inlineTotal    int
	sem            *byteSem
	loadWait       time.Duration
	stats          statsCache
	handler        http.Handler
}

const (
	// Small raster cid images are inlined as data: URIs (see cidInliner).
	defaultInlinePart  = 1 << 20
	defaultInlineTotal = 4 << 20
	// loadBudget bounds the message bytes held by concurrent requests that
	// re-read messages from their sources (see byteSem and loadWeight). It
	// fits one worst-case request: three copies of a message of the
	// largest size a source materialises.
	loadBudget = 3 * source.MaxMessageBytes
	// defaultLoadWait is how long a request waits for load budget before
	// it is answered 503 (busy) with Retry-After.
	defaultLoadWait = 15 * time.Second
	// maxHeaderBytes bounds the request line plus headers.
	maxHeaderBytes = 64 << 10
)

// New returns the server for cfg.
func New(cfg Config) (*Server, error) {
	if cfg.Store == nil {
		return nil, errors.New("server: no index store")
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("server: no randomness: %w", err)
	}
	s := &Server{
		st: cfg.Store, srcs: map[string]source.Source{}, root: cfg.Root, ui: cfg.UI,
		hosts: newHostSet(cfg.AllowedHosts), version: cfg.Version, inputWarnings: cfg.InputWarnings, includeDeleted: cfg.IncludeDeleted,
		log: cfg.Logger, cidKey: key, inlinePart: cfg.inlinePart, inlineTotal: cfg.inlineTotal,
		sem: newByteSem(loadBudget), loadWait: cfg.loadWait,
	}
	if s.loadWait <= 0 {
		s.loadWait = defaultLoadWait
	}
	if s.log == nil {
		s.log = log.Default()
	}
	if s.inlinePart <= 0 {
		s.inlinePart = defaultInlinePart
	}
	if s.inlineTotal <= 0 {
		s.inlineTotal = defaultInlineTotal
	}
	for _, src := range cfg.Sources {
		s.srcs[src.Path()] = src
	}
	s.handler = s.envelope(http.HandlerFunc(s.dispatch))
	return s, nil
}

// ServeHTTP implements http.Handler.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) { s.handler.ServeHTTP(w, r) }

// HTTPServer returns an http.Server for s with timeouts and header limits
// suited to a local viewer (downloads of large attachments may take a
// while; slow or idle clients are cut off).
func (s *Server) HTTPServer() *http.Server {
	return &http.Server{
		Handler:           s,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      10 * time.Minute,
		IdleTimeout:       2 * time.Minute,
		MaxHeaderBytes:    maxHeaderBytes,
		ErrorLog:          s.log,
	}
}

// ErrNotLoopback is returned by Listen for a non-loopback address when
// remote listening is not allowed.
var ErrNotLoopback = errors.New("server: refusing to listen on a non-loopback address (use -allow-remote to override)")

// Listen opens a TCP listener on addr ("host:port"). Unless allowRemote,
// the host must be "localhost" or a loopback IP (an empty host or
// 0.0.0.0/:: is refused), and the bound address is checked again, so a
// "localhost" resolving elsewhere is refused too.
func Listen(addr string, allowRemote bool) (net.Listener, error) {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, fmt.Errorf("server: bad listen address %q: %w", addr, err)
	}
	if !allowRemote && !loopbackHost(host) {
		return nil, ErrNotLoopback
	}
	l, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	if ta, ok := l.Addr().(*net.TCPAddr); !allowRemote && (!ok || !ta.IP.IsLoopback()) {
		l.Close()
		return nil, ErrNotLoopback
	}
	return l, nil
}

// loopbackHost reports whether host is "localhost" or a loopback IP.
func loopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// cidToken authorises cross-origin loads of message id's inline images: the
// sandboxed message iframe has an opaque origin, so its image requests are
// cross-origin and need CORP cross-origin. The token (a MAC of the id under
// a per-process random key) appears only in same-origin JSON, so other web
// pages cannot construct such URLs. Message ids are never reused.
func (s *Server) cidToken(id int64) string {
	m := hmac.New(sha256.New, s.cidKey)
	m.Write([]byte(strconv.FormatInt(id, 10)))
	return hex.EncodeToString(m.Sum(nil))[:32]
}

// sourceName is how a source path is shown: relative to Root when inside
// it, else its base name. Never a full path.
func (s *Server) sourceName(path string) string {
	if s.root != "" {
		if rel, err := filepath.Rel(s.root, path); err == nil && rel != "." && !strings.HasPrefix(rel, "..") && !filepath.IsAbs(rel) {
			return filepath.ToSlash(rel)
		}
	}
	return filepath.Base(path)
}

// scrub replaces every known source path and the root in a message (a
// warning or error from a source) by its display name.
func (s *Server) scrub(msg string, paths ...string) string {
	for _, p := range paths {
		if p != "" && p != "." && p != "/" {
			msg = strings.ReplaceAll(msg, p, s.sourceName(p))
		}
	}
	if s.root != "" && s.root != "/" {
		msg = strings.ReplaceAll(msg, s.root+string(filepath.Separator), "")
		msg = strings.ReplaceAll(msg, s.root, filepath.Base(s.root))
	}
	// Any other absolute path (two or more segments) keeps its last one.
	return absPathRe.ReplaceAllString(msg, "$1$2")
}

// absPathRe matches an absolute path of two or more segments at the start
// of the text or after a space, quote, "(" or "=".
var absPathRe = regexp.MustCompile(`(^|[\s'"(=])(?:/[^\s/:'"<>()]+)+/([^\s/:'"<>()]+)`)
