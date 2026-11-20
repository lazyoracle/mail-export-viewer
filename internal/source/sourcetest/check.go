// Package sourcetest helps test implementations and consumers of the
// source contract: Check verifies a Source against the rules documented in
// package source, and the fake format (FakeFormat, WriteFake) gives
// consumers (Discover, index, server tests) a source without a real format.
package sourcetest

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/lazyoracle/mail-export-viewer/internal/mail"
	"github.com/lazyoracle/mail-export-viewer/internal/source"
)

// T is the part of testing.TB Check uses (*testing.T satisfies it).
type T interface {
	Helper()
	Errorf(format string, args ...any)
	Fatalf(format string, args ...any)
}

// Check verifies src against the source contract. src must yield at least
// one Entry (use a fixture with messages, ideally some with attachments).
// stale are locators that do not point at a message of src (e.g. a wrong
// offset, a missing zip entry or node id): Load, Raw and Attachment must
// return an error wrapping source.ErrStale for each.
//
// It checks: Kind, Path and Inputs; Scan is repeatable and stable, honours
// a cancelled context and returns emit's error; every Folder is a valid
// non-empty path; every Locator survives its text form; Load agrees with
// the scanned summary (Subject, Message-ID); Raw is non-empty and within
// MaxMessageBytes; Attachment(loc, i) returns Message.Attachments[i] (same
// Index, Filename, ContentType) and ErrNotFound for out-of-range indices;
// Load is safe for concurrent use with itself and a running Scan (run the
// test with -race to make that meaningful).
func Check(t T, src source.Source, stale ...source.Locator) {
	t.Helper()
	if src.Kind() == "" {
		t.Errorf("Kind is empty")
	}
	if !filepath.IsAbs(src.Path()) {
		t.Errorf("Path %q is not absolute", src.Path())
	}
	if len(src.Inputs()) == 0 {
		t.Errorf("Inputs is empty")
	}
	for _, in := range src.Inputs() {
		if !filepath.IsAbs(in) {
			t.Errorf("Inputs entry %q is not absolute", in)
		}
	}

	entries, err := scanAll(context.Background(), src)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(entries) == 0 {
		t.Fatalf("Scan yielded no entries: use a fixture with messages")
	}
	again, err := scanAll(context.Background(), src)
	if err != nil || len(again) != len(entries) {
		t.Errorf("second Scan: %d entries (first %d), err %v", len(again), len(entries), err)
	} else {
		for i := range entries {
			if again[i].Loc != entries[i].Loc {
				t.Errorf("Scan is not stable: entry %d at %v, then %v", i, entries[i].Loc, again[i].Loc)
				break
			}
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := scanAll(ctx, src); !errors.Is(err, context.Canceled) {
		t.Errorf("Scan with a cancelled context returned %v, want context.Canceled", err)
	}
	stop := errors.New("sourcetest: stop")
	if err := src.Scan(context.Background(), func(source.Entry) error { return stop }, func(error) {}); !errors.Is(err, stop) {
		t.Errorf("Scan did not return emit's error: %v", err)
	}

	for i, e := range entries {
		checkEntry(t, src, i, e)
	}
	for _, loc := range stale {
		if _, err := src.Load(loc); !errors.Is(err, source.ErrStale) {
			t.Errorf("Load(%v) = %v, want ErrStale", loc, err)
		}
		if _, err := src.Raw(loc); !errors.Is(err, source.ErrStale) {
			t.Errorf("Raw(%v) = %v, want ErrStale", loc, err)
		}
		if _, _, err := src.Attachment(loc, 0); !errors.Is(err, source.ErrStale) {
			t.Errorf("Attachment(%v, 0) = %v, want ErrStale", loc, err)
		}
	}
	checkConcurrent(t, src, entries)
}

func scanAll(ctx context.Context, src source.Source) ([]source.Entry, error) {
	var out []source.Entry
	err := src.Scan(ctx, func(e source.Entry) error { out = append(out, e); return nil }, func(error) {})
	return out, err
}

func checkEntry(t T, src source.Source, i int, e source.Entry) {
	t.Helper()
	if !validFolder(e.Folder) {
		t.Errorf("entry %d: bad Folder %q (must be non-empty, valid UTF-8, '/'-separated without empty segments)", i, e.Folder)
	}
	text, _ := e.Loc.MarshalText()
	var back source.Locator
	if err := back.UnmarshalText(text); err != nil || back != e.Loc {
		t.Errorf("entry %d: Locator %+v does not survive its text form %q", i, e.Loc, text)
	}
	e.Resolve()
	m, err := src.Load(e.Loc)
	if err != nil {
		t.Errorf("entry %d: Load(%v): %v", i, e.Loc, err)
		return
	}
	if m.Subject != e.Msg.Subject {
		t.Errorf("entry %d: Load disagrees with Scan: Subject %q vs %q", i, m.Subject, e.Msg.Subject)
	}
	if m.MessageID != e.Msg.MessageID {
		t.Errorf("entry %d: Load disagrees with Scan: MessageID %q vs %q", i, m.MessageID, e.Msg.MessageID)
	}
	raw, err := src.Raw(e.Loc)
	if err != nil || len(raw) == 0 || len(raw) > source.MaxMessageBytes {
		t.Errorf("entry %d: Raw(%v) returned %d bytes, err %v", i, e.Loc, len(raw), err)
	}
	for j, a := range m.Attachments {
		got, _, err := src.Attachment(e.Loc, j)
		if err != nil {
			t.Errorf("entry %d: Attachment(%d): %v", i, j, err)
			continue
		}
		if got.Index != j || got.Filename != a.Filename || got.ContentType != a.ContentType {
			t.Errorf("entry %d: Attachment(%d) = %+v, does not match Message.Attachments[%d] = %+v (Index/order)", i, j, got, j, a)
		}
	}
	for _, j := range []int{len(m.Attachments), -1} {
		if _, _, err := src.Attachment(e.Loc, j); !errors.Is(err, mail.ErrNotFound) {
			t.Errorf("entry %d: Attachment(%d) = %v, want ErrNotFound", i, j, err)
		}
	}
}

func validFolder(f string) bool {
	if f == "" || !utf8.ValidString(f) {
		return false
	}
	for _, seg := range strings.Split(f, "/") {
		if seg == "" {
			return false
		}
	}
	return true
}

func checkConcurrent(t T, src source.Source, entries []source.Entry) {
	t.Helper()
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		if _, err := scanAll(context.Background(), src); err != nil {
			t.Errorf("concurrent Scan: %v", err)
		}
	}()
	for g := 0; g < 4; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for _, e := range entries {
				if _, err := src.Load(e.Loc); err != nil {
					t.Errorf("concurrent Load(%v): %v", e.Loc, err)
					return
				}
			}
		}()
	}
	wg.Wait()
}
