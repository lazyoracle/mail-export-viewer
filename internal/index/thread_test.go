package index

import (
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/lazyoracle/mail-export-viewer/internal/source"
)

const day = int64(24 * 3600)

var t0 = time.Date(2023, 1, 1, 0, 0, 0, 0, time.UTC).Unix()

// addMsg inserts a bare message row (in source/folder src) and returns its id.
func addMsg(t testing.TB, s *Store, src int64, mid, irt, refs, subj string, date int64) int64 {
	return addMsgFlags(t, s, src, mid, irt, refs, subj, date, 0)
}

func addMsgFlags(t testing.TB, s *Store, src int64, mid, irt, refs, subj string, date int64, flags source.Flags) int64 {
	t.Helper()
	var d any
	if date != 0 {
		d = date
	}
	if _, err := s.wdb.Exec(`INSERT OR IGNORE INTO sources(id, path, kind) VALUES(?, ?, 'mbox')`, src, fmt.Sprint("/p", src)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.wdb.Exec(`INSERT OR IGNORE INTO folders(id, source_id, path, total) VALUES(?, ?, 'f', 0)`, src, src); err != nil {
		t.Fatal(err)
	}
	if _, err := s.wdb.Exec(`UPDATE folders SET total = total + 1 WHERE id = ?`, src); err != nil {
		t.Fatal(err)
	}
	res, err := s.wdb.Exec(`INSERT INTO messages(source_id, folder_id, loc, message_id, in_reply_to, refs, subject, date_unix, flags)
		VALUES(?,?,'0:1:0:',?,?,?,?,?,?)`, src, src, mid, irt, refs, subj, d, flags)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	return id
}

func tid(t testing.TB, s *Store, id int64) int64 {
	t.Helper()
	m, err := s.Message(id)
	if err != nil {
		t.Fatal(err)
	}
	if m.ThreadID == 0 {
		t.Fatalf("message %d has no thread", id)
	}
	return m.ThreadID
}

func assignAll(t testing.TB, s *Store) {
	t.Helper()
	if err := s.assignThreads(); err != nil {
		t.Fatal(err)
	}
}

func TestThreadReplyChain(t *testing.T) {
	s := openStore(t)
	a := addMsg(t, s, 1, "a", "", "", "plan", t0)
	b := addMsg(t, s, 1, "b", "a", "a", "xx1", t0+day)
	c := addMsg(t, s, 1, "c", "b", "a b", "xx2", t0+2*day)
	x := addMsg(t, s, 1, "x", "", "", "unrelated thing", t0)
	assignAll(t, s)
	if tid(t, s, a) != tid(t, s, b) || tid(t, s, b) != tid(t, s, c) {
		t.Fatal("chain not joined")
	}
	if tid(t, s, x) == tid(t, s, a) {
		t.Fatal("unrelated joined")
	}
	th, err := s.Thread(b)
	if err != nil || len(th) != 3 || th[0].ID != a || th[2].ID != c {
		t.Fatalf("thread = %+v err %v", th, err)
	}
}

func TestThreadBrokenMiddle(t *testing.T) {
	s := openStore(t)
	a := addMsg(t, s, 1, "a", "", "", "s1", t0)
	c := addMsg(t, s, 1, "c", "b", "a b", "s2", t0+day)
	d := addMsg(t, s, 1, "d", "b", "b", "s3", t0+2*day)
	assignAll(t, s)
	if tid(t, s, a) != tid(t, s, c) || tid(t, s, c) != tid(t, s, d) {
		t.Fatal("not joined across missing message")
	}
}

func TestThreadSubjectFallback(t *testing.T) {
	s := openStore(t)
	a := addMsg(t, s, 1, "", "", "", "Quarterly report", t0)
	b := addMsg(t, s, 1, "", "", "", "RE: Re: AW: quarterly   REPORT", t0+10*day)
	c := addMsg(t, s, 1, "", "", "", "Fwd: [team] quarterly report", t0+35*day) // chained: 25d after b
	far := addMsg(t, s, 1, "", "", "", "Quarterly report", t0+200*day)
	nodate := addMsg(t, s, 1, "", "", "", "Quarterly report", 0)
	assignAll(t, s)
	if tid(t, s, a) != tid(t, s, b) || tid(t, s, b) != tid(t, s, c) {
		t.Fatal("subject chain not joined")
	}
	if tid(t, s, far) == tid(t, s, a) {
		t.Fatal("far-apart same subject merged")
	}
	if tid(t, s, nodate) == tid(t, s, a) {
		t.Fatal("undated merged by subject")
	}
}

func TestThreadSubjectNotShortOrGeneric(t *testing.T) {
	s := openStore(t)
	var ids []int64
	for _, subj := range []string{"hi", "Re: hi", "", "", "test", "Re: test", "(no subject)", "(no subject)", "Re:", "Re:", "[x]", "[x]"} {
		ids = append(ids, addMsg(t, s, 1, "", "", "", subj, t0))
	}
	assignAll(t, s)
	seen := map[int64]bool{}
	for _, id := range ids {
		th := tid(t, s, id)
		if seen[th] {
			t.Fatalf("generic subjects merged (thread %d)", th)
		}
		seen[th] = true
	}
}

func TestThreadCyclicReferences(t *testing.T) {
	s := openStore(t)
	a := addMsg(t, s, 1, "a", "b", "b c", "s1", t0)
	b := addMsg(t, s, 1, "b", "a", "a", "s2", t0+day)
	c := addMsg(t, s, 1, "c", "c", "c a", "s3", t0+2*day)
	assignAll(t, s)
	if tid(t, s, a) != tid(t, s, b) || tid(t, s, b) != tid(t, s, c) {
		t.Fatal("cycle not one thread")
	}
}

func TestThreadMissingIds(t *testing.T) {
	s := openStore(t)
	a := addMsg(t, s, 1, "", "", "", "alone one", t0)
	b := addMsg(t, s, 1, "", "", "", "alone two", t0)
	assignAll(t, s)
	if tid(t, s, a) == tid(t, s, b) {
		t.Fatal("id-less messages merged")
	}
	c := addMsg(t, s, 1, "", "zzz", "zzz", "other", t0)
	assignAll(t, s)
	if tid(t, s, c) == tid(t, s, a) {
		t.Fatal("joined")
	}
}

func TestThreadAcrossSources(t *testing.T) {
	s := openStore(t)
	// The same message in two sources forms one thread with its replies.
	a1 := addMsg(t, s, 1, "a", "", "", "topic one", t0)
	a2 := addMsg(t, s, 2, "a", "", "", "topic one", t0)
	r := addMsg(t, s, 1, "r", "a", "a", "x", t0+day)
	other := addMsg(t, s, 1, "o", "", "", "topic two", t0)
	assignAll(t, s)
	if tid(t, s, a1) != tid(t, s, a2) || tid(t, s, a1) != tid(t, s, r) {
		t.Fatal("duplicates not one thread")
	}
	if tid(t, s, other) == tid(t, s, a1) {
		t.Fatal("unrelated glued")
	}
	if th, _ := s.Thread(r); len(th) != 3 {
		t.Fatalf("thread len %d", len(th))
	}
}

func TestThreadOrderingUnknownDatesLast(t *testing.T) {
	s := openStore(t)
	a := addMsg(t, s, 1, "a", "", "", "q", 0)
	b := addMsg(t, s, 1, "b", "a", "a", "q", t0+day)
	c := addMsg(t, s, 1, "c", "a", "a", "q", t0)
	assignAll(t, s)
	th, err := s.Thread(a)
	if err != nil || len(th) != 3 || th[0].ID != c || th[1].ID != b || th[2].ID != a {
		t.Fatalf("order %+v err %v", th, err)
	}
	if _, err := s.Thread(9999); err != ErrNotFound {
		t.Fatalf("err = %v", err)
	}
}

func TestThreadHidesDeleted(t *testing.T) {
	for _, include := range []bool{false, true} {
		s := openAt(t, filepath.Join(t.TempDir(), "i.db"), Options{IncludeDeleted: include})
		a := addMsg(t, s, 1, "a", "", "", "q", t0)
		b := addMsgFlags(t, s, 1, "b", "a", "a", "q", t0+day, source.Deleted)
		assignAll(t, s)
		th, _ := s.Thread(a)
		want := 1
		if include {
			want = 2
		}
		if len(th) != want {
			t.Errorf("include=%v: thread len %d", include, len(th))
		}
		res, _ := s.Search(SearchParams{})
		if res.ThreadCounts[a] != want {
			t.Errorf("include=%v: thread count %d", include, res.ThreadCounts[a])
		}
		// The deleted message itself still shows its thread.
		if th, _ := s.Thread(b); len(th) != 2 {
			t.Errorf("include=%v: Thread(deleted) len %d", include, len(th))
		}
	}
}

func TestThreadIncrementalNoRewrite(t *testing.T) {
	s := openStore(t)
	a := addMsg(t, s, 1, "a", "", "", "s1", t0)
	assignAll(t, s)
	before := tid(t, s, a)
	b := addMsg(t, s, 1, "b", "a", "a", "s2", t0)
	assignAll(t, s)
	if tid(t, s, a) != before || tid(t, s, b) != before {
		t.Fatal("thread id unstable")
	}
}

func TestSearchThreadCounts(t *testing.T) {
	s := openStore(t)
	a := addMsg(t, s, 1, "a", "", "", "s1", t0)
	b := addMsg(t, s, 1, "b", "a", "a", "s2", t0+day)
	x := addMsg(t, s, 1, "x", "", "", "s3", t0+2*day)
	assignAll(t, s)
	res, err := s.Search(SearchParams{})
	if err != nil {
		t.Fatal(err)
	}
	if res.ThreadCounts[a] != 2 || res.ThreadCounts[b] != 2 || res.ThreadCounts[x] != 1 {
		t.Fatalf("counts %v", res.ThreadCounts)
	}
}

func TestNormalizeSubject(t *testing.T) {
	for in, want := range map[string]string{
		"Re: Hello World": "hello world", "FW:  RE[2]: x  y ": "x y", "Antw: SV: wg: tr: Budget": "budget",
		"Review": "review", "re:": "", "[dev] Re: Budget": "budget", "Re: [dev] Re: budget": "budget",
		"[only a tag]": "[only a tag]",
	} {
		if got := normalizeSubject(in); got != want {
			t.Errorf("%q -> %q, want %q", in, got, want)
		}
	}
}

func TestThreading100k(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}
	s := openStore(t)
	const n = 100000
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	tx, err := s.wdb.Begin()
	must(err)
	_, err = tx.Exec(`INSERT INTO sources(id, path, kind) VALUES(1, '/p', 'mbox')`)
	must(err)
	_, err = tx.Exec(`INSERT INTO folders(id, source_id, path) VALUES(1, 1, 'f')`)
	must(err)
	st, err := tx.Prepare(`INSERT INTO messages(source_id, folder_id, loc, message_id, in_reply_to, refs, subject, date_unix)
		VALUES(1,1,'0:1:0:',?,?,?,?,?)`)
	must(err)
	for i := 0; i < n; i++ {
		irt, refs := "", ""
		if i%5 != 0 {
			irt = fmt.Sprintf("m%d", i-1)
			refs = fmt.Sprintf("m%d", i-1)
			if i%5 > 1 {
				refs = fmt.Sprintf("m%d m%d", i-2, i-1)
			}
		}
		_, err := st.Exec(fmt.Sprintf("m%d", i), irt, refs, fmt.Sprintf("Subject number %d", i/5), t0+int64(i)*60)
		must(err)
	}
	must(tx.Commit())
	start := time.Now()
	assignAll(t, s)
	t.Logf("threaded %d messages in %v", n, time.Since(start))
	var c int
	must(s.rdb.QueryRow(`SELECT COUNT(DISTINCT thread_id) FROM messages`).Scan(&c))
	if c != n/5 {
		t.Fatalf("threads = %d, want %d", c, n/5)
	}
}

