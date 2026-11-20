package index

import (
	"context"
	"crypto/sha256"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/lazyoracle/mail-export-viewer/internal/source"
	_ "github.com/lazyoracle/mail-export-viewer/internal/source/mbox"
	_ "github.com/lazyoracle/mail-export-viewer/internal/source/olm"
	"github.com/lazyoracle/mail-export-viewer/internal/testutil"
)

// mixedDir writes a directory with a Gmail Takeout mbox, Thunderbird local
// folders (with .sbd nesting), loose .eml files and an OLM archive.
func mixedDir(t testing.TB) string {
	dir := t.TempDir()
	testutil.WriteMbox(t, filepath.Join(dir, "Takeout", "All mail Including Spam and Trash.mbox"),
		"From: Alice <alice@example.com>\nTo: Bob <bob@example.org>\nSubject: Gmail hello\n"+
			"Date: Mon, 6 Mar 2023 10:00:00 +0000\nMessage-ID: <g1@example.com>\nX-Gmail-Labels: Inbox,Unread,Travel\n\nPacking list for the trip.\n",
		"From: Bob <bob@example.org>\nTo: Alice <alice@example.com>\nSubject: Re: Gmail hello\n"+
			"Date: Mon, 6 Mar 2023 11:00:00 +0000\nMessage-ID: <g2@example.org>\nIn-Reply-To: <g1@example.com>\n"+
			"X-Gmail-Labels: Sent,Trash\n\nSounds good.\n")
	testutil.WriteMbox(t, filepath.Join(dir, "Local Folders", "Inbox"),
		"From: Carol <carol@example.net>\nTo: Dan <dan@example.net>\nSubject: Thunderbird tagged\n"+
			"Date: Tue, 7 Mar 2023 09:00:00 +0000\nMessage-ID: <t1@example.net>\nX-Mozilla-Status: 0005\n"+
			"X-Mozilla-Keys: $label2 project\n\nTagged body text.\n")
	testutil.WriteMbox(t, filepath.Join(dir, "Local Folders", "Inbox.sbd", "Archive"),
		"From: Carol <carol@example.net>\nTo: Dan <dan@example.net>\nSubject: Archived note\n"+
			"Date: Wed, 1 Feb 2023 09:00:00 +0000\nMessage-ID: <t2@example.net>\nX-Mozilla-Status: 0001\n\nOld note.\n")
	emlDir := filepath.Join(dir, "loose")
	os.MkdirAll(emlDir, 0o755)
	os.WriteFile(filepath.Join(emlDir, "a.eml"), []byte("From: Erin <erin@example.com>\nTo: Dan <dan@example.net>\n"+
		"Subject: Loose eml\nDate: Thu, 2 Mar 2023 08:00:00 +0000\nMessage-ID: <e1@example.com>\n\nA loose message.\n"), 0o644)
	m := testutil.OLMMsg{Subject: "Outlook budget", MessageID: "<o1@example.org>",
		From: testutil.OLMAddr{Name: "Frank", Email: "frank@example.org"},
		To:   []testutil.OLMAddr{{Name: "Dan", Email: "dan@example.net"}}, Body: "Budget numbers inside",
		Sent: "2023-03-08T10:00:00", Unread: true, Flagged: true, Categories: []string{"Finance"},
		Attachments: []testutil.OLMAtt{{Name: "budget.csv", Type: "text/csv", Data: []byte("a,b\n")}}}
	testutil.WriteOLM(t, filepath.Join(dir, "backup.olm"), testutil.OLMMessageEntries(
		"Accounts/me@example.com/com.microsoft.__Messages/Inbox/msg1.olk15Message", m,
		"Accounts/me@example.com/com.microsoft.__Attachments")...)
	return dir
}

func openAll(t testing.TB, root string) map[string]source.Source {
	t.Helper()
	d, err := source.Discover(root, source.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]source.Source{}
	for _, c := range d.Candidates {
		src, err := c.Open()
		if err != nil {
			t.Fatalf("open %s: %v", c.Rel, err)
		}
		t.Cleanup(func() { src.Close() })
		out[src.Path()] = src
	}
	return out
}

func list(m map[string]source.Source) []source.Source {
	var out []source.Source
	for _, s := range m {
		out = append(out, s)
	}
	return out
}

func hashTree(t testing.TB, root string) map[string][32]byte {
	out := map[string][32]byte{}
	filepath.Walk(root, func(p string, fi os.FileInfo, err error) error {
		if err == nil && fi.Mode().IsRegular() {
			b, _ := os.ReadFile(p)
			out[p] = sha256.Sum256(b)
		}
		return nil
	})
	return out
}

