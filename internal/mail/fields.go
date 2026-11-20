package mail

import (
	"io"
	"mime"
	"net/mail"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

// Address is one mailbox. Either field may be empty.
type Address struct{ Name, Email string }

// String formats a as "Name <email>", "email" or "Name" (no quoting or
// encoding: for display and full-text indexing, not for writing headers).
func (a Address) String() string {
	switch {
	case a.Name != "" && a.Email != "":
		return a.Name + " <" + a.Email + ">"
	case a.Email != "":
		return a.Email
	default:
		return a.Name
	}
}

// FormatAddresses joins the non-empty String forms of as with ", ".
func FormatAddresses(as []Address) string {
	parts := make([]string, 0, len(as))
	for _, a := range as {
		if s := a.String(); s != "" {
			parts = append(parts, s)
		}
	}
	return strings.Join(parts, ", ")
}

// charsetReader lets mime.WordDecoder (used by net/mail) handle any charset,
// falling back like DecodeCharset instead of failing.
func charsetReader(cs string, r io.Reader) (io.Reader, error) {
	b, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	return strings.NewReader(DecodeCharset(b, cs)), nil
}

var addrParser = &mail.AddressParser{WordDecoder: &mime.WordDecoder{
	CharsetReader: charsetReader,
}}

// ParseAddressList parses an address list header value (RFC 2047 words
// allowed; invalid UTF-8 is read as windows-1252). It tries net/mail on the
// whole list, then per comma-separated item, then a heuristic; group names
// are dropped and items that yield nothing are skipped. It never fails.
func ParseAddressList(s string) []Address {
	if !utf8.ValidString(s) {
		s = fallbackDecode([]byte(s))
	}
	return parseAddressList(s)
}

// parseAddressList is ParseAddressList for input already in UTF-8.
func parseAddressList(s string) []Address {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	if list, err := addrParser.ParseList(s); err == nil {
		return convertAddrs(list)
	}
	var out []Address
	for _, item := range splitAddressItems(s) {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		if list, err := addrParser.ParseList(item); err == nil {
			out = append(out, convertAddrs(list)...)
			continue
		}
		if a, ok := heuristicAddress(item); ok {
			out = append(out, a)
		}
	}
	return out
}

func convertAddrs(list []*mail.Address) []Address {
	var out []Address
	for _, a := range list {
		if a == nil {
			continue
		}
		out = append(out, Address{Name: cleanName(a.Name), Email: strings.TrimSpace(a.Address)})
	}
	return out
}

func cleanName(n string) string {
	n = strings.TrimSpace(decodeWords(n))
	return strings.TrimSpace(strings.Trim(n, `"'`))
}

// splitAddressItems splits on commas and semicolons outside quotes, angle
// brackets and comments, and strips group display names ("Team:").
func splitAddressItems(s string) []string {
	var items []string
	var cur strings.Builder
	inQuote, angle, paren := false, 0, 0
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '\\' && i+1 < len(s) && (inQuote || paren > 0):
			cur.WriteByte(c)
			i++
			cur.WriteByte(s[i])
			continue
		case c == '"' && paren == 0:
			inQuote = !inQuote
		case inQuote:
		case c == '(':
			paren++
		case c == ')' && paren > 0:
			paren--
		case paren > 0:
		case c == '<':
			angle++
		case c == '>' && angle > 0:
			angle--
		case angle > 0:
		case c == ',' || c == ';':
			items = append(items, cur.String())
			cur.Reset()
			continue
		case c == ':':
			// Group display name: drop what came before.
			cur.Reset()
			continue
		}
		cur.WriteByte(c)
	}
	items = append(items, cur.String())
	return items
}

var commentRe = regexp.MustCompile(`\(([^()]*)\)`)

