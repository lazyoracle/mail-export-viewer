package server

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lazyoracle/mail-export-viewer/internal/index"
)

func TestStatsEndpoint(t *testing.T) {
	f := newFixture(t)
	var st statsJSON
	rec := f.getJSON("/api/stats", &st)
	noPaths(t, rec.Body.String(), f.dir)
	if st.Total != 9 || st.Undated != 0 || st.OutOfRange != 0 || st.Unread != 1 || st.WithAttachments != 2 || st.Partial {
		t.Fatalf("stats = %+v", st)
	}
	if len(st.ByYear) != 1 || st.ByYear[0] != (yearCount{2023, 9}) {
		t.Errorf("byYear = %+v", st.ByYear)
	}
	if fmt.Sprint(st.ByMonth) != "[{2023-01 3} {2023-02 5} {2023-03 1}]" {
		t.Errorf("byMonth = %+v", st.ByMonth)
	}
	if len(st.TopSenders) != 9 || st.TopSenders[0].Count != 1 {
		t.Errorf("topSenders = %+v", st.TopSenders)
	}
	if fmt.Sprint(st.ByFolder) != "[{Archive 5} {Inbox 3} {backup/Inbox 1}]" {
		t.Errorf("byFolder = %+v", st.ByFolder)
	}
	bySource := map[string]int64{}
	for _, s := range st.BySource {
		bySource[s.Name] = s.Count
	}
	if bySource["Inbox.mbox"] != 3 || bySource["Archive"] != 5 || bySource["backup.olm"] != 1 {
		t.Errorf("bySource = %+v", st.BySource)
	}
}

func TestStatsSendersAndDates(t *testing.T) {
	dir := t.TempDir()
	future := time.Now().AddDate(3, 0, 0).UTC().Format(time.RFC1123Z)
	writeMbox(t, filepath.Join(dir, "Box"),
		"From: Ann <ANN@example.com>\nSubject: a\nDate: Mon, 2 Jan 2023 10:00:00 +0000\n\nx\n",
		"From: Annie <ann@example.com>\nSubject: b\nDate: Mon, 2 Jan 2024 10:00:00 +0000\n\nx\n",
		"From: Bob <bob@example.com>\nSubject: c\nDate: Mon, 2 Jan 1975 10:00:00 +0000\n\nx\n",
		"From: Bob <bob@example.com>\nSubject: d\nDate: "+future+"\n\nx\n",
		"From: Cy <cy@example.com>\nSubject: e\n\nundated\n")
	f := fixtureFor(t, dir)
	var st statsJSON
	f.getJSON("/api/stats", &st)
	// The header-less message gets its mbox From_ line date (2023 here).
	if st.Total != 5 || st.OutOfRange != 2 || fmt.Sprint(st.ByYear) != "[{2023 2} {2024 1}]" {
		t.Errorf("stats = %+v", st)
	}
	// Undated messages are counted by TestComputeStatsUndated.
	if len(st.TopSenders) != 3 || st.TopSenders[0].Email != "ann@example.com" && st.TopSenders[0].Email != "ANN@example.com" ||
		st.TopSenders[0].Count != 2 || st.TopSenders[1].Email != "bob@example.com" {
		t.Errorf("senders = %+v", st.TopSenders)
	}
}

func TestStatsCachedUntilIndexChanges(t *testing.T) {
	f := newFixture(t)
	var a, b statsJSON
	f.getJSON("/api/stats", &a)
	f.getJSON("/api/stats", &b)
	if a.ComputedAt != b.ComputedAt {
		t.Errorf("not cached: %s vs %s", a.ComputedAt, b.ComputedAt)
	}
	arch := filepath.Join(f.dir, "Archive")
	data, err := os.ReadFile(arch)
	if err != nil {
		t.Fatal(err)
	}
	extra := "\nFrom x Mon Mar 6 10:00:00 2023\nFrom: New <new@example.com>\nSubject: Added later\nDate: Mon, 6 Mar 2023 10:00:00 +0000\n\nnew\n\n"
	if err := os.WriteFile(arch, append(data, extra...), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := f.st.IndexSources(context.Background(), f.srcs, nil); err != nil {
		t.Fatal(err)
	}
	f.getJSON("/api/stats", &b)
	if b.Total != a.Total+1 {
		t.Errorf("total after change = %d, want %d", b.Total, a.Total+1)
	}
}

func TestComputeStatsUndated(t *testing.T) {
	rows := []index.Row{{FromEmail: "a@example.com"}, {FromName: "No Address"}, {}}
	var acc statsAcc
	for _, r := range rows {
		acc.add(r)
	}
	st := acc.result()
	if st.Total != 3 || st.Undated != 3 || len(st.TopSenders) != 2 {
		t.Fatalf("stats = %+v", st)
	}
}
