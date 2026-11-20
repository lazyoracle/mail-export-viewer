package corpus

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
	"time"

	"golang.org/x/text/encoding"
	"golang.org/x/text/encoding/charmap"
	"golang.org/x/text/encoding/japanese"
	"golang.org/x/text/encoding/korean"
	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/encoding/traditionalchinese"
	"golang.org/x/text/encoding/unicode"

	"github.com/lazyoracle/mail-export-viewer/internal/source"
	"github.com/lazyoracle/mail-export-viewer/internal/source/pst/pstgen"
	"github.com/lazyoracle/mail-export-viewer/internal/testutil"
)

// legacy charsets: label, encoder, subject, a search word of the body
// (a whole word for alphabetic scripts; for CJK a substring of the subject).
var charsets = []struct {
	label   string
	enc     encoding.Encoding
	subject string
	word    string
	cte     string
}{
	{"iso-8859-1", charmap.ISO8859_1, "Café crème brûlée", "brûlée", "quoted-printable"},
	{"iso-8859-2", charmap.ISO8859_2, "Zażółć gęślą jaźń", "gęślą", "8bit"},
	{"iso-8859-5", charmap.ISO8859_5, "Быстрая лиса ёж", "лиса", "base64"},
	{"iso-8859-7", charmap.ISO8859_7, "Καλημέρα κόσμε", "κόσμε", "8bit"},
	{"iso-8859-15", charmap.ISO8859_15, "Prix 100 € œuvre", "œuvre", "quoted-printable"},
	{"windows-1250", charmap.Windows1250, "Příliš žluťoučký kůň", "žluťoučký", "8bit"},
	{"windows-1251", charmap.Windows1251, "Привет мир", "Привет", "base64"},
	{"windows-1252", charmap.Windows1252, "“Smart quotes” – €5 naïve", "naïve", "quoted-printable"},
	{"koi8-r", charmap.KOI8R, "Съешь ещё этих мягких", "мягких", "8bit"},
	{"shift_jis", japanese.ShiftJIS, "会議の議事録", "議事録", "base64"},
	{"iso-2022-jp", japanese.ISO2022JP, "日本語の件名テスト", "件名", "7bit"},
	{"euc-jp", japanese.EUCJP, "東京タワー見学", "見学", "8bit"},
	{"gbk", simplifiedchinese.GBK, "项目进度报告", "进度", "base64"},
	{"big5", traditionalchinese.Big5, "會議記錄整理", "記錄", "8bit"},
	{"euc-kr", korean.EUCKR, "안녕하세요 회의", "회의", "base64"},
	{"utf-16", unicode.UTF16(unicode.LittleEndian, unicode.UseBOM), "UTF-16 body ok", "", "base64"},
}

func encodeTo(enc encoding.Encoding, s string) []byte {
	b, err := enc.NewEncoder().Bytes([]byte(s))
	if err != nil {
		panic(fmt.Sprintf("corpus: encode %q: %v", s, err))
	}
	return b
}

// qp is a minimal quoted-printable encoder (no soft line breaks needed for
// the short bodies used here).
func qp(b []byte) []byte {
	var out bytes.Buffer
	for _, c := range b {
		if c == '\n' || (c >= 33 && c <= 126 && c != '=') || c == ' ' {
			out.WriteByte(c)
		} else {
			fmt.Fprintf(&out, "=%02X", c)
		}
	}
	return out.Bytes()
}

// messy writes the hostile and broken input set.
func (g *gen) messy() error {
	if err := g.messyMbox(); err != nil {
		return err
	}
	if err := g.messyThreads(); err != nil {
		return err
	}
	if err := g.messyLabels(); err != nil {
		return err
	}
	if err := g.messyNames(); err != nil {
		return err
	}
	if err := g.messyFiles(); err != nil {
		return err
	}
	return g.messyOutlook()
}

// add a message of the messy mbox file with the usual expectations.
type messyAdder struct {
	g      *gen
	f      *mboxFile
	rel    string
	folder string
}

func (a *messyAdder) add(envDate time.Time, raw []byte, m Msg) {
	a.f.add("someone@example.com", envDate, raw)
	m.Source, m.Kind, m.Folder = a.rel, source.Mbox, a.folder
	a.g.add(m)
}

