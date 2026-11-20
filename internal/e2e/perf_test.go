package e2e

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"math/rand/v2"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/lazyoracle/mail-export-viewer/internal/testutil/corpus"
)

// TestPerf measures the built binary on a generated corpus:
//
//	go run ./scripts/gencorpus -out tests/tmp/perf -sets bulk -bulk 100000 -thread 5000 -labels 3000
//	MEV_PERF=$PWD/tests/tmp/perf go test ./internal/e2e -run TestPerf -v -timeout 2h
//
// It reports indexing time, peak RSS (Linux), cache size, restart time and
// p50/p95 latencies of list, search, detail, thread, stats and facets.
// MEV_PERF_REPS sets the repetitions per query (default 20).
func TestPerf(t *testing.T) {
	in := os.Getenv("MEV_PERF")
	if in == "" {
		t.Skip("set MEV_PERF to a corpus directory (see the comment)")
	}
	var m corpus.Manifest
	b, err := os.ReadFile(filepath.Clean(in) + ".manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	reps := 20
	if n, err := strconv.Atoi(os.Getenv("MEV_PERF_REPS")); err == nil && n > 0 {
		reps = n
	}
	home := t.TempDir()
	makeEnv(t, home)
	t0 := time.Now()
	v := start(t, home, in)
	indexTime := time.Since(t0)
	peak := peakRSS(v.cmd.Process.Pid)
	cache := dirSize(t, filepath.Join(home, "cache"))
	var st struct{ Indexing struct{ Messages int64 } }
	v.getJSON("/api/status", &st)
	t.Logf("machine: %s/%s, %d CPUs", runtime.GOOS, runtime.GOARCH, runtime.NumCPU())
	t.Logf("corpus: %d messages, %.1f MiB", len(m.Messages), float64(m.Bytes)/(1<<20))
	t.Logf("indexing: %d messages in %v (%.0f msg/s, %.1f MiB/s), peak RSS %s, cache %.1f MiB",
		st.Indexing.Messages, indexTime.Round(time.Millisecond), float64(st.Indexing.Messages)/indexTime.Seconds(),
		float64(m.Bytes)/(1<<20)/indexTime.Seconds(), peak, float64(cache)/(1<<20))

	// Pick sample ids and a long-thread member.
	var page apiList
	v.getJSON("/api/messages?limit=1000", &page)
	r := rand.New(rand.NewPCG(1, 2))
	var long int64
	var l apiList
	v.getJSON("/api/messages?"+q("q", `subject:"Endless thread"`, "limit", "1"), &l)
	if len(l.Items) > 0 {
		long = l.Items[0].ID
	}
	type bench struct{ name, path string }
	var benches []bench
	add := func(name string, params ...string) {
		benches = append(benches, bench{name, "/api/messages?" + q(append([]string{"limit", "50"}, params...)...)})
	}
	add("list date_desc (page 1)")
	v.getJSON("/api/messages?limit=50", &l)
	add("list date_desc (page 2)", "cursor", l.NextCursor)
	add("list date_asc", "sort", "date_asc")
	add("list from", "sort", "from")
	add("list subject", "sort", "subject")
	add("list folder", "folder", "bulk/takeout/box3")
	add("term: "+corpus.BulkWord, "q", corpus.BulkWord)
	add("term relevance: "+corpus.BulkWord, "q", corpus.BulkWord, "sort", "relevance")
	add("prefix: budg", "q", "budg")
	add("phrase", "q", `"`+corpus.BulkPhrase+`"`)
	add("rare token", "q", m.Messages[len(m.Messages)/2].Token)
	add("no hit", "q", "qzznohitword")
	add("common word", "q", m.CommonWord)
	add("from:", "q", "from:alice@example.com")
	add("label:", "q", `label:"Label 7"`)
	add("category:", "q", `category:"Bulk"`)
	add("tag:", "q", `tag:"Important"`)
	add("folder:", "q", "folder:box3")
	add("is:unread", "q", "is:unread")
	add("is:starred", "q", "is:starred")
	add("has:attachment", "q", "has:attachment")
	add("filename:", "q", "filename:doc123")
	add("before:/after:", "q", "after:2013 before:2014")
	add("-budget", "q", "-budget")
	add("-is:unread", "q", "-is:unread")
	add("-label:", "q", `-label:"Label 7"`)
	add("budget -is:unread", "q", "budget -is:unread")
	add("budget label: is:unread", "q", `budget label:"Label 7" is:unread`)
	add("CJK substring", "q", "会议")
	add("deleted=include is:unread", "q", "is:unread", "deleted", "include")
	for i := 0; i < 5; i++ {
		id := page.Items[r.IntN(len(page.Items))].ID
		benches = append(benches, bench{"detail", fmt.Sprintf("/api/messages/%d", id)})
	}
	if long != 0 {
		benches = append(benches, bench{"thread (long chain)", fmt.Sprintf("/api/threads/%d", long)})
	}
	benches = append(benches, bench{"facets", "/api/facets"}, bench{"status", "/api/status"})

	// Stats: cold (first) call, then warm.
	ts := time.Now()
	v.get("/api/stats")
	t.Logf("stats cold: %v", time.Since(ts).Round(time.Millisecond))
	benches = append(benches, bench{"stats (warm)", "/api/stats"})

	results := map[string][]time.Duration{}
	var order []string
	totals := map[string]int64{}
	for _, b := range benches {
		if _, ok := results[b.name]; !ok {
			order = append(order, b.name)
		}
		for i := 0; i < reps; i++ {
			s := time.Now()
			resp, body := v.get(b.path)
			el := time.Since(s)
			if resp.StatusCode != 200 {
				t.Errorf("%s: %d %s", b.name, resp.StatusCode, trunc(string(body), 200))
				break
			}
			results[b.name] = append(results[b.name], el)
			if i == 0 && strings.HasPrefix(b.path, "/api/messages?") {
				var l apiList
				json.Unmarshal(body, &l)
				totals[b.name] = l.Total
			}
		}
	}
	var sb strings.Builder
	sb.WriteString("\n| request | hits | p50 ms | p95 ms |\n|---|---:|---:|---:|\n")
	for _, name := range order {
		d := results[name]
		sort.Slice(d, func(i, j int) bool { return d[i] < d[j] })
		if len(d) == 0 {
			continue
		}
		hits := ""
		if n, ok := totals[name]; ok {
			hits = strconv.FormatInt(n, 10)
		}
		fmt.Fprintf(&sb, "| %s | %s | %.1f | %.1f |\n", name, hits, ms(d[len(d)/2]), ms(d[(len(d)*95+99)/100-1]))
	}
	t.Log(sb.String())

	// Restart on the warm cache.
	v.stop()
	t1 := time.Now()
	v2 := start(t, home, in)
	t.Logf("restart with a warm cache: ready and indexed in %v, peak RSS %s", time.Since(t1).Round(time.Millisecond), peakRSS(v2.cmd.Process.Pid))
	v2.stop()
}

func ms(d time.Duration) float64 { return float64(d.Microseconds()) / 1000 }

// peakRSS reads VmHWM of pid (Linux); "n/a" elsewhere.
func peakRSS(pid int) string {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", pid))
	if err != nil {
		return "n/a"
	}
	for _, line := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(line, "VmHWM:") {
			f := strings.Fields(line)
			if len(f) >= 2 {
				kb, _ := strconv.Atoi(f[1])
				return fmt.Sprintf("%d MiB", kb/1024)
			}
		}
	}
	return "n/a"
}

func dirSize(t *testing.T, root string) int64 {
	var n int64
	filepath.WalkDir(root, func(_ string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			if fi, err := d.Info(); err == nil {
				n += fi.Size()
			}
		}
		return nil
	})
	return n
}
