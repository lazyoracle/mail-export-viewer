package server

import (
	"context"
	"crypto/hmac"
	"fmt"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/lazyoracle/mail-export-viewer/internal/index"
	"github.com/lazyoracle/mail-export-viewer/internal/mail"
	"github.com/lazyoracle/mail-export-viewer/internal/source"
)

const (
	// maxBodyText caps the text and the HTML (before sanitising) of a
	// message in its JSON; longer bodies are cut and flagged truncated.
	maxBodyText = 8 << 20
	// maxHeaders caps the header fields listed in a message's JSON.
	maxHeaders = 1000
)

// loaded is a message located through the index, with its source. release
// must be called when done: until then the message counts against the
// load budget.
type loaded struct {
	id     int64
	m      index.Message
	src    source.Source
	sem    *byteSem
	weight int64 // budget held; 0 after release
}

// release returns the budget held for l. It may be called more than once.
func (l *loaded) release() {
	if l.weight > 0 {
		l.sem.release(l.weight)
		l.weight = 0
	}
}

// shrink lowers the budget held for l to n bytes (what the request still
// holds once a message has been read and only its result is kept, e.g.
// while a download is written to a slow client).
func (l *loaded) shrink(n int64) {
	n = l.sem.clamp(n)
	if l.weight > n {
		l.sem.release(l.weight - n)
		l.weight = n
	}
}

// loadWeight is the load-budget weight of a message of length bytes read
// factor times over at once. Only mbox byte ranges give a length (the
// reader reads exactly that range); other formats (0) and lengths over the
// limit weigh source.MaxMessageBytes, the most a source materialises for
// one message, so the budget holds whatever the files claim.
func loadWeight(length, factor int64) int64 {
	if length <= 0 || length > source.MaxMessageBytes {
		length = source.MaxMessageBytes
	}
	return length * factor
}

// load finds message idText in the index and its source, and reserves load
// budget for re-reading it (waiting ends when the request is cancelled).
// factor is how many copies of the message the request may hold at once:
// 1 for raw and attachments, 3 for the detail view and cid (Load's parse,
// plus Raw and its parse when cid images are inlined).
func (s *Server) load(r *http.Request, idText string, factor int64) (*loaded, error) {
	id, err := parseID(idText)
	if err != nil {
		return nil, err
	}
	m, err := s.st.Message(id)
	if err != nil {
		return nil, err
	}
	src, ok := s.srcs[m.SourcePath]
	if !ok {
		return nil, errSourceGone
	}
	weight := s.sem.clamp(loadWeight(m.Loc.Length, factor))
	ctx, cancel := context.WithTimeout(r.Context(), s.loadWait)
	defer cancel()
	if err := s.sem.acquire(ctx, weight); err != nil {
		if r.Context().Err() == nil {
			return nil, errBusy
		}
		return nil, err
	}
	return &loaded{id: id, m: m, src: src, sem: s.sem, weight: weight}, nil
}

