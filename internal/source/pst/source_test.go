package pst

import (
	"context"
	"errors"
	"math/rand"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/lazyoracle/mail-export-viewer/internal/mail"
	"github.com/lazyoracle/mail-export-viewer/internal/source"
	"github.com/lazyoracle/mail-export-viewer/internal/source/pst/pstgen"
	"github.com/lazyoracle/mail-export-viewer/internal/source/sourcetest"
)

func writePST(t *testing.T, f *pstgen.File) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "dir", "backup.pst")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, f.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func openSource(t *testing.T, path, rel string) source.Source {
	t.Helper()
	src, err := open(source.Candidate{Format: source.PST, Path: path, Rel: rel})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { src.Close() })
	return src
}

func scan(t *testing.T, src source.Source) ([]source.Entry, []error) {
	t.Helper()
	var es []source.Entry
	var warns []error
	err := src.Scan(context.Background(), func(e source.Entry) error { es = append(es, e); return nil }, func(err error) { warns = append(warns, err) })
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	return es, warns
}

func TestSourceConformance(t *testing.T) {
	f := fixture(pstgen.CryptPermute)
	src := openSource(t, writePST(t, f), "dir/backup.pst")
	sourcetest.Check(t, src, source.Locator{ID: 0x7FFFFFE4}, source.Locator{ID: uint64(f.Folders[0].NID)},
		source.Locator{ID: 0x21}, source.Locator{ID: 1 << 40})
}

func TestSourceScan(t *testing.T) {
	f := fixture(pstgen.CryptNone)
	path := writePST(t, f)
	src := openSource(t, path, "dir/backup.pst")
	if src.Kind() != "pst" || src.Path() != path || !reflect.DeepEqual(src.Inputs(), []string{path}) {
		t.Errorf("Kind/Path/Inputs: %q %q %q", src.Kind(), src.Path(), src.Inputs())
	}
	es, warns := scan(t, src)
	var got []string
	for _, e := range es {
		e.Resolve()
		got = append(got, e.Folder+": "+e.Msg.Subject)
	}
	want := []string{"dir/backup/Inbox: RE: Hello", "dir/backup/Inbox: Café menu", "dir/backup/Inbox/Sub: Rich draft",
		"dir/backup/Inbox/Sub: Plain RTF", "dir/backup/Sent Items: Big body"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("entries:\n%q\nwant\n%q", got, want)
	}
	if len(warns) != 1 || !strings.Contains(warns[0].Error(), "skipped 1 non-mail item") {
		t.Errorf("warnings = %v", warns)
	}
	e := es[0]
	e.Resolve()
	if !reflect.DeepEqual(e.Labels, []string{"Red Category", "Project X"}) || e.Loc != (source.Locator{ID: uint64(f.Folders[0].Messages[0].NID)}) {
		t.Errorf("entry 0: %+v", e)
	}
	if !e.Flags.Has(source.HasAttachment) || e.Msg.Text != "Hello Bob\nSecond line" || e.Msg.From.Email != "alice@example.com" {
		t.Errorf("entry 0 summary: %v %+v", e.Flags, e.Msg)
	}
	if es[1].Flags != source.Unread|source.Starred|source.Important|source.Answered {
		t.Errorf("entry 1 flags = %v", es[1].Flags)
	}
	if !reflect.DeepEqual(es[3].Props, []source.Prop{{Key: "Message class", Value: "IPM.Note.SMIME.MultipartSigned"}}) {
		t.Errorf("entry 3 props = %v", es[3].Props)
	}
	if len(es[4].Msg.Text) > mail.MaxSummaryText {
		t.Errorf("summary text %d bytes", len(es[4].Msg.Text))
	}
	folders := src.(source.FolderLister).Folders()
	if want := []string{"dir/backup/Inbox", "dir/backup/Inbox/Sub", "dir/backup/Sent Items", "dir/backup/Empty"}; !reflect.DeepEqual(folders, want) {
		t.Errorf("Folders = %q", folders)
	}

	raw, err := src.Raw(e.Loc)
	if err != nil {
		t.Fatal(err)
	}
	m := mail.Parse(raw, fixtureDate)
	if m.Subject != "RE: Hello" || m.Header.Get("X-Custom") != "yes" || len(m.Attachments) != 3 || m.Attachments[1].ContentID != "img1@example.com" {
		t.Errorf("Raw parsed back: %q %v %+v", m.Subject, m.Header, m.Attachments)
	}
	if _, data, _ := mail.AttachmentData(raw, 0); len(data) != 20000 {
		t.Errorf("Raw attachment 0: %d bytes", len(data))
	}
}

func TestSourceStem(t *testing.T) {
	path := writePST(t, fixture(pstgen.CryptNone))
	for rel, want := range map[string]string{"": "backup/Inbox", "backup.pst": "backup/Inbox", "a/b.c/x.PST": "a/b.c/x/Inbox", ".pst": "pst/Inbox"} {
		es, _ := scan(t, openSource(t, path, rel))
		if es[0].Folder != want {
			t.Errorf("Rel %q: folder %q, want %q", rel, es[0].Folder, want)
		}
	}
}