func (g *gen) messyMbox() error {
	a := &messyAdder{g: g, f: &mboxFile{}, rel: "messy/messy.mbox", folder: "messy/messy"}
	me := person{"Pat Example", "pat@example.com"}
	// Legacy charsets.
	for i, cs := range charsets {
		d := g.date()
		tok := g.token()
		id := fmt.Sprintf("cs%d@example.com", i)
		body := cs.subject + "\n" + cs.word + " " + tok + "\n"
		subj := "=?" + cs.label + "?B?" + base64.StdEncoding.EncodeToString(encodeTo(cs.enc, cs.subject)) + "?="
		if cs.label == "utf-16" {
			subj = cs.subject
		} else if cs.label == "iso-8859-1" {
			subj = "=?ISO-8859-1?Q?" + strings.ReplaceAll(string(qp(encodeTo(cs.enc, cs.subject))), " ", "_") + "?="
		}
		h := envelope(people[i%len(people)], []person{me}, subj, d, id)
		raw := encodeTo(cs.enc, body)
		switch cs.cte {
		case "base64":
			raw = b64(raw)
		case "quoted-printable":
			raw = qp(raw)
		}
		m := &msg{headers: h, ctype: "text/plain; charset=\"" + cs.label + "\"", cte: cs.cte, raw: raw}
		a.add(d, m.bytes(), Msg{Subject: cs.subject, MessageID: id, From: people[i%len(people)].Email, Date: d, Token: tok,
			Exact: true, TextHas: []string{cs.subject}, Note: "charset " + cs.label})
		if cs.word != "" {
			g.m.Searches = append(g.m.Searches, Search{Q: cs.word, Subjects: []string{cs.subject}})
		}
	}

	// RFC 2047 subjects: raw header value -> expected decoded subject.
	rfc2047 := []struct{ raw, want string }{
		{"=?UTF-8?Q?a?= =?UTF-8?Q?b?=", "ab"},
		{"=?ISO-8859-1?Q?Caf=E9_au_lait?=", "Café au lait"},
		{"Re: =?utf-8?b?w6l0w6k=?= plain", "Re: été plain"},
		{"=?UTF-8?Q?unterminated", "=?UTF-8?Q?unterminated"},
		{"=?UTF-8?Q?long_?=\n =?UTF-8?Q?folded_subject?=", "long folded subject"},
		{"=?UTF-8*en?Q?with_language?=", "with language"},
		{"Raw UTF-8 Größe ✓", "Raw UTF-8 Größe ✓"},
		{"Raw latin1 Gr\xf6\xdfe", "Raw latin1 Größe"},
		{"=?utf-8?B?8J+RqeKAjfCfkrsgUmVsZWFzZSDwn5qA?=", "👩‍💻 Release 🚀"},
	}
	for i, c := range rfc2047 {
		d := g.date()
		tok := g.token()
		id := fmt.Sprintf("rfc2047-%d@example.com", i)
		h := envelope(me, []person{people[1]}, c.raw, d, id)
		m := &msg{headers: h, body: "subject test " + tok}
		a.add(d, m.bytes(), Msg{Subject: c.want, MessageID: id, From: me.Email, Date: d, Token: tok, Exact: true, Note: "rfc2047"})
	}
	// Encoded display names.
	{
		d := g.date()
		tok := g.token()
		h := envelope(me, []person{people[2]}, "Encoded sender", d, "fromname@example.com")
		h[0][1] = "=?UTF-8?Q?Doe=2C_J=C3=B6hn?= <john.doe@example.com>"
		a.add(d, (&msg{headers: h, body: tok}).bytes(), Msg{Subject: "Encoded sender", MessageID: "fromname@example.com",
			From: "john.doe@example.com", FromName: "Doe, Jöhn", Date: d, Token: tok, Exact: true, Note: "encoded from"})
		d = g.date()
		tok = g.token()
		h = envelope(me, []person{people[2]}, "RTL sender", d, "rtl@example.com")
		h[0][1] = "=?UTF-8?B?" + base64.StdEncoding.EncodeToString([]byte("‏مريم أحمد‏")) + "?= <maryam@example.org>"
		a.add(d, (&msg{headers: h, body: tok}).bytes(), Msg{Subject: "RTL sender", MessageID: "rtl@example.com",
			From: "maryam@example.org", FromName: "‏مريم أحمد‏", Date: d, Token: tok, Exact: true, Note: "rtl from"})
	}

	// RFC 2231 / 2047 attachment names.
	{
		d := g.date()
		tok := g.token()
		h := envelope(me, []person{people[3]}, "Attachment names", d, "attnames@example.com")
		mk := func(hdrs ...string) mpart {
			p := mpart{body: b64([]byte("data")), headers: [][2]string{{"Content-Transfer-Encoding", "base64"}}}
			for i := 0; i+1 < len(hdrs); i += 2 {
				p.headers = append(p.headers, [2]string{hdrs[i], hdrs[i+1]})
			}
			return p
		}
		m := &msg{headers: h, parts: []mpart{
			textPart("names " + tok),
			mk("Content-Type", "application/pdf", "Content-Disposition", "attachment; filename*=UTF-8''%E2%82%AC%20rates.pdf"),
			mk("Content-Type", "application/vnd.ms-excel", "Content-Disposition", "attachment; filename*0*=UTF-8''Gr%C3%B6%C3%9Fe;\n filename*1*=%20Tabelle.xls"),
			mk("Content-Type", "application/msword; name=\"=?UTF-8?B?UsOpc3Vtw6kuZG9j?=\"", "Content-Disposition", "attachment"),
			mk("Content-Type", "text/plain", "Content-Disposition", "attachment; filename*=iso-8859-1'fr'r%E9sum%E9.txt"),
		}}
		a.add(d, m.bytes(), Msg{Subject: "Attachment names", MessageID: "attnames@example.com", From: me.Email, Date: d, Token: tok,
			Atts: []string{"€ rates.pdf", "Größe Tabelle.xls", "Résumé.doc", "résumé.txt"}, Flags: source.HasAttachment, Exact: true, Note: "rfc2231"})
	}

	// Broken and odd dates. want zero = the envelope (From_ line) date.
	dates := []struct {
		hdr  string
		want time.Time
		note string
	}{
		{"garbage date", time.Time{}, "garbage date"},
		{"", time.Time{}, "no Date header"},
		{"Fri, 1 Jan 99 10:00:00 GMT", time.Date(1999, 1, 1, 10, 0, 0, 0, time.UTC), "two-digit year"},
		{"Tue, 15 Mar 2005 10:00:00 EST", time.Date(2005, 3, 15, 15, 0, 0, 0, time.UTC), "obsolete zone"},
		{"Fri, 1 Jan 2100 00:00:00 +0000", time.Date(2100, 1, 1, 0, 0, 0, 0, time.UTC), "far future"},
		{"Thu, 1 Jan 1970 00:00:00 +0000", time.Date(1970, 1, 1, 0, 0, 0, 0, time.UTC), "epoch"},
		{"  Mon,   3 Jun 2013   07:08:09   +0200 (CEST)  ", time.Date(2013, 6, 3, 5, 8, 9, 0, time.UTC), "comment and spaces"},
	}
	for i, c := range dates {
		env := g.date()
		tok := g.token()
		id := fmt.Sprintf("date%d@example.com", i)
		subject := "Date case " + strconv.Itoa(i) + ": " + c.note
		h := envelope(me, []person{people[4]}, subject, env, id)
		if c.hdr == "" {
			h = append(h[:3], h[4:]...)
		} else {
			h[3][1] = c.hdr
		}
		want := c.want
		if want.IsZero() {
			want = env
		}
		a.add(env, (&msg{headers: h, body: tok}).bytes(), Msg{Subject: subject, MessageID: id, From: me.Email, Date: want,
			Token: tok, Exact: true, Note: c.note})
	}

	// Broken headers.
	{
		d := g.date()
		tok := g.token()
		raw := "From: Pat Example <pat@example.com>\nThis line has no colon\nSubject: First subject wins\nSubject: Second subject\n" +
			"X-Null: a\x00b\nTo: " + strings.Repeat("list-member@example.com, ", 4000) + "last@example.com\n" +
			"Date: " + d.Format(time.RFC1123Z) + "\nMessage-ID: <broken-headers@example.com>\n\nbody " + tok + "\n"
		a.add(d, []byte(raw), Msg{MessageID: "broken-headers@example.com", From: "pat@example.com", Token: tok, Note: "broken headers"})
		d = g.date()
		tok = g.token()
		raw = "Subject: No sender and no id\nDate: " + d.Format(time.RFC1123Z) + "\n\nbody " + tok + "\n"
		a.add(d, []byte(raw), Msg{Subject: "No sender and no id", Date: d, Token: tok, Exact: true, Note: "missing From and Message-ID"})
		d = g.date()
		tok = g.token()
		raw = "From: pat@example.com\nDate: " + d.Format(time.RFC1123Z) + "\nMessage-ID: <nosubject@example.com>\n\nbody " + tok + "\n"
		a.add(d, []byte(raw), Msg{Subject: "", MessageID: "nosubject@example.com", From: "pat@example.com", Date: d, Token: tok, Exact: true, Note: "no subject"})
		// Headers only, no body, no blank line.
		d = g.date()
		raw = "From: pat@example.com\nSubject: Headers only\nDate: " + d.Format(time.RFC1123Z) + "\nMessage-ID: <headersonly@example.com>\n"
		a.add(d, []byte(raw), Msg{Subject: "Headers only", MessageID: "headersonly@example.com", From: "pat@example.com", Date: d, Exact: true, Note: "headers only"})
		// Mixed CRLF and LF.
		d = g.date()
		tok = g.token()
		raw = "From: pat@example.com\r\nSubject: Mixed line endings\nDate: " + d.Format(time.RFC1123Z) + "\r\nMessage-ID: <mixed@example.com>\n\r\nline one\r\nline two " + tok + "\n"
		a.add(d, []byte(raw), Msg{Subject: "Mixed line endings", MessageID: "mixed@example.com", From: "pat@example.com", Date: d, Token: tok, Exact: true, Note: "mixed CRLF"})
	}

	// mboxrd quoting round trip.
	{
		d := g.date()
		tok := g.token()
		body := "From the start of a line\n>From one quote\n>>From two quotes\nFrom: not a header\n" + tok + "\n"
		h := envelope(me, []person{people[5]}, "Quoted From lines", d, "quoting@example.com")
		a.add(d, (&msg{headers: h, body: body}).bytes(), Msg{Subject: "Quoted From lines", MessageID: "quoting@example.com", From: me.Email,
			Date: d, Token: tok, Exact: true, RawHas: []string{"\nFrom the start of a line\n>From one quote\n>>From two quotes\n"}, Note: "mboxrd"})
	}

	// Nested multipart: mixed(related(alternative(text, html), image), pdf, message/rfc822(with attachment)).
	{
		d := g.date()
		tok := g.token()
		inner := "From: inner@example.org\nSubject: Inner message\nMessage-ID: <inner@example.org>\nMIME-Version: 1.0\n" +
			"Content-Type: multipart/mixed; boundary=in\n\n--in\nContent-Type: text/plain\n\ninner body\n--in\n" +
			"Content-Type: text/plain; name=inner.txt\nContent-Disposition: attachment; filename=inner.txt\n\ninner file\n--in--\n"
		raw := "From: Pat Example <pat@example.com>\nTo: Bob Sample <bob@example.org>\nSubject: Nested multipart\n" +
			"Date: " + d.Format(time.RFC1123Z) + "\nMessage-ID: <nested@example.com>\nMIME-Version: 1.0\n" +
			"Content-Type: multipart/mixed; boundary=\"mix\"\n\n--mix\nContent-Type: multipart/related; boundary=\"rel\"; type=\"multipart/alternative\"\n\n" +
			"--rel\nContent-Type: multipart/alternative; boundary=\"alt\"\n\n--alt\nContent-Type: text/plain; charset=utf-8\n\nplain part " + tok + "\n" +
			"--alt\nContent-Type: text/html; charset=utf-8\n\n<html><body><p>html part <img src=\"cid:logo@example.com\"></p></body></html>\n--alt--\n" +
			"--rel\nContent-Type: image/png\nContent-ID: <logo@example.com>\nContent-Transfer-Encoding: base64\n\n" + string(b64(pngBytes)) +
			"--rel--\n--mix\nContent-Type: application/pdf; name=\"report.pdf\"\nContent-Disposition: attachment; filename=\"report.pdf\"\nContent-Transfer-Encoding: base64\n\n" +
			string(b64(blob(2000, 7))) + "--mix\nContent-Type: message/rfc822\nContent-Disposition: attachment\n\n" + inner + "\n--mix--\n"
		a.add(d, []byte(raw), Msg{Subject: "Nested multipart", MessageID: "nested@example.com", From: "pat@example.com", Date: d, Token: tok,
			Flags: source.HasAttachment, Exact: true, TextHas: []string{"plain part"}, Note: "nested multipart"})
	}

	// Duplicate Message-IDs: three different messages share one id.
	for i := 0; i < 3; i++ {
		d := g.date()
		tok := g.token()
		subject := fmt.Sprintf("Duplicate id copy %d", i)
		h := envelope(me, []person{people[6]}, subject, d, "dup@example.com")
		a.add(d, (&msg{headers: h, body: tok}).bytes(), Msg{Subject: subject, MessageID: "dup@example.com", From: me.Email, Date: d,
			Token: tok, Thread: "dup", Exact: true, Note: "duplicate Message-ID"})
	}

	// Big attachment.
	{
		d := g.date()
		data := blob(g.o.BigBytes, 99)
		sum := sha256.Sum256(data)
		g.m.BigAttSHA256 = hex.EncodeToString(sum[:])
		h := envelope(me, []person{people[7]}, "Big attachment", d, "bigatt@example.com")
		m := &msg{headers: h, parts: []mpart{textPart("see attached " + g.m.BigToken), attPart("big.bin", "application/octet-stream", data)}}
		a.add(d, m.bytes(), Msg{Subject: "Big attachment", MessageID: "bigatt@example.com", From: me.Email, Date: d,
			Atts: []string{"big.bin"}, Flags: source.HasAttachment, Exact: true, Note: "big attachment"})
	}
	// Huge single-line body (no newline at all), token at the start.
	{
		d := g.date()
		tok := g.token()
		var line strings.Builder
		line.WriteString(tok + " ")
		w := g.words(40)
		for line.Len() < 3*g.o.BigBytes {
			line.WriteString(strings.ReplaceAll(w, "\n", " "))
			line.WriteByte(' ')
		}
		h := envelope(me, []person{people[8]}, "Huge single line", d, "hugeline@example.com")
		a.add(d, (&msg{headers: h, body: line.String()}).bytes(), Msg{Subject: "Huge single line", MessageID: "hugeline@example.com",
			From: me.Email, Date: d, Token: tok, Exact: true, Slow: true, Note: "huge single-line body"})
	}
	// HTML bombs.
	bombs := []struct{ name, html string }{
		{"deep nesting", strings.Repeat("<div>", 200000) + "deep" + strings.Repeat("</div>", 200000)},
		{"huge attribute", "<p title=\"" + strings.Repeat("A", 5<<20) + "\">attr</p>"},
		{"many attributes", "<p " + strings.Repeat("data-x=\"1\" ", 100000) + ">attrs</p>"},
		{"unclosed tags", strings.Repeat("<b><i><table><tr><td>", 30000) + "unclosed"},
		{"script and handlers", "<script>alert(1)</script><img src=x onerror=alert(2)><a href=\"javascript:alert(3)\">x</a><iframe src=\"http://example.com/\"></iframe><p style=\"background:url(http://example.com/t.png)\">ok</p>"},
	}
	for i, b := range bombs {
		d := g.date()
		tok := g.token()
		subject := "HTML bomb: " + b.name
		h := envelope(me, []person{people[9]}, subject, d, fmt.Sprintf("bomb%d@example.com", i))
		raw := (&msg{headers: h, ctype: "multipart/alternative; boundary=b",
			raw: []byte("--b\nContent-Type: text/plain\n\n" + tok + "\n--b\nContent-Type: text/html\n\n" + b.html + "\n--b--\n")}).bytes()
		a.add(d, raw, Msg{Subject: subject, MessageID: fmt.Sprintf("bomb%d@example.com", i), From: me.Email, Date: d, Token: tok,
			Exact: true, Slow: true, Note: "html bomb"})
	}
	// Binary garbage inside a message body (no line can look like a From_ line).
	{
		d := g.date()
		tok := g.token()
		garbage := blob(64<<10, 5)
		garbage = bytes.ReplaceAll(garbage, []byte("From "), []byte("Frxm "))
		h := envelope(me, []person{people[0]}, "Binary garbage body", d, "garbage@example.com")
		raw := append((&msg{headers: h, body: "before " + tok + "\n"}).bytes(), garbage...)
		a.add(d, raw, Msg{Subject: "Binary garbage body", MessageID: "garbage@example.com", From: me.Email, Date: d, Exact: true, Note: "binary garbage"})
	}
	// Truncated last message: cut inside a base64 attachment, no closing boundary.
	{
		d := g.date()
		tok := g.token()
		h := envelope(me, []person{people[1]}, "Truncated message", d, "truncated@example.com")
		raw := (&msg{headers: h, parts: []mpart{textPart("start " + tok), attPart("cut.bin", "application/octet-stream", blob(20000, 3))}}).bytes()
		raw = raw[:len(raw)*2/3]
		a.add(d, raw, Msg{Subject: "Truncated message", MessageID: "truncated@example.com", From: me.Email, Date: d, Token: tok,
			Flags: source.HasAttachment, Exact: true, Note: "truncated (the cut attachment part is still listed)"})
	}
	g.source(a.rel, source.Mbox, false)
	return g.write(a.rel, a.f.b.Bytes())
}

