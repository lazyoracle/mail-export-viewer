package mail

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

var envDate = time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)

// rawOf builds a message from header lines and a body (LF endings).
func rawOf(body string, hdrs ...string) []byte {
	return []byte(strings.Join(hdrs, "\n") + "\n\n" + body)
}

// mixed builds a multipart/mixed message with boundary B from part texts.
func mixed(parts ...string) []byte {
	return rawOf("--B\n"+strings.Join(parts, "--B\n")+"--B--\n", "From: a@example.com", "Content-Type: multipart/mixed; boundary=B")
}

const textPart = "Content-Type: text/plain\n\nbody\n"

func TestParseEnvelope(t *testing.T) {
	raw := crlf(`Message-ID: <abc@example.com>
In-Reply-To: <parent@example.com> (Joe's message)
References: <root@example.com>
	<parent@example.com>
From: "Doe, Jane" <jane@example.com>
To: bob@example.com, "Carol" <carol@example.com>
Cc: =?UTF-8?Q?J=C3=B6rg?= <joerg@example.com>
Bcc: hidden@example.com
Subject: Hi
Date: Tue, 2 Jan 2024 15:04:05 +0100
X-Gmail-Labels: Inbox,Important,"Work, Projects",Opened

hello
`)
	m := Parse([]byte(raw), envDate)
	want := Envelope{
		MessageID:  "abc@example.com",
		InReplyTo:  "parent@example.com",
		References: []string{"root@example.com", "parent@example.com"},
		Subject:    "Hi",
		From:       Address{"Doe, Jane", "jane@example.com"},
		To:         []Address{{"", "bob@example.com"}, {"Carol", "carol@example.com"}},
		Cc:         []Address{{"Jörg", "joerg@example.com"}},
		Bcc:        []Address{{"", "hidden@example.com"}},
		Date:       time.Date(2024, 1, 2, 14, 4, 5, 0, time.UTC),
	}
	got := m.Envelope
	if !got.Date.Equal(want.Date) {
		t.Errorf("Date = %v", got.Date)
	}
	got.Date = want.Date
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Envelope = %+v\nwant %+v", got, want)
	}
	if m.Text != "hello\n" {
		t.Errorf("Text = %q", m.Text)
	}
	if len(m.Header) != 10 || m.Header[0] != (Field{"Message-ID", "<abc@example.com>"}) {
		t.Errorf("Header = %q", m.Header)
	}
	if m.Header[5].Decoded() != "Jörg <joerg@example.com>" {
		t.Errorf("decoded Cc = %q", m.Header[5].Decoded())
	}
	if got := ParseLabels(m.Header.Get("X-Gmail-Labels")); !reflect.DeepEqual(got, []string{"Inbox", "Important", "Work, Projects", "Opened"}) {
		t.Errorf("labels = %q", got)
	}
}

func TestSenderFallbackAndMultipleTo(t *testing.T) {
	m := Parse([]byte("Sender: s@example.com\nTo: a@example.com\nTo: b@example.com\n\nx"), envDate)
	if m.From.Email != "s@example.com" {
		t.Errorf("From = %+v", m.From)
	}
	if len(m.To) != 2 || m.To[1].Email != "b@example.com" {
		t.Errorf("To = %+v", m.To)
	}
}

func TestMissingHeaders(t *testing.T) {
	tests := []struct{ in, text string }{
		{"", ""},
		{"\n", ""},
		{"\r\n\r\njust a body", "just a body"},
		{"\n\nbody only", "body only"},
		{"no headers here at all", "no headers here at all"},
		{"Subject: only\n", ""},
	}
	for _, tt := range tests {
		m := Parse([]byte(tt.in), envDate)
		if m == nil {
			t.Fatalf("Parse(%q) = nil", tt.in)
		}
		if !m.Date.Equal(envDate) {
			t.Errorf("Parse(%q).Date = %v, want fallback", tt.in, m.Date)
		}
		if strings.TrimSpace(m.Text) != tt.text || m.HTML != "" || len(m.Attachments) != 0 {
			t.Errorf("Parse(%q) = %+v", tt.in, m)
		}
	}
	if m := Parse(nil, time.Time{}); m == nil || !m.Date.IsZero() {
		t.Errorf("nil input: %+v", m)
	}
	if m := Parse([]byte("Subject: only\n"), envDate); m.Subject != "only" {
		t.Errorf("Subject = %q", m.Subject)
	}
}

func TestDateFallback(t *testing.T) {
	for in, want := range map[string]time.Time{
		"Date: Tue, 2 Jan 2024 15:04:05 +0000\n\nx": time.Date(2024, 1, 2, 15, 4, 5, 0, time.UTC),
		"Date: garbage\n\nx":                         envDate,
		"Subject: none\n\nx":                         envDate,
	} {
		if got := Parse([]byte(in), envDate).Date; !got.Equal(want) {
			t.Errorf("%q: Date = %v, want %v", in, got, want)
		}
	}
}

