package mbox

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/lazyoracle/mail-export-viewer/internal/mail"
	"github.com/lazyoracle/mail-export-viewer/internal/source"
)

func init() {
	source.Register(source.Format{Name: source.EML, Sniff: source.IsEML, Open: openEMLDir, PerDir: true})
}

// emlSource is the loose .eml files of one directory: each file is one
// message of the directory's folder, located by its file name.
type emlSource struct {
	dir, folder string
	files       []string          // absolute paths, in scan order
	byName      map[string]string // base name -> path; the only names Load may serve
}

func openEMLDir(c source.Candidate) (source.Source, error) {
	if len(c.Files) == 0 {
		return nil, fmt.Errorf("eml: no files in %s", c.Path)
	}
	s := &emlSource{dir: c.Path, folder: folderName(c.Rel, false), files: c.Files, byName: map[string]string{}}
	if s.folder == "" {
		s.folder = folderName(filepath.Base(c.Path), false)
	}
	for _, f := range c.Files {
		s.byName[filepath.Base(f)] = f
	}
	return s, nil
}

func (s *emlSource) Kind() string     { return source.EML }
func (s *emlSource) Path() string     { return s.dir }
func (s *emlSource) Inputs() []string { return append([]string(nil), s.files...) }
func (s *emlSource) Close() error     { return nil }

func (s *emlSource) Scan(ctx context.Context, emit func(source.Entry) error, warn func(error)) error {
	for _, p := range s.files {
		if err := ctx.Err(); err != nil {
			return err
		}
		prefix, mtime, err := readPrefix(p, scanPrefix)
		if err != nil {
			warn(fmt.Errorf("%s: %w", p, err))
			continue
		}
		e, ok := safeEntry(func() source.Entry {
			hm := headerMetadata(mail.ParseHeader(prefix), nil)
			return source.Entry{
				Folder: s.folder, Labels: hm.Labels, Tags: hm.Tags, Flags: hm.Flags, Props: hm.Props,
				Loc: source.Locator{Name: filepath.Base(p)}, Raw: prefix, Date: mtime,
			}
		}, func(err error) { warn(fmt.Errorf("%s: %w", p, err)) })
		if !ok {
			continue
		}
		if err := emit(e); err != nil {
			return err
		}
	}
	return nil
}

// readPrefix returns at most n bytes of the file and its modification time.
func readPrefix(path string, n int) ([]byte, time.Time, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, time.Time{}, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, time.Time{}, err
	}
	buf, err := io.ReadAll(io.LimitReader(f, int64(n)))
	return buf, fi.ModTime(), err
}

// read returns the file at loc and its modification time.
func (s *emlSource) read(loc source.Locator) ([]byte, time.Time, error) {
	p, ok := s.byName[loc.Name]
	if !ok {
		return nil, time.Time{}, fmt.Errorf("eml: %q is not a file of %s: %w", loc.Name, s.dir, source.ErrStale)
	}
	fi, err := os.Stat(p)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, time.Time{}, fmt.Errorf("eml: %s: %w", p, source.ErrStale)
	}
	if err != nil {
		return nil, time.Time{}, err
	}
	if fi.Size() > source.MaxMessageBytes {
		return nil, time.Time{}, fmt.Errorf("eml: %s: %d bytes: %w", p, fi.Size(), source.ErrTooLarge)
	}
	raw, err := os.ReadFile(p)
	return raw, fi.ModTime(), err
}

func (s *emlSource) Raw(loc source.Locator) ([]byte, error) {
	raw, _, err := s.read(loc)
	return raw, err
}

func (s *emlSource) Load(loc source.Locator) (*mail.Message, error) {
	raw, mtime, err := s.read(loc)
	if err != nil {
		return nil, err
	}
	return mail.Parse(raw, mtime), nil
}

func (s *emlSource) Attachment(loc source.Locator, index int) (mail.Attachment, []byte, error) {
	raw, _, err := s.read(loc)
	if err != nil {
		return mail.Attachment{}, nil, err
	}
	return mail.AttachmentData(raw, index)
}
