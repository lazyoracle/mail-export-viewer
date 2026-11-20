//go:build !darwin && !linux

package index

func umask(m int) int { return m }
