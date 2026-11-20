package pst

import (
	"bytes"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/lazyoracle/mail-export-viewer/internal/mail"
	"github.com/lazyoracle/mail-export-viewer/internal/source"
)

// Messaging layer ([MS-PST] 2.4): message store, folders, messages,
// recipients and attachments, mapped onto mail.Message.

// Special NIDs and NID types.
const (
	nidMessageStore    = 0x21
	nidNameToIDMap     = 0x61
	nidRootFolder      = 0x122
	nidAttachmentTable = 0x671
	nidRecipientTable  = 0x692

	nidTypeFolder    = 0x02
	nidTypeMessage   = 0x04
	nidTypeHierarchy = 0x0D
	nidTypeContents  = 0x0E
)

// Property IDs ([MS-OXPROPS]).
const (
	pidImportance          = 0x0017
	pidMessageClass        = 0x001A
	pidSubject             = 0x0037
	pidClientSubmitTime    = 0x0039
	pidSentReprName        = 0x0042
	pidSentReprAddrType    = 0x0064
	pidSentReprEmail       = 0x0065
	pidTransportHeaders    = 0x007D
	pidRecipientType       = 0x0C15
	pidSenderName          = 0x0C1A
	pidSenderAddrType      = 0x0C1E
	pidSenderEmail         = 0x0C1F
	pidDeliveryTime        = 0x0E06
	pidMessageFlags        = 0x0E07
	pidAttachSize          = 0x0E20
	pidBody                = 0x1000
	pidRTFCompressed       = 0x1009
	pidHTML                = 0x1013
	pidInternetMessageID   = 0x1035
	pidInternetReferences  = 0x1039
	pidInReplyTo           = 0x1042
	pidLastVerb            = 0x1081
	pidFlagStatus          = 0x1090
	pidDisplayName         = 0x3001
	pidAddrType            = 0x3002
	pidEmailAddress        = 0x3003
	pidCreationTime        = 0x3007
	pidIPMSubtreeEntryID   = 0x35E0
	pidAttachData          = 0x3701
	pidAttachExtension     = 0x3703
	pidAttachFilename      = 0x3704
	pidAttachMethod        = 0x3705
	pidAttachLongFilename  = 0x3707
	pidAttachMimeTag       = 0x370E
	pidAttachContentID     = 0x3712
	pidSMTPAddress         = 0x39FE
	pidInternetCodepage    = 0x3FDE
	pidMessageCodepage     = 0x3FFD
	pidSenderSMTPAddress   = 0x5D01
	pidSentReprSMTPAddress = 0x5D02
	pidNameidStreamGUID    = 0x0002
	pidNameidStreamEntry   = 0x0003
	pidNameidStreamString  = 0x0004
)

const (
	maxFolders     = 100000
	maxFolderDepth = 64
	maxEmbedDepth  = 4
	// Scan reads bounded body prefixes; Summary.Text is capped anyway.
	scanTextLimit = 1 << 20
	scanHTMLLimit = 4 << 20
	scanRTFLimit  = 4 << 20
)

type store struct {
	db       *ndb
	ipm      uint32 // folder whose children are the top-level folders
	keywords uint16 // property ID of the named property "Keywords", or 0
}

func (d *ndb) pcOf(nid uint32) (*pc, error) {
	n, err := d.node(nid)
	if err != nil {
		return nil, err
	}
	o, err := d.openObj(n.bidData, n.bidSub)
	if err != nil {
		return nil, err
	}
	return o.pc()
}

func openStore(d *ndb) (*store, error) {
	p, err := d.pcOf(nidMessageStore)
	if err != nil {
		return nil, fmt.Errorf("pst: message store: %w", err)
	}
	s := &store{db: d, ipm: nidRootFolder}
	if eid := p.set("").bin(pidIPMSubtreeEntryID, 64); len(eid) >= 24 {
		if nid := le32(eid, 20); nid&0x1F == nidTypeFolder {
			if _, err := d.node(nid); err == nil {
				s.ipm = nid
			}
		}
	}
	if s.ipm == nidRootFolder {
		if _, err := d.pcOf(nidRootFolder); err != nil {
			return nil, fmt.Errorf("pst: root folder: %w", err)
		}
		// Without the store's pointer, use the root's "Top of ..." child.
		kids, _ := s.children(nidRootFolder)
		for _, k := range kids {
			if p, err := d.pcOf(k); err == nil && strings.HasPrefix(p.set("").str(pidDisplayName), "Top of ") {
				s.ipm = k
				break
			}
		}
	}
	s.keywords = s.findKeywords()
	return s, nil
}

