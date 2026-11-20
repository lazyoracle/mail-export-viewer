package index

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"sync"

	_ "modernc.org/sqlite" // registers the "sqlite" driver (pure Go, FTS5 built in)
)

// schemaVersion is stored in PRAGMA user_version. A database with another
// non-zero version is a stale cache and is rebuilt from scratch.
const schemaVersion = 1

// ErrNotRegular is returned by Open when the database or lock path is a
// symlink or otherwise not a regular file.
var ErrNotRegular = errors.New("not a regular file (a symlink?); remove it")

// ErrLocked is returned by Open when another Store (in this or another
// process) holds the index.
var ErrLocked = errors.New("index: in use by another instance")

// ErrNotFound is returned when a message id does not exist.
var ErrNotFound = errors.New("index: not found")

// Term kinds (terms.kind).
const (
	kindLabel    = 0 // Gmail label (mbox, eml)
	kindCategory = 1 // Outlook category (olm, pst)
	kindTag      = 2 // Thunderbird tag
)

const schemaSQL = `
CREATE TABLE IF NOT EXISTS sources (
	id          INTEGER PRIMARY KEY,
	path        TEXT NOT NULL UNIQUE,
	kind        TEXT NOT NULL,
	fingerprint TEXT NOT NULL DEFAULT '', -- '' until completely indexed
	warn_count  INTEGER NOT NULL DEFAULT 0,
	warnings    TEXT NOT NULL DEFAULT '', -- newline-separated, at most maxStoredWarnings
	error       TEXT NOT NULL DEFAULT ''
);
CREATE TABLE IF NOT EXISTS folders (
	id             INTEGER PRIMARY KEY,
	source_id      INTEGER NOT NULL,
	path           TEXT NOT NULL,
	total          INTEGER NOT NULL DEFAULT 0,
	unread         INTEGER NOT NULL DEFAULT 0,
	deleted        INTEGER NOT NULL DEFAULT 0,
	deleted_unread INTEGER NOT NULL DEFAULT 0,
	UNIQUE(source_id, path)
);
CREATE TABLE IF NOT EXISTS messages (
	id          INTEGER PRIMARY KEY,
	source_id   INTEGER NOT NULL,
	folder_id   INTEGER NOT NULL,
	loc         TEXT NOT NULL,             -- source.Locator text form
	message_id  TEXT NOT NULL DEFAULT '',
	in_reply_to TEXT NOT NULL DEFAULT '',
	refs        TEXT NOT NULL DEFAULT '',  -- space-separated ids, no angle brackets
	subject     TEXT NOT NULL DEFAULT '',
	from_name   TEXT NOT NULL DEFAULT '',
	from_email  TEXT NOT NULL DEFAULT '',
	to_text     TEXT NOT NULL DEFAULT '',
	cc_text     TEXT NOT NULL DEFAULT '',
	date_unix   INTEGER,                   -- NULL when unknown
	flags       INTEGER NOT NULL DEFAULT 0, -- source.Flags
	att_names   TEXT NOT NULL DEFAULT '',  -- newline-separated
	labels      TEXT NOT NULL DEFAULT '',  -- newline-separated labels or categories
	tags        TEXT NOT NULL DEFAULT '',  -- newline-separated
	snippet     TEXT NOT NULL DEFAULT '',
	parse_error INTEGER NOT NULL DEFAULT 0,
	thread_id   INTEGER                    -- filled by threading
);
CREATE INDEX IF NOT EXISTS messages_folder_date ON messages(folder_id, date_unix);
CREATE INDEX IF NOT EXISTS messages_date ON messages(date_unix);
CREATE INDEX IF NOT EXISTS messages_source ON messages(source_id);
CREATE INDEX IF NOT EXISTS messages_message_id ON messages(message_id);
CREATE INDEX IF NOT EXISTS messages_thread ON messages(thread_id);
-- Labels, categories and tags. Counts are rebuilt after indexing changes
-- (see facets.go); count/unread exclude Deleted messages, *_all do not.
CREATE TABLE IF NOT EXISTS terms (
	id         INTEGER PRIMARY KEY,
	kind       INTEGER NOT NULL,
	name       TEXT NOT NULL,
	color      TEXT NOT NULL DEFAULT '',
	count      INTEGER NOT NULL DEFAULT 0,
	unread     INTEGER NOT NULL DEFAULT 0,
	count_all  INTEGER NOT NULL DEFAULT 0,
	unread_all INTEGER NOT NULL DEFAULT 0,
	UNIQUE(kind, name)
);
CREATE TABLE IF NOT EXISTS message_terms (
	term_id    INTEGER NOT NULL,
	message_id INTEGER NOT NULL,
	PRIMARY KEY(term_id, message_id)
) WITHOUT ROWID;
CREATE INDEX IF NOT EXISTS message_terms_message ON message_terms(message_id);
-- Messages per flag bit, rebuilt with the term counts.
CREATE TABLE IF NOT EXISTS flag_counts (
	flag      INTEGER PRIMARY KEY,
	count     INTEGER NOT NULL,
	count_all INTEGER NOT NULL
);
-- Format-specific properties per message (JSON [[key, value], ...]).
CREATE TABLE IF NOT EXISTS props (
	message_id INTEGER PRIMARY KEY,
	data       TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS meta (
	key   TEXT PRIMARY KEY,
	value INTEGER NOT NULL
) WITHOUT ROWID;
CREATE VIRTUAL TABLE IF NOT EXISTS messages_fts USING fts5(
	subject, from_text, to_text, body, att_names,
	tokenize = 'unicode61 remove_diacritics 2'
);
`

