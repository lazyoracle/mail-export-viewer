# Validation

How mail-export-viewer was validated before release: what was generated,
what was run, and the numbers it gave. Everything here is synthetic and
reproducible from this repository; no real mail was used.

## Corpus

`internal/testutil/corpus` generates deterministic exports (same seed, same
bytes) with a ground-truth manifest for every message: folder, source,
subject, sender, date, labels, categories, tags, flags, attachment names, a
unique searchable body token and thread membership. `scripts/gencorpus` is
its command-line front end; generated corpora go to `tests/tmp/` (ignored)
and are never committed.

| set | what it holds |
|---|---|
| `gmail` | Google Takeout mbox, CRLF, mboxrd quoting, `X-Gmail-Labels` with system labels (Unread, Starred, Important, Spam, Trash, Drafts), RFC 2047 and quoted labels, a reply thread, an attachment, a `multipart/related` inline image |
| `thunderbird` | a profile: `prefs.js` tag names and colours, `Mail/Local Folders` (Inbox, Sent, Drafts, `Archives.sbd/2019.sbd/Q1`), `ImapMail/…/INBOX`, `.msf` summaries that override header flags and add keywords, folders without `.msf`, clutter files |
| `apple` | Apple Mail export: nested `Name.mbox/mbox` directories with `Info.plist` and `table_of_contents` |
| `eml` | loose `.eml` files in nested and Unicode-named directories, LF and CRLF |
| `olm` | Outlook for Mac archives in both layouts (`Accounts/…/com.microsoft.__Messages`, `Local/Messages`), categories, flags, priority, attachments, an inline image, a contact and a calendar item |
| `pst` | PST (permute encoding) built with `pst/pstgen`: nested folders, flags (unread, flagged, importance, replied, forwarded, draft), categories, recipients, attachments, an embedded message, an HTML-only and an RTF-only body, a big body and a big attachment, a contact |
| `messy` | hostile and broken input (below) |
| `bulk` | N realistic messages for performance: 8 Gmail-style mbox files, 4 Thunderbird-style ones, a PST and an OLM, a reply chain of any length, thousands of labels; optionally one mbox padded to several GB |

The `messy` set covers: subjects and bodies in ISO-8859-1/2/5/7/15,
windows-1250/1251/1252, KOI8-R, Shift_JIS, ISO-2022-JP, EUC-JP, GBK, Big5,
EUC-KR and UTF-16 (base64, quoted-printable, 8bit); RFC 2047 edge cases
(adjacent encoded words, `_` in Q encoding, folded words, a language tag,
an unterminated word, raw UTF-8 and raw Latin-1 headers, emoji ZWJ
sequences); RFC 2231 continuations and charsets in file names and RFC 2047
in `name=`; garbage, missing, two-digit-year, obsolete-zone, epoch and
far-future dates; a header line without a colon, a NUL in a header, a
100 KB `To:`, duplicate `Subject:`, missing From, Message-ID or Subject, a
message with headers only, mixed CRLF/LF; mboxrd `From ` quoting; nested
`multipart/mixed(related(alternative))` with a `cid:` image and an attached
`message/rfc822`; three messages sharing one Message-ID; a 300-message
reply chain and a 121-message star thread; 300 messages with 10 of 3,000
distinct labels (Unicode, emoji, RTL, quotes, commas, slashes, HTML) and one
message with 1,000 labels; mailboxes named with emoji, Arabic, quotes and
`<tag>`, an ESC sequence, a newline, a backslash, a trailing space and 200
characters, and a hidden one; attachment names `../../etc/passwd`,
`..\..\boot.ini`, an OSC escape sequence, an RTL override, `<script>`,
quotes and semicolons and 300 characters, plus an HTML attachment; HTML
bombs (200,000 nested `<div>`s, a 5 MB attribute, 100,000 attributes,
90,000 unclosed tags, scripts, event handlers and `javascript:` links); a
3 MB attachment, a 9 MB single-line body, binary garbage inside a body, a
message cut in the middle of a base64 part; a zip-bomb OLM (a 250 MiB
document in a 250 KB archive); a PST with a corrupt message node, one with
8 KB of corrupted pages and a truncated one; 0-byte `.mbox`, `.eml`, `.pst`
and `.olm` files, random bytes named `.mbox`, a fake PST with a valid magic,
a lone `.msf`, a truncated OLM.

## End-to-end tests

`internal/e2e` (part of `go test ./...`, skipped with `-short`) builds the
real binary and, for each set on its own and for a mixed directory of all of
them, runs it with private `HOME`, `XDG_CACHE_HOME` and `TMPDIR` and checks
over HTTP against the manifest:

- `/api/status`: every expected source with its kind, state and count, no
  unexpected source, no path or panic in warnings;
- `/api/facets`: per-folder, per-label, per-category and per-tag counts and
  unread counts, tag colours, per-flag counts (deleted hidden by default);
- `/api/messages`: totals and order for every sort, no duplicates, every
  expected subject in its folder; for every message, its unique token finds
  exactly it, with the right subject, folder, kind, sender (address and
  name), date, flags, labels, categories and tags, and a deleted message is
  hidden by default;
- search operators against totals computed from the manifest: every `is:`
  flag, `is:read`, `has:attachment`, `label:`/`category:`/`tag:` and their
  negations, `folder:` and the `folder=` scope, `from:`, `before:`/`after:`,
  prefix search, a token and its negation, `subject:` plus a token, and
  charset words (Cyrillic, Greek, Polish, Czech, CJK, Korean);
- message detail (within 5 s even for HTML bombs), Message-ID, attachment
  names, text, sanitised HTML (no script, handler or `javascript:`); every
  attachment downloads with its listed size and a safe
  `Content-Disposition`; the big attachments' SHA-256; `cid:` URLs resolve;
  `/raw` keeps mboxrd-unquoted `From ` lines;
- `/api/threads`: every expected thread exactly, oldest first, with its
  `threadCount`;
- `/api/stats`: total, unread, with-attachment and out-of-range counts;
- security headers and CSP on UI files, JSON, raw, attachment, `cid` and
  error responses; a foreign `Host` gets 403; POST gets 405; traversal
  paths get 400/404 without leaking paths;
- a restart reuses the cache and shows the same data;
- the input tree is byte-for-byte unchanged (SHA-256, mode, size and mtime
  of every entry) and nothing is written outside
  `$XDG_CACHE_HOME/mail-export-viewer` (`HOME` and `TMPDIR` stay empty).

