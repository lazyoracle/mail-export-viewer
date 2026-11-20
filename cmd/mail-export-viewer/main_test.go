package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/lazyoracle/mail-export-viewer/internal/index"
	"github.com/lazyoracle/mail-export-viewer/internal/source"
	"github.com/lazyoracle/mail-export-viewer/internal/testutil"
)

func TestVersionDefault(t *testing.T) {
	if version == "" {
		t.Fatal("version must not be empty")
	}
}

func TestParseArgs(t *testing.T) {
	tests := []struct {
		name string
		args []string
		code int // -1: run
		want func(config) bool
		msg  string // expected stderr/stdout substring
	}{
		{"defaults", []string{"in"}, -1, func(c config) bool {
			return c.input == "in" && c.addr == "127.0.0.1:8080" && !c.addrExplicit && !c.noOpen && !c.reindex
		}, ""},
		{"flags after path", []string{"in", "-no-open", "-addr", "127.0.0.1:9000", "-reindex", "-include-deleted"}, -1, func(c config) bool {
			return c.input == "in" && c.noOpen && c.reindex && c.includeDeleted && c.addrExplicit && c.addr == "127.0.0.1:9000"
		}, ""},
		{"flags before path", []string{"-cache-dir", "/x", "-no-open", "in"}, -1, func(c config) bool {
			return c.input == "in" && c.cacheDir == "/x"
		}, ""},
		{"repeatable allowed-host", []string{"-allow-remote", "-addr", "0.0.0.0:80", "-allowed-host", "a", "-allowed-host", "b", "in"}, -1, func(c config) bool {
			return len(c.allowedHosts) == 2 && c.allowedHosts[1] == "b" && c.allowRemote
		}, ""},
		{"allowed-host needs allow-remote", []string{"-allowed-host", "a", "in"}, 2, nil, "-allowed-host requires -allow-remote"},
		{"version", []string{"-version"}, 0, nil, "mail-export-viewer "},
		{"help", []string{"-h"}, 0, nil, "usage:"},
		{"no path", nil, 2, nil, "usage:"},
		{"two paths", []string{"a", "b"}, 2, nil, "exactly one"},
		{"unknown flag", []string{"-bogus", "in"}, 2, nil, "bogus"},
		{"bad addr", []string{"-addr", "nope", "in"}, 2, nil, "invalid -addr"},
		{"remote refused", []string{"-addr", "0.0.0.0:8080", "in"}, 2, nil, "-allow-remote"},
		{"localhost ok", []string{"-addr", "localhost:1", "in"}, -1, nil, ""},
		{"LOCALHOST ok", []string{"-addr", "LOCALHOST:1", "in"}, -1, nil, ""},
		{"double dash", []string{"-no-open", "--", "-weird"}, -1, func(c config) bool { return c.input == "-weird" && c.noOpen }, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var out, errb bytes.Buffer
			cfg, code := parseArgs(tc.args, &out, &errb)
			if code != tc.code {
				t.Fatalf("code = %d, want %d (%s)", code, tc.code, errb.String())
			}
			if tc.want != nil && !tc.want(cfg) {
				t.Fatalf("config = %+v", cfg)
			}
			if tc.msg != "" && !strings.Contains(out.String()+errb.String(), tc.msg) {
				t.Fatalf("output %q lacks %q", out.String()+errb.String(), tc.msg)
			}
		})
	}
}

func TestRunExitCodes(t *testing.T) {
	cache := t.TempDir()
	empty := t.TempDir()
	junk := filepath.Join(t.TempDir(), "x.txt")
	os.WriteFile(junk, []byte("just text\n"), 0o644)
	tests := []struct {
		name string
		args []string
		code int
		msg  string
	}{
		{"missing", []string{"-no-open", "-cache-dir", cache, filepath.Join(empty, "nope")}, 2, "does not exist"},
		{"empty dir lists formats", []string{"-no-open", "-cache-dir", cache, empty}, 2, "mbox"},
		{"unknown file", []string{"-no-open", "-cache-dir", cache, junk}, 2, "supported formats"},
		{"bad usage", []string{}, 2, "usage"},
		{"version", []string{"-version"}, 0, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var out, errb bytes.Buffer
			if code := run(tc.args, &out, &errb); code != tc.code {
				t.Fatalf("code = %d, want %d; stderr %q", code, tc.code, errb.String())
			}
			if !strings.Contains(out.String()+errb.String(), tc.msg) {
				t.Fatalf("output %q lacks %q", errb.String(), tc.msg)
			}
		})
	}
	// Other failures exit 1: the cache dir cannot be created.
	blocker := filepath.Join(t.TempDir(), "file")
	os.WriteFile(blocker, nil, 0o644)
	dir := testutil.MixedDir(t)
	var errb bytes.Buffer
	if code := run([]string{"-no-open", "-addr", "127.0.0.1:0", "-cache-dir", filepath.Join(blocker, "sub"), dir}, io.Discard, &errb); code != 1 {
		t.Fatalf("code = %d, want 1; %s", code, errb.String())
	}
}

