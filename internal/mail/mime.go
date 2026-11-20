package mail

import (
	"bytes"
	"net/url"
	"path"
	"strings"
)

// Limits that keep hostile or broken input bounded.
const (
	maxDepth = 20    // multipart nesting followed
	maxParts = 10000 // total MIME entities visited per message
)

// leaf is a non-multipart MIME entity reached by walk.
type leaf struct {
	h          rawHeader
	mediaType  string
	params     map[string]string
	disp       string
	dispParams map[string]string
	cte        string
	body       []byte // transfer-encoded
}

// walker visits leaves in document order. Every entry point shares it so
// attachment indices agree.
type walker struct {
	dec   headerDecoder
	parts int
	visit func(*leaf)
}

func (w *walker) entity(h rawHeader, body []byte, defaultType string, depth int) {
	w.parts++
	if w.parts > maxParts {
		return
	}
	mt, params := parseMediaType(w.dec.raw(h.get("Content-Type")))
	if mt == "" || !strings.Contains(mt, "/") {
		if mt == "text" {
			mt = "text/plain"
		} else {
			mt = defaultType
		}
	}
	if strings.HasPrefix(mt, "multipart/") {
		boundary := params["boundary"]
		// A boundary holding CR or LF (possible via RFC 2231 %0A) can never
		// match a delimiter line: treat the body as broken.
		if boundary != "" && !strings.ContainsAny(boundary, "\r\n") && depth < maxDepth {
			parts, ok := splitMultipart(body, boundary)
			if ok {
				childDefault := "text/plain"
				if mt == "multipart/digest" {
					childDefault = "message/rfc822"
				}
				for _, p := range parts {
					ch, cb := splitEntity(p)
					w.entity(ch, cb, childDefault, depth+1)
				}
				return
			}
		}
		if depth >= maxDepth {
			return
		}
		// Broken multipart: show the body as text.
		mt, params = "text/plain", map[string]string{"charset": params["charset"]}
	}
	disp, dparams := parseMediaType(w.dec.raw(h.get("Content-Disposition")))
	cte := strings.ToLower(strings.TrimSpace(string(h.get("Content-Transfer-Encoding"))))
	if f := strings.Fields(cte); len(f) > 0 {
		cte = strings.Trim(f[0], `"';`)
	}
	w.visit(&leaf{h: h, mediaType: mt, params: params, disp: disp, dispParams: dparams, cte: cte, body: body})
}

// splitMultipart splits body on boundary lines. ok is false if no boundary
// line was found. A missing close delimiter means the last part runs to the
// end of the body.
func splitMultipart(body []byte, boundary string) (parts [][]byte, ok bool) {
	delim := []byte("--" + boundary)
	start := -1
	pos := 0
	for pos < len(body) {
		i := bytes.Index(body[pos:], delim)
		if i < 0 {
			break
		}
		i += pos
		if i > 0 && body[i-1] != '\n' {
			pos = i + 1
			continue
		}
		j := i + len(delim)
		lineEnd := len(body)
		if k := bytes.IndexByte(body[j:], '\n'); k >= 0 {
			lineEnd = j + k
		}
		rest := bytes.TrimRight(body[j:lineEnd], " \t\r")
		closing := bytes.Equal(rest, []byte("--"))
		if len(rest) > 0 && !closing {
			pos = j
			continue
		}
		if start >= 0 {
			end := i
			if end > start && body[end-1] == '\n' {
				end--
				if end > start && body[end-1] == '\r' {
					end--
				}
			}
			parts = append(parts, body[start:end])
			if len(parts) >= maxParts {
				return parts, true
			}
		}
		ok = true
		if closing {
			return parts, true
		}
		start = lineEnd + 1
		if start > len(body) {
			start = len(body)
		}
		pos = start
	}
	if start >= 0 {
		parts = append(parts, body[start:])
	}
	return parts, ok
}

// --- transfer encodings ---

func decodeTransfer(b []byte, cte string) []byte {
	switch cte {
	case "base64":
		return decodeBase64(b)
	case "quoted-printable":
		return decodeQP(b)
	default:
		return b
	}
}

// transferSize returns len(decodeTransfer(b, cte)) without decoding.
func transferSize(b []byte, cte string) int64 {
	switch cte {
	case "base64":
		n := int64(0)
		for _, c := range b {
			if b64val[c] != 0xff {
				n++
			}
		}
		return n/4*3 + [4]int64{0, 0, 1, 2}[n%4]
	case "quoted-printable":
		_, n := qp(b, false)
		return n
	default:
		return int64(len(b))
	}
}

