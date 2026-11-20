package pst

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/lazyoracle/mail-export-viewer/internal/source"
	"github.com/lazyoracle/mail-export-viewer/internal/source/pst/pstgen"
)

// tinyRowTC is a TCINFO with TCI_bm 2 (smaller than the row ID), no row
// index and a 6-byte row matrix.
func tinyRowTC() pstgen.Built {
	info := []byte{0x7C, 0}
	for _, v := range []uint16{0, 0, 0, 2} {
		info = binary.LittleEndian.AppendUint16(info, v)
	}
	info = binary.LittleEndian.AppendUint32(info, 0)    // hidRowIndex
	info = binary.LittleEndian.AppendUint32(info, 0x20) // hnidRows: allocation 1
	info = binary.LittleEndian.AppendUint32(info, 0)
	return pstgen.Heap(0x7C, []byte{1, 2, 3, 4, 5, 6}, info)
}

func TestTCRowSmallerThanRowID(t *testing.T) {
	o := ltpFile(t, pstgen.CryptNone, tinyRowTC())[0]
	if _, err := o.table(); !errors.Is(err, errCorrupt) {
		t.Fatalf("table() = %v, want errCorrupt", err)
	}
}

// TestSourceHostileRootTable: a crafted root hierarchy table reached from
// openStore, Folders and Scan must give errors or warnings, not panics.
func TestSourceHostileRootTable(t *testing.T) {
	tc := tinyRowTC()
	store := pstgen.PC([]pstgen.Prop{pstgen.Str(0x3001, "Store")})
	root := pstgen.PC([]pstgen.Prop{pstgen.Str(0x3001, "")})
	db := &pstgen.NDB{Nodes: []pstgen.Node{
		{NID: 0x21, Blocks: store.Blocks},
		{NID: 0x122, Blocks: root.Blocks},
		{NID: 0x12D, Blocks: tc.Blocks},
		{NID: 0x12E, Blocks: tc.Blocks},
	}}
	path := filepath.Join(t.TempDir(), "h.pst")
	if err := os.WriteFile(path, db.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	src, err := open(source.Candidate{Path: path, Rel: "h.pst"})
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	src.(source.FolderLister).Folders()
	src.Scan(context.Background(), func(source.Entry) error { return nil }, func(error) {})
}

// patchFirst replaces the first occurrence of old in b at a 64-byte block
// boundary.
func patchBlock(t *testing.T, b, old, new []byte) {
	t.Helper()
	for o := 0; o+len(old) <= len(b); o += 64 {
		if bytes.Equal(b[o:o+len(old)], old) {
			copy(b[o:], new)
			return
		}
	}
	t.Fatalf("pattern %x not found", old)
}

func TestNDBDataTreeLies(t *testing.T) {
	var tiny [][]byte
	for i := 0; i < 1100; i++ {
		tiny = append(tiny, blob(5, byte(i)))
	}
	big := pstgen.Split(blob(3*pstgen.MaxBlock+100, 3))
	img := (&pstgen.NDB{Nodes: []pstgen.Node{{NID: 0x24, Blocks: big}, {NID: 0x44, Blocks: tiny}}}).Bytes()

	// The XBLOCK claims 100 bytes but its blocks hold more.
	x := binary.LittleEndian.AppendUint32([]byte{1, 1, 4, 0}, uint32(3*pstgen.MaxBlock+100))
	patchBlock(t, img, x, binary.LittleEndian.AppendUint32([]byte{1, 1, 4, 0}, 100))
	// The XXBLOCK repeats its first XBLOCK.
	xx := binary.LittleEndian.AppendUint32([]byte{1, 2, 2, 0}, 5500)
	for o := 0; o+16 <= len(img); o += 64 {
		if bytes.Equal(img[o:o+8], xx) {
			copy(img[o+16:o+24], img[o+8:o+16])
		}
	}
	d := openBytes(t, img)
	for _, nid := range []uint32{0x24, 0x44} {
		n, _ := d.node(nid)
		got, total, err := d.read(n.bidData, 1<<20)
		if !errors.Is(err, errCorrupt) || int64(len(got)) > total {
			t.Errorf("node %#x: read %d bytes of declared %d, err %v", nid, len(got), total, err)
		}
		if _, err := d.blocks(n.bidData, 1<<20); !errors.Is(err, errCorrupt) {
			t.Errorf("node %#x: blocks err %v", nid, err)
		}
	}
}

func TestBlockTrailerBID(t *testing.T) {
	img := (&pstgen.NDB{Nodes: []pstgen.Node{{NID: 0x24, Blocks: [][]byte{[]byte("one")}}, {NID: 0x44, Blocks: [][]byte{[]byte("two")}}}}).Bytes()
	d := openBytes(t, img)
	n, _ := d.node(0x24)
	e, err := d.btLookup(d.bbt, ptypeBBT, n.bidData)
	if err != nil {
		t.Fatal(err)
	}
	ib, cb := le64(e, 8), int(le16(e, 16))
	trl := int(ib) + (cb+16+63)/64*64 - 16
	binary.LittleEndian.PutUint64(img[trl+8:], 0xBAD0)
	d = openBytes(t, img)
	if _, _, err := d.read(n.bidData, 100); !errors.Is(err, errCorrupt) {
		t.Errorf("read with a bad trailer BID: %v", err)
	}
	n2, _ := d.node(0x44)
	if got, _, err := d.read(n2.bidData, 100); err != nil || string(got) != "two" {
		t.Errorf("other block: %q %v", got, err)
	}
}

func TestLZFuDoesNotTrustRawSize(t *testing.T) {
	in := pstgen.CompressRTF([]byte("{\\rtf1 x}"))
	binary.LittleEndian.PutUint32(in[4:], 200<<20) // claimed raw size
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	lzfu(in, 256<<20)
	runtime.ReadMemStats(&after)
	if d := after.TotalAlloc - before.TotalAlloc; d > 8<<20 {
		t.Errorf("lzfu allocated %d bytes for a 9-byte body", d)
	}
}

func TestRTFHTMLTagVariants(t *testing.T) {
	// A non-starred \htmltag group inside an \htmlrtf run is still HTML.
	in := `{\rtf1\ansi\fromhtml1 \htmlrtf {\htmltag64 <pre>}x\htmlrtf0 a\tab b{\*\htmltag0 \par }c{\htmltag72 </pre>}{\*\mhtmltag9 <i>}}`
	html, _ := rtfConvert([]byte(in))
	if want := "<pre>a\tb\nc</pre>"; html != want {
		t.Errorf("html = %q, want %q", html, want)
	}
}

// reviewFile has messages for the fixes: a too-large attachment that
// claims to be small, an embedded message whose synthesis is too large,
// HTML only in RTF next to a plain body, inline semantics, an Exchange
// recipient with an '@' address.
func reviewFile() *pstgen.File {
	big := blob(60000, 1)
	inner := &pstgen.Message{Props: []pstgen.Prop{pstgen.Str(0x0037, "inner")},
		Attachments: []*pstgen.Attachment{{Props: []pstgen.Prop{pstgen.Str(0x3707, "b.bin")}, Data: big, Size: 1}}}
	return &pstgen.File{StoreName: "r", Folders: []*pstgen.Folder{{Name: "Inbox", Messages: []*pstgen.Message{
		{Props: []pstgen.Prop{pstgen.Str(0x0037, "lying size")},
			Attachments: []*pstgen.Attachment{{Props: []pstgen.Prop{pstgen.Str(0x3707, "a.bin")}, Data: big, Size: 1}}},
		{Props: []pstgen.Prop{pstgen.Str(0x0037, "lying embedded")},
			Attachments: []*pstgen.Attachment{{Props: []pstgen.Prop{pstgen.Str(0x3001, "inner")}, Embedded: inner, Size: 1}}},
		{Props: []pstgen.Prop{pstgen.Str(0x0037, "rtf html"), pstgen.Str(0x1000, "plain body"),
			pstgen.Bin(0x1009, pstgen.CompressRTF([]byte(fixtureRTFHTML)))}},
		{Props: []pstgen.Prop{pstgen.Str(0x0037, "inline"), pstgen.Str(0x1035, "<inline@example.com>")},
			Recipients: []pstgen.Recipient{{Type: pstgen.To, Name: "Erin", Email: "erin@example.com", AddrType: "EX"}},
			Attachments: []*pstgen.Attachment{
				{Props: []pstgen.Prop{pstgen.Str(0x3707, "1.png"), pstgen.Str(0x3712, "c1")}, Data: []byte("1")},
				{Props: []pstgen.Prop{pstgen.Str(0x3707, "2.png"), pstgen.Str(0x3712, "c2"), pstgen.Bool(0x7FFE, false)}, Data: []byte("2")},
				{Props: []pstgen.Prop{pstgen.Str(0x3707, "3.png"), pstgen.Str(0x3712, "c3"), pstgen.Bool(0x7FFE, false), pstgen.I32(0x3714, 4)}, Data: []byte("3")},
			}},
		{Props: []pstgen.Prop{pstgen.Str(0x001A, "IPM.Contact"), pstgen.Str(0x0037, "contact")}},
		{Props: []pstgen.Prop{pstgen.Str(0x0037, "base64 growth")},
			Attachments: []*pstgen.Attachment{{Props: []pstgen.Prop{pstgen.Str(0x3707, "c.bin")}, Data: blob(40000, 2)}}},
	}}}}
}

func reviewSource(t *testing.T) (source.Source, []source.Entry, *pstgen.File) {
	f := reviewFile()
	src := openSource(t, writePST(t, f), "r.pst")
	es, _ := scan(t, src)
	if len(es) != 5 {
		t.Fatalf("%d entries", len(es))
	}
	return src, es, f
}

func TestTooLargeUsesActualSizes(t *testing.T) {
	old := maxMessage
	maxMessage = 50000
	defer func() { maxMessage = old }()
	src, es, _ := reviewSource(t)
	if _, err := src.Load(es[0].Loc); !errors.Is(err, source.ErrTooLarge) {
		t.Errorf("Load with a small claimed attachment size: %v", err)
	}
	if _, err := src.Raw(es[1].Loc); !errors.Is(err, source.ErrTooLarge) {
		t.Errorf("Raw with a too-large embedded message: %v", err)
	}
	if _, _, err := src.Attachment(es[1].Loc, 0); !errors.Is(err, source.ErrTooLarge) {
		t.Errorf("Attachment of a too-large embedded message: %v", err)
	}
	// 40000 bytes pass the size check but not once base64-encoded.
	if _, err := src.Load(es[4].Loc); err != nil {
		t.Errorf("Load within the limit: %v", err)
	}
	if _, err := src.Raw(es[4].Loc); !errors.Is(err, source.ErrTooLarge) {
		t.Errorf("Raw growing past the limit: %v", err)
	}
}

func TestRTFHTMLOnLoadOnly(t *testing.T) {
	src, es, _ := reviewSource(t)
	if es[2].Msg.Text != "plain body" {
		t.Errorf("scan text %q", es[2].Msg.Text)
	}
	m, err := src.Load(es[2].Loc)
	if err != nil || m.Text != "plain body" || m.HTML != "<html><body><p>Rich café</p></body></html>" {
		t.Errorf("Load: text %q html %q err %v", m.Text, m.HTML, err)
	}
}

func TestInlineAndExchangeRecipient(t *testing.T) {
	src, es, _ := reviewSource(t)
	m, err := src.Load(es[3].Loc)
	if err != nil {
		t.Fatal(err)
	}
	var inline []bool
	for _, a := range m.Attachments {
		inline = append(inline, a.Inline)
	}
	if len(inline) != 3 || !inline[0] || inline[1] || !inline[2] {
		t.Errorf("inline = %v, want [true false true]", inline)
	}
	if len(m.To) != 1 || m.To[0].Email != "erin@example.com" {
		t.Errorf("To = %+v", m.To)
	}
}

func TestLocatorFingerprint(t *testing.T) {
	src, es, f := reviewSource(t)
	loc := es[3].Loc
	if loc.ID>>32 == 0 {
		t.Fatalf("locator %v has no fingerprint", loc)
	}
	for _, bad := range []source.Locator{{ID: loc.ID & 0xFFFFFFFF}, {ID: loc.ID ^ 1<<40}} {
		if _, err := src.Load(bad); !errors.Is(err, source.ErrStale) {
			t.Errorf("Load(%v) = %v, want ErrStale", bad, err)
		}
		if _, err := src.Raw(bad); !errors.Is(err, source.ErrStale) {
			t.Errorf("Raw(%v) = %v, want ErrStale", bad, err)
		}
		if _, _, err := src.Attachment(bad, 0); !errors.Is(err, source.ErrStale) {
			t.Errorf("Attachment(%v) = %v, want ErrStale", bad, err)
		}
	}
	// A non-mail item is not an entry: stale even with its fingerprint.
	st, _ := src.(*pstSource).store()
	contact := f.Folders[0].Messages[4].NID
	o, err := st.openNode(contact)
	if err != nil {
		t.Fatal(err)
	}
	p, _ := o.pc()
	cl := source.Locator{ID: uint64(fingerprint(msgProps(p)))<<32 | uint64(contact)}
	if _, err := src.Load(cl); !errors.Is(err, source.ErrStale) || !strings.Contains(err.Error(), "not mail") {
		t.Errorf("Load(contact) = %v", err)
	}
	if _, _, err := src.Attachment(cl, 0); !errors.Is(err, source.ErrStale) {
		t.Errorf("Attachment(contact) = %v", err)
	}
}
