package index

import (
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"html"
	"strconv"
	"strings"

	"github.com/lazyoracle/mail-export-viewer/internal/source"
)

// SearchParams selects a page of messages. An empty Q lists all messages
// (of the scope, if any) in Sort order.
type SearchParams struct {
	Q string
	// Scope (all optional, ANDed), for clicks in the folder tree:
	// SourceID one source; FolderID one source's folder
	// (FolderRef.ID); Folder one node of the merged tree (FolderNode.Path,
	// matched exactly and case-sensitively, in every source; subfolders
	// are not included). The folder: operator in Q is looser: a path or
	// its last segment(s), case-insensitively.
	SourceID, FolderID int64
	Folder             string
	Sort               string // date_desc (default)|date_asc|from|subject|relevance
	// Deleted chooses how messages flagged Deleted are treated (default:
	// as Options.IncludeDeleted says). It is applied outside the query
	// text, so no query can widen DeletedOnly.
	Deleted DeletedMode
	Limit   int    // default 50, at most 1000
	Cursor  string // NextCursor of the previous page, "" for the first
}

// SearchResult is one page of results.
//
// Total is the number of matching messages, computed only for the first
// page (Cursor == ""); later pages report -1. Highlights maps a message id
// to an HTML snippet of its best-matching text: every character is
// HTML-escaped except the <mark>…</mark> tags around matched terms; it is
// safe to insert as HTML. Messages without a positive full-text match have
// no entry. ThreadCounts maps each returned message id to the size of its
// thread.
type SearchResult struct {
	Items        []Row
	Total        int64
	NextCursor   string
	Highlights   map[int64]string
	ThreadCounts map[int64]int
}

// DeletedMode says which messages flagged Deleted a Search or ThreadPage
// shows.
type DeletedMode int

const (
	// DeletedDefault follows Options.IncludeDeleted (Include if set, else
	// Hide).
	DeletedDefault DeletedMode = iota
	// DeletedHide leaves them out, unless the query has is:deleted.
	DeletedHide
	// DeletedInclude shows them like any other message.
	DeletedInclude
	// DeletedOnly shows nothing else.
	DeletedOnly
)

// resolveDeleted turns DeletedDefault into Hide or Include.
func (s *Store) resolveDeleted(m DeletedMode) DeletedMode {
	if m == DeletedHide || m == DeletedInclude || m == DeletedOnly {
		return m
	}
	if s.opts.IncludeDeleted {
		return DeletedInclude
	}
	return DeletedHide
}

// onlyDeletedCond keeps only Deleted messages.
var onlyDeletedCond = fmt.Sprintf(`m.flags & %d != 0`, source.Deleted)

// ErrBadCursor is returned for a cursor that was not produced by Search with
// the same sort order.
var ErrBadCursor = errors.New("index: invalid search cursor")

const (
	defaultLimit = 50
	maxLimit     = 1000
	// bm25Weights weights the FTS columns subject, from_text, to_text, body,
	// att_names for relevance ranking.
	bm25Weights = "10.0, 5.0, 3.0, 1.0, 2.0"
	// hlOpen/hlClose mark matches in snippet() output (private-use runes)
	// before HTML escaping replaces them with <mark> tags.
	hlOpen  = "\uE000"
	hlClose = "\uE001"
	// snippetTokens is the highlight snippet length in tokens.
	snippetTokens = 20
	commonMax     = 50000
)

// commonMin is the lower bound of the isCommon probe (a variable so tests
// can force the common-match plan on small fixtures).
var commonMin int64 = 2000

// deletedCond hides Deleted messages.
var deletedCond = fmt.Sprintf(`m.flags & %d = 0`, source.Deleted)

