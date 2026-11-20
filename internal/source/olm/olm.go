package olm

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path"
	"sort"
	"strings"

	"github.com/lazyoracle/mail-export-viewer/internal/mail"
	"github.com/lazyoracle/mail-export-viewer/internal/source"
)

func init() {
	source.Register(source.Format{Name: source.OLM, Sniff: source.IsOLM, Open: open})
}

// Limits against hostile archives. Variables so that tests can lower them.
var (
	// maxEntries is the most zip entries an archive may hold. Real
	// mailboxes have one entry per message and attachment, so 1M covers
	// very large exports while bounding the central directory in memory.
	maxEntries = 1_000_000
	// maxXMLBytes is the most uncompressed bytes of one message document
	// that is read. Attachments are separate entries and the API shows at
	// most 8 MiB of body text, so real documents stay far below it; parsing
	// one costs a few times its size in memory.
	maxXMLBytes int64 = 64 << 20
	// maxScanBytes caps the total uncompressed message XML one Scan reads.
	maxScanBytes int64 = 2 << 30
	// The Scan budget is budgetFactor times the archive size, at least
	// budgetFloor and at most maxScanBytes (see scanBudget).
	budgetFloor, budgetFactor int64 = 16 << 20, 20
	// maxMessageBytes is the most Attachment reads, and the most a
	// synthesised Raw message may be. It equals source.MaxMessageBytes
	// (a variable only so tests can lower it).
	maxMessageBytes int64 = source.MaxMessageBytes
)

// scanBudget is the decompression budget of Scan for an archive of size
// bytes: proportional to the archive (real exports of text compress by
// roughly 5-10x), never below budgetFloor, never above maxScanBytes.
func scanBudget(size int64) int64 {
	b := max(budgetFloor, size*budgetFactor)
	if size > maxScanBytes/max(budgetFactor, 1) {
		b = maxScanBytes
	}
	return min(b, maxScanBytes)
}

type olm struct {
	size   int64
	path   string
	f      *os.File
	zr     *zip.Reader
	stem   string
	byName map[string]*zip.File // raw entry name -> entry (first wins)
	byNorm map[string]*zip.File // normalised lower-case name -> entry
	multi  bool                 // several accounts: keep the account in folder names
	folds  []string
}

func open(c source.Candidate) (source.Source, error) {
	f, err := os.Open(c.Path)
	if err != nil {
		return nil, err
	}
	st, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	zr, err := zip.NewReader(f, st.Size())
	if err != nil {
		f.Close()
		return nil, fmt.Errorf("olm: %s: %w", c.Path, err)
	}
	if len(zr.File) > maxEntries {
		f.Close()
		return nil, fmt.Errorf("olm: %s: %d entries exceed the limit of %d", c.Path, len(zr.File), maxEntries)
	}
	rel := c.Rel
	if rel == "" {
		rel = path.Base(c.Path)
	}
	rel = strings.ToValidUTF8(rel, "�")
	s := &olm{size: st.Size(), path: c.Path, f: f, zr: zr, byName: map[string]*zip.File{}, byNorm: map[string]*zip.File{}}
	s.stem = strings.TrimSuffix(rel, path.Ext(rel))
	if s.stem == "" || strings.HasSuffix(s.stem, "/") {
		s.stem += "olm"
	}
	s.stem = strings.Join(cleanSegs(strings.Split(s.stem, "/")), "/")
	accounts := map[string]bool{}
	for _, e := range zr.File {
		if _, dup := s.byName[e.Name]; !dup {
			s.byName[e.Name] = e
		}
		segs := splitName(e.Name)
		if !hasDotDot(segs) {
			k := strings.ToLower(strings.Join(cleanSegs(segs), "/"))
			if _, dup := s.byNorm[k]; !dup {
				s.byNorm[k] = e
			}
		}
		if a := accountOf(segs); a != "" {
			accounts[a] = true
		}
	}
	s.multi = len(accounts) > 1
	set := map[string]bool{}
	for _, e := range zr.File {
		segs := splitName(e.Name)
		if _, folder, ok := s.classifyMail(segs, e); ok {
			addPrefixes(set, folder)
		} else if strings.HasSuffix(e.Name, "/") {
			if folder, ok := s.folderOfDir(segs); ok {
				addPrefixes(set, folder)
			}
		}
	}
	for f := range set {
		s.folds = append(s.folds, f)
	}
	sort.Strings(s.folds)
	return s, nil
}

func addPrefixes(set map[string]bool, folder string) {
	parts := strings.Split(folder, "/")
	for i := 1; i <= len(parts); i++ {
		set[strings.Join(parts[:i], "/")] = true
	}
}

func (s *olm) Kind() string     { return source.OLM }
func (s *olm) Path() string     { return s.path }
func (s *olm) Inputs() []string { return []string{s.path} }
func (s *olm) Close() error     { return s.f.Close() }

