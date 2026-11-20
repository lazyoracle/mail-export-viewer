package index

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lazyoracle/mail-export-viewer/internal/source"
)

// deletedStore indexes one deleted and one normal message that both match
// "alpha".
func deletedStore(t *testing.T, opts Options) *Store {
	t.Helper()
	s := openAt(t, filepath.Join(t.TempDir(), "i.db"), opts)
	del := rawEntry("A", 1, "alpha gone")
	del.Flags = source.Deleted
	a := newFake(t, source.Mbox, "a", del, rawEntry("A", 2, "alpha kept"))
	index(t, s, a)
	return s
}

// The per-request mode must hold whatever the query text is: a query that
// has an open quote, is at the 4 KiB cap or has 64 items cannot smuggle
// normal messages into "only" (a text " is:deleted" suffix would be cut
// off or swallowed by the quote).
func TestSearchDeletedModes(t *testing.T) {
	s := deletedStore(t, Options{})
	long := strings.Repeat("alpha ", 682) // 4092 bytes
	many := strings.TrimSpace(strings.Repeat("alpha ", 64))
	queries := []string{"", "alpha", `"alpha`, `subject:"alpha`, `subject:"x`, long, many, "is:deleted", "-is:deleted", `label:"`}
	for _, q := range queries {
		name := q
		if len(name) > 20 {
			name = fmt.Sprintf("%.20s…(%d)", q, len(q))
		}
		for mode, check := range map[DeletedMode]func([]Row) string{
			DeletedOnly: func(rs []Row) string {
				for _, r := range rs {
					if !r.Flags.Has(source.Deleted) {
						return "normal message in only: " + r.Subject
					}
				}
				return ""
			},
			DeletedHide: func(rs []Row) string {
				for _, r := range rs {
					if r.Flags.Has(source.Deleted) && !strings.Contains(q, "is:deleted") {
						return "deleted message in hide: " + r.Subject
					}
				}
				return ""
			},
		} {
			res, err := s.Search(SearchParams{Q: q, Deleted: mode})
			if err != nil {
				t.Fatalf("%q mode %d: %v", name, mode, err)
			}
			if msg := check(res.Items); msg != "" {
				t.Errorf("%q mode %d: %s", name, mode, msg)
			}
			if res.Total != int64(len(res.Items)) {
				t.Errorf("%q mode %d: total %d, items %d", name, mode, res.Total, len(res.Items))
			}
		}
	}
	for _, q := range []string{"alpha", `subject:"alpha`, long, many} {
		only, _ := s.Search(SearchParams{Q: q, Deleted: DeletedOnly})
		inc, _ := s.Search(SearchParams{Q: q, Deleted: DeletedInclude})
		hide, _ := s.Search(SearchParams{Q: q})
		if len(only.Items) != 1 || len(inc.Items) != 2 || len(hide.Items) != 1 || inc.Total != 2 {
			t.Errorf("%.20q: only %d include %d (total %d) hide %d", q, len(only.Items), len(inc.Items), inc.Total, len(hide.Items))
		}
	}
	// Unfiltered listings count from the folder counters.
	for mode, want := range map[DeletedMode]int64{DeletedDefault: 1, DeletedHide: 1, DeletedInclude: 2, DeletedOnly: 1} {
		res, err := s.Search(SearchParams{Deleted: mode})
		if err != nil || res.Total != want || int64(len(res.Items)) != want {
			t.Errorf("list mode %d: total %d items %d err %v, want %d", mode, res.Total, len(res.Items), err, want)
		}
	}
}

func TestDeletedDefaultFollowsOptions(t *testing.T) {
	s := deletedStore(t, Options{IncludeDeleted: true})
	for mode, want := range map[DeletedMode]int{DeletedDefault: 2, DeletedHide: 1, DeletedInclude: 2, DeletedOnly: 1} {
		res, err := s.Search(SearchParams{Q: "alpha", Deleted: mode})
		if err != nil || len(res.Items) != want {
			t.Errorf("mode %d: %d items (%v), want %d", mode, len(res.Items), err, want)
		}
	}
}