func TestIndexSourcesAssignsAndHealsThreads(t *testing.T) {
	a := source.Entry{Folder: "box", Loc: source.Locator{Offset: 0, Length: 1}, Raw: []byte(
		"From: a@example.com\nSubject: Lunch plans\nDate: Mon, 2 Jan 2023 10:00:00 +0000\nMessage-ID: <a@x>\n\nhi\n")}
	b := source.Entry{Folder: "box", Loc: source.Locator{Offset: 1, Length: 1}, Raw: []byte(
		"From: b@example.com\nSubject: Re: Lunch plans\nDate: Mon, 2 Jan 2023 11:00:00 +0000\nMessage-ID: <b@x>\n" +
			"In-Reply-To: <a@x>\nReferences: <a@x>\n\nyo\n")}
	s := openStore(t)
	f := newFake(t, source.Mbox, "box", a, b)
	index(t, s, f)
	rows := allRows(t, s)
	if len(rows) != 2 || rows[0].ThreadID != rows[1].ThreadID || rows[0].ThreadID == 0 {
		t.Fatalf("not threaded: %+v", rows)
	}
	if _, err := s.wdb.Exec(`UPDATE messages SET thread_id = NULL`); err != nil {
		t.Fatal(err)
	}
	index(t, s, f) // nothing changed on disk
	rows = allRows(t, s)
	if rows[0].ThreadID != rows[1].ThreadID || rows[0].ThreadID == 0 {
		t.Fatal("not healed")
	}
}

