package server

import (
	"net/url"
	"strconv"
	"strings"
	"unicode/utf8"

	"golang.org/x/net/html"
)

// HTML e-mail is hostile input. sanitizeHTML re-serialises it from tokens
// through an allow-list: only known-harmless elements and attributes are
// emitted, text and attribute values are re-escaped, and every URL is
// classified. The result is meant to be shown in a sandboxed iframe (no
// scripts) under a restrictive CSP; the sanitiser is the first line of
// defence, not the only one.

// allowedTags may be emitted (without disallowed attributes).
var allowedTags = setOf(
	"a", "abbr", "acronym", "address", "article", "aside", "b", "bdi", "bdo", "big",
	"blockquote", "br", "caption", "center", "cite", "code", "col", "colgroup", "dd",
	"del", "details", "dfn", "div", "dl", "dt", "em", "figcaption", "figure", "font",
	"footer", "h1", "h2", "h3", "h4", "h5", "h6", "header", "hgroup", "hr", "i", "img",
	"ins", "kbd", "li", "main", "mark", "nav", "ol", "p", "pre", "q", "rp", "rt", "ruby",
	"s", "samp", "section", "small", "span", "strike", "strong", "sub", "summary", "sup",
	"table", "tbody", "td", "tfoot", "th", "thead", "time", "tr", "tt", "u", "ul", "var",
	"wbr",
)

// voidTags never have content or an end tag.
var voidTags = setOf("br", "hr", "img", "wbr", "col")

// dropContentTags are removed together with everything inside them. Several
// are raw-text elements for the tokenizer, so their content is never markup.
// Void elements (embed, frame, ...) must not be listed: they never close.
var dropContentTags = setOf(
	"script", "iframe", "frameset", "object", "applet", "noscript",
	"noembed", "noframes", "template", "svg", "math", "textarea", "select", "xmp",
	"plaintext", "title", "video", "audio", "canvas", "datalist", "option", "optgroup",
	"dialog", "portal", "fencedframe",
)

// allowedAttrs are harmless presentational attributes allowed on any
// allowed element. URL-bearing attributes and style are handled separately.
var allowedAttrs = setOf(
	"abbr", "align", "alt", "axis", "bgcolor", "border", "cellpadding", "cellspacing",
	"char", "charoff", "class", "clear", "color", "cols", "colspan", "compact", "datetime",
	"dir", "face", "frame", "headers", "height", "hspace", "id", "lang", "noshade",
	"nowrap", "open", "reversed", "rowspan", "rules", "scope", "size", "span", "start",
	"summary", "title", "type", "valign", "value", "vspace", "width",
)

func setOf(s ...string) map[string]bool {
	m := make(map[string]bool, len(s))
	for _, v := range s {
		m[v] = true
	}
	return m
}

// maxOpenElements bounds output nesting (browsers cap the DOM depth too).
// Deeper allowed start tags are dropped, keeping their text. It also bounds
// the end-tag search, which would otherwise be quadratic on hostile input.
const maxOpenElements = 256

type sanitizer struct {
	msgID     int64
	cidBase   string                  // cid endpoint prefix ("" = /api/messages/<id>/cid/)
	inline    func(cid string) string // cid -> data: URI, "" for the cid URL
	b         strings.Builder
	open      []string // allowed elements currently open in the output
	hasRemote bool
	bodySeen  bool
}

// sanitizeHTML returns a safe rendering of src for message msgID (cid:
// references point at /api/messages/<id>/cid/) and whether remote content
// (images, stylesheets) was blocked.
func sanitizeHTML(src string, msgID int64) (string, bool) {
	return sanitizeHTMLInline(src, msgID, "", nil)
}

