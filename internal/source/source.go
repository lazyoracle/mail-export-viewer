package source

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/lazyoracle/mail-export-viewer/internal/mail"
)

// Source is one opened input (an mbox file, a PST, an OLM, a directory of
// .eml files). See the package documentation for the full contract.
type Source interface {
	// Kind is the registered format name ("mbox", "pst", ...).
	Kind() string
	// Path is the absolute path of the input (Candidate.Path); it identifies
	// the source in the index.
	Path() string
	// Inputs lists every file the source reads (absolute paths), e.g. an
	// mbox and its .msf sidecar or every .eml of a directory. The index
	// fingerprints them (size+mtime) to decide whether to re-scan.
	Inputs() []string
	// Scan streams one Entry per message, in a stable order. It returns a
	// non-nil error only when the whole source is unreadable, ctx is done,
	// or emit returned an error (which Scan returns as is). Problems with
	// single items or folders go to warn (never nil) and scanning goes on.
	Scan(ctx context.Context, emit func(Entry) error, warn func(error)) error
	// Load fully parses the message at loc (bodies, attachments).
	Load(loc Locator) (*mail.Message, error)
	// Raw returns the message at loc as RFC 5322 bytes (.eml export);
	// synthesised with mail.WriteEML by formats that do not store it.
	Raw(loc Locator) ([]byte, error)
	// Attachment returns attachment index (as in Load's
	// Message.Attachments) of the message at loc and its decoded bytes; an
	// unknown index gives an error wrapping mail.ErrNotFound.
	Attachment(loc Locator, index int) (mail.Attachment, []byte, error)
	// Close releases open files. No other method may be called afterwards.
	Close() error
}

// ErrStale is wrapped by Load, Raw and Attachment when loc no longer points
// at the message it was scanned from (the file changed since indexing).
var ErrStale = errors.New("source: stale locator")

// MaxMessageBytes is the largest message (raw or synthesised size) Load, Raw
// and Attachment handle; above it they return an error wrapping
// ErrTooLarge without reading the message into memory. Scan still lists
// such messages (from a bounded prefix).
const MaxMessageBytes = 256 << 20

// ErrTooLarge is wrapped when a message exceeds MaxMessageBytes.
var ErrTooLarge = errors.New("source: message too large")

// FolderLister is optionally implemented by sources with a folder tree of
// their own (PST, OLM) so that folders without messages are shown too.
// Folders returns every folder path, in the same form as Entry.Folder; the
// index adds them to the folders it learned from Entries. It is called
// after Scan.
type FolderLister interface {
	Folders() []string
}

// TagStyler is optionally implemented by sources that know how their tags
// are shown (Thunderbird: prefs.js names and colours, via the sidecar).
// TagStyles returns one style per tag display name (as in Entry.Tags),
// sorted by name; the index calls it after Scan and shows the colours.
type TagStyler interface {
	TagStyles() []TagStyle
}

// Entry is what Scan yields per message.
type Entry struct {
	// Folder is the folder path, '/'-separated, valid UTF-8, never empty
	// ("Inbox", "Archive/2019"). It is the full display path of the folder
	// (formats derive it from Candidate.Rel and their own folder tree).
	Folder string
	// Labels are Gmail labels or Outlook categories; Tags are Thunderbird
	// tags (display names). In order, without duplicates.
	Labels, Tags []string
	Flags        Flags
	// Props are the remaining format-specific key/values (e.g. X- headers,
	// unmapped OLM/PST properties), in order, shown in a Properties panel.
	Props []Prop
	// Loc locates the message for Load, Raw and Attachment.
	Loc Locator
	// Msg is the indexed summary. Formats that parse while scanning (PST,
	// OLM) set it. Formats holding raw RFC 5322 bytes (mbox, eml) may leave
	// it nil and set Raw and Date instead, so that consumers summarise in
	// parallel workers with Resolve.
	Msg *mail.Summary
	// Raw is the message (or a bounded prefix of it: headers and the start
	// of the body suffice) when Msg is nil.
	Raw []byte
	// Date is the fallback date (e.g. the mbox From_ line date) used when
	// Msg is computed from Raw.
	Date time.Time
}

