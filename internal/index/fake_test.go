package index

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lazyoracle/mail-export-viewer/internal/mail"
	"github.com/lazyoracle/mail-export-viewer/internal/source"
)

// fakeSrc is a scriptable source.Source. Its Path is a real file (so it
// can be fingerprinted); extra inputs may be added.
type fakeSrc struct {
	kind, path string
	inputs     []string
	entries    []source.Entry
	warns      []error
	scanErr    error         // returned by Scan after emitting entries
	failAfter  int           // with scanErr: emit only this many entries first (0 = all)
	block      chan struct{} // when set, Scan waits for it (or ctx) before each entry
	folders    []string
	styles     []source.TagStyle
	panicAt    int  // Scan panics before entry panicAt-1 (0 = never)
	panicMeta  bool // Folders, TagStyles and Inputs panic
	scans      atomic.Int32
}

func newFake(t testing.TB, kind, name string, entries ...source.Entry) *fakeSrc {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte("fake input "+name), 0o644); err != nil {
		t.Fatal(err)
	}
	return &fakeSrc{kind: kind, path: path, inputs: []string{path}, entries: entries}
}

func (f *fakeSrc) Kind() string { return f.kind }
func (f *fakeSrc) Path() string { return f.path }
func (f *fakeSrc) Inputs() []string {
	if f.panicMeta {
		panic("inputs boom")
	}
	return f.inputs
}
func (f *fakeSrc) Close() error { return nil }
func (f *fakeSrc) Folders() []string {
	if f.panicMeta {
		panic("folders boom")
	}
	return f.folders
}
func (f *fakeSrc) TagStyles() []source.TagStyle {
	if f.panicMeta {
		panic("styles boom")
	}
	return f.styles
}

func (f *fakeSrc) Scan(ctx context.Context, emit func(source.Entry) error, warn func(error)) error {
	f.scans.Add(1)
	for _, w := range f.warns {
		warn(w)
	}
	for i, e := range f.entries {
		if f.panicAt > 0 && i == f.panicAt-1 {
			panic("scan boom")
		}
		if f.scanErr != nil && f.failAfter > 0 && i == f.failAfter {
			return f.scanErr
		}
		if f.block != nil {
			select {
			case <-f.block:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := emit(e); err != nil {
			return err
		}
	}
	return f.scanErr
}

var errNoBody = errors.New("fake: no bodies")

func (f *fakeSrc) Load(source.Locator) (*mail.Message, error) { return nil, errNoBody }
func (f *fakeSrc) Raw(source.Locator) ([]byte, error)         { return nil, errNoBody }
func (f *fakeSrc) Attachment(source.Locator, int) (mail.Attachment, []byte, error) {
	return mail.Attachment{}, nil, errNoBody
}

// touch rewrites a fake's input so that its size and mtime change.
func touch(t testing.TB, path string) {
	t.Helper()
	b, _ := os.ReadFile(path)
	if err := os.WriteFile(path, append(b, '+'), 0o644); err != nil {
		t.Fatal(err)
	}
	future := time.Now().Add(time.Hour)
	os.Chtimes(path, future, future)
}

// rawMsg is a small RFC 5322 message.
func rawMsg(i int, subject string) []byte {
	return []byte(fmt.Sprintf("From: Sender %d <sender%d@example.com>\nTo: Rcpt <rcpt@example.com>\n"+
		"Subject: %s\nDate: Mon, 2 Jan 2023 15:04:%02d +0000\nMessage-ID: <m%d@example.com>\n\n"+
		"Body of message %d about %s.\n", i, i, subject, i%60, i, i, subject))
}

// rawEntry is an mbox-like entry (Raw set, Msg nil) located by offset.
func rawEntry(folder string, i int, subject string) source.Entry {
	raw := rawMsg(i, subject)
	return source.Entry{Folder: folder, Raw: raw, Loc: source.Locator{Offset: int64(i * 1000), Length: int64(len(raw))}}
}

// msgEntry is a pst/olm-like entry with its Summary built.
func msgEntry(folder string, id uint64, subject string) source.Entry {
	return source.Entry{Folder: folder, Loc: source.Locator{ID: id}, Msg: &mail.Summary{
		Envelope: mail.Envelope{
			MessageID: fmt.Sprintf("p%d@example.org", id), Subject: subject,
			From: mail.Address{Name: "Olm Sender", Email: "olm@example.org"},
			To:   []mail.Address{{Name: "Rcpt", Email: "rcpt@example.org"}},
			Date: time.Date(2022, 6, 1, 10, 0, int(id%60), 0, time.UTC),
		},
		Text: "Synthetic body for " + subject,
	}}
}

// index runs IndexSources and returns the final progress report.
func index(t testing.TB, s *Store, srcs ...source.Source) Progress {
	t.Helper()
	var last Progress
	calls := 0
	if err := s.IndexSources(context.Background(), srcs, func(p Progress) { last = p; calls++ }); err != nil {
		t.Fatal(err)
	}
	if calls == 0 || !last.Finished || last.Running {
		t.Fatalf("no final Finished progress (calls=%d, last=%+v)", calls, last)
	}
	return last
}

// allRows lists every message (deleted included when the store does).
func allRows(t testing.TB, s *Store) []Row {
	t.Helper()
	r, err := s.Search(SearchParams{Limit: maxLimit, Sort: "date_asc"})
	if err != nil {
		t.Fatal(err)
	}
	return r.Items
}
