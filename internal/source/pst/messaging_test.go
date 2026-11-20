package pst

import (
	"bytes"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/lazyoracle/mail-export-viewer/internal/mail"
	"github.com/lazyoracle/mail-export-viewer/internal/source"
	"github.com/lazyoracle/mail-export-viewer/internal/source/pst/pstgen"
)

func openFixture(t *testing.T, f *pstgen.File) *store {
	t.Helper()
	st, err := openStore(openBytes(t, f.Bytes()))
	if err != nil {
		t.Fatalf("openStore: %v", err)
	}
	return st
}

func folderPaths(t *testing.T, st *store) []string {
	t.Helper()
	fs, warns := st.folders()
	if len(warns) > 0 {
		t.Errorf("folder warnings: %v", warns)
	}
	var out []string
	for _, f := range fs {
		out = append(out, strings.Join(f.path, "/"))
	}
	return out
}

func TestStoreFolders(t *testing.T) {
	f := fixture(pstgen.CryptPermute)
	st := openFixture(t, f)
	if got, want := folderPaths(t, st), []string{"Inbox", "Inbox/Sub", "Sent Items", "Empty"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("folders = %q, want %q", got, want)
	}
	if st.keywords != pstgen.KeywordsPropID {
		t.Errorf("keywords prop = %#x", st.keywords)
	}
	ids, err := st.contents(f.Folders[0].NID)
	if err != nil || len(ids) != 3 || ids[0] != f.Folders[0].Messages[0].NID {
		t.Fatalf("contents = %x, %v", ids, err)
	}

	f.NoIPMEntryID = true
	st = openFixture(t, f)
	if got := folderPaths(t, st); len(got) != 4 || got[0] != "Inbox" {
		t.Fatalf("fallback folders = %q", got)
	}
}

func TestMessagePlain(t *testing.T) {
	f := fixture(pstgen.CryptNone)
	st := openFixture(t, f)
	m, err := st.message(f.Folders[0].Messages[0].NID, true)
	if err != nil {
		t.Fatal(err)
	}
	if m.class != "IPM.Note" || m.Subject != "RE: Hello" || m.MessageID != "m1@example.com" || m.InReplyTo != "m0@example.com" {
		t.Errorf("envelope: %q %q %q %q", m.class, m.Subject, m.MessageID, m.InReplyTo)
	}
	if !m.Date.Equal(fixtureDate) {
		t.Errorf("date = %v", m.Date)
	}
	if m.From != (mail.Address{Name: "Alice Example", Email: "alice@example.com"}) {
		t.Errorf("from = %+v", m.From)
	}
	wantTo := []mail.Address{{Name: "Bob Example", Email: "bob@example.org"}}
	wantCc := []mail.Address{{Name: "Carol Example", Email: "carol@example.com"}}
	wantBcc := []mail.Address{{Name: "Dave Example", Email: "dave@example.net"}}
	if !reflect.DeepEqual(m.To, wantTo) || !reflect.DeepEqual(m.Cc, wantCc) || !reflect.DeepEqual(m.Bcc, wantBcc) {
		t.Errorf("recipients: %+v %+v %+v", m.To, m.Cc, m.Bcc)
	}
	if m.Text != "Hello Bob\nSecond line" || m.HTML != "" {
		t.Errorf("bodies: %q %q", m.Text, m.HTML)
	}
	if m.Header.Get("X-Custom") != "yes" || len(m.Header) != 7 {
		t.Errorf("header: %v", m.Header)
	}
	if !reflect.DeepEqual(m.labels, []string{"Red Category", "Project X"}) {
		t.Errorf("labels = %q", m.labels)
	}
	if m.flags != 0 {
		t.Errorf("flags = %v", m.flags)
	}
	want := []mail.Attachment{
		{Index: 0, Filename: "Quarterly report.pdf", ContentType: "application/pdf", Size: 20000},
		{Index: 1, Filename: "logo.png", ContentType: "image/png", ContentID: "img1@example.com", Size: 9, Inline: true},
		{Index: 2, Filename: "Inner note.eml", ContentType: "message/rfc822", Size: 0},
	}
	if !reflect.DeepEqual(m.Attachments, want) {
		t.Errorf("attachments:\n%+v\nwant\n%+v", m.Attachments, want)
	}
	if s := m.Summary(); !s.HasAttachment || !reflect.DeepEqual(s.AttachmentNames, []string{"Quarterly report.pdf", "logo.png", "Inner note.eml"}) {
		t.Errorf("summary attachments %v %q", s.HasAttachment, s.AttachmentNames)
	}

	a, data, err := st.attachment(f.Folders[0].Messages[0].NID, 0)
	if err != nil || a.Filename != "Quarterly report.pdf" || !bytes.Equal(data, blob(20000, 5)) {
		t.Errorf("attachment 0: %+v %d %v", a, len(data), err)
	}
	a, data, err = st.attachment(f.Folders[0].Messages[0].NID, 2)
	if err != nil || a.ContentType != "message/rfc822" {
		t.Fatalf("embedded: %+v %v", a, err)
	}
	inner := mail.Parse(data, fixtureDate)
	if inner.Subject != "Inner note" || inner.Text != "inner body" || inner.From.Email != "erin@example.com" ||
		len(inner.Attachments) != 1 || inner.Attachments[0].Filename != "inner.txt" {
		t.Errorf("embedded message: %+v", inner)
	}
	if _, ad, _ := mail.AttachmentData(data, 0); string(ad) != "inner attachment" {
		t.Errorf("embedded attachment data %q", ad)
	}
	for _, i := range []int{-1, 3} {
		if _, _, err := st.attachment(f.Folders[0].Messages[0].NID, i); !errors.Is(err, mail.ErrNotFound) {
			t.Errorf("attachment(%d) = %v", i, err)
		}
	}
}

