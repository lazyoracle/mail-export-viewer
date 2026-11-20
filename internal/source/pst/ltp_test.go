package pst

import (
	"bytes"
	"math/rand"
	"reflect"
	"testing"
	"time"

	"github.com/lazyoracle/mail-export-viewer/internal/source/pst/pstgen"
)

// ltpFile writes objects as nodes 0x24, 0x44, ... and opens them.
func ltpFile(t *testing.T, crypt byte, objs ...pstgen.Built) []*obj {
	t.Helper()
	db := &pstgen.NDB{Crypt: crypt}
	for i, b := range objs {
		db.Nodes = append(db.Nodes, pstgen.Node{NID: uint32(i+1)<<5 | 4, Blocks: b.Blocks, Subnodes: b.Subnodes})
	}
	d := openBytes(t, db.Bytes())
	var out []*obj
	for i := range objs {
		n, err := d.node(uint32(i+1)<<5 | 4)
		if err != nil {
			t.Fatal(err)
		}
		o, err := d.openObj(n.bidData, n.bidSub)
		if err != nil {
			t.Fatalf("openObj %d: %v", i, err)
		}
		out = append(out, o)
	}
	return out
}

func TestPC(t *testing.T) {
	when := time.Date(2021, 3, 4, 5, 6, 7, 0, time.UTC)
	big := blob(20000, 9)
	props := []pstgen.Prop{
		pstgen.Str(0x0037, "Grüße, world"),
		pstgen.Str8(0x0E04, []byte("caf\xe9")),
		pstgen.I32(0x0E07, 0x11),
		pstgen.Bool(0x0E1B, true),
		pstgen.Time(0x0039, when),
		pstgen.Bin(0x0FF9, []byte{1, 2, 3}),
		pstgen.Bin(0x1009, big),
		pstgen.MVStr(0x8001, []string{"Red", "Blue category"}),
		pstgen.Str(0x0070, ""),
		{ID: 0x0017, Type: pstgen.TypeInt16, Value: []byte{2, 0}},
		{ID: 0x0E08, Type: pstgen.TypeInt64, Value: []byte{1, 2, 0, 0, 0, 0, 0, 0}},
	}
	// Medium values filling more than eight heap pages (one has a bitmap
	// header).
	for i := 0; i < 30; i++ {
		props = append(props, pstgen.Bin(uint16(0x6000+i), blob(3000, byte(i))))
	}
	var many []pstgen.Prop // more records than one BTH leaf holds
	for i := 0; i < 600; i++ {
		many = append(many, pstgen.I32(uint16(0x7000+i), int32(i)))
	}
	for _, crypt := range []byte{pstgen.CryptNone, pstgen.CryptCyclic} {
		objs := ltpFile(t, crypt, pstgen.PC(props), pstgen.PC(many))
		p, err := objs[0].pc()
		if err != nil {
			t.Fatalf("pc: %v", err)
		}
		if len(objs[0].h.blocks) < 10 {
			t.Fatalf("heap has %d blocks, want > 9", len(objs[0].h.blocks))
		}
		s := p.set("windows-1252")
		if got := s.str(0x0037); got != "Grüße, world" {
			t.Errorf("unicode = %q", got)
		}
		if got := s.str(0x0E04); got != "café" {
			t.Errorf("string8 = %q", got)
		}
		if v, ok := s.int(0x0E07); !ok || v != 0x11 {
			t.Errorf("int32 = %d %v", v, ok)
		}
		if v, ok := s.int(0x0017); !ok || v != 2 {
			t.Errorf("int16 = %d %v", v, ok)
		}
		if v, ok := s.int(0x0E08); !ok || v != 0x0201 {
			t.Errorf("int64 = %d %v", v, ok)
		}
		if !s.boolean(0x0E1B) {
			t.Errorf("bool false")
		}
		if got := s.time(0x0039); !got.Equal(when) {
			t.Errorf("time = %v", got)
		}
		if got := s.bin(0x0FF9, 100); !bytes.Equal(got, []byte{1, 2, 3}) {
			t.Errorf("binary = %v", got)
		}
		if got := s.bin(0x1009, 1<<20); !bytes.Equal(got, big) {
			t.Errorf("subnode binary: %d bytes", len(got))
		}
		if got := s.bin(0x1009, 100); got != nil {
			t.Errorf("over-limit binary should be nil, got %d bytes", len(got))
		}
		if n, err := s.size(0x1009); err != nil || n != 20000 {
			t.Errorf("size = %d %v", n, err)
		}
		if got := s.strs(0x8001); !reflect.DeepEqual(got, []string{"Red", "Blue category"}) {
			t.Errorf("multi string = %q", got)
		}
		if _, ok := p.get(0x0070); !ok || s.str(0x0070) != "" {
			t.Errorf("empty string missing")
		}
		for i := 0; i < 30; i++ {
			if got := s.bin(uint16(0x6000+i), 4000); !bytes.Equal(got, blob(3000, byte(i))) {
				t.Fatalf("medium value %d differs", i)
			}
		}
		if _, ok := p.get(0x1234); ok {
			t.Errorf("unexpected property")
		}
		p2, err := objs[1].pc()
		if err != nil || len(p2.props) != 600 {
			t.Fatalf("multi-level BTH: %d props, %v", len(p2.props), err)
		}
		if v, _ := p2.set("").int(0x7000 + 599); v != 599 {
			t.Errorf("last record = %d", v)
		}
	}
}

