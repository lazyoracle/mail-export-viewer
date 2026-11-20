package mbox

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/lazyoracle/mail-export-viewer/internal/mail"
	"github.com/lazyoracle/mail-export-viewer/internal/source"
)

func init() {
	source.Register(source.Format{Name: source.Mbox, Sniff: source.IsMbox, Open: open})
}

// scanPrefix is how much of each message Scan keeps for the index (headers
// and the start of the body); a variable so tests can lower it.
var scanPrefix = 4 << 20

// mboxSource is one mbox file. It holds no file handle between calls (a
// directory may hold thousands of mbox files), so Close has nothing to do.
type mboxSource struct {
	path, folder string
	sidecar      source.Sidecar // may be nil
	tagInfo      map[string]source.TagStyle
	sidecarErr   error // from SidecarFor; reported by every Scan
}

func open(c source.Candidate) (source.Source, error) {
	fi, err := os.Stat(c.Path)
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() {
		return nil, fmt.Errorf("mbox: %s is not a regular file", c.Path)
	}
	s := &mboxSource{path: c.Path, folder: folderName(c.Rel, true)}
	if s.folder == "" {
		s.folder = folderName(filepath.Base(c.Path), true)
	}
	// (nil, err): unusable, header metadata only; (sc, err): usable but
	// degraded. Either way err is reported as a warning by Scan.
	s.sidecar, s.sidecarErr = source.SidecarFor(c.Path)
	if s.sidecar != nil {
		s.tagInfo = s.sidecar.TagInfo()
	}
	return s, nil
}

func (s *mboxSource) Kind() string { return source.Mbox }
func (s *mboxSource) Path() string { return s.path }
func (s *mboxSource) Close() error { return nil }

