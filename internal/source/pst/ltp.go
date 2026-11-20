package pst

import (
	"sort"
	"strings"
	"time"
	"unicode/utf16"

	"github.com/lazyoracle/mail-export-viewer/internal/mail"
)

// LTP layer ([MS-PST] 2.3): heap-on-node (HN), BTH, property contexts (PC)
// and table contexts (TC).

const (
	maxHeapBytes  = 64 << 20  // one HN
	maxTableBytes = 128 << 20 // one TC row matrix
	maxRows       = 1 << 20
	maxBTHRecords = 1 << 20
	maxBTHLevels  = 8
	// maxStr bounds ordinary string and binary properties (not bodies).
	maxStr = 1 << 20
)

// Property types.
const (
	ptInt16   = 0x0002
	ptInt32   = 0x0003
	ptBool    = 0x000B
	ptErr     = 0x000A
	ptObject  = 0x000D
	ptInt64   = 0x0014
	ptString8 = 0x001E
	ptUnicode = 0x001F
	ptTime    = 0x0040
	ptBinary  = 0x0102
	ptMVStr8  = 0x101E
	ptMVUni   = 0x101F
)

// fixedSize is the size of a fixed-size property type, 0 for variable
// types (stored by HNID).
func fixedSize(typ uint16) int {
	switch typ {
	case ptBool:
		return 1
	case ptInt16:
		return 2
	case ptInt32, 0x0004, ptErr:
		return 4
	case 0x0005, 0x0006, 0x0007, ptInt64, ptTime:
		return 8
	}
	return 0
}

type heap struct {
	blocks [][]byte
	sig    byte
	root   uint32
}

func newHeap(blocks [][]byte) (*heap, error) {
	if len(blocks) == 0 || len(blocks[0]) < 12 || blocks[0][2] != 0xEC {
		return nil, corrupt("no heap-on-node signature")
	}
	return &heap{blocks: blocks, sig: blocks[0][3], root: le32(blocks[0], 4)}, nil
}

// get returns the heap allocation hid.
func (h *heap) get(hid uint32) ([]byte, error) {
	idx, bi := int(hid>>5&0x7FF), int(hid>>16)
	if hid&0x1F != 0 || idx == 0 || bi >= len(h.blocks) {
		return nil, corrupt("bad HID %#x", hid)
	}
	b := h.blocks[bi]
	if len(b) < 2 {
		return nil, corrupt("short heap page")
	}
	pm := int(le16(b, 0))
	if pm+4 > len(b) {
		return nil, corrupt("bad heap page map")
	}
	n := int(le16(b, pm))
	if idx > n || pm+4+2*(n+1) > len(b) {
		return nil, corrupt("bad HID %#x", hid)
	}
	start, end := int(le16(b, pm+4+2*(idx-1))), int(le16(b, pm+4+2*idx))
	if start > end || end > pm {
		return nil, corrupt("bad heap allocation %#x", hid)
	}
	return b[start:end], nil
}

// bth calls fn for every record of the BTH whose header is at hid.
func (h *heap) bth(hid uint32, cbKey, minEnt int, fn func(key, data []byte)) error {
	hdr, err := h.get(hid)
	if err != nil {
		return err
	}
	if len(hdr) < 8 || hdr[0] != 0xB5 || int(hdr[1]) != cbKey || int(hdr[2]) < minEnt || hdr[3] > maxBTHLevels {
		return corrupt("bad BTH header")
	}
	cbEnt := int(hdr[2])
	visits := 0
	var walk func(hid uint32, level int) error
	walk = func(hid uint32, level int) error {
		if visits++; visits > maxBTHRecords {
			return corrupt("BTH too large")
		}
		b, err := h.get(hid)
		if err != nil {
			return err
		}
		if level > 0 {
			for o := 0; o+cbKey+4 <= len(b); o += cbKey + 4 {
				if err := walk(le32(b, o+cbKey), level-1); err != nil {
					return err
				}
			}
			return nil
		}
		for o := 0; o+cbKey+cbEnt <= len(b); o += cbKey + cbEnt {
			if visits++; visits > maxBTHRecords {
				return corrupt("BTH too large")
			}
			fn(b[o:o+cbKey], b[o+cbKey:o+cbKey+cbEnt])
		}
		return nil
	}
	if root := le32(hdr, 4); root != 0 {
		return walk(root, int(hdr[3]))
	}
	return nil
}