// psPublicStrings is PS_PUBLIC_STRINGS {00020329-0000-0000-C000-000000000046}.
var psPublicStrings = []byte{0x29, 0x03, 0x02, 0x00, 0x00, 0x00, 0x00, 0x00, 0xC0, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x46}

// findKeywords looks up the property ID that the name-to-ID map assigns
// to PS_PUBLIC_STRINGS "Keywords" (Outlook categories).
func (s *store) findKeywords() uint16 {
	p, err := s.db.pcOf(nidNameToIDMap)
	if err != nil {
		return 0
	}
	ps := p.set("")
	guids := ps.bin(pidNameidStreamGUID, 16<<20)
	entries := ps.bin(pidNameidStreamEntry, 16<<20)
	strs := ps.bin(pidNameidStreamString, 16<<20)
	want := utf16Bytes("Keywords")
	for o := 0; o+8 <= len(entries); o += 8 {
		id, g, idx := le32(entries, o), le16(entries, o+4), le16(entries, o+6)
		if g&1 == 0 {
			continue
		}
		guid := int(g >> 1)
		if guid != 2 && !(guid >= 3 && (guid-3)*16+16 <= len(guids) && bytes.Equal(guids[(guid-3)*16:(guid-3)*16+16], psPublicStrings)) {
			continue
		}
		if int(id)+4 > len(strs) {
			continue
		}
		n := int(le32(strs, int(id)))
		if n == len(want) && int(id)+4+n <= len(strs) && bytes.Equal(strs[id+4:int(id)+4+n], want) {
			return 0x8000 + idx
		}
	}
	return 0
}

func utf16Bytes(s string) []byte {
	var b []byte
	for _, r := range s {
		b = append(b, byte(r), byte(r>>8))
	}
	return b
}

// table opens the table node nid.
func (d *ndb) tableOf(nid uint32) (*table, error) {
	n, err := d.node(nid)
	if err != nil {
		return nil, err
	}
	o, err := d.openObj(n.bidData, n.bidSub)
	if err != nil {
		return nil, err
	}
	return o.table()
}

// children lists the subfolders of folder nid (hierarchy table order).
func (s *store) children(nid uint32) ([]uint32, error) {
	t, err := s.db.tableOf(nid&^0x1F | nidTypeHierarchy)
	if err != nil {
		return nil, err
	}
	var out []uint32
	for i := 0; i < t.len(); i++ {
		if id := t.rowID(i); id&0x1F == nidTypeFolder {
			out = append(out, id)
		}
	}
	return out, nil
}

// contents lists the message NIDs of folder nid (contents table order).
func (s *store) contents(nid uint32) ([]uint32, error) {
	t, err := s.db.tableOf(nid&^0x1F | nidTypeContents)
	if err != nil {
		return nil, err
	}
	out := make([]uint32, 0, t.len())
	for i := 0; i < t.len(); i++ {
		out = append(out, t.rowID(i))
	}
	return out, nil
}

type folderRef struct {
	nid  uint32
	path []string // display names below the IPM subtree
}

// folders walks the folder tree depth-first below the IPM subtree.
// Unreadable folders are reported and skipped.
func (s *store) folders() ([]folderRef, []error) {
	var out []folderRef
	var warns []error
	seen := map[uint32]bool{s.ipm: true}
	var walk func(nid uint32, path []string)
	walk = func(nid uint32, path []string) {
		kids, err := s.children(nid)
		if err != nil {
			warns = append(warns, fmt.Errorf("folder %q: subfolders: %w", strings.Join(path, "/"), err))
			return
		}
		for _, k := range kids {
			if seen[k] || len(out) >= maxFolders || len(path) >= maxFolderDepth {
				continue
			}
			seen[k] = true
			p, err := s.db.pcOf(k)
			if err != nil {
				warns = append(warns, fmt.Errorf("folder %#x: %w", k, err))
				continue
			}
			name := strings.ReplaceAll(strings.TrimSpace(p.set("").str(pidDisplayName)), "/", "_")
			if name == "" {
				name = "(unnamed)"
			}
			sub := append(append([]string(nil), path...), name)
			out = append(out, folderRef{nid: k, path: sub})
			walk(k, sub)
		}
	}
	walk(s.ipm, nil)
	return out, warns
}

