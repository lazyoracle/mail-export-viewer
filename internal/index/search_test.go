package index

import (
	"fmt"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/lazyoracle/mail-export-viewer/internal/mail"
	"github.com/lazyoracle/mail-export-viewer/internal/source"
)

type fixtureMsg struct {
	key, folder  string
	labels, tags []string
	flags        source.Flags
	raw          string
}

// fixtureMbox are the messages of the mbox-kind fixture source.
var fixtureMbox = []fixtureMsg{
	{"report", "Work", []string{"Inbox", "Work"}, nil, source.Starred,
		"From: Alice Smith <alice@example.com>\nTo: Bob <bob@example.org>\nSubject: Quarterly report\n" +
			"Date: Fri, 10 Mar 2023 09:00:00 +0000\nMessage-ID: <report@example.com>\nMIME-Version: 1.0\n" +
			"Content-Type: multipart/mixed; boundary=XX\n\n--XX\nContent-Type: text/plain\n\nNumbers for the quarter are attached.\n" +
			"--XX\nContent-Type: application/pdf\nContent-Disposition: attachment; filename=\"q3-report.pdf\"\n" +
			"Content-Transfer-Encoding: base64\n\nAAAA\n--XX--\n"},
	{"lunch", "Work", []string{"Inbox"}, nil, source.Unread | source.Answered,
		"From: Bob <bob@example.org>\nTo: Alice Smith <alice@example.com>\nSubject: Lunch plans\n" +
			"Date: Mon, 1 May 2023 12:00:00 +0000\nMessage-ID: <lunch@example.org>\n\n" +
			"Café downtown at noon? The quarterly numbers can wait.\n"},
	{"cjk", "Other", nil, []string{"To Do"}, 0,
		"From: =?UTF-8?B?5bCP5p6X?= <carol@example.net>\nTo: Alice Smith <alice@example.com>\n" +
			"Subject: =?UTF-8?B?5piO5aSp55qE5Lya6K6u5a6J5o6S?=\nDate: Sat, 31 Dec 2022 08:00:00 +0000\n" +
			"MIME-Version: 1.0\nContent-Type: text/plain; charset=utf-8\n\n请查看附件 thanks\n"},
	{"xss", "Other", nil, nil, source.Junk,
		"From: Mallory <mallory@example.net>\nTo: Alice Smith <alice@example.com>\n" +
			"Subject: <b>hello</b> there\nDate: Mon, 1 Jan 2024 00:00:00 +0000\n\n" +
			"<script>alert('x')</script> & dangerous quarterly stuff <img src=x onerror=alert(1)>\n"},
	{"newsletter", "Other", []string{"Promotions", "Saved Items"}, []string{"Later"}, source.Forwarded | source.Unread,
		"From: News <news@example.com>\nTo: Alice Smith <alice@example.com>\nSubject: Weekly newsletter\n" +
			"Date: Wed, 15 Feb 2023 07:00:00 +0000\n\nUnsubscribe anytime. Quarterback stats inside.\n"},
	{"trash", "Other", nil, nil, source.Deleted,
		"From: Alice Smith <alice@example.com>\nTo: Bob <bob@example.org>\nSubject: Old quarterly draft\n" +
			"Date: Sat, 1 Apr 2023 00:00:00 +0000\n\nobsolete\n"},
}

// fixtureSources returns the fixture: an mbox-kind source with
// fixtureMbox and an olm-kind source with one categorised draft ("budget").
func fixtureSources(t testing.TB) []source.Source {
	var es []source.Entry
	for i, m := range fixtureMbox {
		es = append(es, source.Entry{Folder: m.folder, Labels: m.labels, Tags: m.tags, Flags: m.flags,
			Raw: []byte(m.raw), Loc: source.Locator{Offset: int64(i * 10000), Length: int64(len(m.raw))}})
	}
	mb := newFake(t, source.Mbox, "fixture.mbox", es...)
	mb.styles = []source.TagStyle{{Name: "To Do", Color: "#3333FF"}}
	budget := msgEntry("backup/Inbox", 1, "Budget planning")
	budget.Msg.Cc = []mail.Address{{Name: "Dana", Email: "dana@example.org"}}
	budget.Msg.Text = "Draft budget for the next quarter"
	budget.Labels = []string{"Red category"}
	budget.Flags = source.Draft
	olm := newFake(t, source.OLM, "backup.olm", budget)
	olm.folders = []string{"backup/Inbox", "backup/Empty"}
	return []source.Source{mb, olm}
}

