package mbox

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/lazyoracle/mail-export-viewer/internal/source"
)

func TestGmailSystemLabelsMapToFlags(t *testing.T) {
	for _, tc := range []struct {
		labels string
		want   source.Flags
	}{
		{"Inbox,Spam", source.Junk},
		{"Trash", source.Deleted},
		{"Drafts", source.Draft},
		{"Draft,spam", source.Draft | source.Junk},
		{"Sent,Trips", 0},
	} {
		m := meta(t, "X-Gmail-Labels: "+tc.labels, nil)
		if m.Flags != tc.want {
			t.Errorf("%q: flags %q, want %q", tc.labels, m.Flags, tc.want)
		}
		if len(m.Labels) != len(strings.Split(tc.labels, ",")) {
			t.Errorf("%q: labels not kept: %q", tc.labels, m.Labels)
		}
	}
}

func TestAppleMailFolderName(t *testing.T) {
	for rel, want := range map[string]string{
		"Name.mbox/mbox":      "Name",
		"Mail/Work.mbox/mbox": "Mail/Work",
		"Work.MBOX/MBOX":      "Work",
		"mbox":                "mbox",
		"Name/mbox":           "Name/mbox",
		".mbox/mbox":          ".mbox/mbox",
	} {
		if got := folderName(rel, true); got != want {
			t.Errorf("folderName(%q) = %q, want %q", rel, got, want)
		}
	}
}

func TestShortReadIsStale(t *testing.T) {
	dir := t.TempDir()
	path := writeFile(t, filepath.Join(dir, "Inbox"), mboxOf(msgPlain, msgGm))
	src := openMbox(t, path, "Inbox")
	es, _ := scan(t, src)
	// Truncate so that the second message's range ends past EOF.
	if err := os.Truncate(path, es[1].Loc.Offset+es[1].Loc.Length-3); err != nil {
		t.Fatal(err)
	}
	if _, err := src.Raw(es[1].Loc); !errors.Is(err, source.ErrStale) {
		t.Errorf("Raw = %v, want ErrStale", err)
	}
}

func TestNonRegularFilesRefused(t *testing.T) {
	dir := t.TempDir()
	fifo := filepath.Join(dir, "a.eml")
	if err := syscall.Mkfifo(fifo, 0o644); err != nil {
		t.Skip("no fifo support")
	}
	src := openEML(t, dir, "X", fifo)
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, warns := scan(t, src)
		if len(warns) != 1 {
			t.Errorf("warnings %v", warns)
		}
		if _, err := src.Load(source.Locator{Name: "a.eml"}); err == nil {
			t.Error("Load of a fifo succeeded")
		}
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("blocked on a FIFO")
	}
}