func TestMultipartAlternative(t *testing.T) {
	raw := crlf(`From: a@example.com
Content-Type: multipart/alternative; boundary="b1"

preamble
--b1
Content-Type: text/plain; charset=utf-8

plain text
--b1
Content-Type: text/html; charset=utf-8

<p>html text</p>
--b1--
epilogue
`)
	m := Parse([]byte(raw), envDate)
	if m.Text != "plain text" || m.HTML != "<p>html text</p>" || len(m.Attachments) != 0 {
		t.Errorf("Text %q HTML %q Attachments %+v", m.Text, m.HTML, m.Attachments)
	}
}

const nestedMsg = `From: a@example.com
Subject: nested
Content-Type: multipart/mixed; boundary="mixed"

--mixed
Content-Type: multipart/related; boundary="rel"

--rel
Content-Type: multipart/alternative; boundary="alt"

--alt
Content-Type: text/plain; charset=iso-8859-1
Content-Transfer-Encoding: quoted-printable

Caf=E9 soft=
 break
--alt
Content-Type: text/html; charset=utf-8
Content-Transfer-Encoding: base64

PHA+SGVsbG8gPGltZyBzcmM9ImNpZDppbWcxQGV4YW1wbGUiPjwvcD4=
--alt--

--rel
Content-Type: image/png
Content-Transfer-Encoding: base64
Content-ID: <img1@example>

iVBORw0KGgo=
--rel--

--mixed
Content-Type: application/pdf; name="report.pdf"
Content-Disposition: attachment; filename="report.pdf"
Content-Transfer-Encoding: base64

JVBERi0xLjQKJcOkw7zDtsOfCg==
--mixed
Content-Type: application/octet-stream
Content-Disposition: attachment; filename*=UTF-8''%e2%82%ac.txt

euro
--mixed
Content-Type: text/plain; name="only-name.txt"

named
--mixed
Content-Type: message/rfc822

From: inner@example.com
Subject: Inner subject

inner body
--mixed--
`

func TestNestedMultipart(t *testing.T) {
	for _, variant := range []string{nestedMsg, crlf(nestedMsg)} {
		raw := []byte(variant)
		m := Parse(raw, envDate)
		if m.Text != "Café soft break" {
			t.Errorf("Text = %q", m.Text)
		}
		if m.HTML != `<p>Hello <img src="cid:img1@example"></p>` {
			t.Errorf("HTML = %q", m.HTML)
		}
		want := []Attachment{
			{Index: 0, Filename: "attachment-0.png", ContentType: "image/png", ContentID: "img1@example", Size: 8, Inline: true, NameGenerated: true},
			{Index: 1, Filename: "report.pdf", ContentType: "application/pdf", Size: 19},
			{Index: 2, Filename: "€.txt", ContentType: "application/octet-stream", Size: 4},
			{Index: 3, Filename: "only-name.txt", ContentType: "text/plain", Size: 5},
			{Index: 4, Filename: "Inner subject.eml", ContentType: "message/rfc822", NameGenerated: true},
		}
		if len(m.Attachments) != len(want) {
			t.Fatalf("Attachments = %+v", m.Attachments)
		}
		for i, w := range want {
			got := m.Attachments[i]
			if i == 4 {
				w.Size = got.Size // rfc822 size depends on line endings
				if got.Size < 40 {
					t.Errorf("rfc822 size = %d", got.Size)
				}
			}
			if got != w {
				t.Errorf("Attachments[%d] = %+v, want %+v", i, got, w)
			}
		}
		// AttachmentData uses the same traversal and decodes.
		for i, w := range m.Attachments {
			a, data, err := AttachmentData(raw, i)
			if err != nil {
				t.Fatalf("AttachmentData(%d): %v", i, err)
			}
			if a != w || int64(len(data)) != w.Size {
				t.Errorf("AttachmentData(%d) = %+v len %d, want %+v", i, a, len(data), w)
			}
		}
		if _, pdf, _ := AttachmentData(raw, 1); !bytes.HasPrefix(pdf, []byte("%PDF-1.4")) {
			t.Errorf("pdf = %q", pdf)
		}
		_, eml, _ := AttachmentData(raw, 4)
		if inner := Parse(eml, envDate); inner.Subject != "Inner subject" || inner.From.Email != "inner@example.com" {
			t.Errorf("inner = %+v", inner)
		}
		a, img, err := CIDData(raw, "img1@example")
		if err != nil || a.Index != 0 || !bytes.HasPrefix(img, []byte("\x89PNG")) {
			t.Errorf("CIDData = %+v %q %v", a, img, err)
		}
		for _, cid := range []string{"<img1@example>", "cid:img1@example", "IMG1@example", "img1%40example"} {
			if _, _, err := CIDData(raw, cid); err != nil {
				t.Errorf("CIDData(%q): %v", cid, err)
			}
			if a, ok := FindCID(m.Attachments, cid); !ok || a.Index != 0 {
				t.Errorf("FindCID(%q) = %+v %v", cid, a, ok)
			}
		}
		if _, _, err := CIDData(raw, "nope"); err != ErrNotFound {
			t.Errorf("CIDData(nope) err = %v", err)
		}
		for _, i := range []int{5, -1} {
			if _, _, err := AttachmentData(raw, i); err != ErrNotFound {
				t.Errorf("AttachmentData(%d) err = %v", i, err)
			}
		}
		s := m.Summary()
		if !s.HasAttachment || !reflect.DeepEqual(s.AttachmentNames, []string{"report.pdf", "€.txt", "only-name.txt"}) {
			t.Errorf("Summary attachments = %v %q", s.HasAttachment, s.AttachmentNames)
		}
	}
}

