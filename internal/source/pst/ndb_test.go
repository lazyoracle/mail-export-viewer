package pst

import (
	"bytes"
	"errors"
	"fmt"
	"math/rand"
	"testing"

	"github.com/lazyoracle/mail-export-viewer/internal/source/pst/pstgen"
)

func openBytes(t *testing.T, b []byte) *ndb {
	t.Helper()
	d, err := openNDB(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		t.Fatalf("openNDB: %v", err)
	}
	return d
}

func blob(n int, seed byte) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(i*7) + seed
	}
	return b
}

func TestNDBNodes(t *testing.T) {
	for _, crypt := range []byte{pstgen.CryptNone, pstgen.CryptPermute, pstgen.CryptCyclic} {
		t.Run(fmt.Sprint("crypt", crypt), func(t *testing.T) {
			db := &pstgen.NDB{Crypt: crypt}
			// Enough nodes for multi-level NBT and BBT pages.
			for i := 0; i < 300; i++ {
				db.Nodes = append(db.Nodes, pstgen.Node{NID: uint32(0x400+i)<<5 | 4, Parent: 0x122, Blocks: [][]byte{blob(10+i, byte(i))}})
			}
			d := openBytes(t, db.Bytes())
			if d.crypt != crypt {
				t.Fatalf("crypt = %d", d.crypt)
			}
			for i := 0; i < 300; i++ {
				nid := uint32(0x400+i)<<5 | 4
				n, err := d.node(nid)
				if err != nil {
					t.Fatalf("node(%#x): %v", nid, err)
				}
				if n.parent != 0x122 {
					t.Fatalf("parent = %#x", n.parent)
				}
				got, total, err := d.read(n.bidData, 1<<20)
				if err != nil || total != int64(10+i) || !bytes.Equal(got, blob(10+i, byte(i))) {
					t.Fatalf("read node %d: %d bytes total %d err %v", i, len(got), total, err)
				}
			}
			if _, err := d.node(0x7FFFFFE4); !errors.Is(err, errNotFound) {
				t.Fatalf("missing node: %v", err)
			}
		})
	}
}

func TestNDBDataTrees(t *testing.T) {
	big := pstgen.Split(blob(3*pstgen.MaxBlock+100, 3))
	var tiny [][]byte // > one XBLOCK of entries: needs an XXBLOCK
	var tinyAll []byte
	for i := 0; i < 1100; i++ {
		b := blob(5, byte(i))
		tiny = append(tiny, b)
		tinyAll = append(tinyAll, b...)
	}
	db := &pstgen.NDB{Crypt: pstgen.CryptPermute, Nodes: []pstgen.Node{
		{NID: 0x24, Blocks: big},
		{NID: 0x44, Blocks: tiny},
	}}
	d := openBytes(t, db.Bytes())

	n, _ := d.node(0x24)
	blocks, err := d.blocks(n.bidData, 1<<20)
	if err != nil || len(blocks) != 4 {
		t.Fatalf("blocks: %d, %v", len(blocks), err)
	}
	for i := range blocks {
		if !bytes.Equal(blocks[i], big[i]) {
			t.Fatalf("block %d differs", i)
		}
	}
	all, total, err := d.read(n.bidData, 1<<20)
	if err != nil || total != int64(3*pstgen.MaxBlock+100) || !bytes.Equal(all, bytes.Join(big, nil)) {
		t.Fatalf("read: %d %v", total, err)
	}
	prefix, total, err := d.read(n.bidData, 10000)
	if err != nil || total != int64(3*pstgen.MaxBlock+100) || !bytes.Equal(prefix, all[:10000]) {
		t.Fatalf("prefix read: %d %d %v", len(prefix), total, err)
	}
	if sz, err := d.size(n.bidData); err != nil || sz != total {
		t.Fatalf("size = %d %v", sz, err)
	}
	if _, err := d.blocks(n.bidData, 1000); !errors.Is(err, errTooBig) {
		t.Fatalf("blocks over limit: %v", err)
	}

	n, _ = d.node(0x44)
	all, total, err = d.read(n.bidData, 1<<20)
	if err != nil || total != int64(len(tinyAll)) || !bytes.Equal(all, tinyAll) {
		t.Fatalf("xxblock read: %d %v", total, err)
	}
}