// obj is an LTP object: a node's heap and its subnodes (where large values
// live).
type obj struct {
	db   *ndb
	h    *heap
	subs map[uint32]subnode
}

func (d *ndb) openObj(bidData, bidSub uint64) (*obj, error) {
	blocks, err := d.blocks(bidData, maxHeapBytes)
	if err != nil {
		return nil, err
	}
	h, err := newHeap(blocks)
	if err != nil {
		return nil, err
	}
	subs, err := d.subnodes(bidSub)
	if err != nil {
		return nil, err
	}
	return &obj{db: d, h: h, subs: subs}, nil
}

// sub opens the subnode nid of o as an LTP object.
func (o *obj) sub(nid uint32) (*obj, error) {
	s, ok := o.subs[nid]
	if !ok {
		return nil, corrupt("subnode %#x missing", nid)
	}
	return o.db.openObj(s.bidData, s.bidSub)
}

// pval is a property value: inline bytes or a reference (HNID).
type pval struct {
	typ    uint16
	inline []byte
	ref    uint32
	isRef  bool
}

// data returns at most limit bytes of v and its total size.
func (o *obj) data(v pval, limit int64) ([]byte, int64, error) {
	switch {
	case !v.isRef:
		return v.inline, int64(len(v.inline)), nil
	case v.ref == 0:
		return nil, 0, nil
	case v.ref&0x1F == 0:
		b, err := o.h.get(v.ref)
		return b[:min(int64(len(b)), limit)], int64(len(b)), err
	}
	s, ok := o.subs[v.ref]
	if !ok {
		return nil, 0, corrupt("value subnode %#x missing", v.ref)
	}
	return o.db.read(s.bidData, limit)
}

func (o *obj) dataSize(v pval) (int64, error) {
	if !v.isRef || v.ref == 0 || v.ref&0x1F == 0 {
		_, n, err := o.data(v, 0)
		return n, err
	}
	s, ok := o.subs[v.ref]
	if !ok {
		return 0, corrupt("value subnode %#x missing", v.ref)
	}
	return o.db.size(s.bidData)
}

type pc struct {
	o     *obj
	props map[uint16]pval
}

// pc reads o as a property context.
func (o *obj) pc() (*pc, error) {
	if o.h.sig != 0xBC {
		return nil, corrupt("not a property context")
	}
	p := &pc{o: o, props: map[uint16]pval{}}
	err := o.h.bth(o.h.root, 2, 6, func(k, d []byte) {
		typ, v := le16(d, 0), le32(d, 2)
		if n := fixedSize(typ); n > 0 && n <= 4 {
			b := []byte{byte(v), byte(v >> 8), byte(v >> 16), byte(v >> 24)}
			p.props[le16(k, 0)] = pval{typ: typ, inline: b[:n]}
			return
		}
		p.props[le16(k, 0)] = pval{typ: typ, ref: v, isRef: true}
	})
	return p, err
}

func (p *pc) get(id uint16) (pval, bool) { v, ok := p.props[id]; return v, ok }

// set gives typed access to p; cs is the charset of PtypString8 values.
func (p *pc) set(cs string) propSet { return propSet{o: p.o, get: p.get, cs: cs} }

type tcol struct {
	typ         uint16
	ib, cb, bit int
}

type table struct {
	o        *obj
	cols     map[uint16]tcol
	rowSize  int
	ceb      int
	blocks   [][]byte
	perBlock int
	idx      []int // row indexes in table order
	ids      []uint32
}