func TestThreadHubReferenceIgnored(t *testing.T) {
	s := openStore(t)
	var ids []int64
	for i := 0; i < 200; i++ {
		ids = append(ids, addMsg(t, s, 1, fmt.Sprintf("u%d", i), "list-hub", "list-hub", fmt.Sprintf("subject %d", i), t0+int64(i)*day))
	}
	assignAll(t, s)
	seen := map[int64]bool{}
	for _, id := range ids {
		seen[tid(t, s, id)] = true
	}
	if len(seen) != 200 {
		t.Fatalf("hub glued: %d threads, want 200", len(seen))
	}
}

func TestThreadHubClaimedIgnored(t *testing.T) {
	s := openStore(t)
	var ids []int64
	for i := 0; i < 100; i++ {
		ids = append(ids, addMsg(t, s, 1, "constant-id", "", "", fmt.Sprintf("subject %d", i), t0))
	}
	assignAll(t, s)
	seen := map[int64]bool{}
	for _, id := range ids {
		seen[tid(t, s, id)] = true
	}
	if len(seen) != 100 {
		t.Fatalf("claimed hub glued: %d threads", len(seen))
	}
}

func TestThreadBogusIDsIgnored(t *testing.T) {
	s := openStore(t)
	a := addMsg(t, s, 1, "<>", "<>", "<>", "subject aaa", t0)
	b := addMsg(t, s, 1, "<>", "<>", "<>", "subject bbb", t0)
	c := addMsg(t, s, 1, "", " ", "", "subject ccc", t0)
	assignAll(t, s)
	if tid(t, s, a) == tid(t, s, b) || tid(t, s, a) == tid(t, s, c) {
		t.Fatal("bogus ids joined")
	}
}

func TestThreadBigRealThreadStillJoins(t *testing.T) {
	s := openStore(t)
	root := addMsg(t, s, 1, "root", "", "", "big topic", t0)
	ids := []int64{root}
	for i := 0; i < 80; i++ {
		ids = append(ids, addMsg(t, s, 1, fmt.Sprintf("r%d", i), "root", "root", fmt.Sprintf("x%d", i), t0+int64(i+1)*60))
	}
	assignAll(t, s)
	for _, id := range ids {
		if tid(t, s, id) != tid(t, s, root) {
			t.Fatal("big thread split")
		}
	}
}

func TestThreadRecurringSubjectNotMerged(t *testing.T) {
	s := openStore(t)
	seen := map[int64]bool{}
	var ids []int64
	for i := 0; i < 100; i++ {
		ids = append(ids, addMsg(t, s, 1, "", "", "", "Your daily report", t0+int64(i)*25*day))
	}
	assignAll(t, s)
	for _, id := range ids {
		seen[tid(t, s, id)] = true
	}
	if len(seen) != 100 {
		t.Fatalf("recurring merged: %d threads", len(seen))
	}
}