func (s *Server) handleMessage(w http.ResponseWriter, r *http.Request, idText string) {
	l, err := s.load(r, idText, 3)
	if err != nil {
		s.fail(w, err)
		return
	}
	defer l.release()
	msg, err := l.src.Load(l.m.Loc)
	if err != nil {
		s.fail(w, err)
		return
	}
	m := l.m
	out := detailJSON{
		ID: l.id, ThreadID: m.ThreadID,
		Source:   sourceRefJSON{ID: m.SourceID, Name: s.sourceName(m.SourcePath), Kind: m.SourceKind},
		FolderID: m.FolderID, Folder: m.Folder, MessageID: msg.MessageID, InReplyTo: msg.InReplyTo,
		References: nonNil(msg.References), Subject: msg.Subject, From: addrJSON{msg.From.Name, msg.From.Email},
		To: toAddrs(msg.To), Cc: toAddrs(msg.Cc), Bcc: toAddrs(msg.Bcc), Date: jsonDate(msg.Date),
		Flags: flagNames(m.Flags), Labels: nonNil(m.Labels), Categories: nonNil(m.Categories), Tags: tagsJSON(m.Tags),
		Props: make([]propJSON, 0, len(m.Props)), Headers: make([][2]string, 0, min(len(msg.Header), maxHeaders)),
		Attachments: make([]attachmentJSON, 0, len(msg.Attachments)),
		RawURL:      fmt.Sprintf("/api/messages/%d/raw", l.id), ParseError: m.ParseError,
	}
	if out.Date == nil {
		out.Date = jsonDate(m.Date)
	}
	for _, p := range m.Props {
		out.Props = append(out.Props, propJSON{p.Key, p.Value})
	}
	for i, f := range msg.Header {
		if i == maxHeaders {
			break
		}
		out.Headers = append(out.Headers, [2]string{f.Name, f.Decoded()})
	}
	out.Text, out.TextTruncated = capText(msg.PlainText())
	if msg.HTML != "" {
		var src string
		src, out.HTMLTruncated = capText(msg.HTML)
		inl := &cidInliner{atts: msg.Attachments, part: s.inlinePart, total: s.inlineTotal,
			fetch: func(want map[int]bool) map[int][]byte { return attachmentsData(l.src, l.m.Loc, msg.Attachments, want) }}
		out.BodyHTML, out.RemoteContentBlocked = sanitizeHTMLInline(src, l.id,
			fmt.Sprintf("/api/messages/%d/cid/%s/", l.id, s.cidToken(l.id)), inl.resolve)
	}
	for _, a := range msg.Attachments {
		out.Attachments = append(out.Attachments, attachmentJSON{
			Index: a.Index, Filename: a.Filename, ContentType: a.ContentType, ContentID: a.ContentID, Size: a.Size,
			Inline: a.Inline, URL: fmt.Sprintf("/api/messages/%d/attachments/%d", l.id, a.Index),
		})
	}
	s.writeJSON(w, out)
}

// capText cuts s to maxBodyText bytes on a rune boundary.
func capText(s string) (string, bool) {
	if len(s) <= maxBodyText {
		return s, false
	}
	n := maxBodyText
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n], true
}

// attachmentsData returns the bytes of the wanted attachments of the
// message at loc. When the source's Raw bytes parse to exactly the
// attachments Load reported (always for raw-backed formats), they are all
// decoded in one walk; otherwise each is fetched with Source.Attachment.
func attachmentsData(src source.Source, loc source.Locator, atts []mail.Attachment, want map[int]bool) map[int][]byte {
	if raw, err := src.Raw(loc); err == nil {
		if sameAttachments(mail.Parse(raw, time.Time{}).Attachments, atts) {
			out, _ := mail.AttachmentsData(raw, want)
			return out
		}
	}
	out := map[int][]byte{}
	for i := range want {
		if _, d, err := src.Attachment(loc, i); err == nil {
			out[i] = d
		}
	}
	return out
}

func sameAttachments(a, b []mail.Attachment) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func (s *Server) handleRaw(w http.ResponseWriter, r *http.Request, idText string) {
	w.Header().Add("Content-Security-Policy", "sandbox")
	l, err := s.load(r, idText, 1)
	if err != nil {
		s.fail(w, err)
		return
	}
	defer l.release()
	raw, err := l.src.Raw(l.m.Loc)
	if err != nil {
		s.fail(w, err)
		return
	}
	l.shrink(int64(len(raw)))
	name := truncateBytes(safeFilename(l.m.Subject), 100)
	if name == "" {
		name = fmt.Sprintf("message-%d", l.id)
	}
	writeFile(w, "message/rfc822", contentDisposition("attachment", name+".eml", "message.eml"), raw)
}

