package pst

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"sync"

	"github.com/lazyoracle/mail-export-viewer/internal/source/pst/internal/pstcrypt"
)

// NDB layer ([MS-PST] 2.2): header, B-tree pages, blocks, data trees and
// subnode trees. Everything read from the file is bounds-checked; structural
// problems are errCorrupt, never panics.

var (
	errCorrupt  = errors.New("pst: corrupt structure")
	errNotFound = errors.New("pst: not found")
	errTooBig   = errors.New("pst: data too large")
)

func corrupt(format string, args ...any) error {
	return fmt.Errorf("%w: %s", errCorrupt, fmt.Sprintf(format, args...))
}

// layout holds the sizes that differ between ANSI and Unicode files.
type layout struct {
	unicode   bool
	idSize    int // BID/IB size: 8 (Unicode) or 4 (ANSI)
	entries   int // bytes of BTPAGE entry area (the page metadata follows)
	blockTrl  int // block trailer size
	maxBlock  int // largest block payload
	btEntry   int // intermediate BTENTRY size
	bbtEntry  int // leaf BBTENTRY minimum size
	nbtEntry  int // leaf NBTENTRY minimum size
	slHeader  int // SLBLOCK/SIBLOCK header size
	slEntry   int
	siEntry   int
	rowIdxLen int // TCROWID dwRowIndex size
}

var (
	unicodeLayout = layout{unicode: true, idSize: 8, entries: 488, blockTrl: 16, maxBlock: 8192 - 16,
		btEntry: 24, bbtEntry: 20, nbtEntry: 28, slHeader: 8, slEntry: 24, siEntry: 16, rowIdxLen: 4}
	ansiLayout = layout{idSize: 4, entries: 496, blockTrl: 12, maxBlock: 8192 - 12,
		btEntry: 12, bbtEntry: 12, nbtEntry: 16, slHeader: 4, slEntry: 12, siEntry: 8, rowIdxLen: 2}
)

type bref struct{ bid, ib uint64 }

type nodeEntry struct {
	nid             uint32
	bidData, bidSub uint64
	parent          uint32
}

type subnode struct{ bidData, bidSub uint64 }

type ndb struct {
	r        io.ReaderAt
	fileSize int64
	l        layout
	crypt    byte
	nbt      bref
	bbt      bref
	mu       sync.Mutex
	pageMap  map[uint64][]byte
}

const (
	headerSize   = 564
	pageSize     = 512
	maxBTDepth   = 16
	maxPageCache = 4096
	// maxSubnodes bounds one subnode tree.
	maxSubnodes = 1 << 20
)

// Page types.
const (
	ptypeBBT = 0x80
	ptypeNBT = 0x81
)

func openNDB(r io.ReaderAt, size int64) (*ndb, error) {
	h := make([]byte, headerSize)
	if size < headerSize {
		return nil, errors.New("pst: file too short")
	}
	if _, err := r.ReadAt(h, 0); err != nil {
		return nil, fmt.Errorf("pst: reading header: %w", err)
	}
	if string(h[:4]) != "!BDN" || string(h[8:10]) != "SM" {
		return nil, errors.New("pst: not a PST file (bad magic)")
	}
	d := &ndb{r: r, fileSize: size, pageMap: map[uint64][]byte{}}
	ver := binary.LittleEndian.Uint16(h[10:])
	var root []byte
	switch {
	case ver == 14 || ver == 15:
		d.l = ansiLayout
		root = h[164:204]
		d.crypt = h[461]
		d.nbt = bref{uint64(le32(root, 20)), uint64(le32(root, 24))}
		d.bbt = bref{uint64(le32(root, 28)), uint64(le32(root, 32))}
	case ver >= 21 && ver <= 23:
		d.l = unicodeLayout
		root = h[180:252]
		d.crypt = h[513]
		d.nbt = bref{le64(root, 36), le64(root, 44)}
		d.bbt = bref{le64(root, 52), le64(root, 60)}
	case ver == 36:
		return nil, errors.New("pst: 4K-page OST files are not supported")
	default:
		return nil, fmt.Errorf("pst: unsupported format version %d", ver)
	}
	switch d.crypt {
	case 0, 1, 2:
	default:
		return nil, fmt.Errorf("pst: unsupported encryption method %#x", d.crypt)
	}
	return d, nil
}

func le16(b []byte, o int) uint16 { return binary.LittleEndian.Uint16(b[o:]) }
func le32(b []byte, o int) uint32 { return binary.LittleEndian.Uint32(b[o:]) }
func le64(b []byte, o int) uint64 { return binary.LittleEndian.Uint64(b[o:]) }

// id reads a BID or IB of the file's size at o.
func (d *ndb) id(b []byte, o int) uint64 {
	if d.l.idSize == 8 {
		return le64(b, o)
	}
	return uint64(le32(b, o))
}

