// Package pstgen writes small synthetic Unicode PST files for tests of the
// pst reader. It is test-only infrastructure written from [MS-PST]: it
// emits only what a reader needs (header with CRCs, NBT/BBT B-trees, data
// blocks with trailers, XBLOCK/XXBLOCK data trees, SLBLOCK/SIBLOCK subnode
// trees, permute/cyclic encoding, and the LTP and messaging structures in
// ltp.go and messaging.go). Allocation maps are not written (fAMapValid is
// 0, which tells Outlook to rebuild them), so the files are meant for
// readers, not for Outlook.
//
// The writer panics on misuse (oversized blocks, unsorted input it cannot
// fix): it is only ever driven by tests.
package pstgen

import (
	"encoding/binary"
	"hash/crc32"
	"sort"

	"github.com/lazyoracle/mail-export-viewer/internal/source/pst/internal/pstcrypt"
)

// Encodings (bCryptMethod).
const (
	CryptNone    = 0
	CryptPermute = 1
	CryptCyclic  = 2
)

// MaxBlock is the largest data block payload of a Unicode PST.
const MaxBlock = 8192 - 16

// Node is a top-level node of the node B-tree.
type Node struct {
	NID, Parent uint32
	// Blocks are the node's data blocks in order (an HN keeps one heap page
	// per block); nil means no data. Each must be at most MaxBlock bytes.
	Blocks   [][]byte
	Subnodes []Subnode
}

// Subnode is an entry of a node's subnode tree.
type Subnode struct {
	NID      uint32
	Blocks   [][]byte
	Subnodes []Subnode
}

// NDB is the node database layer of a file.
type NDB struct {
	Crypt byte
	Nodes []Node
}

// Split cuts b into blocks of at most MaxBlock bytes (one empty block for
// an empty b).
func Split(b []byte) [][]byte {
	var out [][]byte
	for len(b) > MaxBlock {
		out = append(out, b[:MaxBlock])
		b = b[MaxBlock:]
	}
	return append(out, b)
}

type bbtEntry struct {
	bid, ib uint64
	cb      uint16
}

type nbtEntry struct {
	nid             uint32
	bidData, bidSub uint64
	parent          uint32
}

type writer struct {
	buf      []byte
	crypt    byte
	nextBID  uint64
	nextPage uint64
	bbt      []bbtEntry
}

const dataStart = 0x4400

// Bytes serialises the database as a complete PST file.
func (d *NDB) Bytes() []byte {
	w := &writer{buf: make([]byte, dataStart), crypt: d.Crypt, nextBID: 4, nextPage: 4}
	var nbt []nbtEntry
	for _, n := range d.Nodes {
		nbt = append(nbt, nbtEntry{nid: n.NID, parent: n.Parent, bidData: w.dataTree(n.Blocks), bidSub: w.subTree(n.Subnodes)})
	}
	sort.Slice(nbt, func(i, j int) bool { return nbt[i].nid < nbt[j].nid })
	sort.Slice(w.bbt, func(i, j int) bool { return w.bbt[i].bid < w.bbt[j].bid })

	var nbtLeaves, bbtLeaves [][]byte
	for _, e := range nbt {
		b := make([]byte, 32)
		binary.LittleEndian.PutUint32(b[0:], e.nid)
		binary.LittleEndian.PutUint64(b[8:], e.bidData)
		binary.LittleEndian.PutUint64(b[16:], e.bidSub)
		binary.LittleEndian.PutUint32(b[24:], e.parent)
		nbtLeaves = append(nbtLeaves, b)
	}
	for _, e := range w.bbt {
		b := make([]byte, 24)
		binary.LittleEndian.PutUint64(b[0:], e.bid)
		binary.LittleEndian.PutUint64(b[8:], e.ib)
		binary.LittleEndian.PutUint16(b[16:], e.cb)
		binary.LittleEndian.PutUint16(b[18:], 2) // cRef
		bbtLeaves = append(bbtLeaves, b)
	}
	nbtBid, nbtIB := w.btree(0x81, nbtLeaves)
	bbtBid, bbtIB := w.btree(0x80, bbtLeaves)
	w.header(nbtBid, nbtIB, bbtBid, bbtIB)
	return w.buf
}

