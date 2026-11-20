//go:build !darwin && !linux

package index

// fileLock only creates the lock file on platforms other than macOS and
// Linux (not release targets): concurrent instances are not detected.
type fileLock struct{}

func acquireLock(path string) (*fileLock, error) {
	if err := openPrivate(path, true); err != nil {
		return nil, err
	}
	return &fileLock{}, nil
}

func (*fileLock) release() {}