func TestAttachmentsDataMatchesAttachmentData(t *testing.T) {
	raw := []byte("Content-Type: multipart/mixed; boundary=B\n\n--B\nContent-Type: text/plain\n\nhi\n" +
		"--B\nContent-Type: image/png\nContent-ID: <a>\nContent-Transfer-Encoding: base64\n\naGVsbG8=\n" +
		"--B\nContent-Type: application/pdf\nContent-Disposition: attachment; filename=x.pdf\n\npdf\n" +
		"--B\nContent-Type: image/gif\nContent-ID: <b>\n\nGIF89a\n--B--\n")
	got, err := AttachmentsData(raw, map[int]bool{0: true, 2: true, 9: true})
	if err != nil || len(got) != 2 {
		t.Fatalf("%v %v", got, err)
	}
	for _, i := range []int{0, 2} {
		if _, want, _ := AttachmentData(raw, i); string(got[i]) != string(want) {
			t.Errorf("%d: %q vs %q", i, got[i], want)
		}
	}
	if got, err := AttachmentsData(raw, nil); err != nil || len(got) != 0 {
		t.Errorf("empty want: %v %v", got, err)
	}
}

func TestFilenames(t *testing.T) {
	tests := []struct{ name, hdrs, want string }{
		{"rfc2231 utf8", `Content-Disposition: attachment; filename*=UTF-8''%e2%82%ac.txt`, "€.txt"},
		{"rfc2231 latin1 lang", `Content-Disposition: attachment; filename*=iso-8859-1'en'caf%E9.txt`, "café.txt"},
		{"rfc2231 latin1 no lang", "Content-Disposition: attachment; filename*=iso-8859-1''caf%E9.pdf", "café.pdf"},
		{"rfc2231 continuation", `Content-Disposition: attachment; filename*0*=UTF-8''long%20; filename*1="name.txt"`, "long name.txt"},
		{"latin1 continuation", "Content-Disposition: attachment; filename*0*=iso-8859-1''caf%E9;\n filename*1=\".pdf\"", "café.pdf"},
		{"utf8 encoded continuation", "Content-Disposition: attachment; filename*0*=utf-8''a%20b;\n filename*1*=%20%C3%A9", "a b é"},
		{"folded continuation", "Content-Disposition: attachment;\n filename*0*=utf-8''long%20na;\n filename*1*=me%20%C3%A9.bin", "long name é.bin"},
		{"gb2312 ext", "Content-Disposition: attachment; filename*=gb2312''%C4%E3%BA%C3.txt", "你好.txt"},
		{"rfc2047 in quotes", `Content-Disposition: attachment; filename="=?UTF-8?Q?r=C3=A9sum=C3=A9.pdf?="`, "résumé.pdf"},
		{"rfc2047 in name", `Content-Type: application/pdf; name="=?utf-8?Q?caf=C3=A9.pdf?="`, "café.pdf"},
		{"name* in content-type", "Content-Type: application/pdf; name*=iso-8859-1''caf%E9.pdf", "café.pdf"},
		{"path traversal", `Content-Disposition: attachment; filename="../../etc/passwd"`, "passwd"},
		{"windows path", `Content-Disposition: attachment; filename=C:\dir\file.txt`, "file.txt"},
		{"unquoted spaces", `Content-Disposition: attachment; filename=unquoted name.txt`, "unquoted name.txt"},
		{"unquoted spaces then param", "Content-Disposition: attachment; filename=my file.pdf; size=3", "my file.pdf"},
		{"quoted with semicolon", `Content-Disposition: attachment; filename="a;b.pdf"`, "a;b.pdf"},
		{"escaped quote", `Content-Disposition: attachment; filename="quoted \"x\".txt"`, `quoted "x".txt`},
		{"raw latin1", "Content-Disposition: attachment; filename=\"caf\xe9.txt\"", "café.txt"},
		{"ext wins over plain", "Content-Disposition: attachment; filename=plain.pdf; filename*=utf-8''fancy.pdf", "fancy.pdf"},
		{"unknown charset", "Content-Disposition: attachment; filename*=x-bogus''caf%E9.pdf", "café.pdf"},
		{"control chars dropped", "Content-Disposition: attachment; filename=\"a\x01b.txt\"", "ab.txt"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ct := "Content-Type: application/octet-stream"
			h := tt.hdrs
			if strings.HasPrefix(h, "Content-Type") {
				ct, h = h, "X-None: 1"
			}
			m := Parse(mixed(textPart, ct+"\n"+h+"\n\ndata\n"), envDate)
			if len(m.Attachments) != 1 || m.Attachments[0].Filename != tt.want || m.Attachments[0].NameGenerated {
				t.Errorf("Attachments = %+v, want filename %q", m.Attachments, tt.want)
			}
		})
	}
}

