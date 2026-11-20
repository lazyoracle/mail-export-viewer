package e2e

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/lazyoracle/mail-export-viewer/internal/testutil/corpus"
)

// runCorpus generates the given sets, runs the built binary on them with a
// private HOME/XDG_CACHE_HOME/TMPDIR and checks everything against the
// manifest; then checks that the input is byte-for-byte unchanged and that
// nothing was written outside the cache directory.
func runCorpus(t *testing.T, sets []string, full bool) {
	dir := t.TempDir()
	in := filepath.Join(dir, "in")
	m, err := corpus.Generate(in, corpus.Options{Seed: 7, Sets: sets, BigBytes: 3 << 20})
	if err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(dir, "env")
	makeEnv(t, home)
	before := treeState(t, in)
	envBefore := treeState(t, home)
	v := start(t, home, in)
	c := &checker{t: t, v: v, m: m, full: full}
	c.run()

	// A second start reuses the cache and shows the same data.
	v.stop()
	v2 := start(t, home, in)
	c2 := &checker{t: t, v: v2, m: m}
	c2.checkStatus()
	c2.checkList()
	v2.stop()

	if d := diffTrees(before, treeState(t, in)); len(d) > 0 {
		t.Errorf("input changed:\n  %s", strings.Join(d, "\n  "))
	}
	var outside []string
	for _, e := range diffTrees(envBefore, treeState(t, home)) {
		rel := e[strings.Index(e, " ")+1:]
		if strings.HasPrefix(rel, filepath.Join("cache", "mail-export-viewer")+string(filepath.Separator)) ||
			rel == filepath.Join("cache", "mail-export-viewer") || strings.HasPrefix(e, "changed cache ") {
			continue
		}
		outside = append(outside, e)
	}
	if len(outside) > 0 {
		t.Errorf("written outside the cache directory:\n  %s", strings.Join(outside, "\n  "))
	}
	if t.Failed() {
		t.Logf("viewer stderr:\n%s", trunc(v.stderr.String(), 4000))
	}
}

func TestFormats(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and runs the binary")
	}
	for _, set := range corpus.AllSets {
		t.Run(set, func(t *testing.T) {
			t.Parallel()
			runCorpus(t, []string{set}, true)
		})
	}
}

func TestMixedDirectory(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and runs the binary")
	}
	runCorpus(t, nil, true)
}
