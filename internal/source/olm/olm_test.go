package olm

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/lazyoracle/mail-export-viewer/internal/mail"
	"github.com/lazyoracle/mail-export-viewer/internal/source"
	"github.com/lazyoracle/mail-export-viewer/internal/source/sourcetest"
	"github.com/lazyoracle/mail-export-viewer/internal/testutil"
)

type E = testutil.OLMEntry

func addr(n, e string) testutil.OLMAddr { return testutil.OLMAddr{Name: n, Email: e} }

func openT(t *testing.T, entries ...E) source.Source {
	t.Helper()
	p := testutil.WriteOLM(t, filepath.Join(t.TempDir(), "backup.olm"), entries...)
	s, err := source.Candidate{Format: source.OLM, Path: p, Rel: "backup.olm"}.Open()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func scan(t *testing.T, s source.Source) ([]source.Entry, []error) {
	t.Helper()
	var es []source.Entry
	var ws []error
	err := s.Scan(context.Background(), func(e source.Entry) error { e.Resolve(); es = append(es, e); return nil }, func(e error) { ws = append(ws, e) })
	if err != nil {
		t.Fatal(err)
	}
	return es, ws
}

func cat(parts ...[]E) []E {
	var out []E
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

func fixture() []E {
	full := testutil.OLMMsg{
		Subject: "Quarterly report", MessageID: "<q1@example.com>", InReplyTo: "<q0@example.com>",
		From: addr("Alice", "alice@example.com"), To: []testutil.OLMAddr{addr("Bob", "bob@example.com")},
		Cc: []testutil.OLMAddr{addr("", "carol@example.com")}, Body: "numbers attached", HTML: "<p>numbers <img src=\"cid:logo1\"></p>",
		Sent: "2019-03-05T10:00:00", Received: "2019-03-05T10:00:05", Unread: true, Flagged: true, Priority: "1",
		Categories: []string{"Red", "Work"}, Extra: [][2]string{{"OPFMessageGetSize", "1234"}},
		Attachments: []testutil.OLMAtt{
			{Name: "report.csv", Type: "text/csv", Data: []byte("a,b\n1,2\n")},
			{Name: "logo.png", Type: "image/png", CID: "logo1", Data: []byte("\x89PNGdata")},
			{Name: "gone.pdf", Type: "application/pdf", Missing: true},
		},
	}
	plain := testutil.OLMMsg{Subject: "Hello", MessageID: "h1@example.com", From: addr("Bob", "bob@example.com"),
		To: []testutil.OLMAddr{addr("Alice", "alice@example.com")}, Body: "hi there", Sent: "2020-01-02T03:04:05Z"}
	legacy := testutil.OLMMsg{Subject: "Old mail", From: addr("Dan", "dan@example.com"), Body: "legacy", Received: "2011-06-01T08:00:00"}
	return cat(
		testutil.OLMMessageEntries("Accounts/me@example.com/com.microsoft.__Messages/Inbox/msg1.olk15Message", full, "Accounts/me@example.com/com.microsoft.__Attachments"),
		testutil.OLMMessageEntries("Accounts/me@example.com/com.microsoft.__Messages/Inbox/Sub/msg2.olk15Message", plain, "Accounts/me@example.com/com.microsoft.__Attachments"),
		testutil.OLMMessageEntries("Local/Messages/Archive/message_00001.xml", legacy, "Local/Attachments"),
	)
}

func TestConformance(t *testing.T) {
	s := openT(t, fixture()...)
	sourcetest.Check(t, s,
		source.Locator{Name: "Local/Messages/nope/message_9.xml"},
		source.Locator{Name: "Accounts/x/com.microsoft.__Attachments/0-report.csv"},
		source.Locator{Name: "../../etc/passwd"},
	)
}

func TestScanFields(t *testing.T) {
	s := openT(t, fixture()...)
	es, ws := scan(t, s)
	if len(ws) != 0 {
		t.Fatalf("warnings: %v", ws)
	}
	if len(es) != 3 {
		t.Fatalf("got %d entries", len(es))
	}
	e := es[0]
	if e.Folder != "backup/Inbox" {
		t.Errorf("folder %q", e.Folder)
	}
	if es[1].Folder != "backup/Inbox/Sub" || es[2].Folder != "backup/Archive" {
		t.Errorf("folders %q %q", es[1].Folder, es[2].Folder)
	}
	if want := []string{"Red", "Work"}; !reflect.DeepEqual(e.Labels, want) {
		t.Errorf("labels %v", e.Labels)
	}
	want := source.Unread | source.Starred | source.Important | source.HasAttachment
	if e.Flags != want {
		t.Errorf("flags %v, want %v", e.Flags, want)
	}
	m := e.Msg
	if m.Subject != "Quarterly report" || m.MessageID != "q1@example.com" || m.InReplyTo != "q0@example.com" {
		t.Errorf("envelope %+v", m.Envelope)
	}
	if m.From != (mail.Address{Name: "Alice", Email: "alice@example.com"}) || len(m.To) != 1 || m.To[0].Email != "bob@example.com" || len(m.Cc) != 1 {
		t.Errorf("people %+v", m.Envelope)
	}
	if !m.Date.Equal(time.Date(2019, 3, 5, 10, 0, 0, 0, time.UTC)) {
		t.Errorf("date %v", m.Date)
	}
	if m.Text != "numbers attached" {
		t.Errorf("text %q", m.Text)
	}
	if !reflect.DeepEqual(m.AttachmentNames, []string{"report.csv", "logo.png", "gone.pdf"}) {
		t.Errorf("attachment names %v", m.AttachmentNames)
	}
	found := false
	for _, p := range e.Props {
		if p.Key == "OPFMessageGetSize" && p.Value == "1234" {
			found = true
		}
		if strings.EqualFold(p.Key, "OPFMessageCopySubject") || strings.EqualFold(p.Key, "OPFMessageCopyBody") {
			t.Errorf("consumed field in props: %v", p)
		}
	}
	if !found {
		t.Errorf("props %v lack OPFMessageGetSize", e.Props)
	}
	if es[1].Flags != 0 || es[1].Msg.MessageID != "h1@example.com" || !es[1].Msg.Date.Equal(time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)) {
		t.Errorf("plain: %v %+v", es[1].Flags, es[1].Msg.Envelope)
	}
	if !es[2].Msg.Date.Equal(time.Date(2011, 6, 1, 8, 0, 0, 0, time.UTC)) {
		t.Errorf("received fallback: %v", es[2].Msg.Date)
	}
}

func TestLoadAndAttachments(t *testing.T) {
	s := openT(t, fixture()...)
	es, _ := scan(t, s)
	m, err := s.Load(es[0].Loc)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(m.HTML, "cid:logo1") || m.Text != "numbers attached" {
		t.Errorf("bodies %q %q", m.Text, m.HTML)
	}
	if len(m.Attachments) != 3 {
		t.Fatalf("attachments %+v", m.Attachments)
	}
	if a := m.Attachments[1]; !a.Inline || a.ContentID != "logo1" || a.ContentType != "image/png" || a.Size != 8 {
		t.Errorf("inline att %+v", a)
	}
	if a := m.Attachments[0]; a.Inline || a.Size != 8 {
		t.Errorf("att %+v", a)
	}
	if cid, ok := mail.FindCID(m.Attachments, "logo1"); !ok {
		t.Error("FindCID")
	} else if _, d, err := s.Attachment(es[0].Loc, cid.Index); err != nil || string(d) != "\x89PNGdata" {
		t.Errorf("cid data %q %v", d, err)
	}
	if a, d, err := s.Attachment(es[0].Loc, 2); err != nil || len(d) != 0 || a.Size != 0 {
		t.Errorf("dangling attachment: %+v %q %v", a, d, err)
	}
	raw, err := s.Raw(es[0].Loc)
	if err != nil {
		t.Fatal(err)
	}
	back := mail.Parse(raw, time.Time{})
	if back.Subject != "Quarterly report" || len(back.Attachments) != 3 {
		t.Errorf("round trip %+v", back.Envelope)
	}
}

func TestHTMLInBodyElement(t *testing.T) {
	x := `<emails><email><OPFMessageCopySubject>H</OPFMessageCopySubject><OPFMessageGetHasHTML>1</OPFMessageGetHasHTML>` +
		`<OPFMessageCopyBody>&lt;html&gt;&lt;body&gt;&lt;b&gt;bold&lt;/b&gt; text&lt;/body&gt;&lt;/html&gt;</OPFMessageCopyBody></email></emails>`
	s := openT(t, E{Name: "Local/Messages/Inbox/message_1.xml", Data: []byte(x)})
	es, _ := scan(t, s)
	m, _ := s.Load(es[0].Loc)
	if !strings.Contains(m.HTML, "<b>bold</b>") || m.Text != "" {
		t.Errorf("html %q text %q", m.HTML, m.Text)
	}
	if !strings.Contains(es[0].Msg.Text, "bold text") {
		t.Errorf("summary text %q", es[0].Msg.Text)
	}
}

func TestToleranceCaseAndUnknown(t *testing.T) {
	x := `<emails><email><opfmessagecopysubject>lower</opfmessagecopysubject><Whatever><Nested>x</Nested></Whatever>` +
		`<OPFMessageCopyDisplayTo>Zed</OPFMessageCopyDisplayTo><OPFMessageGetIsRead>false</OPFMessageGetIsRead></email></emails>`
	s := openT(t, E{Name: "Local/Messages/Inbox/message_1.xml", Data: []byte(x)})
	es, ws := scan(t, s)
	if len(ws) != 0 || len(es) != 1 {
		t.Fatalf("%v %v", es, ws)
	}
	if es[0].Msg.Subject != "lower" || len(es[0].Msg.To) != 1 || es[0].Msg.To[0].Name != "Zed" || !es[0].Flags.Has(source.Unread) {
		t.Errorf("%+v %v", es[0].Msg.Envelope, es[0].Flags)
	}
}

func TestNonMailSkippedWithSummary(t *testing.T) {
	contact := []byte(`<contacts><contact><OPFContactCopyDisplayName>X</OPFContactCopyDisplayName></contact></contacts>`)
	cal := []byte(`<appointments><appointment><OPFCalendarEventCopySummary>E</OPFCalendarEventCopySummary></appointment></appointments>`)
	es := cat(fixture(), []E{
		{Name: "Local/Contacts/contact_1.xml", Data: contact},
		{Name: "Local/Contacts/contact_2.xml", Data: contact},
		{Name: "Local/Calendar/event_1.xml", Data: cal},
		{Name: "Accounts/me@example.com/com.microsoft.__Events/a.olk15Event", Data: cal},
		{Name: "Local/Messages/Inbox/message_9.xml", Data: contact}, // mail path, other content
		{Name: "Local/Categories.xml", Data: []byte(`<categories/>`)},
	})
	s := openT(t, es...)
	got, ws := scan(t, s)
	if len(got) != 3 {
		t.Errorf("entries %d", len(got))
	}
	if len(ws) != 1 {
		t.Fatalf("want one summary warning, got %v", ws)
	}
	w := ws[0].Error()
	for _, want := range []string{"5", "non-mail", "contact", "calendar"} {
		if !strings.Contains(w, want) {
			t.Errorf("warning %q lacks %q", w, want)
		}
	}
}

func TestCorruptMessageWarnsAndContinues(t *testing.T) {
	es := cat(fixture(), []E{{Name: "Local/Messages/Inbox/message_bad.xml", Data: []byte("<emails><email><OPFMessageCopySubject>x")}})
	s := openT(t, es...)
	got, ws := scan(t, s)
	if len(got) != 3 || len(ws) != 1 {
		t.Errorf("entries %d warnings %v", len(got), ws)
	}
}

func TestHostileNames(t *testing.T) {
	m := testutil.OLMMsg{Subject: "evil", From: addr("E", "e@example.com"), Body: "x",
		Attachments: []testutil.OLMAtt{{Name: "p", Type: "text/plain", URL: "../../../etc/passwd", Missing: true}}}
	es := cat(fixture(), []E{{Name: "etc/passwd", Data: []byte("secret")}},
		testutil.OLMMessageEntries("Accounts/a/com.microsoft.__Messages/../../../../tmp/x/msg.olk15Message", m, "x"),
		testutil.OLMMessageEntries("/abs/Local/Messages/..\\..\\win/message_2.xml", m, "x"),
	)
	s := openT(t, es...)
	got, _ := scan(t, s)
	if len(got) != 5 {
		t.Fatalf("entries %d", len(got))
	}
	for _, e := range got {
		if strings.Contains(e.Folder, "..") || strings.Contains(e.Folder, "\\") || strings.Contains(e.Folder, "//") {
			t.Errorf("unsafe folder %q", e.Folder)
		}
		if e.Msg.Subject == "evil" {
			if a, d, err := s.Attachment(e.Loc, 0); err != nil || len(d) != 0 || a.Size != 0 {
				t.Errorf("traversal attachment resolved: %+v %q %v", a, d, err)
			}
		}
	}
}

func TestLimits(t *testing.T) {
	defer func(a int, c int64) { maxEntries, maxScanBytes = a, c }(maxEntries, maxScanBytes)
	maxEntries = 3
	p := testutil.WriteOLM(t, filepath.Join(t.TempDir(), "a.olm"), fixture()...)
	if _, err := (source.Candidate{Format: source.OLM, Path: p, Rel: "a.olm"}).Open(); err == nil {
		t.Error("too many entries should fail Open")
	}
	maxEntries = 1000
	maxScanBytes = 1500 // two messages fit, then the budget is spent
	s := openT(t, fixture()...)
	got, ws := scan(t, s)
	if len(got) >= 3 || len(ws) == 0 {
		t.Errorf("budget not enforced: %d entries, warnings %v", len(got), ws)
	}
}

func TestTooLarge(t *testing.T) {
	defer func(n int64) { maxXMLBytes = n }(maxXMLBytes)
	maxXMLBytes = 400
	s := openT(t, fixture()...)
	var locs []source.Locator
	s.Scan(context.Background(), func(e source.Entry) error {
		locs = append(locs, e.Loc)
		return nil
	}, func(error) {})
	var big int
	for _, l := range locs {
		if _, err := s.Load(l); errors.Is(err, source.ErrTooLarge) {
			big++
			if _, err := s.Raw(l); !errors.Is(err, source.ErrTooLarge) {
				t.Errorf("Raw: %v", err)
			}
			if _, _, err := s.Attachment(l, 0); !errors.Is(err, source.ErrTooLarge) {
				t.Errorf("Attachment: %v", err)
			}
		}
	}
	if big == 0 || len(locs) != 3 {
		t.Errorf("%d too large of %d listed", big, len(locs))
	}
}

func TestFolderLister(t *testing.T) {
	es := cat(fixture(), []E{{Name: "Accounts/me@example.com/com.microsoft.__Messages/Empty/Deep/"}})
	s := openT(t, es...)
	scan(t, s)
	fl, ok := s.(source.FolderLister)
	if !ok {
		t.Fatal("not a FolderLister")
	}
	got := map[string]bool{}
	for _, f := range fl.Folders() {
		got[f] = true
	}
	for _, want := range []string{"backup/Inbox", "backup/Inbox/Sub", "backup/Archive", "backup/Empty/Deep", "backup/Empty"} {
		if !got[want] {
			t.Errorf("missing folder %q in %v", want, fl.Folders())
		}
	}
}

func TestMultipleAccountsKeepAccountInFolder(t *testing.T) {
	m := testutil.OLMMsg{Subject: "s", From: addr("A", "a@example.com"), Body: "b"}
	s := openT(t, cat(
		testutil.OLMMessageEntries("Accounts/one/com.microsoft.__Messages/Inbox/a.olk15Message", m, "x"),
		testutil.OLMMessageEntries("Accounts/two/com.microsoft.__Messages/Inbox/a.olk15Message", m, "x"),
	)...)
	es, _ := scan(t, s)
	if es[0].Folder == es[1].Folder {
		t.Errorf("folders merged: %q", es[0].Folder)
	}
}

func TestDiscoverFindsOLM(t *testing.T) {
	dir := t.TempDir()
	testutil.WriteOLM(t, filepath.Join(dir, "x.olm"), fixture()...)
	d, err := source.Discover(dir, source.Limits{})
	if err != nil || len(d.Candidates) != 1 || d.Candidates[0].Format != source.OLM {
		t.Fatalf("%+v %v", d, err)
	}
	s, err := d.Candidates[0].Open()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	es, _ := scan(t, s)
	if len(es) != 3 || !strings.HasPrefix(es[0].Folder, "x/") {
		t.Errorf("%d entries, folder %q", len(es), es[0].Folder)
	}
}
