//go:build !darwin && !linux

package main

// cacheLock is a no-op on platforms other than macOS and Linux (the only
// release targets): concurrent instances on the same input are not detected
// there, so run one at a time.
type cacheLock struct{}

func acquireCacheLock(string) (*cacheLock, error) { return &cacheLock{}, nil }

func (*cacheLock) record(string) {}

func (*cacheLock) release() {}
