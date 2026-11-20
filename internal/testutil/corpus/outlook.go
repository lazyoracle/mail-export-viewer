package corpus

import (
	"fmt"
	"strings"
	"time"

	"github.com/lazyoracle/mail-export-viewer/internal/source"
	"github.com/lazyoracle/mail-export-viewer/internal/source/pst/pstgen"
	"github.com/lazyoracle/mail-export-viewer/internal/testutil"
)

const olmTime = "2006-01-02T15:04:05"

// olm writes two Outlook for Mac archives: the new layout (Accounts/...,
// with a contact and a calendar item that are skipped) and the 2011 layout
// (Local/Messages/...).
func (g *gen) olm() error {
	const acct = "Accounts/me@example.com"
	var entries []testutil.OLMEntry
	newFolders := []string{"Inbox", "Inbox/Project Ω", "Sent Items"}
	cats := [][]string{{"Finance"}, {"Red Category", "Finance"}, nil, {"Personal"}}
	n := 0
	for fi, fo := range newFolders {
		for i := 0; i < 4; i++ {
			n++
			from, to := g.person(), g.person()
			d := g.date()
			tok := g.token()
			id := fmt.Sprintf("olm%d.%d@outlook.example.com", fi, i)
			subject := fmt.Sprintf("Outlook %s item %d", lastSeg(fo), i)
			m := testutil.OLMMsg{Subject: subject, MessageID: "<" + id + ">",
				From: testutil.OLMAddr{Name: from.Name, Email: from.Email},
				To:   []testutil.OLMAddr{{Name: to.Name, Email: to.Email}},
				Body: g.words(20) + " " + tok, Sent: d.Format(olmTime), Categories: cats[i%len(cats)]}
			var flags source.Flags
			var atts []string
			switch i {
			case 0:
				m.Unread = true
				flags |= source.Unread
			case 1:
				m.Flagged = true
				m.Priority = "1"
				flags |= source.Starred | source.Important
			case 2:
				m.HTML = "<html><body><p>Chart <img src=\"cid:chart" + id + "\"> " + tok + "</p></body></html>"
				m.Attachments = []testutil.OLMAtt{
					{Name: "budget.xlsx", Type: "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", Data: blob(4000, uint64(n))},
					{Name: "chart.png", Type: "image/png", CID: "chart" + id, Data: pngBytes},
				}
				atts = []string{"budget.xlsx"} // chart.png is inline
				flags |= source.HasAttachment
			case 3:
				m.Received = d.Format(olmTime)
			}
			entries = append(entries, testutil.OLMMessageEntries(
				fmt.Sprintf("%s/com.microsoft.__Messages/%s/message_%d.olk15Message", acct, fo, i), m, acct+"/com.microsoft.__Attachments")...)
			g.add(Msg{Source: "olm/backup.olm", Kind: source.OLM, Folder: "olm/backup/" + fo, Subject: subject, MessageID: id,
				From: from.Email, Date: d, Cats: m.Categories, Flags: flags, Atts: atts, Token: tok, Exact: true, Note: "olm new layout"})
		}
	}
	entries = append(entries,
		testutil.OLMEntry{Name: acct + "/com.microsoft.__Contacts/Contacts/contact_1.olk15Contact",
			Data: []byte("<?xml version=\"1.0\"?><contacts><contact><OPFContactCopyDisplayName>Someone</OPFContactCopyDisplayName></contact></contacts>")},
		testutil.OLMEntry{Name: acct + "/com.microsoft.__Calendar/Calendar/event_1.olk15Event",
			Data: []byte("<?xml version=\"1.0\"?><appointments><appointment><OPFCalendarEventCopySummary>Standup</OPFCalendarEventCopySummary></appointment></appointments>")},
		testutil.OLMEntry{Name: "Categories.xml", Data: []byte("<?xml version=\"1.0\"?><categories><category name=\"Finance\"/></categories>")},
	)
	b, err := testutil.OLMBytes(entries...)
	if err != nil {
		return err
	}
	if err := g.write("olm/backup.olm", b); err != nil {
		return err
	}
	g.source("olm/backup.olm", source.OLM, false)

	entries = nil
	for i := 0; i < 5; i++ {
		from := g.person()
		d := g.date()
		tok := g.token()
		subject := fmt.Sprintf("Legacy Outlook 2011 mail %d", i)
		m := testutil.OLMMsg{Subject: subject, From: testutil.OLMAddr{Name: from.Name, Email: from.Email},
			Body: g.words(10) + " " + tok, Received: d.Format(olmTime)}
		entries = append(entries, testutil.OLMMessageEntries(fmt.Sprintf("Local/Messages/Archive/message_%05d.xml", i), m, "Local/Attachments")...)
		g.add(Msg{Source: "olm/legacy-2011.olm", Kind: source.OLM, Folder: "olm/legacy-2011/Archive", Subject: subject,
			From: from.Email, Date: d, Token: tok, Exact: true, Note: "olm 2011 layout"})
	}
	if b, err = testutil.OLMBytes(entries...); err != nil {
		return err
	}
	g.source("olm/legacy-2011.olm", source.OLM, false)
	return g.write("olm/legacy-2011.olm", b)
}

