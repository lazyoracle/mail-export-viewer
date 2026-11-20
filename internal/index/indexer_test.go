package index

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lazyoracle/mail-export-viewer/internal/source"
)

func sourceByPath(t testing.TB, s *Store, path string) SourceInfo {
	t.Helper()
	srcs, err := s.Sources()
	if err != nil {
		t.Fatal(err)
	}
	for _, si := range srcs {
		if si.Path == path {
			return si
		}
	}
	t.Fatalf("source %s not indexed (have %+v)", path, srcs)
	return SourceInfo{}
}

func TestIndexRowFields(t *testing.T) {
	s := openStore(t)
	e := rawEntry("Archive/2023", 7, "Quarterly report")
	e.Labels = []string{"Inbox", "Work"}
	e.Tags = []string{"Important"}
	e.Flags = source.Unread | source.Starred
	e.Props = []source.Prop{{Key: "X-Custom", Value: "one"}, {Key: "X-Other", Value: "two"}}
	f := newFake(t, source.Mbox, "box", e)
	index(t, s, f)

	rows := allRows(t, s)
	if len(rows) != 1 {
		t.Fatalf("rows = %d", len(rows))
	}
	r := rows[0]
	want := Row{
		ID: r.ID, SourceID: r.SourceID, FolderID: r.FolderID, ThreadID: r.ThreadID,
		SourceKind: source.Mbox, Folder: "Archive/2023",
		Subject: "Quarterly report", FromName: "Sender 7", FromEmail: "sender7@example.com",
		To:      "Rcpt <rcpt@example.com>",
		Snippet: "Body of message 7 about Quarterly report.",
		Date:    time.Date(2023, 1, 2, 15, 4, 7, 0, time.UTC),
		Flags:   source.Unread | source.Starred,
		Labels:  []string{"Inbox", "Work"}, Tags: []Tag{{Name: "Important"}},
	}
	if !reflect.DeepEqual(r, want) {
		t.Fatalf("row\n%+v\nwant\n%+v", r, want)
	}
	if r.ID == 0 || r.SourceID == 0 || r.FolderID == 0 || r.ThreadID == 0 {
		t.Errorf("ids not set: %+v", r)
	}
	m, err := s.Message(r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if m.SourcePath != f.path || m.Loc != e.Loc || m.MessageID != "m7@example.com" {
		t.Errorf("message = %+v", m)
	}
	if !reflect.DeepEqual(m.Props, e.Props) {
		t.Errorf("props = %v", m.Props)
	}
	if !reflect.DeepEqual(m.Row, r) {
		t.Errorf("Message.Row = %+v", m.Row)
	}
	if _, err := s.Message(r.ID + 100); !errors.Is(err, ErrNotFound) {
		t.Errorf("Message(unknown) = %v", err)
	}
	path, loc, err := s.Location(r.ID)
	if err != nil || path != f.path || loc != e.Loc {
		t.Errorf("Location = %q, %+v, %v", path, loc, err)
	}
}

func TestIndexCategoriesForOutlookKinds(t *testing.T) {
	s := openStore(t)
	e := msgEntry("backup/Inbox", 3, "Budget")
	e.Labels = []string{"Red category"}
	e.Msg.HasAttachment = true
	e.Msg.AttachmentNames = []string{"budget.xlsx"}
	index(t, s, newFake(t, source.OLM, "a.olm", e))
	r := allRows(t, s)[0]
	if r.Labels != nil || !reflect.DeepEqual(r.Categories, []string{"Red category"}) {
		t.Errorf("labels=%v categories=%v", r.Labels, r.Categories)
	}
	if !r.HasAttachment || !r.Flags.Has(source.HasAttachment) || r.SourceKind != source.OLM {
		t.Errorf("row = %+v", r)
	}
	m, _ := s.Message(r.ID)
	if !reflect.DeepEqual(m.AttachmentNames, []string{"budget.xlsx"}) || m.Loc.ID != 3 {
		t.Errorf("message = %+v", m)
	}
}

func TestReindexUnchangedIsCached(t *testing.T) {
	s := openStore(t)
	a := newFake(t, source.Mbox, "a", rawEntry("A", 1, "one"), rawEntry("A", 2, "two"))
	b := newFake(t, source.OLM, "b.olm", msgEntry("b/Inbox", 1, "three"))
	index(t, s, a, b)
	before := allRows(t, s)
	p := index(t, s, a, b)
	if a.scans.Load() != 1 || b.scans.Load() != 1 {
		t.Errorf("scans = %d, %d; want unchanged sources reused", a.scans.Load(), b.scans.Load())
	}
	for _, sp := range p.Sources {
		if sp.State != StateCached {
			t.Errorf("%s state = %s", sp.Path, sp.State)
		}
	}
	if after := allRows(t, s); !reflect.DeepEqual(after, before) {
		t.Errorf("rows changed on no-op reindex")
	}
}

func TestReopenKeepsIndex(t *testing.T) {
	db := filepath.Join(t.TempDir(), "index.db")
	s, err := Open(db, Options{})
	if err != nil {
		t.Fatal(err)
	}
	a := newFake(t, source.Mbox, "a", rawEntry("A", 1, "one"))
	index(t, s, a)
	s.Close()
	s = openAt(t, db, Options{})
	index(t, s, a)
	if a.scans.Load() != 1 {
		t.Errorf("scans = %d after reopen", a.scans.Load())
	}
	if n := len(allRows(t, s)); n != 1 {
		t.Errorf("rows = %d", n)
	}
}

func TestChangedInputReindexesOnlyThatSource(t *testing.T) {
	s := openStore(t)
	a := newFake(t, source.Mbox, "a", rawEntry("A", 1, "one"))
	b := newFake(t, source.Mbox, "b", rawEntry("B", 2, "two"))
	index(t, s, a, b)
	a.entries = append(a.entries, rawEntry("A", 3, "three"))
	touch(t, a.path)
	p := index(t, s, a, b)
	if a.scans.Load() != 2 || b.scans.Load() != 1 {
		t.Errorf("scans = %d, %d", a.scans.Load(), b.scans.Load())
	}
	if p.Sources[0].State != StateDone || p.Sources[1].State != StateCached {
		t.Errorf("states = %+v", p.Sources)
	}
	if got := sourceByPath(t, s, a.path).Count; got != 2 {
		t.Errorf("a count = %d", got)
	}
	if n := len(allRows(t, s)); n != 3 {
		t.Errorf("rows = %d", n)
	}
}

func TestInputSetChangeReindexes(t *testing.T) {
	s := openStore(t)
	a := newFake(t, source.EML, "dir", rawEntry("dir", 1, "one"))
	index(t, s, a)
	extra := filepath.Join(t.TempDir(), "2.eml")
	os.WriteFile(extra, []byte("x"), 0o644)
	a.inputs = append(a.inputs, extra)
	index(t, s, a)
	if a.scans.Load() != 2 {
		t.Errorf("added input: scans = %d", a.scans.Load())
	}
	a.inputs = a.inputs[:1]
	index(t, s, a)
	if a.scans.Load() != 3 {
		t.Errorf("removed input: scans = %d", a.scans.Load())
	}
	// A sidecar input that vanishes also forces a re-scan.
	a.inputs = append(a.inputs, extra)
	index(t, s, a)
	os.Remove(extra)
	index(t, s, a)
	if a.scans.Load() != 5 {
		t.Errorf("vanished input: scans = %d", a.scans.Load())
	}
}

func TestRemovedSourceDropped(t *testing.T) {
	s := openStore(t)
	a := newFake(t, source.Mbox, "a", rawEntry("A", 1, "one"))
	b := newFake(t, source.Mbox, "b", rawEntry("B", 2, "two"))
	index(t, s, a, b)
	index(t, s, b)
	srcs, _ := s.Sources()
	if len(srcs) != 1 || srcs[0].Path != b.path {
		t.Fatalf("sources = %+v", srcs)
	}
	rows := allRows(t, s)
	if len(rows) != 1 || rows[0].Subject != "two" {
		t.Fatalf("rows = %+v", rows)
	}
	var n int
	s.rdb.QueryRow(`SELECT count(*) FROM messages_fts`).Scan(&n)
	if n != 1 {
		t.Errorf("fts rows = %d", n)
	}
	s.rdb.QueryRow(`SELECT count(*) FROM folders`).Scan(&n)
	if n != 1 {
		t.Errorf("folders = %d", n)
	}
}

// cacheFiles returns the bytes of the database and its -wal file.
func cacheFiles(t *testing.T, db string) []byte {
	t.Helper()
	var all []byte
	for _, suffix := range []string{"", "-wal"} {
		b, err := os.ReadFile(db + suffix)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
		all = append(all, b...)
	}
	return all
}

func TestRemovedSourceLeavesNoText(t *testing.T) {
	db := filepath.Join(t.TempDir(), "index.db")
	s := openAt(t, db, Options{})
	var es []source.Entry
	for i := 0; i < 300; i++ {
		es = append(es, rawEntry("A", i, fmt.Sprintf("Quokkasecret plan %d", i)))
	}
	a := newFake(t, source.Mbox, "a", es...)
	b := newFake(t, source.Mbox, "b", rawEntry("B", 1, "two"))
	index(t, s, a, b)
	if !strings.Contains(strings.ToLower(string(cacheFiles(t, db))), "quokkasecret") {
		t.Fatal("test text not found in the index files at all")
	}
	index(t, s, b)
	// Neither the rows, nor the FTS index terms, nor freed pages, nor the
	// WAL keep the removed source's text (checked while still open).
	if bytes.Contains(bytes.ToLower(cacheFiles(t, db)), []byte("quokkasecret")) {
		t.Fatal("removed source's text still in the cache files")
	}
	if r := search(t, s, SearchParams{Q: "two"}); r.Total != 1 {
		t.Fatalf("remaining source: %d", r.Total)
	}
}

func TestRescannedSourceLeavesNoOldText(t *testing.T) {
	db := filepath.Join(t.TempDir(), "index.db")
	s := openAt(t, db, Options{})
	var es []source.Entry
	for i := 0; i < 300; i++ {
		es = append(es, rawEntry("A", i, fmt.Sprintf("Wombatsecret plan %d", i)))
	}
	a := newFake(t, source.Mbox, "a", es...)
	index(t, s, a)
	// The file changed: the messages are now others.
	a.entries = []source.Entry{rawEntry("A", 1, "replacement")}
	touch(t, a.path)
	index(t, s, a)
	if bytes.Contains(bytes.ToLower(cacheFiles(t, db)), []byte("wombatsecret")) {
		t.Fatal("old text of a re-indexed source still in the cache files")
	}
}

func TestScanErrorKeepsPartialAndRetries(t *testing.T) {
	s := openStore(t)
	a := newFake(t, source.Mbox, "a", rawEntry("A", 1, "one"), rawEntry("A", 2, "two"))
	a.scanErr, a.failAfter = errors.New("disk on fire"), 1
	p := index(t, s, a)
	if p.Sources[0].State != StateFailed || !strings.Contains(p.Sources[0].Error, "disk on fire") {
		t.Errorf("progress = %+v", p.Sources[0])
	}
	si := sourceByPath(t, s, a.path)
	if si.Complete || si.Count != 1 || !strings.Contains(si.Error, "disk on fire") {
		t.Errorf("source = %+v", si)
	}
	a.scanErr = nil
	index(t, s, a)
	si = sourceByPath(t, s, a.path)
	if a.scans.Load() != 2 || !si.Complete || si.Count != 2 || si.Error != "" {
		t.Errorf("after retry: scans=%d source=%+v", a.scans.Load(), si)
	}
}

func TestWarningsBounded(t *testing.T) {
	s := openStore(t)
	a := newFake(t, source.Mbox, "a", rawEntry("A", 1, "one"))
	for i := range maxStoredWarnings + 10 {
		a.warns = append(a.warns, fmt.Errorf("bad item %d\nwith newline", i))
	}
	p := index(t, s, a)
	if p.Warnings != int64(maxStoredWarnings+10) || p.Sources[0].Warnings != p.Warnings {
		t.Errorf("progress warnings = %d / %d", p.Warnings, p.Sources[0].Warnings)
	}
	si := sourceByPath(t, s, a.path)
	if si.WarningCount != int64(maxStoredWarnings+10) || len(si.Warnings) != maxStoredWarnings {
		t.Fatalf("source warnings = %d, %d stored", si.WarningCount, len(si.Warnings))
	}
	if si.Warnings[0] != "bad item 0 with newline" {
		t.Errorf("warning[0] = %q", si.Warnings[0])
	}
	if !si.Complete {
		t.Error("warnings made the source incomplete")
	}
}

func TestProgressUnits(t *testing.T) {
	s := openStore(t)
	mb := newFake(t, source.Mbox, "a", rawEntry("A", 0, "one"), rawEntry("A", 1, "two"))
	os.WriteFile(mb.path, make([]byte, 1000+len(rawMsg(1, "two"))), 0o644)
	olm := newFake(t, source.OLM, "b.olm", msgEntry("b/Inbox", 1, "x"), msgEntry("b/Inbox", 2, "y"))
	var reports []Progress
	if err := s.IndexSources(context.Background(), []source.Source{mb, olm}, func(p Progress) {
		reports = append(reports, p)
	}); err != nil {
		t.Fatal(err)
	}
	last := reports[len(reports)-1]
	m, o := last.Sources[0], last.Sources[1]
	if m.Unit != UnitBytes || m.Total != int64(1000+len(rawMsg(1, "two"))) || m.Done != m.Total || m.Messages != 2 {
		t.Errorf("mbox progress = %+v", m)
	}
	if o.Unit != UnitMessages || o.Done != 2 || o.Messages != 2 || o.Kind != source.OLM {
		t.Errorf("olm progress = %+v", o)
	}
	if last.Messages != 4 || !last.Finished {
		t.Errorf("last = %+v", last)
	}
	sawIndexing := false
	for _, p := range reports {
		if !p.Finished && !p.Running {
			t.Errorf("report before the end not Running: %+v", p)
		}
		if p.Sources[0].State == StateIndexing {
			sawIndexing = true
		}
	}
	if !sawIndexing {
		t.Error("never reported a source as indexing")
	}
	if st := s.Status(); !reflect.DeepEqual(st, last) {
		t.Errorf("Status() = %+v, want last report", st)
	}
}

func TestStatusWhileIndexing(t *testing.T) {
	s := openStore(t)
	a := newFake(t, source.Mbox, "a", rawEntry("A", 1, "one"))
	a.block = make(chan struct{})
	done := make(chan error)
	go func() { done <- s.IndexSources(context.Background(), []source.Source{a}, nil) }()
	deadline := time.Now().Add(5 * time.Second)
	for {
		st := s.Status()
		if st.Running && len(st.Sources) == 1 && st.Sources[0].State == StateIndexing {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("status = %+v", st)
		}
		time.Sleep(time.Millisecond)
	}
	// Reads work while indexing.
	if _, err := s.Search(SearchParams{}); err != nil {
		t.Fatal(err)
	}
	close(a.block)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if st := s.Status(); st.Running || !st.Finished {
		t.Errorf("final status = %+v", st)
	}
}

func TestCancelLeavesSourceIncomplete(t *testing.T) {
	s := openStore(t)
	a := newFake(t, source.Mbox, "a", rawEntry("A", 1, "one"), rawEntry("A", 2, "two"))
	a.block = make(chan struct{}, 1)
	a.block <- struct{}{} // first entry passes, then Scan waits
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		for s.Status().Sources == nil || s.Status().Sources[0].State != StateIndexing {
			time.Sleep(time.Millisecond)
		}
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()
	err := s.IndexSources(ctx, []source.Source{a}, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("IndexSources = %v, want Canceled", err)
	}
	if si := sourceByPath(t, s, a.path); si.Complete {
		t.Error("cancelled source marked complete")
	}
	a.block = nil
	index(t, s, a)
	if n := len(allRows(t, s)); n != 2 || a.scans.Load() != 2 {
		t.Errorf("after rerun rows=%d scans=%d", n, a.scans.Load())
	}
}

func TestAlreadyCancelled(t *testing.T) {
	s := openStore(t)
	a := newFake(t, source.Mbox, "a", rawEntry("A", 1, "one"))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := s.IndexSources(ctx, []source.Source{a}, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	if a.scans.Load() != 0 {
		t.Error("scanned with a cancelled context")
	}
}

func TestDeletedHiddenUnlessIncluded(t *testing.T) {
	for _, include := range []bool{false, true} {
		s := openAt(t, filepath.Join(t.TempDir(), "i.db"), Options{IncludeDeleted: include})
		del := rawEntry("A", 1, "gone")
		del.Flags = source.Deleted | source.Unread
		kept := rawEntry("A", 2, "kept")
		kept.Flags = source.Unread
		a := newFake(t, source.Mbox, "a", del, kept)
		index(t, s, a)
		si := sourceByPath(t, s, a.path)
		rows := allRows(t, s)
		want := 1
		if include {
			want = 2
		}
		if len(rows) != want || si.Count != int64(want) || si.Unread != int64(want) {
			t.Errorf("include=%v: rows=%d count=%d unread=%d", include, len(rows), si.Count, si.Unread)
		}
		// Asking for deleted mail shows it either way.
		r, err := s.Search(SearchParams{Q: "is:deleted"})
		if err != nil || len(r.Items) != 1 || r.Items[0].Subject != "gone" {
			t.Errorf("include=%v: is:deleted = %+v, %v", include, r.Items, err)
		}
	}
}

func TestResolvePanicIsWarned(t *testing.T) {
	old := resolve
	resolve = func(e *source.Entry) {
		if strings.Contains(string(e.Raw), "boom") {
			panic("boom")
		}
		e.Resolve()
	}
	t.Cleanup(func() { resolve = old })
	s := openStore(t)
	bad := rawEntry("A", 1, "boom")
	bad.Date = time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	a := newFake(t, source.Mbox, "a", bad, rawEntry("A", 2, "fine"))
	p := index(t, s, a)
	rows := allRows(t, s)
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want the failed message listed too", len(rows))
	}
	if !rows[0].Date.Equal(bad.Date) {
		t.Errorf("failed row = %+v", rows[0])
	}
	if p.Warnings != 1 {
		t.Errorf("warnings = %d", p.Warnings)
	}
}

func TestHostileEntriesSanitised(t *testing.T) {
	s := openStore(t)
	e := msgEntry("", 1, "bad\xffsubject")
	e.Labels = []string{"dup", "dup", "", "in\xffvalid"}
	e.Tags = []string{"t", "t"}
	for i := range maxProps + 50 {
		e.Props = append(e.Props, source.Prop{Key: fmt.Sprint("k", i), Value: strings.Repeat("v", 10)})
	}
	e.Props = append(e.Props, source.Prop{Key: "huge", Value: strings.Repeat("x", 1<<20)})
	index(t, s, newFake(t, source.OLM, "x.olm", e))
	r := allRows(t, s)[0]
	if r.Folder == "" || r.Subject != "bad�subject" {
		t.Errorf("row = %+v", r)
	}
	if !reflect.DeepEqual(r.Categories, []string{"dup", "in�valid"}) || len(r.Tags) != 1 {
		t.Errorf("categories = %q tags = %v", r.Categories, r.Tags)
	}
	m, _ := s.Message(r.ID)
	if len(m.Props) != maxProps {
		t.Errorf("props = %d, want %d", len(m.Props), maxProps)
	}
}

func TestLargeEntriesDoNotDeadlock(t *testing.T) {
	s := openStore(t)
	var es []source.Entry
	for i := range 6 {
		raw := append(rawMsg(i, "big"), []byte(strings.Repeat("filler words here\n", (budgetBytes/18)/2))...)
		es = append(es, source.Entry{Folder: "A", Raw: raw, Loc: source.Locator{Offset: int64(i), Length: 1}})
	}
	// One entry larger than the whole budget.
	es = append(es, source.Entry{Folder: "A", Raw: make([]byte, budgetBytes+1), Loc: source.Locator{Offset: 99, Length: 1}})
	done := make(chan struct{})
	go func() { index(t, s, newFake(t, source.Mbox, "a", es...)); close(done) }()
	select {
	case <-done:
	case <-time.After(60 * time.Second):
		t.Fatal("indexing large entries hangs")
	}
	if n := len(allRows(t, s)); n != 7 {
		t.Errorf("rows = %d", n)
	}
}

func TestScanOrderPreserved(t *testing.T) {
	s := openStore(t)
	var es []source.Entry
	for i := range 300 {
		e := msgEntry("A", uint64(i+1), fmt.Sprint("m", i))
		if i%7 == 0 { // some slow-to-resolve raw entries mixed in
			e = rawEntry("A", i, fmt.Sprint("m", i))
		}
		es = append(es, e)
	}
	index(t, s, newFake(t, source.OLM, "a.olm", es...))
	rows, err := s.rdb.Query(`SELECT subject FROM messages ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	i := 0
	for rows.Next() {
		var subj string
		rows.Scan(&subj)
		if subj != fmt.Sprint("m", i) {
			t.Fatalf("row %d = %q: scan order not kept", i, subj)
		}
		i++
	}
}

func TestEntryUnitsCountSummaries(t *testing.T) {
	big := msgEntry("A", 1, "s")
	big.Msg.Text = strings.Repeat("x", 1<<20-1024) // plus headers: 16 units
	labelled := msgEntry("A", 2, "s")
	labelled.Labels = []string{strings.Repeat("l", 300<<10)}
	labelled.Props = []source.Prop{{Key: "k", Value: strings.Repeat("v", 300<<10)}}
	for _, c := range []struct {
		name string
		e    source.Entry
		want int
	}{
		{"raw", source.Entry{Raw: make([]byte, 1<<20)}, 16},
		{"small", msgEntry("A", 3, "s"), 1},
		{"msg text", big, 16},
		{"labels and props", labelled, 10},
		{"huge", source.Entry{Raw: make([]byte, budgetBytes*2)}, budgetUnits},
	} {
		if got := entryUnits(&c.e); got != c.want {
			t.Errorf("%s: units = %d, want %d", c.name, got, c.want)
		}
	}
}

func TestLargeSummariesDoNotDeadlock(t *testing.T) {
	s := openStore(t)
	var es []source.Entry
	for i := range 5 {
		e := msgEntry("A", uint64(i+1), "big")
		e.Msg.Text = strings.Repeat("words ", budgetBytes/6/3)
		es = append(es, e)
	}
	e := msgEntry("A", 99, "huge")
	e.Msg.Text = strings.Repeat("y", budgetBytes+1)
	es = append(es, e)
	done := make(chan struct{})
	go func() { index(t, s, newFake(t, source.OLM, "a.olm", es...)); close(done) }()
	select {
	case <-done:
	case <-time.After(60 * time.Second):
		t.Fatal("indexing large summaries hangs")
	}
	if n := len(allRows(t, s)); n != 6 {
		t.Errorf("rows = %d", n)
	}
}

func TestPanickingSourceDoesNotCrash(t *testing.T) {
	s := openStore(t)
	bad := newFake(t, source.Mbox, "bad", rawEntry("A", 1, "one"), rawEntry("A", 2, "two"))
	bad.panicAt = 2
	meta := newFake(t, source.OLM, "meta.olm", msgEntry("M", 1, "meta"))
	meta.panicMeta = true
	good := newFake(t, source.Mbox, "good", rawEntry("G", 3, "three"))
	p := index(t, s, bad, meta, good)
	if sp := p.Sources[0]; sp.State != StateFailed || !strings.Contains(sp.Error, "scan boom") || sp.Messages != 1 {
		t.Errorf("panicking Scan: %+v", sp)
	}
	if sp := p.Sources[1]; sp.State != StateDone || sp.Warnings < 2 || sp.Messages != 1 {
		t.Errorf("panicking Folders/TagStyles/Inputs: %+v", sp)
	}
	if sp := p.Sources[2]; sp.State != StateDone || sp.Messages != 1 {
		t.Errorf("good source: %+v", sp)
	}
	si := sourceByPath(t, s, meta.path)
	if !strings.Contains(strings.Join(si.Warnings, "|"), "folders boom") {
		t.Errorf("warnings = %q", si.Warnings)
	}
	// A source whose Inputs panic cannot be fingerprinted: never reused.
	index(t, s, bad, meta, good)
	if meta.scans.Load() != 2 || good.scans.Load() != 1 || bad.scans.Load() != 2 {
		t.Errorf("scans bad=%d meta=%d good=%d", bad.scans.Load(), meta.scans.Load(), good.scans.Load())
	}
}

func TestConcurrentRunsAreRejected(t *testing.T) {
	db := filepath.Join(t.TempDir(), "i.db")
	s, err := Open(db, Options{})
	if err != nil {
		t.Fatal(err)
	}
	a := newFake(t, source.Mbox, "a", rawEntry("A", 1, "one"))
	a.block = make(chan struct{})
	done := make(chan error)
	go func() { done <- s.IndexSources(context.Background(), []source.Source{a}, nil) }()
	for s.Status().Sources == nil || s.Status().Sources[0].State != StateIndexing {
		time.Sleep(time.Millisecond)
	}
	b := newFake(t, source.Mbox, "b", rawEntry("B", 2, "two"))
	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := s.IndexSources(context.Background(), []source.Source{b}, nil); !errors.Is(err, ErrBusy) {
				t.Errorf("concurrent IndexSources = %v, want ErrBusy", err)
			}
		}()
	}
	wg.Wait()
	if err := s.Reset(); !errors.Is(err, ErrBusy) {
		t.Errorf("Reset while indexing = %v", err)
	}
	if err := s.Close(); !errors.Is(err, ErrBusy) {
		t.Errorf("Close while indexing = %v", err)
	}
	if _, err := s.Search(SearchParams{}); err != nil {
		t.Errorf("Search after refused Close: %v", err)
	}
	close(a.block)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if b.scans.Load() != 0 {
		t.Error("rejected run scanned")
	}
	if err := s.Reset(); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestMessageIDsNeverReused(t *testing.T) {
	db := filepath.Join(t.TempDir(), "i.db")
	s := openAt(t, db, Options{})
	a := newFake(t, source.Mbox, "a", rawEntry("A", 1, "one"), rawEntry("A", 2, "two"))
	index(t, s, a)
	var maxOld int64
	for _, r := range allRows(t, s) {
		maxOld = max(maxOld, r.ID)
	}
	b := newFake(t, source.Mbox, "b", rawEntry("B", 3, "three"))
	index(t, s, b) // a removed first, then b written
	touch(t, b.path)
	index(t, s, b) // b re-indexed: its new ids are new too
	rows := allRows(t, s)
	if len(rows) != 1 || rows[0].ID <= maxOld+1 {
		t.Fatalf("rows = %+v, want an id above %d", rows, maxOld+1)
	}
	for id := int64(1); id <= maxOld+1; id++ {
		if _, err := s.Message(id); !errors.Is(err, ErrNotFound) {
			t.Errorf("old id %d resolves: %v", id, err)
		}
	}
}
