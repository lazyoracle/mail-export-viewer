package sourcetest

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lazyoracle/mail-export-viewer/internal/mail"
	"github.com/lazyoracle/mail-export-viewer/internal/source"
)

// The fake format is a toy used to test consumers of the source contract
// (Discover, the index, the server) without a real format: a file
// "FAKEMAIL\n" followed by one subject per line. Each line is a message
// (Subject, Message-ID "<subject@example.com>", a text body and one
// attachment "a.csv" holding "x,y") located by its byte range; an empty line
// is reported as a warning.
const fakeMagic = "FAKEMAIL\n"

// IsFake sniffs the fake format.
func IsFake(h *source.Head) bool { return bytes.HasPrefix(h.Data, []byte(fakeMagic)) }

// WriteFake writes a fake-format file with the given subjects and returns
// path.
func WriteFake(t testing.TB, path string, subjects ...string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	data := fakeMagic
	for _, s := range subjects {
		data += s + "\n"
	}
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// FakeFormat returns a format named name that claims files for which sniff
// reports true and opens them as fake sources (only files written by
// WriteFake scan successfully).
func FakeFormat(name string, sniff func(*source.Head) bool, perDir bool) source.Format {
	return source.Format{Name: name, Sniff: sniff, PerDir: perDir, Open: func(c source.Candidate) (source.Source, error) {
		folder := c.Rel
		if folder == "" {
			folder = filepath.Base(c.Path)
		}
		return &fakeSource{kind: name, path: c.Path, folder: folder}, nil
	}}
}

type fakeSource struct{ kind, path, folder string }

func (s *fakeSource) Kind() string     { return s.kind }
func (s *fakeSource) Path() string     { return s.path }
func (s *fakeSource) Inputs() []string { return []string{s.path} }
func (s *fakeSource) Close() error     { return nil }

func (s *fakeSource) Scan(ctx context.Context, emit func(source.Entry) error, warn func(error)) error {
	data, err := os.ReadFile(s.path)
	if err != nil {
		return err
	}
	if !bytes.HasPrefix(data, []byte(fakeMagic)) {
		return fmt.Errorf("fake: %s is not a fake file", s.path)
	}
	off := int64(len(fakeMagic))
	sc := bufio.NewScanner(bytes.NewReader(data[off:]))
	for sc.Scan() {
		if err := ctx.Err(); err != nil {
			return err
		}
		line := sc.Text()
		n := int64(len(line) + 1)
		if line == "" {
			warn(fmt.Errorf("%s: empty line at %d", s.path, off))
		} else if err := emit(source.Entry{Folder: s.folder, Loc: source.Locator{Offset: off, Length: n}, Raw: fakeMessage(line)}); err != nil {
			return err
		}
		off += n
	}
	return sc.Err()
}

func fakeMessage(subject string) []byte {
	return []byte("Subject: " + subject + "\nMessage-ID: <" + subject + "@example.com>\n" +
		"Content-Type: multipart/mixed; boundary=B\n\n--B\n\nbody of " + subject +
		"\n--B\nContent-Type: text/csv; name=a.csv\n\nx,y\n--B--\n")
}

func (s *fakeSource) Raw(loc source.Locator) ([]byte, error) {
	if loc.Length > source.MaxMessageBytes {
		return nil, fmt.Errorf("fake: %v: %w", loc, source.ErrTooLarge)
	}
	data, err := os.ReadFile(s.path)
	if err != nil {
		return nil, err
	}
	end := loc.Offset + loc.Length
	if loc.Offset < int64(len(fakeMagic)) || loc.Length < 2 || end > int64(len(data)) ||
		data[end-1] != '\n' || data[loc.Offset-1] != '\n' || bytes.IndexByte(data[loc.Offset:end-1], '\n') >= 0 {
		return nil, fmt.Errorf("fake: %v: %w", loc, source.ErrStale)
	}
	return fakeMessage(strings.TrimSpace(string(data[loc.Offset : end-1]))), nil
}

func (s *fakeSource) Load(loc source.Locator) (*mail.Message, error) {
	raw, err := s.Raw(loc)
	if err != nil {
		return nil, err
	}
	return mail.Parse(raw, source.Entry{}.Date), nil
}

func (s *fakeSource) Attachment(loc source.Locator, i int) (mail.Attachment, []byte, error) {
	raw, err := s.Raw(loc)
	if err != nil {
		return mail.Attachment{}, nil, err
	}
	return mail.AttachmentData(raw, i)
}
