package mbox

import (
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/lazyoracle/mail-export-viewer/internal/mail"
	"github.com/lazyoracle/mail-export-viewer/internal/source"
)

// Limits on what hostile headers can make an Entry carry.
const (
	maxProps     = 64
	maxPropValue = 2048
)

// headerMeta is the metadata a message's own headers carry.
type headerMeta struct {
	Flags        source.Flags
	Labels, Tags []string
	Props        []source.Prop
	MessageID    string
}

// X-Mozilla-Status bits (Thunderbird msgFlags): read, replied, marked
// (starred), expunged (deleted, not yet compacted), forwarded; Status2 bit
// for an IMAP-deleted message.
const (
	mozRead      = 0x0001
	mozReplied   = 0x0002
	mozMarked    = 0x0004
	mozExpunged  = 0x0008
	mozForwarded = 0x1000
	moz2IMAPDel  = 0x00200000
)

// defaultTags are Thunderbird's built-in tag names, used for $label1..5
// when no sidecar/prefs.js names them.
var defaultTags = map[string]string{
	"$label1": "Important", "$label2": "Work", "$label3": "Personal", "$label4": "To Do", "$label5": "Later",
}

// consumed header names are folded into flags, labels and tags, and so are
// not repeated as Props.
var consumed = map[string]bool{
	"x-gmail-labels": true, "x-mozilla-status": true, "x-mozilla-status2": true,
	"x-mozilla-keys": true, "x-mozilla-draft-info": true,
}

// headerMetadata derives flags, labels, tags and props from h. tagInfo (from
// a sidecar, may be nil) names X-Mozilla-Keys keys.
func headerMetadata(h mail.Header, tagInfo map[string]source.TagStyle) headerMeta {
	var m headerMeta
	m.MessageID = firstID(h.Get("Message-ID"))

	// Gmail labels; the system labels Unread, Starred, Important, Spam,
	// Trash and Drafts also set flags.
	m.Labels = mail.ParseLabels(h.Get("X-Gmail-Labels"))
	for _, l := range m.Labels {
		switch {
		case strings.EqualFold(l, "Unread"):
			m.Flags |= source.Unread
		case strings.EqualFold(l, "Starred"):
			m.Flags |= source.Starred
		case strings.EqualFold(l, "Important"):
			m.Flags |= source.Important
		case strings.EqualFold(l, "Spam"):
			m.Flags |= source.Junk
		case strings.EqualFold(l, "Trash"):
			m.Flags |= source.Deleted
		case strings.EqualFold(l, "Drafts"), strings.EqualFold(l, "Draft"):
			m.Flags |= source.Draft
		}
	}

	// Thunderbird flags. Without X-Mozilla-Status nothing is known, and the
	// message counts as read.
	if v := strings.TrimSpace(h.Get("X-Mozilla-Status")); v != "" {
		if st, ok := parseHex(v); ok {
			m.Flags |= mozFlags(st)
		}
	}
	if st2, ok := parseHex(h.Get("X-Mozilla-Status2")); ok && st2&moz2IMAPDel != 0 {
		m.Flags |= source.Deleted
	}
	if h.Get("X-Mozilla-Draft-Info") != "" {
		m.Flags |= source.Draft
	}

	m.Flags, m.Tags = mozKeys(h.Get("X-Mozilla-Keys"), tagInfo, m.Flags)

	for _, f := range h {
		if len(m.Props) >= maxProps {
			break
		}
		if len(f.Name) < 2 || !strings.EqualFold(f.Name[:2], "x-") || consumed[strings.ToLower(f.Name)] {
			continue
		}
		v := f.Decoded()
		if v == "" {
			continue
		}
		if len(v) > maxPropValue {
			v = v[:maxPropValue]
			for !utf8.ValidString(v) {
				v = v[:len(v)-1]
			}
		}
		m.Props = append(m.Props, source.Prop{Key: f.Name, Value: v})
	}
	return m
}

func firstID(s string) string {
	if ids := mail.ParseMessageIDs(s); len(ids) > 0 {
		return ids[0]
	}
	return ""
}

// mozFlags maps X-Mozilla-Status bits to flags: unread when the read bit is
// clear.
func mozFlags(st uint64) source.Flags {
	var f source.Flags
	if st&mozRead == 0 {
		f |= source.Unread
	}
	if st&mozReplied != 0 {
		f |= source.Answered
	}
	if st&mozMarked != 0 {
		f |= source.Starred
	}
	if st&mozExpunged != 0 {
		f |= source.Deleted
	}
	if st&mozForwarded != 0 {
		f |= source.Forwarded
	}
	return f
}

func parseHex(s string) (uint64, bool) {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(strings.TrimPrefix(s, "0x"), "0X")
	if s == "" {
		return 0, false
	}
	v, err := strconv.ParseUint(s, 16, 32)
	return v, err == nil
}

// mozKeys turns an X-Mozilla-Keys value (space-separated keys) into tag
// display names. Keys naming IMAP state become flags, other "$" keywords and
// Thunderbird's NonJunk/Junk housekeeping are no tags.
func mozKeys(v string, tagInfo map[string]source.TagStyle, flags source.Flags) (source.Flags, []string) {
	var tags []string
	seen := map[string]bool{}
	for _, key := range strings.Fields(v) {
		lower := strings.ToLower(key)
		switch lower {
		case "$forwarded":
			flags |= source.Forwarded
			continue
		case "junk", "$junk":
			flags |= source.Junk
			continue
		case "nonjunk", "$notjunk", "$mdnsent":
			continue
		}
		name := key
		if ts, ok := tagInfo[key]; ok && ts.Name != "" {
			name = ts.Name
		} else if ts, ok := tagInfo[lower]; ok && ts.Name != "" {
			name = ts.Name
		} else if d, ok := defaultTags[lower]; ok {
			name = d
		} else if strings.HasPrefix(key, "$") {
			continue
		}
		if !seen[name] {
			seen[name] = true
			tags = append(tags, name)
		}
	}
	return flags, tags
}
