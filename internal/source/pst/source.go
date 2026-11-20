package pst

import (
	"context"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"

	"github.com/lazyoracle/mail-export-viewer/internal/mail"
	"github.com/lazyoracle/mail-export-viewer/internal/source"
)

func init() {
	source.Register(source.Format{Name: source.PST, Sniff: source.IsPST, Open: open})
}

type pstSource struct {
	path string
	stem string // folder prefix: Candidate.Rel without its extension
	f    *os.File
	db   *ndb

	once  sync.Once
	st    *store
	stErr error
}

// open validates the header; the node database is read lazily by Scan.
func open(c source.Candidate) (source.Source, error) {
	f, err := os.Open(c.Path)
	if err != nil {
		return nil, err
	}
	fi, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	db, err := openNDB(f, fi.Size())
	if err != nil {
		f.Close()
		return nil, fmt.Errorf("%s: %w", c.Path, err)
	}
	return &pstSource{path: c.Path, stem: stem(c), f: f, db: db}, nil
}

// stem derives the folder prefix ("dir/backup.pst" -> "dir/backup").
func stem(c source.Candidate) string {
	rel := c.Rel
	if rel == "" {
		rel = filepath.Base(c.Path)
	}
	rel = strings.TrimSuffix(rel, path.Ext(rel))
	var segs []string
	for _, s := range strings.Split(rel, "/") {
		if s != "" {
			segs = append(segs, s)
		}
	}
	if len(segs) == 0 {
		return "pst"
	}
	return strings.Join(segs, "/")
}

func (s *pstSource) Kind() string     { return source.PST }
func (s *pstSource) Path() string     { return s.path }
func (s *pstSource) Inputs() []string { return []string{s.path} }
func (s *pstSource) Close() error     { return s.f.Close() }

func (s *pstSource) store() (*store, error) {
	s.once.Do(func() {
		defer recovered(&s.stErr)
		s.st, s.stErr = openStore(s.db)
		if s.stErr != nil {
			s.stErr = fmt.Errorf("%s: %w", s.path, s.stErr)
		}
	})
	return s.st, s.stErr
}

func (s *pstSource) folderPath(f folderRef) string {
	return strings.Join(append([]string{s.stem}, f.path...), "/")
}

// Folders lists every folder below the IPM subtree, empty ones included.
func (s *pstSource) Folders() (out []string) {
	defer func() {
		if recover() != nil {
			out = nil
		}
	}()
	st, err := s.store()
	if err != nil {
		return nil
	}
	fs, _ := st.folders()
	out = make([]string, 0, len(fs))
	for _, f := range fs {
		out = append(out, s.folderPath(f))
	}
	return out
}

func (s *pstSource) Scan(ctx context.Context, emit func(source.Entry) error, warn func(error)) (err error) {
	defer recovered(&err)
	st, err := s.store()
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	fs, warns := st.folders()
	for _, w := range warns {
		warn(fmt.Errorf("pst: %s: %w", s.path, w))
	}
	// Messages directly in the IPM subtree belong to the stem folder.
	fs = append([]folderRef{{nid: st.ipm}}, fs...)
	var skipped, failed int
	var firstErr error
	for _, f := range fs {
		ids, err := st.contents(f.nid)
		if err != nil {
			warn(fmt.Errorf("pst: %s: folder %q: %w", s.path, s.folderPath(f), err))
			continue
		}
		for _, id := range ids {
			if err := ctx.Err(); err != nil {
				return err
			}
			e, ok, err := s.entry(st, f, id)
			switch {
			case err != nil:
				failed++
				if firstErr == nil {
					firstErr = fmt.Errorf("message %#x: %w", id, err)
				}
				continue
			case !ok:
				skipped++
				continue
			}
			if err := emit(e); err != nil {
				return err
			}
		}
	}
	if failed > 0 {
		warn(fmt.Errorf("pst: %s: %d unreadable message(s) skipped (first: %w)", s.path, failed, firstErr))
	}
	if skipped > 0 {
		warn(fmt.Errorf("pst: %s: skipped %d non-mail item(s) (contacts, appointments, tasks, notes)", s.path, skipped))
	}
	return nil
}

// entry reads message id for Scan; ok is false for non-mail items.
func (s *pstSource) entry(st *store, f folderRef, id uint32) (e source.Entry, ok bool, err error) {
	defer recovered(&err)
	if id&0x1F != nidTypeMessage {
		return e, false, nil
	}
	m, err := st.scanMessage(id)
	if err != nil {
		return e, false, err
	}
	if !isMail(m.class) {
		return e, false, nil
	}
	return source.Entry{
		Folder: s.folderPath(f),
		Labels: m.labels,
		Flags:  m.flags,
		Props:  m.props,
		Loc:    source.Locator{ID: locID(id, m.fp)},
		Msg:    m.Message.Summary(),
	}, true, nil
}

// locatorID validates a locator and returns its ID: the message NID in
// the low 32 bits, its fingerprint in the high 32 bits.
func locatorID(loc source.Locator) (uint64, error) {
	if loc.Offset != 0 || loc.Length != 0 || loc.Name != "" {
		return 0, fmt.Errorf("pst: locator %v: %w", loc, source.ErrStale)
	}
	return loc.ID, nil
}

func recovered(err *error) {
	if r := recover(); r != nil {
		*err = fmt.Errorf("%w: internal error: %v", errCorrupt, r)
	}
}

func (s *pstSource) Load(loc source.Locator) (m *mail.Message, err error) {
	defer recovered(&err)
	id, err := locatorID(loc)
	if err != nil {
		return nil, err
	}
	st, err := s.store()
	if err != nil {
		return nil, err
	}
	msg, err := st.message(id, true)
	if err != nil {
		return nil, err
	}
	return &msg.Message, nil
}

func (s *pstSource) Raw(loc source.Locator) (raw []byte, err error) {
	defer recovered(&err)
	id, err := locatorID(loc)
	if err != nil {
		return nil, err
	}
	st, err := s.store()
	if err != nil {
		return nil, err
	}
	o, err := st.openMessage(id)
	if err != nil {
		return nil, err
	}
	m, err := st.readMessage(o, true)
	if err != nil {
		return nil, err
	}
	return st.writeEML(o, &m.Message, -1)
}

func (s *pstSource) Attachment(loc source.Locator, index int) (a mail.Attachment, data []byte, err error) {
	defer recovered(&err)
	id, err := locatorID(loc)
	if err != nil {
		return a, nil, err
	}
	st, err := s.store()
	if err != nil {
		return a, nil, err
	}
	return st.attachment(id, index)
}
