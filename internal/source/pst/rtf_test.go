package pst

import (
	"strings"
	"testing"

	"github.com/lazyoracle/mail-export-viewer/internal/source/pst/pstgen"
)

func TestLZFuSpecExample(t *testing.T) {
	// [MS-OXRTFCP] 3.1.1 example.
	in := []byte{0x2d, 0x00, 0x00, 0x00, 0x2b, 0x00, 0x00, 0x00, 0x4c, 0x5a, 0x46, 0x75, 0xf1, 0xc5, 0xc7, 0xa7,
		0x03, 0x00, 0x0a, 0x00, 0x72, 0x63, 0x70, 0x67, 0x31, 0x32, 0x35, 0x42, 0x32, 0x0a, 0xf3, 0x20,
		0x68, 0x65, 0x6c, 0x09, 0x00, 0x20, 0x62, 0x77, 0x05, 0xb0, 0x6c, 0x64, 0x7d, 0x0a, 0x80, 0x0f, 0xa0}
	got, err := lzfu(in, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if want := "{\\rtf1\\ansi\\ansicpg1252\\pard hello world}\r\n"; string(got) != want {
		t.Fatalf("lzfu = %q", got)
	}
}

func TestLZFuRoundTripAndErrors(t *testing.T) {
	src := []byte(strings.Repeat("{\\rtf1 some text } ", 500))
	got, err := lzfu(pstgen.CompressRTF(src), 1<<20)
	if err != nil || string(got) != string(src) {
		t.Fatalf("round trip: %d bytes, %v", len(got), err)
	}
	mela := append([]byte{5, 0, 0, 0, 5, 0, 0, 0, 'M', 'E', 'L', 'A', 0, 0, 0, 0}, "hello"...)
	if got, err := lzfu(mela, 100); err != nil || string(got) != "hello" {
		t.Fatalf("MELA: %q %v", got, err)
	}
	if _, err := lzfu(pstgen.CompressRTF(src), 100); err == nil {
		t.Errorf("no error above the size limit")
	}
	for _, bad := range [][]byte{nil, []byte("short"), append([]byte{100, 0, 0, 0, 1, 0, 0, 0}, "XXXX0000"...)} {
		if _, err := lzfu(bad, 100); err == nil {
			t.Errorf("lzfu(%q): no error", bad)
		}
	}
	// Garbage after a valid header must not panic.
	g := pstgen.CompressRTF(src)
	for i := 16; i < len(g); i += 7 {
		g[i] ^= 0x5a
	}
	lzfu(g, 1<<20)
}

func TestRTFText(t *testing.T) {
	in := `{\rtf1\ansi\ansicpg1252\deff0{\fonttbl{\f0\fswiss Arial;}}{\colortbl;\red0\green0\blue0;}` +
		`{\*\generator Msftedit;}\pard\f0 Hello \b world\b0\par caf\'e9 \u8364?\tab x\line ` +
		`{\field{\*\fldinst HYPERLINK "http://example.com"}{\fldrslt link}}\par}`
	html, text := rtfConvert([]byte(in))
	if html != "" {
		t.Errorf("html = %q", html)
	}
	if want := "Hello world\ncafé €\tx\nlink\n"; text != want {
		t.Errorf("text = %q, want %q", text, want)
	}
}

func TestRTFFromHTML(t *testing.T) {
	in := `{\rtf1\ansi\ansicpg1252\fromhtml1 \deff0{\fonttbl{\f0\fswiss Arial;}}` + "\r\n" +
		`{\*\htmltag19 <html>}{\*\htmltag34 <head>}{\*\htmltag161 <title>}T{\*\htmltag169 </title>}` +
		`{\*\htmltag41 </head>}{\*\htmltag50 <body>}\htmlrtf {\htmlrtf0 ` + "\r\n" +
		`{\*\htmltag64 <p>}\htmlrtf {\htmlrtf0 Hello caf\'e9 \{x\}{\*\htmltag84 <img src="cid:i1">}` +
		`{\*\mhtmltag84 <img src="cid:other">}\htmlrtf\par\htmlrtf0 {\*\htmltag72 </p>}\htmlrtf }\htmlrtf0 ` +
		`{\*\htmltag58 </body>}{\*\htmltag27 </html>}}`
	html, text := rtfConvert([]byte(in))
	if text != "" {
		t.Errorf("text = %q", text)
	}
	want := `<html><head><title>T</title></head><body><p>Hello café {x}<img src="cid:i1"></p></body></html>`
	if html != want {
		t.Errorf("html = %q\nwant   %q", html, want)
	}
}

func TestRTFHostile(t *testing.T) {
	for _, in := range []string{"", "{", "}", "{\\rtf1", strings.Repeat("{", 100000), "\\'", "\\'zz", "\\u", "\\u-5000000000 x",
		"{\\rtf1\\uc99999\\u65 abc}", "{\\rtf1 \\" + strings.Repeat("a", 100000) + "}"} {
		rtfConvert([]byte(in))
	}
}