// readAt reads n bytes at off after checking they lie inside the file.
func (d *ndb) readAt(off uint64, n int) ([]byte, error) {
	if n < 0 || off > uint64(d.fileSize) || uint64(n) > uint64(d.fileSize)-off {
		return nil, corrupt("range %d+%d outside file", off, n)
	}
	b := make([]byte, n)
	if _, err := d.r.ReadAt(b, int64(off)); err != nil {
		return nil, fmt.Errorf("pst: read at %d: %w", off, err)
	}
	return b, nil
}

// page reads (and caches) the B-tree page at ref, checking its trailer.
func (d *ndb) page(ref bref, ptype byte) ([]byte, error) {
	d.mu.Lock()
	p, ok := d.pageMap[ref.ib]
	d.mu.Unlock()
	if ok {
		return p, nil
	}
	if ref.ib%pageSize != 0 {
		return nil, corrupt("unaligned page at %d", ref.ib)
	}
	p, err := d.readAt(ref.ib, pageSize)
	if err != nil {
		return nil, err
	}
	t := p[pageSize-16:]
	if !d.l.unicode {
		t = p[pageSize-12:]
	}
	var bid uint64
	if d.l.unicode {
		bid = le64(t, 8)
	} else {
		bid = uint64(le32(t, 4))
	}
	if t[0] != ptype || t[1] != ptype || bid != ref.bid {
		return nil, corrupt("bad page trailer at %d", ref.ib)
	}
	d.mu.Lock()
	if len(d.pageMap) >= maxPageCache {
		clear(d.pageMap)
	}
	d.pageMap[ref.ib] = p
	d.mu.Unlock()
	return p, nil
}

// btLookup finds the leaf entry with key in the B-tree rooted at root. For
// the NBT keys are NIDs (32 bits); for the BBT, BIDs with the reserved low
// bit ignored.
func (d *ndb) btLookup(root bref, ptype byte, key uint64) ([]byte, error) {
	ref, level := root, -1
	for depth := 0; depth < maxBTDepth; depth++ {
		p, err := d.page(ref, ptype)
		if err != nil {
			return nil, err
		}
		m := d.l.entries
		cEnt, cbEnt, cLevel := int(p[m]), int(p[m+2]), int(p[m+3])
		if level >= 0 && cLevel != level-1 || level < 0 && cLevel >= maxBTDepth {
			return nil, corrupt("bad B-tree level at %d", ref.ib)
		}
		minEnt := d.l.btEntry
		if cLevel == 0 {
			minEnt = d.l.nbtEntry
			if ptype == ptypeBBT {
				minEnt = d.l.bbtEntry
			}
		}
		if cbEnt < minEnt || cEnt*cbEnt > m {
			return nil, corrupt("bad B-tree entries at %d", ref.ib)
		}
		keyAt := func(i int) uint64 {
			k := d.id(p, i*cbEnt)
			if ptype == ptypeNBT {
				return k & 0xFFFFFFFF
			}
			return k &^ 1
		}
		if cLevel == 0 {
			for i := 0; i < cEnt; i++ {
				if keyAt(i) == key {
					return p[i*cbEnt : (i+1)*cbEnt], nil
				}
			}
			return nil, errNotFound
		}
		pick := -1
		for i := 0; i < cEnt && keyAt(i) <= key; i++ {
			pick = i
		}
		if pick < 0 {
			return nil, errNotFound
		}
		o := pick*cbEnt + d.l.idSize
		ref = bref{d.id(p, o), d.id(p, o+d.l.idSize)}
		level = cLevel
	}
	return nil, corrupt("B-tree too deep")
}

// node looks nid up in the NBT.
func (d *ndb) node(nid uint32) (nodeEntry, error) {
	e, err := d.btLookup(d.nbt, ptypeNBT, uint64(nid))
	if err != nil {
		return nodeEntry{}, err
	}
	s := d.l.idSize
	return nodeEntry{nid: nid, bidData: d.id(e, s), bidSub: d.id(e, 2*s), parent: le32(e, 3*s)}, nil
}

// block reads the block bid and decodes it if it is external.
func (d *ndb) block(bid uint64) ([]byte, error) {
	e, err := d.btLookup(d.bbt, ptypeBBT, bid&^1)
	if err != nil {
		if errors.Is(err, errNotFound) {
			return nil, corrupt("block %#x not in BBT", bid)
		}
		return nil, err
	}
	ib := d.id(e, d.l.idSize)
	cb := int(le16(e, 2*d.l.idSize))
	if cb > d.l.maxBlock {
		return nil, corrupt("block %#x too large", bid)
	}
	// Read the block with its trailer, whose size and BID must agree with
	// the BBT (a mismatch fails this block only).
	n := (cb + d.l.blockTrl + 63) / 64 * 64
	raw, err := d.readAt(ib, n)
	if err != nil {
		return nil, err
	}
	t := raw[n-d.l.blockTrl:]
	tbid := uint64(le32(t, 4))
	if d.l.unicode {
		tbid = le64(t, 8)
	}
	if int(le16(t, 0)) != cb || tbid&^1 != bid&^1 {
		return nil, corrupt("block %#x: trailer does not match", bid)
	}
	b := raw[:cb]
	if bid&2 == 0 {
		switch d.crypt {
		case 1:
			pstcrypt.Permute(b, false)
		case 2:
			pstcrypt.Cyclic(b, uint32(bid))
		}
	}
	return b, nil
}

