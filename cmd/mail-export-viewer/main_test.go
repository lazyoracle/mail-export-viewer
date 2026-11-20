package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/lazyoracle/mail-export-viewer/internal/index"
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
		{"version", []string{"-version"}, 0, nil, "mail-export-viewer "},
		{"help", []string{"-h"}, 0, nil, "usage:"},
		{"no path", nil, 2, nil, "usage:"},
		{"two paths", []string{"a", "b"}, 2, nil, "exactly one"},
		{"unknown flag", []string{"-bogus", "in"}, 2, nil, "bogus"},
		{"bad addr", []string{"-addr", "nope", "in"}, 2, nil, "invalid -addr"},
		{"remote refused", []string{"-addr", "0.0.0.0:8080", "in"}, 2, nil, "-allow-remote"},
		{"localhost ok", []string{"-addr", "localhost:1", "in"}, -1, nil, ""},
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
		t.Fatal(err)
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
	if runtime.GOOS == "windows" {
		t.Skip()
	}
	tmp := t.TempDir()
	bin := filepath.Join(tmp, "mev")
	build := exec.Command(goBin(), "build", "-o", bin, ".")
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	dir := testutil.MixedDir(t)
	cache := filepath.Join(tmp, "cache")
	// Find a free port to pass explicitly.
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := l.Addr().String()
	l.Close()
	cmd := exec.Command(bin, "-no-open", "-addr", addr, "-cache-dir", cache, dir)
	var errb syncBuffer
	cmd.Stderr = &errb
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	defer cmd.Process.Kill()
	base := "http://" + addr
	deadline := time.Now().Add(30 * time.Second)
	for {
		if resp, err := http.Get(base + "/api/status"); err == nil {
			resp.Body.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("server did not come up: %s", errb.String())
		}
		time.Sleep(50 * time.Millisecond)
	}
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
	if _, err := os.Stat(cache); err != nil {
		t.Fatal("cache dir missing")
	}
}

func goBin() string {
	p := filepath.Join(runtime.GOROOT(), "bin", "go")
	if _, err := os.Stat(p); err == nil {
		return p
	}
	return "go"
}
