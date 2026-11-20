package web

import (
	"embed"
	"io/fs"
)

//go:embed static
var static embed.FS

// FS returns the UI files rooted at static/ (index.html at the top).
func FS() fs.FS {
	sub, err := fs.Sub(static, "static")
	if err != nil {
		panic(err) // static is a compile-time constant directory
	}
	return sub
}