// heuristicAddress extracts something useful from an item net/mail rejected.
func heuristicAddress(item string) (Address, bool) {
	var comment string
	if m := commentRe.FindStringSubmatch(item); m != nil {
		comment = m[1]
	}
	item = strings.TrimSpace(commentRe.ReplaceAllString(item, " "))
	if lt := strings.LastIndexByte(item, '<'); lt >= 0 {
		gt := strings.IndexByte(item[lt:], '>')
		var email string
		if gt >= 0 {
			email = item[lt+1 : lt+gt]
		} else {
			email = item[lt+1:]
		}
		email = strings.Trim(strings.TrimSpace(email), "<>")
		name := cleanName(strings.Trim(item[:lt], "<> \t"))
		if name == "" {
			name = cleanName(comment)
		}
		if !strings.Contains(email, "@") || strings.ContainsAny(email, " <>") {
			return Address{}, false
		}
		return Address{Name: name, Email: email}, true
	}
	fields := strings.Fields(item)
	for i, f := range fields {
		if strings.Contains(f, "@") {
			name := strings.Join(append(append([]string{}, fields[:i]...), fields[i+1:]...), " ")
			name = cleanName(name)
			if name == "" {
				name = cleanName(comment)
			}
			return Address{Name: name, Email: strings.Trim(f, `"'<>`)}, true
		}
	}
	if len(fields) == 1 && !strings.ContainsAny(fields[0], `<>"`) {
		return Address{Email: fields[0]}, true
	}
	if len(fields) > 1 {
		return Address{Name: cleanName(item)}, true
	}
	return Address{}, false
}

// --- dates ---

var (
	dateCommentRe = regexp.MustCompile(`\([^()]*\)`)
	weekdayRe     = regexp.MustCompile(`(?i)^(?:mon|tue|wed|thu|fri|sat|sun)[a-z]*\.?,?\s+`)
	gmtOffsetRe   = regexp.MustCompile(`(?i)\b(?:GMT|UTC|UT)\s*([+-]\d{2}):?(\d{2})\b`)
	zoneRe        = regexp.MustCompile(`(?i)\s(EST|EDT|CST|CDT|MST|MDT|PST|PDT|GMT|UTC|UT|Z)$`)
)

// obsoleteZones maps RFC 822 zone names to numeric offsets, so results never
// depend on the host's time zone abbreviations.
var obsoleteZones = map[string]string{
	"EST": "-0500", "EDT": "-0400", "CST": "-0600", "CDT": "-0500",
	"MST": "-0700", "MDT": "-0600", "PST": "-0800", "PDT": "-0700",
	"GMT": "+0000", "UTC": "+0000", "UT": "+0000", "Z": "+0000",
}

// dateLayouts are tried (after the weekday is removed) when net/mail fails.
var dateLayouts = []string{
	"2 Jan 2006 15:04:05 -0700",
	"2 Jan 2006 15:04 -0700",
	"2 Jan 06 15:04:05 -0700",
	"2 Jan 06 15:04 -0700",
	"2 Jan 2006 15:04:05",
	"2 Jan 2006 15:04",
	"Jan _2 15:04:05 2006",
	"Jan _2 15:04:05 -0700 2006",
	time.RFC3339,
	"2006-01-02T15:04:05",
	"2006-01-02 15:04:05 -0700",
	"2006-01-02 15:04:05",
}

// ParseDate parses a Date header value tolerantly (comments, missing or
// wrong weekday, obsolete and "GMT+0530" zones, ctime and ISO forms),
// returning the zero time if nothing matches or the year is outside
// 1970-2200.
func ParseDate(s string) time.Time {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}
	}
	for i := 0; i < 8 && strings.Contains(s, "("); i++ {
		s = dateCommentRe.ReplaceAllString(s, " ")
	}
	if i := strings.IndexByte(s, '('); i >= 0 { // unterminated comment
		s = s[:i]
	}
	s = strings.TrimSuffix(strings.Join(strings.Fields(s), " "), ".")
	s = gmtOffsetRe.ReplaceAllString(s, "$1$2")
	s = zoneRe.ReplaceAllStringFunc(s, func(m string) string {
		return " " + obsoleteZones[strings.ToUpper(strings.TrimSpace(m))]
	})
	t, err := mail.ParseDate(s)
	if err != nil {
		s = weekdayRe.ReplaceAllString(s, "")
		for _, layout := range dateLayouts {
			if t, err = time.Parse(layout, s); err == nil {
				break
			}
		}
	}
	if err != nil || t.Year() < 1970 || t.Year() > 2200 {
		return time.Time{}
	}
	return t
}

