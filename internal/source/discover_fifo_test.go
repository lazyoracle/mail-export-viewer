//go:build !windows

// FIFO tests: syscall.Mkfifo does not exist on Windows (not a target).

package source_test

import (
	"path/filepath"
	"syscall"
	"testing"

	"github.com/lazyoracle/mail-export-viewer/internal/source"
)

func TestDiscoverSkipsSpecialFiles(t *testing.T) {
	useAllFakes(t)
	dir := t.TempDir()
	write(t, filepath.Join(dir, "box"), mboxData)
	fifo := filepath.Join(dir, "fifo")
	if err := syscall.Mkfifo(fifo, 0o644); err != nil {
		t.Skip("mkfifo:", err)
	}
	d, err := source.Discover(dir, source.Limits{}) // must not block on the FIFO
	if err != nil || len(d.Candidates) != 1 {
		t.Fatalf("%+v %v", d, err)
	}
	if _, err := source.Discover(fifo, source.Limits{}); err == nil {
		t.Error("FIFO as input: want error")
	}
}
