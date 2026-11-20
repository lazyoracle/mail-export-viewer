package e2e

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// The binary under test: $MEV_BIN, or built once from ./cmd/mail-export-viewer.
var (
	binOnce sync.Once
	binPath string
	binErr  error
)

func binary(t testing.TB) string {
	t.Helper()
	binOnce.Do(func() {
		if p := os.Getenv("MEV_BIN"); p != "" {
			binPath = p
			return
		}
		dir, err := os.MkdirTemp("", "mev-e2e-bin")
		if err != nil {
			binErr = err
			return
		}
		binPath = filepath.Join(dir, "mail-export-viewer")
		gobin := "go"
		if root := runtime.GOROOT(); root != "" {
			gobin = filepath.Join(root, "bin", "go") // the toolchain running the test
		}
		cmd := exec.Command(gobin, "build", "-trimpath", "-o", binPath, "../../cmd/mail-export-viewer")
		cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
		if out, err := cmd.CombinedOutput(); err != nil {
			binErr = fmt.Errorf("go build: %v\n%s", err, out)
		}
	})
	if binErr != nil {
		t.Fatal(binErr)
	}
	return binPath
}

// viewer is a running binary.
type viewer struct {
	t      testing.TB
	base   string
	cmd    *exec.Cmd
	stderr *syncBuf
	home   string // HOME, XDG_CACHE_HOME and TMPDIR live below it
	done   chan error
	exited atomic.Bool // set once cmd.Wait has returned
}

