package mbox

import (
	"bufio"
	"bytes"
	"io"
	"regexp"
	"strings"
	"time"
)

// message is one message found by scanMessages. Offset and Length cover the
// "From " separator line and the message up to (not including) the next
// separator, so they include the blank line that precedes it. Raw is the
// message after the separator line with mboxrd quoting removed, line endings
// kept and the one separator blank line dropped; when the message is longer
// than the prefix cap it is only the (unquoted) first cap bytes and
// Truncated is set. The pointer passed to the callback is reused.
type message struct {
	Offset, Length int64
	From           string // envelope sender of the From_ line
	Date           time.Time
	Raw            []byte
	Truncated      bool
}

// maxSeparatorLen bounds the length of a line considered as a separator.
const maxSeparatorLen = 1024

const (
	weekdays = `Mon|Tue|Wed|Thu|Fri|Sat|Sun`
	months   = `Jan|Feb|Mar|Apr|May|Jun|Jul|Aug|Sep|Oct|Nov|Dec`
	tz       = `(?: +(?:[A-Za-z]{1,5}|[+-]\d{4}))?`
	tail     = ` +\d{4}` + tz + `[ \t]*\r?\n?$`
)

// looseRE needs a blank previous line (or the start of the file) to count.
var looseRE = regexp.MustCompile(`^From +\S+ +(?:(?:` + weekdays + `),? +)?(?:` + months + `) +\d{1,2} +\d{1,2}:\d\d(?::\d\d)?` + tz + tail)

// strictRE is accepted even without a preceding blank line.
var strictRE = regexp.MustCompile(`^From +\S+ +(?:` + weekdays + `),? +(?:` + months + `) +\d{1,2} +\d{1,2}:\d\d:\d\d` + tz + tail)

var dateLayouts = []string{
	"Mon Jan 2 15:04:05 2006",
	"Mon Jan 2 15:04 2006",
	"Jan 2 15:04:05 2006",
	"Jan 2 15:04 2006",
	"Mon Jan 2 15:04:05 -0700 2006",
	"Mon Jan 2 15:04:05 MST 2006",
	"Jan 2 15:04:05 -0700 2006",
	"Jan 2 15:04:05 MST 2006",
	"Mon Jan 2 15:04:05 2006 -0700",
	"Mon Jan 2 15:04:05 2006 MST",
	"Jan 2 15:04:05 2006 -0700",
	"Jan 2 15:04:05 2006 MST",
}

func isBlank(line []byte) bool {
	return len(line) == 1 && line[0] == '\n' || len(line) == 2 && line[0] == '\r' && line[1] == '\n'
}

// isSeparator reports whether line (one complete line) is a From_ separator;
// prevBlank says whether the line before it was blank.
func isSeparator(line []byte, prevBlank bool) bool {
	if len(line) > maxSeparatorLen || !bytes.HasPrefix(line, []byte("From ")) {
		return false
	}
	if prevBlank && looseRE.Match(line) {
		return true
	}
	return strictRE.Match(line)
}

// parseSeparator extracts the envelope sender and date of a From_ line; the
// date is zero when it does not parse.
func parseSeparator(line []byte) (from string, date time.Time) {
	f := strings.Fields(string(bytes.TrimRight(line, "\r\n")))
	if len(f) < 2 {
		return
	}
	from = f[1]
	rest := strings.ReplaceAll(strings.Join(f[2:], " "), ",", "")
	for _, l := range dateLayouts {
		if t, err := time.Parse(l, rest); err == nil {
			return from, t
		}
	}
	return
}

// scanMessages streams r and calls fn for each message, holding at most
// prefixCap bytes of the message being read (plus a 64 KiB read buffer), so
// memory stays bounded whatever the message sizes or line lengths. A UTF-8
// BOM is skipped. Messages completed before a read error are delivered; the
// one in progress is dropped and the error returned. fn's error stops the
// scan and is returned as is.
func scanMessages(r io.Reader, prefixCap int, fn func(*message) error) error {
	br := bufio.NewReaderSize(r, 64<<10)
	var pos int64
	if b, _ := br.Peek(3); bytes.Equal(b, []byte("\xef\xbb\xbf")) {
		br.Discard(3)
		pos = 3
	}
	var (
		cur       message
		buf       []byte
		inMsg     bool
		truncated bool // chunks were dropped beyond prefixCap
		prevBlank = true
		cont      bool // the chunk continues a line longer than the read buffer
	)
	finish := func(end int64) error {
		cur.Length = end - cur.Offset
		cur.Truncated = truncated
		cur.Raw = unquote(buf)
		if !cur.Truncated {
			cur.Raw = trimSeparatorBlank(cur.Raw)
		}
		inMsg = false
		return fn(&cur)
	}
	for {
		chunk, err := br.ReadSlice('\n')
		long := err == bufio.ErrBufferFull
		if err != nil && !long && err != io.EOF {
			return err
		}
		if len(chunk) > 0 {
			start := pos
			pos += int64(len(chunk))
			if !cont && !long && isSeparator(chunk, prevBlank) {
				if inMsg {
					if ferr := finish(start); ferr != nil {
						return ferr
					}
				}
				cur = message{Offset: start}
				cur.From, cur.Date = parseSeparator(chunk)
				buf = make([]byte, 0, 4096)
				inMsg, truncated = true, false
			} else if inMsg {
				c := chunk
				if room := prefixCap - len(buf); len(c) > room {
					c, truncated = c[:room], true
				}
				buf = append(buf, c...)
			}
			prevBlank = !cont && !long && isBlank(chunk)
			cont = long
		}
		if err == io.EOF {
			break
		}
	}
	if inMsg {
		return finish(pos)
	}
	return nil
}

// unquote removes one '>' from every line matching ^>+From (mboxrd; also
// right for mboxo, which only quotes bare "From "). It works in place on
// body, which the caller must own, and moves bytes only once a quoted line
// has been seen.
func unquote(body []byte) []byte {
	r, w := 0, 0
	for r < len(body) {
		end := len(body)
		if i := bytes.IndexByte(body[r:], '\n'); i >= 0 {
			end = r + i + 1
		}
		skip := 0
		if body[r] == '>' {
			j := r
			for j < end && body[j] == '>' {
				j++
			}
			if bytes.HasPrefix(body[j:end], []byte("From ")) {
				skip = 1
			}
		}
		if skip == 1 || w != r {
			copy(body[w:], body[r+skip:end])
		}
		w += end - r - skip
		r = end
	}
	return body[:w]
}

// trimSeparatorBlank drops the one blank line the mbox format adds after
// every message.
func trimSeparatorBlank(b []byte) []byte {
	switch {
	case bytes.HasSuffix(b, []byte("\n\r\n")):
		return b[:len(b)-2]
	case bytes.HasSuffix(b, []byte("\n\n")):
		return b[:len(b)-1]
	case bytes.Equal(b, []byte("\n")), bytes.Equal(b, []byte("\r\n")):
		return b[:0]
	}
	return b
}
