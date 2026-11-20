package corpus

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/lazyoracle/mail-export-viewer/internal/source"
)

// gmailLabelSets are the X-Gmail-Labels of the Gmail set, cycled. Each
// entry is (header value, labels as the viewer shows them).
var gmailLabelSets = []struct {
	header string
	labels []string
}{
	{"Inbox,Opened,Category Personal", []string{"Inbox", "Opened", "Category Personal"}},
	{"Inbox,Unread,Important", []string{"Inbox", "Unread", "Important"}},
	{"Archived,Starred,Work", []string{"Archived", "Starred", "Work"}},
	{"Sent,Opened", []string{"Sent", "Opened"}},
	{"Inbox,Work,\"Travel/2023\"", []string{"Inbox", "Work", "Travel/2023"}},
	{"=?UTF-8?Q?Fam=C3=ADlia?=,Inbox,Unread", []string{"Família", "Inbox", "Unread"}},
	{"\"Bills, Receipts\",Archived", []string{"Bills, Receipts", "Archived"}},
	{"Trash,Opened", []string{"Trash", "Opened"}},
	{"Spam,Unread", []string{"Spam", "Unread"}},
	{"Drafts", []string{"Drafts"}},
	{"=?UTF-8?B?8J+OiSBQYXJ0eQ==?=,Inbox", []string{"🎉 Party", "Inbox"}},
	{"Inbox, Work ,Work,  ", []string{"Inbox", "Work"}},
}

// gmailFlags maps Gmail system labels to the flags they set (Trash sets
// none: it is an ordinary label).
func gmailFlags(labels []string) source.Flags {
	var f source.Flags
	for _, l := range labels {
		switch strings.ToLower(l) {
		case "unread":
			f |= source.Unread
		case "starred":
			f |= source.Starred
		case "important":
			f |= source.Important
		case "spam":
			f |= source.Junk
		case "drafts", "draft":
			f |= source.Draft
		}
	}
	return f
}

// gmail writes a Google Takeout export: one CRLF mbox with labels, a
// reply thread, an attachment and an inline image.
func (g *gen) gmail() error {
	const rel = "gmail/Takeout/Mail/All mail Including Spam and Trash.mbox"
	const folder = "gmail/Takeout/Mail/All mail Including Spam and Trash"
	f := &mboxFile{crlf: true}
	var prevID string
	var refs []string
	for i := 0; i < 36; i++ {
		ls := gmailLabelSets[i%len(gmailLabelSets)]
		from, to := g.person(), g.person()
		d := g.date()
		tok := g.token()
		id := fmt.Sprintf("gm%d.%d@mail.example.com", i, g.seq)
		subject := fmt.Sprintf("Gmail topic %d", i)
		thread := ""
		h := envelope(from, []person{to}, subject, d, id)
		if i >= 30 { // a six-message conversation
			thread = "gmail-thread"
			if i == 30 {
				subject = "Trip planning"
			} else {
				subject = "Re: Trip planning"
				h = append(h, [2]string{"In-Reply-To", "<" + prevID + ">"}, [2]string{"References", "<" + strings.Join(refs, "> <") + ">"})
			}
			h[2][1] = subject
			refs = append(refs, id)
			prevID = id
		}
		h = append(h, [2]string{"X-GM-THRID", strconv.Itoa(1000 + i)}, [2]string{"X-Gmail-Labels", ls.header})
		m := &msg{headers: h, crlf: true}
		body := "Hello,\n\n" + g.words(30) + " " + tok + "\n\nFrom the desk of " + from.Name + "\n>From an old quote\n"
		var atts []string
		switch i % 9 {
		case 4:
			m.parts = []mpart{textPart(body), attPart("itinerary.pdf", "application/pdf", blob(5000, uint64(i)))}
			atts = []string{"itinerary.pdf"}
		case 7:
			html := "<html><body><p>See <b>picture</b> " + tok + "</p><img src=\"cid:pic" + strconv.Itoa(i) + "@example.com\"></body></html>"
			m.raw = []byte("--rel\nContent-Type: multipart/alternative; boundary=alt\n\n--alt\nContent-Type: text/plain\n\n" + body +
				"\n--alt\nContent-Type: text/html; charset=utf-8\n\n" + html + "\n--alt--\n--rel\nContent-Type: image/png\n" +
				"Content-ID: <pic" + strconv.Itoa(i) + "@example.com>\nContent-Transfer-Encoding: base64\n\n" + string(b64(pngBytes)) + "--rel--\n")
			m.ctype = "multipart/related; boundary=rel"
		default:
			m.body = body
		}
		f.add("1234567890@xxx", d, m.bytes())
		labels := ls.labels
		g.add(Msg{Source: rel, Kind: source.Mbox, Folder: folder, Subject: subject, MessageID: id, From: from.Email,
			Date: d, Labels: labels, Flags: gmailFlags(labels) | attFlag(atts), Atts: atts, Token: tok, Thread: thread,
			Exact: true, Note: "gmail"})
	}
	g.source(rel, source.Mbox, false)
	// Takeout clutter that is not mail.
	g.m.Ignored = append(g.m.Ignored, "gmail/Takeout/archive_browser.html")
	if err := g.write("gmail/Takeout/archive_browser.html", []byte("<html><body>Takeout archive</body></html>\n")); err != nil {
		return err
	}
	return g.write(rel, f.b.Bytes())
}