func TestIntegrationMixedDirectory(t *testing.T) {
	dir := mixedDir(t)
	before := hashTree(t, dir)
	srcs := openAll(t, dir)
	if len(srcs) != 5 {
		t.Fatalf("sources = %d", len(srcs))
	}
	s := openStore(t)
	p := index(t, s, list(srcs)...)
	if p.Messages != 6 {
		t.Errorf("messages = %d", p.Messages)
	}
	for _, sp := range p.Sources {
		want := UnitMessages
		if sp.Kind == source.Mbox {
			want = UnitBytes
		}
		if sp.Unit != want || sp.State != StateDone || sp.Done == 0 {
			t.Errorf("progress %+v", sp)
		}
	}

	// Gmail Trash is an ordinary label (not deleted): all 6 visible.
	all := search(t, s, SearchParams{Limit: 100})
	if all.Total != 6 {
		t.Errorf("total = %d", all.Total)
	}
	f := facets(t, s)
	labels := map[string]int64{}
	for _, l := range f.Labels {
		labels[l.Name] = l.Count
	}
	if labels["Travel"] != 1 || labels["Inbox"] != 1 || labels["Trash"] != 1 {
		t.Errorf("labels = %+v", f.Labels)
	}
	if !reflect.DeepEqual(f.Categories, []TermCount{{Name: "Finance", Count: 1, Unread: 1}}) {
		t.Errorf("categories = %+v", f.Categories)
	}
	var tags []string
	for _, tg := range f.Tags {
		tags = append(tags, tg.Name)
	}
	if !reflect.DeepEqual(tags, []string{"project", "Work"}) {
		t.Errorf("tags = %+v", f.Tags)
	}
	var top []string
	for _, n := range f.Folders {
		top = append(top, n.Path)
	}
	if !reflect.DeepEqual(top, []string{"backup", "Local Folders", "loose", "Takeout"}) {
		t.Errorf("top folders = %v", top)
	}

	for q, want := range map[string]int{
		"tag:work":                     1,
		"category:finance":             1,
		"label:travel is:unread":       1,
		"is:starred":                   2, // Thunderbird 0x4, Outlook flagged
		"has:attachment":               1,
		"folder:archive":               1, // Local Folders/Inbox/Archive
		"is:deleted":                   0,
		"label:trash":                  1, // shown by default
		"budget":                       1,
		"dan":                          4,
		`folder:"Local Folders/Inbox"`: 1,
	} {
		if r := search(t, s, SearchParams{Q: q}); r.Total != int64(want) {
			t.Errorf("%s: total %d, want %d", q, r.Total, want)
		}
	}

	// Every stored locator loads its message from its source.
	for _, r := range all.Items {
		path, loc, err := s.Location(r.ID)
		if err != nil {
			t.Fatal(err)
		}
		msg, err := srcs[path].Load(loc)
		if err != nil {
			t.Fatalf("Load(%s, %v): %v", path, loc, err)
		}
		if msg.Subject != r.Subject {
			t.Errorf("loaded %q for row %q", msg.Subject, r.Subject)
		}
	}
	// Threading joins the Gmail reply (in Trash, which is just a label).
	g := search(t, s, SearchParams{Q: `subject:"gmail hello"`, Sort: "date_asc"}).Items
	if len(g) != 2 {
		t.Fatalf("gmail = %+v", g)
	}
	if th, _ := s.Thread(g[0].ID); len(th) != 2 {
		t.Errorf("thread lacks the reply in Trash: %+v", th)
	}

	// Unchanged inputs are reused; a changed one is re-scanned alone.
	p = index(t, s, list(srcs)...)
	for _, sp := range p.Sources {
		if sp.State != StateCached {
			t.Errorf("%s: %s on unchanged rerun", sp.Path, sp.State)
		}
	}
	inbox := filepath.Join(dir, "Local Folders", "Inbox")
	future := time.Now().Add(time.Hour)
	os.Chtimes(inbox, future, future)
	before[inbox] = hashTree(t, inbox)[inbox]
	p = index(t, s, list(srcs)...)
	for _, sp := range p.Sources {
		want := StateCached
		if sp.Path == inbox {
			want = StateDone
		}
		if sp.State != want {
			t.Errorf("%s: %s after touching the Inbox", sp.Path, sp.State)
		}
	}
	if after := hashTree(t, dir); !reflect.DeepEqual(after, before) {
		t.Error("input directory changed (files written or modified)")
	}
}

func TestIntegrationCancelThenResume(t *testing.T) {
	dir := t.TempDir()
	var msgs []string
	for i := range 3000 {
		msgs = append(msgs, string(rawMsg(i, "bulk")))
	}
	testutil.WriteMbox(t, filepath.Join(dir, "big"), msgs...)
	srcs := openAll(t, dir)
	s := openStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	err := s.IndexSources(ctx, list(srcs), func(p Progress) {
		if p.Messages > 0 || (len(p.Sources) > 0 && p.Sources[0].Messages > 0) {
			cancel()
		}
	})
	if err == nil {
		t.Fatal("not cancelled")
	}
	index(t, s, list(srcs)...)
	if r := search(t, s, SearchParams{}); r.Total != 3000 {
		t.Errorf("total = %d", r.Total)
	}
	if !strings.HasSuffix(sourceByPath(t, s, filepath.Join(dir, "big")).Path, "big") {
		t.Error("source path")
	}
}
