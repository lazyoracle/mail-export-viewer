package mail

import (
	"errors"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// Envelope is the header data shared by Summary and Message. IDs are stored
// without angle brackets; all strings are valid UTF-8.
type Envelope struct {
	MessageID, InReplyTo string
	References           []string
	Subject              string
	From                 Address // Sender if From is missing
	To, Cc, Bcc          []Address
	Date                 time.Time // the fallback date if the Date header is missing/unparseable
}

// Attachment describes a non-body MIME leaf. Index is the leaf's position
// among attachments in document order and is stable between Parse and the
// *Data functions. Size is the decoded size in bytes.
type Attachment struct {
	Index                            int
	Filename, ContentType, ContentID string
	Size                             int64
	// Inline is set for Content-Disposition: inline, or no disposition and a
	// Content-ID (an embedded image).
	Inline bool
	// NameGenerated is set when the part had no filename and Filename was
	// invented ("attachment-N.ext", or "<inner subject>.eml").
	NameGenerated bool
}

// signatureTypes are machine parts that should not make a message look like
// it carries attachments.
var signatureTypes = map[string]bool{
	"application/pgp-signature":     true,
	"application/pkcs7-signature":   true,
	"application/x-pkcs7-signature": true,
	"message/delivery-status":       true,
}

// Signature reports whether the part is a PGP/S-MIME signature or delivery
// status report (machine data, not something the user attached).
func (a Attachment) Signature() bool { return signatureTypes[a.ContentType] }

// CountsAsAttachment reports whether a should mark its message as "has
// attachment": not a signature, and not an inline part referenced by
// Content-ID (an embedded image).
func (a Attachment) CountsAsAttachment() bool {
	return !a.Signature() && (!a.Inline || a.ContentID == "")
}

// Message is a fully parsed message. Text and HTML are UTF-8 with CRLF
// normalised to LF. Text holds only text/plain body parts (empty for
// HTML-only mail; see PlainText). Multiple body parts of one kind are joined
// with "\n".
type Message struct {
	Envelope
	Header      Header
	Text, HTML  string
	Attachments []Attachment
}

// PlainText returns Text, or the text extracted from HTML when Text is
// blank.
func (m *Message) PlainText() string {
	if strings.TrimSpace(m.Text) == "" && m.HTML != "" {
		return TextFromHTML(m.HTML)
	}
	return m.Text
}

// MaxSummaryText caps Summary.Text in bytes.
const MaxSummaryText = 256 << 10

// Summary is what the index stores and searches for one message.
type Summary struct {
	Envelope
	HasAttachment   bool     // some attachment CountsAsAttachment
	AttachmentNames []string // filenames that were not generated, in order
	Text            string   // plain body text, at most MaxSummaryText bytes
}

// Summary derives the index summary of m.
func (m *Message) Summary() *Summary {
	s := &Summary{Envelope: m.Envelope, Text: truncateUTF8(m.PlainText(), MaxSummaryText)}
	for _, a := range m.Attachments {
		if a.CountsAsAttachment() {
			s.HasAttachment = true
		}
		if !a.NameGenerated && a.Filename != "" {
			s.AttachmentNames = append(s.AttachmentNames, a.Filename)
		}
	}
	return s
}

// Summarize is Parse(raw, fallbackDate).Summary().
func Summarize(raw []byte, fallbackDate time.Time) *Summary {
	return Parse(raw, fallbackDate).Summary()
}

// truncateUTF8 cuts s to at most n bytes without splitting a rune.
func truncateUTF8(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// ErrNotFound is returned when the requested attachment does not exist.
var ErrNotFound = errors.New("mail: attachment not found")

// errPanic is returned if parsing panicked (should not happen).
var errPanic = errors.New("mail: internal error")

// Parse parses an RFC 5322 / MIME message tolerantly. It never returns nil
// and never panics: garbage headers, unknown charsets and broken multiparts
// degrade instead of failing. Date falls back to fallbackDate. Attachment
// bodies are not decoded (sizes are computed by counting).
func Parse(raw []byte, fallbackDate time.Time) (m *Message) {
	defer func() {
		if r := recover(); r != nil {
			m = &Message{Envelope: Envelope{Date: fallbackDate}}
		}
	}()
	return parse(raw, fallbackDate)
}

func parse(raw []byte, fallbackDate time.Time) *Message {
	h, body, dec := prepare(raw)
	m := &Message{Header: h.export(dec)}
	m.MessageID = firstMsgID(dec.decode(h.get("Message-ID")))
	m.InReplyTo = firstMsgID(dec.decode(h.get("In-Reply-To")))
	m.References = ParseMessageIDs(dec.decode(h.get("References")))
	m.Subject = dec.decode(h.get("Subject"))
	from := h.get("From")
	if from == nil {
		from = h.get("Sender")
	}
	if list := parseAddressList(dec.raw(from)); len(list) > 0 {
		m.From = list[0]
	}
	addrs := func(name string) []Address {
		var out []Address
		for _, v := range h.all(name) {
			out = append(out, parseAddressList(dec.raw(v))...)
		}
		return out
	}
	m.To, m.Cc, m.Bcc = addrs("To"), addrs("Cc"), addrs("Bcc")
	m.Date = ParseDate(dec.decode(h.get("Date")))
	if m.Date.IsZero() {
		m.Date = fallbackDate
	}

	c := &classifier{dec: dec}
	var texts, htmls []string
	w := &walker{dec: dec}
	w.visit = func(l *leaf) {
		if a, ok := c.classify(l); ok {
			m.Attachments = append(m.Attachments, a)
			return
		}
		data := decodeTransfer(l.body, l.cte)
		cs := l.params["charset"]
		if l.mediaType == "text/html" && !knownCharset(cs) {
			if sniffed := sniffHTMLCharset(data); sniffed != "" {
				cs = sniffed
			}
		}
		s := strings.ReplaceAll(DecodeCharset(data, cs), "\r\n", "\n")
		if strings.TrimSpace(s) == "" {
			return
		}
		if l.mediaType == "text/html" {
			htmls = append(htmls, s)
		} else {
			texts = append(texts, s)
		}
	}
	w.entity(h, body, "text/plain", 0)
	m.Text = strings.Join(texts, "\n")
	m.HTML = strings.Join(htmls, "\n")
	return m
}

// classifier assigns attachment indices in walk order.
type classifier struct {
	dec  headerDecoder
	next int
}

// rfc822SubjectScan bounds how much of a nameless message/rfc822 part is
// read to derive its filename from the inner Subject.
const rfc822SubjectScan = 8 << 10

// classify returns (attachment, true) for an attachment leaf, or false for a
// body (text/plain or text/html without a filename or attachment
// disposition).
func (c *classifier) classify(l *leaf) (Attachment, bool) {
	filename := l.dispParams["filename"]
	if filename == "" {
		filename = l.params["name"]
	}
	filename = sanitizeFilename(filename)
	isText := l.mediaType == "text/plain" || l.mediaType == "text/html"
	if isText && l.disp != "attachment" && filename == "" {
		return Attachment{}, false
	}
	cid := unescapeCID(c.dec.decode(l.h.get("Content-ID")))
	a := Attachment{
		Index:       c.next,
		ContentType: l.mediaType,
		ContentID:   cid,
		Size:        transferSize(l.body, l.cte),
		Inline:      l.disp == "inline" || (l.disp == "" && cid != ""),
	}
	c.next++
	if filename == "" {
		a.NameGenerated = true
		if l.mediaType == "message/rfc822" || l.mediaType == "message/global" {
			prefix := l.body
			if len(prefix) > rfc822SubjectScan {
				prefix = prefix[:rfc822SubjectScan]
			}
			ih, _ := splitEntity(decodeTransfer(prefix, l.cte))
			if s := sanitizeFilename(headerDecoder{}.decode(ih.get("Subject"))); s != "" {
				filename = truncateRunes(s, 100) + ".eml"
			}
		}
	}
	if filename == "" {
		filename = "attachment-" + strconv.Itoa(a.Index) + extByType[l.mediaType]
	}
	a.Filename = filename
	return a, true
}

func truncateRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}

type attachmentRef struct {
	att  Attachment
	leaf *leaf
}

// AttachmentData returns the attachment with the given index (as in
// Message.Attachments) and its decoded bytes, or ErrNotFound.
func AttachmentData(raw []byte, index int) (Attachment, []byte, error) {
	if index < 0 {
		return Attachment{}, nil, ErrNotFound
	}
	return safeAttachmentData(raw, func(r attachmentRef) bool { return r.att.Index == index })
}

// CIDData returns the first attachment whose Content-ID matches cid (with or
// without angle brackets, a "cid:" prefix or URL escaping; case-insensitive)
// and its decoded bytes, or ErrNotFound.
func CIDData(raw []byte, cid string) (Attachment, []byte, error) {
	want := unescapeCID(cid)
	if want == "" {
		return Attachment{}, nil, ErrNotFound
	}
	return safeAttachmentData(raw, func(r attachmentRef) bool { return cidMatches(r.att.ContentID, want) })
}

// FindCID returns the first of atts whose Content-ID matches cid, with the
// same rules as CIDData.
func FindCID(atts []Attachment, cid string) (Attachment, bool) {
	want := unescapeCID(cid)
	if want == "" {
		return Attachment{}, false
	}
	for _, a := range atts {
		if cidMatches(a.ContentID, want) {
			return a, true
		}
	}
	return Attachment{}, false
}

// AttachmentsData decodes, in one walk of the MIME tree, the attachments
// whose Index is in want, and returns their bytes by Index (missing ones are
// simply absent).
func AttachmentsData(raw []byte, want map[int]bool) (out map[int][]byte, err error) {
	defer func() {
		if r := recover(); r != nil {
			out, err = nil, errPanic
		}
	}()
	out = map[int][]byte{}
	if len(want) == 0 {
		return out, nil
	}
	h, body, dec := prepare(raw)
	c := &classifier{dec: dec}
	w := &walker{dec: dec}
	w.visit = func(l *leaf) {
		if len(out) == len(want) {
			return
		}
		if a, ok := c.classify(l); ok && want[a.Index] {
			out[a.Index] = decodeTransfer(l.body, l.cte)
		}
	}
	w.entity(h, body, "text/plain", 0)
	return out, nil
}

func safeAttachmentData(raw []byte, match func(attachmentRef) bool) (a Attachment, data []byte, err error) {
	defer func() {
		if r := recover(); r != nil {
			a, data, err = Attachment{}, nil, errPanic
		}
	}()
	return attachmentData(raw, match)
}

func attachmentData(raw []byte, match func(attachmentRef) bool) (Attachment, []byte, error) {
	h, body, dec := prepare(raw)
	c := &classifier{dec: dec}
	var found *attachmentRef
	w := &walker{dec: dec}
	w.visit = func(l *leaf) {
		if found != nil {
			return
		}
		if a, ok := c.classify(l); ok {
			if r := (attachmentRef{a, l}); match(r) {
				found = &r
			}
		}
	}
	w.entity(h, body, "text/plain", 0)
	if found == nil {
		return Attachment{}, nil, ErrNotFound
	}
	return found.att, decodeTransfer(found.leaf.body, found.leaf.cte), nil
}
