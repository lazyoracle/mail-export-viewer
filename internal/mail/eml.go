package mail

import (
	"bufio"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/quotedprintable"
	"net/mail"
	"strings"
	"time"
)

// structuralFields are top-level fields WriteEML replaces with its own MIME
// structure.
var structuralFields = map[string]bool{
	"mime-version": true, "content-type": true, "content-transfer-encoding": true,
	"content-disposition": true, "content-id": true, "content-length": true, "lines": true,
}

// WriteEML serialises m as an RFC 5322 / MIME message with CRLF line
// endings, for sources that do not store raw messages (PST, OLM) to
// implement Raw (.eml export). data(i) must return the bytes of
// m.Attachments[i]; it may be nil when there are no attachments.
//
// If m.Header is non-empty (e.g. from PST transport headers) its fields are
// written as they are, minus the MIME structural ones; otherwise the header
// is synthesised from m.Envelope. The body is text/plain, text/html or
// multipart/alternative (quoted-printable UTF-8), wrapped in
// multipart/mixed when there are attachments (base64; message/rfc822 parts
// are written unencoded). Inline parts keep their Content-ID, so cid:
// references in the HTML still resolve. Values are stripped of CR/LF, so no
// header can be injected.
func WriteEML(w io.Writer, m *Message, data func(index int) ([]byte, error)) error {
	if len(m.Attachments) > 0 && data == nil {
		return errors.New("mail: WriteEML: attachments without data")
	}
	bw := bufio.NewWriter(w)
	if len(m.Header) > 0 {
		for _, f := range m.Header {
			if structuralFields[strings.ToLower(f.Name)] {
				continue
			}
			if _, _, ok := splitField([]byte(f.Name + ":")); !ok {
				continue
			}
			writeField(bw, f.Name, oneLine(f.Value))
		}
	} else {
		writeEnvelope(bw, &m.Envelope)
	}
	writeField(bw, "MIME-Version", "1.0")
	if err := writeBody(bw, m, data); err != nil {
		return err
	}
	return bw.Flush()
}

func writeEnvelope(bw *bufio.Writer, e *Envelope) {
	if !e.Date.IsZero() {
		writeField(bw, "Date", e.Date.Format(time.RFC1123Z))
	}
	if e.From != (Address{}) {
		writeField(bw, "From", formatAddrList([]Address{e.From}))
	}
	for _, f := range []struct {
		name string
		as   []Address
	}{{"To", e.To}, {"Cc", e.Cc}, {"Bcc", e.Bcc}} {
		if len(f.as) > 0 {
			writeField(bw, f.name, formatAddrList(f.as))
		}
	}
	if e.Subject != "" {
		writeField(bw, "Subject", mime.QEncoding.Encode("utf-8", oneLine(e.Subject)))
	}
	if e.MessageID != "" {
		writeField(bw, "Message-ID", "<"+msgIDText(e.MessageID)+">")
	}
	if e.InReplyTo != "" {
		writeField(bw, "In-Reply-To", "<"+msgIDText(e.InReplyTo)+">")
	}
	if len(e.References) > 0 {
		refs := make([]string, len(e.References))
		for i, r := range e.References {
			refs[i] = "<" + msgIDText(r) + ">"
		}
		writeField(bw, "References", strings.Join(refs, " "))
	}
}

func formatAddrList(as []Address) string {
	parts := make([]string, 0, len(as))
	for _, a := range as {
		name, email := oneLine(a.Name), oneLine(a.Email)
		switch {
		case email != "":
			parts = append(parts, (&mail.Address{Name: name, Address: email}).String())
		case name != "":
			parts = append(parts, mime.QEncoding.Encode("utf-8", name))
		}
	}
	return strings.Join(parts, ", ")
}

func msgIDText(id string) string {
	return strings.Map(func(r rune) rune {
		if r == '<' || r == '>' || r <= ' ' {
			return -1
		}
		return r
	}, id)
}

// oneLine replaces CR and LF (and other control characters) with spaces.
func oneLine(s string) string {
	return strings.TrimSpace(strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, s))
}

// maxLine is RFC 5322's hard line length limit, without CRLF.
const maxLine = 998