// compiled is a Query turned into SQL. conds/args are ANDed WHERE terms over
// messages m; match is the FTS5 expression the result must match ("" for
// none); hl is the positive part of it, used for snippets.
type compiled struct {
	conds     []string
	args      []any
	match, hl string
	// scan: some condition cannot use an index (LIKE, NOT IN, flags), so
	// without a full-text match a sequential scan with a top-N sort beats
	// walking the date index with a random row lookup per message.
	scan bool
	// prefix: match contains a prefix term.
	prefix bool
	// common: match selects a large share of all messages (see isCommon).
	common bool
	// filtered: the query has a condition besides the scope and the
	// hidden-deleted filter (so the count cannot come from folder totals).
	filtered bool
	// hideDeleted: deletedCond is among conds.
	hideDeleted bool
	// onlyDeleted: onlyDeletedCond is among conds.
	onlyDeleted bool
	// flagFilter: the query tests flags (is:, has:attachment).
	flagFilter bool
}

func (c *compiled) add(cond string, args ...any) {
	c.conds = append(c.conds, cond)
	c.args = append(c.args, args...)
	c.filtered = true
}

// addScan adds a condition that no index can serve.
func (c *compiled) addScan(cond string, args ...any) {
	c.add(cond, args...)
	c.scan = true
}

const ftsIn = `m.id IN (SELECT rowid FROM messages_fts WHERE messages_fts MATCH ?)`