// Folders implements source.FolderLister: every folder of the archive's
// message trees, including empty ones (zip directory entries).
func (s *olm) Folders() []string { return append([]string(nil), s.folds...) }

func splitName(name string) []string {
	return strings.Split(strings.ReplaceAll(name, `\`, "/"), "/")
}

func hasDotDot(segs []string) bool {
	for _, g := range segs {
		if g == ".." {
			return true
		}
	}
	return false
}

// cleanSegs drops empty, "." and ".." segments and makes the rest valid
// UTF-8: nothing derived from an archive name can leave its tree.
func cleanSegs(segs []string) []string {
	var out []string
	for _, g := range segs {
		g = strings.TrimSpace(strings.ToValidUTF8(g, "�"))
		if g == "" || g == "." || g == ".." {
			continue
		}
		out = append(out, g)
	}
	return out
}

func accountOf(segs []string) string {
	for i, g := range segs {
		if strings.EqualFold(g, "accounts") && i+1 < len(segs)-1 {
			return segs[i+1]
		}
	}
	return ""
}

func isMessagesDir(g string) bool {
	g = strings.ToLower(g)
	return g == "messages" || g == "com.microsoft.__messages"
}

// nonMail kinds recognised by path or extension.
func nonMailKind(segs []string) string {
	base := strings.ToLower(segs[len(segs)-1])
	switch path.Ext(base) {
	case ".olk15contact":
		return "contact"
	case ".olk15event":
		return "calendar"
	case ".olk15task":
		return "task"
	case ".olk15note":
		return "note"
	case ".xml", ".olk15":
	default:
		if !strings.HasPrefix(path.Ext(base), ".olk15") {
			return ""
		}
	}
	for _, g := range segs[:len(segs)-1] {
		g = strings.ToLower(g)
		switch {
		case strings.Contains(g, "contact"):
			return "contact"
		case strings.Contains(g, "calendar"), strings.Contains(g, "event"):
			return "calendar"
		case strings.Contains(g, "task"):
			return "task"
		case strings.Contains(g, "note"):
			return "note"
		}
	}
	return ""
}

// classifyMail reports whether entry e (split name segs) is a message
// document, and its folder.
func (s *olm) classifyMail(segs []string, e *zip.File) (account, folder string, ok bool) {
	if strings.HasSuffix(e.Name, "/") || e.FileInfo().IsDir() {
		return "", "", false
	}
	ext := strings.ToLower(path.Ext(segs[len(segs)-1]))
	if ext != ".xml" && ext != ".olk15message" {
		return "", "", false
	}
	idx := -1
	for i, g := range segs[:len(segs)-1] {
		if isMessagesDir(g) {
			idx = i
			break
		}
	}
	if idx < 0 {
		return "", "", false
	}
	return accountOf(segs), s.folderName(segs, idx, len(segs)-1), true
}

// folderOfDir is the folder of a zip directory entry below a messages
// directory.
func (s *olm) folderOfDir(segs []string) (string, bool) {
	segs = cleanSegsKeepDots(segs)
	for i, g := range segs {
		if isMessagesDir(g) {
			if i+1 >= len(segs) {
				return "", false
			}
			return s.folderName(segs, i, len(segs)), true
		}
	}
	return "", false
}

func cleanSegsKeepDots(segs []string) []string {
	var out []string
	for _, g := range segs {
		if g != "" {
			out = append(out, g)
		}
	}
	return out
}

// folderName builds "<stem>[/<account>]/<segs[idx+1:end]>".
func (s *olm) folderName(segs []string, idx, end int) string {
	parts := []string{s.stem}
	if s.multi {
		if a := cleanSegs([]string{accountOf(segs)}); len(a) > 0 {
			parts = append(parts, a...)
		}
	}
	parts = append(parts, cleanSegs(segs[idx+1:end])...)
	return strings.Join(parts, "/")
}

// errBudget: a document is larger than what is left of the scan budget.
var errBudget = errors.New("decompression budget used up")

// readXML reads and interprets the message document e, reading at most
// limit bytes of it (errBudget beyond; ErrTooLarge beyond maxXMLBytes).
func (s *olm) readXML(e *zip.File, limit int64) (p *parsed, ok bool, n int64, err error) {
	defer func() {
		if r := recover(); r != nil {
			p, ok, err = nil, false, fmt.Errorf("olm: %s: internal error: %v", e.Name, r)
		}
	}()
	if int64(e.UncompressedSize64) > maxXMLBytes {
		return nil, false, maxXMLBytes, fmt.Errorf("olm: %s: %w", e.Name, source.ErrTooLarge)
	}
	rc, err := e.Open()
	if err != nil {
		return nil, false, 0, fmt.Errorf("olm: %s: %w", e.Name, err)
	}
	defer rc.Close()
	// The declared size is not trusted: only actual bytes count.
	limit = min(limit, maxXMLBytes)
	// Sized once from the declared size (bounded by limit), so a big
	// document is not copied over and over while it grows.
	buf := bytes.NewBuffer(make([]byte, 0, min(int64(e.UncompressedSize64), limit)+1+bytes.MinRead))
	_, err = buf.ReadFrom(io.LimitReader(rc, limit+1))
	data := buf.Bytes()
	if err != nil {
		return nil, false, int64(len(data)), fmt.Errorf("olm: %s: %w", e.Name, err)
	}
	if int64(len(data)) > limit {
		if limit == maxXMLBytes {
			return nil, false, int64(len(data)), fmt.Errorf("olm: %s: %w", e.Name, source.ErrTooLarge)
		}
		return nil, false, int64(len(data)), fmt.Errorf("olm: %s: %w", e.Name, errBudget)
	}
	root, err := parseXML(bytes.NewReader(data))
	if err != nil {
		return nil, false, int64(len(data)), fmt.Errorf("olm: %s: %w", e.Name, err)
	}
	p, ok = interpret(root, e.Name, s.resolveSize)
	return p, ok, int64(len(data)), nil
}

// resolve finds the archive entry an attachment URL points at. URLs are
// only ever looked up in the archive, never turned into file paths; a URL
// with ".." segments never resolves.
func (s *olm) resolve(msgName, ref string) *zip.File {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return nil
	}
	cands := []string{ref}
	if u, err := url.PathUnescape(ref); err == nil && u != ref {
		cands = append(cands, u)
	}
	for _, c := range cands {
		c = strings.TrimPrefix(c, "file://")
		segs := splitName(c)
		if hasDotDot(segs) {
			continue
		}
		k := strings.ToLower(strings.Join(cleanSegs(segs), "/"))
		if k == "" {
			continue
		}
		if e, ok := s.byNorm[k]; ok && !e.FileInfo().IsDir() {
			return e
		}
		dir := splitName(msgName)
		rel := strings.ToLower(strings.Join(cleanSegs(append(append([]string(nil), dir[:len(dir)-1]...), segs...)), "/"))
		if e, ok := s.byNorm[rel]; ok && !e.FileInfo().IsDir() && !hasDotDot(dir) {
			return e
		}
	}
	return nil
}

func (s *olm) resolveSize(msgName, ref string) (int64, bool) {
	if e := s.resolve(msgName, ref); e != nil {
		return int64(e.UncompressedSize64), true
	}
	return 0, false
}

// lookup returns the parsed message at loc.
func (s *olm) lookup(loc source.Locator) (*parsed, error) {
	e, ok := s.byName[loc.Name]
	if !ok {
		return nil, fmt.Errorf("olm: %q: %w", loc.Name, source.ErrStale)
	}
	if _, _, ok := s.classifyMail(splitName(e.Name), e); !ok {
		return nil, fmt.Errorf("olm: %q is not a message: %w", loc.Name, source.ErrStale)
	}
	p, ok, _, err := s.readXML(e, maxXMLBytes)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("olm: %q no longer holds a message: %w", loc.Name, source.ErrStale)
	}
	return p, nil
}

func (s *olm) Load(loc source.Locator) (*mail.Message, error) {
	p, err := s.lookup(loc)
	if err != nil {
		return nil, err
	}
	return &p.msg, nil
}

func (s *olm) Raw(loc source.Locator) ([]byte, error) {
	p, err := s.lookup(loc)
	if err != nil {
		return nil, err
	}
	tooLarge := fmt.Errorf("olm: %q: %w", loc.Name, source.ErrTooLarge)
	// The message is written with base64 attachments (4/3 expansion): the
	// limit applies to that output. Declared sizes only give a cheap early
	// refusal; running totals of the bytes actually read enforce it.
	used := int64(len(p.msg.Text)+len(p.msg.HTML)) + 1024
	for _, a := range p.msg.Attachments {
		used += a.Size / 3 * 4
	}
	if used > maxMessageBytes {
		return nil, tooLarge
	}
	used = int64(len(p.msg.Text)+len(p.msg.HTML)) + 1024
	raw, err := mail.EMLBytes(&p.msg, func(i int) ([]byte, error) {
		remaining := (maxMessageBytes - used) / 4 * 3
		if remaining < 0 {
			return nil, tooLarge
		}
		_, d, err := s.attData(loc.Name, p, i, remaining)
		used += int64(len(d))/3*4 + 4
		return d, err
	}, maxMessageBytes, tooLarge)
	if err != nil {
		if errors.Is(err, source.ErrTooLarge) {
			return nil, tooLarge
		}
		return nil, err
	}
	return raw, nil
}

func (s *olm) Attachment(loc source.Locator, index int) (mail.Attachment, []byte, error) {
	p, err := s.lookup(loc)
	if err != nil {
		return mail.Attachment{}, nil, err
	}
	return s.attData(loc.Name, p, index, maxMessageBytes)
}

// attData reads attachment i, refusing (ErrTooLarge) more than limit
// actual bytes whatever the zip header declares.
func (s *olm) attData(msgName string, p *parsed, i int, limit int64) (mail.Attachment, []byte, error) {
	if i < 0 || i >= len(p.msg.Attachments) {
		return mail.Attachment{}, nil, fmt.Errorf("olm: attachment %d: %w", i, mail.ErrNotFound)
	}
	a := p.msg.Attachments[i]
	e := s.resolve(msgName, p.atts[i].url)
	if e == nil {
		return a, nil, nil // dangling reference (listed with Size 0): an empty attachment
	}
	if int64(e.UncompressedSize64) > limit {
		return a, nil, fmt.Errorf("olm: attachment %q: %w", a.Filename, source.ErrTooLarge)
	}
	rc, err := e.Open()
	if err != nil {
		return a, nil, err
	}
	defer rc.Close()
	d, err := io.ReadAll(io.LimitReader(rc, limit+1))
	if err != nil {
		return a, nil, err
	}
	if int64(len(d)) > limit {
		return a, nil, fmt.Errorf("olm: attachment %q: %w", a.Filename, source.ErrTooLarge)
	}
	return a, d, nil
}

type scanResult int

const (
	resEmit    scanResult = iota // an entry was built
	resNotMail                   // the document holds no message
	resBroken                    // unreadable or malformed (already warned)
	resBudget                    // the scan budget ran out inside the document
)

func (s *olm) Scan(ctx context.Context, emit func(source.Entry) error, warn func(error)) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	var (
		read    int64
		budget  = scanBudget(s.size)
		skipped = map[string]int{}
		total   int
		dups    int
		seen    = map[string]bool{}
	)
	for _, e := range s.zr.File {
		if err := ctx.Err(); err != nil {
			return err
		}
		segs := splitName(e.Name)
		_, folder, isMail := s.classifyMail(segs, e)
		if !isMail {
			if !strings.HasSuffix(e.Name, "/") {
				if k := nonMailKind(segs); k != "" {
					skipped[k]++
					total++
				}
			}
			continue
		}
		if seen[e.Name] {
			// Locators address the first entry of a name: a later one
			// would be served as the wrong message.
			dups++
			continue
		}
		seen[e.Name] = true
		var (
			ent source.Entry
			res = resBudget
			n   int64
		)
		if read < budget {
			ent, res, n = s.scanOne(e, folder, budget-read, warn)
			read += n
		}
		if res == resBudget {
			warn(fmt.Errorf("olm: %s: stopped reading messages: decompression budget of %d bytes used up", s.path, budget))
			break
		}
		switch res {
		case resNotMail:
			skipped["other"]++
			total++
		case resEmit:
			if err := emit(ent); err != nil {
				return err
			}
		}
	}
	if dups > 0 {
		warn(fmt.Errorf("olm: %s: skipped %d duplicate entries (same name as an earlier one)", s.path, dups))
	}
	if total > 0 {
		var parts []string
		for _, k := range []string{"contact", "calendar", "task", "note", "other"} {
			if skipped[k] > 0 {
				parts = append(parts, fmt.Sprintf("%d %s", skipped[k], k))
			}
		}
		warn(fmt.Errorf("olm: %s: skipped %d non-mail items (%s)", s.path, total, strings.Join(parts, ", ")))
	}
	return nil
}

// scanOne builds the entry of message document e and reports how many
// actual bytes it read.
func (s *olm) scanOne(e *zip.File, folder string, limit int64, warn func(error)) (source.Entry, scanResult, int64) {
	p, isMsg, n, err := s.readXML(e, limit)
	switch {
	case errors.Is(err, errBudget):
		return source.Entry{}, resBudget, n
	case errors.Is(err, source.ErrTooLarge):
		// Listed, from what we know, so that it shows up; Load refuses it.
		sum := &mail.Summary{Envelope: mail.Envelope{Subject: "(message too large to read)", Date: e.Modified.UTC()}}
		return source.Entry{Folder: folder, Loc: source.Locator{Name: e.Name}, Msg: sum}, resEmit, n
	case err != nil:
		warn(err)
		return source.Entry{}, resBroken, n
	case !isMsg:
		return source.Entry{}, resNotMail, n
	}
	return source.Entry{
		Folder: folder, Labels: p.labels, Flags: p.flags, Props: p.props,
		Loc: source.Locator{Name: e.Name}, Msg: p.msg.Summary(),
	}, resEmit, n
}
