package e2e

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lazyoracle/mail-export-viewer/internal/testutil/corpus"
)

// A single file given on the command line: folders are named after the
// file (PST, OLM), an mbox is its own folder, and a .msf opens its mbox.
func TestSingleFileInputs(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and runs the binary")
	}
	dir := t.TempDir()
	in := filepath.Join(dir, "in")
	if _, err := corpus.Generate(in, corpus.Options{Seed: 5, Sets: []string{corpus.PST, corpus.OLM, corpus.Thunderbird}, BigBytes: 1 << 20}); err != nil {
		t.Fatal(err)
	}
	inbox := filepath.Join(in, "thunderbird/Profiles/ab12cd34.default-release/Mail/Local Folders/Inbox")
	for _, tc := range []struct {
		path, folder string
		count        int64
		tag          string
	}{
		{filepath.Join(in, "pst/outlook.pst"), "outlook/Inbox", 22, ""},
		{filepath.Join(in, "olm/backup.olm"), "backup/Inbox", 12, ""},
		{inbox, "Inbox", 14, "Urgent"},
		{inbox + ".msf", "Inbox", 14, "Urgent"},
	} {
		t.Run(filepath.Base(tc.path), func(t *testing.T) {
			home := filepath.Join(t.TempDir(), "env")
			v := start(t, home, tc.path)
			defer v.stop()
			items, total := v.list("deleted", "include")
			if total != tc.count {
				t.Errorf("total %d, want %d", total, tc.count)
			}
			found := false
			for _, it := range items {
				if it.Folder == tc.folder {
					found = true
				}
				if strings.Contains(it.Folder, dir) {
					t.Errorf("folder %q holds a path", it.Folder)
				}
			}
			if !found {
				t.Errorf("no message in folder %q", tc.folder)
			}
			if tc.tag != "" {
				var f struct{ Tags []termCount }
				v.getJSON("/api/facets", &f)
				ok := false
				for _, tg := range f.Tags {
					ok = ok || tg.Name == tc.tag
				}
				if !ok {
					t.Errorf("tag %q missing: %+v", tc.tag, f.Tags)
				}
			}
		})
	}
}

const extraMsg = "From x@example.com Mon Jan  2 15:04:05 2023\nFrom: New Sender <new@example.com>\nSubject: Appended later\n" +
	"Date: Mon, 2 Jan 2023 15:04:05 +0000\nMessage-ID: <appended@example.com>\n\nqzappendedword\n\n"

// Changing an input: a restart re-indexes the changed file only; a file
// rewritten under a running viewer gives 410 stale, never another
// message's content.
func TestChangedInput(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and runs the binary")
	}
	dir := t.TempDir()
	in := filepath.Join(dir, "in")
	if _, err := corpus.Generate(in, corpus.Options{Seed: 9, Sets: []string{corpus.Gmail, corpus.Apple}, BigBytes: 1 << 20}); err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(dir, "env")
	mbox := filepath.Join(in, "apple/Mailboxes/Work.mbox/mbox")
	v := start(t, home, in)
	_, before := v.list()
	var l apiList
	v.getJSON("/api/messages?"+q("folder", "apple/Mailboxes/Work", "sort", "date_asc"), &l)
	first := l.Items[0]
	v.stop()

	// Append a message while stopped.
	f, err := os.OpenFile(mbox, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString(extraMsg)
	f.Close()
	v = start(t, home, in)
	if _, after := v.list(); after != before+1 {
		t.Errorf("after appending: %d messages, want %d", after, before+1)
	}
	if !strings.Contains(v.stderr.String(), "indexed 5 messages") {
		t.Errorf("only the changed file should be re-indexed:\n%s", v.stderr.String())
	}
	var found apiList
	v.getJSON("/api/messages?q=qzappendedword", &found)
	if found.Total != 1 {
		t.Errorf("appended message not found")
	}
	// Rewrite the file under the running viewer so that the first message
	// grows (its range no longer ends at a separator).
	v.getJSON("/api/messages?"+q("folder", "apple/Mailboxes/Work", "sort", "date_asc"), &l)
	first = l.Items[0]
	data, err := os.ReadFile(mbox)
	if err != nil {
		t.Fatal(err)
	}
	rewritten := strings.Replace(string(data), "\n\nFrom ", "\nmore text\n\nFrom ", 1)
	if err := os.WriteFile(mbox, []byte(rewritten), 0o644); err != nil {
		t.Fatal(err)
	}
	future := time.Now().Add(time.Hour)
	os.Chtimes(mbox, future, future)
	resp, body := v.get(fmt.Sprintf("/api/messages/%d", first.ID))
	if resp.StatusCode != 410 || !strings.Contains(string(body), `"stale"`) {
		t.Errorf("detail of a rewritten message: %d %s", resp.StatusCode, body)
	}
	// Remove the file.
	os.Remove(mbox)
	resp, body = v.get(fmt.Sprintf("/api/messages/%d/raw", first.ID))
	if resp.StatusCode != 410 {
		t.Errorf("raw of a removed file: %d %s", resp.StatusCode, body)
	}
	v.stop()
	v = start(t, home, in)
	if _, after := v.list(); after != before+1-5 {
		t.Errorf("after removing a 5-message file: %d messages, want %d", after, before-4)
	}
}