// visit calls fn with the leaf blocks of the data tree at bid, in order,
// until fn returns false.
func (d *ndb) visit(bid uint64, level int, fn func([]byte) bool) (bool, error) {
	b, err := d.block(bid)
	if err != nil {
		return false, err
	}
	if bid&2 == 0 {
		if level > 0 {
			return false, corrupt("XBLOCK entry %#x is not a data block", bid)
		}
		return fn(b), nil
	}
	if len(b) < 8 || b[0] != 1 || b[1] < 1 || b[1] > 2 || level >= 0 && int(b[1]) != level {
		return false, corrupt("bad XBLOCK %#x", bid)
	}
	child := int(b[1]) - 1
	n := int(le16(b, 2))
	if 8+n*d.l.idSize > len(b) {
		return false, corrupt("XBLOCK %#x overflows", bid)
	}
	for i := 0; i < n; i++ {
		more, err := d.visit(d.id(b, 8+i*d.l.idSize), child, fn)
		if err != nil || !more {
			return more, err
		}
	}
	return true, nil
}

// size returns the byte count of the data tree at bid.
func (d *ndb) size(bid uint64) (int64, error) {
	if bid == 0 {
		return 0, nil
	}
	b, err := d.block(bid)
	if err != nil {
		return 0, err
	}
	if bid&2 == 0 {
		return int64(len(b)), nil
	}
	if len(b) < 8 || b[0] != 1 {
		return 0, corrupt("bad XBLOCK %#x", bid)
	}
	return int64(le32(b, 4)), nil
}

// blocks returns the leaf blocks of the data tree at bid (an HN keeps one
// heap page per block). It fails with errTooBig above max bytes.
func (d *ndb) blocks(bid uint64, max int64) ([][]byte, error) {
	if bid == 0 {
		return nil, nil
	}
	total, err := d.size(bid)
	if err != nil {
		return nil, err
	}
	if total > max {
		return nil, fmt.Errorf("%w: %d bytes", errTooBig, total)
	}
	var out [][]byte
	var n int64
	_, err = d.visit(bid, -1, func(b []byte) bool {
		n += int64(len(b))
		out = append(out, b)
		return n <= total
	})
	if err == nil && n > total {
		err = corrupt("data tree %#x holds more than its declared %d bytes", bid, total)
	}
	return out, err
}

// read returns at most limit bytes from the start of the data tree at bid,
// and its declared total size. A full read (limit >= total) visits every
// leaf and fails when they hold more than the declared total, so a tree
// that repeats its subtrees cannot amplify reads.
func (d *ndb) read(bid uint64, limit int64) ([]byte, int64, error) {
	total, err := d.size(bid)
	if err != nil || bid == 0 {
		return nil, total, err
	}
	want := min(limit, total)
	var out []byte
	var sum int64
	_, err = d.visit(bid, -1, func(b []byte) bool {
		if sum += int64(len(b)); sum > total {
			return false
		}
		out = append(out, b[:min(int64(len(b)), want-int64(len(out)))]...)
		return int64(len(out)) < want || limit >= total
	})
	if err == nil && sum > total {
		err = corrupt("data tree %#x holds more than its declared %d bytes", bid, total)
	}
	return out, total, err
}

// subnodes reads the subnode tree at bid into a map by NID.
func (d *ndb) subnodes(bid uint64) (map[uint32]subnode, error) {
	out := map[uint32]subnode{}
	if bid == 0 {
		return out, nil
	}
	return out, d.subtree(bid, -1, out)
}

func (d *ndb) subtree(bid uint64, level int, out map[uint32]subnode) error {
	b, err := d.block(bid)
	if err != nil {
		return err
	}
	if bid&2 == 0 || len(b) < d.l.slHeader || b[0] != 2 || b[1] > 1 || level >= 0 && int(b[1]) != level {
		return corrupt("bad subnode block %#x", bid)
	}
	n := int(le16(b, 2))
	s := d.l.idSize
	if b[1] == 0 {
		if d.l.slHeader+n*d.l.slEntry > len(b) {
			return corrupt("SLBLOCK %#x overflows", bid)
		}
		for i := 0; i < n; i++ {
			o := d.l.slHeader + i*d.l.slEntry
			if len(out) >= maxSubnodes {
				return corrupt("too many subnodes")
			}
			out[le32(b, o)] = subnode{bidData: d.id(b, o+s), bidSub: d.id(b, o+2*s)}
		}
		return nil
	}
	if d.l.slHeader+n*d.l.siEntry > len(b) {
		return corrupt("SIBLOCK %#x overflows", bid)
	}
	for i := 0; i < n; i++ {
		if err := d.subtree(d.id(b, d.l.slHeader+i*d.l.siEntry+s), 0, out); err != nil {
			return err
		}
	}
	return nil
}
