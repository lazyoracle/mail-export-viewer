package mail

import (
	"bytes"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
)

func attData(blobs ...[]byte) func(int) ([]byte, error) {
	return func(i int) ([]byte, error) {
		if i < 0 || i >= len(blobs) {
			return nil, ErrNotFound
		}
		return blobs[i], nil
	}
}

func writeEML(t *testing.T, m *Message, data func(int) ([]byte, error)) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := WriteEML(&buf, m, data); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestWriteEMLRoundTripSynthesisedHeader(t *testing.T) {
	png := []byte("\x89PNG\r\n\x1a\nfake image bytes")
	pdf := bytes.Repeat([]byte("%PDF-1.4 binary \x00\xff "), 50)
	inner := []byte("From: inner@example.com\r\nSubject: Inner\r\n\r\ninner body\r\n")
	in := &Message{
		Envelope: Envelope{
			MessageID:  "id1@example.com",
			InReplyTo:  "parent@example.com",
			References: []string{"root@example.com", "parent@example.com"},
			Subject:    "Café report — Q1",
			From:       Address{"Jörg Example", "joerg@example.com"},
			To:         []Address{{"", "a@example.com"}, {"Doe, Jane", "jane@example.com"}},
			Cc:         []Address{{"Carol", "carol@example.com"}},
			Bcc:        []Address{{"", "hidden@example.com"}},
			Date:       time.Date(2024, 3, 5, 10, 0, 0, 0, time.UTC),
		},
		Text: "Hello é\nsecond line with a very long tail " + strings.Repeat("x", 120) + "\n",
		HTML: `<p>Hello <img src="cid:logo@example.com"></p>`,
		Attachments: []Attachment{
			{Index: 0, Filename: "logo.png", ContentType: "image/png", ContentID: "logo@example.com", Inline: true, Size: int64(len(png))},
			{Index: 1, Filename: "résumé.pdf", ContentType: "application/pdf", Size: int64(len(pdf))},
			{Index: 2, Filename: "fwd.eml", ContentType: "message/rfc822", Size: int64(len(inner))},
		},
	}
	raw := writeEML(t, in, attData(png, pdf, inner))
	if bytes.Contains(bytes.ReplaceAll(raw, []byte("\r\n"), nil), []byte("\n")) {
		t.Errorf("bare LF in output")
	}
	out := Parse(raw, time.Time{})
	if !out.Date.Equal(in.Date) {
		t.Errorf("Date = %v", out.Date)
	}
	out.Date = in.Date
	if !reflect.DeepEqual(out.Envelope, in.Envelope) {
		t.Errorf("Envelope = %+v\nwant %+v", out.Envelope, in.Envelope)
	}
	if out.Text != in.Text {
		t.Errorf("Text = %q", out.Text)
	}
	if out.HTML != in.HTML {
		t.Errorf("HTML = %q", out.HTML)
	}
	if len(out.Attachments) != 3 {
		t.Fatalf("Attachments = %+v", out.Attachments)
	}
	for i, w := range in.Attachments {
		got := out.Attachments[i]
		if got.Filename != w.Filename || got.ContentType != w.ContentType || got.ContentID != w.ContentID || got.Inline != w.Inline {
			t.Errorf("att %d = %+v, want %+v", i, got, w)
		}
		_, d, err := AttachmentData(raw, i)
		if err != nil {
			t.Fatal(err)
		}
		want, _ := attData(png, pdf, inner)(i)
		if !bytes.Equal(d, want) || got.Size != int64(len(want)) {
			t.Errorf("att %d data mismatch (len %d size %d want %d)", i, len(d), got.Size, len(want))
		}
	}
}

func TestWriteEMLKeepsOriginalHeader(t *testing.T) {
	in := Parse([]byte("From: a@example.com\nSubject: =?UTF-8?Q?Caf=C3=A9?=\nX-Custom: kept\nMIME-Version: 1.0\nContent-Type: multipart/mixed; boundary=old\nContent-Transfer-Encoding: 7bit\n\n--old\nContent-Type: text/plain\n\nignored\n--old--\n"), time.Time{})
	in.Text = "replacement body"
	raw := writeEML(t, in, nil)
	s := string(raw)
	if !strings.Contains(s, "X-Custom: kept\r\n") || !strings.Contains(s, "Subject: =?UTF-8?Q?Caf=C3=A9?=\r\n") {
		t.Errorf("original fields lost:\n%s", s)
	}
	if strings.Contains(s, "boundary=old") || strings.Count(s, "MIME-Version:") != 1 || strings.Contains(s, "7bit") {
		t.Errorf("stale MIME fields kept:\n%s", s)
	}
	out := Parse(raw, time.Time{})
	if out.Subject != "Café" || out.Text != "replacement body" || out.Header.Get("X-Custom") != "kept" {
		t.Errorf("out = %+v", out)
	}
}