func TestTC(t *testing.T) {
	cols := []pstgen.Col{{ID: 0x0037, Type: pstgen.TypeUnicode}, {ID: 0x0E07, Type: pstgen.TypeInt32}, {ID: 0x0039, Type: pstgen.TypeTime},
		{ID: 0x0E1B, Type: pstgen.TypeBool}, {ID: 0x0017, Type: pstgen.TypeInt16}, {ID: 0x0FF9, Type: pstgen.TypeBinary}}
	when := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)
	small := []pstgen.Row{
		{ID: 0x200064, Props: []pstgen.Prop{pstgen.Str(0x0037, "first"), pstgen.I32(0x0E07, 1), pstgen.Time(0x0039, when), pstgen.Bool(0x0E1B, true)}},
		{ID: 0x200024, Props: []pstgen.Prop{pstgen.Str(0x0037, "second"), {ID: 0x0017, Type: pstgen.TypeInt16, Value: []byte{7, 0}}}},
		{ID: 0x200044, Props: []pstgen.Prop{pstgen.Bin(0x0FF9, blob(5000, 1))}},
	}
	var large []pstgen.Row
	for i := 0; i < 300; i++ {
		large = append(large, pstgen.Row{ID: uint32(i+1)<<5 | 4, Props: []pstgen.Prop{pstgen.Str(0x0037, "row"), pstgen.I32(0x0E07, int32(i))}})
	}
	objs := ltpFile(t, pstgen.CryptPermute, pstgen.TC(cols, small), pstgen.TC(cols, large), pstgen.TC(cols, nil))

	tb, err := objs[0].table()
	if err != nil {
		t.Fatalf("table: %v", err)
	}
	if tb.len() != 3 || tb.rowID(0) != 0x200064 || tb.rowID(1) != 0x200024 || tb.rowID(2) != 0x200044 {
		t.Fatalf("rows: %d", tb.len())
	}
	r0, r1, r2 := tb.set(0, ""), tb.set(1, ""), tb.set(2, "")
	if r0.str(0x0037) != "first" || r1.str(0x0037) != "second" {
		t.Errorf("strings %q %q", r0.str(0x0037), r1.str(0x0037))
	}
	if v, ok := r0.int(0x0E07); !ok || v != 1 {
		t.Errorf("int %d %v", v, ok)
	}
	if _, ok := r1.int(0x0E07); ok {
		t.Errorf("absent cell reported present")
	}
	if v, ok := r1.int(0x0017); !ok || v != 7 {
		t.Errorf("int16 %d %v", v, ok)
	}
	if !r0.time(0x0039).Equal(when) || !r0.boolean(0x0E1B) || r1.boolean(0x0E1B) {
		t.Errorf("time/bool cells wrong")
	}
	if !bytes.Equal(r2.bin(0x0FF9, 1<<20), blob(5000, 1)) {
		t.Errorf("subnode cell differs")
	}

	tb, err = objs[1].table()
	if err != nil || tb.len() != 300 {
		t.Fatalf("large table: %v", err)
	}
	for i := 0; i < 300; i++ {
		if v, _ := tb.set(i, "").int(0x0E07); v != int64(i) || tb.rowID(i) != uint32(i+1)<<5|4 {
			t.Fatalf("row %d: %d %#x", i, v, tb.rowID(i))
		}
	}
	tb, err = objs[2].table()
	if err != nil || tb.len() != 0 {
		t.Fatalf("empty table: %v", err)
	}
}

func TestLTPCorruption(t *testing.T) {
	cols := []pstgen.Col{{ID: 0x0037, Type: pstgen.TypeUnicode}, {ID: 0x0FF9, Type: pstgen.TypeBinary}}
	var rows []pstgen.Row
	for i := 0; i < 100; i++ {
		rows = append(rows, pstgen.Row{ID: uint32(i+1)<<5 | 4, Props: []pstgen.Prop{pstgen.Str(0x0037, "row"), pstgen.Bin(0x0FF9, blob(100, 1))}})
	}
	var props []pstgen.Prop
	for i := 0; i < 50; i++ {
		props = append(props, pstgen.Bin(uint16(0x6000+i), blob(1000, byte(i))), pstgen.MVStr(uint16(0x7000+i), []string{"a", "b"}))
	}
	db := &pstgen.NDB{}
	for i, b := range []pstgen.Built{pstgen.PC(props), pstgen.TC(cols, rows)} {
		db.Nodes = append(db.Nodes, pstgen.Node{NID: uint32(i+1)<<5 | 4, Blocks: b.Blocks, Subnodes: b.Subnodes})
	}
	good := db.Bytes()
	rng := rand.New(rand.NewSource(2))
	for iter := 0; iter < 500; iter++ {
		b := append([]byte(nil), good...)
		for k := 0; k < 1+rng.Intn(10); k++ {
			b[dataStartForTest+rng.Intn(len(b)-dataStartForTest)] = byte(rng.Intn(256))
		}
		d, err := openNDB(bytes.NewReader(b), int64(len(b)))
		if err != nil {
			continue
		}
		for i := 0; i < 2; i++ {
			n, err := d.node(uint32(i+1)<<5 | 4)
			if err != nil {
				continue
			}
			o, err := d.openObj(n.bidData, n.bidSub)
			if err != nil {
				continue
			}
			if p, err := o.pc(); err == nil {
				s := p.set("")
				for id := range p.props {
					s.str(id)
					s.strs(id)
					s.bin(id, 1<<20)
				}
			}
			if tb, err := o.table(); err == nil {
				for r := 0; r < tb.len(); r++ {
					s := tb.set(r, "")
					s.str(0x0037)
					s.bin(0x0FF9, 1<<20)
				}
			}
		}
	}
}

const dataStartForTest = 0x4400
