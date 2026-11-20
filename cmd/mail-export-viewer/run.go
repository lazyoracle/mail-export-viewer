package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/lazyoracle/mail-export-viewer/internal/index"
	"github.com/lazyoracle/mail-export-viewer/internal/server"
	"github.com/lazyoracle/mail-export-viewer/internal/source"
	_ "github.com/lazyoracle/mail-export-viewer/internal/source/mbox"
	_ "github.com/lazyoracle/mail-export-viewer/internal/source/msf"
	_ "github.com/lazyoracle/mail-export-viewer/internal/source/olm"
	_ "github.com/lazyoracle/mail-export-viewer/internal/source/pst"
	"github.com/lazyoracle/mail-export-viewer/internal/web"
)

// version is injected at build time with -ldflags "-X main.version=...".
var version = "dev"

const (
	prog          = "mail-export-viewer"
	defaultAddr   = "127.0.0.1:8080"
	fallbackPorts = 20
)

type config struct {
	addr           string
	addrExplicit   bool
	allowRemote    bool
	allowedHosts   stringList
	cacheDir       string
	reindex        bool
	noOpen         bool
	includeDeleted bool
	input          string
}

// hooks are test seams.
type hooks struct {
	ready func(addr string) // called with the bound address once serving
	index func(ctx context.Context, st *index.Store, srcs []source.Source) error
	// indexed is called once the background indexing has returned and its
	// result has been handled.
	indexed func()
}

// usageError is a problem with the command line or the input (exit 2).
type usageError struct{ msg string }

func (e *usageError) Error() string { return e.msg }

func usageErrf(format string, a ...any) error { return &usageError{fmt.Sprintf(format, a...)} }

// stringList is a repeatable string flag.
type stringList []string

func (l *stringList) String() string     { return strings.Join(*l, ",") }
func (l *stringList) Set(v string) error { *l = append(*l, v); return nil }

// run is main: it returns the process exit code (0 ok, 2 usage or input
// problem, 1 anything else) and stops cleanly on SIGINT/SIGTERM.
func run(args []string, stdout, stderr io.Writer) int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	// After the first signal restore the default handler, so a second
	// Ctrl-C kills the process even if shutdown is slow.
	go func() { <-ctx.Done(); stop() }()
	return runCtx(ctx, args, stdout, stderr, hooks{})
}

func runCtx(ctx context.Context, args []string, stdout, stderr io.Writer, h hooks) int {
	cfg, code := parseArgs(args, stdout, stderr)
	if code >= 0 {
		return code
	}
	if err := serve(ctx, cfg, stderr, h); err != nil {
		fmt.Fprintf(stderr, "%s: %s\n", prog, clean(err.Error()))
		var ue *usageError
		if errors.As(err, &ue) {
			return 2
		}
		return 1
	}
	return 0
}

// reorder moves flags before the path so they may be given after it too.
func reorder(fs *flag.FlagSet, args []string) (flags, rest []string) {
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			return flags, append(rest, args[i+1:]...)
		}
		if len(a) < 2 || a[0] != '-' {
			rest = append(rest, a)
			continue
		}
		flags = append(flags, a)
		name := strings.TrimLeft(a, "-")
		if strings.Contains(name, "=") {
			continue
		}
		if f := fs.Lookup(name); f != nil {
			if bf, ok := f.Value.(interface{ IsBoolFlag() bool }); ok && bf.IsBoolFlag() {
				continue
			}
			if i+1 < len(args) {
				i++
				flags = append(flags, args[i])
			}
		}
	}
	return flags, rest
}

