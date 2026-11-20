package mbox

import (
	"path/filepath"
	"reflect"
	"testing"

	"github.com/lazyoracle/mail-export-viewer/internal/source"
)

func TestTagStyles(t *testing.T) {
	dir := t.TempDir()
	path := writeFile(t, filepath.Join(dir, "Inbox"), mboxOf("Subject: x\n\nbody\n"))
	setSidecar(t, path, &stubSidecar{tags: map[string]source.TagStyle{
		"$label1": {Name: "Important", Color: "#FF0000"},
		"work":    {Name: "Work", Color: ""},
		"$label2": {Name: "Work", Color: "#FF9900"}, // same name: the coloured one wins
		"later":   {Name: "Later"},
		"empty":   {Name: "", Color: "#000000"}, // nameless: dropped
	}})
	src := openMbox(t, path, "Inbox")
	ts, ok := src.(source.TagStyler)
	if !ok {
		t.Fatal("mbox source does not implement source.TagStyler")
	}
	want := []source.TagStyle{{Name: "Important", Color: "#FF0000"}, {Name: "Later"}, {Name: "Work", Color: "#FF9900"}}
	if got := ts.TagStyles(); !reflect.DeepEqual(got, want) {
		t.Errorf("TagStyles = %+v, want %+v", got, want)
	}
	// Without a sidecar there are no styles.
	plain := writeFile(t, filepath.Join(dir, "Other"), mboxOf("Subject: y\n\nbody\n"))
	if got := openMbox(t, plain, "Other").(source.TagStyler).TagStyles(); got != nil {
		t.Errorf("no sidecar: TagStyles = %+v", got)
	}
}