// messyThreads writes a 300-deep reply chain and a star thread of a root
// with 120 direct replies.
func (g *gen) messyThreads() error {
	a := &messyAdder{g: g, f: &mboxFile{}, rel: "messy/threads.mbox", folder: "messy/threads"}
	const deep = 300
	var refs []string
	for i := 0; i < deep; i++ {
		d := g.date()
		id := fmt.Sprintf("deep%03d@example.com", i)
		subject := "Deep chain"
		if i > 0 {
			subject = "Re: Deep chain"
		}
		from := people[i%len(people)]
		h := envelope(from, []person{people[(i+1)%len(people)]}, subject, d, id)
		if i > 0 {
			r := refs
			if len(r) > 20 { // clients trim References: first + last 19
				r = append([]string{refs[0]}, refs[len(refs)-19:]...)
			}
			h = append(h, [2]string{"In-Reply-To", "<" + refs[len(refs)-1] + ">"}, [2]string{"References", "<" + strings.Join(r, ">\n <") + ">"})
		}
		refs = append(refs, id)
		a.add(d, (&msg{headers: h, body: g.words(8)}).bytes(), Msg{Subject: subject, MessageID: id, From: from.Email, Date: d,
			Thread: "deep", Exact: true, Note: "deep chain"})
	}
	g.m.LongThread, g.m.LongThreadLen = "deep", deep
	root := "star-root@example.com"
	for i := 0; i <= 120; i++ {
		d := g.date()
		id := root
		subject := "Star topic"
		h := envelope(people[i%len(people)], []person{people[0]}, subject, d, "")
		if i > 0 {
			id = fmt.Sprintf("star%03d@example.com", i)
			subject = fmt.Sprintf("Re: Star topic (%d)", i)
			h[2][1] = subject
			h = append(h, [2]string{"In-Reply-To", "<" + root + ">"}, [2]string{"References", "<" + root + ">"})
		}
		h = append(h, [2]string{"Message-ID", "<" + id + ">"})
		a.add(d, (&msg{headers: h, body: g.words(6)}).bytes(), Msg{Subject: subject, MessageID: id, From: people[i%len(people)].Email,
			Date: d, Thread: "star", Exact: true, Note: "star thread"})
	}
	g.source(a.rel, source.Mbox, false)
	return g.write(a.rel, a.f.b.Bytes())
}