func TestAcquireCacheLock(t *testing.T) {
	db := filepath.Join(t.TempDir(), "x.db")
	l1, err := acquireCacheLock(db)
	if err != nil {
		t.Fatal(err)
	}
	l1.record("pid 7, http://127.0.0.1:1/")
	_, err = acquireCacheLock(db)
	if err == nil || !strings.Contains(err.Error(), "already running") || !strings.Contains(err.Error(), "http://127.0.0.1:1/") {
		t.Fatalf("second lock: %v", err)
	}
	fi, _ := os.Stat(db + ".run")
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("lock mode %v", fi.Mode())
	}
	l1.release()
	l2, err := acquireCacheLock(db)
	if err != nil {
		t.Fatalf("after release: %v", err)
	}
	l2.release()
}

func TestLockRefusesSymlink(t *testing.T) {
	dir := t.TempDir()
	db := filepath.Join(dir, "x.db")
	target := filepath.Join(dir, "target")
	os.WriteFile(target, []byte("keep"), 0o644)
	if err := os.Symlink(target, db+".run"); err != nil {
		t.Skip(err)
	}
	if _, err := acquireCacheLock(db); err == nil {
		t.Fatal("symlink lock accepted")
	}
	if b, _ := os.ReadFile(target); string(b) != "keep" {
		t.Fatalf("target modified: %q", b)
	}
}

func TestListenFallback(t *testing.T) {
	busy, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer busy.Close()
	addr := busy.Addr().String()
	port := busy.Addr().(*net.TCPAddr).Port

	if _, err := listen(addr, true, false); err == nil {
		t.Fatal("explicit busy addr must fail")
	}
	l, err := listen(addr, false, false)
	if err != nil {
		// Acceptable only if every fallback port is really taken.
		for p := port + 1; p <= port+fallbackPorts; p++ {
			if x, e := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(p))); e == nil {
				x.Close()
				t.Fatalf("free port %d was not used: %v", p, err)
			}
		}
		t.Skip("all fallback ports busy")
	}
	defer l.Close()
	got := l.Addr().(*net.TCPAddr).Port
	if got <= port || got > port+fallbackPorts {
		t.Fatalf("port %d not in (%d, %d]", got, port, port+fallbackPorts)
	}
	if _, err := listen("0.0.0.0:0", false, false); err == nil {
		t.Fatal("non-loopback without allow-remote must fail")
	}
}

func getJSON(t *testing.T, url string, v any) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("GET %s: %s", url, resp.Status)
	}
	if err := json.NewDecoder(resp.Body).Decode(v); err != nil {
		t.Fatal(err)
	}
}

func waitFinished(t *testing.T, base string) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		var st struct {
			Indexing struct{ Finished bool }
		}
		getJSON(t, base+"/api/status", &st)
		if st.Indexing.Finished {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("indexing did not finish")
}