// searchFixture indexes the fixture and returns the store and key -> id.
func searchFixture(t testing.TB, opts ...Options) (*Store, map[string]int64) {
	t.Helper()
	var o Options
	if len(opts) > 0 {
		o = opts[0]
	}
	s := openAt(t, filepath.Join(t.TempDir(), "index.db"), o)
	index(t, s, fixtureSources(t)...)
	ids := map[string]int64{}
	rows, err := s.rdb.Query(`SELECT id, subject FROM messages`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	keyOf := map[string]string{"Budget planning": "budget"}
	for _, m := range fixtureMbox {
		keyOf[mail.Summarize([]byte(m.raw), time.Time{}).Subject] = m.key
	}
	for rows.Next() {
		var id int64
		var subj string
		rows.Scan(&id, &subj)
		ids[keyOf[subj]] = id
	}
	if len(ids) != len(fixtureMbox)+1 {
		t.Fatalf("ids = %v", ids)
	}
	return s, ids
}

func search(t testing.TB, s *Store, p SearchParams) SearchResult {
	t.Helper()
	r, err := s.Search(p)
	if err != nil {
		t.Fatalf("Search(%+v): %v", p, err)
	}
	return r
}

func keysOf(r SearchResult, ids map[string]int64) []string {
	rev := map[int64]string{}
	for k, id := range ids {
		rev[id] = k
	}
	var out []string
	for _, it := range r.Items {
		out = append(out, rev[it.ID])
	}
	return out
}

func sorted(ks []string) []string {
	ks = slices.Clone(ks)
	sort.Strings(ks)
	return ks
}

func TestSearchOperators(t *testing.T) { forPlans(t, testSearchOperators) }

func testSearchOperators(t *testing.T) {
	s, ids := searchFixture(t)
	cases := []struct {
		q    string
		want []string
	}{
		{"quarterly", []string{"lunch", "report", "xss"}},
		{"quarterly numbers", []string{"lunch", "report"}},
		{`"numbers can wait"`, []string{"lunch"}},
		{`"wait numbers"`, nil},
		{"quarter", []string{"budget", "lunch", "newsletter", "report", "xss"}}, // prefix on last term
		{"quarter ", []string{"budget", "report"}},                              // trailing space: exact
		{"cafe", []string{"lunch"}},                                             // diacritics folded
		{"CAFÉ", []string{"lunch"}},
		{"from:alice", []string{"report"}},
		{"from:example.org", []string{"budget", "lunch"}},
		{"to:alice", []string{"cjk", "lunch", "newsletter", "xss"}},
		{"to:dana", []string{"budget"}}, // to: covers Cc
		{"cc:dana", []string{"budget"}},
		{"cc:alice", nil},
		{"subject:quarterly", []string{"report"}},
		{`subject:"lunch plans"`, []string{"lunch"}},
		{"subject:numbers", nil},
		{"folder:work", []string{"lunch", "report"}},
		{"mailbox:Other", []string{"cjk", "newsletter", "xss"}},
		{"folder:inbox", []string{"budget"}}, // last segment of backup/Inbox
		{"folder:backup/inbox", []string{"budget"}},
		{"folder:backup", nil}, // not a subfolder match
		{"folder:nope", nil},
		{"label:inbox", []string{"lunch", "report"}},
		{"label:work", []string{"report"}},
		{`label:"saved items"`, []string{"newsletter"}},
		{"label:inb", nil},                           // whole names only
		{`label:"red category"`, []string{"budget"}}, // label: covers categories
		{`category:"Red Category"`, []string{"budget"}},
		{"category:inbox", nil},
		{`tag:"to do"`, []string{"cjk"}},
		{"tag:later", []string{"newsletter"}},
		{"tag:inbox", nil},
		{"is:unread", []string{"lunch", "newsletter"}},
		{"is:read", []string{"budget", "cjk", "report", "xss"}},
		{"-is:unread", []string{"budget", "cjk", "report", "xss"}},
		{"is:starred", []string{"report"}},
		{"is:flagged", []string{"report"}},
		{"is:answered", []string{"lunch"}},
		{"is:forwarded", []string{"newsletter"}},
		{"is:draft", []string{"budget"}},
		{"is:junk", []string{"xss"}},
		{"is:deleted", []string{"trash"}},
		{"is:deleted quarterly", []string{"trash"}},
		{"is:unread is:answered", []string{"lunch"}},
		{"is:bogus", nil},
		{"has:attachment", []string{"report"}},
		{"-has:attachment", []string{"budget", "cjk", "lunch", "newsletter", "xss"}},
		{"filename:report", []string{"report"}},
		{"filename:.PDF", []string{"report"}},
		{"filename:docx", nil},
		{"before:2023", []string{"budget", "cjk"}},
		{"after:2024", []string{"xss"}},
		{"after:2023-03 before:2023-05-02", []string{"lunch", "report"}},
		{"after:2023/03 before:2023/05/02", []string{"lunch", "report"}},
		{"after:2023-05-02 before:2024", nil},
		{"quarterly -numbers", []string{"xss"}},
		{"-quarterly", []string{"budget", "cjk", "newsletter"}},
		{`-"numbers can wait"`, []string{"budget", "cjk", "newsletter", "report", "xss"}},
		{"-quarterly -newsletter", []string{"budget", "cjk"}},
		{"quarterly from:bob label:inbox after:2023", []string{"lunch"}},
		{"from:alice -has:attachment", nil},
		{"会议", []string{"cjk"}},
		{"小林", []string{"cjk"}},
		{"subject:会议", []string{"cjk"}},
		{"-会议", []string{"budget", "lunch", "newsletter", "report", "xss"}},
		{"请查看附件", []string{"cjk"}},
		{"x:quarterly", nil},
		{"-from:alice", []string{"budget", "cjk", "lunch", "newsletter", "xss"}},
		{"-to:alice", []string{"budget", "report"}},
		{"-subject:quarterly", []string{"budget", "cjk", "lunch", "newsletter", "xss"}},
		{"quarterly -subject:quarterly", []string{"lunch", "xss"}},
		{"quarterly -from:bob", []string{"report", "xss"}},
		{"-mailbox:work", []string{"budget", "cjk", "newsletter", "xss"}},
		{"quarterly -folder:work", []string{"xss"}},
		{"-label:inbox", []string{"budget", "cjk", "newsletter", "xss"}},
		{`-label:"saved items"`, []string{"budget", "cjk", "lunch", "report", "xss"}},
		{`-category:"red category"`, []string{"cjk", "lunch", "newsletter", "report", "xss"}},
		{"-tag:later", []string{"budget", "cjk", "lunch", "report", "xss"}},
		{"quarterly -filename:quarterly", []string{"lunch", "report", "xss"}},
		{"-filename:pdf", []string{"budget", "cjk", "lunch", "newsletter", "xss"}},
		{"-subject:会议", []string{"budget", "lunch", "newsletter", "report", "xss"}},
		{"-from:小林", []string{"budget", "lunch", "newsletter", "report", "xss"}},
		{"-to:bob -from:news", []string{"budget", "cjk", "lunch", "xss"}},
		{"script", []string{"xss"}},
	}
	for _, c := range cases {
		r := search(t, s, SearchParams{Q: c.q, Limit: 100})
		if got := sorted(keysOf(r, ids)); !reflect.DeepEqual(got, c.want) {
			t.Errorf("Search(%q) = %v, want %v", c.q, got, c.want)
		}
		if r.Total != int64(len(c.want)) {
			t.Errorf("Search(%q) Total = %d, want %d", c.q, r.Total, len(c.want))
		}
	}
}

func TestSearchScopes(t *testing.T) {
	s, ids := searchFixture(t)
	var mboxID, workID int64
	srcs, _ := s.Sources()
	for _, si := range srcs {
		if si.Kind == source.Mbox {
			mboxID = si.ID
		}
	}
	for _, r := range allRows(t, s) {
		if r.Folder == "Work" {
			workID = r.FolderID
		}
	}
	for _, c := range []struct {
		p    SearchParams
		want []string
	}{
		{SearchParams{Q: "quarterly", FolderID: workID}, []string{"lunch", "report"}},
		{SearchParams{SourceID: mboxID}, []string{"cjk", "lunch", "newsletter", "report", "xss"}},
		{SearchParams{Folder: "backup/Inbox"}, []string{"budget"}},
		{SearchParams{Folder: "Inbox"}, nil}, // exact tree path only
		{SearchParams{Q: "is:deleted", FolderID: workID}, nil},
	} {
		c.p.Limit = 100
		r := search(t, s, c.p)
		if got := sorted(keysOf(r, ids)); !reflect.DeepEqual(got, c.want) || r.Total != int64(len(c.want)) {
			t.Errorf("Search(%+v) = %v (total %d), want %v", c.p, got, r.Total, c.want)
		}
	}
}

func TestSearchIncludeDeleted(t *testing.T) {
	s, ids := searchFixture(t, Options{IncludeDeleted: true})
	r := search(t, s, SearchParams{Q: "quarterly"})
	if got := sorted(keysOf(r, ids)); !reflect.DeepEqual(got, []string{"lunch", "report", "trash", "xss"}) {
		t.Errorf("quarterly = %v", got)
	}
	if r := search(t, s, SearchParams{}); r.Total != 7 {
		t.Errorf("total = %d", r.Total)
	}
}

func TestSearchGarbageNeverErrors(t *testing.T) { forPlans(t, testSearchGarbageNeverErrors) }

func testSearchGarbageNeverErrors(t *testing.T) {
	s, _ := searchFixture(t)
	inputs := []string{`"`, `""`, `*`, `(`, `)`, `()`, `AND OR NOT`, `NOT`, `a:`, `-`, `--`, `- -`,
		`^`, `NEAR(a b)`, `{subject}:x`, `subject:`, `from:`, `"a" AND "b`, `foo*`, `*foo`, `a OR b`,
		`'`, `\`, `%`, `_`, `col:"x`, `-"`, `before:`, `after:x`, `has:`, `label:`, `is:`, `tag:%`, "\x00", "\xff\xfe",
		`body:secret`, `rowid:1`, `messages_fts:x`, `🙂`, "a\tb\nc", `folder:%_`, `category:"`,
		strings.Repeat("x", 100000), strings.Repeat("ab ", 20000), strings.Repeat(`"`, 5001),
		strings.Repeat("-a ", 3000), strings.Repeat("会", 5000), strings.Repeat("label:x ", 500)}
	for _, q := range inputs {
		for _, so := range []string{"date_desc", "date_asc", "from", "subject", "relevance", "bogus"} {
			if _, err := s.Search(SearchParams{Q: q, Sort: so, Limit: 10}); err != nil {
				t.Errorf("Search(%.40q, %s): %v", q, so, err)
			}
		}
	}
	// FTS syntax in the input is literal, not operators.
	r := search(t, s, SearchParams{Q: "quarterly OR newsletter"})
	if len(r.Items) != 0 {
		t.Errorf("OR was treated as an operator: %d items", len(r.Items))
	}
}

func TestSearchEmptyListsBySort(t *testing.T) {
	s, ids := searchFixture(t)
	for so, want := range map[string][]string{
		"":          {"xss", "lunch", "report", "newsletter", "cjk", "budget"},
		"date_desc": {"xss", "lunch", "report", "newsletter", "cjk", "budget"},
		"date_asc":  {"budget", "cjk", "newsletter", "report", "lunch", "xss"},
		"relevance": {"xss", "lunch", "report", "newsletter", "cjk", "budget"}, // no terms: date_desc
		"from":      {"report", "lunch", "xss", "newsletter", "budget", "cjk"}, // Alice, Bob, Mallory, News, Olm, 小林
		"subject":   {"xss", "budget", "lunch", "report", "newsletter", "cjk"},
	} {
		r := search(t, s, SearchParams{Sort: so})
		if got := keysOf(r, ids); !reflect.DeepEqual(got, want) {
			t.Errorf("sort %q = %v, want %v", so, got, want)
		}
		if r.Total != 6 || r.NextCursor != "" {
			t.Errorf("sort %q Total=%d NextCursor=%q", so, r.Total, r.NextCursor)
		}
	}
}

func TestSearchRelevance(t *testing.T) {
	s, ids := searchFixture(t)
	r := search(t, s, SearchParams{Q: "quarterly", Sort: "relevance"})
	if got := keysOf(r, ids); len(got) != 3 || got[0] != "report" {
		t.Fatalf("relevance = %v, want report first", got)
	}
}

func TestSearchHighlights(t *testing.T) {
	s, ids := searchFixture(t)
	r := search(t, s, SearchParams{Q: "cafe"})
	if h := r.Highlights[ids["lunch"]]; !strings.Contains(h, "<mark>Café</mark>") {
		t.Fatalf("highlight = %q", h)
	}
	if r.ThreadCounts == nil {
		t.Errorf("ThreadCounts is nil, want empty map")
	}
	r = search(t, s, SearchParams{Q: "-cafe"})
	if len(r.Highlights) != 0 {
		t.Errorf("negation-only highlights = %v", r.Highlights)
	}
}

func TestSearchHighlightEscapesHTML(t *testing.T) {
	s, ids := searchFixture(t)
	for _, q := range []string{"dangerous", "script", "alert", "img", "hello"} {
		r := search(t, s, SearchParams{Q: q + " "})
		h, ok := r.Highlights[ids["xss"]]
		if !ok {
			t.Fatalf("%q: no highlight", q)
		}
		if !strings.Contains(h, "<mark>") {
			t.Errorf("%q: no <mark> in %q", q, h)
		}
		rest := strings.NewReplacer("<mark>", "", "</mark>", "").Replace(h)
		if strings.ContainsAny(rest, "<>") {
			t.Errorf("%q: unescaped HTML in highlight %q", q, h)
		}
		if strings.Count(h, "<mark>") != strings.Count(h, "</mark>") {
			t.Errorf("%q: unbalanced marks %q", q, h)
		}
	}
}

func TestHighlightHTML(t *testing.T) {
	for in, want := range map[string]string{
		"a " + hlOpen + "b" + hlClose + " c":          "a <mark>b</mark> c",
		"<x>&" + hlOpen + "<y>" + hlClose:             "&lt;x&gt;&amp;<mark>&lt;y&gt;</mark>",
		hlOpen + "open" + hlOpen + "twice":            "<mark>opentwice</mark>",
		"stray" + hlClose + " " + hlOpen + "unclosed": "stray <mark>unclosed</mark>",
		`"quotes" 'single'`:                           "&#34;quotes&#34; &#39;single&#39;",
	} {
		if got := highlightHTML(in); got != want {
			t.Errorf("highlightHTML(%q) = %q, want %q", in, got, want)
		}
	}
}

// paginationFixture indexes n messages sharing one date plus undated ones.
func paginationFixture(t testing.TB, n, undated int) *Store {
	t.Helper()
	var es []source.Entry
	for i := 0; i < n+undated; i++ {
		raw := fmt.Sprintf("From: Same Person <same@example.com>\nTo: x@example.com\n"+
			"Subject: same subject\nDate: Mon, 2 Jan 2023 15:04:05 +0000\nMessage-ID: <p%d@x>\n\nshared body word %d\n", i, i)
		es = append(es, source.Entry{Folder: "box", Raw: []byte(raw), Loc: source.Locator{Offset: int64(i), Length: 1}})
	}
	s := openStore(t)
	index(t, s, newFake(t, source.Mbox, "box", es...))
	if undated > 0 {
		if _, err := s.wdb.Exec(`UPDATE messages SET date_unix = NULL WHERE id IN
			(SELECT id FROM messages ORDER BY id DESC LIMIT ?)`, undated); err != nil {
			t.Fatal(err)
		}
		if _, err := s.wdb.Exec(`UPDATE messages SET date_unix = date_unix - 86400 WHERE id =
			(SELECT min(id) FROM messages)`); err != nil {
			t.Fatal(err)
		}
	}
	return s
}

func TestSearchPaginationStable(t *testing.T) { forPlans(t, testSearchPaginationStable) }

func testSearchPaginationStable(t *testing.T) {
	s := paginationFixture(t, 23, 4)
	for _, q := range []string{"", "shared", "same", "-nothing", "is:read"} {
		for _, so := range []string{"date_desc", "date_asc", "from", "subject", "relevance"} {
			full := search(t, s, SearchParams{Q: q, Sort: so, Limit: 1000})
			if len(full.Items) != 27 {
				t.Fatalf("%q/%s: full = %d items", q, so, len(full.Items))
			}
			var paged []int64
			cursor := ""
			for page := 0; ; page++ {
				r := search(t, s, SearchParams{Q: q, Sort: so, Limit: 4, Cursor: cursor})
				if page == 0 && r.Total != 27 {
					t.Errorf("%q/%s: first-page Total = %d", q, so, r.Total)
				}
				for _, it := range r.Items {
					paged = append(paged, it.ID)
				}
				if r.NextCursor == "" {
					break
				}
				if len(r.Items) != 4 {
					t.Fatalf("%q/%s: short page %d with cursor", q, so, len(r.Items))
				}
				cursor = r.NextCursor
				if page > 20 {
					t.Fatalf("%q/%s: pagination does not terminate", q, so)
				}
			}
			var want []int64
			for _, it := range full.Items {
				want = append(want, it.ID)
			}
			if !reflect.DeepEqual(paged, want) {
				t.Errorf("%q/%s: paged\n%v\nwant\n%v", q, so, paged, want)
			}
		}
	}
	for _, so := range []string{"date_desc", "date_asc"} {
		r := search(t, s, SearchParams{Sort: so, Limit: 1000})
		for i, it := range r.Items {
			if (i >= 23) != it.Date.IsZero() {
				t.Fatalf("%s: item %d date %v", so, i, it.Date)
			}
		}
	}
}

func TestSearchBadCursor(t *testing.T) {
	s, _ := searchFixture(t)
	r := search(t, s, SearchParams{Limit: 2})
	if r.NextCursor == "" {
		t.Fatal("no cursor")
	}
	for _, c := range []string{"garbage", "!!!", r.NextCursor + "x"} {
		if _, err := s.Search(SearchParams{Limit: 2, Cursor: c}); err == nil {
			t.Errorf("cursor %q accepted", c)
		}
	}
	if _, err := s.Search(SearchParams{Limit: 2, Cursor: r.NextCursor, Sort: "subject"}); err == nil {
		t.Error("cross-sort cursor accepted")
	}
	r2 := search(t, s, SearchParams{Limit: 2, Cursor: r.NextCursor})
	if r2.Total != -1 || len(r2.Items) != 2 {
		t.Errorf("page 2: Total=%d items=%d", r2.Total, len(r2.Items))
	}
}

func TestSearchRowMatchesMessage(t *testing.T) {
	s, ids := searchFixture(t)
	r := search(t, s, SearchParams{Q: `tag:"to do"`})
	if len(r.Items) != 1 {
		t.Fatalf("items = %d", len(r.Items))
	}
	m, err := s.Message(ids["cjk"])
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(r.Items[0], m.Row) {
		t.Fatalf("item = %+v, want %+v", r.Items[0], m.Row)
	}
	if want := []Tag{{Name: "To Do", Color: "#3333FF"}}; !reflect.DeepEqual(m.Tags, want) {
		t.Errorf("tags = %+v", m.Tags)
	}
}

// forPlans runs f with the normal query plans and with every full-text match
// treated as common (the date-index-walk plan of isCommon).
func forPlans(t *testing.T, f func(*testing.T)) {
	t.Run("selective", f)
	t.Run("common", func(t *testing.T) {
		old := commonMin
		commonMin = 1
		t.Cleanup(func() { commonMin = old })
		f(t)
	})
}

// Sorting the whole list by sender or subject walks an index instead of
// sorting every message.
func TestSortByKeyUsesIndex(t *testing.T) {
	s, _ := searchFixture(t)
	for _, key := range []string{`COALESCE(NULLIF(m.from_name, ''), m.from_email)`, `m.subject`} {
		rows, err := s.rdb.Query(`EXPLAIN QUERY PLAN SELECT m.id FROM messages m ORDER BY ` + key + ` COLLATE NOCASE, m.id LIMIT 51`)
		if err != nil {
			t.Fatal(err)
		}
		var plan strings.Builder
		for rows.Next() {
			var id, parent, unused int
			var detail string
			rows.Scan(&id, &parent, &unused, &detail)
			plan.WriteString(detail + "\n")
		}
		rows.Close()
		if strings.Contains(plan.String(), "TEMP B-TREE") {
			t.Errorf("ORDER BY %s sorts in a temp b-tree:\n%s", key, plan.String())
		}
	}
}

func TestCJKSubstringMixedCase(t *testing.T) {
	raw := "Subject: =?UTF-8?B?5Lya6K6uQUJD5a6J5o6S?=\nFrom: x@example.com\n\nbody\n" // 会议ABC安排
	s := openStore(t)
	index(t, s, newFake(t, source.Mbox, "box", source.Entry{Folder: "box", Raw: []byte(raw), Loc: source.Locator{Length: 1}}))
	for _, q := range []string{"会议", "议ABC安", "议abc安"} {
		if r := search(t, s, SearchParams{Q: q}); r.Total != 1 {
			t.Errorf("Search(%q) total = %d, want 1", q, r.Total)
		}
	}
}

// FuzzSearch: no query text (with any sort) makes Search fail.
func FuzzSearch(f *testing.F) {
	for _, s := range []string{"quarterly", `"numbers can wait"`, "-from:alice", "-label:\"saved items\" x",
		"会议 -subject:会", "from: to:x -mailbox:work filename:.pdf", `-"`, "after:2023 before:2024-02",
		"has:attachment -has:att", "is:unread -is:read tag:x category:\"y\""} {
		f.Add(s, uint8(0))
	}
	s, _ := searchFixture(f)
	sorts := []string{"", "date_asc", "from", "subject", "relevance"}
	f.Fuzz(func(t *testing.T, q string, sortN uint8) {
		so := sorts[int(sortN)%len(sorts)]
		r, err := s.Search(SearchParams{Q: q, Sort: so, Limit: 2})
		if err != nil {
			t.Fatalf("Search(%q): %v", q, err)
		}
		if r.NextCursor != "" {
			if _, err := s.Search(SearchParams{Q: q, Sort: so, Limit: 2, Cursor: r.NextCursor}); err != nil {
				t.Fatalf("Search(%q) page 2: %v", q, err)
			}
		}
	})
}