func TestSourceCorruptAndHuge(t *testing.T) {
	f := fixture(pstgen.CryptNone)
	inbox := f.Folders[0]
	inbox.Messages = append([]*pstgen.Message{{Corrupt: true}}, inbox.Messages...)
	huge := &pstgen.Message{Props: []pstgen.Prop{pstgen.Str(0x0037, "Huge")},
		Attachments: []*pstgen.Attachment{{Props: []pstgen.Prop{pstgen.Str(0x3707, "big.bin")}, Data: []byte("x"), Size: 300 << 20}}}
	inbox.Messages = append(inbox.Messages, huge)
	src := openSource(t, writePST(t, f), "backup.pst")
	es, warns := scan(t, src)
	if len(es) != 6 || es[0].Msg.Subject != "RE: Hello" || es[2].Msg.Subject != "Huge" {
		t.Fatalf("%d entries", len(es))
	}
	if len(warns) != 2 || !slices.ContainsFunc(warns, func(e error) bool { return strings.Contains(e.Error(), "1 unreadable message") }) {
		t.Errorf("warnings = %v", warns)
	}
	loc := es[2].Loc
	if _, err := src.Load(loc); !errors.Is(err, source.ErrTooLarge) {
		t.Errorf("Load huge = %v", err)
	}
	if _, err := src.Raw(loc); !errors.Is(err, source.ErrTooLarge) {
		t.Errorf("Raw huge = %v", err)
	}
	if _, data, err := src.Attachment(loc, 0); err != nil || string(data) != "x" {
		t.Errorf("Attachment of huge message: %q %v", data, err)
	}
	if _, err := src.Load(source.Locator{ID: uint64(inbox.Messages[0].NID)}); err == nil {
		t.Errorf("Load of corrupt message succeeded")
	}
}

// TestSourceHostile flips random bytes of a whole fixture file and runs
// every entry point: errors and warnings are fine, panics and hangs are
// not.
func TestSourceHostile(t *testing.T) {
	good := fixture(pstgen.CryptPermute)
	good.Folders[1].Messages = nil // keep the file small
	img := good.Bytes()
	dir := t.TempDir()
	live := 0
	rng := rand.New(rand.NewSource(3))
	for iter := 0; iter < 150; iter++ {
		b := append([]byte(nil), img...)
		for k := 0; k < 1+rng.Intn(30); k++ {
			b[dataStartForTest+rng.Intn(len(b)-dataStartForTest)] = byte(rng.Intn(256))
		}
		path := filepath.Join(dir, "h.pst")
		if err := os.WriteFile(path, b, 0o644); err != nil {
			t.Fatal(err)
		}
		src, err := open(source.Candidate{Path: path, Rel: "h.pst"})
		if err != nil {
			continue
		}
		var locs []source.Locator
		src.Scan(context.Background(), func(e source.Entry) error { locs = append(locs, e.Loc); return nil }, func(error) {})
		src.(source.FolderLister).Folders()
		if len(locs) > 0 {
			live++
		}
		for _, l := range locs {
			src.Load(l)
			src.Raw(l)
			for i := 0; i < 4; i++ {
				src.Attachment(l, i)
			}
		}
		src.Close()
	}
	if live < 20 {
		t.Errorf("only %d of 150 corrupted files yielded entries; the test is too weak", live)
	}
	t.Logf("%d of 150 corrupted files still yielded entries", live)
}

func TestSourceRegisteredAndDiscovered(t *testing.T) {
	if !slices.Contains(source.Formats(), "pst") {
		t.Fatalf("pst not registered: %v", source.Formats())
	}
	path := writePST(t, fixture(pstgen.CryptNone))
	d, err := source.Discover(filepath.Dir(filepath.Dir(path)), source.Limits{})
	if err != nil || len(d.Candidates) != 1 || d.Candidates[0].Format != "pst" || d.Candidates[0].Rel != "dir/backup.pst" {
		t.Fatalf("Discover: %+v %v", d, err)
	}
	src, err := d.Candidates[0].Open()
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	if es, _ := scan(t, src); len(es) != 5 {
		t.Errorf("%d entries", len(es))
	}
}

func TestSourceOpenErrors(t *testing.T) {
	dir := t.TempDir()
	bad := filepath.Join(dir, "bad.pst")
	os.WriteFile(bad, []byte("!BDN garbage"), 0o644)
	if _, err := open(source.Candidate{Path: bad}); err == nil {
		t.Errorf("open of a truncated file succeeded")
	}
	if _, err := open(source.Candidate{Path: filepath.Join(dir, "missing.pst")}); err == nil {
		t.Errorf("open of a missing file succeeded")
	}
	// A valid header over a broken node database fails Scan, not Open.
	img := (&pstgen.NDB{}).Bytes()
	path := filepath.Join(dir, "empty.pst")
	os.WriteFile(path, img, 0o644)
	src, err := open(source.Candidate{Path: path})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer src.Close()
	if err := src.Scan(context.Background(), func(source.Entry) error { return nil }, func(error) {}); err == nil {
		t.Errorf("Scan of a store-less file succeeded")
	}
}
