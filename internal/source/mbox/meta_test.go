package mbox

import (
	"reflect"
	"strings"
	"testing"

	"github.com/lazyoracle/mail-export-viewer/internal/mail"
	"github.com/lazyoracle/mail-export-viewer/internal/source"
)

func meta(t *testing.T, header string, tags map[string]source.TagStyle) headerMeta {
	t.Helper()
	return headerMetadata(mail.ParseHeader([]byte(header+"\n\nbody\n")), tags)
}

func TestMozillaStatus(t *testing.T) {
	for _, tc := range []struct {
		name, header string
		want         source.Flags
	}{
		{"none: read", "Subject: x", 0},
		{"unread", "X-Mozilla-Status: 0000", source.Unread},
		{"read", "X-Mozilla-Status: 0001", 0},
		{"replied", "X-Mozilla-Status: 0003", source.Answered},
		{"flagged", "X-Mozilla-Status: 0005", source.Starred},
		{"expunged", "X-Mozilla-Status: 0009", source.Deleted},
		{"forwarded", "X-Mozilla-Status: 1001", source.Forwarded},
		{"all", "X-Mozilla-Status: 100F", source.Answered | source.Starred | source.Deleted | source.Forwarded},
		{"imap deleted in status2", "X-Mozilla-Status: 0001\nX-Mozilla-Status2: 00200000", source.Deleted},
		{"status2 alone, unread", "X-Mozilla-Status2: 00000000", 0},
		{"draft info", "X-Mozilla-Status: 0001\nX-Mozilla-Draft-Info: internal/draft; vcard=0", source.Draft},
		{"bad value ignored", "X-Mozilla-Status: zz", 0},
		{"0x prefix and case", "x-mozilla-status: 0x0005", source.Starred},
		{"keyword forwarded", "X-Mozilla-Status: 0001\nX-Mozilla-Keys: $Forwarded", source.Forwarded},
		{"keyword junk", "X-Mozilla-Status: 0001\nX-Mozilla-Keys: Junk", source.Junk},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := meta(t, tc.header, nil).Flags; got != tc.want {
				t.Errorf("flags = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestGmailLabels(t *testing.T) {
	m := meta(t, "X-Gmail-Labels: Inbox,Unread,Starred,Important,Work stuff,\"Smith, Jo\"", nil)
	want := []string{"Inbox", "Unread", "Starred", "Important", "Work stuff", "Smith, Jo"}
	if !reflect.DeepEqual(m.Labels, want) {
		t.Errorf("labels = %q", m.Labels)
	}
	if m.Flags != source.Unread|source.Starred|source.Important {
		t.Errorf("flags = %q", m.Flags)
	}
	m = meta(t, "X-Gmail-Labels: =?UTF-8?Q?Caf=C3=A9?=,Sent", nil)
	if !reflect.DeepEqual(m.Labels, []string{"Café", "Sent"}) || m.Flags != 0 {
		t.Errorf("encoded: %q %q", m.Labels, m.Flags)
	}
	m = meta(t, "X-Gmail-Labels: starred", nil)
	if m.Flags != source.Starred {
		t.Errorf("case: %q", m.Flags)
	}
}

func TestMozillaKeys(t *testing.T) {
	tags := map[string]source.TagStyle{"$label2": {Name: "Work stuff", Color: "#FF9900"}, "proj": {Name: "Project X"}}
	m := meta(t, "X-Mozilla-Keys: $label2 proj  $label1 $MDNSent NonJunk $label2 custom", tags)
	// Known tags are renamed; $label1 falls back to Thunderbird's default
	// name; IMAP housekeeping keywords are no tags; unknown keys stay as is.
	want := []string{"Work stuff", "Project X", "Important", "custom"}
	if !reflect.DeepEqual(m.Tags, want) {
		t.Errorf("tags = %q, want %q", m.Tags, want)
	}
	if m.Flags != 0 {
		t.Errorf("flags = %q", m.Flags)
	}
	if got := meta(t, "X-Mozilla-Keys:", nil).Tags; got != nil {
		t.Errorf("empty keys: %q", got)
	}
}

func TestProps(t *testing.T) {
	m := meta(t, "Subject: x\nX-Mailer: Fake Mailer 1.0\nX-Gmail-Labels: Inbox\nX-Mozilla-Status: 0001\nX-Mozilla-Status2: 0\n"+
		"X-Mozilla-Keys: \nX-Mozilla-Draft-Info: a\nX-Spam-Score: 0.1\nX-Mailer: Again\nX-Subject-Enc: =?UTF-8?Q?caf=C3=A9?=", nil)
	want := []source.Prop{{Key: "X-Mailer", Value: "Fake Mailer 1.0"}, {Key: "X-Spam-Score", Value: "0.1"}, {Key: "X-Mailer", Value: "Again"}, {Key: "X-Subject-Enc", Value: "café"}}
	if !reflect.DeepEqual(m.Props, want) {
		t.Errorf("props = %q", m.Props)
	}
}

func TestPropsBounded(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 500; i++ {
		b.WriteString("X-N" + strings.Repeat("a", i%5) + ": v\n")
	}
	b.WriteString("X-Long: " + strings.Repeat("é", 5000) + "\n")
	m := meta(t, b.String(), nil)
	if len(m.Props) != maxProps {
		t.Errorf("%d props, want %d", len(m.Props), maxProps)
	}
	m = meta(t, "X-Long: "+strings.Repeat("é", 5000), nil)
	if v := m.Props[0].Value; len(v) > maxPropValue || !strings.HasPrefix(v, "éé") {
		t.Errorf("long value: %d bytes", len(v))
	}
}

func TestMessageIDAndStatusKey(t *testing.T) {
	m := meta(t, "Message-ID: <abc@example.com>\nX-Mozilla-Status: 0001", nil)
	if m.MessageID != "abc@example.com" || m.StatusKey != "0001" {
		t.Errorf("%+v", m)
	}
}
