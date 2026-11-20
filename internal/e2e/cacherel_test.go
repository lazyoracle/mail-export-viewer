package e2e

import (
	"path/filepath"
	"testing"
)

func TestIsCacheEntry(t *testing.T) {
	own := filepath.Join(cacheRel(), "mail-export-viewer")
	for _, e := range []string{
		"added " + own,
		"added " + filepath.Join(own, "index.db"),
		"changed " + cacheRel() + " ({Mode:drwx------ Size:0})",
		"changed " + filepath.Dir(own) + " ({Mode:drwx------ Size:0})",
	} {
		if !isCacheEntry(e) {
			t.Errorf("%q should count as the viewer's cache", e)
		}
	}
	for _, e := range []string{
		"added " + filepath.Join(filepath.Dir(own), "other"),
		"added " + filepath.Join("home", ".config", "x"),
		"added " + own + "-evil",
		"added tmp/leak.db",
	} {
		if isCacheEntry(e) {
			t.Errorf("%q must not count as the viewer's cache", e)
		}
	}
}