// labelName returns a varied label name: unicode, emoji, RTL, quotes,
// commas, slashes.
func labelName(i int) string {
	switch i % 8 {
	case 0:
		return fmt.Sprintf("Label %04d", i)
	case 1:
		return fmt.Sprintf("Étiquette %04d", i)
	case 2:
		return fmt.Sprintf("📁 Emoji %04d", i)
	case 3:
		return fmt.Sprintf("تسمية %04d", i)
	case 4:
		return fmt.Sprintf("Quote \"%04d\"", i)
	case 5:
		return fmt.Sprintf("Comma, %04d", i)
	case 6:
		return fmt.Sprintf("Nested/Path/%04d", i)
	default:
		return fmt.Sprintf("<b>tag %04d</b>", i)
	}
}

// labelHeader encodes labels as X-Gmail-Labels: plain where possible,
// quoted when holding a comma or quote (quotes encoded as RFC 2047).
func labelHeader(labels []string) string {
	var parts []string
	for _, l := range labels {
		switch {
		case strings.ContainsAny(l, "\""):
			parts = append(parts, "=?UTF-8?B?"+base64.StdEncoding.EncodeToString([]byte(l))+"?=")
		case strings.ContainsAny(l, ","):
			parts = append(parts, "\""+l+"\"")
		default:
			parts = append(parts, l)
		}
	}
	// Fold long values.
	var b strings.Builder
	line := 0
	for i, p := range parts {
		if i > 0 {
			b.WriteByte(',')
			line++
			if line > 70 {
				b.WriteString("\n ")
				line = 1
			}
		}
		b.WriteString(p)
		line += len(p)
	}
	return b.String()
}

