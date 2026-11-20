//go:build darwin || linux

package index

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFileLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.run")
	l1, err := AcquireLock(path)
	if err != nil {
		t.Fatal(err)
	}
	l1.Record("pid 7, http://127.0.0.1:1/")
	_, err = AcquireLock(path)
	if !errors.Is(err, ErrLocked) || !strings.Contains(err.Error(), "(pid 7, http://127.0.0.1:1/)") {
		t.Fatalf("second lock: %v", err)
	}
	if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o600 {
		t.Fatalf("lock mode %v", fi.Mode())
	}
	l1.Release()
	l1.Release() // idempotent
	l2, err := AcquireLock(path)
	if err != nil {
		t.Fatalf("after release: %v", err)
	}
	l2.Release()
	// A symlink at the lock path is refused, its target untouched.
	target := filepath.Join(t.TempDir(), "target")
	os.WriteFile(target, []byte("keep"), 0o644)
	link := filepath.Join(t.TempDir(), "l.run")
	if err := os.Symlink(target, link); err != nil {
		t.Skip(err)
	}
	if _, err := AcquireLock(link); !errors.Is(err, ErrNotRegular) {
		t.Fatalf("symlink lock: %v", err)
	}
	if b, _ := os.ReadFile(target); string(b) != "keep" {
		t.Fatalf("target modified: %q", b)
	}
}