// Resolve makes e.Msg non-nil: when nil it is computed from e.Raw with
// mail.Summarize (e.Date as fallback date) and e.Raw is released. It then
// sets the HasAttachment flag if e.Msg.HasAttachment. It is idempotent and
// never panics.
func (e *Entry) Resolve() {
	if e.Msg == nil {
		e.Msg = mail.Summarize(e.Raw, e.Date)
		e.Raw = nil
	}
	if e.Msg.HasAttachment {
		e.Flags |= HasAttachment
	}
}

// Prop is one format-specific property.
type Prop struct{ Key, Value string }

// Flags is a set of message states. The zero value means read, unflagged.
type Flags uint32

// The flags, in display order.
const (
	Unread Flags = 1 << iota
	Starred
	Answered
	Forwarded
	Draft
	Deleted
	Junk
	HasAttachment
	Important // Gmail Important or Outlook high importance
)

// flagNames are the canonical names (used by is: queries and the API), in
// bit order.
var flagNames = []string{"unread", "starred", "answered", "forwarded", "draft", "deleted", "junk", "attachment", "important"}

var flagAliases = map[string]Flags{
	"flagged": Starred, "replied": Answered, "spam": Junk, "trash": Deleted,
	"has-attachment": HasAttachment, "unseen": Unread,
}

// Has reports whether all of g are set in f.
func (f Flags) Has(g Flags) bool { return f&g == g }

// Names returns the canonical names of the set flags in bit order, or nil.
func (f Flags) Names() []string {
	var out []string
	for i, n := range flagNames {
		if f&(1<<i) != 0 {
			out = append(out, n)
		}
	}
	return out
}

// String joins Names with commas.
func (f Flags) String() string { return strings.Join(f.Names(), ",") }

// ParseFlag returns the flag named name (canonical name or alias,
// case-insensitive).
func ParseFlag(name string) (Flags, bool) {
	name = strings.ToLower(strings.TrimSpace(name))
	for i, n := range flagNames {
		if n == name {
			return 1 << i, true
		}
	}
	f, ok := flagAliases[name]
	return f, ok
}

// Locator says where a message is inside its Source. Each format uses the
// fields it needs and leaves the rest zero:
//
//	mbox: Offset, Length  (byte range of the message incl. its From_ line)
//	eml:  Name            (file name within the directory)
//	olm:  Name            (zip entry name)
//	pst:  ID              (node id of the message)
//
// It is stored by the index in its text form (MarshalText), which is
// printable ASCII.
type Locator struct {
	Name           string
	Offset, Length int64
	ID             uint64
}

// MarshalText encodes l as "offset:length:id:escaped-name".
func (l Locator) MarshalText() ([]byte, error) {
	return []byte(l.String()), nil
}

// String returns the text form of l.
func (l Locator) String() string {
	return strconv.FormatInt(l.Offset, 10) + ":" + strconv.FormatInt(l.Length, 10) + ":" +
		strconv.FormatUint(l.ID, 10) + ":" + escapeName(l.Name)
}

// UnmarshalText parses the text form produced by MarshalText.
func (l *Locator) UnmarshalText(b []byte) error {
	parts := strings.SplitN(string(b), ":", 4)
	if len(parts) != 4 {
		return fmt.Errorf("source: bad locator %q", b)
	}
	off, err1 := strconv.ParseInt(parts[0], 10, 64)
	n, err2 := strconv.ParseInt(parts[1], 10, 64)
	id, err3 := strconv.ParseUint(parts[2], 10, 64)
	name, err4 := url.PathUnescape(parts[3])
	if err := errors.Join(err1, err2, err3, err4); err != nil || off < 0 || n < 0 {
		return fmt.Errorf("source: bad locator %q", b)
	}
	*l = Locator{Name: name, Offset: off, Length: n, ID: id}
	return nil
}

// escapeName percent-escapes everything but unreserved ASCII and '/'.
func escapeName(s string) string {
	const hex = "0123456789ABCDEF"
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || '0' <= c && c <= '9' || strings.IndexByte("-._~/", c) >= 0 {
			b.WriteByte(c)
			continue
		}
		b.WriteByte('%')
		b.WriteByte(hex[c>>4])
		b.WriteByte(hex[c&15])
	}
	return b.String()
}