func TestWriteEMLShapes(t *testing.T) {
	tests := []struct {
		name     string
		m        *Message
		wantType string
	}{
		{"empty", &Message{}, "text/plain"},
		{"text only", &Message{Text: "t"}, "text/plain"},
		{"html only", &Message{HTML: "<b>h</b>"}, "text/html"},
		{"both", &Message{Text: "t", HTML: "<b>h</b>"}, "multipart/alternative"},
		{"attachment", &Message{Text: "t", Attachments: []Attachment{{Filename: "a.bin"}}}, "multipart/mixed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			raw := writeEML(t, tt.m, attData([]byte("x")))
			h := ParseHeader(raw)
			if ct, _ := parseMediaType(h.Get("Content-Type")); ct != tt.wantType {
				t.Errorf("Content-Type = %q", h.Get("Content-Type"))
			}
			out := Parse(raw, time.Time{})
			if out.Text != tt.m.Text || out.HTML != tt.m.HTML || len(out.Attachments) != len(tt.m.Attachments) {
				t.Errorf("round trip = %+v", out)
			}
		})
	}
}

func TestWriteEMLHeaderInjection(t *testing.T) {
	m := &Message{Envelope: Envelope{Subject: "a\r\nBcc: evil@example.com", From: Address{"x\ny", "a@example.com"}},
		Attachments: []Attachment{{Filename: "a\r\nX: y.txt", ContentType: "text/plain\r\nX: z", ContentID: "c\nX: w"}}}
	raw := writeEML(t, m, attData([]byte("x")))
	h := ParseHeader(raw)
	if h.Get("Bcc") != "" || h.Get("X") != "" {
		t.Errorf("header injected:\n%s", raw)
	}
	out := Parse(raw, time.Time{})
	if len(out.Attachments) != 1 || strings.ContainsAny(out.Attachments[0].Filename, "\r\n") {
		t.Errorf("attachments = %+v", out.Attachments)
	}
}

func TestWriteEMLDataError(t *testing.T) {
	boom := errors.New("boom")
	m := &Message{Attachments: []Attachment{{Filename: "a"}}}
	err := WriteEML(&bytes.Buffer{}, m, func(int) ([]byte, error) { return nil, boom })
	if !errors.Is(err, boom) {
		t.Errorf("err = %v", err)
	}
	if err := WriteEML(&bytes.Buffer{}, m, nil); err == nil {
		t.Error("nil data with attachments: want error")
	}
}

func TestWriteEMLFoldsLongPreservedFields(t *testing.T) {
	var refs []string
	for i := 0; i < 200; i++ {
		refs = append(refs, fmt.Sprintf("<ref%03d@example.com>", i))
	}
	long := strings.Join(refs, " ")
	token := strings.Repeat("x", 1200) // no whitespace: cannot be folded
	in := &Message{Header: Header{{"References", long}, {"X-Token", token}}, Text: "t"}
	raw := writeEML(t, in, nil)
	for _, line := range strings.Split(string(raw), "\r\n") {
		if len(line) > 998 && !strings.HasPrefix(line, "X-Token: ") {
			t.Errorf("line of %d bytes: %.40q", len(line), line)
		}
	}
	h := ParseHeader(raw)
	if h.Get("References") != long || h.Get("X-Token") != token {
		t.Errorf("unfolded values differ")
	}
	if got := Parse(raw, time.Time{}).References; len(got) != 200 {
		t.Errorf("References = %d", len(got))
	}
}

func TestEMLBytesLimit(t *testing.T) {
	big := bytes.Repeat([]byte("z"), 3000)
	m := &Message{Envelope: Envelope{Subject: "limit", From: Address{"", "a@example.com"}}, Text: "body\n",
		Attachments: []Attachment{{Index: 0, Filename: "big.bin", ContentType: "application/octet-stream", Size: int64(len(big))}}}
	var want bytes.Buffer
	if err := WriteEML(&want, m, attData(big)); err != nil {
		t.Fatal(err)
	}
	tooLarge := errors.New("too large")
	got, err := EMLBytes(m, attData(big), int64(want.Len()), tooLarge)
	// Same size (the MIME boundary is random) and the same message.
	if err != nil || len(got) != want.Len() || Parse(got, time.Time{}).Attachments[0].Size != int64(len(big)) {
		t.Fatalf("at the limit: %v, %d bytes, want %d", err, len(got), want.Len())
	}
	asked := 0
	if _, err := EMLBytes(m, func(i int) ([]byte, error) { asked++; return big, nil }, 1000, tooLarge); !errors.Is(err, tooLarge) {
		t.Fatalf("over the limit: %v", err)
	}
	if asked != 1 {
		t.Fatalf("data asked %d times", asked)
	}
}
