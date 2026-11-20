package source

import (
	"archive/zip"
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func write(t *testing.T, path string, data []byte) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func zipOf(t *testing.T, names ...string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, n := range names {
		w, err := zw.Create(n)
		if err != nil {
			t.Fatal(err)
		}
		w.Write([]byte("<xml/>"))
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

var (
	mboxData = []byte("From a@example.com Mon Jan  1 00:00:00 2024\nSubject: a\n\nhello\n")
	emlData  = []byte("Return-Path: <a@example.com>\nReceived: from mx.example.com\n\tby mx2.example.com\nFrom: A <a@example.com>\nSubject: hi\n\nbody\n")
	morkData = []byte("// <!-- <mdb:mork:z v=\"1.4\"/> -->\n< <(a=c)> // (f=iso-8859-1)\n")
	pstData  = append([]byte("!BDN\x00\x00\x00\x00SM\x17\x00"), make([]byte, 600)...)
)

func TestSniff(t *testing.T) {
	dir := t.TempDir()
	cases := []struct {
		name string
		data []byte
		want string
	}{
		{"mbox", mboxData, Mbox},
		{"mbox-bom-blank", append([]byte("\xef\xbb\xbf\r\n\n"), mboxData...), Mbox},
		{"mbox-crlf", bytes.ReplaceAll(mboxData, []byte("\n"), []byte("\r\n")), Mbox},
		{"notes.txt", []byte("hello\nFrom here\n"), ""},
		{"pst", pstData, PST},
		{"ost", append([]byte("!BDN\x00\x00\x00\x00SO\x17\x00"), make([]byte, 600)...), PST},
		{"olm-2011", zipOf(t, "Categories.xml", "Local/Messages/Inbox/message_00001.xml"), OLM},
		{"olm-new", zipOf(t, "Accounts/a@example.com/com.microsoft.__Messages/Inbox/x.olk15Message"), OLM},
		{"plain.zip", zipOf(t, "docs/readme.txt"), ""},
		{"broken.zip", []byte("PK\x03\x04garbage"), ""},
		{"Inbox.msf", morkData, Mork},
		{"mork-bom", append([]byte("\xef\xbb\xbf  "), morkData...), Mork},
		{"msg.eml", emlData, EML},
		{"msg-crlf.eml", bytes.ReplaceAll(emlData, []byte("\n"), []byte("\r\n")), EML},
		{"headers-only.eml", []byte("Message-ID: <x@example.com>\nSubject: s\n"), EML},
		{"mime-version-first", []byte("MIME-Version: 1.0\nContent-Type: text/plain\n\nx"), EML},
		{"config-like", []byte("Name: value\nOther: thing\n\nbody"), ""},
		{"subject only", []byte("Subject: s\n\nbody"), ""},
		{"garbage line in header", []byte("From: a@example.com\nnot a header\nSubject: s\n\nb"), ""},
		{"empty", nil, ""},
		{"jpeg", []byte{0xff, 0xd8, 0xff, 0xe0, 0, 0x10, 'J', 'F', 'I', 'F', 0, '\n'}, ""},
		{"blank lines only", []byte("\n\n\r\n"), ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := write(t, filepath.Join(dir, c.name), c.data)
			got, err := Sniff(p)
			if err != nil {
				t.Fatal(err)
			}
			if got != c.want {
				t.Errorf("Sniff = %q, want %q", got, c.want)
			}
		})
	}
	if _, err := Sniff(filepath.Join(dir, "missing")); err == nil {
		t.Error("Sniff(missing): want error")
	}
	if _, err := Sniff(dir); err == nil {
		t.Error("Sniff(dir): want error")
	}
}

func TestSniffHugeHeaderEML(t *testing.T) {
	// Headers longer than the sniffed prefix still count when every line
	// seen so far is a header line.
	data := append([]byte("From: a@example.com\n"), bytes.Repeat([]byte("X-Long: "+string(bytes.Repeat([]byte("v"), 70))+"\n"), 500)...)
	p := write(t, filepath.Join(t.TempDir(), "big"), data)
	if got, _ := Sniff(p); got != EML {
		t.Errorf("Sniff = %q", got)
	}
}
