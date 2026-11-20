//go:build darwin || linux

package index

import (
	"errors"
	"syscall"
)

// oNoFollow makes opening a symlink fail instead of following it.
const oNoFollow = syscall.O_NOFOLLOW

func isSymlinkErr(err error) bool { return errors.Is(err, syscall.ELOOP) }
