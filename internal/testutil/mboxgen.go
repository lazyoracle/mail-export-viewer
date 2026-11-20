package testutil

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// MakeMbox joins RFC 5322 messages (LF line endings) into an mbox: each
// gets a "From " separator line with a fixed envelope date, body lines
// starting with ">*From " are quoted with one more '>' (mboxrd), and
// messages are separated by a blank line.
func MakeMbox(msgs ...string) []byte {
	var b bytes.Buffer
	for _, m := range msgs {
		b.WriteString("From sender@example.com Mon Jan  2 15:04:05 2023\n")
		for _, line := range strings.SplitAfter(strings.TrimSuffix(m, "\n")+"\n", "\n") {
			if strings.HasPrefix(strings.TrimLeft(line, ">"), "From ") {
				b.WriteByte('>')
			}
			b.WriteString(line)
		}
		b.WriteByte('\n')
	}
	return b.Bytes()
}

// WriteMbox writes MakeMbox(msgs...) to path (creating parent directories)
// and returns path.
func WriteMbox(t testing.TB, path string, msgs ...string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, MakeMbox(msgs...), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}