// messyLabels writes 300 messages with 10 labels each from a pool of
// 3000 (each label used once), and one message with 1000 labels.
func (g *gen) messyLabels() error {
	a := &messyAdder{g: g, f: &mboxFile{}, rel: "messy/labels.mbox", folder: "messy/labels"}
	const n, per = 300, 10
	for i := 0; i < n; i++ {
		d := g.date()
		tok := g.token()
		var ls []string
		for j := 0; j < per; j++ {
			ls = append(ls, labelName(i*per+j))
		}
		subject := fmt.Sprintf("Labelled message %03d", i)
		h := envelope(people[i%len(people)], []person{people[0]}, subject, d, fmt.Sprintf("lab%03d@example.com", i))
		h = append(h, [2]string{"X-Gmail-Labels", labelHeader(ls)})
		a.add(d, (&msg{headers: h, body: tok}).bytes(), Msg{Subject: subject, MessageID: fmt.Sprintf("lab%03d@example.com", i),
			From: people[i%len(people)].Email, Date: d, Labels: ls, Token: tok, Exact: true, Note: "many labels"})
	}
	g.m.ManyLabels = n * per
	d := g.date()
	var ls []string
	for j := 0; j < 1000; j++ {
		ls = append(ls, fmt.Sprintf("Over %04d", j))
	}
	h := envelope(people[1], []person{people[0]}, "One thousand labels", d, "lab1000@example.com")
	h = append(h, [2]string{"X-Gmail-Labels", labelHeader(ls)})
	a.add(d, (&msg{headers: h, body: "many"}).bytes(), Msg{Subject: "One thousand labels", MessageID: "lab1000@example.com",
		From: people[1].Email, Date: d, Labels: ls[:256], Exact: true, Note: "labels over the per-message cap"})
	g.source(a.rel, source.Mbox, false)
	return g.write(a.rel, a.f.b.Bytes())
}

