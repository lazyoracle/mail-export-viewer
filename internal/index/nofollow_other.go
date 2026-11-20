//go:build !darwin && !linux

package index

// oNoFollow is not used on platforms other than macOS and Linux; the
// regular-file check after opening still applies.
const oNoFollow = 0

func isSymlinkErr(error) bool { return false }