Also: single-file inputs (a PST, an OLM, an mbox, a `.msf`), appending to an
mbox between runs (only that file is re-indexed), a file rewritten under a
running viewer (410 `stale`, never another message's bytes), and a removed
file (410, then dropped on restart).

## Fuzzing

Native Go fuzz targets: `mail.FuzzParse` (MIME parser), `mbox.FuzzScanMessages`
(framing invariants), `mbox.FuzzSource`, `olm.FuzzArchive` (whole archive
bytes), `olm.FuzzMessageXML` (message document in a valid archive, both
layouts), `pst.FuzzSource` (whole file, seeded with all three encodings),
`msf.FuzzParse` (Mork), `index.FuzzParseQuery`, `index.FuzzSearch`,
`server.FuzzSanitizeHTML`. The source targets run Scan, Load, Raw and every
Attachment through `sourcetest.Exercise` and fail on any panic a format
recovered from; `sourcetest.Within` fails an input that runs for over 10 s
(Go's fuzzer has no per-input timeout).

Two rounds, each target alone with 4 workers: 60 s each before the fixes
below, 45 s each on the final code. No crash, hang (over 10 s), recovered
panic or invariant failure was found. Executions in the final round:
mbox framing 949k, mbox source 196k, OLM archive 45k, OLM document 22k,
query parser 256k, search 50k, MIME parser 252k, sanitiser 56k, Mork 229k.
No crasher inputs were committed (none were found). Run one with, e.g.,
`go test ./internal/source/pst -run XXX -fuzz FuzzSource -fuzztime 60s`.

Note: for the PST and OLM targets Go's progress line under-reports
executions (inputs of tens of KB; workers report rarely); an external
counter confirmed thousands of executions per second for PST.

## Performance

Machine: an 8-vCPU x86-64 cloud VM (Xeon, 2.2 GHz), 32 GB RAM, SSD
persistent disk, Linux; shared with other workloads, so expect about ±20 %
between runs. Binary built with `CGO_ENABLED=0 -trimpath`. Peak RSS is the
process's `VmHWM`.

Reproduce:

```
go run ./scripts/gencorpus -out tests/tmp/perf -sets bulk -bulk 100000 -thread 5000 -labels 3000 -seed 11
MEV_PERF=$PWD/tests/tmp/perf go test ./internal/e2e -run TestPerf -v -timeout 1h
go run ./scripts/gencorpus -out tests/tmp/perf1g -sets bulk -bulk 40000 -seed 12 -file-gb 1.05
MEV_PERF=$PWD/tests/tmp/perf1g go test ./internal/e2e -run TestPerf -v -timeout 1h
```

| corpus | messages | input | index time | throughput | peak RSS | cache | warm restart |
|---|---:|---:|---:|---:|---:|---:|---:|
| 100k (12 mbox, PST, OLM, 3,000 labels, a 5,000-message chain) | 105,000 | 251 MiB | 46.8 s | 2,245 msg/s, 5.4 MiB/s | 174 MiB | 218 MiB | 46 ms, 18 MiB |
| 1 GB (one 1.05 GiB mbox of 320 KB messages, plus 11 smaller files) | 40,000 | 1,165 MiB | 56.8 s | 704 msg/s, 20.5 MiB/s | 236 MiB | 1,172 MiB | 31 ms, 14 MiB |

Request latency on the 100k corpus (20 repetitions; first page of 50,
`total` included; the 1 GB corpus is faster or equal throughout):

| request | hits | p50 ms | p95 ms |
|---|---:|---:|---:|
| list, newest first (page 1 / page 2) | 105,000 | 5.8 / 5.9 | 6.5 / 6.8 |
| list by sender / subject / oldest first | 105,000 | 5.8 / 2.4 / 2.0 | 7.0 / 3.8 / 2.3 |
| one folder (`folder=`) | 9,375 | 2.6 | 3.6 |
| term `budget` (date / relevance) | 5,000 | 27.1 / 30.5 | 33.2 / 32.5 |
| prefix `budg` | 5,000 | 27.5 | 36.5 |
| phrase (3 words) | 1,000 | 29.0 | 35.7 |
| rare token / no hit | 1 / 0 | 2.8 / 2.2 | 3.9 / 2.7 |
| common word (in 25 % of messages) | 26,143 | 69.1 | 78.4 |
| `from:alice@…` | 10,537 | 84.6 | 86.5 |
| `label:` / `category:` / `tag:` | 53 / 5,000 / 2,143 | 5.3 / 48.1 / 26.5 | 7.4 / 59.4 / 30.8 |
| `folder:box3` | 9,375 | 4.1 | 5.9 |
| `is:unread` / `is:starred` / `has:attachment` | 19,000 / 3,750 / 9,000 | 15.5 / 20.2 / 16.1 | 17.4 / 22.9 / 20.0 |
| `-is:unread` / `-label:` / `-budget` | 86,000 / 104,947 / 100,000 | 17.4 / 46.8 / 95.3 | 20.7 / 58.9 / 116.6 |
| `budget -is:unread` / `budget label: is:unread` | 250 / 0 | 45.3 / 19.1 | 48.6 / 21.3 |
| `after:2013 before:2014` | 7,002 | 3.1 | 3.9 |
| `filename:doc123` | 12 | 147.6 | 163.9 |
| CJK substring | 0 | 245.9 | 269.4 |
| message detail | | 0.9 | 1.3 |
| thread of 5,000 | | 93.1 | 112.9 |
| facets (3,000 labels) / status | | 15.6 / 1.2 | 17.7 / 1.8 |
| stats: first call / cached | | 395 / 0.8 | — / 1.0 |

Before the fixes of this validation (same corpus, same machine):
`is:unread` 81 ms, `is:starred` 96 ms, `has:attachment` 134 ms,
`-is:unread` 111 ms, `-label:` 147 ms, `-budget` 230 ms, first stats call
1.7 s. Indexing time and cache size did not change (+2 MB for the new
index).

The pessimistic filters of the index task report, now:

- `is:`, `has:attachment`, negated flags: served from a (date, flags)
  index; no longer linear in table size beyond a covering-index count.
- negations without a positive term: the page walks the date index; the
  `total` still probes every message against the match set (95 ms at 100k
  for `-budget`).
- `filename:` (`LIKE` over attachment names) and CJK substrings (`instr`
  over subject and people) still scan the table: ~150 ms and ~250 ms at
  100k, linear in size (about 1.5 s and 2.5 s at 1M). Fixing them needs an
  attachment-name table or a trigram index; not done (documented).
- very common words (~70 ms at 100k): dominated by the FTS count and the
  membership walk; unchanged.

Indexing is bound by the single SQLite writer (pure-Go modernc SQLite), as
found in the index task; ~2,200 msg/s here, so about 8 minutes for 1M
messages on first run, instant afterwards. The cache is about the size of
the text in the input (the FTS table stores the indexed text for snippets);
for the 1 GB mbox of large text bodies it is 1.17 GB.

Memory under hostile requests: 90 concurrent detail, raw and attachment
requests (16 at a time) on the HTML bombs, the 9 MB single line, the 3 MB
attachment and the zip-bomb neighbour raised peak RSS to 259 MiB, all
answered within 0.3 s.

## Race detector, vet, formatting

- `CGO_ENABLED=1 go test -race ./...` (gcc available): all packages pass,
  including the end-to-end tests (run twice, before and after the fixes).
- `go vet ./...` clean on linux/amd64 and darwin/arm64; `gofmt -l` clean;
  `make check-pii` before every commit; JS unit tests (`node --test`) pass.

## UI

`internal/web/browsertest/ui.mjs` (the existing behaviour check, now
sharing `cdp.mjs`) passes. `internal/web/browsertest/scale.mjs` measures any
running server in headless Chromium (chrome-headless-shell): time to first
rows, sidebar size, paging by scrolling, label filtering, search, the
longest conversation, statistics, DOM size and long main-thread tasks
(over 50 ms). Final numbers on the 100k corpus:

| step | result |
|---|---|
| first rows | 0.4–1 s (cold page) |
| sidebar | 3,002 labels rendered at once (one ~300 ms task at load) |
| scroll to 4,100 rows | 6.7 s, DOM 63k nodes, long tasks 2.4 s total, max 100 ms |
| label filter | 0.6 s (debounced), one ~100 ms task |
| search "budg" | 0.5 s |
| 5,000-message conversation | 1.8 s, long tasks 0.5 s total, max 61 ms (was 5.8 s / 5.1 s) |
| statistics | 0.2–0.4 s |
| while indexing (25 s) | ~70–110 ms task per status poll, two ~400 ms when term counts change (was ~400 ms every second) |

On the mixed messy corpus every hostile message opens within 0.4 s
(HTML bombs: 0.1–0.3 s) except the 9 MB single-line body: 1.4 s to fetch
the 8 MiB JSON plus a ~1 s layout task (was a 4.8 s freeze). Hostile
names, RTL and ESC characters display as text. The only console message is
Chromium reporting the blocked `<script>` inside the sandboxed message
frame, which is intended. No request leaves the origin.

Known UI limits: the list is paged, not virtualised (DOM grows with
scrolling); the full label list is rendered (3,000+ entries cost ~300 ms
once); dates are shown with two-digit years, so a bogus year-2100 date
reads "1/1/00"; an HTML body of several MB laid out in the sandboxed frame
is not capped like the text view.

## Bugs found and fixed

All filed as GitHub issues with a synthetic repro, fixed test-first and
closed with the fixing commit.

| issue | bug | fix |
|---|---|---|
| #13 | Nested Apple Mail mailboxes kept the parent's `.mbox` (`Projects.mbox/Launch`) | `A.mbox/B.mbox/mbox` is `A/B` |
| #14 | Thunderbird tags shown as raw keywords in folders without a usable `.msf` | tags-only sidecar from `prefs.js` |
| #15 | Gmail label lost a quote decoded from an encoded word | quotes trimmed only as list syntax |
| #16 | SQLite temp files with index data written to `TMPDIR` | `temp_store_directory` = cache dir |
| #17 | OLM zip bomb: 250 KB archive, 9 s, 1.4 GB RSS | per-document read capped by the scan budget; 64 MiB document cap |
| #18 | `is:`, `has:attachment`, negations scanned the table | (date, flags) index; rowid-tested negations walk the date index |
| #19 | First `/api/stats` 1.7 s at 100k | one light pass (`Store.EachStatRow`) |
| #20 | Thunderbird drafts lost the draft flag when the `.msf` knew them | header draft flag kept |
| #21 | UI blocked ~400 ms every second while indexing with 3,000 labels | unchanged term lists are not rebuilt |
| #22 | 5,000-message conversation froze the UI ~5 s | cached date formatters, flex layout, `content-visibility` |
| #23 | 8 MiB single-line body froze the reader 4.8 s | text view shows the first 512 KB |

## Not verified

- Real exports. Everything ran on synthetic data built from public
  descriptions of the formats. In particular the OLM layouts and element
  names, PST features no sample file covered (see `internal/source/pst/doc.go`),
  Thunderbird `.msf` files written by real Thunderbird versions and
  Apple Mail's nested layout are modelled, not observed. Gmail Takeout's
  encoding of labels containing quotes is unknown.
- macOS. Tests and the binary ran on Linux only (CI covers macOS).
- Browsers other than headless Chromium; screen readers.
- Corpora above 105k messages / 1.2 GB: the numbers above extrapolate
  linearly for the scanning queries listed.
- Go's fuzzer reports few executions for the PST target (large inputs,
  workers report rarely) and found no new coverage there; an external
  counter showed ~3,400 executions/s, but its coverage guidance is weak.
