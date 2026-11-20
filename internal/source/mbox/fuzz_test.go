package mbox

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lazyoracle/mail-export-viewer/internal/source"
	"github.com/lazyoracle/mail-export-viewer/internal/source/sourcetest"
)

var fuzzSeeds = []string{
	"",
	"From a@example.com Mon Jan  2 15:04:05 2023\nSubject: x\n\nbody\n",
	"From a@example.com Mon Jan  2 15:04:05 2023\r\nSubject: x\r\n\r\n>From quoted\r\n\r\nFrom b Tue Feb  3 01:02:03 2004\r\n\r\n",
	"\xef\xbb\xbfFrom a Mon Jan 2 15:04 2023\nContent-Type: multipart/mixed; boundary=b\n\n--b\nContent-Disposition: attachment; filename=a.txt\n\nx\n--b--\n",
	"From a Mon Jan 2 15:04:05 2023\nX-Gmail-Labels: Inbox,=?UTF-8?Q?caf=C3=A9?=,\"a,b\"\nX-Mozilla-Status: 0011\nX-Mozilla-Status2: 00810000\nX-Mozilla-Keys: $label1 junk\n\n",
	"From a Mon Jan 2 15:04:05 2023\n\x00\xff\xfe garbage\nFrom not-a-separator\n\nFrom b Mon Jan 2 15:04:05 2023\n",
}

// FuzzScanMessages checks the scanner's framing invariants: messages are
// in order, inside the input, non-overlapping, and Raw stays within the
// prefix cap.
func FuzzScanMessages(f *testing.F) {
	for _, s := range fuzzSeeds {
		f.Add([]byte(s), uint16(64))
	}
	f.Fuzz(func(t *testing.T, data []byte, capN uint16) {
		prefixCap := int(capN%4096) + 1
		var end int64
		err := scanMessages(bytes.NewReader(data), prefixCap, func(m *message) error {
			if m.Offset < end || m.Length <= 0 || m.Offset+m.Length > int64(len(data)) {
				t.Fatalf("bad framing: offset %d length %d (previous end %d, input %d)", m.Offset, m.Length, end, len(data))
			}
			if len(m.Raw) > prefixCap {
				t.Fatalf("Raw %d bytes over cap %d", len(m.Raw), prefixCap)
			}
			end = m.Offset + m.Length
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	})
}

// FuzzSource runs the whole mbox source (scan, metadata, Load, Raw,
// Attachment) over the input and fails on any recovered panic.
func FuzzSource(f *testing.F) {
	for _, s := range fuzzSeeds {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		path := filepath.Join(t.TempDir(), "Inbox")
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
		src, err := source.Candidate{Format: source.Mbox, Path: path, Rel: "Inbox"}.Open()
		if err != nil {
			return
		}
		defer src.Close()
		sourcetest.Within(t, 10*time.Second, func() {
			if _, panics := sourcetest.Exercise(src, 50); len(panics) > 0 {
				t.Errorf("recovered panics: %q", panics)
			}
		})
	})
}