// pstMsg returns a PST message with the usual envelope properties.
func pstMsg(subject string, from person, to person, d time.Time, msgID, body string) *pstgen.Message {
	m := &pstgen.Message{Props: []pstgen.Prop{
		pstgen.Str(0x0037, subject),
		pstgen.Str(0x0C1A, from.Name), pstgen.Str(0x0C1F, from.Email), pstgen.Str(0x0C1E, "SMTP"),
		pstgen.Time(0x0039, d),
		pstgen.Str(0x1000, body),
		pstgen.I32(0x0E07, 0x1),
	}, Recipients: []pstgen.Recipient{{Type: pstgen.To, Name: to.Name, Email: to.Email, AddrType: "SMTP"}}}
	if msgID != "" {
		m.Props = append(m.Props, pstgen.Str(0x1035, "<"+msgID+">"))
	}
	return m
}

// setProp replaces or adds a property.
func setProp(m *pstgen.Message, p pstgen.Prop) {
	for i, q := range m.Props {
		if q.ID == p.ID {
			m.Props[i] = p
			return
		}
	}
	m.Props = append(m.Props, p)
}

// pst writes an Outlook PST (permute encoding) with nested folders,
// flags, categories, attachments (one big), an embedded message, a big
// body and an RTF-only body, plus a contact (skipped).
func (g *gen) pst() error {
	const rel = "pst/outlook.pst"
	folders := map[string]*pstgen.Folder{}
	inbox := &pstgen.Folder{Name: "Inbox"}
	sub := &pstgen.Folder{Name: "Clients ✓"}
	inbox.Folders = []*pstgen.Folder{sub}
	sent := &pstgen.Folder{Name: "Sent Items"}
	del := &pstgen.Folder{Name: "Deleted Items"}
	folders["Inbox"], folders["Inbox/Clients ✓"], folders["Sent Items"], folders["Deleted Items"] = inbox, sub, sent, del
	order := []string{"Inbox", "Inbox/Clients ✓", "Sent Items", "Deleted Items"}
	for fi, name := range order {
		fo := folders[name]
		for i := 0; i < 5; i++ {
			from, to := g.person(), g.person()
			d := g.date()
			tok := g.token()
			id := fmt.Sprintf("pst%d.%d@exchange.example.com", fi, i)
			subject := fmt.Sprintf("PST %s message %d", lastSeg(name), i)
			body := g.words(25) + " " + tok + "\r\n"
			m := pstMsg(subject, from, to, d, id, body)
			var flags source.Flags
			var cats, atts []string
			switch i {
			case 0:
				setProp(m, pstgen.I32(0x0E07, 0))
				flags |= source.Unread
				cats = []string{"Blue Category"}
			case 1:
				m.Props = append(m.Props, pstgen.I32(0x1090, 2), pstgen.I32(0x0017, 2), pstgen.I32(0x1081, 102))
				flags |= source.Starred | source.Important | source.Answered
				cats = []string{"Blue Category", "Project Ω"}
			case 2:
				m.Props = append(m.Props, pstgen.I32(0x1081, 104))
				flags |= source.Forwarded
				m.Attachments = []*pstgen.Attachment{
					{Props: []pstgen.Prop{pstgen.Str(0x3707, "contract.pdf"), pstgen.Str(0x370E, "application/pdf")}, Data: blob(30000, uint64(fi))},
				}
				atts = []string{"contract.pdf"}
			case 3:
				setProp(m, pstgen.I32(0x0E07, 0x1|0x8))
				flags |= source.Draft
				// HTML only.
				m.Props = append(m.Props[:5], m.Props[6:]...)
				m.Props = append(m.Props, pstgen.Bin(0x1013, []byte("<html><body><p>HTML only "+tok+"</p></body></html>")))
			case 4:
				if fi == 0 {
					inner := pstMsg("Forwarded original", g.person(), to, d.Add(-time.Hour), "", "inner text")
					m.Attachments = []*pstgen.Attachment{{Props: []pstgen.Prop{pstgen.Str(0x3001, "Forwarded original")}, Embedded: inner}}
					atts = []string{"Forwarded original.eml"}
				}
			}
			m.Categories = cats
			fo.Messages = append(fo.Messages, m)
			g.add(Msg{Source: rel, Kind: source.PST, Folder: "pst/outlook/" + name, Subject: subject, MessageID: id,
				From: from.Email, Date: d, Cats: cats, Flags: flags | attFlag(atts), Atts: atts, Token: tok, Exact: true, Note: "pst"})
		}
	}
	// Big body and big attachment.
	from, to := g.person(), g.person()
	d := g.date()
	bigBody := strings.Repeat(g.words(12)+"\r\n", g.o.BigBytes/80) + g.m.BigToken + "\r\n"
	big := pstMsg("PST big body and attachment", from, to, d, "pstbig@exchange.example.com", bigBody)
	big.Attachments = []*pstgen.Attachment{{Props: []pstgen.Prop{pstgen.Str(0x3707, "disk image.bin"), pstgen.Str(0x370E, "application/octet-stream")},
		Data: blob(g.o.BigBytes, 99)}}
	inbox.Messages = append(inbox.Messages, big)
	g.add(Msg{Source: rel, Kind: source.PST, Folder: "pst/outlook/Inbox", Subject: "PST big body and attachment", MessageID: "pstbig@exchange.example.com",
		From: from.Email, Date: d, Flags: source.HasAttachment, Atts: []string{"disk image.bin"}, Exact: true, Note: "pst big"})
	// RTF-only body.
	d = g.date()
	tok := g.token()
	rtf := pstMsg("PST rich text only", from, to, d, "pstrtf@exchange.example.com", "")
	rtf.Props = append(rtf.Props[:5], rtf.Props[6:]...)
	rtf.Props = append(rtf.Props, pstgen.Bin(0x1009, pstgen.CompressRTF([]byte(`{\rtf1\ansi Rich `+tok+` text\par}`))))
	sent.Messages = append(sent.Messages, rtf)
	g.add(Msg{Source: rel, Kind: source.PST, Folder: "pst/outlook/Sent Items", Subject: "PST rich text only", MessageID: "pstrtf@exchange.example.com",
		From: from.Email, Date: d, Token: tok, Exact: true, Note: "pst rtf"})
	// Non-mail item.
	inbox.Messages = append(inbox.Messages, &pstgen.Message{Props: []pstgen.Prop{pstgen.Str(0x001A, "IPM.Contact"), pstgen.Str(0x0037, "A contact")}})
	f := &pstgen.File{Crypt: pstgen.CryptPermute, StoreName: "Outlook Data File", Folders: []*pstgen.Folder{inbox, sent, del, {Name: "Empty Folder"}}}
	g.source(rel, source.PST, false)
	return g.write(rel, f.Bytes())
}
