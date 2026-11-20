package pst

import (
	"strings"
	"time"

	"github.com/lazyoracle/mail-export-viewer/internal/source/pst/pstgen"
)

var fixtureDate = time.Date(2023, 5, 6, 7, 8, 9, 0, time.UTC)

const fixtureHeaders = "Received: from mx.example.com\r\n by mx.example.org; Sat, 6 May 2023 07:08:09 +0000\r\n" +
	"From: Alice Example <alice@example.com>\r\nTo: Bob Example <bob@example.org>\r\nSubject: RE: Hello\r\n" +
	"Message-ID: <m1@example.com>\r\nX-Custom: yes\r\nContent-Type: text/plain\r\n"

const fixtureRTFHTML = `{\rtf1\ansi\ansicpg1252\fromhtml1 \deff0{\fonttbl{\f0\fswiss Arial;}}` +
	`{\*\htmltag19 <html>}{\*\htmltag50 <body>}\htmlrtf {\htmlrtf0 {\*\htmltag64 <p>}Rich caf\'e9{\*\htmltag72 </p>}` +
	`\htmlrtf }\htmlrtf0 {\*\htmltag58 </body>}{\*\htmltag27 </html>}}`

// fixture builds the synthetic PST used by the messaging and source tests:
//
//	Inbox        m1 (plain, headers, categories, 3 recipients, 3 attachments
//	             incl. inline image and embedded message), m2 (HTML only,
//	             String8 subject, unread/flagged/important/replied), a contact
//	Inbox/Sub    m3 (RTF from HTML, draft), m4 (RTF plain text)
//	Sent Items   m5 (3 MiB body in an XBLOCK data tree)
//	Empty        no messages
func fixture(crypt byte) *pstgen.File {
	inner := &pstgen.Message{
		Props: []pstgen.Prop{pstgen.Str(0x0037, "Inner note"), pstgen.Str(0x1000, "inner body"),
			pstgen.Str(0x0C1A, "Erin Example"), pstgen.Str(0x0C1F, "erin@example.com"), pstgen.Str(0x0C1E, "SMTP")},
		Attachments: []*pstgen.Attachment{{Props: []pstgen.Prop{pstgen.Str(0x3707, "inner.txt"), pstgen.Str(0x370E, "text/plain")}, Data: []byte("inner attachment")}},
	}
	m1 := &pstgen.Message{
		Props: []pstgen.Prop{
			pstgen.Str(0x0037, "\x01\x04RE: Hello"),
			pstgen.Str(0x0042, "Alice Example"), pstgen.Str(0x0065, "alice@example.com"), pstgen.Str(0x0064, "SMTP"),
			pstgen.Time(0x0039, fixtureDate),
			pstgen.Str(0x1035, "<m1@example.com>"),
			pstgen.Str(0x1042, "<m0@example.com>"),
			pstgen.Str(0x1000, "Hello Bob\r\nSecond line"),
			pstgen.I32(0x0E07, 0x1|0x10),
			pstgen.Str(0x007D, fixtureHeaders),
		},
		Categories: []string{"Red Category", "Project X", "Red Category", ""},
		Recipients: []pstgen.Recipient{
			{Type: pstgen.To, Name: "Bob Example", Email: "bob@example.org", AddrType: "SMTP"},
			{Type: pstgen.Cc, Name: "Carol Example", Email: "/O=EXAMPLE/OU=SITE/CN=RECIPIENTS/CN=CAROL", AddrType: "EX", SMTP: "carol@example.com"},
			{Type: pstgen.Bcc, Name: "Dave Example", Email: "dave@example.net", AddrType: "SMTP"},
		},
		Attachments: []*pstgen.Attachment{
			{Props: []pstgen.Prop{pstgen.Str(0x3704, "QUARTE~1.PDF"), pstgen.Str(0x3707, "Quarterly report.pdf"), pstgen.Str(0x370E, "application/pdf")}, Data: blob(20000, 5)},
			{Props: []pstgen.Prop{pstgen.Str(0x3707, "logo.png"), pstgen.Str(0x370E, "image/png"), pstgen.Str(0x3712, "<img1@example.com>")}, Data: []byte("\x89PNG fake")},
			{Props: []pstgen.Prop{pstgen.Str(0x3001, "Inner note")}, Embedded: inner},
		},
	}
	m2 := &pstgen.Message{Props: []pstgen.Prop{
		pstgen.Str8(0x0037, []byte("Caf\xe9 menu")),
		pstgen.I32(0x3FFD, 1252), pstgen.I32(0x3FDE, 1252),
		pstgen.Str(0x0C1A, "Bob Example"), pstgen.Str(0x0C1F, "bob@example.org"), pstgen.Str(0x0C1E, "SMTP"),
		pstgen.Time(0x0E06, fixtureDate.Add(time.Hour)),
		pstgen.Bin(0x1013, []byte("<html><body><p>Caf\xe9 au lait</p></body></html>")),
		pstgen.I32(0x0E07, 0),
		pstgen.I32(0x0017, 2),
		pstgen.I32(0x1090, 2),
		pstgen.I32(0x1081, 102),
		pstgen.Str(0x1035, "m2@example.com"),
	}}
	contact := &pstgen.Message{Props: []pstgen.Prop{pstgen.Str(0x001A, "IPM.Contact"), pstgen.Str(0x0037, "Frank Example")}}
	m3 := &pstgen.Message{Props: []pstgen.Prop{
		pstgen.Str(0x0037, "Rich draft"),
		pstgen.Bin(0x1009, pstgen.CompressRTF([]byte(fixtureRTFHTML))),
		pstgen.I32(0x0E07, 0x1|0x8),
		pstgen.I32(0x0017, 0),
	}}
	m4 := &pstgen.Message{Props: []pstgen.Prop{
		pstgen.Str(0x001A, "IPM.Note.SMIME.MultipartSigned"),
		pstgen.Str(0x0037, "Plain RTF"),
		pstgen.Bin(0x1009, pstgen.CompressRTF([]byte(`{\rtf1\ansi Plain \b text\b0\par}`))),
	}}
	m5 := &pstgen.Message{Props: []pstgen.Prop{
		pstgen.Str(0x0037, "Big body"),
		pstgen.Str(0x1000, strings.Repeat("big body line\r\n", 3<<20/15)),
		pstgen.Str(0x1035, "<m5@example.com>"),
	}}
	return &pstgen.File{Crypt: crypt, StoreName: "Personal Folders", Folders: []*pstgen.Folder{
		{Name: "Inbox", Messages: []*pstgen.Message{m1, m2, contact}, Folders: []*pstgen.Folder{
			{Name: "Sub", Messages: []*pstgen.Message{m3, m4}},
		}},
		{Name: "Sent Items", Messages: []*pstgen.Message{m5}},
		{Name: "Empty"},
	}}
}