func TestBodyCharsetsAndEncodings(t *testing.T) {
	tests := []struct {
		name, ct, cte, body, want string
	}{
		{"latin1", "text/plain; charset=ISO-8859-1", "8bit", "Caf\xe9", "Café"},
		{"latin1 qp", "text/plain; charset=ISO-8859-1", "quoted-printable", "caf=E9 =\nsplit", "café split"},
		{"windows-1252 quotes", "text/plain; charset=windows-1252", "8bit", "\x93hi\x94", "“hi”"},
		{"gb2312", "text/plain; charset=gb2312", "8bit", "\xc4\xe3\xba\xc3", "你好"},
		{"no charset 8bit", "text/plain", "8bit", "Caf\xe9", "Café"},
		{"qp", "text/plain; charset=utf-8", "quoted-printable", "a=3Db=\nc =C3=A9 =ZZ", "a=bc é =ZZ"},
		{"base64 with garbage", "text/plain; charset=utf-8", "base64", "aGVs\nbG8g\n d29y bGQ=\n!!", "hello world"},
		{"base64 unpadded", "text/plain; charset=utf-8", "Base64", "aGk", "hi"},
		{"base64 trailing junk", "text/plain; charset=utf-8", "base64", "aGVsbG8=!!!", "hello"},
		{"utf-8 base64", "text/plain; charset=utf-8", "base64", base64.StdEncoding.EncodeToString([]byte("héllo")), "héllo"},
		{"uppercase cte", "text/plain; charset=utf-8", "QUOTED-PRINTABLE", "=C3=A9", "é"},
		{"unknown cte passthrough", "text/plain; charset=utf-8", "x-weird", "plain", "plain"},
		{"iso-2022-jp 7bit", "text/plain; charset=iso-2022-jp", "7bit", "\x1b$BF|K\\8l\x1b(B", "日本語"},
		{"html meta charset", "text/html", "8bit", `<meta charset="koi8-r"><p>` + "\xf0\xd2\xc9\xd7\xc5\xd4", `<meta charset="koi8-r"><p>Привет`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := Parse(rawOf(tt.body, "Content-Type: "+tt.ct, "Content-Transfer-Encoding: "+tt.cte), envDate)
			got := m.Text
			if strings.HasPrefix(tt.ct, "text/html") {
				got = m.HTML
			}
			if got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
			if !utf8.ValidString(got) {
				t.Errorf("invalid UTF-8")
			}
		})
	}
}