// isMail reports whether a message class is shown (mail, posts, reports,
// meeting requests); contacts, appointments, tasks, notes etc. are not.
func isMail(class string) bool {
	c := strings.ToUpper(class)
	for _, p := range []string{"IPM.NOTE", "IPM.POST", "REPORT.", "IPM.SCHEDULE.MEETING", "IPM.OUTLOOK.RECALL", "IPM.RECALL"} {
		if strings.HasPrefix(c, p) {
			return true
		}
	}
	return c == "" || c == "IPM"
}

// message is a message read from the store.
type message struct {
	mail.Message
	class  string
	flags  source.Flags
	labels []string
	props  []source.Prop
}

// openMessage opens the top-level message node nid; anything that is not
// a message node is stale.
func (s *store) openMessage(nid uint32) (*obj, error) {
	if nid&0x1F != nidTypeMessage {
		return nil, fmt.Errorf("pst: node %#x is not a message: %w", nid, source.ErrStale)
	}
	n, err := s.db.node(nid)
	if errors.Is(err, errNotFound) {
		return nil, fmt.Errorf("pst: message %#x: %w", nid, source.ErrStale)
	}
	if err != nil {
		return nil, err
	}
	return s.db.openObj(n.bidData, n.bidSub)
}

// message reads message nid. With full false (Scan) bodies are read only
// up to bounded prefixes.
func (s *store) message(nid uint32, full bool) (*message, error) {
	o, err := s.openMessage(nid)
	if err != nil {
		return nil, err
	}
	return s.readMessage(o, full)
}

func (s *store) readMessage(o *obj, full bool) (*message, error) {
	p, err := o.pc()
	if err != nil {
		return nil, err
	}
	ps := p.set("")
	cp, _ := ps.int(pidMessageCodepage)
	icp, _ := ps.int(pidInternetCodepage)
	if cp == 0 {
		cp = icp
	}
	ps.cs = cpCharset(int(cp))
	m := &message{class: ps.str(pidMessageClass)}
	if full {
		if err := s.checkSize(o, ps); err != nil {
			return nil, err
		}
	}
	m.Subject = subject(ps.str(pidSubject))
	m.MessageID = trimID(ps.str(pidInternetMessageID))
	m.InReplyTo = trimID(ps.str(pidInReplyTo))
	m.References = mail.ParseMessageIDs(ps.str(pidInternetReferences))
	m.From = address(ps.str(pidSentReprName), ps.str(pidSentReprSMTPAddress), ps.str(pidSentReprEmail), ps.str(pidSentReprAddrType))
	if m.From == (mail.Address{}) {
		m.From = address(ps.str(pidSenderName), ps.str(pidSenderSMTPAddress), ps.str(pidSenderEmail), ps.str(pidSenderAddrType))
	}
	s.recipients(o, &m.Message, ps.cs)
	m.Date = ps.time(pidClientSubmitTime)
	if m.Date.IsZero() {
		m.Date = ps.time(pidDeliveryTime)
	}
	if h := ps.str(pidTransportHeaders); strings.TrimSpace(h) != "" {
		raw := []byte(strings.TrimRight(h, "\r\n") + "\r\n\r\n")
		m.Header = mail.ParseHeader(raw)
		mergeEnvelope(&m.Envelope, mail.Parse(raw, time.Time{}).Envelope)
	}
	if m.Date.IsZero() {
		m.Date = ps.time(pidCreationTime)
	}
	s.bodies(ps, &m.Message, int(icp), full)
	s.flagsAndProps(ps, m)
	m.Attachments = s.attachmentList(o)
	return m, nil
}

// checkSize refuses messages whose bodies and attachments exceed
// source.MaxMessageBytes, before reading them.
func (s *store) checkSize(o *obj, ps propSet) error {
	var total int64
	for _, id := range []uint16{pidBody, pidHTML, pidRTFCompressed} {
		n, _ := ps.size(id)
		total += n
	}
	for _, a := range s.attachmentRefs(o) {
		total += a.size
	}
	if total > source.MaxMessageBytes {
		return fmt.Errorf("pst: message of %d bytes: %w", total, source.ErrTooLarge)
	}
	return nil
}

