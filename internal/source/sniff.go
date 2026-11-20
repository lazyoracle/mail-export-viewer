package source

import (
	"archive/zip"
	"bytes"
	"fmt"
	"io"
	"os"
	"strings"
)

// Content kinds reported by Sniff. Format packages register under the same
// names (Mork files are sidecars of mbox files, not a format of their own).
const (
	Mbox = "mbox"
	PST  = "pst"
	OLM  = "olm"
	Mork = "mork"
	EML  = "eml"
)

// SniffLen is how many leading bytes of a file Head.Data holds.
const SniffLen = 8 << 10

// Head is what content detection sees of a regular file: its path, size
// and first SniffLen bytes. ReaderAt reads the whole file and is valid only
// during the Sniff call (for zip directory checks).
type Head struct {
	Path string
	Size int64
	Data []byte
	r    io.ReaderAt
}

// ReaderAt returns a reader over the whole file (valid during sniffing only).
func (h *Head) ReaderAt() io.ReaderAt { return h.r }

// withHead opens path read-only (it must be a regular file; FIFOs and
// devices are refused before opening so sniffing never blocks), reads its
// head and calls fn.
func withHead(path string, fn func(*Head)) error {
	fi, err := os.Stat(path)
	if err != nil {
		return err
	}
	if !fi.Mode().IsRegular() {
		return fmt.Errorf("source: %s is not a regular file", path)
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	buf := make([]byte, min(fi.Size(), SniffLen))
	n, err := io.ReadFull(f, buf)
	if err != nil && err != io.ErrUnexpectedEOF {
		return err
	}
	fn(&Head{Path: path, Size: fi.Size(), Data: buf[:n], r: f})
	return nil
}

// Sniff detects the content kind of the regular file at path by content
// (never by name): Mbox, PST, OLM, Mork, EML, or "" when unrecognised.
func Sniff(path string) (string, error) {
	kind := ""
	err := withHead(path, func(h *Head) {
		for _, k := range []struct {
			name string
			is   func(*Head) bool
		}{{PST, IsPST}, {OLM, IsOLM}, {Mork, IsMork}, {Mbox, IsMbox}, {EML, IsEML}} {
			if k.is(h) {
				kind = k.name
				return
			}
		}
	})
	return kind, err
}

var bom = []byte("\xef\xbb\xbf")

// IsPST reports a PST/OST file: the "!BDN" magic.
func IsPST(h *Head) bool { return bytes.HasPrefix(h.Data, []byte("!BDN")) }

// IsMork reports a Thunderbird Mork (.msf) file: "// <!-- <mdb:mork" after
// an optional BOM and whitespace.
func IsMork(h *Head) bool {
	d := bytes.TrimLeft(bytes.TrimPrefix(h.Data, bom), " \t\r\n")
	return bytes.HasPrefix(d, []byte("// <!-- <mdb:mork"))
}

// IsMbox reports an mbox file: the first non-blank line (after an optional
// BOM) starts with "From ".
func IsMbox(h *Head) bool {
	d := bytes.TrimLeft(bytes.TrimPrefix(h.Data, bom), " \t\r\n")
	return bytes.HasPrefix(d, []byte("From "))
}

// maxZipNames bounds how many zip entries IsOLM inspects.
const maxZipNames = 10000

// IsOLM reports an Outlook for Mac archive: a zip with an entry under
// "Local/" (Outlook 2011) or "Accounts/" (new Outlook).
func IsOLM(h *Head) bool {
	if !bytes.HasPrefix(h.Data, []byte("PK\x03\x04")) || h.r == nil {
		return false
	}
	zr, err := zip.NewReader(h.r, h.Size)
	if err != nil {
		return false
	}
	for i, f := range zr.File {
		if i >= maxZipNames {
			break
		}
		name := strings.ToLower(strings.TrimLeft(strings.ReplaceAll(f.Name, `\`, "/"), "./"))
		if strings.HasPrefix(name, "local/") || strings.HasPrefix(name, "accounts/") {
			return true
		}
	}
	return false
}

// emlMarkers are header fields typical of a stored message; a plain
// "Key: value" text file has none of them.
var emlMarkers = map[string]bool{
	"from": true, "sender": true, "date": true, "message-id": true, "received": true,
	"return-path": true, "mime-version": true, "delivered-to": true, "in-reply-to": true,
	"references": true, "x-mozilla-status": true,
}

// IsEML reports a single RFC 5322 message: every line before the first
// blank line (or the end of the sniffed prefix) is a header field or a
// continuation, the first is a field, and at least one is a typical mail
// field (From, Date, Message-ID, Received, ...).
func IsEML(h *Head) bool {
	d := bytes.TrimPrefix(h.Data, bom)
	if len(h.Data) == SniffLen && int64(len(h.Data)) < h.Size {
		// Drop the possibly cut last line.
		if i := bytes.LastIndexByte(d, '\n'); i >= 0 {
			d = d[:i+1]
		}
	}
	marker := false
	first := true
	for len(d) > 0 {
		line := d
		if i := bytes.IndexByte(d, '\n'); i >= 0 {
			line, d = d[:i], d[i+1:]
		} else {
			d = nil
		}
		line = bytes.TrimSuffix(line, []byte("\r"))
		if len(line) == 0 {
			break
		}
		if line[0] == ' ' || line[0] == '\t' {
			if first {
				return false
			}
			continue
		}
		name, ok := fieldName(line)
		if !ok {
			return false
		}
		first = false
		if emlMarkers[strings.ToLower(name)] {
			marker = true
		}
	}
	return marker
}

// fieldName returns the name of a "Name: value" line (printable ASCII, no
// spaces).
func fieldName(line []byte) (string, bool) {
	i := bytes.IndexByte(line, ':')
	if i <= 0 || i > 76 {
		return "", false
	}
	for _, c := range line[:i] {
		if c <= ' ' || c >= 0x7f {
			return "", false
		}
	}
	return string(line[:i]), true
}