func (s *Server) handleAttachment(w http.ResponseWriter, r *http.Request, idText, nText string) {
	w.Header().Add("Content-Security-Policy", "sandbox")
	n, err := parseIndex(nText)
	if err != nil {
		s.fail(w, err)
		return
	}
	q, err := query(r)
	if err != nil {
		s.fail(w, err)
		return
	}
	inline := q.Get("inline")
	if inline != "" && inline != "1" {
		s.fail(w, badRequest(`inline must be "1" or absent`))
		return
	}
	l, err := s.load(r, idText, 1)
	if err != nil {
		s.fail(w, err)
		return
	}
	defer l.release()
	a, data, err := l.src.Attachment(l.m.Loc, n)
	if err != nil {
		s.fail(w, err)
		return
	}
	l.shrink(int64(len(data)))
	serveAttachment(w, a, data, inline == "1")
}

func (s *Server) handleCID(w http.ResponseWriter, r *http.Request, idText, token, cid string) {
	w.Header().Add("Content-Security-Policy", "sandbox")
	if cid == "" || len(cid) > maxCIDLen {
		s.fail(w, badRequest("invalid content id"))
		return
	}
	if token != "" {
		id, err := parseID(idText)
		if err != nil {
			s.fail(w, err)
			return
		}
		if !hmac.Equal([]byte(token), []byte(s.cidToken(id))) {
			writeError(w, http.StatusNotFound, codeNotFound, "not found")
			return
		}
		w.Header().Set("Cross-Origin-Resource-Policy", "cross-origin")
	}
	l, err := s.load(r, idText, 3)
	if err != nil {
		s.fail(w, err)
		return
	}
	defer l.release()
	msg, err := l.src.Load(l.m.Loc)
	if err != nil {
		s.fail(w, err)
		return
	}
	a, ok := mail.FindCID(msg.Attachments, cid)
	if !ok {
		s.fail(w, mail.ErrNotFound)
		return
	}
	msg = nil
	a, data, err := l.src.Attachment(l.m.Loc, a.Index)
	if err != nil {
		s.fail(w, err)
		return
	}
	l.shrink(int64(len(data)))
	serveAttachment(w, a, data, true)
}

// inlineImageTypes may be shown inline (when asked); SVG never is, since it
// can carry script.
var inlineImageTypes = setOf("image/png", "image/jpeg", "image/gif", "image/webp", "image/bmp", "image/avif")

func serveAttachment(w http.ResponseWriter, a mail.Attachment, data []byte, wantInline bool) {
	ct := servedType(a.ContentType)
	disp := "attachment"
	if wantInline && inlineImageTypes[ct] {
		disp = "inline"
	}
	writeFile(w, ct, contentDisposition(disp, a.Filename, fmt.Sprintf("attachment-%d", a.Index)), data)
}

// servedTypes are the declared attachment types served as such; anything
// else (HTML, SVG, XML, scripts, unknown) goes out as
// application/octet-stream so no browser treats it as a document.
var servedTypes = setOf(
	"image/png", "image/jpeg", "image/gif", "image/webp", "image/bmp", "image/avif", "image/tiff", "image/heic",
	"application/pdf", "text/plain", "text/csv", "text/calendar",
	"application/zip", "application/gzip", "application/x-gzip", "application/x-7z-compressed",
	"application/x-tar", "application/x-rar-compressed", "application/vnd.rar",
	"application/msword", "application/vnd.ms-excel", "application/vnd.ms-powerpoint", "application/rtf",
	"audio/mpeg", "audio/mp4", "audio/ogg", "audio/wav", "audio/x-wav", "audio/webm", "audio/aac", "audio/flac",
	"video/mp4", "video/webm", "video/ogg", "video/quicktime", "video/mpeg",
)

// servedTypePrefixes cover the Office Open XML and OpenDocument families.
var servedTypePrefixes = []string{
	"application/vnd.openxmlformats-officedocument.", "application/vnd.oasis.opendocument.",
}

// servedType maps a declared Content-Type to the type an attachment is
// served with: parameters dropped, lower-cased, allow-listed.
func servedType(declared string) string {
	mt, _, err := mime.ParseMediaType(declared)
	if err != nil {
		return "application/octet-stream"
	}
	if servedTypes[mt] {
		return mt
	}
	for _, p := range servedTypePrefixes {
		if strings.HasPrefix(mt, p) && !strings.ContainsAny(mt, "+") {
			return mt
		}
	}
	return "application/octet-stream"
}

