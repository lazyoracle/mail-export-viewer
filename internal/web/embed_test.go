package web

import (
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

var (
	// attrRef matches src= and href= attribute values in HTML.
	attrRef = regexp.MustCompile(`(?i)\b(?:src|href)\s*=\s*"([^"]*)"`)
	// importRef matches static and dynamic ES module specifiers.
	importRef = regexp.MustCompile(`(?m)(?:^\s*import\s+(?:[^'";]*?\s+from\s+)?|^\s*export\s+[^'";]*?\s+from\s+|\bimport\s*\(\s*)['"]([^'"]+)['"]`)
	// cssURL matches url(...) references in CSS.
	cssURL = regexp.MustCompile(`(?i)url\(\s*['"]?([^'")]*)`)
)

func readFile(t *testing.T, name string) string {
	t.Helper()
	b, err := fs.ReadFile(FS(), name)
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(b)
}

// TestIndexAndEveryReferencedAssetExist walks index.html, the stylesheet's
// url() references and the ES module import graph, and checks that every
// file is embedded and every embedded script is reachable.
func TestIndexAndEveryReferencedAssetExist(t *testing.T) {
	page := readFile(t, "index.html")
	var queue []string
	for _, m := range attrRef.FindAllStringSubmatch(page, -1) {
		ref := m[1]
		if ref == "" || strings.HasPrefix(ref, "#") {
			continue
		}
		if strings.Contains(ref, ":") || strings.HasPrefix(ref, "/") {
			t.Errorf("index.html: reference %q is not a relative path", ref)
			continue
		}
		queue = append(queue, path.Clean(ref))
	}
	if len(queue) < 3 {
		t.Fatalf("index.html references too few assets: %v", queue)
	}
	seen := map[string]bool{}
	var sawJS, sawCSS bool
	for len(queue) > 0 {
		p := queue[0]
		queue = queue[1:]
		if seen[p] {
			continue
		}
		seen[p] = true
		if !fs.ValidPath(p) {
			t.Errorf("reference %q escapes the UI root", p)
			continue
		}
		b, err := fs.ReadFile(FS(), p)
		if err != nil {
			t.Errorf("referenced file %s is not embedded: %v", p, err)
			continue
		}
		switch path.Ext(p) {
		case ".js":
			sawJS = true
			for _, m := range importRef.FindAllStringSubmatch(string(b), -1) {
				spec := m[1]
				if !strings.HasPrefix(spec, "./") && !strings.HasPrefix(spec, "../") {
					t.Errorf("%s: bare or absolute import %q", p, spec)
					continue
				}
				queue = append(queue, path.Join(path.Dir(p), spec))
			}
		case ".css":
			sawCSS = true
			for _, m := range cssURL.FindAllStringSubmatch(string(b), -1) {
				if ref := m[1]; ref != "" && !strings.HasPrefix(ref, "data:") {
					queue = append(queue, path.Join(path.Dir(p), ref))
				}
			}
		}
	}
	if !sawJS || !sawCSS {
		t.Errorf("expected both JS and CSS assets, saw %v", seen)
	}
	if !seen["js/app.js"] {
		t.Errorf("js/app.js is not reached from index.html: %v", seen)
	}
	// Every embedded file must be reachable (no dead modules or assets).
	err := fs.WalkDir(FS(), ".", func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && p != "index.html" && !seen[p] {
			t.Errorf("%s is embedded but never referenced", p)
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

// externalAllowed lists the only absolute URLs an embedded file may
// contain: XML namespace names, which are identifiers and never fetched.
var externalAllowed = []string{`xmlns="http://www.w3.org/2000/svg"`}

var (
	scriptTag   = regexp.MustCompile(`(?is)<script\b([^>]*)>`)
	inlineEvent = regexp.MustCompile(`(?i)<[^>]+\son[a-z]+\s*=`)
	// rawHTMLSinks are DOM APIs that parse strings as HTML or code; the UI
	// builds all dynamic content with DOM APIs instead.
	rawHTMLSinks = []string{"innerHTML", "outerHTML", "insertAdjacentHTML", "document.write", "createContextualFragment",
		"DOMParser", "eval(", "new Function", "setTimeout('", "setTimeout(\""}
)

// TestNoExternalResources: the UI runs fully offline under the server's CSP
// (no inline script, no remote anything), and the message frame is never
// given scripts or same-origin.
func TestNoExternalResources(t *testing.T) {
	n := 0
	err := fs.WalkDir(FS(), ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		n++
		s := readFile(t, p)
		l := strings.ToLower(s)
		for _, ok := range externalAllowed {
			l = strings.ReplaceAll(l, strings.ToLower(ok), "")
		}
		for _, bad := range []string{"http://", "https://", "url(//", "@import", "src=\"//", "href=\"//", "ws://", "wss://"} {
			if strings.Contains(l, bad) {
				t.Errorf("%s contains %q", p, bad)
			}
		}
		for _, bad := range []string{"allow-scripts", "allow-same-origin", "allow-top-navigation", "allow-forms", "allow-modals"} {
			if strings.Contains(l, bad) {
				t.Errorf("%s mentions iframe sandbox token %q", p, bad)
			}
		}
		switch path.Ext(p) {
		case ".html":
			for _, m := range scriptTag.FindAllStringSubmatch(s, -1) {
				if !regexp.MustCompile(`(?i)\bsrc\s*=`).MatchString(m[1]) {
					t.Errorf("%s: inline <script> without src (CSP forbids it)", p)
				}
			}
			if inlineEvent.MatchString(s) {
				t.Errorf("%s: inline event handler attribute", p)
			}
			if strings.Contains(l, " style=") || strings.Contains(l, "<style") {
				t.Errorf("%s: inline styles (keep CSS in app.css)", p)
			}
		case ".js":
			for _, sink := range rawHTMLSinks {
				if strings.Contains(s, sink) {
					t.Errorf("%s uses %s", p, sink)
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if n == 0 {
		t.Fatal("no embedded files")
	}
}

// TestExternalCheckCatchesURLs guards the scanner itself.
func TestExternalCheckCatchesURLs(t *testing.T) {
	l := strings.ToLower(`<img src="HTTPS://cdn.example.com/x.png"> <svg xmlns="http://www.w3.org/2000/svg">`)
	for _, ok := range externalAllowed {
		l = strings.ReplaceAll(l, strings.ToLower(ok), "")
	}
	if !strings.Contains(l, "https://") {
		t.Fatal("scanner would miss an upper-case remote URL")
	}
	if strings.Contains(l, "http://") {
		t.Fatal("allow-listed namespace not removed")
	}
}

// TestHiddenAttributeWins: the UI toggles visibility with the hidden
// attribute; any class that sets display would override the UA rule, so
// app.css must re-assert it with !important.
func TestHiddenAttributeWins(t *testing.T) {
	re := regexp.MustCompile(`(?m)^\[hidden\]\s*\{\s*display\s*:\s*none\s*!important\s*;?\s*\}`)
	if !re.MatchString(readFile(t, "app.css")) {
		t.Fatal("app.css lacks `[hidden] { display: none !important }`")
	}
}

// TestFSIsRootedAtStatic: index.html sits at the root; tests, dot-files and
// Go sources are not shipped.
func TestFSIsRootedAtStatic(t *testing.T) {
	if _, err := fs.Stat(FS(), "index.html"); err != nil {
		t.Fatal(err)
	}
	err := fs.WalkDir(FS(), ".", func(p string, d fs.DirEntry, err error) error {
		base := path.Base(p)
		if strings.HasSuffix(p, ".test.js") || strings.HasSuffix(p, ".go") || (p != "." && strings.HasPrefix(base, ".")) {
			t.Errorf("unexpected embedded file: %s", p)
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

// TestAppElementIDsExist: every $('id') app.js looks up is in index.html
// (the node tests use hand-built DOM, so check the real page).
func TestAppElementIDsExist(t *testing.T) {
	js := readFile(t, "js/app.js")
	page := readFile(t, "index.html")
	ids := regexp.MustCompile(`\$\('([\w-]+)'\)`).FindAllStringSubmatch(js, -1)
	if len(ids) < 30 {
		t.Fatalf("found only %d element lookups in app.js", len(ids))
	}
	for _, m := range ids {
		if !strings.Contains(page, `id="`+m[1]+`"`) {
			t.Errorf("app.js looks up #%s, which index.html lacks", m[1])
		}
	}
	for _, kind := range []string{"label", "category", "tag"} {
		for _, id := range []string{"terms-" + kind, "terms-" + kind + "-head", "terms-" + kind + "-panel"} {
			if !strings.Contains(page, `id="`+id+`"`) {
				t.Errorf("index.html lacks #%s", id)
			}
		}
	}
}

// TestJSUnits runs the node unit tests of the UI modules when node is
// installed; the Go build never depends on it.
func TestJSUnits(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not on PATH; skipping JS unit tests (run make test-js)")
	}
	files, err := filepath.Glob(filepath.Join("jstest", "*.test.js"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no JS tests found: %v", err)
	}
	// Read every JS input so `go test` caching notices changes to files
	// only node opens.
	inputs, _ := filepath.Glob(filepath.Join("jstest", "*.js"))
	mods, _ := filepath.Glob(filepath.Join("static", "js", "*.js"))
	for _, f := range append(inputs, mods...) {
		if _, err := os.ReadFile(f); err != nil {
			t.Fatal(err)
		}
	}
	out, err := exec.Command(node, append([]string{"--test"}, files...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("node --test failed: %v\n%s", err, out)
	}
}
