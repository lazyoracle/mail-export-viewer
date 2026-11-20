package server

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/lazyoracle/mail-export-viewer/internal/index"
)

const (
	maxQueryLen  = 4096 // the index parses at most 4 KiB of query text
	maxCursorLen = 2048 // index cursors stay under ~1.5 KB
	maxFolderLen = 4096
	maxLimit     = 1000
	maxCIDLen    = 998
	// maxStatusWarnings and maxWarningLen bound the warnings per source in
	// /api/status.
	maxStatusWarnings = 20
	maxWarningLen     = 300
)

var validSorts = map[string]bool{"": true, "date_desc": true, "date_asc": true, "from": true, "subject": true, "relevance": true}

// route dispatches /api/ requests. It splits the escaped path itself and
// unescapes each segment, so an encoded "/" or ".." inside an id or a
// Content-ID stays inside its segment and nothing is ever redirected.
func (s *Server) route(w http.ResponseWriter, r *http.Request) {
	rest, ok := strings.CutPrefix(r.URL.EscapedPath(), "/api/")
	if !ok || rest == "" {
		writeError(w, http.StatusNotFound, codeNotFound, "not found")
		return
	}
	segs := strings.Split(rest, "/")
	for i, seg := range segs {
		u, err := url.PathUnescape(seg)
		if err != nil {
			writeError(w, http.StatusBadRequest, codeBadRequest, "bad path")
			return
		}
		segs[i] = u
	}
	n := len(segs)
	switch {
	case n == 1 && segs[0] == "status":
		s.handleStatus(w, r)
	case n == 1 && segs[0] == "facets":
		s.handleFacets(w, r)
	case n == 1 && segs[0] == "stats":
		s.handleStats(w, r)
	case n == 1 && segs[0] == "messages":
		s.handleMessages(w, r)
	case n == 2 && segs[0] == "threads":
		s.handleThread(w, r, segs[1])
	case n == 2 && segs[0] == "messages":
		s.handleMessage(w, r, segs[1])
	case n == 3 && segs[0] == "messages" && segs[2] == "raw":
		s.handleRaw(w, r, segs[1])
	case n == 4 && segs[0] == "messages" && segs[2] == "attachments":
		s.handleAttachment(w, r, segs[1], segs[3])
	case n == 4 && segs[0] == "messages" && segs[2] == "cid":
		s.handleCID(w, r, segs[1], "", segs[3])
	case n == 5 && segs[0] == "messages" && segs[2] == "cid":
		s.handleCID(w, r, segs[1], segs[3], segs[4])
	default:
		writeError(w, http.StatusNotFound, codeNotFound, "not found")
	}
}

// parseID parses a positive decimal id: digits only, no sign, no spaces.
func parseID(s string) (int64, error) {
	if s == "" || len(s) > 19 || strings.Trim(s, "0123456789") != "" {
		return 0, badRequest("invalid id")
	}
	id, err := strconv.ParseInt(s, 10, 64)
	if err != nil || id <= 0 {
		return 0, badRequest("invalid id")
	}
	return id, nil
}

// parseIndex parses an attachment index (0 or more).
func parseIndex(s string) (int, error) {
	if s == "" || len(s) > 6 || strings.Trim(s, "0123456789") != "" {
		return 0, badRequest("invalid attachment index")
	}
	n, _ := strconv.Atoi(s)
	return n, nil
}

// query parses the query string, refusing repeated parameters (which
// would be silently ambiguous).
func query(r *http.Request) (url.Values, error) {
	q, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return nil, badRequest("malformed query string")
	}
	for k, v := range q {
		if len(v) > 1 {
			return nil, badRequest("repeated parameter " + strconv.Quote(truncate(k, 40)))
		}
	}
	return q, nil
}

func searchParams(r *http.Request) (index.SearchParams, error) {
	var p index.SearchParams
	q, err := query(r)
	if err != nil {
		return p, err
	}
	p.Q = q.Get("q")
	if len(p.Q) > maxQueryLen {
		return p, badRequest("query too long")
	}
	for name, dst := range map[string]*int64{"source": &p.SourceID, "folderId": &p.FolderID} {
		if v := q.Get(name); v != "" {
			id, err := parseID(v)
			if err != nil {
				return p, badRequest("invalid " + name)
			}
			*dst = id
		}
	}
	p.Folder = q.Get("folder")
	if len(p.Folder) > maxFolderLen {
		return p, badRequest("folder too long")
	}
	switch q.Get("deleted") {
	case "":
	case "only":
		p.Q += " is:deleted"
	default:
		return p, badRequest(`deleted must be "only" or absent`)
	}
	p.Sort = q.Get("sort")
	if !validSorts[p.Sort] {
		return p, badRequest("invalid sort")
	}
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > maxLimit || strings.Trim(v, "0123456789") != "" {
			return p, badRequest("limit must be between 1 and 1000")
		}
		p.Limit = n
	}
	p.Cursor = q.Get("cursor")
	if len(p.Cursor) > maxCursorLen {
		return p, index.ErrBadCursor
	}
	return p, nil
}

func (s *Server) handleMessages(w http.ResponseWriter, r *http.Request) {
	p, err := searchParams(r)
	if err != nil {
		s.fail(w, err)
		return
	}
	res, err := s.st.Search(p)
	if err != nil {
		s.fail(w, err)
		return
	}
	out := listJSON{Items: make([]itemJSON, 0, len(res.Items)), Total: res.Total, NextCursor: res.NextCursor}
	for _, row := range res.Items {
		it := toItem(row)
		it.ThreadCount = res.ThreadCounts[row.ID]
		it.HighlightHTML = res.Highlights[row.ID] // escaped by the index except <mark>
		out.Items = append(out.Items, it)
	}
	s.writeJSON(w, out)
}

