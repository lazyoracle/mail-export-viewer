# HTTP API

The viewer's server (`internal/server`) answers on one origin, normally
`http://127.0.0.1:<port>/`. Everything under `/api/` is the JSON API below;
every other path is a file of the embedded UI (`/` is `index.html`).

The API is read-only. It is meant for the embedded UI only: there is no CORS,
so other origins cannot read it.

## Conventions

- **Methods.** Only `GET` and `HEAD` are accepted. Any other method gets
  `405` with `Allow: GET, HEAD`.
- **Host.** The `Host` header must be `localhost`, `127.0.0.1` or `[::1]`,
  with any port, or a host added with `-allowed-host`. Any other value
  gets `403` (`forbidden_host`). This protects against DNS rebinding.
- **Paths.** Path segments are percent-decoded one at a time, so `%2F`
  inside a Content-ID stays part of that segment. Empty segments (`//`),
  `.` and `..` segments (also when encoded), backslashes and NULs get
  `400`. Nothing is ever redirected. A path over 4 KiB or a query string
  over 8 KiB gets `414`.
- **Query parameters.** A parameter given twice gets `400`.
- **JSON.** Responses use `Content-Type: application/json; charset=utf-8`
  and `Cache-Control: no-store`. Arrays are never `null` (an empty list is
  `[]`). Dates are RFC 3339 UTC strings (`"2023-01-02T10:00:00Z"`), or
  `null` when unknown.