func attFlag(atts []string) source.Flags {
	if len(atts) > 0 {
		return source.HasAttachment
	}
	return 0
}

// pngBytes is a 1x1 PNG.
var pngBytes = []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR\x00\x00\x00\x01\x00\x00\x00\x01\x08\x06\x00\x00\x00\x1f\x15\xc4\x89" +
	"\x00\x00\x00\rIDATx\x9cc\xf8\x0f\x00\x00\x01\x01\x00\x05\x18\xd8N\x00\x00\x00\x00IEND\xaeB`\x82")

// tbFolder is one Thunderbird mbox of the profile.
type tbFolder struct {
	rel, folder string
	n           int
	msf         bool // write a .msf summary
	drafts      bool // messages carry X-Mozilla-Draft-Info
}

// tbTags: keyword -> display name and colour (prefs.js), plus the
// Thunderbird defaults used for $label keys not in prefs.js.
var tbTags = []struct{ key, name, color string }{
	{"$label1", "Urgent", "#FF0000"}, // renamed default
	{"$label2", "Work", "#FF9900"},   // Thunderbird default, not in prefs.js
	{"projx", "Projekt Ü", "#3366CC"},
	{"later", "Read \"later\"", "#00AA00"},
}

// thunderbird writes a profile: prefs.js, Local Folders (Inbox with .msf,
// Sent without, Archives.sbd/2019 and Archives.sbd/2019.sbd/Q1 nested) and
// an ImapMail account.
func (g *gen) thunderbird() error {
	const prof = "thunderbird/Profiles/ab12cd34.default-release"
	prefs := "// Mozilla User Preferences\n" +
		"user_pref(\"mailnews.tags.$label1.tag\", \"Urgent\");\n" +
		"user_pref(\"mailnews.tags.$label1.color\", \"#FF0000\");\n" +
		"user_pref(\"mailnews.tags.projx.tag\", \"Projekt \\u00DC\");\n" +
		"user_pref(\"mailnews.tags.projx.color\", \"#3366CC\");\n" +
		"user_pref(\"mailnews.tags.later.tag\", \"Read \\\"later\\\"\");\n" +
		"user_pref(\"mailnews.tags.later.color\", \"#00AA00\");\n" +
		"user_pref(\"mail.server.server1.hostname\", \"pop.example.com\");\n"
	if err := g.write(prof+"/prefs.js", []byte(prefs)); err != nil {
		return err
	}
	for _, t := range tbTags {
		if t.color != "" {
			g.m.TagColors[t.name] = t.color
		}
	}
	folders := []tbFolder{
		{prof + "/Mail/Local Folders/Inbox", prof + "/Mail/Local Folders/Inbox", 14, true, false},
		{prof + "/Mail/Local Folders/Sent", prof + "/Mail/Local Folders/Sent", 6, false, false},
		{prof + "/Mail/Local Folders/Drafts", prof + "/Mail/Local Folders/Drafts", 2, true, true},
		{prof + "/Mail/Local Folders/Archives.sbd/2019", prof + "/Mail/Local Folders/Archives/2019", 5, true, false},
		{prof + "/Mail/Local Folders/Archives.sbd/2019.sbd/Q1", prof + "/Mail/Local Folders/Archives/2019/Q1", 3, false, false},
		{prof + "/ImapMail/imap.example.com/INBOX", prof + "/ImapMail/imap.example.com/INBOX", 8, true, false},
	}
	// Empty Thunderbird folder files next to .sbd directories, and clutter.
	clutter := map[string]string{
		prof + "/Mail/Local Folders/Archives":           "",
		prof + "/Mail/Local Folders/Trash":              "",
		prof + "/Mail/Local Folders/Trash.msf":          "// <!-- <mdb:mork:z v=\"1.4\"/> -->\n",
		prof + "/Mail/Local Folders/msgFilterRules.dat": "version=\"9\"\nlogging=\"no\"\n",
		prof + "/Mail/Local Folders/popstate.dat":       "# POP3 State File\n",
		prof + "/times.json":                            "{\"created\":1}\n",
	}
	for rel, data := range clutter {
		if err := g.write(rel, []byte(data)); err != nil {
			return err
		}
		g.m.Ignored = append(g.m.Ignored, rel)
	}
	for _, fo := range folders {
		if err := g.tbFolder(fo); err != nil {
			return err
		}
	}
	return nil
}

