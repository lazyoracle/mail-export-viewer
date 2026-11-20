package server

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/lazyoracle/mail-export-viewer/internal/index"
	"github.com/lazyoracle/mail-export-viewer/internal/source"
)

// Statistics (from earlier-tool): counts by year and month, top senders,
// plus per folder and per source counts. They need one pass over every
// listed message, so they are cached until the index changes (and, while
// indexing runs, recomputed at most every statsRefresh).

const (
	topSendersN  = 20
	topFoldersN  = 20
	statsPage    = 1000
	statsRefresh = 30 * time.Second
)

type yearCount struct {
	Year  int   `json:"year"`
	Count int64 `json:"count"`
}

type monthCount struct {
	Month string `json:"month"` // YYYY-MM
	Count int64  `json:"count"`
}

type senderCount struct {
	Name  string `json:"name"`
	Email string `json:"email"`
	Count int64  `json:"count"`
}

type folderCount struct {
	Path  string `json:"path"`
	Count int64  `json:"count"`
}

type sourceCount struct {
	ID    int64  `json:"id"`
	Name  string `json:"name"`
	Kind  string `json:"kind"`
	Count int64  `json:"count"`
}

type statsJSON struct {
	Total           int64         `json:"total"`
	Undated         int64         `json:"undated"`
	OutOfRange      int64         `json:"outOfRange"` // dated before 1980 or over a year ahead
	Unread          int64         `json:"unread"`
	WithAttachments int64         `json:"withAttachments"`
	ByYear          []yearCount   `json:"byYear"`
	ByMonth         []monthCount  `json:"byMonth"`
	TopSenders      []senderCount `json:"topSenders"`
	ByFolder        []folderCount `json:"byFolder"`
	BySource        []sourceCount `json:"bySource"`
	Partial         bool          `json:"partial"` // computed while indexing ran
	ComputedAt      string        `json:"computedAt"`
}

// statsAcc accumulates statsJSON over rows. Years and months are taken in
// UTC; implausible dates (broken clocks, mangled headers) would stretch the
// charts, so they are only counted in OutOfRange.
type statsAcc struct {
	st      statsJSON
	years   map[int]int64
	months  map[string]int64
	senders map[string]*senderCount // keyed by lower-cased address (or name)
	minDate time.Time
	maxDate time.Time
}

func (a *statsAcc) add(r index.Row) {
	if a.years == nil {
		a.years, a.months, a.senders = map[int]int64{}, map[string]int64{}, map[string]*senderCount{}
		a.minDate = time.Date(1980, 1, 1, 0, 0, 0, 0, time.UTC)
		a.maxDate = time.Now().AddDate(0, 0, 366)
	}
	a.st.Total++
	if r.Flags.Has(source.Unread) {
		a.st.Unread++
	}
	if r.HasAttachment {
		a.st.WithAttachments++
	}
	d := r.Date.UTC()
	switch {
	case r.Date.IsZero():
		a.st.Undated++
	case d.Before(a.minDate) || d.After(a.maxDate):
		a.st.OutOfRange++
	default:
		a.years[d.Year()]++
		a.months[fmt.Sprintf("%04d-%02d", d.Year(), int(d.Month()))]++
	}
	key := strings.ToLower(r.FromEmail)
	if key == "" {
		key = strings.ToLower(r.FromName)
	}
	if key == "" {
		return
	}
	sd := a.senders[key]
	if sd == nil {
		if len(a.senders) >= maxSenders {
			return
		}
		sd = &senderCount{Email: r.FromEmail}
		a.senders[key] = sd
	}
	sd.Count++
	if r.FromName != "" {
		sd.Name = r.FromName
	}
}

// maxSenders bounds the distinct senders tracked (memory on huge archives);
// later new senders are not counted.
const maxSenders = 200000