// subject drops the PST subject prefix marker (U+0001 and a length
// character).
func subject(s string) string {
	if strings.HasPrefix(s, "\x01") {
		r := []rune(s)
		if len(r) >= 2 {
			return string(r[2:])
		}
		return ""
	}
	return s
}

func trimID(s string) string {
	return strings.TrimSpace(strings.Trim(strings.TrimSpace(s), "<>"))
}

// address picks the SMTP address of a sender or recipient; Exchange
// (X.500) addresses without an SMTP form leave Email empty.
func address(name, smtp, email, addrType string) mail.Address {
	a := mail.Address{Name: strings.TrimSpace(name)}
	switch {
	case strings.Contains(smtp, "@"):
		a.Email = strings.TrimSpace(smtp)
	case strings.Contains(email, "@") && (addrType == "" || strings.EqualFold(addrType, "SMTP")):
		a.Email = strings.TrimSpace(email)
	}
	if a.Name == a.Email {
		a.Name = ""
	}
	return a
}

func (s *store) recipients(o *obj, m *mail.Message, cs string) {
	if _, ok := o.subs[nidRecipientTable]; !ok {
		return
	}
	ro, err := o.sub(nidRecipientTable)
	if err != nil {
		return
	}
	t, err := ro.table()
	if err != nil {
		return
	}
	for i := 0; i < t.len(); i++ {
		r := t.set(i, cs)
		a := address(r.str(pidDisplayName), r.str(pidSMTPAddress), r.str(pidEmailAddress), r.str(pidAddrType))
		if a == (mail.Address{}) {
			continue
		}
		switch typ, _ := r.int(pidRecipientType); typ & 0xF {
		case 2:
			m.Cc = append(m.Cc, a)
		case 3:
			m.Bcc = append(m.Bcc, a)
		default:
			m.To = append(m.To, a)
		}
	}
}

// mergeEnvelope fills what the properties lacked from the transport
// headers (e.g. SMTP addresses of Exchange senders).
func mergeEnvelope(e *mail.Envelope, h mail.Envelope) {
	if e.From.Email == "" && h.From.Email != "" {
		if h.From.Name == "" {
			h.From.Name = e.From.Name
		}
		e.From = h.From
	}
	for _, f := range []struct{ dst, src *[]mail.Address }{{&e.To, &h.To}, {&e.Cc, &h.Cc}, {&e.Bcc, &h.Bcc}} {
		if !hasEmail(*f.dst) && len(*f.src) > 0 {
			*f.dst = *f.src
		}
	}
	if e.MessageID == "" {
		e.MessageID = h.MessageID
	}
	if e.InReplyTo == "" {
		e.InReplyTo = h.InReplyTo
	}
	if len(e.References) == 0 {
		e.References = h.References
	}
	if e.Subject == "" {
		e.Subject = h.Subject
	}
	if e.Date.IsZero() {
		e.Date = h.Date
	}
}

func hasEmail(as []mail.Address) bool {
	for _, a := range as {
		if a.Email != "" {
			return true
		}
	}
	return false
}

// bodies sets Text and HTML from PR_BODY, PR_HTML and, when one is
// missing, PR_RTF_COMPRESSED.
func (s *store) bodies(ps propSet, m *mail.Message, icp int, full bool) {
	textLim, htmlLim, rtfLim := int64(scanTextLimit), int64(scanHTMLLimit), int64(scanRTFLimit)
	if full {
		textLim, htmlLim, rtfLim = source.MaxMessageBytes, source.MaxMessageBytes, source.MaxMessageBytes
	}
	if v, ok := ps.get(pidBody); ok {
		b, _ := ps.prefix(pidBody, textLim*2)
		m.Text = decodeString(v.typ, b, ps.cs)
		if !full {
			m.Text = truncateValid(m.Text, int(textLim))
		}
	}
	if v, ok := ps.get(pidHTML); ok {
		b, _ := ps.prefix(pidHTML, htmlLim)
		if v.typ == ptBinary {
			cs := cpCharset(icp)
			if cs == "" {
				cs = ps.cs
			}
			m.HTML = mail.DecodeCharset(b, cs)
		} else {
			m.HTML = decodeString(v.typ, b, ps.cs)
		}
	}
	if strings.TrimSpace(m.Text) == "" && strings.TrimSpace(m.HTML) == "" {
		if b := ps.bin(pidRTFCompressed, rtfLim); b != nil {
			if rtf, err := lzfu(b, int(min(rtfLim*4, source.MaxMessageBytes))); err == nil {
				m.HTML, m.Text = rtfConvert(rtf)
			}
		}
	}
	m.Text = strings.ReplaceAll(m.Text, "\r\n", "\n")
	m.HTML = strings.ReplaceAll(m.HTML, "\r\n", "\n")
}