// writeField writes "name: value", folding at spaces so that no line
// exceeds maxLine. A run without spaces longer than that cannot be folded
// without changing the value and is written as is.
func writeField(bw *bufio.Writer, name, value string) {
	line := name + ": " + value
	for len(line) > maxLine {
		cut := strings.LastIndexByte(line[:maxLine+1], ' ')
		if cut <= len(name)+1 {
			// No space within the limit: break at the next one, if any.
			next := strings.IndexByte(line[maxLine:], ' ')
			if next < 0 {
				break
			}
			cut = maxLine + next
		}
		bw.WriteString(line[:cut])
		bw.WriteString("\r\n")
		line = line[cut:] // continuation starts with the space
	}
	bw.WriteString(line)
	bw.WriteString("\r\n")
}

func newBoundary() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err) // crypto/rand never fails on supported platforms
	}
	return "mev-" + hex.EncodeToString(b[:])
}

func writeBody(bw *bufio.Writer, m *Message, data func(int) ([]byte, error)) error {
	if len(m.Attachments) == 0 {
		writeTextBody(bw, m)
		return nil
	}
	b := newBoundary()
	writeField(bw, "Content-Type", mime.FormatMediaType("multipart/mixed", map[string]string{"boundary": b}))
	bw.WriteString("\r\n")
	bw.WriteString("--" + b + "\r\n")
	writeTextBody(bw, m)
	for i, a := range m.Attachments {
		d, err := data(i)
		if err != nil {
			return fmt.Errorf("mail: attachment %d: %w", i, err)
		}
		bw.WriteString("\r\n--" + b + "\r\n")
		writeAttachment(bw, a, d)
	}
	bw.WriteString("\r\n--" + b + "--\r\n")
	return nil
}

// writeTextBody writes the body entity (headers and content) for m's text
// and HTML.
func writeTextBody(bw *bufio.Writer, m *Message) {
	switch {
	case m.Text != "" && m.HTML != "":
		b := newBoundary()
		writeField(bw, "Content-Type", mime.FormatMediaType("multipart/alternative", map[string]string{"boundary": b}))
		bw.WriteString("\r\n--" + b + "\r\n")
		writeTextPart(bw, "text/plain", m.Text)
		bw.WriteString("\r\n--" + b + "\r\n")
		writeTextPart(bw, "text/html", m.HTML)
		bw.WriteString("\r\n--" + b + "--\r\n")
	case m.HTML != "":
		writeTextPart(bw, "text/html", m.HTML)
	default:
		writeTextPart(bw, "text/plain", m.Text)
	}
}

func writeTextPart(bw *bufio.Writer, typ, s string) {
	writeField(bw, "Content-Type", typ+"; charset=utf-8")
	writeField(bw, "Content-Transfer-Encoding", "quoted-printable")
	bw.WriteString("\r\n")
	qw := quotedprintable.NewWriter(bw)
	qw.Write([]byte(s))
	qw.Close()
}

func writeAttachment(bw *bufio.Writer, a Attachment, d []byte) {
	name := sanitizeFilename(a.Filename)
	ct := strings.ToLower(oneLine(a.ContentType))
	params := map[string]string{}
	if name != "" {
		params["name"] = name
	}
	ctv := mime.FormatMediaType(ct, params)
	if ctv == "" {
		ct = "application/octet-stream"
		ctv = mime.FormatMediaType(ct, params)
	}
	writeField(bw, "Content-Type", ctv)
	disp := "attachment"
	if a.Inline {
		disp = "inline"
	}
	dparams := map[string]string{}
	if name != "" {
		dparams["filename"] = name
	}
	writeField(bw, "Content-Disposition", mime.FormatMediaType(disp, dparams))
	if cid := msgIDText(a.ContentID); cid != "" {
		writeField(bw, "Content-ID", "<"+cid+">")
	}
	if ct == "message/rfc822" || ct == "message/global" {
		writeField(bw, "Content-Transfer-Encoding", "8bit")
		bw.WriteString("\r\n")
		bw.Write(d)
		return
	}
	writeField(bw, "Content-Transfer-Encoding", "base64")
	bw.WriteString("\r\n")
	enc := base64.StdEncoding.EncodeToString(d)
	for len(enc) > 76 {
		bw.WriteString(enc[:76])
		bw.WriteString("\r\n")
		enc = enc[76:]
	}
	bw.WriteString(enc)
}
