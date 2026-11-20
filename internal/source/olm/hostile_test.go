package olm

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"runtime"
	"strings"
	"testing"
	"time"
	"unicode/utf16"

	"github.com/lazyoracle/mail-export-viewer/internal/source"
	"github.com/lazyoracle/mail-export-viewer/internal/testutil"
)

func msgXML(body string) []byte {
	return []byte(`<emails><email><OPFMessageCopySubject>s</OPFMessageCopySubject><OPFMessageCopyBody>` + body +
		`</OPFMessageCopyBody></email></emails>`)
}

func TestXMLTokenFlood(t *testing.T) {
	for _, tc := range []struct {
		n       int
		wantErr bool
	}{{300_000, false}, {1_500_000, true}} {
		x := msgXML(strings.Repeat("x<!---->", tc.n))
		start := time.Now()
		root, err := parseXML(bytes.NewReader(x))
		if d := time.Since(start); d > 5*time.Second {
			t.Errorf("n=%d took %v (quadratic?)", tc.n, d)
		}
		if tc.wantErr != (err != nil) {
			t.Errorf("n=%d: err %v", tc.n, err)
		}
		if err == nil {
			if m, _ := interpret(root, "x.xml", func(string, string) (int64, bool) { return 0, false }); m == nil || len(m.msg.Text) != tc.n {
				t.Errorf("text length wrong")
			}
		}
	}
}

func TestPITokenFlood(t *testing.T) {
	x := msgXML(strings.Repeat("<?p?>", 1_200_000))
	if _, err := parseXML(bytes.NewReader(x)); err == nil {
		t.Error("PI flood accepted")
	}
}

func TestUTF16Document(t *testing.T) {
	u := utf16.Encode([]rune(`<?xml version="1.0" encoding="UTF-16"?><emails><email><OPFMessageCopySubject>héllo</OPFMessageCopySubject></email></emails>`))
	b := []byte{0xFF, 0xFE}
	for _, c := range u {
		b = append(b, byte(c), byte(c>>8))
	}
	s := openT(t, E{Name: "Local/Messages/Inbox/message_1.xml", Data: b})
	es, ws := scan(t, s)
	if len(es) != 1 || len(ws) != 0 || es[0].Msg.Subject != "héllo" {
		t.Errorf("%v %v", es, ws)
	}
}

func TestScanChargesTooLarge(t *testing.T) {
	defer func(n int64) { maxXMLBytes = n }(maxXMLBytes)
	defer func(n int64) { maxScanBytes = n }(maxScanBytes)
	maxXMLBytes = 100
	maxScanBytes = 250
	var es []E
	for i := 0; i < 20; i++ {
		es = append(es, E{Name: "Local/Messages/Inbox/message_" + string(rune('a'+i)) + ".xml", Data: msgXML(strings.Repeat("y", 500))})
	}
	s := openT(t, es...)
	got, ws := scan(t, s)
	if len(got) >= 20 || len(ws) == 0 {
		t.Errorf("too-large entries were not charged: %d entries, %v", len(got), ws)
	}
}

func TestDecompressionBudgetScalesWithArchive(t *testing.T) {
	defer func(a int64, b int64) { budgetFloor, budgetFactor = a, b }(budgetFloor, budgetFactor)
	budgetFloor, budgetFactor = 1, 2
	var es []E
	for i := 0; i < 40; i++ {
		es = append(es, E{Name: "Local/Messages/Inbox/message_" + string(rune('a'+i%26)) + string(rune('a'+i/26)) + ".xml", Data: msgXML(strings.Repeat("z", 50_000))})
	}
	s := openT(t, es...)
	got, ws := scan(t, s)
	if len(got) >= 40 || len(ws) == 0 || !strings.Contains(ws[0].Error(), "budget") {
		t.Errorf("budget did not trip: %d entries, %v", len(got), ws)
	}
	if b := scanBudget(1 << 40); b != 2<<30 {
		t.Errorf("cap: %d", b)
	}
}