func truncateValid(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return strings.ToValidUTF8(s[:n], "")
}

// Message flags ([MS-OXCMSG] PidTagMessageFlags) and verbs.
const (
	msgFlagRead   = 0x01
	msgFlagUnsent = 0x08
)

func (s *store) flagsAndProps(ps propSet, m *message) {
	if f, ok := ps.int(pidMessageFlags); ok {
		if f&msgFlagRead == 0 {
			m.flags |= source.Unread
		}
		if f&msgFlagUnsent != 0 {
			m.flags |= source.Draft
		}
	}
	if st, _ := ps.int(pidFlagStatus); st == 2 {
		m.flags |= source.Starred
	} else if st == 1 {
		m.props = append(m.props, source.Prop{Key: "Follow-up", Value: "complete"})
	}
	if imp, ok := ps.int(pidImportance); ok {
		switch imp {
		case 2:
			m.flags |= source.Important
		case 0:
			m.props = append(m.props, source.Prop{Key: "Importance", Value: "low"})
		}
	}
	switch v, _ := ps.int(pidLastVerb); v {
	case 102, 103:
		m.flags |= source.Answered
	case 104:
		m.flags |= source.Forwarded
	}
	if c := m.class; c != "" && !strings.EqualFold(c, "IPM.Note") {
		m.props = append(m.props, source.Prop{Key: "Message class", Value: c})
	}
	if s.keywords != 0 {
		seen := map[string]bool{}
		for _, k := range ps.strs(s.keywords) {
			if k = strings.TrimSpace(k); k != "" && !seen[k] {
				seen[k] = true
				m.labels = append(m.labels, k)
			}
		}
	}
}

// attRef is one row of a message's attachment table.
type attRef struct {
	nid  uint32
	size int64
}

func (s *store) attachmentRefs(o *obj) []attRef {
	if _, ok := o.subs[nidAttachmentTable]; !ok {
		return nil
	}
	ao, err := o.sub(nidAttachmentTable)
	if err != nil {
		return nil
	}
	t, err := ao.table()
	if err != nil {
		return nil
	}
	var out []attRef
	for i := 0; i < t.len(); i++ {
		n, _ := t.set(i, "").int(pidAttachSize)
		out = append(out, attRef{nid: t.rowID(i), size: max(n, 0)})
	}
	return out
}

// attachment is an opened attachment object.
type attachment struct {
	mail.Attachment
	o      *obj
	ps     propSet
	method int64
}

// Attachment methods.
const (
	attachByValue  = 1
	attachEmbedded = 5
)

// attachments opens the attachments of o, in table order; unreadable ones
// are left out (consistently, so indices stay stable).
func (s *store) attachments(o *obj) []attachment {
	var out []attachment
	for _, r := range s.attachmentRefs(o) {
		ao, err := o.sub(r.nid)
		if err != nil {
			continue
		}
		p, err := ao.pc()
		if err != nil {
			continue
		}
		ps := p.set("")
		a := attachment{o: ao, ps: ps}
		a.Index = len(out)
		a.method, _ = ps.int(pidAttachMethod)
		a.Filename = strings.TrimSpace(ps.str(pidAttachLongFilename))
		if a.Filename == "" {
			a.Filename = strings.TrimSpace(ps.str(pidAttachFilename))
		}
		if a.Filename == "" {
			a.Filename = strings.TrimSpace(ps.str(pidDisplayName))
		}
		a.ContentType = strings.ToLower(strings.TrimSpace(ps.str(pidAttachMimeTag)))
		a.ContentID = trimID(ps.str(pidAttachContentID))
		a.Inline = a.ContentID != ""
		if a.method == attachEmbedded {
			a.ContentType = "message/rfc822"
			if a.Filename == "" {
				if em, err := s.embedded(a); err == nil {
					if sp, err := em.pc(); err == nil {
						a.Filename = subject(sp.set("").str(pidSubject))
					}
				}
				a.NameGenerated = true
			}
			if a.Filename == "" {
				a.Filename = "attachment-" + strconv.Itoa(a.Index)
			}
			if !strings.HasSuffix(strings.ToLower(a.Filename), ".eml") {
				a.Filename += ".eml"
			}
			a.Size = r.size
		} else {
			if a.Filename == "" {
				a.Filename = "attachment-" + strconv.Itoa(a.Index) + strings.TrimSpace(ps.str(pidAttachExtension))
				a.NameGenerated = true
			}
			if a.ContentType == "" {
				a.ContentType = "application/octet-stream"
			}
			a.Size, _ = s.attachSize(a)
		}
		a.Filename = strings.ReplaceAll(a.Filename, "\x00", "")
		out = append(out, a)
	}
	return out
}

