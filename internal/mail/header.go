package mail

import (
	"bytes"
	"encoding/base64"
	"regexp"
	"strings"
	"unicode/utf8"
)

// Field is one top-level header field. Name is as written; Value is
// unfolded, trimmed and converted to UTF-8 (raw 8-bit bytes via the
// message's declared charset or windows-1252) but RFC 2047 encoded words are
// NOT decoded, so callers that must split before decoding (X-Gmail-Labels)
// can. Use Decoded for display.
type Field struct{ Name, Value string }

// Decoded returns Value with RFC 2047 encoded words decoded.
func (f Field) Decoded() string { return strings.TrimSpace(decodeWords(f.Value)) }

// Header is the ordered list of top-level header fields of a message.
type Header []Field

// Get returns the Value of the first field named name (case-insensitive)
// whose value is not blank, or "".
func (h Header) Get(name string) string {
	for _, f := range h {
		if strings.EqualFold(f.Name, name) && f.Value != "" {
			return f.Value
		}
	}
	return ""
}

// Values returns the Values of every field named name, in order.
func (h Header) Values(name string) []string {
	var out []string
	for _, f := range h {
		if strings.EqualFold(f.Name, name) {
			out = append(out, f.Value)
		}
	}
	return out
}

// Decoded returns Get(name) with RFC 2047 encoded words decoded.
func (h Header) Decoded(name string) string { return Field{Value: h.Get(name)}.Decoded() }

// ParseHeader parses only the header block of raw (no MIME walk), for
// callers that need header metadata cheaply. It never fails: see Parse for
// the tolerance rules.
func ParseHeader(raw []byte) Header {
	h, _, dec := prepare(raw)
	return h.export(dec)
}

// DecodeHeader converts a raw header value to UTF-8 (invalid UTF-8 is read
// as windows-1252) and decodes RFC 2047 encoded words; the result is
// trimmed.
func DecodeHeader(s string) string { return headerDecoder{}.decode([]byte(s)) }

// maxFieldLen caps one unfolded header field; longer values are truncated.
const maxFieldLen = 1 << 20

// rawField is one raw header field: name as written, value unfolded but not
// decoded (may contain 8-bit bytes and RFC 2047 encoded words).
type rawField struct {
	name  string
	value []byte
}

type rawHeader []rawField

// get returns the raw value of the first field with a non-blank value named
// name (case-insensitive), or nil.
func (h rawHeader) get(name string) []byte {
	for _, f := range h {
		if strings.EqualFold(f.name, name) && len(bytes.TrimSpace(f.value)) > 0 {
			return f.value
		}
	}
	return nil
}

// all returns the raw values of every field named name.
func (h rawHeader) all(name string) [][]byte {
	var out [][]byte
	for _, f := range h {
		if strings.EqualFold(f.name, name) {
			out = append(out, f.value)
		}
	}
	return out
}

func (h rawHeader) export(dec headerDecoder) Header {
	if len(h) == 0 {
		return nil
	}
	out := make(Header, len(h))
	for i, f := range h {
		out[i] = Field{Name: f.name, Value: strings.TrimSpace(dec.raw(f.value))}
	}
	return out
}

// prepare splits the top-level entity and picks the header decoder. Every
// entry point must use it so decoded values (filenames, attachment
// indices) agree.
func prepare(raw []byte) (rawHeader, []byte, headerDecoder) {
	h, body := splitEntity(raw)
	var dec headerDecoder
	_, params := parseMediaType(fallbackDecode(h.get("Content-Type")))
	if cs := params["charset"]; knownCharset(cs) {
		dec.hint = cs
	}
	return h, body, dec
}

// splitEntity splits a message or MIME part into its header fields and body.
// It never fails: a first line that is not a header field means there are no
// headers, header lines that are neither fields nor continuations are
// skipped, and a missing blank line means the body is empty. A leading mbox
// "From " line is skipped. Field values are capped at maxFieldLen.
func splitEntity(raw []byte) (rawHeader, []byte) {
	var h rawHeader
	pos := 0
	first := true
	for pos < len(raw) {
		end := bytes.IndexByte(raw[pos:], '\n')
		var line []byte
		next := len(raw)
		if end >= 0 {
			line = raw[pos : pos+end]
			next = pos + end + 1
		} else {
			line = raw[pos:]
		}
		line = bytes.TrimSuffix(line, []byte("\r"))
		if len(line) == 0 {
			return h, raw[next:]
		}
		if first && bytes.HasPrefix(line, []byte("From ")) {
			// Stray mbox separator line.
			first = false
			pos = next
			continue
		}
		if line[0] == ' ' || line[0] == '\t' {
			content := bytes.TrimLeft(line, " \t")
			if len(content) == 0 && len(h) > 0 && !headerLineFollows(raw[next:]) {
				// Whitespace-only line not followed by more header: treat it
				// as the blank separator so the body is not swallowed.
				return h, raw[next:]
			}
			if len(h) > 0 {
				last := &h[len(h)-1]
				if len(content) > 0 && len(last.value)+1+len(content) <= maxFieldLen {
					last.value = append(last.value, ' ')
					last.value = append(last.value, content...)
				}
			} else if first {
				return nil, raw
			}
			pos = next
			first = false
			continue
		}
		name, value, ok := splitField(line)
		if !ok {
			if first {
				return nil, raw
			}
			pos = next
			continue
		}
		first = false
		value = bytes.TrimLeft(value, " \t")
		if len(value) > maxFieldLen {
			value = value[:maxFieldLen]
		}
		// Copy so appends for continuation lines never alias raw.
		h = append(h, rawField{name: name, value: append([]byte(nil), value...)})
		pos = next
	}
	return h, nil
}

