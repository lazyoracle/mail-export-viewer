package mail

import (
	"strings"

	"golang.org/x/net/html"
)

// blockTags start a new line in extracted text.
var blockTags = map[string]bool{
	"p": true, "div": true, "br": true, "li": true, "tr": true, "table": true,
	"h1": true, "h2": true, "h3": true, "h4": true, "h5": true, "h6": true,
	"blockquote": true, "pre": true, "ul": true, "ol": true, "hr": true,
	"section": true, "article": true, "header": true, "footer": true,
	"dt": true, "dd": true, "dl": true, "address": true, "center": true,
	"form": true, "body": true, "html": true, "tbody": true, "thead": true,
	"fieldset": true, "nav": true, "aside": true, "main": true, "caption": true,
}

// skipTags have content that is never displayed as text.
var skipTags = map[string]bool{
	"script": true, "style": true, "head": true, "title": true,
	"noscript": true, "template": true, "svg": true, "object": true,
}

// TextFromHTML extracts readable plain text from HTML for indexing and
// plain-text display: scripts/styles/comments dropped, entities decoded,
// whitespace (including NBSP) collapsed, one line per block element, no
// blank lines.
func TextFromHTML(h string) string {
	z := html.NewTokenizer(strings.NewReader(h))
	var lines []string
	var cur strings.Builder
	skip := 0
	newline := func() {
		if l := strings.Join(strings.Fields(cur.String()), " "); l != "" {
			lines = append(lines, l)
		}
		cur.Reset()
	}
	for {
		tt := z.Next()
		switch tt {
		case html.ErrorToken:
			newline()
			return strings.Join(lines, "\n")
		case html.TextToken:
			if skip == 0 {
				cur.Write(z.Text())
			}
		case html.StartTagToken, html.EndTagToken, html.SelfClosingTagToken:
			name, _ := z.TagName()
			tag := string(name)
			if skipTags[tag] {
				switch tt {
				case html.StartTagToken:
					skip++
				case html.EndTagToken:
					if skip > 0 {
						skip--
					}
				}
				continue
			}
			if blockTags[tag] {
				newline()
			} else if tag == "td" || tag == "th" {
				cur.WriteByte(' ')
			}
		}
	}
}
