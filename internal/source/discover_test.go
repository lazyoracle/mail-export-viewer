package source

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"

	"github.com/lazyoracle/mail-export-viewer/internal/mail"
)

// fakeSource is a toy format proving the registry/Discover/Source contract:
// a file "FAKEMAIL\n" followed by one subject per line; each line is a
// message located by its byte range.
type fakeSource struct {
	kind, path, folder string
	closed             bool
}

func (s *fakeSource) Kind() string     { return s.kind }
func (s *fakeSource) Path() string     { return s.path }
func (s *fakeSource) Inputs() []string { return []string{s.path} }
func (s *fakeSource) Close() error     { s.closed = true; return nil }

func (s *fakeSource) Scan(ctx context.Context, emit func(Entry) error, warn func(error)) error {
	data, err := os.ReadFile(s.path)
	if err != nil {
		return err
	}
	off := int64(len("FAKEMAIL\n"))
	sc := bufio.NewScanner(bytes.NewReader(data[off:]))
	for sc.Scan() {
		if err := ctx.Err(); err != nil {
			return err
		}
		line := sc.Text()
		n := int64(len(line) + 1)
		if line == "" {
			warn(fmt.Errorf("%s: empty line at %d", s.path, off))
		} else if err := emit(Entry{Folder: s.folder, Loc: Locator{Offset: off, Length: n}, Raw: s.msg(line)}); err != nil {
			return err
		}
		off += n
	}
	return nil
}

func (s *fakeSource) msg(subject string) []byte {
	return []byte("Subject: " + subject + "\nContent-Type: multipart/mixed; boundary=B\n\n--B\n\nbody\n--B\nContent-Type: text/csv; name=a.csv\n\nx,y\n--B--\n")
}

func (s *fakeSource) Raw(loc Locator) ([]byte, error) {
	data, err := os.ReadFile(s.path)
	if err != nil {
		return nil, err
	}
	if loc.Offset+loc.Length > int64(len(data)) || data[loc.Offset+loc.Length-1] != '\n' {
		return nil, fmt.Errorf("fake: %v: %w", loc, ErrStale)
	}
	return s.msg(string(data[loc.Offset : loc.Offset+loc.Length-1])), nil
}

func (s *fakeSource) Load(loc Locator) (*mail.Message, error) {
	raw, err := s.Raw(loc)
	if err != nil {
		return nil, err
	}
	return mail.Parse(raw, Entry{}.Date), nil
}

func (s *fakeSource) Attachment(loc Locator, i int) (mail.Attachment, []byte, error) {
	raw, err := s.Raw(loc)
	if err != nil {
		return mail.Attachment{}, nil, err
	}
	return mail.AttachmentData(raw, i)
}

// fakeFormat returns a format that claims files for which is reports true.
func fakeFormat(name string, is func(*Head) bool, perDir bool) Format {
	return Format{Name: name, Sniff: is, PerDir: perDir, Open: func(c Candidate) (Source, error) {
		return &fakeSource{kind: name, path: c.Path, folder: c.Rel}, nil
	}}
}

func isFake(h *Head) bool { return bytes.HasPrefix(h.Data, []byte("FAKEMAIL\n")) }

// useFormats replaces the registry for one test.
func useFormats(t *testing.T, fs ...Format) {
	t.Helper()
	regMu.Lock()
	saved := registry
	registry = map[string]Format{}
	regMu.Unlock()
	t.Cleanup(func() {
		regMu.Lock()
		registry = saved
		regMu.Unlock()
	})
	for _, f := range fs {
		Register(f)
	}
}

func useAllFakes(t *testing.T) {
	useFormats(t,
		fakeFormat("fake", isFake, false),
		fakeFormat(Mbox, IsMbox, false),
		fakeFormat(EML, IsEML, true),
	)
}

type cand struct{ Format, Rel, Path string }