var b64val = func() (t [256]byte) {
	for i := range t {
		t[i] = 0xff
	}
	const alpha = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"
	for i := 0; i < len(alpha); i++ {
		t[alpha[i]] = byte(i)
	}
	return t
}()

// decodeBase64 decodes tolerantly: every non-alphabet byte (whitespace,
// padding, garbage) is skipped, and a dangling final sextet is dropped.
func decodeBase64(b []byte) []byte {
	out := make([]byte, 0, transferSize(b, "base64"))
	var acc uint32
	n := 0
	for _, c := range b {
		v := b64val[c]
		if v == 0xff {
			continue
		}
		acc = acc<<6 | uint32(v)
		n++
		if n == 4 {
			out = append(out, byte(acc>>16), byte(acc>>8), byte(acc))
			acc, n = 0, 0
		}
	}
	switch n {
	case 2:
		out = append(out, byte(acc>>4))
	case 3:
		out = append(out, byte(acc>>10), byte(acc>>2))
	}
	return out
}

// decodeQP decodes quoted-printable tolerantly: soft line breaks (with
// optional trailing whitespace) are removed, =XX (either case) is decoded,
// and any other '=' is kept literally.
func decodeQP(b []byte) []byte {
	out, _ := qp(b, true)
	return out
}

// qp implements decodeQP. With emit false it only counts output bytes and
// does not allocate, so sizes and decoded data always agree.
func qp(b []byte, emit bool) ([]byte, int64) {
	var out []byte
	if emit {
		out = make([]byte, 0, len(b))
	}
	var n int64
	put := func(c byte) {
		n++
		if emit {
			out = append(out, c)
		}
	}
	for i := 0; i < len(b); i++ {
		c := b[i]
		if c != '=' {
			put(c)
			continue
		}
		if i+2 < len(b) && isHex(b[i+1]) && isHex(b[i+2]) {
			put(unhex(b[i+1])<<4 | unhex(b[i+2]))
			i += 2
			continue
		}
		// Soft line break: '=' [WSP...] [CR] LF, or '=' at end of input.
		j := i + 1
		for j < len(b) && (b[j] == ' ' || b[j] == '\t') {
			j++
		}
		if j < len(b) && b[j] == '\r' {
			j++
		}
		if j < len(b) && b[j] == '\n' || j >= len(b) {
			i = j
			continue
		}
		put(c)
	}
	return out, n
}

// --- filenames and content ids ---

var extByType = map[string]string{
	"message/rfc822":  ".eml",
	"message/global":  ".eml",
	"image/png":       ".png",
	"image/jpeg":      ".jpg",
	"image/jpg":       ".jpg",
	"image/gif":       ".gif",
	"image/webp":      ".webp",
	"image/bmp":       ".bmp",
	"image/svg+xml":   ".svg",
	"image/tiff":      ".tif",
	"application/pdf": ".pdf",
	"application/zip": ".zip",
	"text/plain":      ".txt",
	"text/html":       ".html",
	"text/calendar":   ".ics",
	"text/csv":        ".csv",
	"audio/mpeg":      ".mp3",
	"video/mp4":       ".mp4",
}

// sanitizeFilename keeps only the last path element (either slash) and
// removes control characters.
func sanitizeFilename(name string) string {
	name = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, name)
	name = strings.ReplaceAll(name, `\`, "/")
	name = path.Base(strings.TrimRight(name, "/"))
	name = strings.TrimSpace(name)
	if name == "." || name == ".." || name == "/" {
		return ""
	}
	return name
}

// unescapeCID normalises a Content-ID or cid: reference.
func unescapeCID(s string) string {
	s = strings.TrimSpace(s)
	if len(s) >= 4 && strings.EqualFold(s[:4], "cid:") {
		s = s[4:]
	}
	return strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(strings.TrimSpace(s), "<"), ">"))
}

func cidMatches(have, want string) bool {
	if have == "" {
		return false
	}
	if strings.EqualFold(have, want) {
		return true
	}
	if u, err := url.PathUnescape(want); err == nil && strings.EqualFold(have, unescapeCID(u)) {
		return true
	}
	return false
}