func TestDeletedModeThreadsAndFacets(t *testing.T) {
	s := openStore(t)
	a := addMsg(t, s, 1, "a", "", "", "q", t0)
	b := addMsgFlags(t, s, 1, "b", "a", "a", "q", t0+day, source.Deleted)
	if _, err := s.wdb.Exec(`UPDATE folders SET deleted = 1`); err != nil {
		t.Fatal(err)
	}
	assignAll(t, s)
	for mode, want := range map[DeletedMode]int{DeletedHide: 1, DeletedInclude: 2} {
		th, total, err := s.ThreadPage(ThreadParams{ID: a, Deleted: mode})
		if err != nil || len(th) != want || total != want {
			t.Errorf("mode %d: thread %d of %d %v", mode, len(th), total, err)
		}
		res, _ := s.Search(SearchParams{Deleted: mode})
		if res.ThreadCounts[a] != want {
			t.Errorf("mode %d: thread count %d", mode, res.ThreadCounts[a])
		}
	}
	res, _ := s.Search(SearchParams{Deleted: DeletedOnly})
	if len(res.Items) != 1 || res.Items[0].ID != b || res.ThreadCounts[b] != 2 {
		t.Errorf("only: %+v counts %v", res.Items, res.ThreadCounts)
	}
	for _, inc := range []bool{false, true} {
		f, err := s.FacetsFor(inc)
		if err != nil {
			t.Fatal(err)
		}
		want := int64(1)
		if inc {
			want = 2
		}
		if f.Folders[0].Count != want {
			t.Errorf("FacetsFor(%v): folder count %d", inc, f.Folders[0].Count)
		}
	}
}

// A huge thread is capped, and the requested message is always in the
// page, in its place (last when it falls beyond the cap).
func TestThreadPageCap(t *testing.T) {
	s := openStore(t)
	const n = 6000
	ids := make([]int64, n)
	tx, err := s.wdb.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`INSERT INTO sources(id, path, kind) VALUES(1, '/p1', 'mbox')`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`INSERT INTO folders(id, source_id, path, total) VALUES(1, 1, 'f', ?)`, n); err != nil {
		t.Fatal(err)
	}
	for i := range n {
		irt := ""
		if i > 0 {
			irt = fmt.Sprint("m", i-1)
		}
		r, err := tx.Exec(`INSERT INTO messages(source_id, folder_id, loc, message_id, in_reply_to, refs, subject, date_unix, flags)
			VALUES(1,1,'0:1:0:',?,?,?,'chain',?,0)`, fmt.Sprint("m", i), irt, irt, t0+int64(i))
		if err != nil {
			t.Fatal(err)
		}
		ids[i], _ = r.LastInsertId()
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	assignAll(t, s)
	all, total, err := s.ThreadPage(ThreadParams{ID: ids[0]})
	if err != nil || len(all) != n || total != n {
		t.Fatalf("uncapped: %d of %d %v", len(all), total, err)
	}
	page, total, err := s.ThreadPage(ThreadParams{ID: ids[n-1], Limit: 5000})
	if err != nil || len(page) != 5000 || total != n || page[4999].ID != ids[n-1] || page[0].ID != ids[0] {
		t.Fatalf("capped (last): %d of %d %v", len(page), total, err)
	}
	page, total, _ = s.ThreadPage(ThreadParams{ID: ids[10], Limit: 5000})
	if len(page) != 5000 || total != n || page[10].ID != ids[10] {
		t.Fatalf("capped (early): %d of %d", len(page), total)
	}
	page, total, _ = s.ThreadPage(ThreadParams{ID: ids[0], Limit: n})
	if len(page) != n || total != n {
		t.Fatalf("limit == size: %d of %d", len(page), total)
	}
	if th, err := s.Thread(ids[0]); err != nil || len(th) != n {
		t.Fatalf("Thread: %d %v", len(th), err)
	}
}
