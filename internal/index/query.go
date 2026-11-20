package index

import (
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/lazyoracle/mail-export-viewer/internal/source"
)

const (
	// maxQueryBytes bounds the query text that is parsed; the rest is ignored.
	maxQueryBytes = 4096
	// maxQueryTerms bounds the number of parsed items (terms, phrases,
	// negations and operator values together).
	maxQueryTerms = 64
)

// Query is a parsed search query. All values are raw user text; they are
// escaped when the query is compiled to SQL/FTS5.
//
// Syntax: plain terms (the last one, while being typed, is a prefix),
// "quoted phrases", -negation (of a term, phrase or operator), and the
// operators
//
//	from: to: cc: subject:        full text in that field (to: covers Cc)
//	folder: (alias mailbox:)      folder path, or its last segment(s)
//	label: category: tag:         whole name, case-insensitive (ASCII);
//	                              label: also matches categories
//	filename:                     substring of an attachment name
//	is:unread|read|starred|flagged|answered|replied|forwarded|draft|
//	   deleted|trash|junk|spam|important
//	has:attachment
//	before: after:                YYYY, YYYY-MM or YYYY-MM-DD ('/' allowed),
//	                              UTC; before exclusive, after inclusive;
//	                              undated messages match only the negations
//
// Values may be quoted (inside quotes \" is a literal double quote and \\
// a literal backslash). Unknown operators and malformed values are plain
// text. A negated field operator excludes messages matching it in that
// field only (-from:bob keeps messages that merely mention bob elsewhere).
type Query struct {
	Terms, Phrases, Not []string

	From, To, Cc, Subject, Folder, Label, Category, Tag, Filename               []string
	NotFrom, NotTo, NotCc, NotSubject, NotFolder, NotLabel, NotCategory, NotTag []string
	NotFilename                                                                 []string

	// Is lists flags that must be set, NotIs flags that must be clear
	// (is:read is NotIs Unread; has:attachment is Is HasAttachment).
	Is, NotIs source.Flags

	Before, After *time.Time
	// NotBefore/NotAfter come from -before:/-after:; unlike after:/before:
	// they keep messages without a date.
	NotBefore, NotAfter *time.Time

	// prefixLast: the last item of the input is a plain (non-negated,
	// unquoted) term still being typed; it is matched as a prefix.
	prefixLast bool
}

// ParseQuery parses s. It never fails: anything it cannot interpret is
// treated as plain text.
func ParseQuery(s string) Query {
	var q Query
	if len(s) > maxQueryBytes {
		s = truncateUTF8(s, maxQueryBytes)
	}
	s = strings.ToValidUTF8(s, "")
	items := 0
	i := 0
	for i < len(s) && items < maxQueryTerms {
		for i < len(s) && isSpace(s[i]) {
			i++
		}
		if i >= len(s) {
			break
		}
		q.prefixLast = false
		neg := false
		if s[i] == '-' && i+1 < len(s) && !isSpace(s[i+1]) {
			neg = true
			i++
		}
		if s[i] == '"' {
			var ph string
			ph, i = readQuoted(s, i)
			if strings.TrimSpace(ph) == "" {
				continue
			}
			items++
			if neg {
				q.Not = append(q.Not, ph)
			} else {
				q.Phrases = append(q.Phrases, ph)
			}
			continue
		}
		start := i
		for i < len(s) && !isSpace(s[i]) && s[i] != ':' {
			i++
		}
		if i < len(s) && s[i] == ':' {
			key := strings.ToLower(s[start:i])
			if isOperator(key) {
				i++ // past ':'
				var val string
				quoted := i < len(s) && s[i] == '"'
				if quoted {
					val, i = readQuoted(s, i)
				} else {
					vs := i
					for i < len(s) && !isSpace(s[i]) {
						i++
					}
					val = s[vs:i]
				}
				val = strings.TrimSpace(val)
				if val == "" {
					continue // empty operator value: ignored
				}
				items++
				if !q.addOperator(key, val, neg) {
					// Malformed value: the whole token is plain text.
					q.addTerm(s[start:i], neg, i == len(s) && !quoted)
				}
				continue
			}
		}
		for i < len(s) && !isSpace(s[i]) {
			i++
		}
		items++
		q.addTerm(s[start:i], neg, i == len(s))
	}
	return q
}

func (q *Query) addTerm(t string, neg, atEnd bool) {
	if neg {
		q.Not = append(q.Not, t)
		return
	}
	q.Terms = append(q.Terms, t)
	q.prefixLast = atEnd
}