func likeEscape(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

// cjkCond matches v by FTS (expr) or as a substring of any of cols.
// Without ASCII letters (LIKE folds only ASCII case) the substring test is
// instr(), which is cheaper than LIKE over every row.
func cjkCond(expr, v string, cols ...string) (string, []any) {
	parts := []string{ftsIn}
	args := []any{expr}
	if hasASCIILetter(v) {
		pat := "%" + likeEscape(v) + "%"
		for _, col := range cols {
			parts = append(parts, "m."+col+` LIKE ? ESCAPE '\'`)
			args = append(args, pat)
		}
	} else {
		for _, col := range cols {
			parts = append(parts, "instr(m."+col+", ?) > 0")
			args = append(args, v)
		}
	}
	return "(" + strings.Join(parts, " OR ") + ")", args
}

func hasASCIILetter(s string) bool {
	for i := 0; i < len(s); i++ {
		if c := s[i] | 0x20; c >= 'a' && c <= 'z' {
			return true
		}
	}
	return false
}

// termCond selects messages linked to a term of the given kinds named v
// (case-insensitive for ASCII).
func termCond(kinds string, v string) (string, any) {
	return `m.id IN (SELECT mt.message_id FROM terms t JOIN message_terms mt ON mt.term_id = t.id
		WHERE t.kind IN (` + kinds + `) AND t.name = ? COLLATE NOCASE)`, v
}

// ccCond selects messages whose Cc holds v: the FTS to/cc column has the
// words (as for to:) and the Cc text the substring; CJK terms (one long
// token in FTS) by substring only. -cc: is exactly its negation. "" for a
// term without token characters (no condition).
func ccCond(v string) (string, []any) {
	if !hasTokenChars(v) {
		return "", nil
	}
	like := `m.cc_text LIKE ? ESCAPE '\'`
	pat := "%" + likeEscape(v) + "%"
	if isCJK(v) {
		if hasASCIILetter(v) {
			return like, []any{pat}
		}
		return `instr(m.cc_text, ?) > 0`, []any{v}
	}
	return "(" + ftsIn + " AND " + like + ")", []any{"to_text : " + ftsQuote(v), pat}
}

// folderCond selects messages in folders whose path is v or ends in "/v".
func folderCond(v string) (string, []any) {
	return `m.folder_id IN (SELECT id FROM folders WHERE path = ? COLLATE NOCASE OR path LIKE ? ESCAPE '\')`,
		[]any{v, "%/" + likeEscape(v)}
}

func compileQuery(q Query, includeDeleted bool) compiled {
	var c compiled
	var pos, neg []string
	// text adds a positive full-text item; col is "" for all columns.
	text := func(v, col string, prefix bool, likeCols ...string) {
		if !hasTokenChars(v) {
			return
		}
		expr := ftsQuote(v)
		if col != "" {
			expr = col + " : " + expr
		}
		if isCJK(v) {
			cond, args := cjkCond(expr, v, likeCols...)
			c.addScan(cond, args...)
			return
		}
		if prefix {
			expr += "*"
			c.prefix = true
		}
		pos = append(pos, expr)
	}
	// LIKE/instr fallback columns (messages table) behind each FTS column.
	textCols := []string{"subject", "from_name", "from_email"}
	fromCols := []string{"from_name", "from_email"}
	toCols := []string{"to_text", "cc_text"} // FTS to_text holds To and Cc
	subjectCols := []string{"subject"}
	for i, t := range q.Terms {
		text(t, "", q.prefixLast && i == len(q.Terms)-1 && prefixable(t), textCols...)
	}
	for _, t := range q.Phrases {
		text(t, "", false, textCols...)
	}
	for _, t := range q.From {
		text(t, "from_text", false, fromCols...)
	}
	for _, t := range q.To {
		text(t, "to_text", false, toCols...)
	}
	for _, t := range q.Subject {
		text(t, "subject", false, subjectCols...)
	}
	for _, t := range q.Cc {
		if cond, args := ccCond(t); cond != "" {
			c.addScan(cond, args...)
		}
	}
	// notText adds a negative full-text item; col is "" for all columns.
	notText := func(v, col string, likeCols ...string) {
		if !hasTokenChars(v) {
			return
		}
		expr := ftsQuote(v)
		if col != "" {
			expr = col + " : " + expr
		}
		if isCJK(v) {
			cond, args := cjkCond(expr, v, likeCols...)
			c.addScan("NOT "+cond, args...)
			return
		}
		neg = append(neg, expr)
	}
	for _, t := range q.Not {
		notText(t, "", textCols...)
	}
	for _, t := range q.NotFrom {
		notText(t, "from_text", fromCols...)
	}
	for _, t := range q.NotTo {
		notText(t, "to_text", toCols...)
	}
	for _, t := range q.NotSubject {
		notText(t, "subject", subjectCols...)
	}
	for _, t := range q.NotCc {
		if cond, args := ccCond(t); cond != "" {
			c.addScan("NOT "+cond, args...)
		}
	}
	c.hl = strings.Join(pos, " AND ")
	c.match = c.hl
	if len(neg) > 0 {
		if c.match != "" {
			c.match = "(" + c.match + ") NOT (" + strings.Join(neg, " OR ") + ")"
		} else {
			// Tested on the rowid, which every index holds: walking the
			// date index stays cheap, so this is no scan condition.
			c.add(`m.id NOT IN (SELECT rowid FROM messages_fts WHERE messages_fts MATCH ?)`,
				strings.Join(neg, " OR "))
		}
	}
	if c.match != "" {
		c.filtered = true
	}

	for _, v := range q.Folder {
		cond, args := folderCond(v)
		c.add(cond, args...)
	}
	for _, v := range q.NotFolder {
		cond, args := folderCond(v)
		c.add("NOT "+cond, args...)
	}
	labelKinds := fmt.Sprintf("%d, %d", kindLabel, kindCategory)
	for _, l := range []struct {
		kinds    string
		pos, neg []string
	}{
		{labelKinds, q.Label, q.NotLabel},
		{strconv.Itoa(kindCategory), q.Category, q.NotCategory},
		{strconv.Itoa(kindTag), q.Tag, q.NotTag},
	} {
		for _, v := range l.pos {
			cond, arg := termCond(l.kinds, v)
			c.add(cond, arg)
		}
		for _, v := range l.neg {
			cond, arg := termCond(l.kinds, v)
			c.add("NOT "+cond, arg) // on the rowid, like the negated match
		}
	}
	for _, v := range q.Filename {
		c.addScan(`m.att_names LIKE ? ESCAPE '\'`, "%"+likeEscape(v)+"%")
	}
	for _, v := range q.NotFilename {
		c.addScan(`m.att_names NOT LIKE ? ESCAPE '\'`, "%"+likeEscape(v)+"%")
	}
	// The date index holds the flags, so flag tests need no table scan:
	// the walk in date order tests them in the index, and counts use it
	// as a covering index.
	if q.Is != 0 {
		c.add(`m.flags & ? = ?`, int64(q.Is), int64(q.Is))
		c.flagFilter = true
	}
	if q.NotIs != 0 {
		c.add(`m.flags & ? = 0`, int64(q.NotIs))
		c.flagFilter = true
	}
	if q.Before != nil {
		c.add(`m.date_unix < ?`, q.Before.Unix())
	}
	if q.After != nil {
		c.add(`m.date_unix >= ?`, q.After.Unix())
	}
	if q.NotBefore != nil {
		c.add(`(m.date_unix IS NULL OR m.date_unix >= ?)`, q.NotBefore.Unix())
	}
	if q.NotAfter != nil {
		c.add(`(m.date_unix IS NULL OR m.date_unix < ?)`, q.NotAfter.Unix())
	}
	if !includeDeleted && !q.Is.Has(source.Deleted) {
		c.conds = append(c.conds, deletedCond)
		c.hideDeleted = true
	}
	return c
}

// cursor is the decoded position after the last item of a page. Date sorts
// use seg (0: dated messages, 1: unknown date), num (date_unix) and id;
// from/subject use str and id; relevance uses num as an offset.
type cursor struct {
	sort string
	seg  int
	num  int64
	id   int64
	str  string
}

func (c cursor) encode() string {
	return base64.RawURLEncoding.EncodeToString(
		fmt.Appendf(nil, "%s|%d|%d|%d|%s", c.sort, c.seg, c.num, c.id, c.str))
}

func decodeCursor(s, sort string) (cursor, error) {
	b, err := base64.RawURLEncoding.Strict().DecodeString(s)
	if err != nil {
		return cursor{}, ErrBadCursor
	}
	f := strings.SplitN(string(b), "|", 5)
	if len(f) != 5 || f[0] != sort {
		return cursor{}, ErrBadCursor
	}
	var c cursor
	c.sort, c.str = f[0], f[4]
	var e1, e2, e3 error
	c.seg, e1 = strconv.Atoi(f[1])
	c.num, e2 = strconv.ParseInt(f[2], 10, 64)
	c.id, e3 = strconv.ParseInt(f[3], 10, 64)
	if e1 != nil || e2 != nil || e3 != nil || c.seg < 0 || c.seg > 1 || c.num < 0 && sort == "relevance" {
		return cursor{}, ErrBadCursor
	}
	if c.encode() != s { // canonical form only: rejects any tampering
		return cursor{}, ErrBadCursor
	}
	return c, nil
}

func normalizeSort(s string) string {
	switch s {
	case "date_asc", "from", "subject", "relevance":
		return s
	}
	return "date_desc"
}

// Search returns one page of messages matching p.Q (see Query for the
// syntax) within p's scope. Malformed query text never causes an error;
// only database errors and a bad cursor do. Deleted messages are left out
// unless Options.IncludeDeleted or the query has is:deleted.
//
// Ordering: date sorts put messages with unknown dates last and break ties
// by id (in the sort direction); from/subject sort case-insensitively, ties
// by id; relevance ranks by bm25 (subject and sender weighted higher), ties
// by id, and falls back to date_desc when the query has no full-text term.
// Pagination is keyset-based (stable across equal keys) except for
// relevance, which pages by offset.
func (s *Store) Search(p SearchParams) (SearchResult, error) {
	// Without Deleted messages in the index there is nothing to hide, and
	// leaving the filter out keeps the cheap plans available.
	mode := s.resolveDeleted(p.Deleted)
	var anyDeleted bool
	if mode == DeletedHide {
		if err := s.rdb.QueryRow(`SELECT EXISTS(SELECT 1 FROM folders WHERE deleted > 0)`).Scan(&anyDeleted); err != nil {
			return SearchResult{}, fmt.Errorf("index: search: %w", err)
		}
	}
	c := compileQuery(ParseQuery(p.Q), mode != DeletedHide || !anyDeleted)
	if mode == DeletedOnly {
		// Not "filtered": an unfiltered count comes from folders.deleted.
		c.conds = append(c.conds, onlyDeletedCond)
		c.scan, c.onlyDeleted = true, true
	}
	var scope []string
	var scopeArgs []any
	if p.SourceID != 0 {
		scope, scopeArgs = append(scope, `m.source_id = ?`), append(scopeArgs, p.SourceID)
	}
	if p.FolderID != 0 {
		scope, scopeArgs = append(scope, `m.folder_id = ?`), append(scopeArgs, p.FolderID)
	}
	if p.Folder != "" {
		scope = append(scope, `m.folder_id IN (SELECT id FROM folders WHERE path = ?)`)
		scopeArgs = append(scopeArgs, p.Folder)
	}
	c.conds = append(c.conds, scope...)
	c.args = append(c.args, scopeArgs...)

	sort := normalizeSort(p.Sort)
	if sort == "relevance" && c.match == "" {
		sort = "date_desc"
	}
	limit := p.Limit
	if limit <= 0 {
		limit = defaultLimit
	}
	limit = min(limit, maxLimit)
	var cur *cursor
	if p.Cursor != "" {
		cc, err := decodeCursor(p.Cursor, sort)
		if err != nil {
			return SearchResult{}, err
		}
		cur = &cc
	}
	var err error
	if c.match != "" && sort != "relevance" {
		if c.common, err = s.isCommon(c.match); err != nil {
			return SearchResult{}, err
		}
	}
	res := SearchResult{Total: -1, Highlights: map[int64]string{}, ThreadCounts: map[int64]int{}}
	switch sort {
	case "relevance":
		res.Items, res.NextCursor, err = s.pageRelevance(c, limit, cur)
	case "from", "subject":
		res.Items, res.NextCursor, err = s.pageByKey(c, sort, limit, cur)
	default:
		res.Items, res.NextCursor, err = s.pageByDate(c, sort, limit, cur)
	}
	if err != nil {
		return SearchResult{}, err
	}
	if cur == nil {
		if res.Total, err = s.count(c, p); err != nil {
			return SearchResult{}, err
		}
	}
	if err := s.highlight(c, res.Items, res.Highlights); err != nil {
		return SearchResult{}, err
	}
	if err := s.threadCounts(res.Items, res.ThreadCounts, mode == DeletedHide); err != nil {
		return SearchResult{}, err
	}
	return res, nil
}

// where returns the WHERE clause (with leading " WHERE ") for c plus extra
// conditions, and the args; the FTS match is added when withMatch is set.
func (c compiled) where(withMatch bool, extra []string, extraArgs ...any) (string, []any) {
	return c.whereMode(withMatch, false, extra, extraArgs...)
}

// whereMode is where; forCount never uses the common-match plan (which
// only helps to find the first rows in sort order).
func (c compiled) whereMode(withMatch, forCount bool, extra []string, extraArgs ...any) (string, []any) {
	conds := append([]string(nil), c.conds...)
	args := append([]any(nil), c.args...)
	if withMatch && c.match != "" {
		if c.common && !forCount {
			// The unary + stops the planner driving the query from the
			// match set (a row lookup per match, then a sort); it walks an
			// index in sort order and tests membership instead.
			conds = append(conds, "+"+ftsIn)
		} else {
			conds = append(conds, ftsIn)
		}
		args = append(args, c.match)
	}
	conds = append(conds, extra...)
	args = append(args, extraArgs...)
	if len(conds) == 0 {
		return "", args
	}
	return " WHERE " + strings.Join(conds, " AND "), args
}

func (s *Store) pageByDate(c compiled, sort string, limit int, cur *cursor) ([]Row, string, error) {
	dir, cmp := "DESC", "<"
	if sort == "date_asc" {
		dir, cmp = "ASC", ">"
	}
	date := `m.date_unix`
	from := ` FROM messages m`
	switch {
	case c.scan && c.match == "":
		date = `+m.date_unix` // keep the planner off the date index
	case c.flagFilter && c.match == "":
		// Without statistics the planner prefers messages_date and looks
		// up every row to test its flags; this index has them.
		from = ` FROM messages m INDEXED BY messages_date_flags`
	}
	want := limit + 1
	var items []Row
	startSeg := 0
	if cur != nil {
		startSeg = cur.seg
	}
	for seg := startSeg; seg <= 1 && len(items) < want; seg++ {
		var extra []string
		var args []any
		var order string
		if seg == 0 {
			extra = []string{date + ` IS NOT NULL`}
			order = ` ORDER BY ` + date + ` ` + dir + `, m.id ` + dir
			if cur != nil && cur.seg == 0 {
				// Row-value comparison: a single index range, unlike an OR.
				extra = append(extra, `(`+date+`, m.id) `+cmp+` (?, ?)`)
				args = append(args, cur.num, cur.id)
			}
		} else {
			extra = []string{date + ` IS NULL`}
			order = ` ORDER BY m.id ` + dir
			if cur != nil && cur.seg == 1 {
				extra = append(extra, `m.id `+cmp+` ?`)
				args = append(args, cur.id)
			}
		}
		w, wargs := c.where(true, extra, args...)
		got, _, err := s.queryRows(`SELECT `+rowCols+from+w+order+` LIMIT ?`,
			append(wargs, want-len(items)), nil)
		if err != nil {
			return nil, "", err
		}
		items = append(items, got...)
	}
	if len(items) <= limit {
		return items, "", nil
	}
	items = items[:limit]
	last := items[limit-1]
	nc := cursor{sort: sort, id: last.ID}
	if last.Date.IsZero() {
		nc.seg = 1
	} else {
		nc.num = last.Date.Unix()
	}
	return items, nc.encode(), nil
}

func (s *Store) pageByKey(c compiled, sort string, limit int, cur *cursor) ([]Row, string, error) {
	key := `m.subject`
	if sort == "from" {
		key = `COALESCE(NULLIF(m.from_name, ''), m.from_email)`
	}
	var extra []string
	var args []any
	if cur != nil {
		extra = []string{`(` + key + ` > ? COLLATE NOCASE OR (` + key + ` = ? COLLATE NOCASE AND m.id > ?))`}
		args = []any{cur.str, cur.str, cur.id}
	}
	w, wargs := c.where(true, extra, args...)
	q := `SELECT ` + rowCols + `, ` + key + ` FROM messages m` + w +
		` ORDER BY ` + key + ` COLLATE NOCASE, m.id LIMIT ?`
	items, keys, err := s.queryRows(q, append(wargs, limit+1), func() any { return new(string) })
	if err != nil {
		return nil, "", err
	}
	if len(items) <= limit {
		return items, "", nil
	}
	items = items[:limit]
	nc := cursor{sort: sort, id: items[limit-1].ID, str: *keys[limit-1].(*string)}
	return items, nc.encode(), nil
}

func (s *Store) pageRelevance(c compiled, limit int, cur *cursor) ([]Row, string, error) {
	var off int64
	if cur != nil {
		off = cur.num
	}
	w, args := c.where(false, []string{`messages_fts MATCH ?`}, c.match)
	q := `SELECT ` + rowCols + ` FROM messages_fts JOIN messages m ON m.id = messages_fts.rowid` + w +
		` ORDER BY bm25(messages_fts, ` + bm25Weights + `), m.id LIMIT ? OFFSET ?`
	items, _, err := s.queryRows(q, append(args, limit+1, off), nil)
	if err != nil {
		return nil, "", err
	}
	if len(items) <= limit {
		return items, "", nil
	}
	items = items[:limit]
	return items, cursor{sort: "relevance", num: off + int64(limit)}.encode(), nil
}

// isCommon reports whether match selects at least ~1/20 of all messages
// (and at least commonMin), probing at most that many FTS rows. For such
// a match, walking the date index and testing membership finds a page much
// faster than looking up and sorting every match.
func (s *Store) isCommon(match string) (bool, error) {
	var total int64
	if err := s.rdb.QueryRow(`SELECT COALESCE(SUM(total), 0) FROM folders`).Scan(&total); err != nil {
		return false, fmt.Errorf("index: search: %w", err)
	}
	threshold := min(max(total/20, commonMin), commonMax)
	var n int64
	err := s.rdb.QueryRow(`SELECT count(*) FROM (SELECT rowid FROM messages_fts WHERE messages_fts MATCH ? LIMIT ?)`,
		match, threshold).Scan(&n)
	if err != nil {
		return false, fmt.Errorf("index: search: %w", err)
	}
	return n >= threshold, nil
}

// count returns the number of messages matching c. An unfiltered listing
// (all, one source, one folder or folder path) is answered from the
// per-folder counters.
func (s *Store) count(c compiled, p SearchParams) (int64, error) {
	var n int64
	var err error
	if !c.filtered {
		col := "total"
		switch {
		case c.onlyDeleted:
			col = "deleted"
		case c.hideDeleted:
			col = "total - deleted"
		}
		var conds []string
		var args []any
		if p.SourceID != 0 {
			conds, args = append(conds, "source_id = ?"), append(args, p.SourceID)
		}
		if p.FolderID != 0 {
			conds, args = append(conds, "id = ?"), append(args, p.FolderID)
		}
		if p.Folder != "" {
			conds, args = append(conds, "path = ?"), append(args, p.Folder)
		}
		w := ""
		if len(conds) > 0 {
			w = " WHERE " + strings.Join(conds, " AND ")
		}
		err = s.rdb.QueryRow(`SELECT COALESCE(SUM(`+col+`), 0) FROM folders`+w, args...).Scan(&n)
	} else if len(c.conds) == 0 && c.match != "" {
		// Every FTS row is a message.
		err = s.rdb.QueryRow(`SELECT count(*) FROM messages_fts WHERE messages_fts MATCH ?`, c.match).Scan(&n)
	} else {
		w, args := c.whereMode(true, true, nil)
		err = s.rdb.QueryRow(`SELECT count(*) FROM messages m`+w, args...).Scan(&n)
	}
	if err != nil {
		return 0, fmt.Errorf("index: search count: %w", err)
	}
	return n, nil
}

// highlight fills hl with HTML snippets for items matching c's positive
// full-text expression.
func (s *Store) highlight(c compiled, items []Row, hl map[int64]string) error {
	if c.hl == "" || len(items) == 0 {
		return nil
	}
	args := []any{hlOpen, hlClose, c.hl}
	ph := make([]string, len(items))
	for i, it := range items {
		ph[i] = "?"
		args = append(args, it.ID)
	}
	// Normally FTS5 seeks each rowid. A prefix term is expanded again on
	// every seek, though, so then one MATCH pass filtered by rowid (the +
	// stops the rowid constraint reaching FTS5) is much cheaper.
	rowid := "rowid"
	if c.prefix {
		rowid = "+rowid"
	}
	rows, err := s.rdb.Query(fmt.Sprintf(`SELECT rowid, snippet(messages_fts, -1, ?, ?, '…', %d)
		FROM messages_fts WHERE messages_fts MATCH ? AND %s IN (%s)`, snippetTokens, rowid, strings.Join(ph, ",")), args...)
	if err != nil {
		return fmt.Errorf("index: search highlight: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var snip sql.NullString
		if err := rows.Scan(&id, &snip); err != nil {
			return err
		}
		if snip.String != "" {
			hl[id] = highlightHTML(snip.String)
		}
	}
	return rows.Err()
}

// highlightHTML HTML-escapes s and turns the hlOpen/hlClose markers into
// balanced <mark> tags (stray or nested markers are dropped).
func highlightHTML(s string) string {
	var b strings.Builder
	open := false
	for {
		i := strings.IndexAny(s, hlOpen+hlClose)
		if i < 0 {
			break
		}
		b.WriteString(html.EscapeString(s[:i]))
		isOpen := strings.HasPrefix(s[i:], hlOpen)
		switch {
		case isOpen && !open:
			b.WriteString("<mark>")
			open = true
		case !isOpen && open:
			b.WriteString("</mark>")
			open = false
		}
		s = s[i+len(hlOpen):] // both markers are 3 bytes
	}
	b.WriteString(html.EscapeString(s))
	if open {
		b.WriteString("</mark>")
	}
	return b.String()
}
