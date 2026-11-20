package pst

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"

	"github.com/lazyoracle/mail-export-viewer/internal/source"
	"github.com/lazyoracle/mail-export-viewer/internal/source/pst/pstgen"
	"github.com/lazyoracle/mail-export-viewer/internal/source/sourcetest"
)

// Third-party sample PSTs are never committed. Point PST_SAMPLES at a
// directory of .pst files to validate the reader against them locally.
func samples(t *testing.T) []string {
	dir := os.Getenv("PST_SAMPLES")
	if dir == "" {
		t.Skip("PST_SAMPLES not set")
	}
	files, _ := filepath.Glob(filepath.Join(dir, "*.pst"))
	if len(files) == 0 {
		t.Skip("no .pst files in PST_SAMPLES")
	}
	return files
}

// TestRealMessages reads every folder, message and attachment and logs
// aggregate counts only (no message content).
func TestRealMessages(t *testing.T) {
	for _, path := range samples(t) {
		t.Run(filepath.Base(path), func(t *testing.T) {
			b, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			st, err := openStore(openBytes(t, b))
			if err != nil {
				t.Fatal(err)
			}
			fs, warns := st.folders()
			for _, w := range warns {
				t.Errorf("folder warning: %v", w)
			}
			classes := map[string]int{}
			var msgs, fails, withText, withHTML, withHeaders, withFrom, withDate, atts, attFails, labelled, msgIDs int
			for _, f := range fs {
				ids, err := st.contents(f.nid)
				if err != nil {
					t.Errorf("folder %d contents: %v", len(f.path), err)
					continue
				}
				for _, id := range ids {
					if sm, err := st.scanMessage(id); err == nil && !isMail(sm.class) {
						classes[sm.class]++
						continue
					}
					lid := mid(t, st, id)
					m, err := st.message(lid, true)
					if err != nil {
						fails++
						t.Logf("message %#x: %v", id, err)
						continue
					}
					msgs++
					classes[m.class]++
					if m.Text != "" {
						withText++
					}
					if m.HTML != "" {
						withHTML++
					}
					if len(m.Header) > 0 {
						withHeaders++
					}
					if m.From.Email != "" || m.From.Name != "" {
						withFrom++
					}
					if !m.Date.IsZero() {
						withDate++
					}
					if m.MessageID != "" {
						msgIDs++
					}
					if len(m.labels) > 0 {
						labelled++
					}
					for i := range m.Attachments {
						atts++
						if _, _, err := st.attachment(lid, i); err != nil {
							attFails++
							t.Logf("attachment %#x/%d: %v", id, i, err)
						}
					}
				}
			}
			t.Logf("%d folders, %d messages (%d failed), classes %v", len(fs), msgs, fails, classes)
			t.Logf("text %d, html %d, headers %d, from %d, date %d, message-id %d, categorised %d, attachments %d (%d failed)",
				withText, withHTML, withHeaders, withFrom, withDate, msgIDs, labelled, atts, attFails)
			if fails > 0 || attFails > 0 {
				t.Errorf("%d messages and %d attachments failed", fails, attFails)
			}
		})
	}
}

// TestRealConformance runs the source contract check on each sample that
// holds mail.
func TestRealConformance(t *testing.T) {
	for _, path := range samples(t) {
		t.Run(filepath.Base(path), func(t *testing.T) {
			src := openSource(t, path, filepath.Base(path))
			es, warns := scan(t, src)
			t.Logf("%d entries, %d folders, warnings %v", len(es), len(src.(source.FolderLister).Folders()), warns)
			if len(es) == 0 {
				t.Skip("no mail in this sample")
			}
			sourcetest.Check(t, src, source.Locator{ID: 0x7FFFFFE4}, source.Locator{ID: 0x21})
			for _, e := range es[:min(len(es), 200)] {
				if _, err := src.Raw(e.Loc); err != nil {
					t.Errorf("Raw(%v): %v", e.Loc, err)
				}
			}
		})
	}
}

func TestRealNDB(t *testing.T) {
	for _, path := range samples(t) {
		t.Run(filepath.Base(path), func(t *testing.T) {
			b, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			d := openBytes(t, b)
			if d.l.unicode {
				// The test writer's CRC agrees with real headers.
				if got, want := pstgen.CRC(b[8:8+471]), binary.LittleEndian.Uint32(b[4:]); got != want {
					t.Errorf("header CRC %#x, file has %#x", got, want)
				}
			}
			for _, nid := range []uint32{0x21, 0x61, 0x122} {
				n, err := d.node(nid)
				if err != nil {
					t.Fatalf("node %#x: %v", nid, err)
				}
				bl, err := d.blocks(n.bidData, 1<<24)
				if err != nil || len(bl) == 0 || len(bl[0]) < 4 || bl[0][2] != 0xEC {
					t.Fatalf("node %#x: no HN (%d blocks, %v)", nid, len(bl), err)
				}
				if _, err := d.subnodes(n.bidSub); err != nil {
					t.Errorf("node %#x subnodes: %v", nid, err)
				}
				o, err := d.openObj(n.bidData, n.bidSub)
				if err != nil {
					t.Fatalf("node %#x: %v", nid, err)
				}
				p, err := o.pc()
				if err != nil {
					t.Fatalf("node %#x PC: %v", nid, err)
				}
				t.Logf("node %#x: %d properties, display name %q", nid, len(p.props), p.set("").str(0x3001))
			}
			n, err := d.node(0x12D)
			if err != nil {
				t.Fatal(err)
			}
			o, err := d.openObj(n.bidData, n.bidSub)
			if err != nil {
				t.Fatal(err)
			}
			tb, err := o.table()
			if err != nil {
				t.Fatalf("root hierarchy table: %v", err)
			}
			for i := 0; i < tb.len(); i++ {
				t.Logf("root child %#x %q", tb.rowID(i), tb.set(i, "").str(0x3001))
			}
		})
	}
}