// sanitizeHTMLInline is sanitizeHTML with cid: references rewritten to
// cidBase + escaped Content-ID (cidBase must end in "/"; "" means the plain
// /api/messages/<id>/cid/ endpoint), where inline (may be nil) may resolve a
// reference to a raster data: URI instead.
func sanitizeHTMLInline(src string, msgID int64, cidBase string, inline func(cid string) string) (string, bool) {
	s := &sanitizer{msgID: msgID, cidBase: cidBase, inline: inline}
	z := html.NewTokenizer(strings.NewReader(src))
	var (
		skipTag   string // name of the dropped element we are inside
		skipDepth int
		inStyle   bool
		css       strings.Builder
	)
	for {
		tt := z.Next()
		if tt == html.ErrorToken {
			break // io.EOF or a read error: either way we are done
		}
		tok := z.Token()
		if skipTag != "" {
			switch {
			case (tt == html.StartTagToken || tt == html.SelfClosingTagToken) && tok.Data == skipTag:
				skipDepth++
			case tt == html.EndTagToken && tok.Data == skipTag:
				skipDepth--
				if skipDepth == 0 {
					skipTag = ""
				}
			}
			continue
		}
		if inStyle {
			if tt == html.TextToken {
				css.WriteString(tok.Data)
				continue
			}
			inStyle = false
			s.writeStyle(css.String())
			css.Reset()
			if tt == html.EndTagToken && tok.Data == "style" {
				continue
			}
		}
		switch tt {
		case html.TextToken:
			s.b.WriteString(html.EscapeString(tok.Data))
		case html.StartTagToken, html.SelfClosingTagToken:
			switch {
			case dropContentTags[tok.Data]:
				// Pushed even when self-closing: "<script/>" opens a raw
				// text element in HTML, the "/" is ignored.
				skipTag, skipDepth = tok.Data, 1
			case tok.Data == "style":
				inStyle = true
			case tok.Data == "body":
				s.bodyTag(tok)
			case allowedTags[tok.Data] && (voidTags[tok.Data] || len(s.open) < maxOpenElements):
				s.startTag(tok)
			}
			// Anything else (html, head, body, form, input, base, meta,
			// link, unknown elements...) is dropped; its text is kept.
		case html.EndTagToken:
			s.endTag(tok.Data)
		}
		// Comments and doctypes are dropped.
	}
	if inStyle {
		s.writeStyle(css.String())
	}
	for i := len(s.open) - 1; i >= 0; i-- {
		s.b.WriteString("</" + s.open[i] + ">")
	}
	return s.b.String(), s.hasRemote
}

func (s *sanitizer) writeStyle(css string) {
	if out := s.sanitizeCSS(css, false); strings.TrimSpace(out) != "" {
		s.b.WriteString("<style>" + out + "</style>")
	}
}

func (s *sanitizer) startTag(tok html.Token) {
	name := tok.Data
	s.b.WriteString("<" + name)
	externalLink := false
	for _, a := range tok.Attr {
		if a.Namespace != "" {
			continue
		}
		key := a.Key
		switch {
		case key == "href" && name == "a":
			u, ok, ext := s.linkURL(a.Val)
			if !ok {
				continue
			}
			externalLink = externalLink || ext
			s.attr("href", u)
		case key == "src" && name == "img", key == "background":
			u, ok, blocked := s.imageURL(a.Val)
			switch {
			case ok:
				s.attr(key, u)
			case blocked && key == "src":
				s.attr("data-blocked-src", a.Val)
			}
		case key == "srcset":
			if isRemoteish(a.Val) {
				s.hasRemote = true
			}
		case key == "style":
			if v := s.sanitizeCSS(a.Val, true); strings.TrimSpace(v) != "" {
				s.attr("style", v)
			}
		case allowedAttrs[key]:
			s.attr(key, a.Val)
		}
	}
	if externalLink {
		s.attr("target", "_blank")
		s.attr("rel", "noopener noreferrer")
	}
	s.b.WriteByte('>')
	if !voidTags[name] {
		s.open = append(s.open, name)
	}
}

// safeColor matches a colour name or #hex value of a <body> attribute.
func safeColor(v string) bool {
	if v == "" || len(v) > 32 {
		return false
	}
	for i, c := range []byte(v) {
		if !('a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || '0' <= c && c <= '9' || c == '#' && i == 0) {
			return false
		}
	}
	return true
}

// bodyTag carries the first <body>'s bgcolor, text and style attributes
// (from earlier-tool) as a wrapping <div style>, which stays open until the
// end of the output. The style goes through sanitizeCSS like any other.
func (s *sanitizer) bodyTag(tok html.Token) {
	if s.bodySeen {
		return
	}
	s.bodySeen = true
	var parts []string
	var style string
	for _, a := range tok.Attr {
		switch {
		case a.Namespace != "":
		case a.Key == "bgcolor" && safeColor(a.Val):
			parts = append(parts, "background-color: "+a.Val)
		case a.Key == "text" && safeColor(a.Val):
			parts = append(parts, "color: "+a.Val)
		case a.Key == "style":
			style = s.sanitizeCSS(a.Val, true)
		}
	}
	if strings.TrimSpace(style) != "" {
		parts = append(parts, strings.TrimSpace(style))
	}
	if len(parts) == 0 || len(s.open) >= maxOpenElements {
		return
	}
	s.b.WriteString("<div")
	s.attr("style", strings.Join(parts, "; "))
	s.b.WriteByte('>')
	s.open = append(s.open, "div")
}