// --- message ids ---

var msgIDRe = regexp.MustCompile(`<([^<>\s]+)>`)

// ParseMessageIDs extracts message ids (without angle brackets) from a
// Message-ID, In-Reply-To or References value. Values with no bracketed ids
// fall back to whitespace-separated tokens.
func ParseMessageIDs(s string) []string {
	var ids []string
	for _, m := range msgIDRe.FindAllStringSubmatch(s, -1) {
		ids = append(ids, m[1])
	}
	if ids == nil {
		for _, f := range strings.Fields(s) {
			f = strings.Trim(f, "<>")
			if f != "" {
				ids = append(ids, f)
			}
		}
	}
	return ids
}

func firstMsgID(s string) string {
	if ids := ParseMessageIDs(s); len(ids) > 0 {
		return ids[0]
	}
	return ""
}

// --- label lists ---

// ParseLabels turns a raw (unfolded, not RFC 2047 decoded) comma-separated
// label list such as an X-Gmail-Labels value into labels. Two encodings
// are seen:
//   - the whole value as RFC 2047 encoded words only: decoded first, then
//     split;
//   - plain or per-label encoded values: split on commas outside quotes
//     and outside encoded words, then each label decoded, so an encoded
//     name holding a comma stays one label.
//
// Quotes of the list syntax are dropped (quotes decoded from an encoded
// word are kept); empty labels and repeats are skipped; control characters
// become spaces.
func ParseLabels(s string) []string {
	if !utf8.ValidString(s) {
		s = fallbackDecode([]byte(s))
	}
	var parts []string
	if strings.TrimSpace(encodedWordRe.ReplaceAllString(s, "")) == "" {
		parts = splitLabels(decodeWords(s))
	} else {
		for _, p := range splitLabelsRaw(s) {
			parts = append(parts, decodeWords(p))
		}
	}
	var out []string
	seen := map[string]bool{}
	for _, l := range parts {
		l = strings.Map(func(r rune) rune {
			if r < 0x20 || r == 0x7f {
				return ' '
			}
			return r
		}, l)
		// The splitters already dropped the quotes of the list syntax;
		// quotes left here were decoded from an encoded word and are
		// part of the name.
		l = strings.TrimSpace(l)
		if l == "" || seen[l] {
			continue
		}
		seen[l] = true
		out = append(out, l)
	}
	return out
}

// splitLabelsRaw is splitLabels that also never splits inside an RFC 2047
// encoded word.
func splitLabelsRaw(s string) []string {
	words := encodedWordRe.FindAllStringIndex(s, -1)
	var out []string
	var cur strings.Builder
	inQuote := false
	emit := func() {
		l := strings.TrimSpace(cur.String())
		l = strings.TrimSpace(strings.Trim(l, `"`))
		if l != "" {
			out = append(out, l)
		}
		cur.Reset()
	}
	for i := 0; i < len(s); i++ {
		if len(words) > 0 && i == words[0][0] {
			cur.WriteString(s[i:words[0][1]])
			i = words[0][1] - 1
			words = words[1:]
			continue
		}
		c := s[i]
		switch {
		case c == '"':
			inQuote = !inQuote
		case c == ',' && !inQuote:
			emit()
		default:
			cur.WriteByte(c)
		}
	}
	emit()
	return out
}

// splitLabels splits a label list on commas outside quotes.
func splitLabels(s string) []string {
	var out []string
	var cur strings.Builder
	inQuote := false
	emit := func() {
		l := strings.TrimSpace(cur.String())
		l = strings.TrimSpace(strings.Trim(l, `"`))
		if l != "" {
			out = append(out, l)
		}
		cur.Reset()
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '"':
			inQuote = !inQuote
		case c == ',' && !inQuote:
			emit()
		default:
			cur.WriteByte(c)
		}
	}
	emit()
	return out
}
