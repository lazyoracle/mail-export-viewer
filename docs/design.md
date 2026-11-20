# mail-export-viewer — Design

## Intent
A read-only, fully offline viewer for exported mailboxes. One portable Go binary:
`mail-export-viewer <file-or-dir>` starts a localhost-only web server with a friendly UI for
browsing and searching years of mail exported from Thunderbird, Outlook (Windows/Mac) and Gmail.
It merges and generalises two earlier tools (an earlier-tool with SQLite/FTS5 and a Thunderbird
folder reader) and adds the formats they lack. Nothing is ever written next to the input or sent anywhere.

## Goals
- Formats (detected by content, never by extension alone):
  - **mbox** — Gmail Takeout (`X-Gmail-Labels`), Thunderbird local folders (+ `.sbd` nesting), mutt, etc.
    mboxo/mboxrd/mboxcl, LF/CRLF, BOM.
  - **msf** — Thunderbird Mork summary files. Parsed as a *metadata sidecar* of the mbox next to it
    (tags/keywords, read/starred/replied/forwarded flags; Thunderbird's old `$label1..5` labels are
    keywords and show as tags). Pointing at a `.msf` opens its mbox.
    Tag display names/colours come from the profile's `prefs.js` when it can be found.
  - **pst** (Outlook Windows, Unicode and ANSI PST/OST-style files; the "compressible" and "high"
    encodings, [MS-PST] permute and cyclic, are supported: neither needs a key) — pure Go reader.
  - **olm** (Outlook for Mac export, a zip of XML) — pure Go reader.
  - Loose `.eml` files in a directory are read as messages of their folder.
  - A directory may mix any of the above; everything shows in one folder tree, tagged with its source file.
- Metadata preserved and shown: folder path, Gmail labels, Outlook categories, Thunderbird tags,
  flags (unread, starred/flagged, answered, forwarded, draft, deleted, junk, has attachment), importance,
  and every format-specific property that is not folded into the above (shown in a "Properties" panel).
- UI: folder tree + labels/tags/categories sections with counts, paged infinite-scroll message list
  (keyset pages of 100; amended from "virtualised": the DOM grows with the rows scrolled past, about
  15 nodes per row, and is not recycled; measured acceptable on a 100k-message archive, see
  docs/validation.md, and listed under Limitations in the README), sort,
  conversation threading, message view (sanitised HTML in sandboxed iframe / plain text / headers / raw),
  attachments download, inline `cid:` images, `.eml` export, filters, keyboard shortcuts, dark mode, deep links.
- Search: full-text (subject/people/body/attachment names), ranking, snippets, Gmail-style operators
  `from: to: subject: mailbox:|folder: label: tag: is:unread|starred|… has:attachment filename: before: after:`,
  phrases, negation, prefix-as-you-type.
- Scale: multi-GB archives; streaming scan; bodies read lazily; persistent SQLite/FTS5 index cache
  (user cache dir, 0600) for instant restarts.
- One static binary (CGO off) for macOS + Linux (amd64/arm64); UI embedded with `go:embed`; no build
  step for the UI (vanilla ES modules); zero network at runtime.
- MIT licensed.

## Non-goals
Sending/replying, editing/deleting, IMAP, auth/remote use, Windows, searching inside attachment contents,
calendar/contacts/tasks (only mail items are shown; others are counted and skipped), PSTs with any
other encryption method (e.g. Windows Information Protection; Outlook's "high" setting is the keyless
cyclic encoding and *is* read) and password-protected archives.

## Anti-goals
Anything phoning home; modifying input; one corrupt message/folder crashing or hanging the run;
shipping any real mail, paths, names or emails in the repo, tests, fixtures or commit history;
duplicate implementations of the same thing (one MIME parser, one sanitiser, one index, one UI).