// messyNames writes mailboxes and attachments with hostile and unusual
// names.
func (g *gen) messyNames() error {
	names := []string{
		"messy/names/📁 Emoji Folder ✨",
		"messy/names/بريد وارد",
		"messy/names/\"quoted\" & <tag>",
		"messy/names/esc\x1b[31mred",
		"messy/names/new\nline",
		"messy/names/back\\slash",
		"messy/names/trailing space ",
		"messy/names/" + strings.Repeat("long", 50),
	}
	for i, n := range names {
		d := g.date()
		tok := g.token()
		subject := fmt.Sprintf("Hostile mailbox name %d", i)
		f := &mboxFile{}
		f.add("x@example.com", d, (&msg{headers: envelope(people[i%len(people)], []person{people[0]}, subject, d, fmt.Sprintf("name%d@example.com", i)),
			body: tok}).bytes())
		// The index turns control characters into spaces and trims
		// folder segments.
		folder := strings.TrimSpace(strings.ReplaceAll(n, "\n", " "))
		g.add(Msg{Source: n, Kind: source.Mbox, Folder: folder, Subject: subject, MessageID: fmt.Sprintf("name%d@example.com", i),
			From: people[i%len(people)].Email, Date: d, Token: tok, Exact: true, Note: "hostile mailbox name"})
		g.source(n, source.Mbox, false)
		if err := g.write(n, f.b.Bytes()); err != nil {
			return err
		}
	}
	// Hidden mailbox: skipped by discovery.
	f := &mboxFile{}
	f.add("x@example.com", g.date(), []byte("Subject: hidden\n\nhidden\n"))
	if err := g.write("messy/names/..hidden.mbox", f.b.Bytes()); err != nil {
		return err
	}
	g.m.Ignored = append(g.m.Ignored, "messy/names/..hidden.mbox")

	// Hostile attachment names.
	d := g.date()
	tok := g.token()
	atts := []string{
		"../../etc/passwd",
		"..\\..\\boot.ini",
		"\x1b]0;title\x07.txt",
		"‮fdp.exe",
		"<script>alert(1)</script>.html",
		"semi;colon\"quote.txt",
		strings.Repeat("n", 300) + ".txt",
	}
	m := &msg{headers: envelope(people[2], []person{people[0]}, "Hostile attachment names", d, "hostileatt@example.com"),
		parts: []mpart{textPart("hostile " + tok)}}
	for i, n := range atts {
		enc := "=?UTF-8?B?" + base64.StdEncoding.EncodeToString([]byte(n)) + "?="
		m.parts = append(m.parts, mpart{headers: [][2]string{
			{"Content-Type", "application/octet-stream; name=\"" + enc + "\""},
			{"Content-Disposition", "attachment; filename=\"" + enc + "\""},
			{"Content-Transfer-Encoding", "base64"},
		}, body: b64([]byte("hostile " + strconv.Itoa(i)))})
	}
	m.parts = append(m.parts, mpart{headers: [][2]string{
		{"Content-Type", "text/html; name=\"page.html\""},
		{"Content-Disposition", "attachment; filename=\"page.html\""},
	}, body: []byte("<script>alert(document.domain)</script>\n")})
	ff := &mboxFile{}
	ff.add("x@example.com", d, m.bytes())
	g.add(Msg{Source: "messy/names/attachments.mbox", Kind: source.Mbox, Folder: "messy/names/attachments", Subject: "Hostile attachment names",
		MessageID: "hostileatt@example.com", From: people[2].Email, Date: d, Token: tok, Flags: source.HasAttachment, Exact: true, Note: "hostile attachment names"})
	g.source("messy/names/attachments.mbox", source.Mbox, false)
	return g.write("messy/names/attachments.mbox", ff.b.Bytes())
}