func TestDuplicateEntriesSkipped(t *testing.T) {
	a := msgXML("first")
	b := []byte(strings.Replace(string(msgXML("second")), ">s<", ">dup<", 1))
	s := openT(t, E{Name: "Local/Messages/Inbox/message_1.xml", Data: a}, E{Name: "Local/Messages/Inbox/message_1.xml", Data: b})
	es, ws := scan(t, s)
	if len(es) != 1 || es[0].Msg.Subject != "s" {
		t.Fatalf("entries %+v", es)
	}
	if len(ws) != 1 || !strings.Contains(ws[0].Error(), "duplicate") {
		t.Errorf("warnings %v", ws)
	}
}

func attMsg(n int) testutil.OLMMsg {
	m := testutil.OLMMsg{Subject: "a", Body: "b"}
	for i := 0; i < n; i++ {
		m.Attachments = append(m.Attachments, testutil.OLMAtt{Name: "f" + string(rune('0'+i)) + ".bin", Type: "application/octet-stream", Data: make([]byte, 600)})
	}
	return m
}

func TestAttachmentTooLarge(t *testing.T) {
	defer func(n int64) { maxMessageBytes = n }(maxMessageBytes)
	maxMessageBytes = 500
	s := openT(t, testutil.OLMMessageEntries("Local/Messages/Inbox/message_1.xml", attMsg(1), "Local/Attachments")...)
	es, _ := scan(t, s)
	if _, _, err := s.Attachment(es[0].Loc, 0); !errors.Is(err, source.ErrTooLarge) {
		t.Errorf("Attachment: %v", err)
	}
	if _, err := s.Raw(es[0].Loc); !errors.Is(err, source.ErrTooLarge) {
		t.Errorf("Raw: %v", err)
	}
}

func TestRawTotalIncludesAllAttachmentsAndExpansion(t *testing.T) {
	defer func(n int64) { maxMessageBytes = n }(maxMessageBytes)
	maxMessageBytes = 1000
	s := openT(t, testutil.OLMMessageEntries("Local/Messages/Inbox/message_1.xml", attMsg(3), "Local/Attachments")...)
	es, _ := scan(t, s)
	for i := 0; i < 3; i++ {
		if _, d, err := s.Attachment(es[0].Loc, i); err != nil || len(d) != 600 {
			t.Fatalf("attachment %d: %v", i, err)
		}
	}
	if _, err := s.Raw(es[0].Loc); !errors.Is(err, source.ErrTooLarge) {
		t.Errorf("Raw: %v", err)
	}
}

func TestLyingHeaderCannotBypassLimits(t *testing.T) {
	defer func(n int64) { maxMessageBytes = n }(maxMessageBytes)
	maxMessageBytes = 1000
	m := testutil.OLMMsg{Subject: "a", Body: "b", Attachments: []testutil.OLMAtt{{Name: "z.bin", Type: "application/octet-stream", URL: "Local/Attachments/z.bin", Missing: true}}}
	es := testutil.OLMMessageEntries("Local/Messages/Inbox/message_1.xml", m, "x")
	es = append(es, E{Name: "Local/Attachments/z.bin", Data: make([]byte, 1<<20), DeclaredSize: 10})
	s := openT(t, es...)
	got, _ := scan(t, s)
	// archive/zip itself refuses to inflate past the declared size, so a
	// header that understates fails (any error) instead of yielding data.
	if _, d, err := s.Attachment(got[0].Loc, 0); err == nil || len(d) > 1000 {
		t.Errorf("Attachment: %d bytes, %v", len(d), err)
	}
	if b, err := s.Raw(got[0].Loc); err == nil || len(b) > 1000 {
		t.Errorf("Raw: %d bytes, %v", len(b), err)
	}
}

func TestLyingHighHeaderIsRefused(t *testing.T) {
	defer func(n int64) { maxMessageBytes = n }(maxMessageBytes)
	maxMessageBytes = 1000
	m := testutil.OLMMsg{Subject: "a", Body: "b", Attachments: []testutil.OLMAtt{{Name: "z.bin", Type: "application/octet-stream", URL: "Local/Attachments/z.bin", Missing: true}}}
	es := testutil.OLMMessageEntries("Local/Messages/Inbox/message_1.xml", m, "x")
	es = append(es, E{Name: "Local/Attachments/z.bin", Data: make([]byte, 10), DeclaredSize: 1 << 30})
	s := openT(t, es...)
	got, _ := scan(t, s)
	if _, _, err := s.Attachment(got[0].Loc, 0); !errors.Is(err, source.ErrTooLarge) {
		t.Errorf("Attachment: %v", err)
	}
}