func (g *gen) tbFolder(fo tbFolder) error {
	f := &mboxFile{}
	type row struct {
		id     string
		offset int64
		flags  uint64
		keys   string
		prio   int
	}
	var rows []row
	for i := 0; i < fo.n; i++ {
		from, to := g.person(), g.person()
		d := g.date()
		tok := g.token()
		id := fmt.Sprintf("tb%d.%d@pop.example.com", i, g.seq)
		subject := fmt.Sprintf("Thunderbird %s note %d", lastSeg(fo.folder), i)
		h := envelope(from, []person{to}, subject, d, id)
		var flags source.Flags
		var tags []string
		// Header metadata on every message; the .msf (when present) is
		// authoritative for flags and adds its keywords.
		status := uint64(0x0001) // read
		hkeys := ""
		switch i % 5 {
		case 0:
			status = 0
			flags |= source.Unread
		case 1:
			status |= 0x0004 | 0x0002 // starred, replied
			flags |= source.Starred | source.Answered
			hkeys = "$label2"
			tags = append(tags, "Work")
		case 2:
			status |= 0x1000 // forwarded
			flags |= source.Forwarded
			hkeys = "projx later"
			tags = append(tags, "Projekt Ü", "Read \"later\"")
		case 3:
			status |= 0x0008 // expunged: deleted
			flags |= source.Deleted
		}
		h = append(h, [2]string{"X-Mozilla-Status", fmt.Sprintf("%04x", status)},
			[2]string{"X-Mozilla-Status2", "00000000"}, [2]string{"X-Mozilla-Keys", hkeys + strings.Repeat(" ", 20)},
			[2]string{"X-Account-Key", "account1"})
		if fo.drafts {
			h = append(h, [2]string{"X-Mozilla-Draft-Info", "internal/draft; vcard=0; receipt=0"})
			flags |= source.Draft
		}
		body := g.words(25) + "\n" + tok + "\nFrom here on the body has a From_ line.\n"
		m := &msg{headers: h, body: body}
		var atts []string
		if i%4 == 3 {
			m.parts = []mpart{textPart(body), attPart("notes "+strconv.Itoa(i)+".txt", "text/plain", []byte("plain notes\n"))}
			m.body = ""
			atts = []string{"notes " + strconv.Itoa(i) + ".txt"}
		}
		f.add("- ", d, m.bytes())
		r := row{id: id, offset: f.offsets[len(f.offsets)-1], flags: status}
		if fo.msf {
			// The summary differs from the headers on some messages and
			// wins: message 4 is starred and urgent in the .msf only,
			// message 0 is read there.
			switch i % 5 {
			case 0:
				if i%2 == 0 {
					r.flags = 0x0001
					flags &^= source.Unread
				}
			case 4:
				r.flags = 0x0001 | 0x0004
				r.keys = "$label1"
				r.prio = 6
				flags |= source.Starred | source.Important
				tags = append(tags, "Urgent")
			}
			if hkeys != "" {
				r.keys = strings.TrimSpace(r.keys + " " + hkeys)
			}
			rows = append(rows, r)
		}
		g.add(Msg{Source: fo.rel, Kind: source.Mbox, Folder: fo.folder, Subject: subject, MessageID: id, From: from.Email,
			Date: d, Tags: tags, Flags: flags | attFlag(atts), Atts: atts, Token: tok, Exact: true, Note: "thunderbird"})
	}
	g.source(fo.rel, source.Mbox, false)
	if err := g.write(fo.rel, f.b.Bytes()); err != nil {
		return err
	}
	if !fo.msf {
		return nil
	}
	var mrows []map[string]string
	for k, r := range rows {
		mr := map[string]string{"_id": strconv.FormatInt(int64(k+1), 16), "message-id": r.id,
			"storeToken": strconv.FormatInt(r.offset, 10), "flags": strconv.FormatUint(r.flags, 16)}
		if r.keys != "" {
			mr["keywords"] = r.keys
		}
		if r.prio != 0 {
			mr["priority"] = strconv.Itoa(r.prio)
		}
		mrows = append(mrows, mr)
	}
	return g.write(fo.rel+".msf", []byte(mork(mrows)))
}