// messyFiles writes files that must be ignored or survived: 0-byte files
// of every kind, random bytes, a lone .msf, a truncated zip.
func (g *gen) messyFiles() error {
	files := map[string][]byte{
		"messy/files/empty.mbox":    nil,
		"messy/files/empty.eml":     nil,
		"messy/files/empty.pst":     nil,
		"messy/files/empty.olm":     nil,
		"messy/files/random.bin":    blob(100000, 11),
		"messy/files/fake.mbox":     blob(5000, 12),
		"messy/files/fake.pst":      append([]byte("!BDN"), blob(600, 13)...),
		"messy/files/notmail.eml":   []byte("just some text\nthat is not a mail\n"),
		"messy/files/orphan.msf":    []byte("// <!-- <mdb:mork:z v=\"1.4\"/> -->\n< <(a=c)> >\n"),
		"messy/files/truncated.olm": nil,
	}
	b, err := testutil.OLMBytes(testutil.OLMMessageEntries("Accounts/a@example.com/com.microsoft.__Messages/Inbox/m.olk15Message",
		testutil.OLMMsg{Subject: "cut", Body: "cut"}, "Accounts/a@example.com/com.microsoft.__Attachments")...)
	if err != nil {
		return err
	}
	files["messy/files/truncated.olm"] = b[:len(b)/2]
	for rel, data := range files {
		if err := g.write(rel, data); err != nil {
			return err
		}
		if rel == "messy/files/fake.pst" {
			// Has the PST magic: opened, then fails.
			g.source(rel, source.PST, true)
			continue
		}
		g.m.Ignored = append(g.m.Ignored, rel)
	}
	return nil
}

