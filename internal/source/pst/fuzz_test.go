package pst

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lazyoracle/mail-export-viewer/internal/source"
	"github.com/lazyoracle/mail-export-viewer/internal/source/pst/pstgen"
	"github.com/lazyoracle/mail-export-viewer/internal/source/sourcetest"
)

// smallFixture is fixture without the 3 MiB message, small enough to be a
// fuzz seed.
func smallFixture(crypt byte) []byte {
	f := fixture(crypt)
	f.Folders = append(f.Folders[:1], f.Folders[2:]...)
	return f.Bytes()
}

// FuzzSource feeds arbitrary bytes as a .pst file (seeded with valid
// synthetic files in each encoding) through Open, Scan, Folders, Load, Raw
// and Attachment, failing on any recovered panic.
func FuzzSource(f *testing.F) {
	for _, c := range []byte{pstgen.CryptNone, pstgen.CryptPermute, pstgen.CryptCyclic} {
		f.Add(smallFixture(c))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		path := filepath.Join(t.TempDir(), "backup.pst")
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
		src, err := open(source.Candidate{Format: source.PST, Path: path, Rel: "backup.pst"})
		if err != nil {
			return
		}
		defer src.Close()
		sourcetest.Within(t, 10*time.Second, func() {
			if st, err := src.(*pstSource).store(); err == nil {
				st.folders() // unrecovered: Folders() hides panics
			}
			if _, panics := sourcetest.Exercise(src, 50); len(panics) > 0 {
				t.Errorf("recovered panics: %q", panics)
			}
		})
	})
}