func lastSeg(p string) string { return p[strings.LastIndexByte(p, '/')+1:] }

// apple writes an Apple Mail export: Name.mbox directories holding an
// "mbox" file plus Info.plist and table_of_contents, nested.
func (g *gen) apple() error {
	boxes := []struct{ dir, folder string }{
		{"apple/Mailboxes/Work.mbox", "apple/Mailboxes/Work"},
		{"apple/Mailboxes/Projects.mbox", "apple/Mailboxes/Projects"},
		{"apple/Mailboxes/Projects.mbox/Launch.mbox", "apple/Mailboxes/Projects/Launch"},
	}
	for bi, b := range boxes {
		f := &mboxFile{}
		for i := 0; i < 4; i++ {
			from, to := g.person(), g.person()
			d := g.date()
			tok := g.token()
			id := fmt.Sprintf("apple%d.%d@icloud.example.com", bi, i)
			subject := fmt.Sprintf("Apple %s message %d", lastSeg(b.folder), i)
			h := envelope(from, []person{to}, subject, d, id)
			m := &msg{headers: h, body: g.words(20) + " " + tok + "\n"}
			f.add("MAILER-DAEMON", d, m.bytes())
			g.add(Msg{Source: b.dir + "/mbox", Kind: source.Mbox, Folder: b.folder, Subject: subject, MessageID: id,
				From: from.Email, Date: d, Token: tok, Exact: true, Note: "apple"})
		}
		g.source(b.dir+"/mbox", source.Mbox, false)
		if err := g.write(b.dir+"/mbox", f.b.Bytes()); err != nil {
			return err
		}
		plist := "<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n<plist version=\"1.0\"><dict><key>MailboxName</key><string>" +
			lastSeg(b.folder) + "</string></dict></plist>\n"
		for name, data := range map[string][]byte{"Info.plist": []byte(plist), "table_of_contents": blob(300, uint64(bi))} {
			if err := g.write(b.dir+"/"+name, data); err != nil {
				return err
			}
			g.m.Ignored = append(g.m.Ignored, b.dir+"/"+name)
		}
	}
	return nil
}