func (s *sanitizer) attr(k, v string) {
	s.b.WriteString(" " + k + `="` + html.EscapeString(v) + `"`)
}

func (s *sanitizer) endTag(name string) {
	for i := len(s.open) - 1; i >= 0; i-- {
		if s.open[i] == name {
			for j := len(s.open) - 1; j >= i; j-- {
				s.b.WriteString("</" + s.open[j] + ">")
			}
			s.open = s.open[:i]
			return
		}
	}
	// Not open (stray or never emitted): dropped.
}

// cleanURL strips what browsers ignore when parsing a URL: ASCII tab and
// newlines anywhere, and leading/trailing C0 controls and spaces.
func cleanURL(v string) string {
	v = strings.Map(func(r rune) rune {
		if r == '\t' || r == '\n' || r == '\r' {
			return -1
		}
		return r
	}, v)
	return strings.TrimFunc(v, func(r rune) bool { return r <= ' ' })
}

// urlScheme returns the lower-cased scheme of u, or "" for a relative URL.
func urlScheme(u string) string {
	for i := 0; i < len(u); i++ {
		c := u[i]
		switch {
		case c == ':':
			if i == 0 {
				return ""
			}
			return asciiLower(u[:i])
		case 'a' <= c && c <= 'z', 'A' <= c && c <= 'Z':
		case i > 0 && ('0' <= c && c <= '9' || c == '+' || c == '-' || c == '.'):
		default:
			return ""
		}
	}
	return ""
}

