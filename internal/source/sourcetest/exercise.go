package sourcetest

import (
	"context"
	"fmt"
	"runtime"
	"strings"
	"time"

	"github.com/lazyoracle/mail-export-viewer/internal/source"
)

// Exercise is for fuzz targets and hostile-input tests: it scans src and,
// for at most maxEntries entries, resolves the summary and calls Load, Raw
// and every Attachment (plus one out of range). Errors are expected on
// hostile input and ignored, except those that report a recovered panic
// (formats turn panics into "panic: …" or "internal error" errors and
// warnings): those are returned, so a fuzz target can fail on them. It also
// returns the number of entries scanned.
func Exercise(src source.Source, maxEntries int) (n int, panics []string) {
	note := func(what string, err error) {
		if err == nil {
			return
		}
		if s := err.Error(); strings.Contains(s, "panic") || strings.Contains(s, "internal error") {
			panics = append(panics, what+": "+s)
		}
	}
	var entries []source.Entry
	err := src.Scan(context.Background(), func(e source.Entry) error {
		n++
		if len(entries) < maxEntries {
			e.Resolve()
			entries = append(entries, e)
		}
		return nil
	}, func(err error) { note("scan warning", err) })
	note("scan", err)
	for i, e := range entries {
		m, err := src.Load(e.Loc)
		note(fmt.Sprintf("load %d", i), err)
		_, err = src.Raw(e.Loc)
		note(fmt.Sprintf("raw %d", i), err)
		atts := 1
		if m != nil {
			atts += len(m.Attachments)
		}
		for j := 0; j < atts; j++ {
			_, _, err := src.Attachment(e.Loc, j)
			note(fmt.Sprintf("attachment %d/%d", i, j), err)
		}
	}
	return n, panics
}

// Within runs fn and fails t (with every goroutine's stack, to show where
// it is stuck) if fn has not returned after d. Go's fuzzer has no
// per-input timeout, so fuzz targets wrap their body in Within to turn a
// hang into a reported, minimised failure.
func Within(t T, d time.Duration, fn func()) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		fn()
	}()
	select {
	case <-done:
	case <-time.After(d):
		buf := make([]byte, 1<<20)
		buf = buf[:runtime.Stack(buf, true)]
		t.Fatalf("still running after %v:\n%s", d, buf)
	}
}