// eml writes loose .eml files in nested directories (LF and CRLF, one with
// an attachment), plus non-mail files that must be ignored.
func (g *gen) eml() error {
	dirs := []string{"eml/2023", "eml/2023/receipts", "eml/Ünïcødé ✉ dir"}
	for di, dir := range dirs {
		for i := 0; i < 3; i++ {
			from, to := g.person(), g.person()
			d := g.date()
			tok := g.token()
			id := fmt.Sprintf("eml%d.%d@example.org", di, i)
			subject := fmt.Sprintf("Loose eml %d in %s", i, lastSeg(dir))
			m := &msg{headers: envelope(from, []person{to}, subject, d, id), body: g.words(15) + " " + tok + "\n", crlf: i == 1}
			var atts []string
			if i == 2 {
				m.parts = []mpart{textPart(m.body), attPart("receipt.csv", "text/csv", []byte("item,price\ntea,3\n"))}
				atts = []string{"receipt.csv"}
			}
			name := fmt.Sprintf("%s/message %d.eml", dir, i)
			if err := g.write(name, m.bytes()); err != nil {
				return err
			}
			g.add(Msg{Source: dir, Kind: source.EML, Folder: dir, Subject: subject, MessageID: id, From: from.Email,
				Date: d, Atts: atts, Flags: attFlag(atts), Token: tok, Exact: true, Note: "eml"})
		}
		g.source(dir, source.EML, false)
	}
	for rel, data := range map[string]string{"eml/README.txt": "exported messages\n", "eml/2023/.DS_Store": "\x00\x00\x00\x01Bud1"} {
		if err := g.write(rel, []byte(data)); err != nil {
			return err
		}
		g.m.Ignored = append(g.m.Ignored, rel)
	}
	return nil
}

// mork renders a minimal Thunderbird .msf: a column dictionary and one row
// per message in the msgs table. Values with spaces or specials are escaped.
func mork(rows []map[string]string) string {
	colSet := map[string]bool{}
	var cols []string
	for _, r := range rows {
		for k := range r {
			if k != "_id" && !colSet[k] {
				colSet[k] = true
				cols = append(cols, k)
			}
		}
	}
	sort.Strings(cols)
	var b strings.Builder
	b.WriteString("// <!-- <mdb:mork:z v=\"1.4\"/> -->\n< <(a=c)> // (f=iso-8859-1)\n")
	b.WriteString("  (80=ns:msg:db:row:scope:dbfolderinfo:all)\n  (81=ns:msg:db:row:scope:msgs:all)\n")
	id := map[string]int{}
	for i, c := range cols {
		id[c] = 0x90 + i
		fmt.Fprintf(&b, "  (%X=%s)\n", 0x90+i, c)
	}
	b.WriteString(">\n<(80=0)(81=lit)>\n{1:^80 {(k^BE:c)(s=9)} [7F:^80(flags=1)]}\n{2:^81 {(k^BE:c)(s=9)}\n")
	for _, r := range rows {
		fmt.Fprintf(&b, "  [%s:^81", r["_id"])
		for _, c := range cols {
			if v, ok := r[c]; ok {
				fmt.Fprintf(&b, "(^%X=%s)", id[c], morkEscape(v))
			}
		}
		b.WriteString("]\n")
	}
	b.WriteString("}\n")
	return b.String()
}

func morkEscape(v string) string {
	var b strings.Builder
	for i := 0; i < len(v); i++ {
		c := v[i]
		switch {
		case c == ')' || c == '\\' || c == '$':
			fmt.Fprintf(&b, "$%02X", c)
		case c < 0x21 || c >= 0x7f:
			fmt.Fprintf(&b, "$%02X", c)
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}
