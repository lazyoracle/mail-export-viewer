package corpus

import (
	"crypto/sha256"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

func treeHash(t *testing.T, root string) [32]byte {
	t.Helper()
	h := sha256.New()
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		// pstgen numbers subnodes from a process-wide counter, so a PST
		// differs between two generations in one process (not between
		// two runs of gencorpus).
		if err != nil || d.IsDir() || filepath.Ext(p) == ".pst" {
			return err
		}
		b, err := os.ReadFile(p)
		rel, _ := filepath.Rel(root, p)
		h.Write([]byte(rel + "\x00"))
		h.Write(b)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	var out [32]byte
	copy(out[:], h.Sum(nil))
	return out
}

// The same options give byte-identical corpora (PST files aside, see
// treeHash) and manifests.
func TestDeterministic(t *testing.T) {
	o := Options{Seed: 3, BigBytes: 64 << 10, Sets: []string{Gmail, Thunderbird, Apple, EML, OLM, PST, Bulk}, Bulk: 300, BulkThread: 20}
	a, b := t.TempDir(), t.TempDir()
	ma, err := Generate(a, o)
	if err != nil {
		t.Fatal(err)
	}
	mb, err := Generate(b, o)
	if err != nil {
		t.Fatal(err)
	}
	if treeHash(t, a) != treeHash(t, b) || len(ma.Messages) != len(mb.Messages) {
		t.Fatal("corpus is not deterministic")
	}
	tokens := map[string]bool{}
	for _, m := range ma.Messages {
		if m.Token != "" && tokens[m.Token] {
			t.Fatalf("token %s used twice", m.Token)
		}
		tokens[m.Token] = true
		if m.Source == "" || m.Folder == "" || m.Kind == "" {
			t.Fatalf("incomplete ground truth: %+v", m)
		}
	}
}