- **Text vs HTML (important for the UI).** Every string field is plain
  text: insert it with `textContent`. The only exceptions are fields whose
  name ends in `Html`:
  - `highlightHtml`: an escaped search snippet in which only
    `<mark>…</mark>` is markup.
  - `bodyHtml`: the server-sanitised message HTML. Show it only in the
    sandboxed `srcdoc` iframe described under
    [Message detail](#get-apimessagesid).

  `snippet`, `text`, subjects, names, headers, props and warnings can
  contain `<` and `&` as literal characters.
- **Ids.** Message ids are positive integers. They are never reused, but
  rebuilding the index (`-reindex`, or a changed input file) gives the
  same message a new id. A stale id (from an old deep link) gets `404`
  with `code: "not_found"`. The UI should treat that as "message no longer
  exists", not as a crash. Source ids and folder ids can also change after
  a rebuild.
- **While indexing.** Every endpoint works while the index is being built.
  Results grow as indexing progresses: poll `/api/status`.

### Errors

Every error is JSON with a fixed, path-free message and a stable `code`:

```json
{"error": "message not found (the index may have been rebuilt)", "code": "not_found"}
```

| status | code | meaning |
|---|---|---|
| 400 | `bad_request` | Invalid id, index or parameter, a bad cursor, or a bad path. |
| 403 | `forbidden_host` | The `Host` header is not allowed. |
| 404 | `not_found` | Unknown endpoint, message (stale id), attachment or Content-ID, or UI file. |
| 405 | `method_not_allowed` | The method is not `GET` or `HEAD`. |
| 410 | `stale` | The source file changed since it was indexed. Reindex to refresh. |
| 410 | `gone` | The source file is missing, or is no longer one of the opened inputs. |
| 413 | `too_large` | The message is over 256 MiB. It is listed but cannot be opened. |
| 414 | `uri_too_long` | The path is over 4 KiB or the query string is over 8 KiB. |
| 500 | `internal` | Unexpected error. The details go to the server log only. |
| 503 | `unavailable` | The request was cancelled while waiting for memory budget. |

### Security headers (all responses)

- `X-Content-Type-Options: nosniff`
- `Referrer-Policy: no-referrer`
- `X-Frame-Options: DENY`
- `Cross-Origin-Opener-Policy: same-origin`
- `Cross-Origin-Resource-Policy: same-origin`. Tokenised cid URLs send
  `cross-origin` instead (see [cid images](#get-apimessagesidcidcid)).

`Content-Security-Policy`:

- UI files: `default-src 'self'; img-src 'self' data:; style-src 'self' 'unsafe-inline'; object-src 'none'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'`
- API responses: `default-src 'none'; img-src 'self' data:; style-src 'unsafe-inline'`
- Raw, attachment and cid responses get a second header, `sandbox`, on top
  of the API policy.

The UI therefore cannot load anything from outside the origin. No inline
scripts are allowed (`script-src` falls back to `'self'`). Use ES modules
served from the UI tree.

## Endpoints

| path | returns |
|---|---|
| `GET /api/status` | Version, indexing progress and per-source state. |
| `GET /api/facets` | Sources, folder tree, labels, categories, tags and flags, with counts. |
| `GET /api/messages` | A page of the message list or search results. |
| `GET /api/messages/{id}` | Message detail. |
| `GET /api/threads/{id}` | Every message in the thread of message `{id}`. |
| `GET /api/messages/{id}/raw` | The message as an `.eml` download. |
| `GET /api/messages/{id}/attachments/{index}` | One attachment. |
| `GET /api/messages/{id}/cid/{cid}` | An inline (`cid:`) part. |
| `GET /api/messages/{id}/cid/{token}/{cid}` | The same part, loadable from the sandboxed frame. |
| `GET /api/stats` | Statistics: counts by year, month, sender, folder and source. |

---

### `GET /api/status`

```json
{
  "version": "v1.0.0",
  "includeDeleted": false,
  "indexing": {"running": false, "finished": true, "messages": 10, "warnings": 0, "error": ""},
  "sources": [
    {"id": 2, "name": "Inbox.mbox", "kind": "mbox", "state": "done", "unit": "bytes",
     "done": 2089, "total": 2089, "messages": 3, "count": 3, "unread": 0, "complete": true,
     "warningCount": 0, "warnings": [], "error": ""}
  ]
}
```

`includeDeleted` reports the `-include-deleted` setting. It is the
default for the per-request `deleted` parameter of `/api/messages`,
`/api/threads/{id}` and `/api/facets`, so a UI toggle should start from
it. When it is false and no `deleted` parameter is given, messages flagged
deleted are hidden from lists, counts and facets.

`indexing` describes the current or last indexing run:

- `running`: a run is in progress.
- `finished`: the last run completed (it was not cancelled).
- `messages`, `warnings`: totals for this run.
- `error`: the error that ended the run, if any.

Poll `/api/status` about once a second while `running` is true.

`sources` lists the sources of the run first, in run order, then any other
sources the index still holds.

- `id` is `0` until the source has been written to the index once.
- `name` is the path relative to the input given on the command line (for
  example `Local Folders/Inbox`), or the file's base name. Full paths are
  never shown.
- `kind` is one of `mbox`, `eml`, `olm`, `pst`.
- `state` is one of:

  | state | meaning |
  |---|---|
  | `queued` | Waiting in this run. |
  | `indexing` | Being read now. |
  | `done` | Indexed in this run. |
  | `cached` | Unchanged, reused from the cache. |
  | `failed` | Unreadable. What was read is kept, and it is retried next run. |
  | `incomplete` | Not part of this run, and its last run did not finish. |

- `unit` is `bytes` or `messages`. It says what `done` and `total`
  measure. `total` is `0` when unknown (for the `messages` unit), so show a
  count instead of a percentage.
- `messages` is the number written in this run. `count` and `unread` are
  the indexed totals.
- `complete`: fully indexed from the current files.
- `warningCount` counts every warning. `warnings` holds at most the first
  20, each at most about 300 bytes. Paths in warnings are shortened to
  names.
- `error`: why the source failed, if it did.

### `GET /api/facets`

Data for the sidebar.

| param | meaning |
|---|---|
| `deleted` | `include` counts messages flagged deleted, `hide` leaves them out. Absent means the server default (`/api/status` `includeDeleted`). `only` is not accepted here (`400`). |

```json
{
  "sources": [{"id": 3, "name": "backup.olm", "kind": "olm", "count": 1, "unread": 1}],
  "folders": [
    {"name": "backup", "path": "backup", "count": 0, "unread": 0,
     "sources": [{"folderId": 4, "sourceId": 3, "sourceKind": "olm", "source": "backup.olm", "count": 0, "unread": 0}],
     "children": [
       {"name": "Inbox", "path": "backup/Inbox", "count": 1, "unread": 1,
        "sources": [{"folderId": 3, "sourceId": 3, "sourceKind": "olm", "source": "backup.olm", "count": 1, "unread": 1}],
        "children": []}
     ]}
  ],
  "labels":     [{"name": "Inbox", "color": "", "count": 3, "unread": 0}],
  "categories": [{"name": "Finance", "color": "", "count": 1, "unread": 1}],
  "tags":       [{"name": "Important", "color": "#FF0000", "count": 5, "unread": 0}],
  "flags":      [{"name": "unread", "count": 1}, {"name": "starred", "count": 1}]
}
```

**`folders`** is one merged tree for all sources, keyed by path. Node
fields:

- `count` and `unread` count the messages directly in that folder, over
  all sources. Subfolders are not included.
- `sources` has one entry per source that has this folder. Its
  `folderId` is that source's folder. An intermediate node can have
  `sources: []`.
- Children are sorted by name.

Folders inside a PST or OLM file sit under a node named after the file
(`backup.olm` → `backup/…`). Folders of different mbox files in the same
directory share the tree.

To draw a tree grouped by source, start from `sources` and, for each
source, keep the nodes whose `sources[].sourceId` matches (plus their
ancestors).

To list a folder:

- One source's folder: `GET /api/messages?folderId=<folderId>`.
- A tree node across every source with that exact path:
  `GET /api/messages?folder=<path>`.

**`labels`** are Gmail labels (mbox/eml sources). **`categories`** are
Outlook categories (olm/pst sources). **`tags`** are Thunderbird tags.
Each list is sorted by name, ignoring case. `color` is exactly
`"#RRGGBB"` or `""` (the server drops any other value); only tags have
colours. To filter, add an operator to `q` with the name
quoted: `label:"Name"`, `category:"Name"`, `tag:"Name"`.

**`flags`** has one entry per flag, in this order: `unread`, `starred`,
`answered`, `forwarded`, `draft`, `deleted`, `junk`, `attachment`,
`important`. Filter with `is:<name>` (`has:attachment` for attachments).
The `deleted` count always includes deleted messages.

Label, category, tag and flag counts can lag behind while indexing runs.
Folder counts are live.

### `GET /api/messages`

Lists or searches messages, one page at a time.

| param | meaning |
|---|---|
| `q` | Search query, at most 4096 bytes (see [Query language](#query-language)). Empty lists everything. |
| `sort` | `date_desc` (default), `date_asc`, `from`, `subject`, `relevance`. `relevance` falls back to `date_desc` when `q` has no text to rank by. |
| `limit` | Page size, 1–1000 (default 50). |
| `cursor` | The `nextCursor` of the previous page. It is only valid with the same `q`, `sort` and scope. |
| `source` | Only this source (`sources[].id`). |
| `folderId` | Only this source folder (`folders[].sources[].folderId`). |
| `folder` | Only folders with exactly this path (`folders[].path`, case-sensitive), in every source. Subfolders are not included. |
| `deleted` | Which messages flagged deleted to show: `hide`, `include`, or `only` (nothing else). Absent means the server default (`/api/status` `includeDeleted`). This is applied outside `q`, so no query text can bring normal messages into `only`. With `hide`, a query containing `is:deleted` still shows deleted messages. |

The scope parameters can be combined; they are ANDed. An unknown id gives
an empty list, not an error.

```json
{
  "items": [
    {"id": 10, "threadId": 10, "threadCount": 1, "sourceId": 3, "sourceKind": "olm",
     "folderId": 3, "folder": "backup/Inbox", "subject": "Outlook budget",
     "from": {"name": "Frank", "email": "frank@example.org"}, "toText": "Bob <bob@example.com>",
     "date": "2023-03-08T10:00:00Z", "flags": ["unread", "starred", "attachment"],
     "hasAttachment": true, "labels": [], "categories": ["Finance"], "tags": [],
     "snippet": "Budget numbers inside", "highlightHtml": "Outlook <mark>budget</mark>"}
  ],
  "total": 1,
  "nextCursor": ""
}
```

- `total` is the number of matches. It is only computed for the first page
  (no `cursor`); later pages report `-1`, so keep the first value.
- `nextCursor` is `""` on the last page.
- Paging is keyset-based for the date, from and subject sorts, so it stays
  stable while indexing adds messages. `relevance` pages by offset.
- `threadId` is `0` until threading has run. `threadCount` is the number of
  visible messages in the thread.
- `toText` is the formatted To list.
- `flags` are the flag names above.
- `snippet` is plain text from the start of the body.
- `highlightHtml` is present only when the query has a text match. It is
  safe HTML with `<mark>` around the matches; show it instead of the
  snippet.
- An invalid parameter or a cursor from another sort order gives `400`.

#### Query language

Plain words match subject, people, body and attachment names. The last word
matches as a prefix (3 or more characters). Use `"quoted phrases"`, and put
`-` before a word, phrase or operator to negate it.

| operator | meaning |
|---|---|
| `from:` `to:` `cc:` `subject:` | Words in that field (`to:` includes Cc). |
| `folder:` / `mailbox:` | A folder path or its last segment(s), ignoring case. |
| `label:` | A label or a category, as a whole name, ignoring case. |
| `category:` | A category only. |
| `tag:` | A tag only. |
| `is:` | `unread` `read` `starred`/`flagged` `answered`/`replied` `forwarded` `draft` `deleted`/`trash` `junk`/`spam` `important` `attachment` |
| `has:attachment` | Messages with attachments. |
| `filename:` | Substring of an attachment name. |
| `before:` `after:` | `YYYY`, `YYYY-MM` or `YYYY-MM-DD` (`/` also allowed), in UTC. `before:` is exclusive. Undated mail matches only the negated forms. |

Values with spaces need quotes, for example `label:"My Label"`; inside
quotes, write `\"` and `\\` for a literal quote and backslash. Unknown
operators and bad values are searched as plain text, so a query never
errors.

### `GET /api/messages/{id}`

Message detail. The server re-reads the message from its source file.

```json
{
  "id": 10, "threadId": 10,
  "source": {"id": 3, "name": "backup.olm", "kind": "olm"},
  "folderId": 3, "folder": "backup/Inbox",
  "messageId": "o1@example.org", "inReplyTo": "", "references": [],
  "subject": "Outlook budget",
  "from": {"name": "Frank", "email": "frank@example.org"},
  "to": [{"name": "Bob", "email": "bob@example.com"}], "cc": [], "bcc": [],
  "date": "2023-03-08T10:00:00Z",
  "flags": ["unread", "starred", "attachment"],
  "labels": [], "categories": ["Finance"], "tags": [{"name": "Work", "color": "#FF9900"}],
  "props": [{"key": "X-Custom-Thing", "value": "kept as a prop"}],
  "headers": [["Subject", "Outlook budget"]],
  "text": "Budget numbers inside", "textTruncated": false,
  "bodyHtml": "<p>Budget numbers <b>inside</b></p><img src=\"data:image/png;base64,…\">",
  "htmlTruncated": false,
  "remoteContentBlocked": false,
  "attachments": [
    {"index": 0, "filename": "budget.csv", "contentType": "text/csv", "contentId": "",
     "size": 8, "inline": false, "url": "/api/messages/10/attachments/0"},
    {"index": 1, "filename": "chart.png", "contentType": "image/png", "contentId": "chart@example.org",
     "size": 21, "inline": true, "url": "/api/messages/10/attachments/1"}
  ],
  "rawUrl": "/api/messages/10/raw",
  "parseError": false
}
```

**Header fields**

- `headers` lists the header fields in order, as `[name, decoded value]`
  text pairs, at most 1000. It can be empty for PST/OLM messages that have
  no transport headers.
- `messageId`, `inReplyTo` and `references` are given without angle
  brackets.

**Properties.** `props` are format-specific properties for the
Properties panel: unmapped `X-` headers, Thunderbird `.msf` values, and
OLM/PST fields. They are listed in source order, and keys can repeat.

**Bodies**

- `text` is the plain-text body. For an HTML-only message it is text
  extracted from the HTML.
- `bodyHtml` is `""` when the message has no HTML part. Otherwise it is
  sanitised HTML. Show it in an iframe with
  `sandbox="allow-popups allow-popups-to-escape-sandbox"` (never
  `allow-scripts` or `allow-same-origin`) and `srcdoc` set to `bodyHtml`.
  The frame inherits the UI's CSP. In the sanitised HTML:
  - Links open in a new tab with `noopener noreferrer`.
  - Remote images are never loaded: they appear as
    `<img data-blocked-src="…">` and set `remoteContentBlocked: true`, so
    show a "Remote content blocked" notice. There is no "load anyway"
    option, by design.
  - Small inline (`cid:`) raster images are embedded as `data:` URIs.
    Larger ones point at `/api/messages/{id}/cid/{token}/{cid}`.
  - The `<body>` colours and style are kept on a wrapping `<div>`.
- Each of `text` and `bodyHtml` is cut at 8 MiB. `textTruncated` and
  `htmlTruncated` say when that happened.

**Attachments.** `attachments` lists every non-body part in MIME order;
`index` is the part's position. `inline` means an embedded part (for
example a `cid:` image). `url` downloads the part.

**Other fields**

- `flags`, `labels`, `categories` and `tags` are the same as in the list.
- `parseError: true` means the message could not be fully parsed when it
  was indexed. It is shown with whatever data could be read.

Errors: `404 not_found` (a stale id), `410 stale`, `410 gone`, `413 too_large`.

### `GET /api/threads/{id}`

`{"items": [ … ], "truncated": false}`: every message in the thread
containing message `{id}`, oldest first, with unknown dates last. The
items have the list shape. An unknown id gives `404`.

- At most 5000 messages are returned. When the thread is longer,
  `truncated` is `true`, the first 5000 are returned, and message `{id}`
  is always among them (it replaces the 5000th when it would fall
  beyond). `threadCount` is always the full size of the thread.
- `deleted` (`hide`, `include`, `only`; absent means the server default)
  says whether messages flagged deleted are part of the thread. With
  `hide`, message `{id}` itself is still shown. `only` behaves like
  `include`, because a thread is shown whole.

### `GET /api/messages/{id}/raw`

The message as `message/rfc822`, with
`Content-Disposition: attachment; filename="<subject>.eml"` (or
`message-<id>.eml` when there is no subject). `HEAD` costs as much as
`GET` here (and for attachments and cid parts): the message is re-read
to know the length. mbox and eml messages are
returned byte for byte. PST and OLM messages are rebuilt as MIME with
their attachments.

### `GET /api/messages/{id}/attachments/{index}`

Returns the decoded bytes of one attachment.

- `Content-Type` is the declared type if it is on an allow-list (common
  images other than SVG, PDF, plain text, CSV, calendar, archives, Office
  and OpenDocument files, audio, video). Anything else, including HTML,
  SVG, XML and scripts, is served as `application/octet-stream`.
- `Content-Disposition` is always `attachment` (RFC 6266). It carries an
  ASCII `filename` and a UTF-8 `filename*`. The name is reduced to a safe
  base name: directories, control and bidi characters are removed, and
  `"<>:|?*;` become `_`.
- With `?inline=1`, raster images (PNG, JPEG, GIF, WebP, BMP, AVIF) are
  sent with `inline` instead. Use this for previews. SVG is never inline.
  `inline` accepts only `1`; any other value gives `400`.
- An index out of range gives `404`.

### `GET /api/messages/{id}/cid/{cid}`

Returns the part whose Content-ID matches `{cid}`. Angle brackets are
optional, and the value must be percent-encoded as a path segment. Raster
images are sent inline, everything else as an attachment. The response
has `Cross-Origin-Resource-Policy: same-origin`.

`GET /api/messages/{id}/cid/{token}/{cid}` is the form the sanitised HTML
uses. `{token}` is a per-message MAC under a key that changes every time
the server starts, so a page from another origin cannot build such a URL.
This form answers with `Cross-Origin-Resource-Policy: cross-origin`, so
the sandboxed (opaque-origin) message frame can load it. A wrong token
gives `404`. Use URLs exactly as they appear in `bodyHtml`.

### `GET /api/stats`

```json
{
  "total": 9, "undated": 0, "outOfRange": 0, "unread": 1, "withAttachments": 2,
  "byYear":  [{"year": 2023, "count": 9}],
  "byMonth": [{"month": "2023-01", "count": 3}, {"month": "2023-02", "count": 5}],
  "topSenders": [{"name": "Alice Example", "email": "alice@example.com", "count": 1}],
  "byFolder": [{"path": "Archive", "count": 5}, {"path": "Inbox", "count": 3}],
  "bySource": [{"id": 1, "name": "Archive", "kind": "mbox", "count": 5}],
  "partial": false,
  "computedAt": "2026-01-01T12:00:00.123Z"
}
```

- Statistics cover the messages the default list shows, so deleted
  messages count only with `-include-deleted`. There is no `deleted`
  parameter here.
- Years and months are in UTC, sorted ascending.
- `outOfRange` counts dates before 1980 or more than a year in the future.
  Those messages are left out of `byYear` and `byMonth`.
- `topSenders` holds the 20 most frequent senders. Senders are grouped by
  address, ignoring case.
- `byFolder` holds the 20 largest folders.
- The result is cached until the index changes. While indexing runs it is
  recomputed at most every 30 s, and `partial` is `true`.
- The first call on a large archive can take several seconds. Concurrent
  calls wait for the same computation.

## UI files

Any path outside `/api/` is a file of the embedded UI tree.

- `/` serves `index.html`.
- Directories are never listed. A path ending in `/`, other than `/`
  itself, gets `404`.
- Dot-files are not served.
- `Content-Type` comes from the file extension: `.html .js .mjs .css .json
  .svg .png .ico .webp .woff2 .txt .map`. Anything else is
  `application/octet-stream`.
- UI files are sent with `Cache-Control: no-cache`.

Unknown paths get `404`; there is no catch-all route. Use the URL hash or
query for client-side deep links, for example `/#/m/123?q=…`.
