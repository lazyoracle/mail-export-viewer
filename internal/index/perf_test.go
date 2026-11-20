package index

import (
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/lazyoracle/mail-export-viewer/internal/testutil"
)

var perfWords = strings.Fields(`budget meeting project report invoice travel schedule review design
	release quarterly lunch contract proposal update feedback agenda minutes deadline holiday
	conference training security incident customer support payment shipping order delivery
	newsletter weekly summary planning roadmap launch analytics hiring interview offer welcome`)

// perfCorpus writes n synthetic messages into a few mbox files under dir
// (Gmail labels, Thunderbird status/keys, threads, attachments, CJK).
// Subjects use perfWords; bodies draw Zipf-distributed words from a 20k
// vocabulary whose most frequent words are "the", "meeting", "report".
func perfCorpus(t testing.TB, dir string, n int) int64 {
	rng := rand.New(rand.NewSource(1))
	vocab := make([]string, 20000)
	for i := range vocab {
		vocab[i] = fmt.Sprintf("w%c%d", 'a'+i%26, i)
	}
	vocab[0], vocab[1], vocab[2] = "the", "meeting", "report"
	zipf := rand.NewZipf(rng, 1.1, 2, uint64(len(vocab)-1))
	files := 8
	var total int64
	for f := range files {
		var msgs []string
		for i := f; i < n; i += files {
			w := func() string { return perfWords[rng.Intn(len(perfWords))] }
			subj := fmt.Sprintf("%s %s %d", w(), w(), i/4)
			hdr := fmt.Sprintf("From: Person %d <p%d@example.com>\nTo: Team <team%d@example.org>\n"+
				"Subject: %s\nDate: %s\nMessage-ID: <m%d@example.com>\n",
				i%500, i%500, i%20, subj, time.Unix(1.5e9+int64(i)*600, 0).UTC().Format(time.RFC1123Z), i)
			if i%4 != 0 {
				hdr += fmt.Sprintf("In-Reply-To: <m%d@example.com>\nReferences: <m%d@example.com>\n", i-1, i-1)
			}
			if f < 4 {
				labels := []string{"Inbox", "Work", "Personal", "Receipts", "Travel"}
				hdr += "X-Gmail-Labels: " + labels[i%5]
				if i%3 == 0 {
					hdr += ",Unread"
				}
				hdr += "\n"
			} else {
				hdr += fmt.Sprintf("X-Mozilla-Status: %04x\n", 1+(i%2)*4)
				if i%7 == 0 {
					hdr += "X-Mozilla-Keys: $label1\n"
				}
			}
			var body strings.Builder
			for range 60 + rng.Intn(120) {
				body.WriteString(vocab[zipf.Uint64()])
				body.WriteByte(' ')
			}
			if i%97 == 0 {
				body.WriteString("会议安排 ")
			}
			msg := hdr
			if i%10 == 0 {
				msg += "MIME-Version: 1.0\nContent-Type: multipart/mixed; boundary=B\n\n--B\nContent-Type: text/plain\n\n" +
					body.String() + "\n--B\nContent-Type: application/pdf\nContent-Disposition: attachment; filename=\"doc" +
					fmt.Sprint(i) + ".pdf\"\nContent-Transfer-Encoding: base64\n\nJVBERi0xLjQK\n--B--\n"
			} else {
				msg += "\n" + body.String() + "\n"
			}
			msgs = append(msgs, msg)
		}
		p := testutil.WriteMbox(t, filepath.Join(dir, fmt.Sprintf("box%d.mbox", f)), msgs...)
		fi, _ := os.Stat(p)
		total += fi.Size()
	}
	return total
}

// TestPerf50k indexes 50k synthetic messages through the real mbox source
// and logs indexing throughput and query latencies. Run with
// MEV_PERF=1 go test -run Perf -v ./internal/index/ (MEV_PERF_N overrides n).
func TestPerf50k(t *testing.T) {
	if os.Getenv("MEV_PERF") == "" {
		t.Skip("set MEV_PERF=1")
	}
	n := 50000
	fmt.Sscan(os.Getenv("MEV_PERF_N"), &n)
	dir := t.TempDir()
	start := time.Now()
	size := perfCorpus(t, dir, n)
	t.Logf("corpus: %d messages, %.1f MiB in %v", n, float64(size)/(1<<20), time.Since(start))
	srcs := openAll(t, dir)
	db := filepath.Join(t.TempDir(), "index.db")
	s := openAt(t, db, Options{})
	start = time.Now()
	p := index(t, s, list(srcs)...)
	el := time.Since(start)
	fi, _ := os.Stat(db)
	t.Logf("index: %d messages in %v = %.0f msg/s, %.1f MiB/s; db %.1f MiB", p.Messages, el.Round(time.Millisecond),
		float64(p.Messages)/el.Seconds(), float64(size)/(1<<20)/el.Seconds(), float64(fi.Size())/(1<<20))
	start = time.Now()
	index(t, s, list(srcs)...)
	t.Logf("no-op reindex: %v", time.Since(start).Round(time.Millisecond))

	queries := []SearchParams{
		{}, {Sort: "date_asc"}, {Sort: "from"}, {Sort: "subject"},
		{Q: "the"}, {Q: "meeting"}, {Q: "meeting", Sort: "relevance"}, {Q: "budget"}, {Q: "budget", Sort: "relevance"},
		{Q: "budget meeting"}, {Q: `"the meeting"`}, {Q: "wq12345"}, {Q: "bud"}, {Q: "conf"}, {Q: "wq1234"}, {Q: "from:p42@example.com"}, {Q: "label:work"}, {Q: "is:unread"},
		{Q: "is:starred"}, {Q: "tag:important"}, {Q: "has:attachment"}, {Q: "filename:doc123"},
		{Q: "-budget"}, {Q: "-the"}, {Q: "budget -meeting"}, {Q: "会议"}, {Q: "after:2017-06 before:2017-07"},
		{Q: "folder:box3"}, {Q: "budget label:inbox is:unread"}, {Q: "zzzznotfound"},
	}
	for _, q := range queries {
		var res SearchResult
		var err error
		times := make([]time.Duration, 5)
		for i := range times {
			st := time.Now()
			res, err = s.Search(q)
			times[i] = time.Since(st)
			if err != nil {
				t.Fatal(err)
			}
		}
		sort.Slice(times, func(i, j int) bool { return times[i] < times[j] })
		var page2 time.Duration
		if res.NextCursor != "" {
			q2 := q
			q2.Cursor = res.NextCursor
			st := time.Now()
			if _, err := s.Search(q2); err != nil {
				t.Fatal(err)
			}
			page2 = time.Since(st)
		}
		t.Logf("%-40q sort=%-9s total=%6d median=%7.2fms page2=%6.2fms", q.Q, q.Sort, res.Total,
			float64(times[2].Microseconds())/1000, float64(page2.Microseconds())/1000)
	}
	st := time.Now()
	if _, err := s.Facets(); err != nil {
		t.Fatal(err)
	}
	t.Logf("facets: %.2fms", float64(time.Since(st).Microseconds())/1000)
	rows := search(t, s, SearchParams{Limit: 1}).Items
	st = time.Now()
	th, _ := s.Thread(rows[0].ID)
	t.Logf("thread(%d msgs): %.2fms", len(th), float64(time.Since(st).Microseconds())/1000)
}

func BenchmarkSearch(b *testing.B) {
	s, _ := searchFixture(b)
	for _, q := range []string{"", "quarterly", "quart", "label:inbox is:unread", "会议"} {
		b.Run(q, func(b *testing.B) {
			for range b.N {
				if _, err := s.Search(SearchParams{Q: q}); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