func summarize(root string, cs []Candidate) []cand {
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

	d, err := Discover(dir, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	want := []cand{
		{EML, "", "."},
		{Mbox, "Inbox", "Inbox"},
		{Mbox, "Inbox.sbd/Work", "Inbox.sbd/Work"},
		{Mbox, "Inbox.sbd/Work.sbd/Deep.mbox", "Inbox.sbd/Work.sbd/Deep.mbox"},
		{Mbox, "Takeout/Mail/All mail.mbox", "Takeout/Mail/All mail.mbox"},
		{"fake", "box.fake", "box.fake"},
		{EML, "emls", "emls"},
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
		d, err := Discover(in, Limits{})
		if err != nil {
			t.Fatal(err)
		}
		if got := summarize(dir, d.Candidates); !reflect.DeepEqual(got, []cand{{Mbox, "All mail.mbox", "All mail.mbox"}}) {
			t.Errorf("%s: %v", in, got)
		}
		if d.Root != mbox {
			t.Errorf("Root = %q", d.Root)
		}
	}
	d, err := Discover(eml, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if got := summarize(dir, d.Candidates); !reflect.DeepEqual(got, []cand{{EML, "sub", "sub"}}) || !reflect.DeepEqual(d.Candidates[0].Files, []string{eml}) {
		t.Errorf("eml: %v %q", got, d.Candidates[0].Files)
	}
	if _, err := Discover(unknown, Limits{}); !errors.Is(err, ErrUnknownFormat) {
		t.Errorf("unknown: err = %v", err)
	}
	if _, err := Discover(orphan, Limits{}); err == nil || !strings.Contains(err.Error(), "Orphan") {
		t.Errorf("orphan msf: err = %v", err)
	}
	if _, err := Discover(filepath.Join(dir, "nope"), Limits{}); err == nil {
		t.Error("missing: want error")
	}
	// Relative input paths are made absolute.
	wd, _ := os.Getwd()
	defer os.Chdir(wd)
	os.Chdir(dir)
	d, err = Discover("All mail.mbox", Limits{})
	if err != nil || len(d.Candidates) != 1 || d.Candidates[0].Path != mbox {
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
	d, err := Discover(dir, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	// The looped directory is not re-entered and the symlinked duplicate of
	// a/box is reported once.
	if got := summarize(dir, d.Candidates); !reflect.DeepEqual(got, []cand{{Mbox, "a/box", "a/box"}}) {
		t.Fatalf("candidates = %v", got)
	}
}

func TestDiscoverSkipsSpecialFiles(t *testing.T) {
	useAllFakes(t)
	dir := t.TempDir()
	write(t, filepath.Join(dir, "box"), mboxData)
	fifo := filepath.Join(dir, "fifo")
	if err := syscall.Mkfifo(fifo, 0o644); err != nil {
		t.Skip("mkfifo:", err)
	}
	d, err := Discover(dir, Limits{}) // must not block on the FIFO
	if err != nil || len(d.Candidates) != 1 {
		t.Fatalf("%+v %v", d, err)
	}
	if _, err := Discover(fifo, Limits{}); err == nil {
		t.Error("FIFO as input: want error")
	}
}

func TestDiscoverLimits(t *testing.T) {
	useAllFakes(t)
	dir := t.TempDir()
	write(t, filepath.Join(dir, "top"), mboxData)
	write(t, filepath.Join(dir, "a", "one"), mboxData)
	write(t, filepath.Join(dir, "a", "b", "two"), mboxData)
	d, err := Discover(dir, Limits{MaxDepth: 1})
	if err != nil {
		t.Fatal(err)
	}
	if got := summarize(dir, d.Candidates); !reflect.DeepEqual(got, []cand{{Mbox, "a/one", "a/one"}, {Mbox, "top", "top"}}) {
		t.Errorf("depth: %v", got)
	}
	if len(d.Warnings) != 1 || !strings.Contains(d.Warnings[0], "depth") {
		t.Errorf("depth warnings = %q", d.Warnings)
	}
	for i := 0; i < 10; i++ {
		write(t, filepath.Join(dir, "many", fmt.Sprintf("m%d", i)), mboxData)
	}
	d, err = Discover(dir, Limits{MaxEntries: 5})
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
		d, err := Discover(in, Limits{})
		if err != nil || len(d.Candidates) != 1 {
			t.Fatalf("%+v %v", d, err)
		}
		if c := d.Candidates[0]; c.Rel != "caf�.mbox" || c.Path != p {
			t.Errorf("candidate = %+v", c)
		}
	}
}

func TestCandidateOpenAndSourceContract(t *testing.T) {
	useAllFakes(t)
	dir := t.TempDir()
	write(t, filepath.Join(dir, "x", "box.fake"), []byte("FAKEMAIL\nfirst\n\nsecond\n"))
	write(t, filepath.Join(dir, "x", "inbox"), mboxData)
	d, err := Discover(dir, Limits{})
	if err != nil || len(d.Candidates) != 2 {
		t.Fatalf("%+v %v", d, err)
	}
	c := d.Candidates[0]
	if c.Format != "fake" {
		t.Fatalf("candidate = %+v", c)
	}
	src, err := c.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	if src.Kind() != "fake" || src.Path() != c.Path || !reflect.DeepEqual(src.Inputs(), []string{c.Path}) {
		t.Errorf("source = %s %s %q", src.Kind(), src.Path(), src.Inputs())
	}
	var entries []Entry
	var warns []error
	err = src.Scan(context.Background(), func(e Entry) error { entries = append(entries, e); return nil }, func(err error) { warns = append(warns, err) })
	if err != nil || len(entries) != 2 || len(warns) != 1 {
		t.Fatalf("Scan: %d entries, warns %v, err %v", len(entries), warns, err)
	}
	for i, subject := range []string{"first", "second"} {
		e := entries[i]
		e.Resolve()
		if e.Folder != "x/box.fake" || e.Msg.Subject != subject || !e.Flags.Has(HasAttachment) {
			t.Errorf("entry %d = %+v %+v", i, e, e.Msg)
		}
		// The locator survives the index's text round trip.
		var loc Locator
		if err := loc.UnmarshalText([]byte(e.Loc.String())); err != nil {
			t.Fatal(err)
		}
		m, err := src.Load(loc)
		if err != nil || m.Subject != subject || len(m.Attachments) != 1 {
			t.Errorf("Load = %+v %v", m, err)
		}
		a, data, err := src.Attachment(loc, 0)
		if err != nil || a.Filename != "a.csv" || string(data) != "x,y" {
			t.Errorf("Attachment = %+v %q %v", a, data, err)
		}
		if _, _, err := src.Attachment(loc, 1); !errors.Is(err, mail.ErrNotFound) {
			t.Errorf("Attachment(1) err = %v", err)
		}
	}
	if _, err := src.Raw(Locator{Offset: 9, Length: 3}); !errors.Is(err, ErrStale) {
		t.Errorf("stale Raw err = %v", err)
	}
	// emit errors stop the scan and are returned as is.
	stop := errors.New("stop")
	if err := src.Scan(context.Background(), func(Entry) error { return stop }, func(error) {}); err != stop {
		t.Errorf("Scan err = %v", err)
	}
	// A candidate whose format is not registered cannot be opened.
	if _, err := (Candidate{Format: "nope", Path: c.Path}).Open(); err == nil {
		t.Error("Open(unregistered): want error")
	}
}

func TestDiscoverWithoutFormats(t *testing.T) {
	useFormats(t)
	dir := t.TempDir()
	p := write(t, filepath.Join(dir, "box"), mboxData)
	if d, err := Discover(dir, Limits{}); err != nil || len(d.Candidates) != 0 {
		t.Errorf("%+v %v", d, err)
	}
	if _, err := Discover(p, Limits{}); !errors.Is(err, ErrUnknownFormat) {
		t.Errorf("err = %v", err)
	}
}

func TestRegister(t *testing.T) {
	useFormats(t, fakeFormat("fake", isFake, false))
	if got := Formats(); !reflect.DeepEqual(got, []string{"fake"}) {
		t.Errorf("Formats = %q", got)
	}
	ok := fakeFormat("x", isFake, false)
	for name, f := range map[string]Format{
		"duplicate":  fakeFormat("fake", isFake, false),
		"empty name": {Sniff: ok.Sniff, Open: ok.Open},
		"nil sniff":  {Name: "y", Open: ok.Open},
		"nil open":   {Name: "z", Sniff: ok.Sniff},
	} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("%s: Register did not panic", name)
				}
			}()
			Register(f)
		}()
	}
}
