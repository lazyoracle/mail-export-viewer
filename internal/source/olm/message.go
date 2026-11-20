package olm

import (
	"mime"
	"path"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/lazyoracle/mail-export-viewer/internal/mail"
	"github.com/lazyoracle/mail-export-viewer/internal/source"
)

// maxPropBytes bounds one Props value; larger scalar fields are dropped.
const maxPropBytes = 4096

// parsed is one message document, interpreted.
type parsed struct {
	msg    mail.Message
	flags  source.Flags
	labels []string
	props  []source.Prop
	atts   []attRef // parallel to msg.Attachments
}

// attRef is the archive reference of one attachment.
type attRef struct{ url string }

// resolver finds an archive entry (and its size) for an attachment URL
// found in the message at msgName.
type resolver func(msgName, url string) (size int64, ok bool)

// key strips the OPFMessage/Copy/Get decoration from an element name
// (already lower-case): "opfmessagecopysubject" -> "subject".
func key(name string) string {
	rest, ok := strings.CutPrefix(name, "opfmessage")
	if !ok {
		return name
	}
	rest = strings.TrimPrefix(rest, "copy")
	return strings.TrimPrefix(rest, "get")
}

func truthy(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "1", "true", "yes", "y", "-1":
		return true
	}
	return false
}

var timeLayouts = []string{
	time.RFC3339Nano, "2006-01-02T15:04:05.999999999", "2006-01-02 15:04:05.999999999",
	"2006-01-02T15:04:05Z0700", time.RFC1123Z, time.RFC1123, time.RFC822Z,
}

func parseTime(s string) time.Time {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}
	}
	for _, l := range timeLayouts {
		if t, err := time.ParseInLocation(l, s, time.UTC); err == nil {
			if t.Year() < 1970 || t.Year() > 2200 {
				return time.Time{}
			}
			return t.UTC()
		}
	}
	return time.Time{}
}

// findMessage returns the element that holds the message fields: the
// first element (depth first) with a child named OPFMessage*.
func findMessage(n *node) *node {
	for _, k := range n.kids {
		if strings.HasPrefix(k.name, "opfmessage") {
			return n
		}
	}
	for _, k := range n.kids {
		if m := findMessage(k); m != nil {
			return m
		}
	}
	return nil
}

func addrList(n *node) []mail.Address {
	var out []mail.Address
	for _, k := range n.kids {
		email := k.attrOrChild("addressaddress", "emailaddress", "address", "email")
		name := k.attrOrChild("addressname", "name")
		if email == "" && name == "" {
			continue
		}
		out = append(out, mail.Address{Name: strings.TrimSpace(name), Email: strings.TrimSpace(email)})
	}
	if len(out) == 0 && len(n.kids) == 0 && strings.TrimSpace(n.text) != "" {
		out = mail.ParseAddressList(n.text)
	}
	return out
}

func collectCategories(n *node, out *[]string) {
	for _, a := range n.attrs {
		if strings.Contains(a.name, "name") || a.name == "value" {
			*out = append(*out, strings.TrimSpace(a.val))
		}
	}
	if len(n.kids) == 0 {
		for _, part := range strings.FieldsFunc(n.text, func(r rune) bool { return r == ',' || r == ';' }) {
			*out = append(*out, strings.TrimSpace(part))
		}
	}
	for _, k := range n.kids {
		collectCategories(k, out)
	}
}

func looksHTML(s string) bool {
	h := strings.ToLower(strings.TrimSpace(s))
	if len(h) > 2048 {
		h = h[:2048]
	}
	return strings.HasPrefix(h, "<html") || strings.HasPrefix(h, "<!doctype html")
}

// bodyIsHTML decides whether a plain body element actually holds HTML when
// there is no HTML element: a document start (<html, <!doctype html), or,
// when the message says it has HTML (hashtml), any body starting with a tag.
func bodyIsHTML(body string, hasHTML bool) bool {
	if looksHTML(body) {
		return true
	}
	return hasHTML && strings.HasPrefix(strings.TrimSpace(body), "<")
}

var (
	answeredKeys  = map[string]bool{"isreplied": true, "wasrepliedto": true, "hasbeenrepliedto": true, "isanswered": true}
	forwardedKeys = map[string]bool{"isforwarded": true, "wasforwarded": true}
)

