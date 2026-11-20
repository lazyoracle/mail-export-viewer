package server

import (
	"encoding/base64"
	"mime"
	"net/http"

	"github.com/lazyoracle/mail-export-viewer/internal/mail"
)

// inlineRasterTypes may be inlined as data: URIs. The declared type and the
// sniffed bytes must both be one of these; SVG (which can carry script) is
// never inlined.
var inlineRasterTypes = setOf("image/png", "image/jpeg", "image/gif", "image/webp")

// maxInlineParts caps the distinct parts inlined per message, so mail with
// thousands of cid parts costs a bounded amount of work.
const maxInlineParts = 64

// cidInliner resolves cid: references in one message's HTML to data: URIs,
// so inline images render in every browser without the sandboxed frame
// (opaque origin) having to fetch them cross-origin. Images over part bytes
// (decoded), past total bytes emitted for the message (each reference
// counts, so repeating one image cannot blow up the page), or beyond the
// first maxInlineParts candidates keep the tokenised cid URL route.
//
// The candidates (cid-bearing raster parts, in document order, within the
// part/total/count limits by declared size) are fetched together on the
// first reference, so the message is re-read once however many cid parts
// it has.
type cidInliner struct {
	atts        []mail.Attachment
	fetch       func(want map[int]bool) map[int][]byte
	part, total int
	used        int                    // decoded bytes inlined so far
	done        map[string]inlinedPart // by cid reference
	decoded     map[int][]byte         // candidate index -> bytes
	prepared    bool
}

type inlinedPart struct {
	uri  string // "" if not inlinable
	size int    // decoded bytes
}

func (c *cidInliner) resolve(cid string) string {
	if c.used >= c.total {
		return ""
	}
	p, seen := c.done[cid]
	if !seen {
		p = c.build(cid)
		if c.done == nil {
			c.done = map[string]inlinedPart{}
		}
		c.done[cid] = p
	}
	if p.uri == "" || c.used+p.size > c.total {
		return ""
	}
	c.used += p.size
	return p.uri
}

func (c *cidInliner) candidate(a mail.Attachment) bool {
	if a.ContentID == "" || a.Size <= 0 || a.Size > int64(c.part) {
		return false
	}
	mt, _, err := mime.ParseMediaType(a.ContentType)
	return err == nil && inlineRasterTypes[mt]
}

func (c *cidInliner) prepare() {
	c.prepared = true
	want := map[int]bool{}
	var sum int64
	for _, a := range c.atts {
		if len(want) == maxInlineParts {
			break
		}
		if c.candidate(a) && sum+a.Size <= int64(c.total) {
			want[a.Index] = true
			sum += a.Size
		}
	}
	if len(want) > 0 {
		c.decoded = c.fetch(want)
	}
}

func (c *cidInliner) build(cid string) (p inlinedPart) {
	a, ok := mail.FindCID(c.atts, cid)
	if !ok {
		return p
	}
	if !c.prepared {
		c.prepare()
	}
	data, ok := c.decoded[a.Index]
	if !ok || len(data) == 0 || len(data) > c.part {
		return p
	}
	sniffed := http.DetectContentType(data)
	if !inlineRasterTypes[sniffed] {
		return p
	}
	return inlinedPart{uri: "data:" + sniffed + ";base64," + base64.StdEncoding.EncodeToString(data), size: len(data)}
}
