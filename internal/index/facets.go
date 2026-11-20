package index

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/lazyoracle/mail-export-viewer/internal/source"
)

// SourceInfo is one indexed source (an input file or eml directory).
type SourceInfo struct {
	ID         int64
	Path, Kind string
	// Complete: the source was fully indexed from its current inputs.
	Complete bool
	// Count and Unread are message counts (Deleted messages excluded
	// unless Options.IncludeDeleted).
	Count, Unread int64
	// WarningCount is the number of scan warnings of the last indexing run
	// of this source; Warnings holds the first of them (bounded).
	WarningCount int64
	Warnings     []string
	// Error is set when the source could not be read at all or failed
	// part-way.
	Error string
}

// Sources returns every indexed source, sorted by path: the persisted
// state as of the last write. For a running index, merge it with Status
// (Progress.Sources, keyed by the same Path) for live state and progress;
// a source not yet reached by a first run is in Status only.
func (s *Store) Sources() ([]SourceInfo, error) {
	rows, err := s.rdb.Query(`SELECT s.id, s.path, s.kind, s.fingerprint <> '', s.warn_count, s.warnings, s.error,
		COALESCE(SUM(f.total), 0), COALESCE(SUM(f.unread), 0), COALESCE(SUM(f.deleted), 0), COALESCE(SUM(f.deleted_unread), 0)
		FROM sources s LEFT JOIN folders f ON f.source_id = s.id GROUP BY s.id ORDER BY s.path`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SourceInfo
	for rows.Next() {
		var si SourceInfo
		var warnings string
		var del, delUnread int64
		if err := rows.Scan(&si.ID, &si.Path, &si.Kind, &si.Complete, &si.WarningCount, &warnings, &si.Error,
			&si.Count, &si.Unread, &del, &delUnread); err != nil {
			return nil, err
		}
		if !s.opts.IncludeDeleted {
			si.Count -= del
			si.Unread -= delUnread
		}
		si.Warnings = splitLines(warnings)
		out = append(out, si)
	}
	return out, rows.Err()
}

// Facets are the counts shown next to the message list: the folder tree
// and the label, category, tag and flag sections. Counts exclude Deleted
// messages unless Options.IncludeDeleted (the "deleted" flag count always
// counts them). Folder counts are live; label/category/tag/flag counts are
// recomputed at the end of an indexing run that changed messages and,
// during it, after large sources (see facetBigSource), so they can lag
// behind a running index.
type Facets struct {
	Folders []FolderNode
	// Labels (Gmail, from mbox/eml sources), Categories (Outlook, from
	// olm/pst sources) and Tags (Thunderbird, with colours), sorted by name
	// case-insensitively.
	Labels, Categories, Tags []TermCount
	// Flags has one entry per flag, in source.Flags bit order.
	Flags []FlagCount
}

// FolderNode is one node of the merged folder tree. Nodes are keyed by
// path, so the same path in several sources is one node with one
// FolderRef per source; intermediate nodes may have none.
type FolderNode struct {
	Name, Path    string
	Count, Unread int64 // messages directly in this folder, over all sources
	Sources       []FolderRef
	Children      []FolderNode
}

// FolderRef is one source's folder at a tree path (SearchParams.FolderID).
type FolderRef struct {
	ID, SourceID           int64
	SourceKind, SourcePath string
	Count, Unread          int64
}

// TermCount is a label, category or tag with its message counts.
type TermCount struct {
	Name, Color   string // Color: tags only ("#RRGGBB" or "")
	Count, Unread int64
}

// FlagCount is the number of messages with a flag set.
type FlagCount struct {
	Flag  source.Flags
	Name  string // Flag.Names()[0]
	Count int64
}

// numFlags is the number of defined source.Flags bits.
const numFlags = 9

// During a run, facets are recounted after a source that wrote at least
// facetBigSource messages, or after any source once facetInterval has
// passed since the last recount (a variable so tests can lower it).
const facetInterval = 10 * time.Second

var facetBigSource int64 = 20000

const setFacetsDirtySQL = `INSERT INTO meta(key, value) VALUES('facets_dirty', 1)
	ON CONFLICT(key) DO UPDATE SET value = 1`

// Facets returns the folder tree and the label, category, tag and flag
// counts.
func (s *Store) Facets() (Facets, error) {
	f := Facets{Labels: []TermCount{}, Categories: []TermCount{}, Tags: []TermCount{}}
	var err error
	if f.Folders, err = s.folderTree(); err != nil {
		return f, err
	}
	countCol, unreadCol := "count", "unread"
	if s.opts.IncludeDeleted {
		countCol, unreadCol = "count_all", "unread_all"
	}
	colors, err := s.tagColors()
	if err != nil {
		return f, err
	}
	rows, err := s.rdb.Query(`SELECT kind, name, ` + countCol + `, ` + unreadCol + ` FROM terms WHERE ` +
		countCol + ` > 0`)
	if err != nil {
		return f, err
	}
	defer rows.Close()
	for rows.Next() {
		var kind int
		var tc TermCount
		if err := rows.Scan(&kind, &tc.Name, &tc.Count, &tc.Unread); err != nil {
			return f, err
		}
		switch kind {
		case kindLabel:
			f.Labels = append(f.Labels, tc)
		case kindCategory:
			f.Categories = append(f.Categories, tc)
		case kindTag:
			tc.Color = colors[tc.Name]
			f.Tags = append(f.Tags, tc)
		}
	}
	if err := rows.Err(); err != nil {
		return f, err
	}
	for _, l := range [][]TermCount{f.Labels, f.Categories, f.Tags} {
		slices.SortFunc(l, func(a, b TermCount) int { return compareNames(a.Name, b.Name) })
	}
	counts := map[int64][2]int64{}
	frows, err := s.rdb.Query(`SELECT flag, count, count_all FROM flag_counts`)
	if err != nil {
		return f, err
	}
	defer frows.Close()
	for frows.Next() {
		var flag int64
		var c [2]int64
		if err := frows.Scan(&flag, &c[0], &c[1]); err != nil {
			return f, err
		}
		counts[flag] = c
	}
	if err := frows.Err(); err != nil {
		return f, err
	}
	for bit := range numFlags {
		fl := source.Flags(1) << bit
		c := counts[int64(fl)][0]
		if s.opts.IncludeDeleted || fl == source.Deleted {
			c = counts[int64(fl)][1]
		}
		f.Flags = append(f.Flags, FlagCount{Flag: fl, Name: fl.Names()[0], Count: c})
	}
	return f, nil
}

func compareNames(a, b string) int {
	if c := strings.Compare(strings.ToLower(a), strings.ToLower(b)); c != 0 {
		return c
	}
	return strings.Compare(a, b)
}

// folderTree builds the merged folder tree from the folders table.
func (s *Store) folderTree() ([]FolderNode, error) {
	rows, err := s.rdb.Query(`SELECT f.id, f.source_id, s.kind, s.path, f.path, f.total, f.unread, f.deleted,
		f.deleted_unread FROM folders f JOIN sources s ON s.id = f.source_id ORDER BY s.path, f.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	type node struct {
		FolderNode
		kids map[string]*node
	}
	root := &node{kids: map[string]*node{}}
	for rows.Next() {
		var ref FolderRef
		var path string
		var del, delUnread int64
		if err := rows.Scan(&ref.ID, &ref.SourceID, &ref.SourceKind, &ref.SourcePath, &path, &ref.Count, &ref.Unread,
			&del, &delUnread); err != nil {
			return nil, err
		}
		if !s.opts.IncludeDeleted {
			ref.Count -= del
			ref.Unread -= delUnread
		}
		n := root
		for _, seg := range strings.Split(path, "/") {
			k := n.kids[seg]
			if k == nil {
				k = &node{kids: map[string]*node{}}
				k.Name = seg
				if n == root {
					k.Path = seg
				} else {
					k.Path = n.Path + "/" + seg
				}
				n.kids[seg] = k
			}
			n = k
		}
		n.Sources = append(n.Sources, ref)
		n.Count += ref.Count
		n.Unread += ref.Unread
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	var build func(n *node) []FolderNode
	build = func(n *node) []FolderNode {
		var out []FolderNode
		for _, k := range n.kids {
			fn := k.FolderNode
			fn.Children = build(k)
			out = append(out, fn)
		}
		slices.SortFunc(out, func(a, b FolderNode) int { return compareNames(a.Name, b.Name) })
		return out
	}
	return build(root), nil
}

// setTagColors records the tag colours of source srcID (its earlier ones
// were deleted with its rows).
func (s *Store) setTagColors(srcID int64, styles []source.TagStyle) error {
	for i, st := range styles {
		name := names([]string{st.Name})
		if len(name) == 0 || st.Color == "" || i >= maxNames {
			continue
		}
		if _, err := s.wdb.Exec(`INSERT INTO tag_colors(source_id, name, color) VALUES(?, ?, ?)
			ON CONFLICT(source_id, name) DO UPDATE SET color = excluded.color`, srcID, name[0], clean(st.Color, 32)); err != nil {
			return err
		}
	}
	return nil
}

// rebuildFacetsIfDirty recounts terms and flags when messages changed since
// the last recount.
func (s *Store) rebuildFacetsIfDirty() error {
	var dirty bool
	if err := s.wdb.QueryRow(`SELECT EXISTS(SELECT 1 FROM meta WHERE key = 'facets_dirty' AND value <> 0)`).Scan(&dirty); err != nil || !dirty {
		return err
	}
	return s.rebuildFacets()
}

// rebuildFacets recounts terms and flag_counts in one transaction, which
// also clears the dirty marker (so an interrupted run never leaves stale
// counts marked clean). Terms without messages are dropped.
func (s *Store) rebuildFacets() error {
	tx, err := s.wdb.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	unread, deleted := int64(source.Unread), int64(source.Deleted)
	stmts := []struct {
		q    string
		args []any
	}{
		{`UPDATE terms SET count = 0, unread = 0, count_all = 0, unread_all = 0`, nil},
		{`WITH c AS (SELECT mt.term_id AS id, count(*) AS n_all, SUM(m.flags & ? <> 0) AS u_all,
				SUM(m.flags & ? = 0) AS n, SUM(m.flags & ? = ?) AS u
			FROM message_terms mt JOIN messages m ON m.id = mt.message_id GROUP BY mt.term_id)
			UPDATE terms SET count = c.n, unread = c.u, count_all = c.n_all, unread_all = c.u_all
			FROM c WHERE terms.id = c.id`, []any{unread, deleted, unread | deleted, unread}},
		{`DELETE FROM terms WHERE count_all = 0`, nil},
		{`DELETE FROM flag_counts`, nil},
	}
	for _, st := range stmts {
		if _, err := tx.Exec(st.q, st.args...); err != nil {
			return err
		}
	}
	var cols []string
	for bit := range numFlags {
		f := int64(1) << bit
		cols = append(cols, fmt.Sprintf("COALESCE(SUM(flags & %d <> 0), 0)", f),
			fmt.Sprintf("COALESCE(SUM(flags & %d = %d), 0)", f|deleted, f))
	}
	vals := make([]int64, 2*numFlags)
	ptrs := make([]any, len(vals))
	for i := range vals {
		ptrs[i] = &vals[i]
	}
	if err := tx.QueryRow(`SELECT ` + strings.Join(cols, ", ") + ` FROM messages`).Scan(ptrs...); err != nil {
		return err
	}
	for bit := range numFlags {
		f := int64(1) << bit
		visible := vals[2*bit+1]
		if f == deleted {
			visible = 0
		}
		if _, err := tx.Exec(`INSERT INTO flag_counts(flag, count, count_all) VALUES(?, ?, ?)`,
			f, visible, vals[2*bit]); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`DELETE FROM meta WHERE key = 'facets_dirty'`); err != nil {
		return err
	}
	return tx.Commit()
}
