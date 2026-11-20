// Package server is the HTTP side of the viewer: a read-only JSON API over
// the index (status, facets, message list/search, message detail, threads,
// statistics), endpoints that re-read message bytes from their sources (raw
// .eml, attachments, cid: images), the embedded UI, and one security
// envelope around all of it. The API is specified in docs/api.md.
//
// # Use
//
//	srv, err := server.New(server.Config{Store: st, Sources: srcs, Root: input, UI: webFS, Version: v})
//	l, err := server.Listen("127.0.0.1:8080", allowRemote) // ErrNotLoopback otherwise
//	err = srv.HTTPServer().Serve(l)
//
// Messages are found with index.Store.Message and re-read from the Source
// whose Path() equals the indexed source path (Load for the detail view,
// Raw for .eml, Attachment for downloads, FindCID+Attachment for cid:).
// Reads work while the index is being built; ids that vanished (the index
// was rebuilt, ids are never reused) are 404 "not_found", a changed file is
// 410 "stale", a source no longer opened or a missing file 410 "gone".
//
// # Security model (from earlier-tool, see its README)
//
//   - Listen refuses non-loopback addresses unless allowRemote, before and
//     after binding (a "localhost" resolving elsewhere is refused too).
//   - Every response: Host allow-list (localhost, 127.0.0.1, [::1] plus
//     Config.AllowedHosts; the DNS-rebinding defence), GET/HEAD only (405
//     with Allow), no CORS headers ever, X-Content-Type-Options nosniff,
//     Referrer-Policy no-referrer, X-Frame-Options DENY, COOP and CORP
//     same-origin, panic recovery (500 JSON, logged), 414 for a path over
//     4 KiB or a query over 8 KiB, 400 for dot/empty segments, backslashes
//     or NULs (nothing is ever redirected or resolved). http.Server
//     timeouts and a 64 KiB header limit come from HTTPServer.
//   - Content-Security-Policy exactly as documented: the UI gets
//     "default-src 'self'; img-src 'self' data:; style-src 'self'
//     'unsafe-inline'; object-src 'none'; base-uri 'none'; form-action
//     'none'; frame-ancestors 'none'" (the sandboxed srcdoc message frame
//     inherits it); every API response gets "default-src 'none'; img-src
//     'self' data:; style-src 'unsafe-inline'", and raw/attachment/cid
//     responses a second policy "sandbox".
//   - JSON never carries unsanitised HTML: only fields named *Html hold
//     HTML (bodyHtml: the one sanitiser's output; highlightHtml: the
//     index's escaped snippet with <mark>); every other string is text.
//   - Attachments are downloads (Content-Disposition attachment, RFC 6266
//     with an ASCII fallback and RFC 5987 filename*, the name reduced to a
//     safe base name); types outside an allow-list go out as
//     application/octet-stream; only raster images are ever inline.
//   - Error bodies are {"error","code"} with fixed messages; internal
//     errors are logged, never echoed. Source names are shown relative to
//     Config.Root (or as base names) and paths in warnings are scrubbed.
//   - The UI is served from an fs.FS: "/" is index.html, directories are
//     never listed, dot-files are not served, unknown types are
//     application/octet-stream.
//
// # Sanitiser
//
// One allow-list sanitiser (earlier-tool's, tokenizer based) re-serialises
// message HTML: allowed elements and attributes only, text and values
// re-escaped, URLs classified (http(s)/mailto links open in a new tab with
// noopener noreferrer; cid: rewritten; raster data: images kept; remote
// images blocked into data-blocked-src and flagged), CSS decoded and
// checked to a fixpoint (no @import, expression, remote url()), nesting
// capped at 256. From earlier-tool: the <body>'s bgcolor, text and style are
// kept on a wrapping <div>. Small raster cid: images (declared and sniffed
// PNG/JPEG/GIF/WebP, at most 1 MiB each, 4 MiB and 64 parts per message)
// are inlined as data: URIs; others use per-message cid URLs carrying a
// MAC under a per-process key, answered with CORP cross-origin so the
// opaque-origin frame may load them.
//
// # Decisions
//
//   - Deleted messages: the list, threads and facets take
//     deleted=hide|include (and only, for list and threads), passed to
//     the index as SearchParams/ThreadParams.Deleted or FacetsFor, outside
//     the query text, so no query can widen "only". Absent means the
//     server default (Config.IncludeDeleted, reported by /api/status).
//     Statistics always use the default.
//   - Threads are capped at 5000 rows (truncated:true; the requested
//     message is always included; threadCount is the full size).
//   - Tag colours are passed on only as "#RRGGBB", else "".
//   - Message bytes held by concurrent loads are bounded by a 768 MiB
//     budget (three times source.MaxMessageBytes, so one worst-case
//     request fits). A request weighs the message size times the copies
//     it may hold: 1 for raw and attachments, 3 for the detail view and
//     cid (Load's parse, plus Raw and its parse for cid inlining). The
//     size is Loc.Length for mbox (the reader reads exactly that range);
//     for the other formats, whose sizes are not known before reading (or
//     only claimed by the file), it is the 256 MiB a source may
//     materialise at most. So a detail view of a PST, OLM or .eml message
//     takes the whole budget while it is read: such views are served one
//     at a time, which is fast for ordinary messages. Raw, attachment and
//     cid responses drop their reservation to the bytes they send once
//     read, so a slow download does not block other requests. A request
//     that cannot get its budget within 15 s is answered 503 "busy" with
//     Retry-After: 5. Text and HTML in a detail are capped at 8 MiB each
//     (flagged truncated), headers at 1000.
//   - HEAD on raw/attachment/cid re-reads the message like GET (the
//     length is only known after reading; net/http drops the body).
//   - Config.UI is meant to be the embed.FS of internal/web: files are
//     read whole per request, and nothing guards against symlinks in an
//     os.DirFS, so do not pass a directory from disk.
//   - HTTPServer's WriteTimeout (10 min) bounds a whole response, so a
//     very slow client downloading a huge attachment (up to 256 MiB) is
//     cut off; on loopback that is far beyond normal transfer times.
//   - cid inlining re-reads the message once: Raw is parsed and used when
//     its attachments equal Load's (raw-backed formats), otherwise each
//     part comes from Source.Attachment.
//   - Statistics (earlier-tool's: years, months, top senders; plus folders,
//     sources, unread, attachments) take one pass over the listed messages
//     and are cached until the index changes (at most every 30 s while
//     indexing). Years and months are UTC. One mutex guards the cache: while
//     one request computes, concurrent /api/stats requests wait for it
//     (that wait is not cancellable; the computing request stops when its
//     own client goes away, and the next caller starts over).
//   - The raw .eml is named after the subject (earlier-tool), else
//     "message-<id>.eml".
package server
