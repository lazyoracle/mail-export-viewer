// Command mail-export-viewer browses mail exports in a local web UI.
package main

import "os"

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}
