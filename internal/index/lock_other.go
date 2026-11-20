//go:build !darwin && !linux

package index

// FileLock only creates the lock file on platforms other than macOS and
// Linux (not release targets): concurrent instances are not detected.
type FileLock struct{}

// AcquireLock creates the lock file at path (mode 0600, never through a
// symlink) without locking it.
func AcquireLock(path string) (*FileLock, error) {
	if err := openPrivate(path, true); err != nil {
		return nil, err
	}
	return &FileLock{}, nil
}

// Record does nothing on this platform.
func (*FileLock) Record(string) {}

// Release does nothing on this platform.
func (*FileLock) Release() {}
