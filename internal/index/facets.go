package index

import (
	"strings"
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
	// Error is set when the source could not be read at all.
	Error string
}

// Sources returns every indexed source, sorted by path.
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
		if warnings != "" {
			si.Warnings = strings.Split(warnings, "\n")
		}
		out = append(out, si)
	}
	return out, rows.Err()
}