// isRemoteish reports whether v looks like it points off-machine.
func isRemoteish(v string) bool {
	l := asciiLower(cleanURL(v))
	return strings.Contains(l, "http:") || strings.Contains(l, "https:") ||
		strings.Contains(l, "//") || strings.Contains(l, `\\`) || strings.Contains(l, `/\`)
}

// linkURL classifies an <a href>: http(s) and mailto links and same-document
// fragments are kept; ext reports a link that must open externally.
func (s *sanitizer) linkURL(v string) (u string, ok, ext bool) {
	u = cleanURL(v)
	switch urlScheme(u) {
	case "http", "https":
		return u, true, true
	case "mailto":
		return u, true, false
	case "":
		if safeFragment(u) {
			return u, true, false
		}
	}
	return "", false, false
}

// safeFragment reports whether u is a same-document link "#name" with a
// plain name (letters, digits, "-", "_", ".").
func safeFragment(u string) bool {
	if len(u) < 2 || u[0] != '#' {
		return false
	}
	for _, c := range []byte(u[1:]) {
		if !('a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || '0' <= c && c <= '9' || c == '-' || c == '_' || c == '.') {
			return false
		}
	}
	return true
}

// imageURL classifies an image source. cid: references are rewritten to the
// message's cid endpoint and safe raster data: URLs kept (ok); remote URLs
// are blocked and flagged; anything else is dropped.
func (s *sanitizer) imageURL(v string) (u string, ok, blocked bool) {
	u = cleanURL(v)
	switch urlScheme(u) {
	case "cid":
		if p := s.cidPath(u[len("cid:"):]); p != "" {
			return p, true, false
		}
		return "", false, false
	case "data":
		if safeDataImage(u) {
			return u, true, false
		}
		return "", false, false
	case "http", "https":
		s.hasRemote = true
		return "", false, true
	case "":
		if isRemoteish(u) && (strings.HasPrefix(u, "//") || strings.HasPrefix(u, `\`) || strings.HasPrefix(u, `/\`)) {
			s.hasRemote = true
			return "", false, true
		}
	}
	return "", false, false
}

func (s *sanitizer) cidPath(cid string) string {
	if dec, err := url.PathUnescape(cid); err == nil {
		cid = dec
	}
	cid = strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(strings.TrimSpace(cid), "<"), ">"))
	if cid == "" || cid == "." || cid == ".." {
		return ""
	}
	if s.inline != nil {
		if d := s.inline(cid); d != "" && safeDataImage(d) {
			return d
		}
	}
	return s.cidPrefix() + url.PathEscape(cid)
}

func safeDataImage(u string) bool {
	l := asciiLower(u)
	for _, t := range []string{"png", "jpeg", "gif", "webp"} {
		p := "data:image/" + t
		if strings.HasPrefix(l, p) && len(l) > len(p) && (l[len(p)] == ';' || l[len(p)] == ',') {
			return true
		}
	}
	return false
}

// cssBanned make a whole stylesheet / style attribute be dropped: script
// execution (old IE/Firefox) and string-taking image functions that would
// fetch without url().
var cssBanned = []string{
	"expression", "-moz-binding", "behavior", "behaviour", "javascript:", "vbscript:",
	"image-set(", "image(", "cross-fade(", "src(", "@namespace",
}

// maxCSSPasses bounds the fixpoint loop in sanitizeCSS.
const maxCSSPasses = 4

// sanitizeCSS makes CSS (a style attribute when attr, else a <style> body)
// safe. Escapes and comments are resolved first so nothing hides behind
// them, and "<" is removed so a <style> body can never close its element.
// Then @import rules are removed (each replaced by a space, so the text on
// either side cannot join into a new token) and every url() is rewritten:
// cid: → endpoint, raster data: kept, anything else → none; remote ones set
// hasRemote. That pass is repeated until the output is stable, and the final
// output is verified again (cssSafe). Anything banned, unstable or failing
// verification drops the whole attribute/stylesheet (fail closed), flagging
// hasRemote if it referenced something remote. @import is never valid in a
// style attribute, so there it also drops the attribute.
func (s *sanitizer) sanitizeCSS(in string, attr bool) string {
	d := decodeCSS(in)
	drop := func() string {
		if isRemoteish(d) {
			s.hasRemote = true
		}
		return ""
	}
	if cssBannedIn(d) || attr && strings.Contains(asciiLower(d), "@import") {
		return drop()
	}
	out := d
	for pass := 0; ; pass++ {
		if pass == maxCSSPasses {
			return drop()
		}
		next := s.cssPass(out)
		if next == out {
			break
		}
		out = next
	}
	if !s.cssSafe(out) {
		return drop()
	}
	return out
}

func cssBannedIn(d string) bool {
	l := asciiLower(d)
	for _, bad := range cssBanned {
		if strings.Contains(l, bad) {
			return true
		}
	}
	return false
}

// cssPass is one rewrite pass over decoded CSS. It is idempotent on its own
// output: url()s it produced are recognised and kept as they are.
func (s *sanitizer) cssPass(d string) string {
	l := asciiLower(d)
	var b strings.Builder
	for i := 0; i < len(d); {
		switch {
		case strings.HasPrefix(l[i:], "@import"):
			end := strings.IndexByte(l[i:], ';')
			if end < 0 {
				end = len(l) - i
			} else {
				end++
			}
			if isRemoteish(d[i : i+end]) {
				s.hasRemote = true
			}
			b.WriteByte(' ')
			i += end
		case strings.HasPrefix(l[i:], "url("):
			arg, n := cssURLArg(d[i+len("url("):])
			i += len("url(") + n
			if s.localCIDURL(arg) {
				b.WriteString(`url("` + arg + `")`)
				continue
			}
			u, ok, _ := s.imageURL(arg)
			if !ok {
				b.WriteString(" none ")
				continue
			}
			u = strings.Map(func(r rune) rune {
				if r == '"' || r == '\'' || r == ')' || r == '(' || r < ' ' {
					return -1
				}
				return r
			}, u)
			b.WriteString(`url("` + u + `")`)
		default:
			b.WriteByte(d[i])
			i++
		}
	}
	return b.String()
}

// cssSafe verifies final CSS output: nothing left for a CSS parser to
// decode, no banned token, no @import, and every url() local.
func (s *sanitizer) cssSafe(out string) bool {
	if decodeCSS(out) != out || cssBannedIn(out) {
		return false
	}
	l := asciiLower(out)
	if strings.Contains(l, "@import") {
		return false
	}
	for i := strings.Index(l, "url("); i >= 0; {
		arg, n := cssURLArg(out[i+len("url("):])
		if !s.localCIDURL(arg) && !safeDataImage(arg) {
			return false
		}
		i += len("url(") + n
		j := strings.Index(l[i:], "url(")
		if j < 0 {
			break
		}
		i += j
	}
	return true
}

func (s *sanitizer) cidPrefix() string {
	if s.cidBase != "" {
		return s.cidBase
	}
	return "/api/messages/" + strconv.FormatInt(s.msgID, 10) + "/cid/"
}

// localCIDURL reports whether u is exactly a cid endpoint URL as produced by
// cidPath (path-escaped Content-ID, not "." or ".." even percent-encoded).
func (s *sanitizer) localCIDURL(u string) bool {
	rest, ok := strings.CutPrefix(u, s.cidPrefix())
	if !ok || rest == "" {
		return false
	}
	// A browser normalises %2e like "." in a path, so dot segments are
	// rejected after unescaping too.
	if dec, err := url.PathUnescape(rest); err != nil || dec == "." || dec == ".." {
		return false
	}
	for _, c := range []byte(rest) {
		if !('a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || '0' <= c && c <= '9' || strings.IndexByte("-_.~$&+:=@%", c) >= 0) {
			return false
		}
	}
	return true
}

// cssURLArg parses the argument of url( from s (just after the parenthesis),
// returning it unquoted and the number of bytes consumed including ")".
func cssURLArg(s string) (string, int) {
	i := 0
	for i < len(s) && isCSSSpace(s[i]) {
		i++
	}
	var arg string
	if i < len(s) && (s[i] == '"' || s[i] == '\'') {
		q := s[i]
		j := strings.IndexByte(s[i+1:], q)
		if j < 0 {
			return s[i+1:], len(s)
		}
		arg = s[i+1 : i+1+j]
		i += j + 2
	} else {
		j := strings.IndexByte(s[i:], ')')
		if j < 0 {
			return strings.TrimSpace(s[i:]), len(s)
		}
		arg = strings.TrimRight(s[i:i+j], " \t\n\r\f")
		i += j
	}
	if j := strings.IndexByte(s[i:], ')'); j >= 0 {
		i += j + 1
	} else {
		i = len(s)
	}
	return arg, i
}

func isCSSSpace(c byte) bool { return c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\f' }

// decodeCSS strips comments, resolves CSS escapes and removes NUL, "<" and
// any backslash so the result means the same to every CSS parser.
func decodeCSS(s string) string {
	s = strings.ToValidUTF8(s, "\uFFFD")
	var b strings.Builder
	for i := 0; i < len(s); {
		c := s[i]
		switch {
		case c == '/' && i+1 < len(s) && s[i+1] == '*':
			end := strings.Index(s[i+2:], "*/")
			if end < 0 {
				return b.String()
			}
			i += end + 4
		case c == '\\':
			i++
			if i >= len(s) {
				continue
			}
			if isHex(s[i]) {
				j := i
				for j < len(s) && j-i < 6 && isHex(s[j]) {
					j++
				}
				v, _ := strconv.ParseUint(s[i:j], 16, 32)
				i = j
				if i < len(s) && isCSSSpace(s[i]) {
					if s[i] == '\r' && i+1 < len(s) && s[i+1] == '\n' {
						i++
					}
					i++
				}
				r := rune(v)
				if v == 0 || v > utf8.MaxRune || (0xD800 <= v && v <= 0xDFFF) {
					r = utf8.RuneError
				}
				if r != '\\' && r != '<' {
					b.WriteRune(r)
				}
				continue
			}
			r, n := utf8.DecodeRuneInString(s[i:])
			i += n
			if r != '\n' && r != '\r' && r != '\f' && r != '\\' && r != '<' {
				b.WriteRune(r)
			}
		case c == 0 || c == '<':
			i++
		default:
			b.WriteByte(c)
			i++
		}
	}
	return b.String()
}

func isHex(c byte) bool {
	return '0' <= c && c <= '9' || 'a' <= c && c <= 'f' || 'A' <= c && c <= 'F'
}

// asciiLower lower-cases ASCII letters only, keeping byte offsets intact.
func asciiLower(s string) string {
	b := []byte(s)
	for i, c := range b {
		if 'A' <= c && c <= 'Z' {
			b[i] = c + 'a' - 'A'
		}
	}
	return string(b)
}
