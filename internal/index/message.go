package index

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/lazyoracle/mail-export-viewer/internal/source"
)

// Tag is a Thunderbird tag with its colour ("#RRGGBB", or "" for the
// default).
type Tag struct{ Name, Color string }

// Row is the list-view data of one message: everything a message list row
// shows.
type Row struct {
	ID, SourceID, FolderID int64
	// ThreadID is 0 until threading has run (the smallest message id of the
	// thread afterwards).
	ThreadID int64
	// SourceKind is the format of the message's source ("mbox", "olm",
	// ...); Folder is the folder path (Entry.Folder).
	SourceKind, Folder           string
	Subject, FromName, FromEmail string
	// To is the formatted To list ("Name <addr>, ...").
	To      string
	Snippet string
	Date    time.Time // zero when unknown
	Flags   source.Flags
	// HasAttachment mirrors Flags.Has(source.HasAttachment).
	HasAttachment bool
	// Labels are Gmail labels (mbox, eml sources); Categories are Outlook
	// categories (olm, pst sources); Tags are Thunderbird tags.
	Labels, Categories []string
	Tags               []Tag
}

// Message is a Row plus what the message view needs from the index.
type Message struct {
	Row
	// SourcePath identifies the Source (Source.Path) to Load the message
	// from with Loc.
	SourcePath           string
	Loc                  source.Locator
	MessageID, InReplyTo string
	References           []string
	Cc                   string
	AttachmentNames      []string
	// Props are the format-specific properties (Properties panel).
	Props []source.Prop
	// ParseError: the message could not be summarised (it is listed with
	// what the source provided).
	ParseError bool
}

// isCategoryKind reports whether Entry.Labels of a source of this kind are
// Outlook categories rather than Gmail labels.
func isCategoryKind(kind string) bool { return kind == source.OLM || kind == source.PST }

// rowCols is the column list scanned by scanRow (prefix "m.").
const rowCols = `m.id, m.source_id, m.folder_id, COALESCE(m.thread_id, 0), m.subject, m.from_name, m.from_email,
	m.to_text, m.snippet, m.date_unix, m.flags, m.labels, m.tags`

type scanner interface{ Scan(dest ...any) error }

// scannedRow is a Row before decorate: labels and tags still raw.
type scannedRow struct {
	Row
	labels, tags string
}

func scanRow(r scanner, extra ...any) (scannedRow, error) {
	var m scannedRow
	var date sql.NullInt64
	dest := []any{&m.ID, &m.SourceID, &m.FolderID, &m.ThreadID, &m.Subject, &m.FromName, &m.FromEmail,
		&m.To, &m.Snippet, &date, &m.Flags, &m.labels, &m.tags}
	if err := r.Scan(append(dest, extra...)...); err != nil {
		return m, err
	}
	if date.Valid {
		m.Date = time.Unix(date.Int64, 0).UTC()
	}
	m.HasAttachment = m.Flags.Has(source.HasAttachment)
	return m, nil
}

func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

// decorate fills in folder paths, source kinds, labels/categories and tag
// colours.
func (s *Store) decorate(in []scannedRow) ([]Row, error) {
	out := make([]Row, len(in))
	if len(in) == 0 {
		return out, nil
	}
	type folderInfo struct{ path, kind string }
	folders := map[int64]folderInfo{}
	var args []any
	for _, r := range in {
		if _, ok := folders[r.FolderID]; !ok {
			folders[r.FolderID] = folderInfo{}
			args = append(args, r.FolderID)
		}
	}
	rows, err := s.rdb.Query(`SELECT f.id, f.path, s.kind FROM folders f JOIN sources s ON s.id = f.source_id
		WHERE f.id IN (?`+strings.Repeat(",?", len(args)-1)+`)`, args...)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id int64
		var fi folderInfo
		if err := rows.Scan(&id, &fi.path, &fi.kind); err != nil {
			rows.Close()
			return nil, err
		}
		folders[id] = fi
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	var colors map[string]string
	for i, r := range in {
		fi := folders[r.FolderID]
		row := r.Row
		row.Folder, row.SourceKind = fi.path, fi.kind
		if isCategoryKind(fi.kind) {
			row.Categories = splitLines(r.labels)
		} else {
			row.Labels = splitLines(r.labels)
		}
		for _, t := range splitLines(r.tags) {
			if colors == nil {
				if colors, err = s.tagColors(); err != nil {
					return nil, err
				}
			}
			row.Tags = append(row.Tags, Tag{Name: t, Color: colors[t]})
		}
		out[i] = row
	}
	return out, nil
}

