package pst

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"

	"github.com/lazyoracle/mail-export-viewer/internal/source/pst/pstgen"
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
			}
		})
	}
}