func (a *statsAcc) result() statsJSON {
	st := a.st
	st.ByYear, st.ByMonth, st.TopSenders = []yearCount{}, []monthCount{}, []senderCount{}
	for y, c := range a.years {
		st.ByYear = append(st.ByYear, yearCount{y, c})
	}
	sort.Slice(st.ByYear, func(i, j int) bool { return st.ByYear[i].Year < st.ByYear[j].Year })
	for m, c := range a.months {
		st.ByMonth = append(st.ByMonth, monthCount{m, c})
	}
	sort.Slice(st.ByMonth, func(i, j int) bool { return st.ByMonth[i].Month < st.ByMonth[j].Month })
	for _, sd := range a.senders {
		st.TopSenders = append(st.TopSenders, *sd)
	}
	sort.Slice(st.TopSenders, func(i, j int) bool {
		x, y := st.TopSenders[i], st.TopSenders[j]
		if x.Count != y.Count {
			return x.Count > y.Count
		}
		return strings.ToLower(x.Email+"\x00"+x.Name) < strings.ToLower(y.Email+"\x00"+y.Name)
	})
	if len(st.TopSenders) > topSendersN {
		st.TopSenders = st.TopSenders[:topSendersN]
	}
	return st
}

// statsCache holds the last statistics and the index state they were
// computed from.
type statsCache struct {
	mu  sync.Mutex
	sig string
	at  time.Time
	st  *statsJSON
}

// statsSignature identifies the index content: per-source counts and
// completeness, plus whether a run is going and how far it got.
func statsSignature(p index.Progress, infos []index.SourceInfo) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%v/%d;", p.Running, p.Messages)
	for _, si := range infos {
		fmt.Fprintf(&b, "%d:%d:%v:%d;", si.ID, si.Count, si.Complete, si.Unread)
	}
	return b.String()
}

func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	st, err := s.statistics(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	s.writeJSON(w, st)
}

// statistics returns cached statistics, recomputing them when the index
// changed (at most every statsRefresh while indexing runs). Concurrent
// callers wait for one computation.
func (s *Server) statistics(ctx context.Context) (*statsJSON, error) {
	infos, err := s.st.Sources()
	if err != nil {
		return nil, err
	}
	p := s.st.Status()
	sig := statsSignature(p, infos)
	c := &s.stats
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.st != nil && (c.sig == sig || p.Running && time.Since(c.at) < statsRefresh) {
		return c.st, nil
	}
	var acc statsAcc
	cursor := ""
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		res, err := s.st.Search(index.SearchParams{Sort: "date_asc", Limit: statsPage, Cursor: cursor})
		if err != nil {
			return nil, err
		}
		for _, row := range res.Items {
			acc.add(row)
		}
		if res.NextCursor == "" {
			break
		}
		cursor = res.NextCursor
	}
	st := acc.result()
	st.ByFolder, st.BySource = []folderCount{}, []sourceCount{}
	if fc, err := s.st.Facets(); err == nil {
		var walk func([]index.FolderNode)
		walk = func(ns []index.FolderNode) {
			for _, n := range ns {
				if n.Count > 0 {
					st.ByFolder = append(st.ByFolder, folderCount{n.Path, n.Count})
				}
				walk(n.Children)
			}
		}
		walk(fc.Folders)
	} else {
		return nil, err
	}
	sort.SliceStable(st.ByFolder, func(i, j int) bool { return st.ByFolder[i].Count > st.ByFolder[j].Count })
	if len(st.ByFolder) > topFoldersN {
		st.ByFolder = st.ByFolder[:topFoldersN]
	}
	for _, si := range infos {
		st.BySource = append(st.BySource, sourceCount{ID: si.ID, Name: s.sourceName(si.Path), Kind: si.Kind, Count: si.Count})
	}
	st.Partial = p.Running
	now := time.Now()
	st.ComputedAt = now.UTC().Format(time.RFC3339Nano)
	c.sig, c.at, c.st = sig, now, &st
	return &st, nil
}
