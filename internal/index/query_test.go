package index

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/lazyoracle/mail-export-viewer/internal/source"
)

func tp(y int, m time.Month, d int) *time.Time {
	t := time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
	return &t
}

func TestParseQuery(t *testing.T) {
	cases := []struct {
		in   string
		want Query
	}{
		{"", Query{}},
		{"   ", Query{}},
		{"hello world", Query{Terms: []string{"hello", "world"}, prefixLast: true}},
		{"hello world ", Query{Terms: []string{"hello", "world"}}},
		{`"exact phrase" foo`, Query{Phrases: []string{"exact phrase"}, Terms: []string{"foo"}, prefixLast: true}},
		{`"unterminated phrase`, Query{Phrases: []string{"unterminated phrase"}}},
		{`foo -bar -"baz qux"`, Query{Terms: []string{"foo"}, Not: []string{"bar", "baz qux"}}},
		{"-foo", Query{Not: []string{"foo"}}},
		{"from:alice to:bob cc:carol subject:report", Query{From: []string{"alice"}, To: []string{"bob"},
			Cc: []string{"carol"}, Subject: []string{"report"}}},
		{"FROM:Alice", Query{From: []string{"Alice"}}},
		{`subject:"quarterly report" label:"My Label"`, Query{Subject: []string{"quarterly report"}, Label: []string{"My Label"}}},
		{"mailbox:Inbox folder:Archive label:work", Query{Folder: []string{"Inbox", "Archive"}, Label: []string{"work"}}},
		{`tag:"To Do" category:Red`, Query{Tag: []string{"To Do"}, Category: []string{"Red"}}},
		{"has:attachment", Query{Is: source.HasAttachment}},
		{"-has:attachment", Query{NotIs: source.HasAttachment}},
		{"has:pizza", Query{Terms: []string{"has:pizza"}, prefixLast: true}},
		{"is:unread is:flagged", Query{Is: source.Unread | source.Starred}},
		{"is:read", Query{NotIs: source.Unread}},
		{"-is:read", Query{Is: source.Unread}},
		{"-is:starred is:answered is:forwarded", Query{NotIs: source.Starred, Is: source.Answered | source.Forwarded}},
		{"is:draft is:deleted is:junk is:important", Query{Is: source.Draft | source.Deleted | source.Junk | source.Important}},
		{"IS:Unread", Query{Is: source.Unread}},
		{"is:pizza", Query{Terms: []string{"is:pizza"}, prefixLast: true}},
		{"filename:report.pdf", Query{Filename: []string{"report.pdf"}}},
		{"before:2023", Query{Before: tp(2023, 1, 1)}},
		{"after:2023-05", Query{After: tp(2023, 5, 1)}},
		{"after:2023-05-17 before:2024-02-29", Query{After: tp(2023, 5, 17), Before: tp(2024, 2, 29)}},
		{"after:2023/05/17 before:2024/02", Query{After: tp(2023, 5, 17), Before: tp(2024, 2, 1)}},
		{"-before:2023", Query{After: tp(2023, 1, 1)}},
		{"before:2023-13", Query{Terms: []string{"before:2023-13"}, prefixLast: true}},
		{"before:yesterday x", Query{Terms: []string{"before:yesterday", "x"}, prefixLast: true}},
		{"http://example.com", Query{Terms: []string{"http://example.com"}, prefixLast: true}},
		{"from: foo", Query{Terms: []string{"foo"}, prefixLast: true}},
		{"from:", Query{}},
		{"-from:bob", Query{NotFrom: []string{"bob"}}},
		{"-to:a -subject:b -mailbox:c -label:d -filename:e -tag:f -category:g -cc:h", Query{NotTo: []string{"a"},
			NotSubject: []string{"b"}, NotFolder: []string{"c"}, NotLabel: []string{"d"}, NotFilename: []string{"e"},
			NotTag: []string{"f"}, NotCategory: []string{"g"}, NotCc: []string{"h"}}},
		{`-label:"My Label" x`, Query{NotLabel: []string{"My Label"}, Terms: []string{"x"}, prefixLast: true}},
		{"-", Query{Terms: []string{"-"}, prefixLast: true}},
		{`"`, Query{}},
		{`""`, Query{}},
		{"café 会议", Query{Terms: []string{"café", "会议"}, prefixLast: true}},
		{`foo "bar"`, Query{Terms: []string{"foo"}, Phrases: []string{"bar"}}},
	}
	for _, c := range cases {
		got := ParseQuery(c.in)
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("ParseQuery(%q) =\n%#v\nwant\n%#v", c.in, got, c.want)
		}
	}
}

func TestParseQueryHugeInput(t *testing.T) {
	q := ParseQuery(strings.Repeat("word ", 100000))
	if n := len(q.Terms); n == 0 || n > maxQueryTerms {
		t.Fatalf("terms = %d, want 1..%d", n, maxQueryTerms)
	}
}

func TestFTSQuote(t *testing.T) {
	for in, want := range map[string]string{
		`foo`:       `"foo"`,
		`a"b`:       `"a""b"`,
		`AND`:       `"AND"`,
		"x\x00y":    `"xy"`,
		`col:value`: `"col:value"`,
	} {
		if got := ftsQuote(in); got != want {
			t.Errorf("ftsQuote(%q) = %s, want %s", in, got, want)
		}
	}
}

// Label, tag and category names may hold spaces, commas, slashes, unicode
// and (rarely) a double quote. Inside a quoted value \" is a literal quote
// and \\ a literal backslash; any other backslash is kept as is.
func TestParseQueryQuotedValues(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{`label:"Work, Projects"`, []string{"Work, Projects"}},
		{`label:"Work/Projects/2024 Q1"`, []string{"Work/Projects/2024 Q1"}},
		{`label:"Café 日本"`, []string{"Café 日本"}},
		{`label:Work/Projects`, []string{"Work/Projects"}},
		{`label:"Category Updates" label:Inbox`, []string{"Category Updates", "Inbox"}},
		{`label:"say \"hi\" now"`, []string{`say "hi" now`}},
		{`label:"back\\slash"`, []string{`back\slash`}},
		{`label:"C:\temp"`, []string{`C:\temp`}},
		{`label:"ends with \\"`, []string{`ends with \`}},
		{`label:"unterminated \"rest`, []string{`unterminated "rest`}},
		{`label:a"b`, []string{`a"b`}}, // unquoted: literal
	}
	for _, c := range cases {
		q := ParseQuery(c.in)
		if !reflect.DeepEqual(q.Label, c.want) {
			t.Errorf("ParseQuery(%q).Label = %q, want %q", c.in, q.Label, c.want)
		}
	}
	q := ParseQuery(`-label:"Say \"x\"" "a \"b\" c"`)
	if !reflect.DeepEqual(q.NotLabel, []string{`Say "x"`}) || !reflect.DeepEqual(q.Phrases, []string{`a "b" c`}) {
		t.Errorf("negated/phrase escapes: %+v", q)
	}
}
