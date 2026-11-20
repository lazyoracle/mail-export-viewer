//go:build darwin || linux

package main

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"syscall"
)

// cacheLock is an exclusive advisory lock (flock) on "<cache db>.run" (not the
// index's own "<db>.lock", which a second flock would collide with). It
// keeps a second instance (or a -reindex run) from writing the same cache
// while another instance uses it. The kernel releases it if the process
// dies, so a stale lock file never blocks a restart.
type cacheLock struct{ f *os.File }

// acquireCacheLock takes the lock for db without waiting. If another process
// holds it, the error says so, with the pid and URL that process recorded.
func acquireCacheLock(db string) (*cacheLock, error) {
	path := db + ".run"
	// O_NOFOLLOW: a symlink planted at the lock path is refused rather
	// than followed (which would truncate and chmod its target).
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		if errors.Is(err, syscall.ELOOP) {
			return nil, fmt.Errorf("cache lock %s is not a regular file (a symlink?); remove it", path)
		}
		return nil, fmt.Errorf("cannot open cache lock: %w", err)
	}
	if fi, err := f.Stat(); err != nil || !fi.Mode().IsRegular() {
		f.Close()
		return nil, fmt.Errorf("cache lock %s is not a regular file; remove it", path)
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
			return nil, fmt.Errorf("already running for this input%s; "+
				"stop that instance first, or use a different -cache-dir", owner)
		}
		return nil, fmt.Errorf("cannot lock the cache: %w", err)
	}
	f.Chmod(0o600)
	l := &cacheLock{f: f}
	l.record(fmt.Sprintf("pid %d", os.Getpid()))
	return l, nil
}

// record replaces the owner note shown to a refused second instance.
func (l *cacheLock) record(note string) {
	if l == nil || l.f == nil {
		return
	}
	if err := l.f.Truncate(0); err == nil {
		l.f.WriteAt([]byte(note+"\n"), 0)
	}
}

// release drops the lock. The lock file itself is left in place: removing
// it would race with another instance that has just opened it.
func (l *cacheLock) release() {
	if l == nil || l.f == nil {
		return
	}
	l.f.Truncate(0)
	syscall.Flock(int(l.f.Fd()), syscall.LOCK_UN)
	l.f.Close()
	l.f = nil
}
