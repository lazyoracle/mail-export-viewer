// Package index keeps the persistent SQLite (pure Go, modernc.org/sqlite)
// and FTS5 index of every message of every Source: metadata, locators to
// re-read bodies lazily, labels/categories/tags, flags, properties and
// threads. It consumes source.Source generically and never imports a
// format package (tests blank-import mbox and olm to run real sources).
//
// # Use
//
//	db, _ := index.CachePath(cacheDir, input)       // hashed name, under the cache dir
//	st, err := index.Open(db, index.Options{})      // ErrLocked: another instance
//	go st.IndexSources(ctx, srcs, nil)              // poll st.Status() meanwhile
//	res, _ := st.Search(index.SearchParams{Q: "from:alice is:unread"})
//	f, _ := st.Facets()                             // folder tree, labels, categories, tags, flags
//	m, _ := st.Message(id)                          // Row + Loc, Props, ids, Cc
//	msg, _ := srcs[m.SourcePath].Load(m.Loc)        // the server keeps Sources by Path
//
// Reads (Search, Facets, Message, Thread, Sources, Status) are safe while
// IndexSources runs. IndexSources is synchronous and one run at a time; a
// second run, Reset and Close return ErrBusy meanwhile (cancel the run's
// context and wait for IndexSources to return first).
//
// # Schema
//
//   - sources: one row per Source.Path, its kind, the input fingerprint
//     ("" while incomplete), warning count and the first warnings, error.
//   - folders: (source, path) with live counters total/unread/deleted/
//     deleted_unread, maintained by the writer.
//   - messages: list-row data, Locator text, ids for threading, flags
//     (source.Flags bits), labels/tags newline-joined (labels are
//     categories for olm/pst sources), thread_id.
//   - terms + message_terms: labels (kind 0), categories (1), tags (2)
//     for filtering and counts; tag_colors: colours per source and name.
//     Counts (with and without Deleted messages) and flag_counts are
//     recomputed after a run that changed messages (and after large
//     sources during it), guarded by a dirty marker so an interrupted run
//     heals.
//   - meta: dirty markers (facets, threads) and the next message id.
//   - props: per-message JSON [[key, value], ...] (Properties panel).
//   - messages_fts: subject, from, to+cc, body (mail.Summary.Text), and
//     attachment names; unicode61 with diacritics removed.
//
// # Indexing
//
// A source is reused when its fingerprint matches: sha256 over kind, path
// and the sorted, de-duplicated Inputs with each one's size and mtime (a
// missing input counts as size -1), so a changed, added or removed input
// (an eml directory gaining a file, a vanished .msf) re-scans it. The
// fingerprint is taken before Scan, so a file changing during the scan is
// re-scanned next time. Re-scanning replaces all rows of the source. A
// Scan error keeps what was written, records the error and leaves the
// source incomplete (retried next run); a cancelled run does the same.
//
// Scan feeds runtime.NumCPU workers that call Entry.Resolve (recovering
// from panics: the message is listed from what the source gave, flagged
// parse_error, and a warning recorded). A panic in Scan fails the source
// like a Scan error; one in Inputs (never fingerprinted, so always
// re-scanned), Folders or TagStyles is a warning. Entry bytes in flight
// (Raw, or the summary, labels, tags and props for formats that build the
// summary) are bounded by a 64 MiB budget in 64 KiB units, released only
// when a row reaches the batch in scan order, so the reorder buffer is
// bounded too; a writer
// goroutine commits batches (2000 rows or 16 MiB) with multi-row INSERTs
// and explicit ids (the driver re-parses every statement, so one
// statement per row costs far more). Rows are written in scan order. Ids
// are never reused (a persisted counter survives removals and Reset), so
// a stale deep link gives ErrNotFound rather than another message.
// Strings are made valid UTF-8 without the highlight marker runes U+E000
// and U+E001; subject and sender are capped at 1 KiB (they travel in
// paging cursors), other header text at 8 KiB; folders get no empty segments (the source
// file name stands in for an empty folder); labels, tags and attachment
// names are de-duplicated and capped (256 of 512 bytes); props are capped
// at 256 entries / 64 KiB; references at the last 100.
//
// Progress is per source: bytes of Source.Path when entries carry a byte
// range (Loc.Length > 0, i.e. mbox), else message counts.
//
// FolderLister folders are added after a successful Scan, so empty
// folders appear in the tree. Tag colours come from source.TagStyler,
// called after a successful Scan and stored per source (tag_colors), so
// they are replaced when the source is re-indexed and dropped with it;
// when sources disagree on a name, the one indexed first (lowest source
// id) loses to later ones.
//
// # Deleted messages
//
// Messages flagged Deleted are hidden from Search, Thread, counts and
// facets unless Options.IncludeDeleted, except that a query with
// is:deleted shows them, the "deleted" flag count always counts them and
// Thread(id) of a deleted id includes it.
//
// Per request, SearchParams.Deleted (Hide, Include, Only; Default follows
// Options) and ThreadParams.Deleted override that, and FacetsFor counts
// with or without them. The mode is a condition outside the query text,
// so no query (an open quote, the 4 KiB or 64-item caps) can widen
// DeletedOnly. Only and Include count a thread whole. ThreadPage caps a
// thread at a row limit (reporting the full count), always keeping the
// requested message.
//
// # Search
//
// See Query for the syntax. Decisions beyond earlier-tool:
//
//   - folder: (alias mailbox:) matches a folder path or its last
//     segment(s) ("inbox" matches "Inbox" and "backup/Inbox"), not
//     subfolders. The tree scopes are exact instead: SearchParams.Folder
//     is one FolderNode.Path (case-sensitive) in every source, FolderID
//     one source's folder (FolderRef.ID).
//   - label: matches labels and categories, category: only categories,
//     tag: only tags; whole names, ASCII case-insensitive (SQLite NOCASE).
//   - is: takes every source.ParseFlag name or alias plus read/seen;
//     has:attachment is the HasAttachment flag.
//   - cc: (from earlier-tool) narrows by the FTS to/cc column and checks the
//     Cc column; before:/after: accept '/' separators (earlier-tool).
//   - -before:/-after: keep undated messages (before:/after: never match
//     them). -cc:x is exactly the negation of cc:x.
//   - The last plain term is a prefix (3+ runes, not CJK). CJK terms also
//     match as substrings of subject and people (LIKE/instr), not of the
//     body (unicode61 indexes a CJK run as one token).
//   - Without Deleted messages in the index the hidden-deleted filter is
//     left out, which keeps the FTS-only count and index-walk plans.
//
// Ranking is bm25 with subject 10, from 5, to 3, attachment names 2,
// body 1. Highlights are FTS snippets with every character HTML-escaped
// except <mark> tags.
//
// # Threading
//
// As in earlier-tool: union-find over Message-ID/In-Reply-To/References
// with phantom nodes and hub guards, plus same-normalised-subject chains
// within 30 days (at most 25 long, no generic or short subjects). From
// earlier-tool: mailing-list tags ("[dev] ") are stripped with reply
// prefixes in any interleaving. Threads span sources. The thread id is
// the smallest message id of the component. Threading reruns whenever a
// threads_dirty marker (set with every message change, cleared only after
// a complete pass) is present, so an interrupted pass heals. ThreadCounts
// count visible messages, plus the row itself for a hidden deleted row
// shown by is:deleted.
//
// # Cache files
//
// Open creates a missing cache directory 0700. The database, its
// -wal/-shm (checked before SQLite opens them) and the "<db>.lock" file
// are created 0600 and opened with O_NOFOLLOW (a symlink is refused:
// ErrNotRegular). SQLite's temporary files (sorts, FTS merges, VACUUM)
// are put in the cache directory too (PRAGMA temp_store_directory), never
// in TMPDIR. Reset drops everything, vacuums and truncates the WAL,
// so no indexed text stays on disk. The
// lock is an exclusive flock held until Close (released by the kernel if
// the process dies). A database SQLite reports corrupt or not-a-database
// at Open, or of another schema version, is rebuilt from scratch;
// corruption found later surfaces as errors.
package index