func TestRunInProcess(t *testing.T) {
	dir := testutil.MixedDir(t)
	cache := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	addrCh := make(chan string, 1)
	done := make(chan int, 1)
	var errb syncBuffer
	h := hooks{ready: func(a string) { addrCh <- a }}
	go func() {
		done <- runCtx(ctx, []string{"-no-open", "-addr", "127.0.0.1:0", "-cache-dir", cache, dir}, io.Discard, &errb, h)
	}()
	var base string
	select {
	case a := <-addrCh:
		base = "http://" + a
	case code := <-done:
		t.Fatalf("exited early: %d %s", code, errb.String())
	case <-time.After(20 * time.Second):
		t.Fatal("no ready")
	}
	var st struct {
		Version string
		Sources []struct{ Kind string }
	}
	getJSON(t, base+"/api/status", &st)
	if st.Version == "" {
		t.Fatal("status lacks version")
	}
	waitFinished(t, base)
	var msgs struct{ Items []struct{ Subject string } }
	getJSON(t, base+"/api/messages", &msgs)
	if len(msgs.Items) != testutil.MixedDirMessages {
		t.Fatalf("%d messages, want %d", len(msgs.Items), testutil.MixedDirMessages)
	}
	// The UI is served too.
	resp, err := http.Get(base + "/")
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("UI: %v %v", resp, err)
	}
	resp.Body.Close()

	// A second instance for the same input is refused while this one runs.
	var e2 bytes.Buffer
	if code := run([]string{"-no-open", "-addr", "127.0.0.1:0", "-cache-dir", cache, dir}, io.Discard, &e2); code != 1 || !strings.Contains(e2.String(), "already running") {
		t.Fatalf("second instance: %d %s", code, e2.String())
	}

	cancel()
	select {
	case code := <-done:
		if code != 0 {
			t.Fatalf("exit %d: %s", code, errb.String())
		}
	case <-time.After(20 * time.Second):
		t.Fatal("no shutdown")
	}
	out := errb.String()
	for _, want := range []string{"http://127.0.0.1:", "indexed " + fmt.Sprint(testutil.MixedDirMessages) + " messages", "mbox 4", "eml 1", "olm 1", "warnings"} {
		if !strings.Contains(out, want) {
			t.Errorf("stderr lacks %q:\n%s", want, out)
		}
	}
	// Released after exit: a new run (with -reindex) works and the cache is the only thing written.
	ctx2, cancel2 := context.WithCancel(context.Background())
	addr2 := make(chan string, 1)
	done2 := make(chan int, 1)
	go func() {
		done2 <- runCtx(ctx2, []string{"-no-open", "-reindex", "-addr", "127.0.0.1:0", "-cache-dir", cache, dir}, io.Discard, io.Discard, hooks{ready: func(a string) { addr2 <- a }})
	}()
	select {
	case a := <-addr2:
		waitFinished(t, "http://"+a)
	case code := <-done2:
		t.Fatalf("restart failed: %d", code)
	case <-time.After(20 * time.Second):
		t.Fatal("no restart")
	}
	cancel2()
	if code := <-done2; code != 0 {
		t.Fatalf("exit %d", code)
	}
	db, _ := index.CachePath(cache, dir)
	if _, err := os.Stat(db); err != nil {
		t.Fatalf("index file not at CachePath: %v", err)
	}
}

func TestSubprocessSmoke(t *testing.T) {
	if testing.Short() || runtime.GOOS == "windows" {
		t.Skip("builds the binary")
	}
	tmp := t.TempDir()
	bin := buildBinary(t, tmp)
	dir := testutil.MixedDir(t)
	cmd, errb, exited := startBinary(t, bin, "-no-open", "-addr", "127.0.0.1:0", "-cache-dir", filepath.Join(tmp, "cache"), dir)
	base := waitURL(t, errb, exited)
	waitFinished(t, base)
	var msgs struct{ Items []struct{ Subject string } }
	getJSON(t, base+"/api/messages", &msgs)
	if len(msgs.Items) != testutil.MixedDirMessages {
		t.Fatalf("%d messages", len(msgs.Items))
	}
	cmd.Process.Signal(syscall.SIGINT)
	select {
	case err := <-exited:
		if err != nil {
			t.Fatalf("exit: %v\n%s", err, errb.String())
		}
	case <-time.After(20 * time.Second):
		t.Fatal("no exit after SIGINT")
	}
}

func TestSubprocessDoubleSIGINT(t *testing.T) {
	if testing.Short() || runtime.GOOS == "windows" {
		t.Skip("builds the binary")
	}
	tmp := t.TempDir()
	bin := buildBinary(t, tmp)
	big := filepath.Join(tmp, "big.mbox")
	var msgs []string
	body := strings.Repeat("lorem ipsum dolor sit amet\n", 200)
	for i := 0; i < 20000; i++ {
		msgs = append(msgs, fmt.Sprintf("From: A <a@example.com>\nSubject: m%d\nDate: Mon, 2 Jan 2023 10:00:00 +0000\nMessage-ID: <%d@x>\n\n%s", i, i, body))
	}
	testutil.WriteMbox(t, big, msgs...)
	cmd, errb, exited := startBinary(t, bin, "-no-open", "-addr", "127.0.0.1:0", "-cache-dir", filepath.Join(tmp, "cache"), big)
	waitURL(t, errb, exited)
	cmd.Process.Signal(syscall.SIGINT)
	time.Sleep(50 * time.Millisecond)
	cmd.Process.Signal(syscall.SIGINT)
	select {
	case <-exited:
	case <-time.After(15 * time.Second):
		t.Fatalf("no prompt exit after two SIGINTs:\n%s", errb.String())
	}
}

