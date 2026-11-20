package pst

import (
	"errors"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/lazyoracle/mail-export-viewer/internal/mail"
)

// Compressed RTF ([MS-OXRTFCP]) and the RTF-to-text/HTML fallback used when
// a message has PR_RTF_COMPRESSED but no plain or HTML body.

const lzfuPrebuf = "{\\rtf1\\ansi\\mac\\deff0\\deftab720{\\fonttbl;}{\\f0\\fnil \\froman \\fswiss \\fmodern \\fscript \\fdecor MS Sans SerifSymbolArialTimes New RomanCourier{\\colortbl\\red0\\green0\\blue0\r\n\\par \\pard\\plain\\f0\\fs20\\b\\i\\u\\tab\\tx"

var errRTF = errors.New("pst: bad compressed RTF")

// lzfu decompresses PR_RTF_COMPRESSED data; max bounds the output size.
func lzfu(b []byte, max int) ([]byte, error) {
	if len(b) < 16 {
		return nil, errRTF
	}
	comp, raw := int(le32(b, 0)), int(le32(b, 4))
	if raw > max {
		return nil, errTooBig
	}
	data := b[16:]
	if comp >= 12 && comp-12 < len(data) {
		data = data[:comp-12]
	}
	switch string(b[8:12]) {
	case "MELA":
		return data[:min(raw, len(data))], nil
	case "LZFu":
	default:
		return nil, errRTF
	}
	var dict [4096]byte
	copy(dict[:], lzfuPrebuf)
	wpos := len(lzfuPrebuf)
	out := make([]byte, 0, min(raw, 1<<20)) // raw is only a claim
	for i := 0; i < len(data); {
		ctrl := data[i]
		i++
		for bit := 0; bit < 8 && i < len(data); bit++ {
			if ctrl&(1<<bit) == 0 {
				c := data[i]
				i++
				dict[wpos] = c
				wpos = (wpos + 1) % 4096
				out = append(out, c)
			} else {
				if i+2 > len(data) {
					return out, nil
				}
				ref := int(data[i])<<8 | int(data[i+1])
				i += 2
				off, n := ref>>4, ref&15+2
				if off == wpos {
					return out, nil
				}
				for k := 0; k < n; k++ {
					c := dict[(off+k)%4096]
					dict[wpos] = c
					wpos = (wpos + 1) % 4096
					out = append(out, c)
				}
			}
			if len(out) > max {
				return nil, errTooBig
			}
		}
	}
	return out, nil
}

// rtfConvert extracts the encapsulated HTML of an RTF body produced from
// HTML ([MS-OXRTFEX], \fromhtml1), or else its plain text. Exactly one of
// html and text is set (both empty for empty input).
func rtfConvert(b []byte) (html, text string) {
	c := rtfConverter{in: b, cp: 1252}
	c.run()
	out := strings.ReplaceAll(c.out.String(), "\r\n", "\n")
	if c.fromHTML {
		return out, ""
	}
	return "", out
}

type rtfGroup struct {
	skip, htmlrtf, htmltag bool
	uc                     int
}

type rtfConverter struct {
	in       []byte
	pos      int
	cp       int
	fromHTML bool
	out      strings.Builder
	pend     []byte // \'hh bytes awaiting code-page decoding
	skipN    int    // characters to skip after \uN
}

const (
	maxRTFDepth = 1000
	maxRTFOut   = 64 << 20
)

// rtfSkipDest are destinations whose content is not body text.
var rtfSkipDest = map[string]bool{
	"fonttbl": true, "colortbl": true, "stylesheet": true, "info": true, "pict": true, "object": true,
	"header": true, "headerl": true, "headerr": true, "headerf": true, "footer": true, "footerl": true,
	"footerr": true, "footerf": true, "footnote": true, "fldinst": true, "listtable": true,
	"listoverridetable": true, "rsidtbl": true, "generator": true, "themedata": true, "xmlnstbl": true,
	"colorschememapping": true, "datastore": true, "latentstyles": true, "pgdsctbl": true,
	"filetbl": true, "revtbl": true, "mhtmltag": true, "bkmkstart": true, "bkmkend": true,
}

var rtfSymbols = map[string]string{
	"par": "\n", "line": "\n", "sect": "\n", "page": "\n", "row": "\n", "cell": "\t", "tab": "\t",
	"lquote": "‘", "rquote": "’", "ldblquote": "“", "rdblquote": "”",
	"bullet": "•", "endash": "–", "emdash": "—", "emspace": " ", "enspace": " ",
}

func (c *rtfConverter) emitting(g rtfGroup) bool {
	return !g.skip && (g.htmltag || !g.htmlrtf) && c.out.Len() < maxRTFOut
}

func (c *rtfConverter) flush() {
	if len(c.pend) > 0 {
		c.out.WriteString(mail.DecodeCharset(c.pend, cpCharset(c.cp)))
		c.pend = c.pend[:0]
	}
}

// char handles one body character (a literal byte or a \'hh byte).
func (c *rtfConverter) char(g rtfGroup, b byte, hex bool) {
	if c.skipN > 0 {
		c.skipN--
		return
	}
	if !c.emitting(g) {
		return
	}
	if hex || b >= 0x80 {
		c.pend = append(c.pend, b)
		return
	}
	c.flush()
	c.out.WriteByte(b)
}