var allTables = []string{"messages_fts", "messages", "sources", "folders", "terms", "message_terms",
	"flag_counts", "props", "meta"}

// sortIndexSQL creates the indexes behind the sender and subject sort orders
// (they must match the ORDER BY expressions in search.go). Maintaining them
// row by row slows indexing a lot, so IndexSources drops them before
// writing and builds them in one pass afterwards.
const sortIndexSQL = `
CREATE INDEX IF NOT EXISTS messages_from_sort ON messages(COALESCE(NULLIF(from_name, ''), from_email) COLLATE NOCASE, id);
CREATE INDEX IF NOT EXISTS messages_subject_sort ON messages(subject COLLATE NOCASE, id);
`

const dropSortIndexSQL = `
DROP INDEX IF EXISTS messages_from_sort;
DROP INDEX IF EXISTS messages_subject_sort;
`

// Options configure a Store.
type Options struct {
	// IncludeDeleted lists messages flagged Deleted (expunged-not-compacted
	// Thunderbird mail, Gmail Trash, Outlook Deleted Items flags) like any
	// other. By default they are hidden from lists, searches and counts,
	// unless a query asks for is:deleted.
	IncludeDeleted bool
}

// Store is an open index. Writes go through a single connection (wdb);
// reads use a separate query-only pool (rdb) so the UI can query while
// indexing runs (WAL mode). An exclusive lock file "<db>.lock" keeps other
// Stores off the same database until Close.
type Store struct {
	wdb, rdb *sql.DB
	lock     *fileLock
	opts     Options

	mu     sync.Mutex // guards status
	status Progress
}

func dsn(path string, pragmas ...string) string {
	q := url.Values{}
	for _, p := range pragmas {
		q.Add("_pragma", p)
	}
	return "file:" + (&url.URL{Path: path}).EscapedPath() + "?" + q.Encode()
}

// CachePath returns the index database path for input (a file or directory
// given on the command line) inside cacheDir: a hash of the cleaned
// absolute path, so the name reveals nothing about the input.
func CachePath(cacheDir, input string) (string, error) {
	abs, err := filepath.Abs(input)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(filepath.Clean(abs)))
	return filepath.Join(cacheDir, "index-"+hex.EncodeToString(sum[:12])+".db"), nil
}

// Open opens (creating if needed) the index at dbPath and takes its lock
// (ErrLocked if another Store holds it). A database written by another
// schema version, or one SQLite reports corrupt, is wiped and recreated:
// it is only a cache.
//
// The index holds mail metadata and full text, so the database (and its
// WAL, shared-memory and lock files) is kept private to the user (mode
// 0600), and a symlink at any of these paths is refused rather than
// followed.
func Open(dbPath string, opts Options) (*Store, error) {
	lock, err := acquireLock(dbPath + ".lock")
	if err != nil {
		return nil, err
	}
	s, err := openDB(dbPath)
	if err != nil && isCorrupt(err) {
		if err = wipe(dbPath); err == nil {
			s, err = openDB(dbPath)
		}
	}
	if err != nil {
		lock.release()
		return nil, err
	}
	s.lock, s.opts = lock, opts
	return s, nil
}

