package mbox

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lazyoracle/mail-export-viewer/internal/source"
)

func TestGmailSystemLabelsMapToFlags(t *testing.T) {
	for _, tc := range []struct {
		labels string
		want   source.Flags
	}{
		{"Inbox,Spam", source.Junk},
		{"Trash", 0}, // an ordinary label: hiding it would hide the Trash label itself
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
		// Nested mailboxes are nested Name.mbox directories (issue #13).
		"Projects.mbox/Launch.mbox/mbox": "Projects/Launch",
		"Mail/A.mbox/B.mbox/C.mbox/mbox": "Mail/A/B/C",
		"Mail.sbd/A.mbox/mbox":           "Mail/A",
		"x.mbox/Notes/mbox":              "x.mbox/Notes/mbox",
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