// interpret turns a parsed document into a message. ok is false when the
// document holds no message (a contact, an event, ...). Unknown elements
// are ignored; unknown scalar ones become Props.
func interpret(root *node, msgName string, resolve resolver) (p *parsed, ok bool) {
	m := findMessage(root)
	if m == nil {
		return nil, false
	}
	p = &parsed{}
	var (
		body, html          string
		hasHTML             bool
		from, sender        []mail.Address
		displayTo           string
		sent, received      time.Time
		attNode             *node
		props               []source.Prop
		receivedProp        source.Prop
		haveSent, haveRecvd bool
	)
	for _, k := range m.kids {
		kk := key(k.name)
		txt := strings.TrimSpace(k.text)
		switch {
		case kk == "subject":
			p.msg.Subject = txt
		case kk == "messageid":
			p.msg.MessageID = strings.Trim(txt, "<> ")
		case kk == "inreplyto" || kk == "inreplytoid":
			p.msg.InReplyTo = strings.Trim(txt, "<> ")
		case kk == "references":
			for _, r := range strings.Fields(txt) {
				p.msg.References = append(p.msg.References, strings.Trim(r, "<>"))
			}
		case kk == "fromaddresses" || kk == "fromaddress" || kk == "from":
			from = addrList(k)
		case kk == "senderaddress" || kk == "senderaddresses" || kk == "sender":
			sender = addrList(k)
		case kk == "toaddresses" || kk == "toaddress" || kk == "to":
			p.msg.To = append(p.msg.To, addrList(k)...)
		case kk == "ccaddresses" || kk == "ccaddress" || kk == "cc":
			p.msg.Cc = append(p.msg.Cc, addrList(k)...)
		case kk == "bccaddresses" || kk == "bccaddress" || kk == "bcc":
			p.msg.Bcc = append(p.msg.Bcc, addrList(k)...)
		case kk == "displayto":
			displayTo = txt
		case kk == "body" || kk == "textbody" || kk == "plaintextbody":
			body = k.text
		case kk == "htmlbody" || kk == "html":
			html = k.text
		case kk == "hashtml" || kk == "ishtml":
			hasHTML = truthy(txt)
		case kk == "senttime" || kk == "sentdate":
			sent, haveSent = parseTime(txt), txt != ""
		case kk == "receivedtime" || kk == "receiveddate":
			received, haveRecvd = parseTime(txt), txt != ""
			receivedProp = source.Prop{Key: k.orig, Value: txt}
		case kk == "isread" || kk == "read":
			if txt != "" && !truthy(txt) {
				p.flags |= source.Unread
			}
		case kk == "isflagged" || kk == "flagged" || kk == "flagstatus":
			if truthy(txt) || (kk == "flagstatus" && txt != "" && txt != "0" && !strings.EqualFold(txt, "none") && !strings.EqualFold(txt, "notflagged")) {
				p.flags |= source.Starred
			}
		case kk == "priority" || kk == "importance":
			switch strings.ToLower(txt) {
			case "high", "1", "2":
				p.flags |= source.Important
			}
		case kk == "isdraft" || kk == "draft":
			if truthy(txt) {
				p.flags |= source.Draft
			}
		case answeredKeys[kk]:
			if truthy(txt) {
				p.flags |= source.Answered
			}
		case forwardedKeys[kk]:
			if truthy(txt) {
				p.flags |= source.Forwarded
			}
		case kk == "isdeleted" || kk == "deleted":
			if truthy(txt) {
				p.flags |= source.Deleted
			}
		case kk == "isjunk" || kk == "junk":
			if truthy(txt) {
				p.flags |= source.Junk
			}
		case strings.Contains(kk, "categor"):
			collectCategories(k, &p.labels)
		case strings.Contains(kk, "attachment") && len(k.kids) > 0:
			attNode = k
		case len(k.kids) == 0 && len(k.attrs) == 0 && txt != "" && len(txt) <= maxPropBytes && utf8.ValidString(txt):
			props = append(props, source.Prop{Key: k.orig, Value: txt})
		}
	}
	switch {
	case len(from) > 0:
		p.msg.From = from[0]
	case len(sender) > 0:
		p.msg.From = sender[0]
	}
	if len(p.msg.To) == 0 && displayTo != "" {
		for _, part := range strings.Split(displayTo, ";") {
			if part = strings.TrimSpace(part); part != "" {
				p.msg.To = append(p.msg.To, mail.Address{Name: part})
			}
		}
	}
	switch {
	case haveSent && !sent.IsZero():
		p.msg.Date = sent
		if haveRecvd {
			props = append(props, receivedProp)
		}
	case !received.IsZero():
		p.msg.Date = received
	}
	if html == "" && bodyIsHTML(body, hasHTML) {
		html, body = body, ""
	}
	p.msg.Text, p.msg.HTML = body, html
	p.labels = dedupe(p.labels)
	p.props = props
	if attNode != nil {
		for i, a := range attNode.kids {
			url := a.attrOrChild("attachmenturl", "url", "path")
			name := a.attrOrChild("attachmentname", "filename", "name")
			ctype := a.attrOrChild("contenttype", "mimetype")
			cid := strings.Trim(a.attrOrChild("contentid", "cid"), "<> ")
			ext := a.attrOrChild("contentextension", "extension")
			att := mail.Attachment{Index: len(p.msg.Attachments), Filename: name, ContentID: cid}
			if att.Filename == "" {
				att.Filename = path.Base(strings.ReplaceAll(url, `\`, "/"))
				if att.Filename == "." || att.Filename == "/" {
					att.Filename = ""
				}
			}
			if att.Filename == "" {
				e := ""
				if ext != "" {
					e = "." + strings.TrimPrefix(ext, ".")
				}
				att.Filename, att.NameGenerated = "attachment-"+strconv.Itoa(i+1)+e, true
			}
			if ctype == "" {
				ctype = mime.TypeByExtension(path.Ext(att.Filename))
			}
			if ctype == "" {
				ctype = "application/octet-stream"
			}
			if mt, _, err := mime.ParseMediaType(ctype); err == nil {
				ctype = mt
			}
			att.ContentType = strings.ToLower(ctype)
			att.Inline = cid != ""
			if size, ok := resolve(msgName, url); ok {
				att.Size = size
			}
			p.msg.Attachments = append(p.msg.Attachments, att)
			p.atts = append(p.atts, attRef{url: url})
		}
	}
	return p, true
}

func dedupe(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		s = strings.TrimSpace(strings.ToValidUTF8(s, "�"))
		if s != "" && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}