func TestBrokenMultipart(t *testing.T) {
	tests := []struct{ name, raw, wantText string }{
		{"no boundary param", "Content-Type: multipart/mixed\n\nhello", "hello"},
		{"boundary absent", "Content-Type: multipart/mixed; boundary=qq\n\nhello", "hello"},
		{"wrong boundary", "Content-Type: multipart/mixed; boundary=B\n\njust some text, wrong boundary\n--other\nx\n", "just some text, wrong boundary\n--other\nx\n"},
		{"prefix boundaries", "Content-Type: multipart/mixed; boundary=b\n\n--b\nContent-Type: multipart/alternative; boundary=b_1\n\n--b_1\nContent-Type: text/plain\n\ninner\n--b_1--\n--b--\n", "inner"},
		{"transport padding", "Content-Type: multipart/mixed; boundary=b\n\n--b  \nContent-Type: text/plain\n\npad\n--b-- \n", "pad"},
		{"bad content-type", "Content-Type: ;;;garbage\n\nhello", "hello"},
		{"text without subtype", "Content-Type: text\n\nhello", "hello"},
		{"part without headers", "Content-Type: multipart/mixed; boundary=b\n\n--b\n\nbare\n--b--\n", "bare"},
		{"two text parts", "Content-Type: multipart/mixed; boundary=b\n\n--b\n\none\n--b\n\ntwo\n--b--\n", "one\ntwo"},
		{"truncated", "Content-Type: multipart/mixed; boundary=B\n\n--B\nContent-Type: text/plain\n\npartial", "partial"},
		{"quoted boundary with space", "Content-Type: multipart/mixed; boundary=\"a b\"\n\n--a b\nContent-Type: text/plain\n\nhi\n--a b--\n", "hi"},
		{"boundary then param", "Content-Type: multipart/mixed; boundary=ab; charset=x\n\n--ab\nContent-Type: text/plain\n\nhi\n--ab--\n", "hi"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if m := Parse([]byte(tt.raw), envDate); m.Text != tt.wantText {
				t.Errorf("Text = %q, want %q", m.Text, tt.wantText)
			}
		})
	}
}

func TestTruncatedMultipartAttachment(t *testing.T) {
	raw := "Content-Type: multipart/mixed; boundary=zz\n\n--zz\nContent-Type: text/plain\n\nfirst\n--zz\nContent-Type: application/pdf\nContent-Disposition: attachment; filename=a.pdf\nContent-Transfer-Encoding: base64\n\nJVBERi0x"
	m := Parse([]byte(raw), envDate)
	if m.Text != "first" || len(m.Attachments) != 1 || m.Attachments[0].Filename != "a.pdf" || m.Attachments[0].Size != 6 {
		t.Errorf("Text %q Attachments %+v", m.Text, m.Attachments)
	}
}

func TestBoundaryWithLineBreakRejected(t *testing.T) {
	for _, ct := range []string{
		"multipart/mixed; boundary*=utf-8''a%0Ab",
		"multipart/mixed; boundary*=utf-8''a%0D%0Ab",
		"multipart/mixed; boundary*=utf-8''a%0Db",
	} {
		body := "--a\nb\nContent-Type: application/pdf\nContent-Disposition: attachment; filename=x.pdf\n\nx\n--a\rb\nContent-Type: text/plain\n\nhi\n--a\nb--\n"
		m := Parse(rawOf(body, "From: a@example.com", "Content-Type: "+ct), envDate)
		if len(m.Attachments) != 0 {
			t.Errorf("%q: boundary with CR/LF was used: %+v", ct, m.Attachments)
		}
		if !strings.Contains(m.Text, "filename=x.pdf") {
			t.Errorf("%q: body not shown as text: %q", ct, m.Text)
		}
	}
}

func TestDepthLimit(t *testing.T) {
	const n = 40
	var level func(i int) string
	level = func(i int) string {
		s := fmt.Sprintf("--B%d\nContent-Type: text/plain\n\nlvl%d\n", i, i)
		if i < n {
			s += fmt.Sprintf("--B%d\nContent-Type: multipart/mixed; boundary=B%d\n\n%s\n", i, i+1, level(i+1))
		}
		return s + fmt.Sprintf("--B%d--\n", i)
	}
	m := Parse(rawOf(level(0), "From: a@example.com", "Content-Type: multipart/mixed; boundary=B0"), envDate)
	if !strings.Contains(m.Text, "lvl0\n") || !strings.Contains(m.Text, "lvl19") {
		t.Errorf("shallow parts lost: %q", m.Text)
	}
	if strings.Contains(m.Text, "lvl20") || strings.Contains(m.Text, "lvl39") {
		t.Errorf("depth limit not enforced: %q", m.Text)
	}
	// Unterminated deep nesting stays bounded.
	var b strings.Builder
	for i := 0; i < 500; i++ {
		b.WriteString("Content-Type: multipart/mixed; boundary=b" + strconv.Itoa(i) + "\n\n--b" + strconv.Itoa(i) + "\n")
	}
	b.WriteString("Content-Type: text/plain\n\ndeep\n")
	if m := Parse([]byte("Subject: deep\n"+b.String()), envDate); m.Subject != "deep" {
		t.Fatalf("m = %+v", m)
	}
}

func TestPartCountCap(t *testing.T) {
	var b strings.Builder
	for i := 0; i < maxParts*2; i++ {
		b.WriteString("--B\nContent-Type: application/octet-stream\nContent-Disposition: attachment; filename=f\n\nx\n")
	}
	b.WriteString("--B--\n")
	m := Parse(rawOf(b.String(), "From: a@example.com", "Content-Type: multipart/mixed; boundary=B"), envDate)
	if len(m.Attachments) == 0 || len(m.Attachments) > maxParts {
		t.Errorf("attachments = %d", len(m.Attachments))
	}
}