func buildBinary(t *testing.T, tmp string) string {
	t.Helper()
	bin := filepath.Join(tmp, "mev")
	build := exec.Command(goBin(), "build", "-o", bin, ".")
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	return bin
}

func startBinary(t *testing.T, bin string, args ...string) (*exec.Cmd, *syncBuffer, chan error) {
	t.Helper()
	cmd := exec.Command(bin, args...)
	errb := &syncBuffer{}
	cmd.Stderr = errb
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	t.Cleanup(func() { cmd.Process.Kill() })
	return cmd, errb, exited
}

var urlRe = regexp.MustCompile(`http://[^\s/]+`)

// waitURL parses the URL the child printed (it bound port 0 itself).
func waitURL(t *testing.T, errb *syncBuffer, exited chan error) string {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if u := urlRe.FindString(errb.String()); u != "" {
			return u
		}
		select {
		case err := <-exited:
			t.Fatalf("exited early: %v\n%s", err, errb.String())
		case <-time.After(20 * time.Millisecond):
		}
	}
	t.Fatalf("no URL printed:\n%s", errb.String())
	return ""
}

func goBin() string {
	p := filepath.Join(runtime.GOROOT(), "bin", "go")
	if _, err := os.Stat(p); err == nil {
		return p
	}
	return "go"
}

func TestCleanControlChars(t *testing.T) {
	got := clean("a\x1b[31mb\nc\x00d\u202ee\tf é")
	for _, r := range got {
		if r < 0x20 || r == 0x7f || r == 0x202e {
			t.Fatalf("control rune %q left in %q", r, got)
		}
	}
	if !strings.Contains(got, "é") || !strings.Contains(got, "a") {
		t.Fatalf("printable text lost: %q", got)
	}
}

func TestErrorOutputSanitised(t *testing.T) {
	var errb bytes.Buffer
	bad := filepath.Join(t.TempDir(), "no\x1b[31m\nforged: line")
	code := run([]string{"-no-open", "-cache-dir", t.TempDir(), bad}, io.Discard, &errb)
	if code != 2 {
		t.Fatalf("code %d", code)
	}
	if strings.ContainsRune(errb.String(), 0x1b) || strings.Count(errb.String(), "\n") != 1 {
		t.Fatalf("unsanitised output %q", errb.String())
	}
	// Unsupported file with a hostile name.
	junk := filepath.Join(t.TempDir(), "j\x1b]0;x\x07.txt")
	os.WriteFile(junk, []byte("text\n"), 0o644)
	errb.Reset()
	run([]string{"-no-open", "-cache-dir", t.TempDir(), junk}, io.Discard, &errb)
	if strings.ContainsRune(errb.String(), 0x1b) || strings.ContainsRune(errb.String(), 7) {
		t.Fatalf("unsanitised output %q", errb.String())
	}
}

func TestCachePathCanonical(t *testing.T) {
	dir := testutil.MixedDir(t)
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(dir, link); err != nil {
		t.Skip(err)
	}
	a, err := canonicalPath(dir)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := canonicalPath(link)
	if a != b {
		t.Fatalf("symlinked input got another canonical path: %s vs %s", a, b)
	}
	t.Chdir(dir)
	if c, _ := canonicalPath("."); c != a {
		t.Fatalf("relative input: %q, want %q", c, a)
	}
	missing := filepath.Join(dir, "missing")
	if c, err := canonicalPath(missing); err != nil || !filepath.IsAbs(c) || filepath.Base(c) != "missing" {
		t.Fatalf("fallback: %q %v", c, err)
	}
}

func TestSymlinkedInputSharesLock(t *testing.T) {
	dir := testutil.MixedDir(t)
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(dir, link); err != nil {
		t.Skip(err)
	}
	cache := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	addr := make(chan string, 1)
	done := make(chan int, 1)
	go func() {
		done <- runCtx(ctx, []string{"-no-open", "-addr", "127.0.0.1:0", "-cache-dir", cache, dir}, io.Discard, io.Discard, hooks{ready: func(a string) { addr <- a }})
	}()
	select {
	case <-addr:
	case <-time.After(20 * time.Second):
		t.Fatal("no ready")
	}
	var e bytes.Buffer
	if code := run([]string{"-no-open", "-addr", "127.0.0.1:0", "-cache-dir", cache, link}, io.Discard, &e); code != 1 || !strings.Contains(e.String(), "already running") {
		t.Fatalf("via symlink: %d %s", code, e.String())
	}
	cancel()
	<-done
}