// table reads o as a table context.
func (o *obj) table() (*table, error) {
	if o.h.sig != 0x7C {
		return nil, corrupt("not a table context")
	}
	info, err := o.h.get(o.h.root)
	if err != nil {
		return nil, err
	}
	if len(info) < 22 || info[0] != 0x7C || len(info) < 22+8*int(info[1]) {
		return nil, corrupt("bad TCINFO")
	}
	end4, end2, end1, bm := int(le16(info, 2)), int(le16(info, 4)), int(le16(info, 6)), int(le16(info, 8))
	// A row holds at least the 4-byte row ID.
	if end4 > end2 || end2 > end1 || end1 > bm || bm < 4 {
		return nil, corrupt("bad TC row layout")
	}
	t := &table{o: o, cols: map[uint16]tcol{}, rowSize: bm, ceb: end1}
	for i := 0; i < int(info[1]); i++ {
		c := info[22+8*i:]
		tag := le32(c, 0)
		col := tcol{typ: uint16(tag), ib: int(le16(c, 4)), cb: int(c[6]), bit: int(c[7])}
		if col.ib+col.cb > end1 || end1+col.bit/8 >= bm {
			return nil, corrupt("bad TC column %#x", tag)
		}
		if _, dup := t.cols[uint16(tag>>16)]; !dup {
			t.cols[uint16(tag>>16)] = col
		}
	}
	rows := le32(info, 14)
	switch {
	case rows == 0:
	case rows&0x1F == 0:
		b, err := o.h.get(rows)
		if err != nil {
			return nil, err
		}
		t.blocks, t.perBlock = [][]byte{b}, len(b)/max(bm, 1)
	default:
		s, ok := o.subs[rows]
		if !ok {
			return nil, corrupt("row matrix subnode %#x missing", rows)
		}
		if t.blocks, err = o.db.blocks(s.bidData, maxTableBytes); err != nil {
			return nil, err
		}
		t.perBlock = o.db.l.maxBlock / max(bm, 1)
	}
	if bm == 0 || t.perBlock == 0 {
		return t, nil
	}
	if hid := le32(info, 10); hid != 0 {
		type ent struct {
			id  uint32
			idx int
		}
		var ents []ent
		cbIdx := o.db.l.rowIdxLen
		err := o.h.bth(hid, 4, cbIdx, func(k, d []byte) {
			i := int(le16(d, 0))
			if cbIdx == 4 {
				i = int(le32(d, 0))
			}
			if t.row(i) != nil && len(ents) < maxRows {
				ents = append(ents, ent{le32(k, 0), i})
			}
		})
		if err != nil {
			return nil, err
		}
		sort.Slice(ents, func(i, j int) bool { return ents[i].idx < ents[j].idx })
		for i, e := range ents {
			if i == 0 || e.idx != ents[i-1].idx {
				t.idx = append(t.idx, e.idx)
				t.ids = append(t.ids, e.id)
			}
		}
		return t, nil
	}
	for i := 0; t.row(i) != nil && i < maxRows; i++ {
		t.idx = append(t.idx, i)
		t.ids = append(t.ids, le32(t.row(i), 0))
	}
	return t, nil
}

// row returns the raw row at matrix index i, or nil.
func (t *table) row(i int) []byte {
	if i < 0 || t.perBlock == 0 {
		return nil
	}
	b, off := i/t.perBlock, (i%t.perBlock)*t.rowSize
	if b >= len(t.blocks) || off+t.rowSize > len(t.blocks[b]) {
		return nil
	}
	return t.blocks[b][off : off+t.rowSize]
}

func (t *table) len() int           { return len(t.idx) }
func (t *table) rowID(i int) uint32 { return t.ids[i] }

// cell returns the value of column id in row i (table order).
func (t *table) cell(i int, id uint16) (pval, bool) {
	c, ok := t.cols[id]
	row := t.row(t.idx[i])
	if !ok || row == nil || row[t.ceb+c.bit/8]&(0x80>>(c.bit%8)) == 0 {
		return pval{}, false
	}
	cell := row[c.ib : c.ib+c.cb]
	if n := fixedSize(c.typ); n > 0 {
		if n != c.cb {
			return pval{}, false
		}
		return pval{typ: c.typ, inline: cell}, true
	}
	if c.cb != 4 {
		return pval{}, false
	}
	return pval{typ: c.typ, ref: le32(cell, 0), isRef: true}, true
}

func (t *table) set(i int, cs string) propSet {
	return propSet{o: t.o, get: func(id uint16) (pval, bool) { return t.cell(i, id) }, cs: cs}
}

// propSet gives typed access to the properties of a PC or a table row.
// Accessors return zero values for absent, mistyped or unreadable values.
type propSet struct {
	o   *obj
	get func(uint16) (pval, bool)
	cs  string
}

// bin returns the bytes of id, or nil when absent or larger than limit.
func (s propSet) bin(id uint16, limit int64) []byte {
	v, ok := s.get(id)
	if !ok {
		return nil
	}
	b, total, err := s.o.data(v, limit)
	if err != nil || total > limit {
		return nil
	}
	return b
}