// addOperator records a known operator; false means the value is malformed
// and the token should be treated as plain text.
func (q *Query) addOperator(key, val string, neg bool) bool {
	switch key {
	case "is", "has":
		f, ok := parseIs(key, val)
		if !ok {
			return false
		}
		if f == 0 { // is:read
			f, neg = source.Unread, !neg
		}
		if neg {
			q.NotIs |= f
		} else {
			q.Is |= f
		}
		return true
	case "before", "after":
		t, ok := parseQueryDate(val)
		if !ok {
			return false
		}
		switch {
		case key == "before" && neg:
			q.NotBefore = &t
		case key == "before":
			q.Before = &t
		case neg:
			q.NotAfter = &t
		default:
			q.After = &t
		}
		return true
	}
	lists := map[string][2]*[]string{
		"from": {&q.From, &q.NotFrom}, "to": {&q.To, &q.NotTo}, "cc": {&q.Cc, &q.NotCc},
		"subject": {&q.Subject, &q.NotSubject}, "folder": {&q.Folder, &q.NotFolder},
		"mailbox": {&q.Folder, &q.NotFolder}, "label": {&q.Label, &q.NotLabel},
		"category": {&q.Category, &q.NotCategory}, "tag": {&q.Tag, &q.NotTag},
		"filename": {&q.Filename, &q.NotFilename},
	}
	l := lists[key][0]
	if neg {
		l = lists[key][1]
	}
	*l = append(*l, val)
	return true
}

// parseIs maps an is:/has: value to its flag; is:read gives 0.
func parseIs(key, val string) (source.Flags, bool) {
	val = strings.ToLower(val)
	if key == "has" {
		switch val {
		case "attachment", "attachments", "att":
			return source.HasAttachment, true
		}
		return 0, false
	}
	switch val {
	case "read", "seen":
		return 0, true
	}
	return source.ParseFlag(val)
}

func isOperator(key string) bool {
	switch key {
	case "from", "to", "cc", "subject", "folder", "mailbox", "label", "category", "tag", "is", "has",
		"filename", "before", "after":
		return true
	}
	return false
}

// readQuoted reads a "quoted" string starting at s[i] == '"'. Inside it, \"
// is a literal double quote and \\ a literal backslash; any other backslash
// is kept as is. An unterminated quote runs to the end of s. It returns the
// contents and the index after it.
func readQuoted(s string, i int) (string, int) {
	i++
	var b strings.Builder
	for i < len(s) {
		switch c := s[i]; {
		case c == '"':
			return b.String(), i + 1
		case c == '\\' && i+1 < len(s) && (s[i+1] == '"' || s[i+1] == '\\'):
			b.WriteByte(s[i+1])
			i += 2
		default:
			b.WriteByte(c)
			i++
		}
	}
	return b.String(), len(s)
}

func isSpace(b byte) bool {
	return b == ' ' || b == '\t' || b == '\n' || b == '\r' || b == '\f' || b == '\v'
}

func parseQueryDate(v string) (time.Time, bool) {
	v = strings.ReplaceAll(v, "/", "-")
	for _, layout := range []string{"2006-01-02", "2006-01", "2006"} {
		if len(v) != len(layout) {
			continue
		}
		if t, err := time.Parse(layout, v); err == nil {
			return t.UTC(), true
		}
	}
	return time.Time{}, false
}

// ftsQuote returns s as one FTS5 string (a phrase of whatever tokens the
// tokenizer finds in it). Nothing inside is interpreted as FTS5 syntax.
func ftsQuote(s string) string {
	s = strings.ReplaceAll(s, "\x00", "")
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}

// hasTokenChars reports whether the unicode61 tokenizer would find at least
// one token in s (letters, numbers and private-use characters are token
// characters). A string without any is dropped: it can never match.
func hasTokenChars(s string) bool {
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsNumber(r) || unicode.Is(unicode.Co, r) {
			return true
		}
	}
	return false
}

// isCJK reports whether s contains characters of scripts written without
// spaces, which unicode61 indexes as one long token per run; such terms are
// matched by substring (LIKE/instr) on subject and people as well.
func isCJK(s string) bool {
	for _, r := range s {
		if unicode.In(r, unicode.Han, unicode.Hiragana, unicode.Katakana, unicode.Hangul, unicode.Thai) {
			return true
		}
	}
	return false
}

// minPrefixRunes is the minimum length of a type-ahead term matched as a
// prefix; shorter prefixes expand to too many index terms to be fast.
const minPrefixRunes = 3

func prefixable(t string) bool {
	return utf8.RuneCountInString(t) >= minPrefixRunes && !isCJK(t)
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