// messyOutlook writes a zip-bomb OLM, a PST with a corrupt message node, a
// PST with corrupted pages and a truncated PST.
func (g *gen) messyOutlook() error {
	// Zip bomb: one message document of ~250 MiB of text that compresses
	// to a few hundred KiB, next to a normal message.
	const acct = "Accounts/bomb@example.com/com.microsoft.__Messages/Inbox/"
	d := g.date()
	tok := g.token()
	good := testutil.OLMMsg{Subject: "Next to the bomb", Body: "fine " + tok, Sent: d.Format(olmTime), From: testutil.OLMAddr{Name: "A", Email: "a@example.com"}}
	entries := testutil.OLMMessageEntries(acct+"good.olk15Message", good, "Accounts/bomb@example.com/com.microsoft.__Attachments")
	bomb := []byte("<?xml version=\"1.0\"?><emails><email><OPFMessageCopySubject>Bomb</OPFMessageCopySubject><OPFMessageCopyBody>")
	bomb = append(bomb, bytes.Repeat([]byte("A"), 250<<20)...)
	bomb = append(bomb, "</OPFMessageCopyBody></email></emails>"...)
	entries = append(entries, testutil.OLMEntry{Name: acct + "bomb.olk15Message", Data: bomb})
	b, err := testutil.OLMBytes(entries...)
	if err != nil {
		return err
	}
	if err := g.write("messy/outlook/bomb.olm", b); err != nil {
		return err
	}
	g.add(Msg{Source: "messy/outlook/bomb.olm", Kind: source.OLM, Folder: "messy/outlook/bomb/Inbox", Subject: "Next to the bomb",
		From: "a@example.com", Date: d, Token: tok, Exact: true, Note: "zip bomb neighbour"})
	g.add(Msg{Source: "messy/outlook/bomb.olm", Kind: source.OLM, Folder: "messy/outlook/bomb/Inbox", Note: "zip bomb (listed, maybe as too large)"})
	g.source("messy/outlook/bomb.olm", source.OLM, false)

	// PST: one corrupt message node among good ones.
	mk := func(n int, prefix string) *pstgen.File {
		inbox := &pstgen.Folder{Name: "Inbox"}
		for i := 0; i < n; i++ {
			d := g.date()
			inbox.Messages = append(inbox.Messages, pstMsg(fmt.Sprintf("%s %d", prefix, i), people[i%len(people)], people[0], d,
				fmt.Sprintf("%s%d@example.com", strings.ToLower(strings.ReplaceAll(prefix, " ", "")), i), g.words(40)))
		}
		return &pstgen.File{Crypt: pstgen.CryptNone, StoreName: "Messy", Folders: []*pstgen.Folder{inbox}}
	}
	f := mk(6, "Corrupt node neighbour")
	f.Folders[0].Messages[2].Corrupt = true
	for i, m := range f.Folders[0].Messages {
		if i == 2 {
			continue
		}
		var subj string
		for _, p := range m.Props {
			if p.ID == 0x0037 {
				subj = fmt.Sprintf("Corrupt node neighbour %d", i)
			}
		}
		g.add(Msg{Source: "messy/outlook/corrupt-node.pst", Kind: source.PST, Folder: "messy/outlook/corrupt-node/Inbox", Subject: subj,
			MessageID: fmt.Sprintf("corruptnodeneighbour%d@example.com", i), From: people[i%len(people)].Email, Note: "pst corrupt node neighbour"})
	}
	if err := g.write("messy/outlook/corrupt-node.pst", f.Bytes()); err != nil {
		return err
	}
	g.source("messy/outlook/corrupt-node.pst", source.PST, false)

	// PST with garbage over a range of data pages: an unknown number of
	// messages survive. Listed as a source whose count is not checked.
	pb := mk(40, "Corrupt page").Bytes()
	for i := len(pb) / 3; i < len(pb)/3+8192 && i < len(pb); i++ {
		pb[i] ^= byte(i*131 + 7)
	}
	if err := g.write("messy/outlook/corrupt-pages.pst", pb); err != nil {
		return err
	}
	g.source("messy/outlook/corrupt-pages.pst", source.PST, true)
	tb := mk(20, "Truncated pst").Bytes()
	if err := g.write("messy/outlook/truncated.pst", tb[:len(tb)/2]); err != nil {
		return err
	}
	g.source("messy/outlook/truncated.pst", source.PST, true)
	return nil
}
