package sourcetest

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/lazyoracle/mail-export-viewer/internal/mail"
	"github.com/lazyoracle/mail-export-viewer/internal/source"
)

func openFake(t *testing.T, subjects ...string) (source.Source, string) {
	t.Helper()
	p := WriteFake(t, filepath.Join(t.TempDir(), "box.fake"), subjects...)
	src, err := FakeFormat("fake", IsFake, false).Open(source.Candidate{Format: "fake", Path: p, Rel: "Archive/box.fake"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { src.Close() })
	return src, p
}

func TestCheckPassesOnFake(t *testing.T) {
	src, _ := openFake(t, "first", "second", "third")
	Check(t, src, source.Locator{Offset: 9, Length: 3}, source.Locator{Offset: 1 << 40, Length: 5})
}

func TestFakeRefusesHugeMessages(t *testing.T) {
	src, _ := openFake(t, "x")
	loc := source.Locator{Offset: 9, Length: source.MaxMessageBytes + 1}
	if _, err := src.Raw(loc); !errors.Is(err, source.ErrTooLarge) {
		t.Errorf("Raw err = %v", err)
	}
	if _, err := src.Load(loc); !errors.Is(err, source.ErrTooLarge) {
		t.Errorf("Load err = %v", err)
	}
	if _, _, err := src.Attachment(loc, 0); !errors.Is(err, source.ErrTooLarge) {
		t.Errorf("Attachment err = %v", err)
	}
}

// recorder collects failures; Fatalf ends the calling goroutine like
// testing.T does.
type recorder struct {
	mu   sync.Mutex
	msgs []string
}

func (r *recorder) Helper() {}
func (r *recorder) Errorf(format string, args ...any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.msgs = append(r.msgs, fmt.Sprintf(format, args...))
}
func (r *recorder) Fatalf(format string, args ...any) {
	r.Errorf(format, args...)
	panic(fatal{})
}

type fatal struct{}

func runCheck(src source.Source, stale ...source.Locator) []string {
	r := &recorder{}
	func() {
		defer func() {
			if v := recover(); v != nil {
				if _, ok := v.(fatal); !ok {
					panic(v)
				}
			}
		}()
		Check(r, src, stale...)
	}()
	return r.msgs
}

// broken wraps a working source and breaks one rule.
type broken struct {
	source.Source
	rule string
}

func (b broken) Scan(ctx context.Context, emit func(source.Entry) error, warn func(error)) error {
	return b.Source.Scan(ctx, func(e source.Entry) error {
		switch b.rule {
		case "folder":
			e.Folder = ""
		case "segment":
			e.Folder = "a//b"
		}
		return emit(e)
	}, warn)
}

func (b broken) Load(loc source.Locator) (*mail.Message, error) {
	m, err := b.Source.Load(loc)
	if err == nil && b.rule == "subject" {
		m.Subject = "other"
	}
	return m, err
}

func (b broken) Raw(loc source.Locator) ([]byte, error) {
	if b.rule == "stale" {
		return []byte("x"), nil
	}
	if b.rule == "raw" {
		return nil, nil
	}
	return b.Source.Raw(loc)
}

func (b broken) Attachment(loc source.Locator, i int) (mail.Attachment, []byte, error) {
	if b.rule == "notfound" && i != 0 {
		return mail.Attachment{}, nil, errors.New("some other error")
	}
	a, d, err := b.Source.Attachment(loc, i)
	if b.rule == "index" {
		a.Index++
	}
	return a, d, err
}

func TestCheckCatchesViolations(t *testing.T) {
	for rule, want := range map[string]string{
		"folder":   "Folder",
		"segment":  "Folder",
		"subject":  "Subject",
		"stale":    "ErrStale",
		"raw":      "Raw",
		"notfound": "ErrNotFound",
		"index":    "Index",
	} {
		t.Run(rule, func(t *testing.T) {
			src, _ := openFake(t, "first")
			msgs := runCheck(broken{src, rule}, source.Locator{Offset: 9, Length: 3})
			if len(msgs) == 0 || !strings.Contains(strings.Join(msgs, "\n"), want) {
				t.Errorf("messages = %q, want one mentioning %q", msgs, want)
			}
		})
	}
	// An empty source is a fixture mistake.
	src, _ := openFake(t)
	if msgs := runCheck(src); len(msgs) == 0 {
		t.Error("empty source passed")
	}
}