## Constraints
- Real data is never available to development. All fixtures are synthetic and generated in tests/tools.
  PST has no public writer, so a minimal synthetic PST *writer* lives in test code; the reader is
  additionally validated against third-party sample PST files fetched ad hoc into a scratch dir
  and **never committed**.
- Repo is private now, will be published: commits use the GitHub noreply identity only; a scripted
  PII/secret scan (`make check-pii`) runs before every commit and in CI.
- Dependencies kept minimal: `golang.org/x/text` (charsets), `golang.org/x/net` (html parser for the
  sanitiser), `modernc.org/sqlite` (pure Go SQLite+FTS5). Nothing else without justification.
- Go 1.24, stdlib-first.

## Approaches considered
1. **Per-format viewers behind a router.** Duplicates index/search/server/UI per format. Rejected (the exact
   duplication the user wants to avoid).
2. **Convert everything to mbox/eml first, reuse earlier-tool as is.** Loses metadata (tags, categories, flags,
   properties) and needs temp-disk writes. Rejected.
3. **One normalised model; each format is a small reader that streams `Entry` records into one index;
   one parser/sanitiser/server/UI.** **Chosen** (author's call; the user delegated design).
   Within it, storage: SQLite+FTS5 cache with lazy body reads (chosen, from earlier-tool) over
   in-memory-only index (from the Thunderbird reader; fine for small input, bad at multi-GB).

## Architecture
```
cmd/mail-export-viewer     flags, lock, browser open, wiring
internal/mail              Message model + RFC 5322/MIME parser, RFC 2047/2231, charsets, html→text,
                           addresses, dates (single implementation, ported from the two sources)
internal/source            Entry/Flags/Meta types, Source interface, content sniffing, directory discovery
internal/source/mbox       streaming scanner + Thunderbird/Gmail header metadata extraction
internal/source/msf        Mork parser → per-message metadata merged by the mbox source
internal/source/olm        zip+XML reader
internal/source/pst        NDB/LTP/messaging reader (+ test-only writer in pst/pstgen)
internal/index             SQLite schema, indexer, query parser, FTS, labels/tags/flags, threading, cache
internal/server            JSON API, sanitiser, security headers, cid/attachments
internal/web               embedded UI (+ node unit tests for pure JS modules)
internal/testutil          synthetic corpus generators for every format
```
Core contract (names indicative; the builder of `internal/source` finalises them):
```go
type Entry struct {            // what a reader yields per message
    Folder string              // "Inbox", "Archive/2019" (source-relative, '/'-joined)
    Labels, Tags []string      // Gmail labels / Outlook categories ; Thunderbird tags (display names)
    Flags  Flags               // bitset: Read Starred Answered Forwarded Draft Deleted Junk HasAttach ...
    Props  []Prop              // remaining format-specific key/values, in order
    Loc    Locator             // opaque, serialisable; resolved later by the same Source
    Msg    *mail.Summary       // headers parsed while scanning (subject, from, to, date, ids, attach names, text for FTS)
}
type Source interface {
    Kind() string
    Scan(ctx context.Context, emit func(Entry) error) error  // streaming; per-item errors are warnings, not fatal
    Load(Locator) (*mail.Message, error)                      // full parse on demand (body, attachments)
    Raw(Locator) ([]byte, error)                              // .eml export (synthesised for pst/olm)
}
```
Index cache is keyed by input path+size+mtime per source file; changed files are re-indexed, stale locators
are detected on read (offset validation / zip entry / node id) and reported, never mis-served.
**Residual risk (accepted):** an mbox locator (offset+length) carries no identity of the message. If an
mbox is rewritten *while the viewer runs* so that a separator line lands at the same offset and the next
one at the same end, the other message at that range is served, until the next start re-indexes the file
(its size/mtime fingerprint changed). Stated in internal/source/mbox/doc.go and the README limitations.

## Left to the builder
Schema details, UI visual design, ranking, pagination mechanics, exact Mork/PST/OLM edge-case handling
(documented in each package's doc.go).
