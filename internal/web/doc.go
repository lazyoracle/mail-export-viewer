// Package web embeds the browser UI: one page (static/index.html) of
// vanilla ES modules with no build step, no npm dependencies and no
// external resources (no CDN, webfonts or remote images; system font
// stacks only). FS returns it for server.Config.UI. The API it uses is
// docs/api.md.
//
// # Modules (static/js)
//
// Pure, unit-tested under node (jstest, `make test-js`, built-in runner
// with a small fake DOM that has no HTML parser): api (client, deleted
// modes), router (hash routes), state (paging, progress, warnings),
// querychips (tokenizer mirroring internal/index/query.go, chips,
// operator autocomplete), labels (labels, categories, coloured tags),
// flags, tree (folder tree grouped by source), list, reader, stats,
// format, text, debounce, shortcuts, theme, clipboard, srcdoc. app.js is
// the controller that wires them to the page; it is checked in a real
// browser rather than under node.
//
// # Decisions
//
//   - Every string from the API goes into the DOM as text (textContent,
//     attributes). Only highlightHtml is read, and only its <mark> tags are
//     honoured (text.highlightSegments). bodyHtml is shown only in an
//     iframe with sandbox="allow-popups allow-popups-to-escape-sandbox"
//     and srcdoc, never with scripts or same-origin; the frame inherits
//     the UI's CSP. Tests forbid innerHTML-style sinks and inline scripts,
//     styles and handlers.
//   - Deep links live in the URL hash (#/, #/m/<id>, #/stats, with
//     ?q=&sort=&src=&fid=&folder=&deleted=) because the server has no
//     catch-all route. A stale id (index rebuilt) is explained, not shown
//     as a crash.
//   - The folder tree is cut per source from the merged /api/facets tree;
//     wrappers that only repeat the file name are dropped. Source rows
//     list ?source=, folders ?folderId=; statistics link folders by path
//     (?folder=).
//   - The deleted toggle is the API's per-request deleted=hide|include|only
//     (absent: the server default from /api/status includeDeleted), sent
//     to the list, threads and facets (facets count "only" as include).
//   - Conversations: the API has no grouped list, so the "Conversations"
//     toggle keeps the first row of each threadId among the loaded pages
//     and opens the thread view; counts stay message counts. Threads are
//     capped by the server (truncated) and the reader says so.
//   - The list pages through keyset cursors (100 per page) with infinite
//     scroll rather than DOM virtualisation.
//   - Theme: system by default, toggled light/dark, stored in localStorage
//     (theme-init.js applies it before first paint). The message body
//     always renders on a light "paper" ground, as most HTML mail assumes.
//   - Layout: three panes; below 1100px the sidebar becomes a drawer;
//     below 760px one pane shows at a time.
package web
