package source_test

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/lazyoracle/mail-export-viewer/internal/source"
	"github.com/lazyoracle/mail-export-viewer/internal/source/sourcetest"
)

var (
	mboxData = []byte("From a@example.com Mon Jan  1 00:00:00 2024\nSubject: a\n\nhello\n")
	emlData  = []byte("Return-Path: <a@example.com>\nFrom: A <a@example.com>\nSubject: hi\n\nbody\n")
	morkData = []byte("// <!-- <mdb:mork:z v=\"1.4\"/> -->\n")
)

func write(t *testing.T, path string, data []byte) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func useAllFakes(t *testing.T) {
	source.UseFormats(t,
		sourcetest.FakeFormat("fake", sourcetest.IsFake, false),
		sourcetest.FakeFormat(source.Mbox, source.IsMbox, false),
		sourcetest.FakeFormat(source.EML, source.IsEML, true),
	)
}

type cand struct{ FormatName, Rel, Path string }

func summarize(root string, cs []source.Candidate) []cand {
	var out []cand
	for _, c := range cs {
		rel, _ := filepath.Rel(root, c.Path)
		out = append(out, cand{c.Format, c.Rel, filepath.ToSlash(rel)})
	}
	return out
}

func TestDiscoverDirectory(t *testing.T) {
	useAllFakes(t)
	dir := t.TempDir()
	write(t, filepath.Join(dir, "Inbox"), mboxData)
	write(t, filepath.Join(dir, "Inbox.msf"), morkData)
	write(t, filepath.Join(dir, "Inbox.sbd", "Work"), mboxData)
	write(t, filepath.Join(dir, "Inbox.sbd", "Work.msf"), morkData)
	write(t, filepath.Join(dir, "Inbox.sbd", "Work.sbd", "Deep.mbox"), mboxData)
	write(t, filepath.Join(dir, "Takeout", "Mail", "All mail.mbox"), mboxData)
	write(t, filepath.Join(dir, "photo.jpg"), []byte{0xff, 0xd8, 0xff, 0xe0})
	write(t, filepath.Join(dir, "notes.txt"), []byte("hello\nFrom here\n"))
	write(t, filepath.Join(dir, "empty"), nil)
	write(t, filepath.Join(dir, ".hidden"), mboxData)
	write(t, filepath.Join(dir, ".git", "objects"), mboxData)
	write(t, filepath.Join(dir, "box.fake"), []byte("FAKEMAIL\none\n"))
	write(t, filepath.Join(dir, "emls", "b.eml"), emlData)
	write(t, filepath.Join(dir, "emls", "a.eml"), emlData)
	write(t, filepath.Join(dir, "emls", "readme.txt"), []byte("Subject: not mail\n"))
	write(t, filepath.Join(dir, "root.eml"), emlData)

	d, err := source.Discover(dir, source.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	want := []cand{
		{source.EML, "", "."},
		{source.Mbox, "Inbox", "Inbox"},
		{source.Mbox, "Inbox.sbd/Work", "Inbox.sbd/Work"},
		{source.Mbox, "Inbox.sbd/Work.sbd/Deep.mbox", "Inbox.sbd/Work.sbd/Deep.mbox"},
		{source.Mbox, "Takeout/Mail/All mail.mbox", "Takeout/Mail/All mail.mbox"},
		{"fake", "box.fake", "box.fake"},
		{source.EML, "emls", "emls"},
	}
	if got := summarize(dir, d.Candidates); !reflect.DeepEqual(got, want) {
		t.Fatalf("candidates =\n%v\nwant\n%v", got, want)
	}
	if d.Root != dir || len(d.Warnings) != 0 {
		t.Errorf("Root %q Warnings %q", d.Root, d.Warnings)
	}
	if f := d.Candidates[6].Files; !reflect.DeepEqual(f, []string{filepath.Join(dir, "emls", "a.eml"), filepath.Join(dir, "emls", "b.eml")}) {
		t.Errorf("eml files = %q", f)
	}
	if f := d.Candidates[0].Files; !reflect.DeepEqual(f, []string{filepath.Join(dir, "root.eml")}) {
		t.Errorf("root eml files = %q", f)
	}
	if d.Candidates[1].Files != nil {
		t.Errorf("non-PerDir Files = %q", d.Candidates[1].Files)
	}
}

func TestDiscoverSingleFile(t *testing.T) {
	useAllFakes(t)
	dir := t.TempDir()
	mbox := write(t, filepath.Join(dir, "All mail.mbox"), mboxData)
	msf := write(t, filepath.Join(dir, "All mail.mbox.msf"), morkData)
	orphan := write(t, filepath.Join(dir, "Orphan.msf"), morkData)
	unknown := write(t, filepath.Join(dir, "weird"), []byte("not mail"))
	eml := write(t, filepath.Join(dir, "sub", "one.eml"), emlData)

	for _, in := range []string{mbox, msf} {
		d, err := source.Discover(in, source.Limits{})
		if err != nil {
			t.Fatal(err)
		}
		if got := summarize(dir, d.Candidates); !reflect.DeepEqual(got, []cand{{source.Mbox, "All mail.mbox", "All mail.mbox"}}) {
			t.Errorf("%s: %v", in, got)
		}
		if d.Root != mbox {
			t.Errorf("Root = %q", d.Root)
		}
	}
	d, err := source.Discover(eml, source.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if got := summarize(dir, d.Candidates); !reflect.DeepEqual(got, []cand{{source.EML, "sub", "sub"}}) || !reflect.DeepEqual(d.Candidates[0].Files, []string{eml}) {
		t.Errorf("eml: %v %q", got, d.Candidates[0].Files)
	}
	if _, err := source.Discover(unknown, source.Limits{}); !errors.Is(err, source.ErrUnknownFormat) {
		t.Errorf("unknown: err = %v", err)
	}
	if _, err := source.Discover(orphan, source.Limits{}); err == nil || !strings.Contains(err.Error(), "Orphan") {
		t.Errorf("orphan msf: err = %v", err)
	}
	if _, err := source.Discover(filepath.Join(dir, "nope"), source.Limits{}); err == nil {
		t.Error("missing: want error")
	}
	// Relative input paths are made absolute.
	wd, _ := os.Getwd()
	defer os.Chdir(wd)
	os.Chdir(dir)
	d, err = source.Discover("All mail.mbox", source.Limits{})
	if err != nil || len(d.Candidates) != 1 || !samePath(d.Candidates[0].Path, mbox) {
		t.Errorf("relative: %+v %v", d, err)
	}
}

func TestDiscoverSymlinks(t *testing.T) {
	useAllFakes(t)
	dir := t.TempDir()
	write(t, filepath.Join(dir, "a", "box"), mboxData)
	if err := os.Symlink(dir, filepath.Join(dir, "a", "loop")); err != nil {
		t.Skip("symlinks unsupported:", err)
	}
	os.Symlink(filepath.Join(dir, "a", "box"), filepath.Join(dir, "boxlink"))
	os.Symlink(filepath.Join(dir, "missing"), filepath.Join(dir, "dangling"))
	d, err := source.Discover(dir, source.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	// The looped directory is not re-entered and the symlinked duplicate of
	// a/box is reported once.
	if got := summarize(dir, d.Candidates); !reflect.DeepEqual(got, []cand{{source.Mbox, "a/box", "a/box"}}) {
		t.Fatalf("candidates = %v", got)
	}
}

func TestDiscoverLimits(t *testing.T) {
	useAllFakes(t)
	dir := t.TempDir()
	write(t, filepath.Join(dir, "top"), mboxData)
	write(t, filepath.Join(dir, "a", "one"), mboxData)
	write(t, filepath.Join(dir, "a", "b", "two"), mboxData)
	d, err := source.Discover(dir, source.Limits{MaxDepth: 1})
	if err != nil {
		t.Fatal(err)
	}
	if got := summarize(dir, d.Candidates); !reflect.DeepEqual(got, []cand{{source.Mbox, "a/one", "a/one"}, {source.Mbox, "top", "top"}}) {
		t.Errorf("depth: %v", got)
	}
	if len(d.Warnings) != 1 || !strings.Contains(d.Warnings[0], "depth") {
		t.Errorf("depth warnings = %q", d.Warnings)
	}
	for i := 0; i < 10; i++ {
		write(t, filepath.Join(dir, "many", fmt.Sprintf("m%d", i)), mboxData)
	}
	d, err = source.Discover(dir, source.Limits{MaxEntries: 5})
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Candidates) > 5 || len(d.Warnings) != 1 || !strings.Contains(d.Warnings[0], "entries") {
		t.Errorf("entries: %d candidates, warnings %q", len(d.Candidates), d.Warnings)
	}
}

func TestDiscoverNonUTF8Name(t *testing.T) {
	useAllFakes(t)
	dir := t.TempDir()
	p := filepath.Join(dir, "caf\xe9.mbox")
	if err := os.WriteFile(p, mboxData, 0o644); err != nil {
		t.Skipf("filesystem rejects non-UTF-8 names: %v", err)
	}
	for _, in := range []string{dir, p} {
		d, err := source.Discover(in, source.Limits{})
		if err != nil || len(d.Candidates) != 1 {
			t.Fatalf("%+v %v", d, err)
		}
		if c := d.Candidates[0]; c.Rel != "caf�.mbox" || c.Path != p {
			t.Errorf("candidate = %+v", c)
		}
	}
}

func TestDiscoverWithoutFormats(t *testing.T) {
	source.UseFormats(t)
	dir := t.TempDir()
	p := write(t, filepath.Join(dir, "box"), mboxData)
	if d, err := source.Discover(dir, source.Limits{}); err != nil || len(d.Candidates) != 0 {
		t.Errorf("%+v %v", d, err)
	}
	if _, err := source.Discover(p, source.Limits{}); !errors.Is(err, source.ErrUnknownFormat) {
		t.Errorf("err = %v", err)
	}
}

func TestRegister(t *testing.T) {
	source.UseFormats(t, sourcetest.FakeFormat("fake", sourcetest.IsFake, false))
	if got := source.Formats(); !reflect.DeepEqual(got, []string{"fake"}) {
		t.Errorf("Formats = %q", got)
	}
	ok := sourcetest.FakeFormat("x", sourcetest.IsFake, false)
	for name, f := range map[string]source.Format{
		"duplicate":  sourcetest.FakeFormat("fake", sourcetest.IsFake, false),
		"empty name": {Sniff: ok.Sniff, Open: ok.Open},
		"nil sniff":  {Name: "y", Open: ok.Open},
		"nil open":   {Name: "z", Sniff: ok.Sniff},
	} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("%s: source.Register did not panic", name)
				}
			}()
			source.Register(f)
		}()
	}
}