func TestLargeAttachmentSizeWithoutDecodeInParse(t *testing.T) {
	data := bytes.Repeat([]byte{0xAB, 0xCD, 0xEF, 0x01, 0x02}, 2<<20) // 10 MiB
	enc := base64.StdEncoding.EncodeToString(data)
	var lines strings.Builder
	for len(enc) > 76 {
		lines.WriteString(enc[:76] + "\r\n")
		enc = enc[76:]
	}
	lines.WriteString(enc + "\r\n")
	raw := "Content-Type: multipart/mixed; boundary=b\r\n\r\n--b\r\nContent-Type: text/plain\r\n\r\nsee attached\r\n--b\r\nContent-Type: application/octet-stream; name=big.bin\r\nContent-Transfer-Encoding: base64\r\n\r\n" + lines.String() + "--b--\r\n"
	m := Parse([]byte(raw), envDate)
	if len(m.Attachments) != 1 || m.Attachments[0].Size != int64(len(data)) || m.Attachments[0].Filename != "big.bin" {
		t.Fatalf("Attachments = %+v", m.Attachments)
	}
	if _, got, err := AttachmentData([]byte(raw), 0); err != nil || !bytes.Equal(got, data) {
		t.Fatalf("AttachmentData mismatch: len %d err %v", len(got), err)
	}
}

func TestBase64SizeMatchesData(t *testing.T) {
	for n := 0; n <= 10; n++ {
		enc := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{'x'}, n))
		var b strings.Builder
		for i := 0; i < len(enc); i += 4 {
			b.WriteString(enc[i:min(i+4, len(enc))] + "\r\n")
		}
		raw := mixed(textPart, "Content-Type: application/octet-stream\nContent-Disposition: attachment; filename=f\nContent-Transfer-Encoding: base64\n\n"+b.String())
		m := Parse(raw, envDate)
		if m.Attachments[0].Size != int64(n) {
			t.Errorf("n=%d size=%d", n, m.Attachments[0].Size)
		}
		if _, d, _ := AttachmentData(raw, 0); len(d) != n {
			t.Errorf("n=%d data=%d", n, len(d))
		}
	}
}

func TestQPAttachmentSizeMatchesDataWithoutAllocating(t *testing.T) {
	raw := mixed(textPart, "Content-Type: text/csv; name=a.csv\nContent-Disposition: attachment; filename=a.csv\nContent-Transfer-Encoding: quoted-printable\n\nx=3Dy=\n,z\n")
	m := Parse(raw, envDate)
	_, d, err := AttachmentData(raw, 0)
	if err != nil || string(d) != "x=y,z" || m.Attachments[0].Size != int64(len(d)) {
		t.Errorf("%q %v size %d", d, err, m.Attachments[0].Size)
	}
	body := []byte(strings.Repeat("caf=C3=A9 soft=\r\nline =ZZ\r\n", 1000))
	want := int64(len(decodeTransfer(body, "quoted-printable")))
	var got int64
	allocs := testing.AllocsPerRun(10, func() { got = transferSize(body, "quoted-printable") })
	if allocs != 0 || got != want {
		t.Errorf("allocs = %v, size = %d want %d", allocs, got, want)
	}
}