func openDB(dbPath string) (*Store, error) {
	// Create the file ourselves so SQLite never creates it with the umask
	// default; it gives the -wal and -shm files the database file's mode.
	if err := openPrivate(dbPath, true); err != nil {
		return nil, err
	}
	wdb, err := sql.Open("sqlite", dsn(dbPath,
		"busy_timeout(10000)", "journal_mode(WAL)", "synchronous(NORMAL)", "cache_size(-32000)"))
	if err != nil {
		return nil, err
	}
	wdb.SetMaxOpenConns(1)
	wdb.SetConnMaxLifetime(0)
	if err := initSchema(wdb); err != nil {
		wdb.Close()
		return nil, fmt.Errorf("index: open %s: %w", dbPath, err)
	}
	rdb, err := sql.Open("sqlite", dsn(dbPath, "busy_timeout(10000)", "query_only(1)", "cache_size(-16000)"))
	if err != nil {
		wdb.Close()
		return nil, err
	}
	rdb.SetMaxOpenConns(max(4, runtime.NumCPU()))
	if err := rdb.Ping(); err != nil {
		wdb.Close()
		rdb.Close()
		return nil, err
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		openPrivate(dbPath+suffix, false) // best effort: they may not exist yet
	}
	return &Store{wdb: wdb, rdb: rdb}, nil
}

func initSchema(db *sql.DB) error {
	var v int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&v); err != nil {
		return err
	}
	if v != 0 && v != schemaVersion {
		for _, t := range allTables {
			if _, err := db.Exec(`DROP TABLE IF EXISTS ` + t); err != nil {
				return err
			}
		}
	}
	if _, err := db.Exec(schemaSQL); err != nil {
		return err
	}
	_, err := db.Exec(fmt.Sprintf(`PRAGMA user_version = %d`, schemaVersion))
	return err
}

// isCorrupt reports SQLITE_CORRUPT or SQLITE_NOTADB.
func isCorrupt(err error) bool {
	var ce interface{ Code() int }
	if !errors.As(err, &ce) {
		return false
	}
	switch ce.Code() & 0xff {
	case 11, 26: // SQLITE_CORRUPT, SQLITE_NOTADB
		return true
	}
	return false
}

// wipe removes the database and its WAL and shared-memory files (a symlink
// is removed itself, never followed).
func wipe(dbPath string) error {
	for _, p := range []string{dbPath, dbPath + "-wal", dbPath + "-shm"} {
		if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("index: remove corrupt cache: %w", err)
		}
	}
	return nil
}

// Reset forgets everything indexed (for a forced re-index), keeping the
// database file and the lock. It must not run concurrently with
// IndexSources.
func (s *Store) Reset() error {
	for _, t := range allTables {
		if _, err := s.wdb.Exec(`DROP TABLE IF EXISTS ` + t); err != nil {
			return err
		}
	}
	if _, err := s.wdb.Exec(schemaSQL); err != nil {
		return err
	}
	s.mu.Lock()
	s.status = Progress{}
	s.mu.Unlock()
	return nil
}

// Close closes the store and releases its lock. It must not be called
// while IndexSources runs (cancel its context and wait for it first).
func (s *Store) Close() error {
	err := errors.Join(s.rdb.Close(), s.wdb.Close())
	s.lock.release()
	return err
}

// openPrivate opens path without following a symlink (creating it when
// create is set), checks that it is a regular file and sets mode 0600 on the
// open file.
func openPrivate(path string, create bool) error {
	f, err := openPrivateFile(path, create)
	if err != nil {
		return err
	}
	return f.Close()
}

func openPrivateFile(path string, create bool) (*os.File, error) {
	flags := os.O_RDWR | oNoFollow
	if create {
		flags |= os.O_CREATE
	}
	f, err := os.OpenFile(path, flags, 0o600)
	if err != nil {
		if isSymlinkErr(err) {
			return nil, fmt.Errorf("index: %s is %w", path, ErrNotRegular)
		}
		return nil, fmt.Errorf("index: open %s: %w", path, err)
	}
	fi, err := f.Stat()
	if err == nil && !fi.Mode().IsRegular() {
		err = fmt.Errorf("index: %s is %w", path, ErrNotRegular)
	}
	if err == nil {
		err = f.Chmod(0o600)
	}
	if err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}