// CRC is the [MS-PST] 5.3 CRC: CRC-32 (reflected 0xEDB88320) without the
// initial and final inversion.
func CRC(b []byte) uint32 { return ^crc32.Update(^uint32(0), crc32.IEEETable, b) }

// sig is ComputeSig of [MS-PST] 5.5.
func sig(ib, bid uint64) uint16 {
	v := ib ^ bid
	return uint16(v>>16) ^ uint16(v)
}

func (w *writer) align(n int) {
	for len(w.buf)%n != 0 {
		w.buf = append(w.buf, 0)
	}
}

func (w *writer) newBID(internal bool) uint64 {
	b := w.nextBID
	w.nextBID += 4
	if internal {
		b |= 2
	}
	return b
}

// block writes one block and returns its BID.
func (w *writer) block(data []byte, internal bool) uint64 {
	if len(data) > MaxBlock {
		panic("pstgen: block too large")
	}
	bid := w.newBID(internal)
	stored := append([]byte(nil), data...)
	if !internal {
		switch w.crypt {
		case CryptPermute:
			pstcrypt.Permute(stored, true)
		case CryptCyclic:
			pstcrypt.Cyclic(stored, uint32(bid))
		}
	}
	w.align(64)
	ib := uint64(len(w.buf))
	total := (len(stored) + 16 + 63) / 64 * 64
	blk := make([]byte, total)
	copy(blk, stored)
	t := blk[total-16:]
	binary.LittleEndian.PutUint16(t[0:], uint16(len(stored)))
	binary.LittleEndian.PutUint16(t[2:], sig(ib, bid))
	binary.LittleEndian.PutUint32(t[4:], CRC(stored))
	binary.LittleEndian.PutUint64(t[8:], bid)
	w.buf = append(w.buf, blk...)
	w.bbt = append(w.bbt, bbtEntry{bid: bid, ib: ib, cb: uint16(len(stored))})
	return bid
}

// xblock writes an XBLOCK (level 1) or XXBLOCK (level 2) over bids.
func (w *writer) xblock(level byte, bids []uint64, total int) uint64 {
	b := make([]byte, 8+8*len(bids))
	b[0], b[1] = 1, level
	binary.LittleEndian.PutUint16(b[2:], uint16(len(bids)))
	binary.LittleEndian.PutUint32(b[4:], uint32(total))
	for i, id := range bids {
		binary.LittleEndian.PutUint64(b[8+8*i:], id)
	}
	return w.block(b, true)
}

const xPerBlock = (MaxBlock - 8) / 8

// dataTree writes blocks as a single block, an XBLOCK or an XXBLOCK.
func (w *writer) dataTree(blocks [][]byte) uint64 {
	if blocks == nil {
		return 0
	}
	if len(blocks) == 1 {
		return w.block(blocks[0], false)
	}
	var bids []uint64
	var sizes []int
	total := 0
	for _, b := range blocks {
		bids = append(bids, w.block(b, false))
		sizes = append(sizes, len(b))
		total += len(b)
	}
	if len(bids) <= xPerBlock {
		return w.xblock(1, bids, total)
	}
	var xs []uint64
	for i := 0; i < len(bids); i += xPerBlock {
		j := min(i+xPerBlock, len(bids))
		n := 0
		for _, s := range sizes[i:j] {
			n += s
		}
		xs = append(xs, w.xblock(1, bids[i:j], n))
	}
	if len(xs) > xPerBlock {
		panic("pstgen: data tree too large")
	}
	return w.xblock(2, xs, total)
}

const slPerBlock = (MaxBlock - 8) / 24