func TestMessageHTMLAndFlags(t *testing.T) {
	f := fixture(pstgen.CryptCyclic)
	st := openFixture(t, f)
	m, err := st.message(f.Folders[0].Messages[1].NID, true)
	if err != nil {
		t.Fatal(err)
	}
	if m.Subject != "Café menu" || m.HTML != "<html><body><p>Café au lait</p></body></html>" || m.Text != "" {
		t.Errorf("subject %q html %q text %q", m.Subject, m.HTML, m.Text)
	}
	if m.From.Email != "bob@example.org" || !m.Date.Equal(fixtureDate.Add(3600e9)) || m.MessageID != "m2@example.com" {
		t.Errorf("from %+v date %v id %q", m.From, m.Date, m.MessageID)
	}
	if want := source.Unread | source.Starred | source.Important | source.Answered; m.flags != want {
		t.Errorf("flags = %v, want %v", m.flags, want)
	}
	if m.Summary().Text != "Café au lait" {
		t.Errorf("summary text %q", m.Summary().Text)
	}

	contact, err := st.message(f.Folders[0].Messages[2].NID, false)
	if err != nil || isMail(contact.class) {
		t.Errorf("contact: %q %v", contact.class, err)
	}
}

func TestMessageRTF(t *testing.T) {
	f := fixture(pstgen.CryptPermute)
	st := openFixture(t, f)
	m, err := st.message(f.Folders[0].Folders[0].Messages[0].NID, true)
	if err != nil {
		t.Fatal(err)
	}
	if m.HTML != "<html><body><p>Rich café</p></body></html>" || m.Text != "" {
		t.Errorf("rtf html %q text %q", m.HTML, m.Text)
	}
	if m.flags != source.Draft {
		t.Errorf("flags = %v", m.flags)
	}
	if !reflect.DeepEqual(m.props, []source.Prop{{Key: "Importance", Value: "low"}}) {
		t.Errorf("props = %v", m.props)
	}
	m, err = st.message(f.Folders[0].Folders[0].Messages[1].NID, true)
	if err != nil || m.Text != "Plain text\n" || m.HTML != "" || !isMail(m.class) {
		t.Errorf("rtf text %q html %q class %q err %v", m.Text, m.HTML, m.class, err)
	}
}

func TestMessageBigBody(t *testing.T) {
	f := fixture(pstgen.CryptNone)
	st := openFixture(t, f)
	nid := f.Folders[1].Messages[0].NID
	m, err := st.message(nid, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Text) > scanTextLimit || !strings.HasPrefix(m.Text, "big body line\n") {
		t.Errorf("scan text %d bytes", len(m.Text))
	}
	m, err = st.message(nid, true)
	if err != nil || len(m.Text) != 3<<20/15*14 {
		t.Errorf("full text %d bytes, %v", len(m.Text), err)
	}
}

func TestMessageStale(t *testing.T) {
	f := fixture(pstgen.CryptNone)
	st := openFixture(t, f)
	for _, nid := range []uint32{0x7FFFFFE4, f.Folders[0].NID, 0x21} {
		if _, err := st.message(nid, true); !errors.Is(err, source.ErrStale) {
			t.Errorf("message(%#x) = %v, want ErrStale", nid, err)
		}
	}
}