func TestAttachmentClassification(t *testing.T) {
	tests := []struct {
		name, part  string
		inline, gen bool
		counts      bool
		filename    string
	}{
		{"inline with filename", "Content-Type: image/jpeg; name=pic.jpg\nContent-Disposition: inline; filename=pic.jpg\n\nxx\n", true, false, true, "pic.jpg"},
		{"inline pdf no cid", "Content-Type: application/pdf; name=x.pdf\nContent-Disposition: inline; filename=x.pdf\n\nx\n", true, false, true, "x.pdf"},
		{"cid image", "Content-Type: image/png\nContent-ID: <a@example.com>\n\nxx\n", true, true, false, "attachment-0.png"},
		{"cid with attachment disposition", "Content-Type: image/gif\nContent-Disposition: attachment\nContent-ID: <g@x>\n\nyy\n", false, true, true, "attachment-0.gif"},
		{"bare image", "Content-Type: image/png\n\nxx\n", false, true, true, "attachment-0.png"},
		{"inline no cid", "Content-Type: image/png\nContent-Disposition: inline\n\nxx\n", true, true, true, "attachment-0.png"},
		{"attachment without filename", "Content-Type: application/zip\nContent-Disposition: attachment\n\nPK\n", false, true, true, "attachment-0.zip"},
		{"rfc822 named", "Content-Type: message/rfc822\nContent-Disposition: attachment; filename=\"fwd.eml\"\n\nFrom: i@example.com\nSubject: inner\n\nin\n", false, false, true, "fwd.eml"},
		{"rfc822 no subject", "Content-Type: message/rfc822\n\nFrom: i@example.com\n\nin\n", false, true, true, "attachment-0.eml"},
		{"text attachment", "Content-Type: text/plain\nContent-Disposition: attachment\n\nt\n", false, true, true, "attachment-0.txt"},
		{"pgp signature", "Content-Type: application/pgp-signature; name=signature.asc\nContent-Disposition: attachment; filename=signature.asc\n\nsig\n", false, false, false, "signature.asc"},
		{"pkcs7 signature", "Content-Type: application/pkcs7-signature; name=smime.p7s\n\nAAEC\n", false, false, false, "smime.p7s"},
		{"x-pkcs7 signature", "Content-Type: application/x-pkcs7-signature\n\nsig\n", false, true, false, "attachment-0"},
		{"delivery status", "Content-Type: message/delivery-status\n\nReporting-MTA: dns; x\n", false, true, false, "attachment-0"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := Parse(mixed(textPart, tt.part), envDate)
			if len(m.Attachments) != 1 {
				t.Fatalf("Attachments = %+v", m.Attachments)
			}
			a := m.Attachments[0]
			if a.Inline != tt.inline || a.NameGenerated != tt.gen || a.CountsAsAttachment() != tt.counts || a.Filename != tt.filename {
				t.Errorf("got %+v counts=%v", a, a.CountsAsAttachment())
			}
			if s := m.Summary(); s.HasAttachment != tt.counts {
				t.Errorf("Summary.HasAttachment = %v", s.HasAttachment)
			}
			if strings.TrimSpace(m.Text) != "body" {
				t.Errorf("Text = %q", m.Text)
			}
		})
	}
}

func TestSinglePartAttachment(t *testing.T) {
	m := Parse([]byte("Content-Type: application/pdf\nContent-Transfer-Encoding: base64\n\nJVBERi0x\n"), envDate)
	if m.Text != "" || len(m.Attachments) != 1 || m.Attachments[0].Filename != "attachment-0.pdf" {
		t.Errorf("m = %+v", m)
	}
}

func TestDigestDefaultsToRFC822(t *testing.T) {
	m := Parse([]byte("Content-Type: multipart/digest; boundary=d\n\n--d\n\nSubject: one\n\nfirst\n--d--\n"), envDate)
	if len(m.Attachments) != 1 || m.Attachments[0].ContentType != "message/rfc822" || m.Attachments[0].Filename != "one.eml" {
		t.Errorf("Attachments = %+v", m.Attachments)
	}
}

func TestRFC822FilenameReadsBoundedPrefix(t *testing.T) {
	pad := "X-Pad: " + strings.Repeat("a", 9000) + "\n"
	raw := "Content-Type: multipart/mixed; boundary=b\n\n--b\nContent-Type: message/rfc822\n\nSubject: early\n\nx\n--b\nContent-Type: message/rfc822\n\n" + pad + "Subject: late\n\nx\n--b--\n"
	m := Parse([]byte(raw), envDate)
	if len(m.Attachments) != 2 || m.Attachments[0].Filename != "early.eml" || m.Attachments[1].Filename != "attachment-1.eml" {
		t.Errorf("Attachments = %+v", m.Attachments)
	}
}

func TestLatin1MislabelledUTF8(t *testing.T) {
	for _, cs := range []string{"iso-8859-1", "windows-1252", "latin1", "us-ascii"} {
		raw := "Subject: =?" + cs + "?Q?Caf=C3=A9?=\nContent-Type: text/plain; charset=" + cs + "\n\nCaf\xc3\xa9 \xe2\x82\xac"
		m := Parse([]byte(raw), envDate)
		if m.Text != "Café €" || m.Subject != "Café" {
			t.Errorf("%s: Text = %q Subject = %q", cs, m.Text, m.Subject)
		}
	}
}

func TestCRLFNormalised(t *testing.T) {
	m := Parse([]byte("From: a@example.com\r\nSubject: =?utf-8?Q?a?=\r\n =?utf-8?Q?b?=\r\nContent-Type: text/plain; charset=utf-8\r\n\r\nline1\r\nline2\r\n"), envDate)
	if m.Subject != "ab" || m.Text != "line1\nline2\n" {
		t.Errorf("Subject %q Text %q", m.Subject, m.Text)
	}
}