type syncBuf struct {
	mu sync.Mutex
	b  strings.Builder
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

var urlRE = regexp.MustCompile(`http://127\.0\.0\.1:\d+/`)

// start runs the binary on input with a private HOME, XDG_CACHE_HOME and
// TMPDIR below home (created if needed), waits for the URL and for indexing
// to finish.
func start(t testing.TB, home, input string, args ...string) *viewer {
	t.Helper()
	bin := binary(t)
	makeEnv(t, home)
	all := append([]string{"-no-open", "-addr", "127.0.0.1:0"}, args...)
	cmd := exec.Command(bin, append(all, input)...)
	cmd.Env = []string{"HOME=" + filepath.Join(home, "home"), "XDG_CACHE_HOME=" + filepath.Join(home, "cache"), // ignored on macOS, see cacheRel

		"TMPDIR=" + filepath.Join(home, "tmp"), "PATH=/usr/bin:/bin", "LANG=C.UTF-8"}
	pr, pw := io.Pipe()
	v := &viewer{t: t, cmd: cmd, stderr: &syncBuf{}, home: home, done: make(chan error, 1)}
	cmd.Stderr = pw
	cmd.Stdout = io.Discard
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	found := make(chan string, 1)
	go func() {
		sc := bufio.NewScanner(pr)
		sc.Buffer(make([]byte, 64<<10), 1<<20)
		sent := false
		for sc.Scan() {
			v.stderr.Write([]byte(sc.Text() + "\n"))
			if m := urlRE.FindString(sc.Text()); m != "" && !sent {
				found <- m
				sent = true
			}
		}
	}()
	go func() { err := cmd.Wait(); v.exited.Store(true); v.done <- err; pw.Close() }()
	t.Cleanup(func() { v.stop() })
	select {
	case v.base = <-found:
	case err := <-v.done:
		t.Fatalf("viewer exited: %v\n%s", err, v.stderr.String())
	case <-time.After(30 * time.Second):
		t.Fatalf("no URL after 30s:\n%s", v.stderr.String())
	}
	v.waitIndexed(10 * time.Minute)
	return v
}

// makeEnv creates the HOME, XDG_CACHE_HOME and TMPDIR of a viewer below
// home.
func makeEnv(t testing.TB, home string) {
	for _, d := range []string{"home", "cache", "tmp"} {
		if err := os.MkdirAll(filepath.Join(home, d), 0o700); err != nil {
			t.Fatal(err)
		}
	}
}

func (v *viewer) stop() {
	if v.exited.Load() {
		return
	}
	v.cmd.Process.Signal(os.Interrupt)
	select {
	case <-v.done:
	case <-time.After(20 * time.Second):
		v.cmd.Process.Kill()
		<-v.done
		v.t.Errorf("viewer did not stop on SIGINT")
	}
}

func (v *viewer) waitIndexed(limit time.Duration) {
	v.t.Helper()
	deadline := time.Now().Add(limit)
	for time.Now().Before(deadline) {
		var st struct {
			Indexing struct {
				Running, Finished bool
				Error             string
			}
		}
		v.getJSON("/api/status", &st)
		if !st.Indexing.Running && st.Indexing.Finished {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	v.t.Fatalf("indexing did not finish within %v", limit)
}

var client = &http.Client{Timeout: 60 * time.Second}

// get returns the response (body read) for path.
func (v *viewer) get(path string) (*http.Response, []byte) {
	v.t.Helper()
	resp, err := client.Get(v.base + strings.TrimPrefix(path, "/"))
	if err != nil {
		v.t.Fatalf("GET %s: %v", path, err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		v.t.Fatalf("GET %s: read: %v", path, err)
	}
	return resp, b
}

func (v *viewer) getJSON(path string, out any) {
	v.t.Helper()
	resp, b := v.get(path)
	if resp.StatusCode != 200 {
		v.t.Fatalf("GET %s: %d %s", path, resp.StatusCode, b)
	}
	if err := json.Unmarshal(b, out); err != nil {
		v.t.Fatalf("GET %s: %v", path, err)
	}
}

// API shapes (only what the tests read).
type apiAddr struct{ Name, Email string }
type apiTag struct{ Name, Color string }
type apiItem struct {
	ID, ThreadID, SourceID, FolderID int64
	ThreadCount                      int
	SourceKind, Folder, Subject      string
	From                             apiAddr
	Date                             *time.Time
	Flags, Labels, Categories        []string
	Tags                             []apiTag
	HasAttachment                    bool
	HighlightHTML                    string `json:"highlightHtml"`
}
type apiList struct {
	Items      []apiItem
	Total      int64
	NextCursor string
}
type apiAtt struct {
	Index                            int
	Filename, ContentType, ContentID string
	Size                             int64
	Inline                           bool
	URL                              string
}
type apiDetail struct {
	apiItem
	MessageID            string
	Text                 string
	BodyHTML             string `json:"bodyHtml"`
	TextTruncated        bool
	HTMLTruncated        bool
	RemoteContentBlocked bool
	Attachments          []apiAtt
	RawURL               string
	ParseError           bool
}

func q(params ...string) string {
	v := url.Values{}
	for i := 0; i+1 < len(params); i += 2 {
		v.Set(params[i], params[i+1])
	}
	return v.Encode()
}

// list pages through /api/messages with the given parameters.
func (v *viewer) list(params ...string) ([]apiItem, int64) {
	v.t.Helper()
	var all []apiItem
	var total int64 = -1
	cursor := ""
	for {
		p := append([]string{"limit", "1000"}, params...)
		if cursor != "" {
			p = append(p, "cursor", cursor)
		}
		var l apiList
		v.getJSON("/api/messages?"+q(p...), &l)
		if total < 0 {
			total = l.Total
		}
		all = append(all, l.Items...)
		if l.NextCursor == "" {
			return all, total
		}
		cursor = l.NextCursor
	}
}

// fileState is what treeState records per file.
type fileState struct {
	Mode  fs.FileMode
	Size  int64
	MTime time.Time
	Hash  string
}

// treeState hashes every entry below root (symlinks not followed).
func treeState(t testing.TB, root string) map[string]fileState {
	t.Helper()
	out := map[string]fileState{}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		fi, err := d.Info()
		if err != nil {
			return err
		}
		st := fileState{Mode: fi.Mode(), Size: fi.Size(), MTime: fi.ModTime()}
		if fi.Mode().IsRegular() {
			f, err := os.Open(p)
			if err != nil {
				return err
			}
			h := sha256.New()
			_, err = io.Copy(h, f)
			f.Close()
			if err != nil {
				return err
			}
			st.Hash = hex.EncodeToString(h.Sum(nil))
		}
		if fi.IsDir() {
			st.MTime = fi.ModTime()
			st.Size = 0
		}
		rel, _ := filepath.Rel(root, p)
		out[rel] = st
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// diffTrees lists entries added, removed or changed between a and b.
func diffTrees(a, b map[string]fileState) []string {
	var out []string
	for k, va := range a {
		vb, ok := b[k]
		switch {
		case !ok:
			out = append(out, "removed "+k)
		case va != vb:
			out = append(out, fmt.Sprintf("changed %s (%+v -> %+v)", k, va, vb))
		}
	}
	for k := range b {
		if _, ok := a[k]; !ok {
			out = append(out, "added "+k)
		}
	}
	sort.Strings(out)
	return out
}

// cacheRel is the cache directory of a viewer started by start, relative to
// the base directory given to start. os.UserCacheDir honours XDG_CACHE_HOME
// on Linux but is always $HOME/Library/Caches on macOS.
func cacheRel() string {
	if runtime.GOOS == "darwin" {
		return filepath.Join("home", "Library", "Caches")
	}
	return "cache"
}

// isCacheEntry reports whether a tree-diff entry ("added x", "changed x (...)")
// is the viewer's own cache directory, something below it, or one of the
// directories leading to it that its creation necessarily touched.
func isCacheEntry(entry string) bool {
	rel := entry[strings.Index(entry, " ")+1:]
	if i := strings.Index(rel, " ("); i >= 0 {
		rel = rel[:i]
	}
	own := filepath.Join(cacheRel(), "mail-export-viewer")
	sep := string(filepath.Separator)
	return rel == own || strings.HasPrefix(rel, own+sep) || strings.HasPrefix(own, rel+sep)
}
