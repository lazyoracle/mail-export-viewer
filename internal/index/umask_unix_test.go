//go:build darwin || linux

package index

import "syscall"

func umask(m int) int { return syscall.Umask(m) }