func TestDiscoveredFakePassesConformance(t *testing.T) {
	useAllFakes(t)
	dir := t.TempDir()
	sourcetest.WriteFake(t, filepath.Join(dir, "x", "box.fake"), "first", "", "second")
	write(t, filepath.Join(dir, "x", "inbox"), mboxData)
	d, err := source.Discover(dir, source.Limits{})
	if err != nil || len(d.Candidates) != 2 || d.Candidates[0].Format != "fake" {
		t.Fatalf("%+v %v", d, err)
	}
	src, err := d.Candidates[0].Open()
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	sourcetest.Check(t, src, source.Locator{Offset: 9, Length: 3})
	if _, err := (source.Candidate{Format: "nope", Path: dir}).Open(); err == nil {
		t.Error("Open(unregistered): want error")
	}
}

func TestDiscoverConfinesSymlinksToRoot(t *testing.T) {
	useAllFakes(t)
	base := t.TempDir()
	root := filepath.Join(base, "root")
	outside := filepath.Join(base, "outside")
	write(t, filepath.Join(root, "inbox"), mboxData)
	write(t, filepath.Join(root, "sub", "box"), mboxData)
	write(t, filepath.Join(outside, "secret"), mboxData)
	write(t, filepath.Join(outside, "dir", "other"), mboxData)
	if err := os.Symlink(filepath.Join(outside, "secret"), filepath.Join(root, "filelink")); err != nil {
		t.Skip("symlinks unsupported:", err)
	}
	os.Symlink(filepath.Join(outside, "dir"), filepath.Join(root, "dirlink"))
	os.Symlink(filepath.Join(root, "sub"), filepath.Join(root, "insidelink")) // inside: followed; sub is then a duplicate
	os.Symlink(filepath.Join(root, "inbox"), filepath.Join(root, "sub", "inboxlink"))

	d, err := source.Discover(root, source.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if got := summarize(root, d.Candidates); !reflect.DeepEqual(got, []cand{{source.Mbox, "inbox", "inbox"}, {source.Mbox, "insidelink/box", "insidelink/box"}}) {
		t.Errorf("candidates = %v", got)
	}
	w := strings.Join(d.Warnings, "\n")
	if len(d.Warnings) != 2 || !strings.Contains(w, "filelink") || !strings.Contains(w, "dirlink") {
		t.Errorf("warnings = %q", d.Warnings)
	}
	// The root itself may be a symlink (to anywhere).
	link := filepath.Join(base, "rootlink")
	os.Symlink(outside, link)
	d, err = source.Discover(link, source.Limits{})
	if err != nil || len(d.Candidates) != 2 || len(d.Warnings) != 0 {
		t.Errorf("symlinked root: %+v %v", d, err)
	}
	if d.Candidates[0].Path != filepath.Join(link, "dir", "other") {
		t.Errorf("path = %q", d.Candidates[0].Path)
	}
}

// samePath reports whether a and b name the same file once symlinks are
// resolved (the temp directory is itself a symlink on macOS).
func samePath(a, b string) bool {
	ra, err1 := filepath.EvalSymlinks(a)
	rb, err2 := filepath.EvalSymlinks(b)
	return err1 == nil && err2 == nil && ra == rb
}