func (s *Store) tagColors() (map[string]string, error) {
	// When sources disagree, the most recently added source wins.
	rows, err := s.rdb.Query(`SELECT name, color FROM tag_colors ORDER BY source_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	m := map[string]string{}
	for rows.Next() {
		var n, c string
		if err := rows.Scan(&n, &c); err != nil {
			return nil, err
		}
		m[n] = c
	}
	return m, rows.Err()
}

// queryRows runs a query selecting rowCols (plus one extra column per
// extra() value, which is collected into the second result).
func (s *Store) queryRows(query string, args []any, extra func() any) ([]Row, []any, error) {
	rows, err := s.rdb.Query(query, args...)
	if err != nil {
		return nil, nil, fmt.Errorf("index: query: %w", err)
	}
	defer rows.Close()
	var out []scannedRow
	var keys []any
	for rows.Next() {
		var ex []any
		if extra != nil {
			k := extra()
			ex = append(ex, k)
			keys = append(keys, k)
		}
		r, err := scanRow(rows, ex...)
		if err != nil {
			return nil, nil, err
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	rows.Close()
	dec, err := s.decorate(out)
	return dec, keys, err
}

// Message returns message id, or ErrNotFound.
func (s *Store) Message(id int64) (Message, error) {
	var m Message
	var loc, refs, atts string
	var props sql.NullString
	row := s.rdb.QueryRow(`SELECT `+rowCols+`, src.path, m.loc, m.message_id, m.in_reply_to, m.refs, m.cc_text,
		m.att_names, m.parse_error, p.data
		FROM messages m JOIN sources src ON src.id = m.source_id LEFT JOIN props p ON p.message_id = m.id
		WHERE m.id = ?`, id)
	sr, err := scanRow(row, &m.SourcePath, &loc, &m.MessageID, &m.InReplyTo, &refs, &m.Cc, &atts,
		&m.ParseError, &props)
	if errors.Is(err, sql.ErrNoRows) {
		return m, ErrNotFound
	}
	if err != nil {
		return m, err
	}
	dec, err := s.decorate([]scannedRow{sr})
	if err != nil {
		return m, err
	}
	m.Row = dec[0]
	if err := m.Loc.UnmarshalText([]byte(loc)); err != nil {
		return m, err
	}
	m.References = strings.Fields(refs)
	m.AttachmentNames = splitLines(atts)
	if props.Valid {
		var kv [][2]string
		if err := json.Unmarshal([]byte(props.String), &kv); err != nil {
			return m, err
		}
		for _, p := range kv {
			m.Props = append(m.Props, source.Prop{Key: p[0], Value: p[1]})
		}
	}
	return m, nil
}

// Location returns where message id lives: the Source path and the locator
// to pass to its Load, Raw or Attachment.
func (s *Store) Location(id int64) (sourcePath string, loc source.Locator, err error) {
	var text string
	err = s.rdb.QueryRow(`SELECT src.path, m.loc FROM messages m JOIN sources src ON src.id = m.source_id
		WHERE m.id = ?`, id).Scan(&sourcePath, &text)
	if errors.Is(err, sql.ErrNoRows) {
		return "", loc, ErrNotFound
	}
	if err == nil {
		err = loc.UnmarshalText([]byte(text))
	}
	return
}
