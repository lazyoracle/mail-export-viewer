package mail

import (
	"reflect"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// crlf converts LF line endings to CRLF so tests cover both.
func crlf(s string) string { return strings.ReplaceAll(s, "\n", "\r\n") }

func TestDecodeHeaderWords(t *testing.T) {
	tests := []struct {
		name, in, want string
	}{
		{"plain", "Hello world", "Hello world"},
		{"Q utf-8", "=?UTF-8?Q?Caf=C3=A9_au_lait?=", "Café au lait"},
		{"B utf-8", "=?utf-8?B?w6l0w6k=?=", "été"},
		{"B utf-8 umlaut", "=?UTF-8?B?SGVsbG8gd8O2cmxk?=", "Hello wörld"},
		{"multi-word adjacent", "=?UTF-8?Q?Hello?= =?UTF-8?Q?_World?=", "Hello World"},
		{"adjacent words no space", "=?utf-8?Q?a?= =?utf-8?Q?b?=", "ab"},
		{"mixed text and word", "Re: =?ISO-8859-1?Q?Caf=E9?= time", "Re: Café time"},
		{"iso-8859-1 B", "=?iso-8859-1?B?Q2Fm6Q==?=", "Café"},
		{"koi8-r", "=?KOI8-R?Q?=F0=D2=C9=D7=C5=D4?=", "Привет"},
		{"shift_jis", "=?Shift_JIS?B?k/qWew==?=", "日本"},
		{"gb2312", "=?GB2312?B?1tDOxA==?=", "中文"},
		{"gb2312 hello", "=?gb2312?B?xOO6ww==?=", "你好"},
		{"split multibyte across words", "=?UTF-8?B?4oI=?= =?UTF-8?B?rA==?=", "€"},
		{"lowercase q and lang", "=?utf-8*en?q?hi_there?=", "hi there"},
		{"unknown charset utf8 bytes", "=?x-bogus?Q?Caf=C3=A9?=", "Café"},
		{"unknown charset latin1 bytes", "=?x-bogus?Q?Caf=E9?=", "Café"},
		{"unknown-8bit", "=?unknown-8bit?Q?Caf=E9?=", "Café"},
		{"malformed word left as is", "=?UTF-8?B?***?= x", "=?UTF-8?B?***?= x"},
		{"garbage encoded", "=?utf-8?B?!!!?= tail", "=?utf-8?B?!!!?= tail"},
		{"raw utf-8 header", "Caf\xc3\xa9", "Café"},
		{"raw latin1 header", "Caf\xe9", "Café"},
		{"iso-2022-jp", "=?ISO-2022-JP?B?GyRCRnxLXDhsGyhC?=", "日本語"},
		{"latin1 mislabelled utf8", "=?iso-8859-1?Q?Caf=C3=A9?=", "Café"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := DecodeHeader(tt.in); got != tt.want {
				t.Errorf("DecodeHeader(%q) = %q, want %q", tt.in, got, tt.want)
			}
			// The same value read from a message header.
			h := ParseHeader([]byte("From: a@example.com\nSubject: " + tt.in + "\n\nbody\n"))
			if got := h.Decoded("Subject"); got != tt.want {
				t.Errorf("Decoded(Subject) = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestParseHeaderFields(t *testing.T) {
	raw := crlf("Subject: This is a\n  long subject\n\tline\nSubject: second\nTo: a@example.com\nTo: b@example.com\nX-Dup: 1\nX-Dup: 2\nX-Empty:\nX-Enc: =?UTF-8?Q?Caf=C3=A9?=\n\nbody")
	h := ParseHeader([]byte(raw))
	if got := h.Get("subject"); got != "This is a long subject line" {
		t.Errorf("Get(subject) = %q", got)
	}
	if got := h.Values("X-Dup"); !reflect.DeepEqual(got, []string{"1", "2"}) {
		t.Errorf("Values(X-Dup) = %q", got)
	}
	if got := h.Get("X-Empty"); got != "" {
		t.Errorf("Get(X-Empty) = %q", got)
	}
	if got := h.Get("X-Enc"); got != "=?UTF-8?Q?Caf=C3=A9?=" {
		t.Errorf("Get keeps encoded words: %q", got)
	}
	if got := h.Decoded("X-Enc"); got != "Café" {
		t.Errorf("Decoded(X-Enc) = %q", got)
	}
	if len(h) != 8 || h[0].Name != "Subject" || h[7].Name != "X-Enc" {
		t.Errorf("fields = %q", h)
	}
	if got := h[7].Decoded(); got != "Café" {
		t.Errorf("Field.Decoded = %q", got)
	}
}

func TestRaw8BitHeaderUsesDeclaredBodyCharset(t *testing.T) {
	h := ParseHeader([]byte("Subject: \xf0\xd2\xc9\xd7\xc5\xd4\nContent-Type: text/plain; charset=koi8-r\n\nx\n"))
	if got := h.Decoded("Subject"); got != "Привет" {
		t.Errorf("Subject = %q", got)
	}
	// Valid UTF-8 is never reinterpreted with the hint.
	h = ParseHeader([]byte("Subject: Caf\xc3\xa9\nContent-Type: text/plain; charset=koi8-r\n\nx"))
	if got := h.Decoded("Subject"); got != "Café" {
		t.Errorf("Subject = %q", got)
	}
}

func TestParseHeaderOddShapes(t *testing.T) {
	tests := []struct {
		name, raw string
		want      []Field
	}{
		{"empty", "", nil},
		{"no headers", "no headers here at all", nil},
		{"body only", "\r\n\r\njust a body", nil},
		{"stray mbox separator", "From someone@example.com Mon Jan  1 00:00:00 2024\nSubject: s\n\nb", []Field{{"Subject", "s"}}},
		{"junk line skipped", "From: a@example.com\nthis is not a header\nSubject: still here\n\nbody", []Field{{"From", "a@example.com"}, {"Subject", "still here"}}},
		{"whitespace-only line ends headers", "Subject: x\n \t\nHello body", []Field{{"Subject", "x"}}},
		{"whitespace line then field", "Subject: x\n \nTo: a@example.com\n\nbody", []Field{{"Subject", "x"}, {"To", "a@example.com"}}},
		{"whitespace line then continuation", "Subject: x\n \n y\n\nbody", []Field{{"Subject", "x y"}}},
		{"space before colon", "Subject : spaced\n\nb", []Field{{"Subject", "spaced"}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ParseHeader([]byte(tt.raw))
			if len(got) == 0 && len(tt.want) == 0 {
				return
			}
			if !reflect.DeepEqual([]Field(got), tt.want) {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestHugeFoldedHeaderIsLinearAndCapped(t *testing.T) {
	var b strings.Builder
	b.WriteString("Subject: start\n")
	line := " " + strings.Repeat("x", 60) + "\n"
	for b.Len() < 8<<20 {
		b.WriteString(line)
	}
	b.WriteString("From: a@example.com\n\nbody\n")
	start := time.Now()
	h := ParseHeader([]byte(b.String()))
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("ParseHeader took %v on 8MB of folded lines", d)
	}
	s := h.Get("Subject")
	if len(s) > maxFieldLen+100 {
		t.Fatalf("subject not capped: %d bytes", len(s))
	}
	if !strings.HasPrefix(s, "start x") {
		t.Fatalf("subject prefix = %q", s[:20])
	}
	if h.Get("From") != "a@example.com" {
		t.Fatalf("field after huge header lost: %q", h)
	}
}

func TestHeaderAlwaysValidUTF8(t *testing.T) {
	h := ParseHeader([]byte("From: \xff\xfe <a@example.com>\nSubject: \xc3\x28 bad\nX-\xff: v\n\nx"))
	for _, f := range h {
		if !utf8.ValidString(f.Name) || !utf8.ValidString(f.Value) || !utf8.ValidString(f.Decoded()) {
			t.Errorf("invalid UTF-8 in %q", f)
		}
	}
}

func TestDecodeCharset(t *testing.T) {
	tests := []struct {
		name, cs, in, want string
	}{
		{"latin1", "ISO-8859-1", "Caf\xe9", "Café"},
		{"windows-1252 quotes", "windows-1252", "\x93hi\x94", "“hi”"},
		{"unknown utf8", "x-nope", "Caf\xc3\xa9", "Café"},
		{"unknown latin1", "x-nope", "Caf\xe9 \x80", "Café €"},
		{"unknown-8bit", "unknown-8bit", "Caf\xe9", "Café"},
		{"none", "", "Caf\xe9", "Café"},
		{"us-ascii with utf8", "us-ascii", "Caf\xc3\xa9", "Café"},
		{"quoted", `"utf-8"`, "Caf\xc3\xa9", "Café"},
		{"gb2312", "gb2312", "\xc4\xe3\xba\xc3", "你好"},
		{"shift_jis", "shift_jis", "\x93\xfa\x96\x7b", "日本"},
		{"cp932 alias", "cp932", "\x93\xfa\x96\x7b", "日本"},
		{"koi8-r", "koi8-r", "\xf0\xd2\xc9\xd7\xc5\xd4", "Привет"},
		{"iso-2022-jp", "iso-2022-jp", "\x1b$BF|K\\8l\x1b(B", "日本語"},
		{"utf-8 mislabel latin1", "utf-8", "Caf\xe9", "Café"},
		{"utf-8 with a bad byte", "utf-8", "Caf\xc3\xa9 \xff", "Café �"},
		{"latin1 holding utf8", "iso-8859-1", "Caf\xc3\xa9 \xe2\x82\xac", "Café €"},
		{"replacement encoding falls back", "iso-2022-kr", "Caf\xe9", "Café"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := DecodeCharset([]byte(tt.in), tt.cs)
			if got != tt.want {
				t.Errorf("DecodeCharset(%q, %q) = %q, want %q", tt.in, tt.cs, got, tt.want)
			}
			if !utf8.ValidString(got) {
				t.Errorf("invalid UTF-8 %q", got)
			}
		})
	}
}

func TestParseAddressList(t *testing.T) {
	tests := []struct {
		name, in string
		want     []Address
	}{
		{"simple list", `bob@example.com, "Carol" <carol@example.com>`, []Address{{"", "bob@example.com"}, {"Carol", "carol@example.com"}}},
		{"quoted comma", `"Doe, Jane" <jane@example.com>`, []Address{{"Doe, Jane", "jane@example.com"}}},
		{"encoded name", "=?ISO-8859-1?Q?Andr=E9?= <andre@example.com>", []Address{{"André", "andre@example.com"}}},
		{"encoded utf8 name", "=?utf-8?Q?J=C3=BCrgen?= <j@example.com>", []Address{{"Jürgen", "j@example.com"}}},
		{"comment", "jane@example.com (Jane Doe)", []Address{{"Jane Doe", "jane@example.com"}}},
		{"group", "Team: a@example.com, B <b@example.com>;, c@example.com",
			[]Address{{"", "a@example.com"}, {"B", "b@example.com"}, {"", "c@example.com"}}},
		{"empty group", "undisclosed-recipients:;", nil},
		{"empty group then address", "undisclosed-recipients:;, foo@example.com", []Address{{"", "foo@example.com"}}},
		{"garbage mixed", "Good <good@example.com>, <<bad>>, plain@example.com",
			[]Address{{"Good", "good@example.com"}, {"", "plain@example.com"}}},
		{"unquoted dot name", "John Q. Public <jqp@example.com>", []Address{{"John Q. Public", "jqp@example.com"}}},
		{"raw utf8 name", "Jörg <j@example.com>", []Address{{"Jörg", "j@example.com"}}},
		{"no at sign", "MAILER-DAEMON", []Address{{"", "MAILER-DAEMON"}}},
		{"trailing comma", "a@example.com,", []Address{{"", "a@example.com"}}},
		{"unterminated angle", "Weird Name <a@example.com", []Address{{"Weird Name", "a@example.com"}}},
		{"empty", "  ", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ParseAddressList(tt.in); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("ParseAddressList(%q) = %+v, want %+v", tt.in, got, tt.want)
			}
		})
	}
}

func TestAddressString(t *testing.T) {
	tests := []struct {
		a    Address
		want string
	}{
		{Address{"Jane", "jane@example.com"}, "Jane <jane@example.com>"},
		{Address{"", "jane@example.com"}, "jane@example.com"},
		{Address{"Jane", ""}, "Jane"},
		{Address{}, ""},
	}
	for _, tt := range tests {
		if got := tt.a.String(); got != tt.want {
			t.Errorf("%+v.String() = %q, want %q", tt.a, got, tt.want)
		}
	}
	if got := FormatAddresses([]Address{{"A", "a@example.com"}, {}, {"", "b@example.com"}}); got != "A <a@example.com>, b@example.com" {
		t.Errorf("FormatAddresses = %q", got)
	}
}

func TestParseDate(t *testing.T) {
	utc := func(y int, mo time.Month, d, h, mi, s int) time.Time {
		return time.Date(y, mo, d, h, mi, s, 0, time.UTC)
	}
	tests := []struct {
		in   string
		want time.Time // zero: unparsable
	}{
		{"Tue, 2 Jan 2024 15:04:05 +0000", utc(2024, 1, 2, 15, 4, 5)},
		{"2 Jan 2024 15:04:05 -0000", utc(2024, 1, 2, 15, 4, 5)},
		{"Tue, 2 Jan 2024 15:04:05 +0000 (UTC)", utc(2024, 1, 2, 15, 4, 5)},
		{"Mon, 01 Jan 2024 10:00:00 +0100 (CET)", utc(2024, 1, 1, 9, 0, 0)},
		{"Tue, 02 Jan 2024 15:04:05 GMT", utc(2024, 1, 2, 15, 4, 5)},
		{"Tue Jan  2 15:04:05 2024", utc(2024, 1, 2, 15, 4, 5)},
		{"2024-01-02T15:04:05Z", utc(2024, 1, 2, 15, 4, 5)},
		{"2024-01-02 15:04:05", utc(2024, 1, 2, 15, 4, 5)},
		{"Mon, 01 Jan 2024 10:00 +0000", utc(2024, 1, 1, 10, 0, 0)},
		{"Mon, 01 Jan 2024 10:00:00 GMT+0530", utc(2024, 1, 1, 4, 30, 0)},
		{"Mon, 01 Jan 2024 10:00:00 UTC+05:30", utc(2024, 1, 1, 4, 30, 0)},
		{"Mon, 01 Jan 2024 10:00:00 UT", utc(2024, 1, 1, 10, 0, 0)},
		{"Fri, 01 Jan 2024 10:00:00 +0000", utc(2024, 1, 1, 10, 0, 0)},
		{"Mon, 01 Jan 2024 10:00:00 +0000 (unterminated", utc(2024, 1, 1, 10, 0, 0)},
		{"Mon, 01 Jan 2024 10:00:00 +0000.", utc(2024, 1, 1, 10, 0, 0)},
		{"Mar 5 10:00:00 2024", utc(2024, 3, 5, 10, 0, 0)},
		{"Tue Jan 2 15:04:05 2024", utc(2024, 1, 2, 15, 4, 5)},
		{"Mon, 2 Jan 06 15:04:05 -0700", utc(2006, 1, 2, 22, 4, 5)},
		{"garbage", time.Time{}},
		{"yesterday-ish", time.Time{}},
		{"", time.Time{}},
		{"Mon, 1 Jan 0001 00:00:00 +0000", time.Time{}},
		{"Mon, 1 Jan 2999 00:00:00 +0000", time.Time{}},
	}
	for _, tt := range tests {
		got := ParseDate(tt.in)
		if tt.want.IsZero() {
			if !got.IsZero() {
				t.Errorf("ParseDate(%q) = %v, want zero", tt.in, got)
			}
			continue
		}
		if !got.Equal(tt.want) {
			t.Errorf("ParseDate(%q) = %v, want %v", tt.in, got, tt.want)
		}
	}
}

func TestObsoleteZonesIndependentOfHostTZ(t *testing.T) {
	old := time.Local
	defer func() { time.Local = old }()
	// Host zones whose abbreviations collide with RFC 5322 names.
	for _, loc := range []*time.Location{time.FixedZone("EST", 9*3600), time.FixedZone("PST", -1*3600), time.UTC} {
		time.Local = loc
		zones := map[string]int{"EST": -5, "EDT": -4, "CST": -6, "CDT": -5, "MST": -7, "MDT": -6, "PST": -8, "PDT": -7, "GMT": 0, "UT": 0}
		for z, off := range zones {
			for _, tail := range []string{"", " (comment)"} {
				got := ParseDate("Mon, 01 Jan 2024 10:00:00 " + z + tail)
				want := time.Date(2024, 1, 1, 10-off, 0, 0, 0, time.UTC)
				if !got.Equal(want) {
					t.Errorf("zone %s tail %q local %v: got %v want %v", z, tail, loc, got, want)
				}
			}
		}
	}
}

func TestParseMessageIDs(t *testing.T) {
	tests := []struct {
		in   string
		want []string
	}{
		{"<abc@example.com>", []string{"abc@example.com"}},
		{"<parent@example.com> (Joe's message)", []string{"parent@example.com"}},
		{"<r1@example.com>\n <r2@example.com> <r3@example.com>", []string{"r1@example.com", "r2@example.com", "r3@example.com"}},
		{"bare@example.com other@example.com", []string{"bare@example.com", "other@example.com"}},
		{"", nil},
	}
	for _, tt := range tests {
		if got := ParseMessageIDs(tt.in); !reflect.DeepEqual(got, tt.want) {
			t.Errorf("ParseMessageIDs(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestParseLabels(t *testing.T) {
	tests := []struct {
		in   string
		want []string
	}{
		{`Inbox,Important,"Work, Projects",Opened`, []string{"Inbox", "Important", "Work, Projects", "Opened"}},
		{`Inbox, Important ,"Work",,Sent`, []string{"Inbox", "Important", "Work", "Sent"}},
		{`Inbox,"Work, stuff",Important, "Plain"`, []string{"Inbox", "Work, stuff", "Important", "Plain"}},
		{`Inbox, "Work, Projects" ,Work/Projects,=?UTF-8?Q?Caf=C3=A9=2C_bar?=, Inbox,=?UTF-8?B?5pel5pys?=,Category Updates,,Inbox`,
			[]string{"Inbox", "Work, Projects", "Work/Projects", "Café, bar", "日本", "Category Updates"}},
		{"a\x01b", []string{"a b"}},
		{"", nil},
	}
	for _, tt := range tests {
		if got := ParseLabels(tt.in); !reflect.DeepEqual(got, tt.want) {
			t.Errorf("ParseLabels(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

// Whole-header encodings (one or more encoded words and nothing else) are
// decoded first and then split.
var wholeHeaderEncoded = []struct {
	header string
	want   []string
}{
	{`=?UTF-8?Q?Inbox,Caf=C3=A9,Work?=`, []string{"Inbox", "Café", "Work"}},
	{`=?UTF-8?B?SW5ib3gsQ2Fmw6ksV29yaw==?=`, []string{"Inbox", "Café", "Work"}},
	{"=?UTF-8?Q?Inbox,Caf=C3=A9?= =?UTF-8?Q?,Work_Stuff?=", []string{"Inbox", "Café", "Work Stuff"}},
	{`=?UTF-8?Q?Caf=C3=A9=2C_bar?=`, []string{"Café", "bar"}},
	{`=?UTF-8?Q?Inbox,"Work,_Projects",Sent?=`, []string{"Inbox", "Work, Projects", "Sent"}},
}

func TestParseLabelsWholeHeaderEncoded(t *testing.T) {
	for _, c := range wholeHeaderEncoded {
		if got := ParseLabels(c.header); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%q: Labels = %q, want %q", c.header, got, c.want)
		}
		// Parity with decode-then-split (the earlier-tool cache format).
		if old := splitLabels(DecodeHeader(c.header)); !reflect.DeepEqual(ParseLabels(c.header), old) {
			t.Errorf("%q: new %q, old %q", c.header, ParseLabels(c.header), old)
		}
	}
}

// Per-label encodings: commas inside an encoded word never split it.
func TestParseLabelsPerLabelEncoded(t *testing.T) {
	for h, want := range map[string][]string{
		`Inbox,=?UTF-8?Q?Caf=C3=A9=2C_bar?=,Work`:      {"Inbox", "Café, bar", "Work"},
		`Inbox,=?UTF-8?Q?a,b?=`:                        {"Inbox", "a,b"},
		`=?UTF-8?B?5pel5pys?=,Sent`:                    {"日本", "Sent"},
		`"Work, Projects",=?UTF-8?Q?Caf=C3=A9?=,Inbox`: {"Work, Projects", "Café", "Inbox"},
	} {
		if got := ParseLabels(h); !reflect.DeepEqual(got, want) {
			t.Errorf("%q: Labels = %q, want %q", h, got, want)
		}
	}
}