func writeFile(w http.ResponseWriter, contentType, disposition string, data []byte) {
	h := w.Header()
	h.Set("Content-Type", contentType)
	h.Set("Content-Disposition", disposition)
	h.Set("Content-Length", strconv.Itoa(len(data)))
	w.Write(data)
}

// contentDisposition builds a header value (RFC 6266) with an ASCII
// filename fallback and an RFC 5987 filename*. The name goes through
// safeFilename first; def is used when nothing is left.
func contentDisposition(kind, name, def string) string {
	name = safeFilename(name)
	if name == "" {
		name = def
	}
	var ascii, ext strings.Builder
	for _, r := range name {
		if r >= 0x20 && r < 0x7f && r != '"' && r != '\\' && r != '%' && r != ';' {
			ascii.WriteRune(r)
		} else {
			ascii.WriteByte('_')
		}
	}
	for _, c := range []byte(name) {
		if isAttrChar(c) {
			ext.WriteByte(c)
		} else {
			fmt.Fprintf(&ext, "%%%02X", c)
		}
	}
	return kind + `; filename="` + ascii.String() + `"; filename*=UTF-8''` + ext.String()
}

// isAttrChar reports RFC 5987 attr-char.
func isAttrChar(c byte) bool {
	switch {
	case 'a' <= c && c <= 'z', 'A' <= c && c <= 'Z', '0' <= c && c <= '9':
		return true
	}
	return strings.IndexByte("!#$&+-.^_`|~", c) >= 0
}

// safeFilename reduces name to a harmless base name: the last path
// element, without control, line-separator and bidi-override characters,
// with characters reserved by shells and file systems (`"<>:|?*;`)
// replaced by '_', without leading/trailing dots and spaces, at most 180
// bytes. It may return "".
func safeFilename(name string) string {
	if i := strings.LastIndexAny(name, `/\`); i >= 0 {
		name = name[i+1:]
	}
	name = strings.ToValidUTF8(name, "")
	name = strings.Map(func(r rune) rune {
		switch {
		case unicode.IsControl(r), r == 0x2028, r == 0x2029, r == 0x200e, r == 0x200f,
			0x202a <= r && r <= 0x202e, 0x2066 <= r && r <= 0x2069, r == 0xfeff:
			return -1
		case strings.ContainsRune(`"<>:|?*;`, r):
			return '_'
		}
		return r
	}, name)
	name = strings.Trim(name, " .")
	return strings.TrimRight(truncateBytes(name, 180), " .")
}

// truncateBytes cuts s to at most n bytes on a rune boundary.
func truncateBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// byteSem is a weighted semaphore over bytes. A request for more than the
// whole budget is clamped to it, so it runs only when nothing else does.
type byteSem struct {
	mu    sync.Mutex
	cap   int64
	used  int64
	freed chan struct{} // closed (and replaced) on every release
}

func newByteSem(capacity int64) *byteSem {
	return &byteSem{cap: capacity, freed: make(chan struct{})}
}

func (s *byteSem) clamp(n int64) int64 { return min(max(n, 1), s.cap) }

// inUse returns the bytes currently held.
func (s *byteSem) inUse() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.used
}

// acquire waits until n bytes are free or ctx is done.
func (s *byteSem) acquire(ctx context.Context, n int64) error {
	n = s.clamp(n)
	for {
		s.mu.Lock()
		if s.used+n <= s.cap {
			s.used += n
			s.mu.Unlock()
			return nil
		}
		freed := s.freed
		s.mu.Unlock()
		select {
		case <-freed:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// release returns n bytes (the same n given to acquire).
func (s *byteSem) release(n int64) {
	n = s.clamp(n)
	s.mu.Lock()
	s.used -= n
	close(s.freed)
	s.freed = make(chan struct{})
	s.mu.Unlock()
}
