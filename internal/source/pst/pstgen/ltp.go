package pstgen

import (
	"encoding/binary"
	"sort"
	"time"
	"unicode/utf16"
)

// LTP layer ([MS-PST] 2.3): heap-on-node, BTH, property contexts and table
// contexts.

// Property types.
const (
	TypeInt16   = 0x0002
	TypeInt32   = 0x0003
	TypeBool    = 0x000B
	TypeObject  = 0x000D
	TypeInt64   = 0x0014
	TypeString8 = 0x001E
	TypeUnicode = 0x001F
	TypeTime    = 0x0040
	TypeBinary  = 0x0102
	TypeMVUni   = 0x101F
	TypeMVStr8  = 0x101E
)

// Prop is one property: Value holds the encoded bytes (little-endian
// integers, UTF-16LE strings, multi-value layouts).
type Prop struct {
	ID, Type uint16
	Value    []byte
}

// UTF16 encodes s as UTF-16LE.
func UTF16(s string) []byte {
	u := utf16.Encode([]rune(s))
	b := make([]byte, 2*len(u))
	for i, c := range u {
		binary.LittleEndian.PutUint16(b[2*i:], c)
	}
	return b
}

// Str is a PtypString property.
func Str(id uint16, s string) Prop { return Prop{id, TypeUnicode, UTF16(s)} }

// Str8 is a PtypString8 property holding raw code-page bytes.
func Str8(id uint16, b []byte) Prop { return Prop{id, TypeString8, b} }

// Bin is a PtypBinary property.
func Bin(id uint16, b []byte) Prop { return Prop{id, TypeBinary, b} }

// I32 is a PtypInteger32 property.
func I32(id uint16, v int32) Prop {
	return Prop{id, TypeInt32, binary.LittleEndian.AppendUint32(nil, uint32(v))}
}

// Bool is a PtypBoolean property.
func Bool(id uint16, v bool) Prop {
	b := []byte{0}
	if v {
		b[0] = 1
	}
	return Prop{id, TypeBool, b}
}

// Time is a PtypTime property (FILETIME).
func Time(id uint16, t time.Time) Prop {
	ft := uint64(t.UnixNano()/100) + 116444736000000000
	return Prop{id, TypeTime, binary.LittleEndian.AppendUint64(nil, ft)}
}

// MVStr is a PtypMultipleString property.
func MVStr(id uint16, ss []string) Prop {
	head := binary.LittleEndian.AppendUint32(nil, uint32(len(ss)))
	off := 4 + 4*len(ss)
	var data []byte
	for _, s := range ss {
		head = binary.LittleEndian.AppendUint32(head, uint32(off+len(data)))
		data = append(data, UTF16(s)...)
	}
	return Prop{id, TypeMVUni, append(head, data...)}
}

// fixedSize is the size of fixed-size types, 0 for variable ones.
func fixedSize(typ uint16) int {
	switch typ {
	case TypeBool:
		return 1
	case TypeInt16:
		return 2
	case TypeInt32, 0x0004, 0x000A:
		return 4
	case 0x0005, 0x0006, 0x0007, TypeInt64, TypeTime:
		return 8
	}
	return 0
}

// maxAlloc is the largest heap allocation; larger values go to subnodes.
const maxAlloc = 3580

// Built is an LTP object: its node data blocks and subnodes.
type Built struct {
	Blocks   [][]byte
	Subnodes []Subnode
}

type heapBuilder struct {
	sig    byte
	blocks [][][]byte // allocations per block
}

func hnHeader(block int) int {
	switch {
	case block == 0:
		return 12
	case block >= 8 && (block-8)%128 == 0:
		return 66
	}
	return 2
}

func (h *heapBuilder) alloc(data []byte) uint32 {
	if len(data) > maxAlloc {
		panic("pstgen: heap allocation too large")
	}
	fits := func(b int) bool {
		used := hnHeader(b)
		for _, a := range h.blocks[b] {
			used += len(a)
		}
		used += len(data)
		used += used & 1
		return used+4+2*(len(h.blocks[b])+2) <= MaxBlock && len(h.blocks[b]) < 2047
	}
	if len(h.blocks) == 0 || !fits(len(h.blocks)-1) {
		h.blocks = append(h.blocks, nil)
	}
	b := len(h.blocks) - 1
	h.blocks[b] = append(h.blocks[b], data)
	return uint32(b)<<16 | uint32(len(h.blocks[b]))<<5
}