func (s *store) attachmentList(o *obj) []mail.Attachment {
	var out []mail.Attachment
	for _, a := range s.attachments(o) {
		out = append(out, a.Attachment)
	}
	return out
}

// embedded opens the message object of an embedded-message attachment.
func (s *store) embedded(a attachment) (*obj, error) {
	v, ok := a.ps.get(pidAttachData)
	if !ok || v.typ != ptObject {
		return nil, corrupt("embedded message without object")
	}
	b, _, err := a.o.data(v, 8)
	if err != nil || len(b) < 4 {
		return nil, corrupt("bad object reference")
	}
	return a.o.sub(le32(b, 0))
}

// objectData returns the subnode a PtypObject value points to.
func (s *store) objectSubnode(a attachment) (subnode, error) {
	v, _ := a.ps.get(pidAttachData)
	b, _, err := a.o.data(v, 8)
	if err != nil || len(b) < 4 {
		return subnode{}, corrupt("bad object reference")
	}
	sn, ok := a.o.subs[le32(b, 0)]
	if !ok {
		return subnode{}, corrupt("object subnode missing")
	}
	return sn, nil
}

func (s *store) attachSize(a attachment) (int64, error) {
	if a.ps.typ(pidAttachData) == ptObject {
		sn, err := s.objectSubnode(a)
		if err != nil {
			return 0, err
		}
		return s.db.size(sn.bidData)
	}
	return a.ps.size(pidAttachData)
}

// attachment returns attachment i of message nid with its bytes; embedded
// messages are synthesised as .eml.
func (s *store) attachment(nid uint32, i int) (mail.Attachment, []byte, error) {
	o, err := s.openMessage(nid)
	if err != nil {
		return mail.Attachment{}, nil, err
	}
	return s.attachmentData(o, i, 0)
}

func (s *store) attachmentData(o *obj, i, depth int) (mail.Attachment, []byte, error) {
	atts := s.attachments(o)
	if i < 0 || i >= len(atts) {
		return mail.Attachment{}, nil, fmt.Errorf("pst: attachment %d: %w", i, mail.ErrNotFound)
	}
	a := atts[i]
	if a.method == attachEmbedded {
		if depth >= maxEmbedDepth {
			return a.Attachment, nil, corrupt("embedded messages nested too deep")
		}
		em, err := s.embedded(a)
		if err != nil {
			return a.Attachment, nil, err
		}
		inner, err := s.readMessage(em, true)
		if err != nil {
			return a.Attachment, nil, err
		}
		var buf bytes.Buffer
		err = mail.WriteEML(&buf, &inner.Message, func(j int) ([]byte, error) {
			_, d, err := s.attachmentData(em, j, depth+1)
			return d, err
		})
		if buf.Len() > source.MaxMessageBytes {
			return a.Attachment, nil, fmt.Errorf("pst: embedded message: %w", source.ErrTooLarge)
		}
		return a.Attachment, buf.Bytes(), err
	}
	size, err := s.attachSize(a)
	if err != nil {
		return a.Attachment, nil, err
	}
	if size > source.MaxMessageBytes {
		return a.Attachment, nil, fmt.Errorf("pst: attachment of %d bytes: %w", size, source.ErrTooLarge)
	}
	var data []byte
	if a.ps.typ(pidAttachData) == ptObject {
		sn, err := s.objectSubnode(a)
		if err != nil {
			return a.Attachment, nil, err
		}
		data, _, err = s.db.read(sn.bidData, source.MaxMessageBytes)
		if err != nil {
			return a.Attachment, nil, err
		}
	} else {
		data = a.ps.bin(pidAttachData, source.MaxMessageBytes)
	}
	return a.Attachment, data, nil
}
