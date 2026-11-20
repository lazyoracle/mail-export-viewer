package mail

import (
	"regexp"
	"strings"
	"unicode/utf8"

	"golang.org/x/text/encoding"
	"golang.org/x/text/encoding/charmap"
	"golang.org/x/text/encoding/htmlindex"
	"golang.org/x/text/encoding/ianaindex"
)

// charsetAliases maps labels seen in real mail that neither the WHATWG nor
// the IANA index know to a label they do.
var charsetAliases = map[string]string{
	"utf8":        "utf-8",
	"x-sjis":      "shift_jis",
	"sjis":        "shift_jis",
	"cp932":       "windows-31j",
	"x-gbk":       "gbk",
	"cp936":       "gbk",
	"ks_c_5601":   "euc-kr",
	"cp949":       "euc-kr",
	"x-euc-jp":    "euc-jp",
	"cp-1252":     "windows-1252",
	"win-1252":    "windows-1252",
	"cp850":       "ibm850",
	"x-mac-roman": "macintosh",
}

// fallbackLabels are labels that carry no usable charset information.
var fallbackLabels = map[string]bool{
	"": true, "us-ascii": true, "ascii": true, "unknown-8bit": true,
	"x-unknown": true, "unknown": true, "default": true, "8bit": true,
	"7bit": true, "binary": true, "none": true, "charset": true,
}

// normCharset lowercases a charset label and strips quotes and an RFC 2231
// language suffix ("utf-8*en").
func normCharset(cs string) string {
	cs = strings.ToLower(strings.Trim(strings.TrimSpace(cs), `"' `))
	if i := strings.IndexByte(cs, '*'); i >= 0 {
		cs = cs[:i]
	}
	return cs
}

// lookupEncoding returns a decoder for a normalised label, or nil if the
// label is unknown or unsupported.
func lookupEncoding(cs string) encoding.Encoding {
	if a, ok := charsetAliases[cs]; ok {
		cs = a
	}
	enc, err := htmlindex.Get(cs)
	if err != nil || enc == nil {
		enc, err = ianaindex.IANA.Encoding(cs)
		if err != nil || enc == nil {
			return nil
		}
	}
	// The WHATWG "replacement" encoding decodes everything to one U+FFFD,
	// which is worse than the fallback.
	if enc == encoding.Replacement {
		return nil
	}
	return enc
}

// knownCharset reports whether cs names a charset that DecodeCharset would
// actually use (as opposed to falling back).
func knownCharset(cs string) bool {
	cs = normCharset(cs)
	if fallbackLabels[cs] {
		return false
	}
	return cs == "utf-8" || lookupEncoding(cs) != nil
}

// DecodeCharset converts b from the charset labelled cs (any MIME, IANA or
// WHATWG label, case-insensitive, quotes tolerated) to UTF-8. Unknown
// charsets, charsets that fail to decode, and labels that carry no
// information ("", "us-ascii", "unknown-8bit", ...) fall back to: b as is if
// it is valid UTF-8, else windows-1252. UTF-8 declared as Latin-1 and
// Latin-1 declared as UTF-8 (both common) are detected. The result is always
// valid UTF-8.
func DecodeCharset(b []byte, cs string) string {
	cs = normCharset(cs)
	if a, ok := charsetAliases[cs]; ok {
		cs = a
	}
	if fallbackLabels[cs] {
		return fallbackDecode(b)
	}
	if cs == "utf-8" {
		if utf8.Valid(b) {
			return string(b)
		}
		// Mislabelled single-byte text has no valid multi-byte sequences;
		// otherwise it is UTF-8 with a few bad bytes.
		if !hasMultibyteUTF8(b) {
			return fallbackDecode(b)
		}
		return strings.ToValidUTF8(string(b), "�")
	}
	if isASCII(b) && asciiTransparent(cs) {
		// Skip the decoder for the common pure-ASCII case.
		return string(b)
	}
	enc := lookupEncoding(cs)
	if enc == nil {
		return fallbackDecode(b)
	}
	if (enc == charmap.Windows1252 || enc == charmap.ISO8859_1) && hasMultibyteUTF8(b) && utf8.Valid(b) {
		// Common mislabel: UTF-8 declared as Latin-1/windows-1252.
		return string(b)
	}
	out, err := enc.NewDecoder().Bytes(b)
	if err != nil {
		return fallbackDecode(b)
	}
	return strings.ToValidUTF8(string(out), "�")
}

// asciiTransparent reports whether pure 7-bit input decodes to itself in
// charset cs. UTF-16/32 and the 7-bit stateful encodings (ISO-2022-*, HZ,
// UTF-7) use escape sequences or code units made of ASCII bytes.
func asciiTransparent(cs string) bool {
	for _, p := range []string{"utf-16", "utf-32", "iso-2022", "csiso2022", "hz-", "utf-7", "unicode-1-1-utf-7"} {
		if strings.HasPrefix(cs, p) {
			return false
		}
	}
	return true
}

// fallbackDecode returns b as is if it is valid UTF-8, otherwise decodes it
// as windows-1252 (a superset of ISO-8859-1 that maps every byte).
func fallbackDecode(b []byte) string {
	if utf8.Valid(b) {
		return string(b)
	}
	out, err := charmap.Windows1252.NewDecoder().Bytes(b)
	if err != nil {
		return strings.ToValidUTF8(string(b), "�")
	}
	return strings.ToValidUTF8(string(out), "�")
}

func isASCII(b []byte) bool {
	for _, c := range b {
		if c >= 0x80 {
			return false
		}
	}
	return true
}

func hasMultibyteUTF8(b []byte) bool {
	for i := 0; i < len(b); {
		if b[i] < 0x80 {
			i++
			continue
		}
		r, n := utf8.DecodeRune(b[i:])
		if r != utf8.RuneError || n > 1 {
			return true
		}
		i++
	}
	return false
}

var metaCharsetRe = regexp.MustCompile(`(?i)<meta[^>]+charset\s*=\s*["']?\s*([A-Za-z0-9_.:\-]+)`)

// sniffHTMLCharset looks for a <meta charset> declaration near the start of
// an HTML body.
func sniffHTMLCharset(b []byte) string {
	if len(b) > 4096 {
		b = b[:4096]
	}
	if m := metaCharsetRe.FindSubmatch(b); m != nil {
		return string(m[1])
	}
	return ""
}