// subTree writes an SLBLOCK, or SLBLOCKs under an SIBLOCK.
func (w *writer) subTree(subs []Subnode) uint64 {
	if len(subs) == 0 {
		return 0
	}
	subs = append([]Subnode(nil), subs...)
	sort.Slice(subs, func(i, j int) bool { return subs[i].NID < subs[j].NID })
	type ent struct {
		nid             uint32
		bidData, bidSub uint64
	}
	var ents []ent
	for _, s := range subs {
		ents = append(ents, ent{s.NID, w.dataTree(s.Blocks), w.subTree(s.Subnodes)})
	}
	sl := func(es []ent) uint64 {
		b := make([]byte, 8+24*len(es))
		b[0], b[1] = 2, 0
		binary.LittleEndian.PutUint16(b[2:], uint16(len(es)))
		for i, e := range es {
			o := 8 + 24*i
			binary.LittleEndian.PutUint32(b[o:], e.nid)
			binary.LittleEndian.PutUint64(b[o+8:], e.bidData)
			binary.LittleEndian.PutUint64(b[o+16:], e.bidSub)
		}
		return w.block(b, true)
	}
	if len(ents) <= slPerBlock {
		return sl(ents)
	}
	var si []byte
	n := 0
	for i := 0; i < len(ents); i += slPerBlock {
		j := min(i+slPerBlock, len(ents))
		e := make([]byte, 16)
		binary.LittleEndian.PutUint32(e[0:], ents[i].nid)
		binary.LittleEndian.PutUint64(e[8:], sl(ents[i:j]))
		si = append(si, e...)
		n++
	}
	b := append([]byte{2, 1, byte(n), byte(n >> 8), 0, 0, 0, 0}, si...)
	return w.block(b, true)
}

// page writes one 512-byte page and returns its BID and offset.
func (w *writer) page(ptype byte, body []byte) (uint64, uint64) {
	w.align(512)
	ib := uint64(len(w.buf))
	bid := w.nextPage
	w.nextPage += 4
	p := make([]byte, 512)
	copy(p, body)
	p[496], p[497] = ptype, ptype
	binary.LittleEndian.PutUint16(p[498:], sig(ib, bid))
	binary.LittleEndian.PutUint32(p[500:], CRC(p[:496]))
	binary.LittleEndian.PutUint64(p[504:], bid)
	w.buf = append(w.buf, p...)
	return bid, ib
}

// btree writes the pages of a B-tree over sorted leaf entries (key in the
// first 8 bytes) and returns the root page's BREF.
func (w *writer) btree(ptype byte, leaves [][]byte) (uint64, uint64) {
	level := 0
	entries := leaves
	cb := 32
	if ptype == 0x80 {
		cb = 24
	}
	for {
		per := 488 / cb
		var next [][]byte
		var bid, ib uint64
		for i := 0; i < len(entries) || i == 0; i += per {
			j := min(i+per, len(entries))
			body := make([]byte, 496)
			for k, e := range entries[i:j] {
				copy(body[k*cb:], e)
			}
			body[488] = byte(j - i)
			body[489] = byte(per)
			body[490] = byte(cb)
			body[491] = byte(level)
			bid, ib = w.page(ptype, body)
			e := make([]byte, 24)
			if j > i {
				copy(e[:8], entries[i][:8])
			}
			binary.LittleEndian.PutUint64(e[8:], bid)
			binary.LittleEndian.PutUint64(e[16:], ib)
			next = append(next, e)
		}
		if len(next) == 1 {
			return bid, ib
		}
		entries, cb = next, 24
		level++
	}
}

func (w *writer) header(nbtBid, nbtIB, bbtBid, bbtIB uint64) {
	w.align(512)
	h := w.buf[:564]
	copy(h, "!BDN")
	copy(h[8:], "SM")
	binary.LittleEndian.PutUint16(h[10:], 23)
	binary.LittleEndian.PutUint16(h[12:], 19)
	h[14], h[15] = 1, 1
	binary.LittleEndian.PutUint64(h[32:], w.nextPage)
	binary.LittleEndian.PutUint32(h[40:], 1)
	for i := 0; i < 32; i++ {
		binary.LittleEndian.PutUint32(h[44+4*i:], 0x10000)
	}
	r := h[180:]
	binary.LittleEndian.PutUint64(r[4:], uint64(len(w.buf)))
	binary.LittleEndian.PutUint64(r[36:], nbtBid)
	binary.LittleEndian.PutUint64(r[44:], nbtIB)
	binary.LittleEndian.PutUint64(r[52:], bbtBid)
	binary.LittleEndian.PutUint64(r[60:], bbtIB)
	for i := 256; i < 512; i++ {
		h[i] = 0xFF
	}
	h[512] = 0x80
	h[513] = w.crypt
	binary.LittleEndian.PutUint64(h[516:], w.nextBID)
	binary.LittleEndian.PutUint32(h[4:], CRC(h[8:8+471]))
	binary.LittleEndian.PutUint32(h[524:], CRC(h[8:8+516]))
}