func (s *Server) handleThread(w http.ResponseWriter, r *http.Request, idText string) {
	id, err := parseID(idText)
	if err != nil {
		s.fail(w, err)
		return
	}
	rows, err := s.st.Thread(id)
	if err == nil && len(rows) == 0 {
		err = index.ErrNotFound
	}
	if err != nil {
		s.fail(w, err)
		return
	}
	items := make([]itemJSON, 0, len(rows))
	for _, row := range rows {
		it := toItem(row)
		it.ThreadCount = len(rows)
		items = append(items, it)
	}
	s.writeJSON(w, map[string]any{"items": items})
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	infos, err := s.st.Sources()
	if err != nil {
		s.fail(w, err)
		return
	}
	s.writeJSON(w, s.status(s.st.Status(), infos))
}

// status merges the live progress of the current or last run with the
// persisted state of every indexed source (keyed by path): sources of the
// run first, in run order, then sources known only to the index. Warnings
// and errors are bounded and scrubbed of paths.
func (s *Server) status(p index.Progress, infos []index.SourceInfo) statusJSON {
	var paths []string
	byPath := map[string]index.SourceInfo{}
	for _, si := range infos {
		byPath[si.Path] = si
		paths = append(paths, si.Path)
	}
	for _, sp := range p.Sources {
		paths = append(paths, sp.Path)
	}
	out := statusJSON{Version: s.version, IncludeDeleted: s.includeDeleted, Sources: []statusSource{},
		Indexing: indexingJSON{Running: p.Running, Finished: p.Finished, Messages: p.Messages, Warnings: p.Warnings,
			Error: s.scrub(truncate(p.Err, maxWarningLen), paths...)}}
	inRun := map[string]bool{}
	add := func(src statusSource, si index.SourceInfo, ok bool) {
		if ok {
			src.ID, src.Count, src.Unread, src.Complete = si.ID, si.Count, si.Unread, si.Complete
			if src.WarningCount == 0 {
				src.WarningCount = si.WarningCount
			}
			if src.Error == "" {
				src.Error = si.Error
			}
			for i, wmsg := range si.Warnings {
				if i == maxStatusWarnings {
					break
				}
				src.Warnings = append(src.Warnings, s.scrub(truncate(wmsg, maxWarningLen), paths...))
			}
		}
		src.Error = s.scrub(truncate(src.Error, maxWarningLen), paths...)
		src.Warnings = nonNil(src.Warnings)
		out.Sources = append(out.Sources, src)
	}
	for _, sp := range p.Sources {
		inRun[sp.Path] = true
		si, ok := byPath[sp.Path]
		add(statusSource{Name: s.sourceName(sp.Path), Kind: sp.Kind, State: sp.State, Unit: sp.Unit, Done: sp.Done,
			Total: sp.Total, Messages: sp.Messages, WarningCount: sp.Warnings, Error: sp.Error}, si, ok)
	}
	for _, si := range infos {
		if inRun[si.Path] {
			continue
		}
		state := stateIncomplete
		switch {
		case si.Error != "":
			state = index.StateFailed
		case si.Complete:
			state = index.StateCached
		}
		add(statusSource{Name: s.sourceName(si.Path), Kind: si.Kind, State: state}, si, true)
	}
	return out
}

// stateIncomplete is the state of a source known to the index, not part of
// the current run, whose last indexing did not complete.
const stateIncomplete = "incomplete"

// truncate cuts s to at most n bytes on a rune boundary, adding "…".
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n] + "…"
}

func (s *Server) handleFacets(w http.ResponseWriter, r *http.Request) {
	fc, err := s.st.Facets()
	if err != nil {
		s.fail(w, err)
		return
	}
	infos, err := s.st.Sources()
	if err != nil {
		s.fail(w, err)
		return
	}
	out := facetsJSON{Sources: []sourceJSON{}, Folders: s.folders(fc.Folders), Labels: terms(fc.Labels),
		Categories: terms(fc.Categories), Tags: terms(fc.Tags), Flags: []flagJSON{}}
	for _, si := range infos {
		out.Sources = append(out.Sources, sourceJSON{ID: si.ID, Name: s.sourceName(si.Path), Kind: si.Kind, Count: si.Count, Unread: si.Unread})
	}
	for _, f := range fc.Flags {
		out.Flags = append(out.Flags, flagJSON{Name: f.Name, Count: f.Count})
	}
	s.writeJSON(w, out)
}

func (s *Server) folders(ns []index.FolderNode) []folderJSON {
	out := make([]folderJSON, 0, len(ns))
	for _, n := range ns {
		f := folderJSON{Name: n.Name, Path: n.Path, Count: n.Count, Unread: n.Unread,
			Sources: make([]folderRefJSON, 0, len(n.Sources)), Children: s.folders(n.Children)}
		for _, ref := range n.Sources {
			f.Sources = append(f.Sources, folderRefJSON{FolderID: ref.ID, SourceID: ref.SourceID, SourceKind: ref.SourceKind,
				Source: s.sourceName(ref.SourcePath), Count: ref.Count, Unread: ref.Unread})
		}
		out = append(out, f)
	}
	return out
}

func terms(ts []index.TermCount) []termJSON {
	out := make([]termJSON, 0, len(ts))
	for _, t := range ts {
		out = append(out, termJSON{Name: t.Name, Color: t.Color, Count: t.Count, Unread: t.Unread})
	}
	return out
}
