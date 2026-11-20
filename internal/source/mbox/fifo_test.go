//go:build !windows

// FIFO tests: syscall.Mkfifo does not exist on Windows (not a target).

package mbox

import (
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/lazyoracle/mail-export-viewer/internal/source"
)

func TestNonRegularFilesRefused(t *testing.T) {
	dir := t.TempDir()
	fifo := filepath.Join(dir, "a.eml")
	if err := syscall.Mkfifo(fifo, 0o644); err != nil {
		t.Skip("no fifo support")
	}
	src := openEML(t, dir, "X", fifo)
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, warns := scan(t, src)
		if len(warns) != 1 {
			t.Errorf("warnings %v", warns)
		}
		if _, err := src.Load(source.Locator{Name: "a.eml"}); err == nil {
			t.Error("Load of a fifo succeeded")
		}
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("blocked on a FIFO")
	}
}
