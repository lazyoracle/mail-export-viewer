package mbox

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/lazyoracle/mail-export-viewer/internal/source"
	"github.com/lazyoracle/mail-export-viewer/internal/source/sourcetest"
)

func openEML(t *testing.T, dir, rel string, files ...string) source.Source {
	t.Helper()
	src, err := source.Candidate{Format: source.EML, Path: dir, Rel: rel, Files: files}.Open()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { src.Close() })
	return src
}

func TestRegisteredFormats(t *testing.T) {
	if got := source.Formats(); !reflect.DeepEqual(got, []string{"eml", "mbox"}) {
		t.Errorf("Formats = %q", got)
	}
}

func TestEMLConformance(t *testing.T) {
	dir := t.TempDir()
	a := writeFile(t, filepath.Join(dir, "a.eml"), msgPlain)
	b := writeFile(t, filepath.Join(dir, "b.eml"), msgAtt)
	c := writeFile(t, filepath.Join(dir, "c.eml"), msgGm+"X-Mozilla-Status: 0001\n")
	src := openEML(t, dir, "Mail/Work", a, b, c)
	if src.Kind() != "eml" || src.Path() != dir || !reflect.DeepEqual(src.Inputs(), []string{a, b, c}) {
		t.Errorf("Kind %q Path %q Inputs %q", src.Kind(), src.Path(), src.Inputs())
	}
	sourcetest.Check(t, src,
		source.Locator{Name: "missing.eml"},
		source.Locator{Name: "../a.eml"},
		source.Locator{Name: "sub/a.eml"},
		source.Locator{Name: ""},
		source.Locator{Name: "a.eml/.."},
	)
}

func TestEMLEntries(t *testing.T) {
	dir := t.TempDir()
	withDate := writeFile(t, filepath.Join(dir, "a.eml"), msgPlain)
	noDate := writeFile(t, filepath.Join(dir, "b.eml"), "Subject: undated\nMessage-ID: <u@example.com>\nX-Mozilla-Status: 0000\nX-Gmail-Labels: Inbox\n\nbody\n")
	mtime := time.Date(2022, 5, 6, 7, 8, 9, 0, time.UTC)
	if err := os.Chtimes(noDate, mtime, mtime); err != nil {
		t.Fatal(err)
	}
	es, warns := scan(t, openEML(t, dir, "Archive.sbd/2019", withDate, noDate))
	if len(warns) != 0 || len(es) != 2 {
		t.Fatalf("%d entries, warnings %v", len(es), warns)
	}
	if es[0].Folder != "Archive/2019" || es[0].Loc != (source.Locator{Name: "a.eml"}) || es[0].Msg.Subject != "plain" {
		t.Errorf("first: %+v", es[0])
	}
	if es[1].Flags != source.Unread || !reflect.DeepEqual(es[1].Labels, []string{"Inbox"}) || !es[1].Msg.Date.Equal(mtime) {
		t.Errorf("second: flags %q labels %q date %v", es[1].Flags, es[1].Labels, es[1].Msg.Date)
	}
	// The root directory itself: the folder is its base name.
	es, _ = scan(t, openEML(t, dir, "", withDate))
	if es[0].Folder != filepath.Base(dir) {
		t.Errorf("root folder %q", es[0].Folder)
	}
}

func TestEMLDiscover(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "Mail", "Work", "1.eml"), msgPlain)
	writeFile(t, filepath.Join(root, "Mail", "Work", "2.eml"), msgAtt)
	writeFile(t, filepath.Join(root, "top.eml"), msgGm)
	d, err := source.Discover(root, source.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]int{}
	for _, c := range d.Candidates {
		src, err := c.Open()
		if err != nil {
			t.Fatal(err)
		}
		es, _ := scan(t, src)
		for _, e := range es {
			got[e.Folder]++
		}
	}
	if want := map[string]int{"Mail/Work": 2, filepath.Base(root): 1}; !reflect.DeepEqual(got, want) {
		t.Errorf("folders %v, want %v", got, want)
	}
}

func TestEMLProblems(t *testing.T) {
	dir := t.TempDir()
	good := writeFile(t, filepath.Join(dir, "good.eml"), msgPlain)
	gone := writeFile(t, filepath.Join(dir, "gone.eml"), msgAtt)
	src := openEML(t, dir, "X", good, gone)
	os.Remove(gone)
	es, warns := scan(t, src)
	if len(es) != 1 || len(warns) != 1 || !strings.Contains(warns[0].Error(), "gone.eml") {
		t.Errorf("entries %d, warnings %v", len(es), warns)
	}
	if _, err := src.Load(source.Locator{Name: "gone.eml"}); !errors.Is(err, source.ErrStale) {
		t.Errorf("Load of a removed file: %v", err)
	}
	if _, err := (source.Candidate{Format: source.EML, Path: dir}).Open(); err == nil {
		t.Error("Open without files succeeded")
	}
	if err := src.Scan(canceled(), func(source.Entry) error { return nil }, func(error) {}); !errors.Is(err, context.Canceled) {
		t.Errorf("canceled scan: %v", err)
	}
}

func canceled() context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	return ctx
}

func TestEMLTooLargeAndPrefix(t *testing.T) {
	dir := t.TempDir()
	big := filepath.Join(dir, "big.eml")
	writeFile(t, big, "Subject: big\nMessage-ID: <big@example.com>\n\n"+strings.Repeat("0123456789\n", 1000))
	old := scanPrefix
	scanPrefix = 128
	t.Cleanup(func() { scanPrefix = old })
	src := openEML(t, dir, "X", big)
	es, _ := scan(t, src)
	if len(es) != 1 || es[0].Msg.Subject != "big" {
		t.Fatalf("entries %+v", es)
	}
	if m, err := src.Load(es[0].Loc); err != nil || len(m.Text) < 10000 {
		t.Errorf("Load: %v", err)
	}
	if err := os.Truncate(big, source.MaxMessageBytes+1); err != nil { // sparse
		t.Fatal(err)
	}
	loc := source.Locator{Name: "big.eml"}
	if _, err := src.Load(loc); !errors.Is(err, source.ErrTooLarge) {
		t.Errorf("Load = %v", err)
	}
	if _, err := src.Raw(loc); !errors.Is(err, source.ErrTooLarge) {
		t.Errorf("Raw = %v", err)
	}
	if _, _, err := src.Attachment(loc, 0); !errors.Is(err, source.ErrTooLarge) {
		t.Errorf("Attachment = %v", err)
	}
}