// TagStyles returns the sidecar's tag styles, one per display name (a
// coloured one wins, else the first key in sorted order), sorted by name;
// nil without a sidecar.
func (s *mboxSource) TagStyles() []source.TagStyle {
	keys := make([]string, 0, len(s.tagInfo))
	for k := range s.tagInfo {
		keys = append(keys, k)
	}
	sort.Strings(keys) // deterministic when two keys share a name
	byName := map[string]source.TagStyle{}
	for _, k := range keys {
		ts := s.tagInfo[k]
		if ts.Name == "" {
			continue
		}
		if old, ok := byName[ts.Name]; !ok || old.Color == "" {
			byName[ts.Name] = ts
		}
	}
	if len(byName) == 0 {
		return nil
	}
	out := make([]source.TagStyle, 0, len(byName))
	for _, ts := range byName {
		out = append(out, ts)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func (s *mboxSource) Inputs() []string {
	in := []string{s.path}
	if s.sidecar != nil {
		in = append(in, s.sidecar.Inputs()...)
	}
	return in
}

func (s *mboxSource) Scan(ctx context.Context, emit func(source.Entry) error, warn func(error)) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	f, _, err := openRegular(s.path)
	if err != nil {
		return err
	}
	defer f.Close()
	if s.sidecarErr != nil {
		warn(fmt.Errorf("%s: sidecar: %w", s.path, s.sidecarErr))
	}
	var cbErr error
	emitted := 0
	err = scanMessages(f, scanPrefix, func(m *message) error {
		if cbErr = ctx.Err(); cbErr != nil {
			return cbErr
		}
		e, ok := safeEntry(func() source.Entry { return s.entry(m) }, func(err error) {
			warn(fmt.Errorf("%s: message at offset %d: %w", s.path, m.Offset, err))
		})
		if !ok {
			return nil
		}
		emitted++
		cbErr = emit(e)
		return cbErr
	})
	switch {
	case err == nil:
		return nil
	case cbErr != nil:
		return cbErr
	case emitted > 0: // a read error part-way: keep what was listed
		warn(fmt.Errorf("%s: read error after %d messages, the rest is not listed: %w", s.path, emitted, err))
		return nil
	}
	return err
}

// safeEntry runs build, turning a panic into a warning (the message is then
// skipped).
func safeEntry(build func() source.Entry, warn func(error)) (e source.Entry, ok bool) {
	defer func() {
		if r := recover(); r != nil {
			warn(fmt.Errorf("panic: %v", r))
			e, ok = source.Entry{}, false
		}
	}()
	return build(), true
}

// entry builds the Entry of m: header metadata first, then the sidecar's
// when it knows the message.
func (s *mboxSource) entry(m *message) source.Entry {
	hm := headerMetadata(mail.ParseHeader(m.Raw), s.tagInfo)
	e := source.Entry{
		Folder: s.folder, Labels: hm.Labels, Tags: hm.Tags, Flags: hm.Flags, Props: hm.Props,
		Loc: source.Locator{Offset: m.Offset, Length: m.Length}, Raw: m.Raw, Date: m.Date,
	}
	if s.sidecar != nil {
		if sm, ok := s.sidecar.Lookup(hm.MessageID, m.Offset); ok {
			e.Flags = sm.Flags &^ source.HasAttachment
			e.Tags = appendUnique(e.Tags, sm.Tags...)
			e.Labels = appendUnique(e.Labels, sm.Labels...)
			for _, p := range sm.Props {
				if !containsProp(e.Props, p) {
					e.Props = append(e.Props, p)
				}
			}
		}
	}
	return e
}

func appendUnique(dst []string, add ...string) []string {
	for _, a := range add {
		dup := false
		for _, d := range dst {
			if d == a {
				dup = true
				break
			}
		}
		if !dup {
			dst = append(dst, a)
		}
	}
	return dst
}

func containsProp(ps []source.Prop, p source.Prop) bool {
	for _, q := range ps {
		if q == p {
			return true
		}
	}
	return false
}

// read returns the message at loc, unquoted and without its From_ line, and
// the From_ line date. It checks that loc still delimits a message: a
// separator line at Offset and, unless the range ends at the end of the
// file, a separator line right after it. Anything else is ErrStale; a range
// over MaxMessageBytes is ErrTooLarge (checked before reading).
func (s *mboxSource) read(loc source.Locator) ([]byte, time.Time, error) {
	stale := func(why string) ([]byte, time.Time, error) {
		return nil, time.Time{}, fmt.Errorf("mbox: %s at %d+%d: %s: %w", s.path, loc.Offset, loc.Length, why, source.ErrStale)
	}
	f, fi, err := openRegular(s.path)
	if err != nil {
		return nil, time.Time{}, err
	}
	defer f.Close()
	size := fi.Size()
	if loc.Offset < 0 || loc.Length <= 0 || loc.Offset >= size || loc.Length > size-loc.Offset {
		return stale("outside the file")
	}
	if loc.Length > source.MaxMessageBytes {
		return nil, time.Time{}, fmt.Errorf("mbox: %s at %d: %d bytes: %w", s.path, loc.Offset, loc.Length, source.ErrTooLarge)
	}
	buf := make([]byte, loc.Length)
	if n, err := f.ReadAt(buf, loc.Offset); n < len(buf) {
		if err == nil || err == io.EOF {
			return stale("file shrank while reading")
		}
		return nil, time.Time{}, err
	}
	nl := bytes.IndexByte(buf, '\n')
	if nl < 0 || !isSeparator(buf[:nl+1], true) {
		return stale("no From_ line")
	}
	if end := loc.Offset + loc.Length; end < size {
		next := make([]byte, min(size-end, maxSeparatorLen+2))
		if _, err := f.ReadAt(next, end); err != nil && err != io.EOF {
			return nil, time.Time{}, err
		}
		if i := bytes.IndexByte(next, '\n'); i >= 0 {
			next = next[:i+1]
		}
		prevBlank := bytes.HasSuffix(buf, []byte("\n\n")) || bytes.HasSuffix(buf, []byte("\n\r\n"))
		if buf[len(buf)-1] != '\n' || !isSeparator(next, prevBlank) {
			return stale("range does not end at a From_ line")
		}
	}
	_, date := parseSeparator(buf[:nl+1])
	return trimSeparatorBlank(unquote(buf[nl+1:])), date, nil
}

// openRegular opens path read-only, refusing anything but a regular file
// (a FIFO or device could block or never end). The path is stat-ed before
// opening, so a FIFO is never opened, and the handle is checked again.
func openRegular(path string) (*os.File, os.FileInfo, error) {
	if fi, err := os.Stat(path); err != nil {
		return nil, nil, err
	} else if !fi.Mode().IsRegular() {
		return nil, nil, fmt.Errorf("%s is not a regular file", path)
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	fi, err := f.Stat()
	if err == nil && !fi.Mode().IsRegular() {
		err = fmt.Errorf("%s is not a regular file", path)
	}
	if err != nil {
		f.Close()
		return nil, nil, err
	}
	return f, fi, nil
}

func (s *mboxSource) Raw(loc source.Locator) ([]byte, error) {
	raw, _, err := s.read(loc)
	return raw, err
}

func (s *mboxSource) Load(loc source.Locator) (*mail.Message, error) {
	raw, date, err := s.read(loc)
	if err != nil {
		return nil, err
	}
	return mail.Parse(raw, date), nil
}

func (s *mboxSource) Attachment(loc source.Locator, index int) (mail.Attachment, []byte, error) {
	raw, _, err := s.read(loc)
	if err != nil {
		return mail.Attachment{}, nil, err
	}
	return mail.AttachmentData(raw, index)
}

// folderName turns a Candidate.Rel (raw '/'-separated path below the
// discovery root) into a folder path: Thunderbird's "X.sbd" directories
// become "X", and with stripExt a ".mbox"/".mbx"/".mbs" extension of the last
// element is dropped; Apple Mail's "Name.mbox/mbox" is "Name" (and its
// nested "A.mbox/B.mbox/mbox" is "A/B"). A name that would become empty is
// kept as is.
func folderName(rel string, stripExt bool) string {
	parts := strings.Split(rel, "/")
	// Apple Mail: "Name.mbox/mbox" is the folder "Name", and sub-mailboxes
	// are nested "Name.mbox" directories ("A.mbox/B.mbox/mbox" is "A/B").
	apple := false
	if n := len(parts); stripExt && n >= 2 && strings.EqualFold(parts[n-1], "mbox") {
		if base, ok := cutSuffixFold(parts[n-2], ".mbox"); ok && base != "" {
			parts = parts[:n-1]
			apple = true
		}
	}
	out := parts[:0]
	for i, p := range parts {
		if p == "" {
			continue
		}
		last := i == len(parts)-1
		if base, ok := cutSuffixFold(p, ".sbd"); ok && base != "" && (!last || !stripExt) {
			p = base
		} else if base, ok := cutSuffixFold(p, ".mbox"); ok && base != "" && apple && !last {
			p = base
		} else if last && stripExt {
			for _, ext := range []string{".mbox", ".mbx", ".mbs"} {
				if base, ok := cutSuffixFold(p, ext); ok && base != "" {
					p = base
					break
				}
			}
		}
		out = append(out, p)
	}
	return strings.Join(out, "/")
}

func cutSuffixFold(s, suffix string) (string, bool) {
	if len(s) >= len(suffix) && strings.EqualFold(s[len(s)-len(suffix):], suffix) {
		return s[:len(s)-len(suffix)], true
	}
	return s, false
}