func TestAllStringsValidUTF8(t *testing.T) {
	m := Parse(rawOf("body \xff\n", "From: \xff\xfe <a@example.com>", "Subject: \xc3\x28 bad", "X-Gmail-Labels: \xffl", "Content-Type: text/plain; charset=utf-8"), envDate)
	check := func(s string) {
		if !utf8.ValidString(s) {
			t.Errorf("invalid UTF-8: %q", s)
		}
	}
	check(m.Subject)
	check(m.From.Name)
	check(m.Text)
	for _, f := range m.Header {
		check(f.Name)
		check(f.Value)
	}
	for _, l := range ParseLabels(m.Header.Get("X-Gmail-Labels")) {
		check(l)
	}
}

func TestPlainTextAndSummary(t *testing.T) {
	m := Parse([]byte("Subject: s\nContent-Type: text/html; charset=utf-8\n\n<p>Only <i>html</i></p>"), envDate)
	if m.Text != "" || m.HTML != "<p>Only <i>html</i></p>" {
		t.Errorf("Text %q HTML %q", m.Text, m.HTML)
	}
	if m.PlainText() != "Only html" {
		t.Errorf("PlainText = %q", m.PlainText())
	}
	s := m.Summary()
	if s.Subject != "s" || s.Text != "Only html" || s.HasAttachment || s.AttachmentNames != nil {
		t.Errorf("Summary = %+v", s)
	}
	// Text wins over HTML; Summary text is capped on a rune boundary.
	long := strings.Repeat("é", MaxSummaryText)
	m = Parse([]byte("Content-Type: text/plain; charset=utf-8\n\n"+long), envDate)
	if s := m.Summary(); len(s.Text) > MaxSummaryText || !utf8.ValidString(s.Text) || len(s.Text) < MaxSummaryText-1 {
		t.Errorf("Summary text len %d", len(s.Text))
	}
	if got := Summarize([]byte("Subject: x\n\nhello"), envDate); got.Subject != "x" || got.Text != "hello" || !got.Date.Equal(envDate) {
		t.Errorf("Summarize = %+v", got)
	}
}

func TestParseNeverPanicsOnHostileInput(t *testing.T) {
	inputs := []string{
		"Content-Type: multipart/mixed; boundary=\"\"\n\n--\n",
		"Content-Type: multipart/mixed; boundary=B\n\n--B\nContent-Type: text/plain\n--B\nContent-Type: image/png\n\nx\n--B--\n",
		"Content-Disposition: attachment; filename*0*=UTF-8''a%; filename*1=b\nContent-Type: application/x\n\nz",
		"Date: Mon, 01 Jan 2024 10:00:00 GMT+0530 (x\n\n",
		"From: a <a@example.com>\nTo: ,,<>\nDate: x\nReferences: <\n\n",
		"Subject: =?utf-8?B?\xff?=\n",
	}
	for _, in := range inputs {
		m := parse([]byte(in), envDate) // no recover: panics surface
		_ = m.Summary()
	}
}

func FuzzParse(f *testing.F) {
	seeds := []string{
		"", "\n", "Subject: x\n\nbody", nestedMsg, crlf(nestedMsg),
		"Content-Type: multipart/mixed; boundary=b\n\n--b\nContent-Type: text/plain\n\nfirst\n--b",
		"Subject: =?UTF-8?B?4oI=?= =?x?Q?=E9?=\nTo: a:;, <<b>>, (c) d@e\n\nx",
		"Content-Type: text/plain; charset=koi8-r\nContent-Transfer-Encoding: base64\n\n8NLJ18XU",
		"Content-Type: multipart/digest; boundary=d\n\n--d\n\nSubject: one\n\nfirst\n--d--\n",
		"Content-Disposition: attachment; filename*0*=UTF-8''a%; filename*1=b\nContent-Type: application/x\n\nz",
		"Content-Type: multipart/mixed; boundary*=utf-8''a%0Ab\n\n--a\nb\nx\n--a\nb--\n",
	}
	for _, s := range seeds {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, raw []byte) {
		m := parse(raw, envDate) // no recover: panics surface
		if m == nil {
			t.Fatal("nil message")
		}
		for i, a := range m.Attachments {
			got, data, err := attachmentData(raw, func(r attachmentRef) bool { return r.att.Index == i })
			if err != nil {
				t.Fatalf("attachment %d missing: %v", i, err)
			}
			if got != a || int64(len(data)) != a.Size {
				t.Fatalf("attachment %d mismatch: %+v vs %+v (len %d)", i, got, a, len(data))
			}
		}
		s := m.Summary()
		for _, str := range []string{s.Subject, s.From.Name, s.From.Email, s.MessageID, s.Text} {
			if !utf8.ValidString(str) {
				t.Fatalf("invalid UTF-8 %q", str)
			}
		}
	})
}
