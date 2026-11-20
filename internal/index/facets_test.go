package index

import (
	"reflect"
	"testing"

	"github.com/lazyoracle/mail-export-viewer/internal/source"
)

func facets(t testing.TB, s *Store) Facets {
	t.Helper()
	f, err := s.Facets()
	if err != nil {
		t.Fatal(err)
	}
	return f
}

// stripRefs drops per-source refs so trees compare by shape and counts.
func stripRefs(ns []FolderNode) []FolderNode {
	var out []FolderNode
	for _, n := range ns {
		n.Sources = nil
		n.Children = stripRefs(n.Children)
		out = append(out, n)
	}
	return out
}

func TestFacetsFixture(t *testing.T) {
	s, _ := searchFixture(t)
	f := facets(t, s)
	wantTree := []FolderNode{
		{Name: "backup", Path: "backup", Children: []FolderNode{
			{Name: "Empty", Path: "backup/Empty"},
			{Name: "Inbox", Path: "backup/Inbox", Count: 1},
		}},
		{Name: "Other", Path: "Other", Count: 3, Unread: 1},
		{Name: "Work", Path: "Work", Count: 2, Unread: 1},
	}
	if got := stripRefs(f.Folders); !reflect.DeepEqual(got, wantTree) {
		t.Errorf("folders\n%+v\nwant\n%+v", got, wantTree)
	}
	work := f.Folders[2]
	if len(work.Sources) != 1 || work.Sources[0].SourceKind != source.Mbox || work.Sources[0].Count != 2 ||
		work.Sources[0].ID == 0 || work.Sources[0].SourceID == 0 || work.Sources[0].SourcePath == "" {
		t.Errorf("Work refs = %+v", work.Sources)
	}
	if n := len(f.Folders[0].Sources); n != 0 {
		t.Errorf("intermediate node has %d refs", n)
	}
	if got, want := f.Labels, []TermCount{{Name: "Inbox", Count: 2, Unread: 1}, {Name: "Promotions", Count: 1, Unread: 1},
		{Name: "Saved Items", Count: 1, Unread: 1}, {Name: "Work", Count: 1}}; !reflect.DeepEqual(got, want) {
		t.Errorf("labels = %+v", got)
	}
	if got, want := f.Categories, []TermCount{{Name: "Red category", Count: 1}}; !reflect.DeepEqual(got, want) {
		t.Errorf("categories = %+v", got)
	}
	if got, want := f.Tags, []TermCount{{Name: "Later", Count: 1, Unread: 1}, {Name: "To Do", Color: "#3333FF", Count: 1}}; !reflect.DeepEqual(got, want) {
		t.Errorf("tags = %+v", got)
	}
	wantFlags := map[string]int64{"unread": 2, "starred": 1, "answered": 1, "forwarded": 1, "draft": 1,
		"deleted": 1, "junk": 1, "attachment": 1, "important": 0}
	if len(f.Flags) != len(wantFlags) {
		t.Errorf("flags = %+v", f.Flags)
	}
	for _, fc := range f.Flags {
		if fc.Count != wantFlags[fc.Name] || fc.Flag.Names()[0] != fc.Name {
			t.Errorf("flag %+v, want count %d", fc, wantFlags[fc.Name])
		}
	}
}

func TestFacetsIncludeDeleted(t *testing.T) {
	s, _ := searchFixture(t, Options{IncludeDeleted: true})
	f := facets(t, s)
	if other := f.Folders[1]; other.Name != "Other" || other.Count != 4 {
		t.Errorf("Other = %+v", other)
	}
}

func TestFacetsMergeSourcesByPath(t *testing.T) {
	s := openStore(t)
	a := newFake(t, source.Mbox, "a", rawEntry("Inbox", 1, "one"))
	b := newFake(t, source.Mbox, "b", rawEntry("Inbox", 2, "two"), rawEntry("Inbox/Sub", 3, "three"))
	index(t, s, a, b)
	f := facets(t, s)
	if len(f.Folders) != 1 {
		t.Fatalf("folders = %+v", f.Folders)
	}
	in := f.Folders[0]
	if in.Path != "Inbox" || in.Count != 2 || len(in.Sources) != 2 || len(in.Children) != 1 || in.Children[0].Count != 1 {
		t.Errorf("Inbox = %+v", in)
	}
	if in.Sources[0].SourcePath == in.Sources[1].SourcePath || in.Sources[0].Count != 1 {
		t.Errorf("refs = %+v", in.Sources)
	}
}

func TestFacetsFollowReindexAndRemoval(t *testing.T) {
	s := openStore(t)
	e := rawEntry("A", 1, "one")
	e.Labels = []string{"Old"}
	a := newFake(t, source.Mbox, "a", e)
	index(t, s, a)
	if l := facets(t, s).Labels; len(l) != 1 || l[0].Name != "Old" {
		t.Fatalf("labels = %+v", l)
	}
	a.entries[0].Labels = []string{"New"}
	touch(t, a.path)
	index(t, s, a)
	if l := facets(t, s).Labels; len(l) != 1 || l[0].Name != "New" {
		t.Fatalf("after reindex labels = %+v", l)
	}
	index(t, s)
	f := facets(t, s)
	if len(f.Labels) != 0 || len(f.Folders) != 0 {
		t.Fatalf("after removal facets = %+v", f)
	}
}

func TestFacetsHealAfterInterruptedRun(t *testing.T) {
	s := openStore(t)
	e := rawEntry("A", 1, "one")
	e.Labels = []string{"L"}
	a := newFake(t, source.Mbox, "a", e)
	index(t, s, a)
	// Simulate a crash between writing messages and recounting.
	if _, err := s.wdb.Exec(`UPDATE terms SET count = 0, count_all = 0`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.wdb.Exec(setFacetsDirtySQL); err != nil {
		t.Fatal(err)
	}
	index(t, s, a) // nothing changed on disk
	if l := facets(t, s).Labels; len(l) != 1 || l[0].Count != 1 {
		t.Fatalf("labels = %+v", l)
	}
}

func TestFacetsEmpty(t *testing.T) {
	s := openStore(t)
	f := facets(t, s)
	if f.Folders != nil || f.Labels == nil || f.Tags == nil || f.Categories == nil || len(f.Flags) != 9 {
		t.Errorf("empty facets = %+v", f)
	}
}
