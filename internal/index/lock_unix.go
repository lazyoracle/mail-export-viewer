//go:build darwin || linux

package index

import (
	"errors"
	"fmt"
	"os"
	"syscall"
)

// fileLock is an exclusive advisory lock (flock) on "<db>.lock". The kernel
// releases it when the process dies, so a stale lock file never blocks a
// restart. The file records the owner's pid for the error message.
type fileLock struct{ f *os.File }

func acquireLock(path string) (*fileLock, error) {
	f, err := openPrivateFile(path, true)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, fmt.Errorf("%w (lock %s)", ErrLocked, path)
		}
		return nil, fmt.Errorf("index: lock %s: %w", path, err)
	}
	if err := f.Truncate(0); err == nil {
		f.WriteAt([]byte(fmt.Sprintf("pid %d\n", os.Getpid())), 0)
	}
	return &fileLock{f: f}, nil
}

// release drops the lock. The file stays: removing it would race with
// another instance that has just opened it.
func (l *fileLock) release() {
	if l == nil || l.f == nil {
		return
	}
	l.f.Truncate(0)
	syscall.Flock(int(l.f.Fd()), syscall.LOCK_UN)
	l.f.Close()
	l.f = nil
}