func TestNDBSubnodes(t *testing.T) {
	var many []pstgen.Subnode
	for i := 0; i < 400; i++ { // more than one SLBLOCK: needs an SIBLOCK
		many = append(many, pstgen.Subnode{NID: uint32(i+1)<<5 | 0x1F, Blocks: [][]byte{blob(3, byte(i))}})
	}
	db := &pstgen.NDB{Nodes: []pstgen.Node{
		{NID: 0x24, Blocks: [][]byte{[]byte("msg")}, Subnodes: []pstgen.Subnode{
			{NID: 0x671, Blocks: [][]byte{[]byte("table")}},
			{NID: 0x8025, Blocks: [][]byte{[]byte("attach")}, Subnodes: []pstgen.Subnode{
				{NID: 0x2A1F, Blocks: pstgen.Split(blob(9000, 1))},
			}},
		}},
		{NID: 0x44, Subnodes: many},
	}}
	d := openBytes(t, db.Bytes())
	n, _ := d.node(0x24)
	subs, err := d.subnodes(n.bidSub)
	if err != nil || len(subs) != 2 {
		t.Fatalf("subnodes: %v %v", subs, err)
	}
	got, _, _ := d.read(subs[0x671].bidData, 100)
	if string(got) != "table" {
		t.Fatalf("subnode data %q", got)
	}
	inner, err := d.subnodes(subs[0x8025].bidSub)
	if err != nil || len(inner) != 1 {
		t.Fatalf("nested subnodes: %v %v", inner, err)
	}
	got, _, _ = d.read(inner[0x2A1F].bidData, 1<<20)
	if !bytes.Equal(got, blob(9000, 1)) {
		t.Fatalf("nested data differs")
	}
	n, _ = d.node(0x44)
	if n.bidData != 0 {
		t.Fatalf("bidData = %d", n.bidData)
	}
	subs, err = d.subnodes(n.bidSub)
	if err != nil || len(subs) != 400 {
		t.Fatalf("SIBLOCK subnodes: %d %v", len(subs), err)
	}
	if got, _, _ := d.read(subs[400<<5|0x1F].bidData, 10); !bytes.Equal(got, blob(3, byte(399%256))) {
		t.Fatalf("last subnode %v", got)
	}
}

func TestNDBHeaderErrors(t *testing.T) {
	good := (&pstgen.NDB{Nodes: []pstgen.Node{{NID: 0x24, Blocks: [][]byte{[]byte("x")}}}}).Bytes()
	mut := func(f func(b []byte)) []byte {
		b := append([]byte(nil), good...)
		f(b)
		return b
	}
	for name, b := range map[string][]byte{
		"empty":     nil,
		"short":     good[:100],
		"magic":     mut(func(b []byte) { b[0] = 'X' }),
		"version":   mut(func(b []byte) { b[10] = 99 }),
		"crypt":     mut(func(b []byte) { b[513] = 0x10 }),
		"4k ost":    mut(func(b []byte) { b[10] = 36 }),
		"no client": mut(func(b []byte) { b[8] = 'X' }),
	} {
		if _, err := openNDB(bytes.NewReader(b), int64(len(b))); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}

// TestNDBCorruption flips random bytes of a file and walks it: errors are
// fine, panics and hangs are not.
func TestNDBCorruption(t *testing.T) {
	db := &pstgen.NDB{Crypt: pstgen.CryptPermute}
	for i := 0; i < 40; i++ {
		db.Nodes = append(db.Nodes, pstgen.Node{NID: uint32(0x400+i)<<5 | 4, Blocks: pstgen.Split(blob(100+i*500, byte(i))),
			Subnodes: []pstgen.Subnode{{NID: 0x671, Blocks: [][]byte{blob(20, 1)}}}})
	}
	good := db.Bytes()
	rng := rand.New(rand.NewSource(1))
	for iter := 0; iter < 300; iter++ {
		b := append([]byte(nil), good...)
		for k := 0; k < 1+rng.Intn(20); k++ {
			b[rng.Intn(len(b))] = byte(rng.Intn(256))
		}
		d, err := openNDB(bytes.NewReader(b), int64(len(b)))
		if err != nil {
			continue
		}
		for i := 0; i < 40; i++ {
			n, err := d.node(uint32(0x400+i)<<5 | 4)
			if err != nil {
				continue
			}
			d.blocks(n.bidData, 1<<20)
			d.read(n.bidData, 1<<20)
			d.subnodes(n.bidSub)
		}
	}
}
