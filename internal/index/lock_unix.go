//go:build darwin || linux

package index

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"syscall"
)

// FileLock is an exclusive advisory lock (flock) on a file: the index's
// "<db>.lock" (taken by Open) and the command's instance lock. The kernel
// releases it when the process dies, so a stale lock file never blocks a
// restart. The file holds an owner note (Record), quoted to a refused
// second owner.
type FileLock struct{ f *os.File }

// AcquireLock takes the lock at path without waiting, creating the file
// with mode 0600 (never through a symlink: ErrNotRegular). If another
// process holds it, the error wraps ErrLocked and quotes the owner note.
func AcquireLock(path string) (*FileLock, error) {
	f, err := openPrivateFile(path, true)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		defer f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			owner := ""
			if b, rerr := os.ReadFile(path); rerr == nil {
				if s := strings.TrimSpace(string(b)); s != "" && len(s) < 512 {
					owner = " (" + s + ")"
				}
			}
			return nil, fmt.Errorf("%w%s", ErrLocked, owner)
		}
		return nil, fmt.Errorf("index: lock %s: %w", path, err)
	}
	l := &FileLock{f: f}
	l.Record(fmt.Sprintf("pid %d", os.Getpid()))
	return l, nil
}

// Record replaces the owner note.
func (l *FileLock) Record(note string) {
	if l == nil || l.f == nil {
		return
	}
	if err := l.f.Truncate(0); err == nil {
		l.f.WriteAt([]byte(note+"\n"), 0)
	}
}

// Release drops the lock; later calls do nothing. The file stays:
// removing it would race with another process that has just opened it.
func (l *FileLock) Release() {
	if l == nil || l.f == nil {
		return
	}
	l.f.Truncate(0)
	syscall.Flock(int(l.f.Fd()), syscall.LOCK_UN)
	l.f.Close()
	l.f = nil
}
