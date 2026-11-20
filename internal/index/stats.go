package index

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/lazyoracle/mail-export-viewer/internal/source"
)

// EachStatRow calls fn for every message the default list shows (deleted
// ones only with Options.IncludeDeleted), in no particular order, in one
// pass over the table. Only ID, Date, Flags, HasAttachment, FromName and
// FromEmail are set: what statistics need, without the cost of full rows.
func (s *Store) EachStatRow(ctx context.Context, fn func(Row)) error {
	q := `SELECT id, date_unix, flags, from_name, from_email FROM messages m`
	if s.resolveDeleted(DeletedDefault) == DeletedHide {
		q += ` WHERE ` + deletedCond
	}
	rows, err := s.rdb.QueryContext(ctx, q)
	if err != nil {
		return fmt.Errorf("index: stats: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var r Row
		var date sql.NullInt64
		var flags int64
		if err := rows.Scan(&r.ID, &date, &flags, &r.FromName, &r.FromEmail); err != nil {
			return fmt.Errorf("index: stats: %w", err)
		}
		if date.Valid {
			r.Date = time.Unix(date.Int64, 0).UTC()
		}
		r.Flags = source.Flags(flags)
		r.HasAttachment = r.Flags.Has(source.HasAttachment)
		fn(r)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("index: stats: %w", err)
	}
	return ctx.Err()
}