// parseArgs returns the config, or a non-negative exit code when the
// program should exit immediately.
func parseArgs(args []string, stdout, stderr io.Writer) (config, int) {
	var cfg config
	fs := flag.NewFlagSet(prog, flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fmt.Fprintf(stderr, "usage: %s [flags] <path>\n\n<path> is a mail export: a file or a directory searched recursively.\n"+
			"Supported formats: %s (and loose .eml files). Detection is by content.\n\nflags:\n", prog, strings.Join(source.Formats(), ", "))
		fs.PrintDefaults()
	}
	fs.StringVar(&cfg.addr, "addr", defaultAddr, "address to listen on (host:port); without it the next free port is used if busy")
	fs.StringVar(&cfg.cacheDir, "cache-dir", "", "directory for the index cache (default: <user cache dir>/"+prog+")")
	fs.BoolVar(&cfg.reindex, "reindex", false, "discard the cached index for this input and rebuild it")
	fs.BoolVar(&cfg.noOpen, "no-open", false, "do not open a browser")
	fs.BoolVar(&cfg.includeDeleted, "include-deleted", false, "show messages flagged as deleted")
	fs.BoolVar(&cfg.allowRemote, "allow-remote", false, "allow a non-loopback -addr; WARNING: exposes ALL your mail to the network with no authentication")
	fs.Var(&cfg.allowedHosts, "allowed-host", "extra Host name or IP to accept; requires -allow-remote (bare name, no port; repeatable)")
	showVersion := fs.Bool("version", false, "print the version and exit")
	flags, rest := reorder(fs, args)
	if err := fs.Parse(flags); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return cfg, 0
		}
		return cfg, 2
	}
	if *showVersion {
		fmt.Fprintf(stdout, "%s %s\n", prog, version)
		return cfg, 0
	}
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "addr" {
			cfg.addrExplicit = true
		}
	})
	if len(rest) != 1 {
		fmt.Fprintf(stderr, "%s: expected exactly one argument: the path of the mail export\n", prog)
		fs.Usage()
		return cfg, 2
	}
	cfg.input = rest[0]
	if _, _, err := net.SplitHostPort(cfg.addr); err != nil {
		fmt.Fprintf(stderr, "%s: invalid -addr %q: %v\n", prog, cfg.addr, err)
		return cfg, 2
	}
	if len(cfg.allowedHosts) > 0 && !cfg.allowRemote {
		fmt.Fprintf(stderr, "%s: -allowed-host requires -allow-remote: on the default loopback listener only localhost names are accepted, which protects against DNS rebinding\n", prog)
		return cfg, 2
	}
	if !cfg.allowRemote && !isLoopbackAddr(cfg.addr) {
		fmt.Fprintf(stderr, "%s: refusing to listen on non-loopback address %q: the viewer has no authentication and would expose your mail to the network; use -allow-remote to override\n", prog, cfg.addr)
		return cfg, 2
	}
	return cfg, -1
}

