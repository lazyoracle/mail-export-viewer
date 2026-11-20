// Command mail-export-viewer browses mail exports in a local web UI.
package main

import (
	"flag"
	"fmt"
	"os"
)

// version is injected at build time with -ldflags "-X main.version=...".
var version = "dev"

func main() {
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()
	if *showVersion {
		fmt.Println("mail-export-viewer", version)
		return
	}
	fmt.Fprintln(os.Stderr, "mail-export-viewer: not implemented yet; use --version")
	os.Exit(2)
}
