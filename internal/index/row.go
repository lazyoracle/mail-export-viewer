package index

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/lazyoracle/mail-export-viewer/internal/mail"
	"github.com/lazyoracle/mail-export-viewer/internal/source"
)

const (
	// snippetLen is the snippet length in runes.
	snippetLen = 200
	// maxProps and maxPropBytes bound the properties kept per message
	// (key and value each cut to maxPropBytes/4).
	maxProps     = 256
	maxPropBytes = 64 << 10
	// maxNames bounds labels, tags and attachment names kept per message,
	// maxNameBytes the length of each.
	maxNames     = 256
	maxNameBytes = 512
	// maxFieldBytes bounds header-derived text columns; maxSortKeyBytes
	// the sort keys (subject, sender), which travel in paging cursors.
	maxFieldBytes   = 8 << 10
	maxSortKeyBytes = 1 << 10
	// maxRefs bounds the References kept for threading.
	maxRefs = 100
)

// resolve is source.Entry.Resolve; a variable so tests can make it panic.
var resolve = (*source.Entry).Resolve

// row is the bounded data of one message, ready to insert.
type row struct {
	folder, loc                  string
	messageID, inReplyTo, refs   string
	subject, fromName, fromEmail string
	toText, ccText               string
	date                         sql.NullInt64
	flags                        source.Flags
	attNames                     string
	labels, tags                 []string
	snippet, props               string
	parseErr                     bool
	ftsFrom, ftsTo, body         string
	end                          int64 // Loc.Offset+Length (byte progress)
	size                         int   // approximate bytes held
}

// resolveEntry runs resolve, recovering from a panic: the message is then
// listed from what the source gave (its fallback date) and the panic is
// returned as an error.
func resolveEntry(e *source.Entry) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("summarise message %s: panic: %v", e.Loc, r)
			if e.Msg == nil {
				e.Msg = &mail.Summary{Envelope: mail.Envelope{Date: e.Date}}
			}
			e.Raw = nil
		}
	}()
	resolve(e)
	return nil
}

// hlStrip removes the private-use runes Search uses as highlight markers,
// so indexed text can never fake or unbalance them.
var hlStrip = strings.NewReplacer(hlOpen, "", hlClose, "")

// clean makes s valid UTF-8 without highlight markers, at most n bytes.
func clean(s string, n int) string {
	return truncateUTF8(hlStrip.Replace(strings.ToValidUTF8(s, "�")), n)
}

// names cleans a name list: valid UTF-8, no newlines, bounded, no empty
// names or duplicates, in order.
func names(in []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, n := range in {
		n = strings.TrimSpace(strings.ReplaceAll(clean(n, maxNameBytes), "\n", " "))
		if n == "" || seen[n] {
			continue
		}
		seen[n] = true
		out = append(out, n)
		if len(out) == maxNames {
			break
		}
	}
	return out
}

// buildRow turns a resolved entry into a row. srcName names the source
// when the entry has no folder.
func buildRow(e *source.Entry, srcName string) row {
	m := e.Msg
	r := row{
		folder: cleanFolder(e.Folder, srcName),
		loc:    e.Loc.String(),
		flags:  e.Flags,
		labels: names(e.Labels),
		tags:   names(e.Tags),
		end:    e.Loc.Offset + e.Loc.Length,
	}
	r.messageID = clean(m.MessageID, maxNameBytes)
	r.inReplyTo = clean(m.InReplyTo, maxNameBytes)
	refs := m.References
	if len(refs) > maxRefs {
		refs = refs[len(refs)-maxRefs:]
	}
	r.refs = clean(strings.Join(refs, " "), maxRefs*maxNameBytes)
	r.subject = clean(m.Subject, maxSortKeyBytes)
	r.fromName, r.fromEmail = clean(m.From.Name, maxSortKeyBytes), clean(m.From.Email, maxSortKeyBytes)
	r.toText = clean(mail.FormatAddresses(m.To), maxFieldBytes)
	r.ccText = clean(mail.FormatAddresses(m.Cc), maxFieldBytes)
	if !m.Date.IsZero() {
		r.date = sql.NullInt64{Int64: m.Date.Unix(), Valid: true}
	}
	if m.HasAttachment {
		r.flags |= source.HasAttachment
	}
	r.attNames = strings.Join(names(m.AttachmentNames), "\n")
	r.body = clean(m.Text, mail.MaxSummaryText)
	r.snippet = makeSnippet(r.body)
	r.ftsFrom = strings.TrimSpace(r.fromName + " " + r.fromEmail)
	r.ftsTo = strings.TrimSpace(r.toText + " " + r.ccText)
	r.props = encodeProps(e.Props)
	r.size = len(r.body) + len(r.props) + len(r.subject) + len(r.toText) + len(r.ccText) + 512
	return r
}

// cleanFolder normalises a folder path: valid UTF-8, no empty segments,
// never empty (the source name stands in).
func cleanFolder(f, srcName string) string {
	var segs []string
	for _, s := range strings.Split(clean(f, maxFieldBytes), "/") {
		if s = strings.TrimSpace(strings.ReplaceAll(s, "\n", " ")); s != "" {
			segs = append(segs, s)
		}
	}
	if len(segs) == 0 {
		return clean(srcName, maxNameBytes)
	}
	return strings.Join(segs, "/")
}

func encodeProps(ps []source.Prop) string {
	if len(ps) == 0 {
		return ""
	}
	kv := make([][2]string, 0, min(len(ps), maxProps))
	total := 0
	for _, p := range ps {
		k, v := clean(p.Key, maxPropBytes/4), clean(p.Value, maxPropBytes/4)
		if len(kv) == maxProps || total+len(k)+len(v) > maxPropBytes {
			break
		}
		total += len(k) + len(v)
		kv = append(kv, [2]string{k, v})
	}
	b, _ := json.Marshal(kv)
	return string(b)
}

// makeSnippet collapses whitespace and keeps the first snippetLen runes.
func makeSnippet(text string) string {
	var b strings.Builder
	runes := 0
	for _, w := range strings.Fields(truncateUTF8(text, snippetLen*8)) {
		if runes > 0 {
			if runes >= snippetLen {
				break
			}
			b.WriteByte(' ')
			runes++
		}
		for _, c := range w {
			if runes >= snippetLen {
				break
			}
			b.WriteRune(c)
			runes++
		}
	}
	return b.String()
}