func (c *rtfConverter) str(g rtfGroup, s string) {
	if c.emitting(g) {
		c.flush()
		c.out.WriteString(s)
	}
}

func (c *rtfConverter) run() {
	stack := []rtfGroup{{uc: 1}}
	star := false // the last token was \*
	for c.pos < len(c.in) {
		g := &stack[len(stack)-1]
		ch := c.in[c.pos]
		c.pos++
		switch ch {
		case '{':
			if len(stack) < maxRTFDepth {
				stack = append(stack, *g)
			}
			star = false
			c.skipN = 0
		case '}':
			if len(stack) > 1 {
				stack = stack[:len(stack)-1]
			}
			star = false
			c.skipN = 0
		case '\r', '\n':
		case '\\':
			c.control(&stack, &star)
		default:
			c.char(*g, ch, false)
			star = false
		}
	}
	c.flush()
}

func isLetter(b byte) bool { return 'a' <= b && b <= 'z' || 'A' <= b && b <= 'Z' }

func (c *rtfConverter) control(stack *[]rtfGroup, star *bool) {
	g := &(*stack)[len(*stack)-1]
	if c.pos >= len(c.in) {
		return
	}
	ch := c.in[c.pos]
	if !isLetter(ch) {
		c.pos++
		switch ch {
		case '\'':
			if c.pos+2 <= len(c.in) {
				if v, err := strconv.ParseUint(string(c.in[c.pos:c.pos+2]), 16, 8); err == nil {
					c.char(*g, byte(v), true)
				}
				c.pos += 2
			}
		case '*':
			*star = true
			return
		case '~':
			c.str(*g, " ")
		case '_':
			c.str(*g, "-")
		case '\r', '\n':
			c.str(*g, "\n")
		case '-':
		default: // \\ \{ \} and other symbols stand for themselves
			c.char(*g, ch, false)
		}
		*star = false
		return
	}
	start := c.pos
	for c.pos < len(c.in) && isLetter(c.in[c.pos]) && c.pos-start < 32 {
		c.pos++
	}
	word := string(c.in[start:c.pos])
	for c.pos < len(c.in) && isLetter(c.in[c.pos]) {
		c.pos++ // overlong word: ignore the rest
	}
	numStart := c.pos
	if c.pos < len(c.in) && c.in[c.pos] == '-' {
		c.pos++
	}
	for c.pos < len(c.in) && '0' <= c.in[c.pos] && c.in[c.pos] <= '9' && c.pos-numStart < 11 {
		c.pos++
	}
	num, err := strconv.Atoi(string(c.in[numStart:c.pos]))
	hasNum := err == nil
	for c.pos < len(c.in) && '0' <= c.in[c.pos] && c.in[c.pos] <= '9' {
		c.pos++
	}
	if c.pos < len(c.in) && c.in[c.pos] == ' ' {
		c.pos++
	}
	wasStar := *star
	*star = false
	switch {
	case word == "htmltag": // \*\htmltagN or \htmltagN
		g.htmltag = true
		return
	case rtfSkipDest[word] || wasStar:
		g.skip = true
		return
	}
	switch word {
	case "fromhtml":
		c.fromHTML = true
	case "htmlrtf":
		g.htmlrtf = !hasNum || num != 0
	case "ansicpg":
		if hasNum {
			c.flush()
			c.cp = num
		}
	case "uc":
		if hasNum && num >= 0 && num < 10 {
			g.uc = num
		}
	case "u":
		if hasNum {
			if num < 0 {
				num += 65536
			}
			r := rune(num)
			if !utf8.ValidRune(r) || num < 0 {
				r = utf8.RuneError
			}
			c.str(*g, string(r))
			c.skipN = g.uc
		}
	default:
		if s, ok := rtfSymbols[word]; ok {
			c.str(*g, s)
		}
	}
}

// cpCharset maps a Windows code page to a charset label for
// mail.DecodeCharset ("" when unknown: UTF-8 if valid, else windows-1252).
func cpCharset(cp int) string {
	switch {
	case cp >= 1250 && cp <= 1258:
		return "windows-" + strconv.Itoa(cp)
	case cp >= 28591 && cp <= 28606:
		return "iso-8859-" + strconv.Itoa(cp-28590)
	}
	switch cp {
	case 65001:
		return "utf-8"
	case 20127:
		return "us-ascii"
	case 874:
		return "windows-874"
	case 932:
		return "shift_jis"
	case 936:
		return "gbk"
	case 949:
		return "euc-kr"
	case 950:
		return "big5"
	case 1200:
		return "utf-16le"
	case 1201:
		return "utf-16be"
	case 20866:
		return "koi8-r"
	case 21866:
		return "koi8-u"
	case 50220, 50221, 50222:
		return "iso-2022-jp"
	case 51932:
		return "euc-jp"
	case 51949:
		return "euc-kr"
	case 54936:
		return "gb18030"
	case 866:
		return "ibm866"
	case 10000:
		return "macintosh"
	}
	return ""
}