func isLoopbackAddr(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func isWildcardHost(host string) bool {
	ip := net.ParseIP(host)
	return host == "" || (ip != nil && ip.IsUnspecified())
}

// listen binds addr through server.Listen (loopback enforcement). When
// explicit is false and the port is busy it tries the next fallbackPorts.
func listen(addr string, explicit, allowRemote bool) (net.Listener, error) {
	l, err := server.Listen(addr, allowRemote)
	if err == nil {
		return l, nil
	}
	host, portStr, serr := net.SplitHostPort(addr)
	port, perr := strconv.Atoi(portStr)
	if explicit || serr != nil || perr != nil || port == 0 || !errors.Is(err, syscall.EADDRINUSE) {
		return nil, fmt.Errorf("cannot listen on %s: %w", addr, err)
	}
	for p := port + 1; p <= port+fallbackPorts && p <= 65535; p++ {
		if l, err2 := server.Listen(net.JoinHostPort(host, strconv.Itoa(p)), allowRemote); err2 == nil {
			return l, nil
		}
	}
	return nil, fmt.Errorf("cannot listen on %s or the next %d ports: %w", addr, fallbackPorts, err)
}

func cacheDirFor(cfg config) (string, error) {
	if cfg.cacheDir != "" {
		return cfg.cacheDir, nil
	}
	base, err := os.UserCacheDir()
	if err != nil {
		return "", fmt.Errorf("cannot determine the cache directory (use -cache-dir): %w", err)
	}
	return filepath.Join(base, prog), nil
}

// openSources discovers the input and opens every candidate; a candidate
// that fails to open is reported and skipped.
func openSources(input string, stderr io.Writer) ([]source.Source, error) {
	unsupported := func() error {
		return usageErrf("no supported mail found in %s\nsupported formats: %s (detected by content, not by extension)",
			input, strings.Join(source.Formats(), ", "))
	}
	d, err := source.Discover(input, source.Limits{})
	switch {
	case errors.Is(err, os.ErrNotExist):
		return nil, usageErrf("%s does not exist", input)
	case errors.Is(err, os.ErrPermission):
		return nil, usageErrf("cannot read %s: permission denied", input)
	case errors.Is(err, source.ErrUnknownFormat):
		return nil, unsupported()
	case err != nil:
		return nil, usageErrf("cannot read %s: %v", input, err)
	}
	for _, w := range d.Warnings {
		fmt.Fprintf(stderr, "%s: warning: %s\n", prog, clean(w))
	}
	if len(d.Candidates) == 0 {
		return nil, unsupported()
	}
	var srcs []source.Source
	for _, c := range d.Candidates {
		s, err := c.Open()
		if err != nil {
			fmt.Fprintf(stderr, "%s: warning: skipping %s: %v\n", prog, clean(c.Path), clean(err.Error()))
			continue
		}
		srcs = append(srcs, s)
	}
	if len(srcs) == 0 {
		return nil, usageErrf("none of the %d inputs found in %s could be opened", len(d.Candidates), input)
	}
	return srcs, nil
}

// serve opens the input, serves the UI and API at once, indexes in the
// background and blocks until ctx is done (nil after a clean shutdown) or
// the server fails.
func serve(ctx context.Context, cfg config, stderr io.Writer, h hooks) error {
	// Everything downstream works on the canonical input path: sources
	// report absolute paths, and they are named relative to this root.
	root, err := canonicalPath(cfg.input)
	if err != nil {
		return usageErrf("cannot resolve %s: %v", cfg.input, err)
	}
	srcs, err := openSources(root, stderr)
	if err != nil {
		return err
	}
	defer func() {
		for _, s := range srcs {
			s.Close()
		}
	}()
	cacheDir, err := cacheDirFor(cfg)
	if err != nil {
		return err
	}
	// Needed before index.Open: the instance lock lives next to the index.
	if err := os.MkdirAll(cacheDir, 0o700); err != nil {
		return fmt.Errorf("cannot create cache directory: %w", err)
	}
	db, err := index.CachePath(cacheDir, root)
	if err != nil {
		return err
	}
	lock, err := acquireCacheLock(db)
	if err != nil {
		return err
	}
	defer lock.release()
	ln, err := listen(cfg.addr, cfg.addrExplicit, cfg.allowRemote)
	if err != nil {
		return err
	}
	defer ln.Close()
	st, err := index.Open(db, index.Options{IncludeDeleted: cfg.includeDeleted})
	switch {
	case errors.Is(err, index.ErrLocked):
		return fmt.Errorf("already running for this input: %w", err)
	case err != nil:
		return fmt.Errorf("cannot open the index %s: %w (remove it or try -reindex)", db, err)
	}
	defer st.Close()
	if cfg.reindex {
		if err := st.Reset(); err != nil {
			return fmt.Errorf("cannot reset the index: %w", err)
		}
	}

	hosts := append([]string(nil), cfg.allowedHosts...)
	if cfg.allowRemote {
		host, _, _ := net.SplitHostPort(cfg.addr)
		if !isLoopbackAddr(cfg.addr) {
			fmt.Fprintf(stderr, "%s: WARNING: listening on %s with -allow-remote: ALL your mail is readable by anyone who can reach it, with no authentication\n", prog, ln.Addr())
			if isWildcardHost(host) && len(hosts) == 0 {
				fmt.Fprintf(stderr, "%s: warning: only localhost Host headers are accepted; add the names or IPs clients use with -allowed-host (repeatable)\n", prog)
			} else if !isWildcardHost(host) {
				hosts = append(hosts, host)
			}
		}
	}
	srv, err := server.New(server.Config{Store: st, Sources: srcs, Root: root, UI: web.FS(),
		AllowedHosts: hosts, Version: version, IncludeDeleted: cfg.includeDeleted})
	if err != nil {
		return err
	}
	httpSrv := srv.HTTPServer()
	srvErr := make(chan error, 1)
	go func() { srvErr <- httpSrv.Serve(ln) }()

	addr := ln.Addr().String()
	_, port, _ := net.SplitHostPort(addr)
	urlHost := addr
	if host, _, _ := net.SplitHostPort(addr); isWildcardHost(host) {
		urlHost = net.JoinHostPort("localhost", port)
	}
	url := "http://" + urlHost + "/"
	lock.record(fmt.Sprintf("pid %d, %s", os.Getpid(), url))
	fmt.Fprintf(stderr, "%s: %s (%d inputs; indexing in the background)\n", prog, url, len(srcs))
	if h.ready != nil {
		h.ready(addr)
	}
	if !cfg.noOpen {
		openBrowser(url)
	}

	start := time.Now()
	indexFn := h.index
	if indexFn == nil {
		indexFn = func(ctx context.Context, st *index.Store, srcs []source.Source) error {
			return st.IndexSources(ctx, srcs, nil)
		}
	}
	idxCtx, cancelIdx := context.WithCancel(ctx)
	idxDone := make(chan struct{})
	var idxErr error // written before idxDone closes
	go func() {
		defer close(idxDone)
		if h.indexed != nil {
			defer h.indexed()
		}
		err := indexFn(idxCtx, st, srcs)
		switch {
		case err == nil:
			fmt.Fprintf(stderr, "%s: %s\n", prog, summary(st.Status(), time.Since(start)))
		case idxCtx.Err() == nil:
			idxErr = err
			fmt.Fprintf(stderr, "%s: indexing failed: %s\n", prog, clean(err.Error()))
		}
	}()

	var result error
	select {
	case <-ctx.Done():
	case err := <-srvErr:
		result = fmt.Errorf("server: %w", err)
	}
	// Shutdown order: cancel indexing first (so it stops writing), then
	// stop the HTTP server, then wait for the indexer (bounded), and only
	// then close the store (deferred) and release the lock.
	cancelIdx()
	sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := httpSrv.Shutdown(sctx); err != nil {
		httpSrv.Close()
	}
	select {
	case <-idxDone:
		if result == nil && idxErr != nil {
			result = fmt.Errorf("indexing failed: %w", idxErr) // exit code 1
		}
	case <-time.After(shutdownWait):
		if result == nil {
			result = fmt.Errorf("indexing did not stop within %s; exiting anyway", shutdownWait)
		}
	}
	return result
}

// summary describes a finished indexing run: messages per format, warnings
// and elapsed time.
func summary(p index.Progress, elapsed time.Duration) string {
	per := map[string]int64{}
	for _, s := range p.Sources {
		per[s.Kind] += s.Messages
	}
	kinds := make([]string, 0, len(per))
	for k := range per {
		kinds = append(kinds, k)
	}
	sort.Strings(kinds)
	parts := make([]string, 0, len(kinds))
	for _, k := range kinds {
		parts = append(parts, fmt.Sprintf("%s %d", k, per[k]))
	}
	s := fmt.Sprintf("indexed %d messages", p.Messages)
	if len(parts) > 0 {
		s += " (" + strings.Join(parts, ", ") + ")"
	}
	return fmt.Sprintf("%s, %d warnings, in %s", s, p.Warnings, elapsed.Round(time.Millisecond))
}

// openBrowser starts the platform opener; failure is ignored.
func openBrowser(url string) {
	var cmd string
	switch runtime.GOOS {
	case "darwin":
		cmd = "open"
	case "linux":
		cmd = "xdg-open"
	default:
		return
	}
	c := exec.Command(cmd, url)
	if err := c.Start(); err == nil {
		go func() { _ = c.Wait() }()
	}
}

// shutdownWait bounds how long shutdown waits for a stuck indexer.
var shutdownWait = 10 * time.Second

// clean makes an untrusted string (file names, warnings, error texts that
// embed them) safe for a terminal and for line-oriented logs: control and
// format characters (escape sequences, newlines, bidi overrides) become
// \xNN / \uNNNN escapes.
func clean(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r == utf8.RuneError || unicode.IsControl(r) || unicode.Is(unicode.Cf, r):
			if r < 0x100 {
				fmt.Fprintf(&b, "\\x%02x", r)
			} else {
				fmt.Fprintf(&b, "\\u%04x", r)
			}
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// canonicalPath returns the absolute, symlink-resolved form of input (the
// plain absolute path when it cannot be resolved, e.g. it does not exist).
// The index cache is named after it, so one input reached through
// different links or relative paths shares one cache and one lock, and
// sources are named relative to it.
func canonicalPath(input string) (string, error) {
	abs, err := filepath.Abs(input)
	if err != nil {
		return "", err
	}
	if real, err := filepath.EvalSymlinks(abs); err == nil {
		abs = real
	}
	return abs, nil
}