// prefix returns at most limit bytes of id and its full size.
func (s propSet) prefix(id uint16, limit int64) ([]byte, int64) {
	v, ok := s.get(id)
	if !ok {
		return nil, 0
	}
	b, total, err := s.o.data(v, limit)
	if err != nil {
		return nil, 0
	}
	return b, total
}

func (s propSet) size(id uint16) (int64, error) {
	v, ok := s.get(id)
	if !ok {
		return 0, nil
	}
	return s.o.dataSize(v)
}

func (s propSet) typ(id uint16) uint16 {
	v, _ := s.get(id)
	return v.typ
}

func (s propSet) str(id uint16) string {
	v, ok := s.get(id)
	if !ok {
		return ""
	}
	b, total, err := s.o.data(v, maxStr)
	if err != nil {
		return ""
	}
	_ = total // a longer value is cut at maxStr
	return decodeString(v.typ, b, s.cs)
}

func decodeString(typ uint16, b []byte, cs string) string {
	switch typ {
	case ptUnicode:
		return utf16String(b)
	case ptString8:
		return mail.DecodeCharset([]byte(strings.TrimRight(string(b), "\x00")), cs)
	}
	return ""
}

// utf16String decodes UTF-16LE, dropping trailing NULs; the result is valid
// UTF-8.
func utf16String(b []byte) string {
	u := make([]uint16, len(b)/2)
	for i := range u {
		u[i] = le16(b, 2*i)
	}
	for len(u) > 0 && u[len(u)-1] == 0 {
		u = u[:len(u)-1]
	}
	return string(utf16.Decode(u))
}

// strs returns a multi-valued string (a single string gives one value).
func (s propSet) strs(id uint16) []string {
	v, ok := s.get(id)
	if !ok {
		return nil
	}
	switch v.typ {
	case ptUnicode, ptString8:
		if x := s.str(id); x != "" {
			return []string{x}
		}
		return nil
	case ptMVUni, ptMVStr8:
	default:
		return nil
	}
	b, total, err := s.o.data(v, maxStr)
	if err != nil || total > maxStr || len(b) < 4 {
		return nil
	}
	n := int(le32(b, 0))
	if n > (len(b)-4)/4 {
		return nil
	}
	typ := uint16(ptUnicode)
	if v.typ == ptMVStr8 {
		typ = ptString8
	}
	var out []string
	for i := 0; i < n; i++ {
		start, end := int(le32(b, 4+4*i)), len(b)
		if i+1 < n {
			end = int(le32(b, 8+4*i))
		}
		if start < 4+4*n || start > end || end > len(b) {
			return out
		}
		out = append(out, decodeString(typ, b[start:end], s.cs))
	}
	return out
}

// int returns an integer or boolean property.
func (s propSet) int(id uint16) (int64, bool) {
	v, ok := s.get(id)
	if !ok {
		return 0, false
	}
	b, _, err := s.o.data(v, 8)
	if err != nil {
		return 0, false
	}
	switch {
	case v.typ == ptBool && len(b) >= 1:
		return int64(b[0]), true
	case v.typ == ptInt16 && len(b) >= 2:
		return int64(int16(le16(b, 0))), true
	case (v.typ == ptInt32 || v.typ == ptErr) && len(b) >= 4:
		return int64(int32(le32(b, 0))), true
	case v.typ == ptInt64 && len(b) >= 8:
		return int64(le64(b, 0)), true
	}
	return 0, false
}

func (s propSet) boolean(id uint16) bool {
	v, ok := s.int(id)
	return ok && v != 0 && s.typ(id) == ptBool
}

// time returns a PtypTime property, zero when absent or outside 1970-2200.
func (s propSet) time(id uint16) time.Time {
	v, ok := s.get(id)
	if !ok || v.typ != ptTime {
		return time.Time{}
	}
	b, _, err := s.o.data(v, 8)
	if err != nil || len(b) < 8 {
		return time.Time{}
	}
	return filetime(le64(b, 0))
}

// filetime converts a FILETIME; values before 1970 or after about 2200 are
// zero (missing), as package mail treats such dates, which also drops
// placeholders like 1601-01-01.
func filetime(ft uint64) time.Time {
	const unixEpoch = 116444736000000000
	const max = unixEpoch + 230*365*24*3600*10000000 // about 2200
	if ft < unixEpoch || ft > max {
		return time.Time{}
	}
	d := ft - unixEpoch
	return time.Unix(int64(d/10000000), int64(d%10000000)*100).UTC()
}