func (h *heapBuilder) finish(root uint32) [][]byte {
	if len(h.blocks) == 0 {
		h.blocks = append(h.blocks, nil)
	}
	var out [][]byte
	for i, allocs := range h.blocks {
		b := make([]byte, hnHeader(i))
		offs := []int{len(b)}
		for _, a := range allocs {
			b = append(b, a...)
			offs = append(offs, len(b))
		}
		if len(b)%2 == 1 {
			b = append(b, 0)
		}
		binary.LittleEndian.PutUint16(b[0:], uint16(len(b)))
		if i == 0 {
			b[2], b[3] = 0xEC, h.sig
			binary.LittleEndian.PutUint32(b[4:], root)
		}
		b = binary.LittleEndian.AppendUint16(b, uint16(len(allocs)))
		b = binary.LittleEndian.AppendUint16(b, 0)
		for _, o := range offs {
			b = binary.LittleEndian.AppendUint16(b, uint16(o))
		}
		out = append(out, b)
	}
	return out
}

// Heap builds a raw heap-on-node with client signature sig whose
// allocations are allocs (in one page: allocation k has HID (k+1)<<5) and
// whose user root is the last allocation. It is for crafting hostile
// structures.
func Heap(sig byte, allocs ...[]byte) Built {
	h := &heapBuilder{sig: sig}
	var root uint32
	for _, a := range allocs {
		root = h.alloc(a)
	}
	return Built{Blocks: h.finish(root)}
}

// bth writes a BTH over sorted records (key followed by data) and returns
// the HID of its header.
func (h *heapBuilder) bth(cbKey, cbEnt int, recs [][]byte) uint32 {
	var root uint32
	levels := 0
	for len(recs) > 0 {
		size := len(recs[0])
		per := maxAlloc / size
		var next [][]byte
		for i := 0; i < len(recs); i += per {
			j := min(i+per, len(recs))
			var a []byte
			for _, r := range recs[i:j] {
				a = append(a, r...)
			}
			hid := h.alloc(a)
			next = append(next, binary.LittleEndian.AppendUint32(append([]byte(nil), recs[i][:cbKey]...), hid))
		}
		if len(next) == 1 {
			root = binary.LittleEndian.Uint32(next[0][cbKey:])
			break
		}
		recs = next
		levels++
	}
	hdr := []byte{0xB5, byte(cbKey), byte(cbEnt), byte(levels)}
	return h.alloc(binary.LittleEndian.AppendUint32(hdr, root))
}

// subAlloc stores large values in subnodes.
type subAlloc struct{ subs []Subnode }

func (s *subAlloc) add(data []byte) uint32 {
	nid := subNID()
	s.subs = append(s.subs, Subnode{NID: nid, Blocks: Split(data)})
	return nid
}

// hnid stores a variable-size value in the heap or a subnode.
func hnid(h *heapBuilder, s *subAlloc, v []byte) uint32 {
	if len(v) == 0 {
		return 0
	}
	if len(v) <= maxAlloc {
		return h.alloc(v)
	}
	return s.add(v)
}

// PC builds a property context. Object properties (TypeObject) take a
// Value of 4 bytes: the NID of a subnode supplied in extra, whose data size
// is computed from it.
func PC(props []Prop, extra ...Subnode) Built {
	h := &heapBuilder{sig: 0xBC}
	s := &subAlloc{}
	props = append([]Prop(nil), props...)
	sort.SliceStable(props, func(i, j int) bool { return props[i].ID < props[j].ID })
	var recs [][]byte
	for _, p := range props {
		r := binary.LittleEndian.AppendUint16(nil, p.ID)
		r = binary.LittleEndian.AppendUint16(r, p.Type)
		var v uint32
		switch n := fixedSize(p.Type); {
		case p.Type == TypeObject:
			nid := binary.LittleEndian.Uint32(p.Value)
			size := 0
			for _, e := range extra {
				if e.NID == nid {
					for _, b := range e.Blocks {
						size += len(b)
					}
				}
			}
			v = h.alloc(binary.LittleEndian.AppendUint32(append([]byte(nil), p.Value[:4]...), uint32(size)))
		case n > 0 && n <= 4:
			var b [4]byte
			copy(b[:], p.Value)
			v = binary.LittleEndian.Uint32(b[:])
		default:
			v = hnid(h, s, p.Value)
		}
		recs = append(recs, binary.LittleEndian.AppendUint32(r, v))
	}
	root := h.bth(2, 6, recs)
	return Built{Blocks: h.finish(root), Subnodes: append(s.subs, extra...)}
}