// headerLineFollows reports whether rest starts with a header field or a
// continuation line with content.
func headerLineFollows(rest []byte) bool {
	line := rest
	if i := bytes.IndexByte(rest, '\n'); i >= 0 {
		line = rest[:i]
	}
	line = bytes.TrimSuffix(line, []byte("\r"))
	if len(line) == 0 {
		return false
	}
	if line[0] == ' ' || line[0] == '\t' {
		return len(bytes.TrimSpace(line)) > 0
	}
	_, _, ok := splitField(line)
	return ok
}

// splitField splits "Name: value". The name must be 1-76 printable ASCII
// characters without spaces or colons (whitespace before the colon is
// tolerated).
func splitField(line []byte) (string, []byte, bool) {
	i := bytes.IndexByte(line, ':')
	if i <= 0 {
		return "", nil, false
	}
	name := bytes.TrimRight(line[:i], " \t")
	if len(name) == 0 || len(name) > 76 {
		return "", nil, false
	}
	for _, c := range name {
		if c <= ' ' || c >= 0x7f {
			return "", nil, false
		}
	}
	return string(name), line[i+1:], true
}

// headerDecoder decodes raw header values to UTF-8. hint is a charset used
// for raw 8-bit bytes that are not valid UTF-8 (the message's declared body
// charset), or "".
type headerDecoder struct{ hint string }

// raw converts raw header bytes to UTF-8 without RFC 2047 decoding.
func (d headerDecoder) raw(b []byte) string {
	if d.hint != "" && !utf8.Valid(b) {
		return DecodeCharset(b, d.hint)
	}
	return fallbackDecode(b)
}

// decode converts raw header bytes to UTF-8 and decodes RFC 2047 words.
func (d headerDecoder) decode(b []byte) string {
	return strings.TrimSpace(decodeWords(d.raw(b)))
}

var encodedWordRe = regexp.MustCompile(`=\?([^?\s]+)\?([bBqQ])\?([^?\r\n]*)\?=`)

// decodeWords decodes RFC 2047 encoded words in s. Adjacent encoded words
// separated only by whitespace are joined; consecutive words in the same
// charset are concatenated as bytes before charset conversion, so multi-byte
// characters split across words survive. Words that cannot be decoded are
// left as is.
func decodeWords(s string) string {
	if !strings.Contains(s, "=?") {
		return s
	}
	matches := encodedWordRe.FindAllStringSubmatchIndex(s, -1)
	if matches == nil {
		return s
	}
	var out strings.Builder
	var pend []byte
	pendCS := ""
	havePend := false
	flush := func() {
		if havePend {
			out.WriteString(DecodeCharset(pend, pendCS))
			pend, havePend = nil, false
		}
	}
	last := 0
	prevWord := false
	for _, m := range matches {
		between := s[last:m[0]]
		if !(prevWord && strings.TrimSpace(between) == "") {
			flush()
			out.WriteString(between)
		}
		cs := normCharset(s[m[2]:m[3]])
		data, ok := decodeWordText(s[m[4]:m[5]], s[m[6]:m[7]])
		if !ok {
			flush()
			out.WriteString(s[m[0]:m[1]])
			prevWord = false
			last = m[1]
			continue
		}
		if havePend && cs != pendCS {
			flush()
		}
		pend = append(pend, data...)
		pendCS, havePend = cs, true
		prevWord = true
		last = m[1]
	}
	flush()
	out.WriteString(s[last:])
	return out.String()
}

func decodeWordText(enc, text string) ([]byte, bool) {
	switch enc {
	case "b", "B":
		clean := strings.Map(func(r rune) rune {
			if r == ' ' || r == '\t' {
				return -1
			}
			return r
		}, text)
		clean = strings.TrimRight(clean, "=")
		if b, err := base64.RawStdEncoding.DecodeString(clean); err == nil {
			return b, true
		}
		return nil, false
	default:
		var out []byte
		for i := 0; i < len(text); i++ {
			c := text[i]
			switch {
			case c == '_':
				out = append(out, ' ')
			case c == '=' && i+2 < len(text) && isHex(text[i+1]) && isHex(text[i+2]):
				out = append(out, unhex(text[i+1])<<4|unhex(text[i+2]))
				i += 2
			default:
				out = append(out, c)
			}
		}
		return out, true
	}
}

func isHex(c byte) bool {
	return '0' <= c && c <= '9' || 'a' <= c && c <= 'f' || 'A' <= c && c <= 'F'
}

func unhex(c byte) byte {
	switch {
	case '0' <= c && c <= '9':
		return c - '0'
	case 'a' <= c && c <= 'f':
		return c - 'a' + 10
	default:
		return c - 'A' + 10
	}
}
