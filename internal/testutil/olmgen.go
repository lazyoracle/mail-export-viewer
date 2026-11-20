package testutil

import (
	"archive/zip"
	"bytes"
	"compress/flate"
	"hash/crc32"
	"encoding/xml"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// The OLM generator builds synthetic Outlook for Mac archives for tests.
// The layouts and element names are modelled on public descriptions of
// OLM files and have NOT been verified against real Outlook exports (see
// internal/source/olm/doc.go).

// OLMEntry is one file of a synthetic archive.
type OLMEntry struct {
	Name string
	Data []byte
	// DeclaredSize, when non-zero, is written as the entry's uncompressed
	// size in the zip headers instead of len(Data) (a lying archive).
	DeclaredSize uint64
}

// OLMAddr is a recipient or sender.
type OLMAddr struct{ Name, Email string }

// OLMAtt is an attachment of an OLMMsg. Data is written as the zip entry
// URL (a default path next to the message is used when URL is empty).
type OLMAtt struct {
	Name, Type, CID, URL string
	Data                 []byte
	Missing              bool // reference an entry that does not exist
}

// OLMMsg describes a message to render with OLMMessageXML.
type OLMMsg struct {
	Subject, MessageID, InReplyTo string
	From                          OLMAddr
	To, Cc, Bcc                   []OLMAddr
	Body, HTML                    string
	Sent, Received                string // e.g. "2019-03-05T10:00:00"
	Unread, Flagged               bool
	Priority                      string // e.g. "1" (high), "3"
	Categories                    []string
	Extra                         [][2]string // additional scalar elements
	Attachments                   []OLMAtt
}

// OLMMessageXML renders m as a message document. Attachment URLs are
// "<attDir>/<n>-<name>" unless set; the returned entries hold attachment
// data to add to the archive.
func OLMMessageXML(m OLMMsg, attDir string) ([]byte, []OLMEntry) {
	var b bytes.Buffer
	esc := func(s string) { xml.EscapeText(&b, []byte(s)) }
	el := func(name, text string) {
		b.WriteString("<" + name + ">")
		esc(text)
		b.WriteString("</" + name + ">\n")
	}
	flag := func(v bool) string {
		if v {
			return "1"
		}
		return "0"
	}
	addrs := func(name string, as []OLMAddr) {
		if len(as) == 0 {
			return
		}
		b.WriteString("<" + name + ">\n")
		for _, a := range as {
			b.WriteString(`<emailAddress OPFContactEmailAddressAddress="`)
			esc(a.Email)
			b.WriteString(`" OPFContactEmailAddressName="`)
			esc(a.Name)
			b.WriteString(`" OPFContactEmailAddressType="1"/>` + "\n")
		}
		b.WriteString("</" + name + ">\n")
	}
	b.WriteString(`<?xml version="1.0" encoding="UTF-8" standalone="no"?>` + "\n<emails>\n<email>\n")
	el("OPFMessageCopySubject", m.Subject)
	if m.MessageID != "" {
		el("OPFMessageCopyMessageID", m.MessageID)
	}
	if m.InReplyTo != "" {
		el("OPFMessageCopyInReplyTo", m.InReplyTo)
	}
	addrs("OPFMessageCopyFromAddresses", []OLMAddr{m.From})
	addrs("OPFMessageCopyToAddresses", m.To)
	addrs("OPFMessageCopyCCAddresses", m.Cc)
	addrs("OPFMessageCopyBCCAddresses", m.Bcc)
	if m.Sent != "" {
		el("OPFMessageCopySentTime", m.Sent)
	}
	if m.Received != "" {
		el("OPFMessageCopyReceivedTime", m.Received)
	}
	el("OPFMessageGetIsRead", flag(!m.Unread))
	el("OPFMessageGetIsFlagged", flag(m.Flagged))
	if m.Priority != "" {
		el("OPFMessageGetPriority", m.Priority)
	}
	el("OPFMessageGetHasHTML", flag(m.HTML != ""))
	if m.Body != "" {
		el("OPFMessageCopyBody", m.Body)
	}
	if m.HTML != "" {
		el("OPFMessageCopyHTMLBody", m.HTML)
	}
	if len(m.Categories) > 0 {
		b.WriteString("<OPFMessageCopyCategories>\n")
		for _, c := range m.Categories {
			b.WriteString(`<category OPFCategoryName="`)
			esc(c)
			b.WriteString(`"/>` + "\n")
		}
		b.WriteString("</OPFMessageCopyCategories>\n")
	}
	for _, kv := range m.Extra {
		el(kv[0], kv[1])
	}
	var files []OLMEntry
	if len(m.Attachments) > 0 {
		b.WriteString("<OPFMessageCopyAttachmentList>\n")
		for i, a := range m.Attachments {
			url := a.URL
			if url == "" {
				url = attDir + "/" + strconv.Itoa(i) + "-" + a.Name
			}
			if !a.Missing {
				files = append(files, OLMEntry{Name: url, Data: a.Data})
			}
			b.WriteString(`<messageAttachment OPFAttachmentName="`)
			esc(a.Name)
			b.WriteString(`" OPFAttachmentContentType="`)
			esc(a.Type)
			b.WriteString(`" OPFAttachmentContentID="`)
			esc(a.CID)
			b.WriteString(`" OPFAttachmentURL="`)
			esc(url)
			b.WriteString(`" OPFAttachmentContentExtension="` + strings.TrimPrefix(filepath.Ext(a.Name), ".") + `"/>` + "\n")
		}
		b.WriteString("</OPFMessageCopyAttachmentList>\n")
	}
	b.WriteString("</email>\n</emails>\n")
	return b.Bytes(), files
}

// OLMMessageEntries renders m and returns its message entry at name plus
// its attachment entries.
func OLMMessageEntries(name string, m OLMMsg, attDir string) []OLMEntry {
	x, files := OLMMessageXML(m, attDir)
	return append([]OLMEntry{{Name: name, Data: x}}, files...)
}

// WriteOLM writes a zip archive with the given entries (in order, names
// used verbatim, so hostile names can be tested) and returns path.
func WriteOLM(t testing.TB, path string, entries ...OLMEntry) string {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, e := range entries {
		if e.DeclaredSize != 0 {
			var comp bytes.Buffer
			fw, _ := flate.NewWriter(&comp, flate.BestCompression)
			fw.Write(e.Data)
			fw.Close()
			w, err := zw.CreateRaw(&zip.FileHeader{Name: e.Name, Method: zip.Deflate, CRC32: crc32.ChecksumIEEE(e.Data),
				CompressedSize64: uint64(comp.Len()), UncompressedSize64: e.DeclaredSize})
			if err != nil {
				t.Fatal(err)
			}
			w.Write(comp.Bytes())
			continue
		}
		w, err := zw.Create(e.Name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write(e.Data); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}