func TestLyingXMLHeader(t *testing.T) {
	defer func(n int64) { maxXMLBytes = n }(maxXMLBytes)
	maxXMLBytes = 2000
	s := openT(t, E{Name: "Local/Messages/Inbox/message_1.xml", Data: msgXML(strings.Repeat("q", 1<<20)), DeclaredSize: 10})
	var n, w int
	err := s.Scan(context.Background(), func(e source.Entry) error { n++; return nil }, func(error) { w++ })
	if err != nil || n != 0 || w != 1 {
		t.Fatal(n, w, err)
	}
	if _, err := s.Load(source.Locator{Name: "Local/Messages/Inbox/message_1.xml"}); err == nil {
		t.Error("Load of a lying document succeeded")
	}
}

func TestPlainTextWithBodyWordIsNotHTML(t *testing.T) {
	x := `<emails><email><OPFMessageCopySubject>s</OPFMessageCopySubject><OPFMessageCopyBody>write &lt;body&gt; tags in HTML</OPFMessageCopyBody></email></emails>`
	s := openT(t, E{Name: "Local/Messages/Inbox/message_1.xml", Data: []byte(x)})
	es, _ := scan(t, s)
	m, _ := s.Load(es[0].Loc)
	if m.HTML != "" || !strings.Contains(m.Text, "<body>") {
		t.Errorf("text %q html %q", m.Text, m.HTML)
	}
}

func TestKeyOnlyStripsAfterPrefix(t *testing.T) {
	if key("copyright") != "copyright" || key("getaway") != "getaway" || key("opfmessagecopysubject") != "subject" || key("opfmessagegetisread") != "isread" {
		t.Error("key")
	}
}

// A zip bomb (a tiny archive holding a huge, compressible message
// document) is read only up to the scan budget, so its memory and time
// stay small (issue #17).
func TestZipBombDocumentBounded(t *testing.T) {
	doc := append([]byte("<?xml version=\"1.0\"?><emails><email><OPFMessageCopySubject>Bomb</OPFMessageCopySubject><OPFMessageCopyBody>"),
		bytes.Repeat([]byte("A"), 60<<20)...)
	doc = append(doc, "</OPFMessageCopyBody></email></emails>"...)
	s := openT(t, E{Name: "Local/Messages/Inbox/message_1.xml", Data: msgXML("before")},
		E{Name: "Local/Messages/Inbox/message_2.xml", Data: doc})
	doc = nil
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	es, _ := scan(t, s)
	runtime.ReadMemStats(&after)
	if alloc := after.TotalAlloc - before.TotalAlloc; alloc > 64<<20 {
		t.Errorf("scanning the bomb allocated %d MiB", alloc>>20)
	}
	if len(es) == 0 || es[0].Msg.Subject != "s" {
		t.Errorf("the message before the bomb is missing: %v", es)
	}
}

// One document is read only up to what is left of the scan budget.
func TestScanBudgetCapsOneDocument(t *testing.T) {
	defer func(a int64, b int64) { budgetFloor, budgetFactor = a, b }(budgetFloor, budgetFactor)
	budgetFloor, budgetFactor = 5000, 0
	s := openT(t, E{Name: "Local/Messages/Inbox/message_1.xml", Data: msgXML(strings.Repeat("a", 3000))},
		E{Name: "Local/Messages/Inbox/message_2.xml", Data: msgXML(strings.Repeat("b", 100000))})
	o := s.(*olm)
	var e2 *zip.File
	for _, f := range o.zr.File {
		if strings.HasSuffix(f.Name, "message_2.xml") {
			e2 = f
		}
	}
	_, _, n, err := o.readXML(e2, 2000)
	if n > 2001 || err == nil {
		t.Errorf("read %d bytes with 2000 left (err %v)", n, err)
	}
	es, ws := scan(t, s)
	if len(es) != 1 || len(ws) == 0 || !strings.Contains(ws[len(ws)-1].Error(), "budget") {
		t.Errorf("entries %d, warnings %v", len(es), ws)
	}
}
