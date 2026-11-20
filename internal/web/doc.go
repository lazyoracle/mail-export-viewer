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
// format, text, debounce, shortcuts, theme, clipboard, srcdoc, and logic
// (the controller's pure decisions). app.js is the controller that wires
// them to the page; jstest/app.test.js runs it on the real index.html
// skeleton with a fake fetch, and browsertest/ui.mjs drives it in headless
// Chromium (TestBrowser, when MEV_CHROME is set; screenshots never go
// into the repository).
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
//     scroll rather than DOM virtualisation. Paging, linkify and the other
//     text helpers are linear in their input (timed tests with 300k-char
//     hostile tails); URL candidates over 2048 characters stay text.
//   - Typing in the search box only flips the pressed state of sidebar
//     entries; the sidebar is rebuilt when the route changes. Regrouping
//     the list by conversation and mounting a long thread happen a chunk
//     per animation frame.
//   - List rows are listbox options, which must not contain controls, so
//     their label/category/tag pills are plain spans (a mouse click still
//     filters through the list's delegated handler); the keyboard path to
//     a term is the reader's pill buttons or the sidebar.
//   - Attachment links are built from the message id and part index, not
//     taken from the payload's url.
//   - Collapsed tree state is read from localStorage as an array of string
//     keys only and pruned to the current tree on every facets load.
//   - Theme: system by default, toggled light/dark, stored in localStorage
//     (theme-init.js applies it before first paint). The message body
//     always renders on a light "paper" ground, as most HTML mail assumes.
//   - Layout: three panes; below 1100px the sidebar becomes a drawer;
//     below 760px one pane shows at a time.
package web