// Col is a table column.
type Col struct{ ID, Type uint16 }

// Row is a table row: its row ID (the NID of the object it describes) and
// cell values (columns without a value are absent).
type Row struct {
	ID    uint32
	Props []Prop
}

// TC builds a table context with the given columns (the row ID and row
// version columns are added).
func TC(cols []Col, rows []Row) Built {
	h := &heapBuilder{sig: 0x7C}
	s := &subAlloc{}
	type colDesc struct {
		Col
		ib, cb int
		bit    int
	}
	all := append([]Col{{0x67F2, TypeInt32}, {0x67F3, TypeInt32}}, cols...)
	descs := make([]colDesc, len(all))
	cellSize := func(c Col) int {
		if n := fixedSize(c.Type); n > 0 {
			return n
		}
		return 4
	}
	// Row ID and version first, then 8-, 4-, 2- and 1-byte cells, then the
	// cell existence bitmap.
	descs[0] = colDesc{all[0], 0, 4, 0}
	descs[1] = colDesc{all[1], 4, 4, 1}
	off, end4, end2 := 8, 0, 0
	for _, want := range []int{8, 4, 2, 1} {
		switch want {
		case 2:
			end4 = off
		case 1:
			end2 = off
		}
		for i := 2; i < len(all); i++ {
			if cellSize(all[i]) == want {
				descs[i] = colDesc{all[i], off, want, i}
				off += want
			}
		}
	}
	end1 := off
	rowSize := end1 + (len(all)+7)/8

	var matrix []byte
	var idx [][]byte
	for ri, r := range rows {
		row := make([]byte, rowSize)
		vals := append([]Prop{I32(0x67F2, int32(r.ID)), I32(0x67F3, 1)}, r.Props...)
		for _, p := range vals {
			for _, d := range descs {
				if d.ID != p.ID || d.Type != p.Type {
					continue
				}
				if fixedSize(p.Type) > 0 {
					copy(row[d.ib:d.ib+d.cb], p.Value)
				} else {
					binary.LittleEndian.PutUint32(row[d.ib:], hnid(h, s, p.Value))
				}
				row[end1+d.bit/8] |= 0x80 >> (d.bit % 8)
			}
		}
		matrix = append(matrix, row...)
		k := binary.LittleEndian.AppendUint32(nil, r.ID)
		idx = append(idx, binary.LittleEndian.AppendUint32(k, uint32(ri)))
	}
	sort.Slice(idx, func(i, j int) bool {
		return binary.LittleEndian.Uint32(idx[i]) < binary.LittleEndian.Uint32(idx[j])
	})
	var rowsRef uint32
	switch {
	case len(matrix) == 0:
	case len(matrix) <= maxAlloc:
		rowsRef = h.alloc(matrix)
	default:
		per := MaxBlock / rowSize
		var blocks [][]byte
		for i := 0; i < len(rows); i += per {
			j := min(i+per, len(rows))
			blocks = append(blocks, matrix[i*rowSize:j*rowSize])
		}
		rowsRef = subNID()
		s.subs = append(s.subs, Subnode{NID: rowsRef, Blocks: blocks})
	}
	rowIndex := h.bth(4, 4, idx)

	sorted := append([]colDesc(nil), descs...)
	sort.Slice(sorted, func(i, j int) bool {
		return uint32(sorted[i].ID)<<16|uint32(sorted[i].Type) < uint32(sorted[j].ID)<<16|uint32(sorted[j].Type)
	})
	info := []byte{0x7C, byte(len(all))}
	for _, v := range []int{end4, end2, end1, rowSize} {
		info = binary.LittleEndian.AppendUint16(info, uint16(v))
	}
	info = binary.LittleEndian.AppendUint32(info, rowIndex)
	info = binary.LittleEndian.AppendUint32(info, rowsRef)
	info = binary.LittleEndian.AppendUint32(info, 0)
	for _, d := range sorted {
		info = binary.LittleEndian.AppendUint32(info, uint32(d.ID)<<16|uint32(d.Type))
		info = binary.LittleEndian.AppendUint16(info, uint16(d.ib))
		info = append(info, byte(d.cb), byte(d.bit))
	}
	root := h.alloc(info)
	return Built{Blocks: h.finish(root), Subnodes: s.subs}
}