func TestListenHostnameFallback(t *testing.T) {
	busy, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer busy.Close()
	port := busy.Addr().(*net.TCPAddr).Port
	l, err := listen("localhost:"+strconv.Itoa(port), false, false)
	if err != nil {
		t.Skipf("localhost fallback unavailable here: %v", err)
	}
	defer l.Close()
	if l.Addr().(*net.TCPAddr).Port == port {
		t.Fatal("bound the busy port")
	}
}

func startInProcess(t *testing.T, h hooks, args ...string) (base string, stop func() int, errb *syncBuffer) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	addr := make(chan string, 1)
	done := make(chan int, 1)
	errb = &syncBuffer{}
	h.ready = func(a string) { addr <- a }
	go func() { done <- runCtx(ctx, args, io.Discard, errb, h) }()
	select {
	case a := <-addr:
		base = "http://" + a
	case code := <-done:
		cancel()
		t.Fatalf("exited early %d: %s", code, errb.String())
	case <-time.After(20 * time.Second):
		cancel()
		t.Fatal("no ready")
	}
	return base, func() int {
		cancel()
		select {
		case c := <-done:
			return c
		case <-time.After(30 * time.Second):
			t.Fatal("no shutdown")
			return -1
		}
	}, errb
}

func hostStatus(t *testing.T, base, host string) int {
	t.Helper()
	req, _ := http.NewRequest("GET", base+"/api/status", nil)
	req.Host = host
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

func TestAllowedHostEndToEnd(t *testing.T) {
	dir := testutil.MixedDir(t)
	base, stop, _ := startInProcess(t, hooks{}, "-no-open", "-allow-remote", "-allowed-host", "viewer.test",
		"-addr", "127.0.0.1:0", "-cache-dir", t.TempDir(), dir)
	if c := hostStatus(t, base, "viewer.test"); c != 200 {
		t.Errorf("allowed host: %d", c)
	}
	if c := hostStatus(t, base, "evil.test"); c != 403 {
		t.Errorf("other host: %d", c)
	}
	if c := stop(); c != 0 {
		t.Errorf("exit %d", c)
	}
	// Without -allowed-host the name is refused.
	base, stop, _ = startInProcess(t, hooks{}, "-no-open", "-addr", "127.0.0.1:0", "-cache-dir", t.TempDir(), dir)
	if c := hostStatus(t, base, "viewer.test"); c != 403 {
		t.Errorf("unlisted host: %d", c)
	}
	stop()
}

func TestShutdownWaitBounded(t *testing.T) {
	dir := testutil.MixedDir(t)
	old := shutdownWait
	shutdownWait = 300 * time.Millisecond
	defer func() { shutdownWait = old }()
	release := make(chan struct{})
	defer close(release)
	h := hooks{index: func(ctx context.Context, st *index.Store, srcs []source.Source) error {
		<-release // ignores ctx: a stuck indexer
		return nil
	}}
	_, stop, errb := startInProcess(t, h, "-no-open", "-addr", "127.0.0.1:0", "-cache-dir", t.TempDir(), dir)
	start := time.Now()
	code := stop()
	if time.Since(start) > 5*time.Second {
		t.Fatalf("shutdown took %v", time.Since(start))
	}
	if code != 1 || !strings.Contains(errb.String(), "did not stop") {
		t.Fatalf("code %d: %s", code, errb.String())
	}
}

func TestIndexFailureReported(t *testing.T) {
	dir := testutil.MixedDir(t)
	indexed := make(chan struct{})
	h := hooks{
		index: func(ctx context.Context, st *index.Store, srcs []source.Source) error {
			return errors.New("disk on fire")
		},
		indexed: func() { close(indexed) },
	}
	_, stop, errb := startInProcess(t, h, "-no-open", "-addr", "127.0.0.1:0", "-cache-dir", t.TempDir(), dir)
	// Stop only once the failure has been handled, so the run cannot end
	// (cancelling indexing) before the indexer returned its error.
	select {
	case <-indexed:
	case <-time.After(20 * time.Second):
		t.Fatal("indexing never returned")
	}
	if code := stop(); code != 1 {
		t.Fatalf("exit %d, want 1", code)
	}
	if !strings.Contains(errb.String(), "indexing failed: disk on fire") {
		t.Fatalf("stderr: %s", errb.String())
	}
}

// appleMailDir writes an Apple Mail style export under parent/name: every
// mailbox is a file called "mbox" inside a .mbox directory, so only the
// path relative to the input tells them apart.
func appleMailDir(t *testing.T, parent, name string) {
	t.Helper()
	msg := func(subj string) string {
		return "From: Alice <alice@example.com>\nTo: Bob <bob@example.org>\nSubject: " + subj +
			"\nDate: Mon, 6 Mar 2023 10:00:00 +0000\n\nBody.\n"
	}
	for _, p := range []string{"Inbox.mbox/mbox", "Projects.mbox/mbox", "Projects.mbox/Launch.mbox/mbox"} {
		testutil.WriteMbox(t, filepath.Join(parent, name, "Mailboxes", filepath.FromSlash(p)), msg(p))
	}
}

func TestRelativeInputNamesSources(t *testing.T) {
	parent := t.TempDir()
	appleMailDir(t, parent, "corp")
	t.Chdir(parent)
	for _, input := range []string{"corp", "./corp/", filepath.Join("corp", "Mailboxes", "..")} {
		t.Run(input, func(t *testing.T) {
			base, stop, errb := startInProcess(t, hooks{}, "-no-open", "-addr", "127.0.0.1:0", "-cache-dir", t.TempDir(), input)
			waitFinished(t, base)
			var st struct{ Sources []struct{ Name string } }
			getJSON(t, base+"/api/status", &st)
			var names []string
			for _, s := range st.Sources {
				names = append(names, s.Name)
			}
			sort.Strings(names)
			want := []string{"Mailboxes/Inbox.mbox/mbox", "Mailboxes/Projects.mbox/Launch.mbox/mbox", "Mailboxes/Projects.mbox/mbox"}
			if strings.Join(names, "|") != strings.Join(want, "|") {
				t.Fatalf("source names %q, want %q", names, want)
			}
			if c := stop(); c != 0 {
				t.Fatalf("exit %d: %s", c, errb.String())
			}
		})
	}
	// "." (the most common invocation) from inside the export.
	t.Chdir(filepath.Join(parent, "corp", "Mailboxes"))
	base, stop, _ := startInProcess(t, hooks{}, "-no-open", "-addr", "127.0.0.1:0", "-cache-dir", t.TempDir(), ".")
	waitFinished(t, base)
	var st struct{ Sources []struct{ Name string } }
	getJSON(t, base+"/api/status", &st)
	for _, s := range st.Sources {
		if !strings.HasSuffix(s.Name, ".mbox/mbox") {
			t.Errorf("source name %q", s.Name)
		}
	}
	stop()
}

func TestSkippedInputReported(t *testing.T) {
	parent := t.TempDir()
	appleMailDir(t, parent, "corp")
	fake := filepath.Join(parent, "corp", "files", "fake.pst")
	os.MkdirAll(filepath.Dir(fake), 0o755)
	os.WriteFile(fake, append([]byte("!BDN"), bytes.Repeat([]byte{0x5a}, 600)...), 0o644)
	t.Chdir(parent)
	base, stop, errb := startInProcess(t, hooks{}, "-no-open", "-addr", "127.0.0.1:0", "-cache-dir", t.TempDir(), "corp")
	waitFinished(t, base)
	var st struct {
		InputWarnings     []string
		InputWarningCount int
	}
	getJSON(t, base+"/api/status", &st)
	if st.InputWarningCount != 1 || len(st.InputWarnings) != 1 || !strings.HasPrefix(st.InputWarnings[0], "skipped files/fake.pst: ") ||
		strings.Contains(st.InputWarnings[0], parent) {
		t.Fatalf("input warnings %d %q", st.InputWarningCount, st.InputWarnings)
	}
	stop()
	// stderr names the file once, with the reason.
	line := ""
	for _, l := range strings.Split(errb.String(), "\n") {
		if strings.Contains(l, "fake.pst") {
			line = l
		}
	}
	if line == "" || strings.Count(line, "fake.pst") != 1 || !strings.Contains(line, "warning: skipped ") {
		t.Fatalf("stderr line %q in:\n%s", line, errb.String())
	}
}
