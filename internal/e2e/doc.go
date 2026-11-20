// Package e2e holds end-to-end tests: they build the real binary, generate
// synthetic corpora with internal/testutil/corpus, run the viewer on them
// over HTTP and compare every endpoint with the corpus ground truth. Run
// with `go test ./internal/e2e` (skipped with -short); MEV_BIN selects a
// prebuilt binary.
package e2e
