package index

import (
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/lazyoracle/mail-export-viewer/internal/source"
)

const (
	// subjectWindow is the largest gap between consecutive same-subject
	// messages (by date) that still merges them into one thread.
	subjectWindow = 30 * 24 * 3600
	// minSubjectLen is the shortest normalised subject used for merging.
	minSubjectLen = 4
	threadBatch   = 5000
	// maxLinkFanout: ids used by more messages than this are not link nodes.
	maxLinkFanout = 50
	// maxSubjectChain: longer same-subject chains are not subject-merged.
	maxSubjectChain = 25
)

const setThreadsDirtySQL = `INSERT INTO meta(key, value) VALUES('threads_dirty', 1)
	ON CONFLICT(key) DO UPDATE SET value = 1`

var (
	subjectPrefix = regexp.MustCompile(`^(?:re|fwd?|aw|sv|antw|wg|tr)(?:\[\d+\])?\s*:\s*`)
	// listTag is a mailing-list tag ("[dev] "), stripped like a prefix
	// unless it is the whole subject (from earlier-tool).
	listTag = regexp.MustCompile(`^\[[^\]]*\]\s*`)
)

// genericSubjects never merge messages, however close in time.
var genericSubjects = map[string]bool{
	"no subject": true, "(no subject)": true, "(kein betreff)": true, "hello": true,
	"hallo": true, "test": true, "untitled": true, "subject": true,
}

// normalizeSubject strips reply/forward prefixes and mailing-list tags in
// any interleaving, collapses whitespace and lower-cases.
func normalizeSubject(s string) string {
	s = strings.ToLower(strings.Join(strings.Fields(s), " "))
	for {
		t := strings.TrimSpace(subjectPrefix.ReplaceAllString(s, ""))
		if loc := listTag.FindStringIndex(t); loc != nil && loc[1] < len(t) {
			t = t[loc[1]:]
		}
		if t == s {
			return s
		}
		s = t
	}
}

func usableSubject(n string) bool {
	return utf8.RuneCountInString(n) >= minSubjectLen && !genericSubjects[n]
}

type uf struct{ p []int32 }

func (u *uf) add() int32 { u.p = append(u.p, int32(len(u.p))); return int32(len(u.p) - 1) }
func (u *uf) find(x int32) int32 {
	for u.p[x] != x {
		u.p[x] = u.p[u.p[x]]
		x = u.p[x]
	}
	return x
}
func (u *uf) union(a, b int32) {
	a, b = u.find(a), u.find(b)
	if a != b {
		u.p[b] = a
	}
}

type subjMsg struct {
	node int32
	date int64
}

// cleanToken drops blank and "<>" ids.
func cleanToken(t string) string {
	t = strings.TrimSpace(t)
	if t == "<>" {
		return ""
	}
	return t
}

// msgLinks parses the id fields of one message. refs excludes duplicates.
func msgLinks(mid, irt, refs string) (own, parent string, chain []string) {
	own = cleanToken(mid)
	parent = cleanToken(irt)
	seen := map[string]bool{}
	for _, r := range strings.Fields(refs) {
		if r = cleanToken(r); r != "" && !seen[r] {
			seen[r] = true
			chain = append(chain, r)
		}
	}
	if parent == "" && len(chain) > 0 {
		parent = chain[len(chain)-1]
	}
	return
}

// assignThreads recomputes thread_id for every message and writes the rows
// whose value changed. A thread is a connected component of messages linked
// by Message-ID / In-Reply-To / References, plus same-normalised-subject
// messages chained within subjectWindow. Messages sharing a Message-ID are
// the same node and ids that name no stored message are phantom nodes.
//
// Hub guard: an id referenced by or claimed as Message-ID by more than
// maxLinkFanout messages (mailing-list ids, constant generator ids) is not
// a link node, except that a parent link to an id claimed by a real message
// (1..maxLinkFanout claimants) is kept, so a long thread whose root has many
// direct replies still joins. Pass 1 counts, pass 2 unions; both stream.
//
// The thread id is the smallest message row id in the component, so ids are
// stable as threads grow. No per-message queries.
func (s *Store) assignThreads() (err error) {
	const q = `SELECT id, message_id, in_reply_to, refs, subject, date_unix, COALESCE(thread_id, 0)
		FROM messages ORDER BY id`
	// Pass 1: fan-out counts.
	refCnt := map[string]int32{}
	claimCnt := map[string]int32{}
	rows, err := s.rdb.Query(q)
	if err != nil {
		return err
	}
	for rows.Next() {
		var id, oldTid int64
		var mid, irt, refs, subj string
		var date *int64
		if err := rows.Scan(&id, &mid, &irt, &refs, &subj, &date, &oldTid); err != nil {
			rows.Close()
			return err
		}
		own, parent, chain := msgLinks(mid, irt, refs)
		if own != "" {
			claimCnt[own]++
		}
		if parent != "" {
			refCnt[parent]++
		}
		for _, r := range chain {
			if r != parent {
				refCnt[r]++
			}
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	hub := func(t string) bool { return refCnt[t] > maxLinkFanout || claimCnt[t] > maxLinkFanout }
	parentOK := func(t string) bool {
		if !hub(t) {
			return true
		}
		return claimCnt[t] >= 1 && claimCnt[t] <= maxLinkFanout
	}

	// Pass 2: union.
	rows, err = s.rdb.Query(q)
	if err != nil {
		return err
	}
	defer rows.Close()
	var (
		u      uf
		ids    []int64
		old    []int64
		byMID  = map[string]int32{}
		bySubj = map[string][]subjMsg{}
		// msgNode[i] is the union-find node of message i; phantom and
		// Message-ID nodes are allocated in the same array on demand.
		msgNode []int32
	)
	nodeOf := func(mid string) int32 {
		n, ok := byMID[mid]
		if !ok {
			n = u.add()
			byMID[mid] = n
		}
		return n
	}
	for rows.Next() {
		var id, oldTid int64
		var mid, irt, refs, subj string
		var date *int64
		if err := rows.Scan(&id, &mid, &irt, &refs, &subj, &date, &oldTid); err != nil {
			return err
		}
		n := u.add()
		msgNode = append(msgNode, n)
		ids = append(ids, id)
		old = append(old, oldTid)
		own, parent, chain := msgLinks(mid, irt, refs)
		if own != "" && claimCnt[own] <= maxLinkFanout {
			u.union(nodeOf(own), n)
		}
		if parent != "" && parentOK(parent) {
			u.union(nodeOf(parent), n)
		}
		for i := 1; i < len(chain); i++ {
			if !hub(chain[i-1]) && !hub(chain[i]) {
				u.union(nodeOf(chain[i-1]), nodeOf(chain[i]))
			}
		}
		if date != nil {
			if ns := normalizeSubject(subj); usableSubject(ns) {
				bySubj[ns] = append(bySubj[ns], subjMsg{n, *date})
			}
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	rows.Close()
	for _, g := range bySubj {
		if len(g) < 2 {
			continue
		}
		sort.Slice(g, func(i, j int) bool { return g[i].date < g[j].date })
		// Split into chains (gap <= window); chains longer than
		// maxSubjectChain are recurring mail, not a conversation.
		start := 0
		for i := 1; i <= len(g); i++ {
			if i < len(g) && g[i].date-g[i-1].date <= subjectWindow {
				continue
			}
			if i-start <= maxSubjectChain {
				for j := start + 1; j < i; j++ {
					u.union(g[j-1].node, g[j].node)
				}
			}
			start = i
		}
	}
	bySubj, byMID = nil, nil

	// Thread id = smallest row id in the component (rows are in id order).
	rep := make(map[int32]int64, len(ids))
	for i, n := range msgNode {
		r := u.find(n)
		if _, ok := rep[r]; !ok {
			rep[r] = ids[i]
		}
	}
	var changed []threadUpdate
	for i, n := range msgNode {
		if t := rep[u.find(n)]; t != old[i] {
			changed = append(changed, threadUpdate{ids[i], t})
		}
	}
	defer func() {
		if err == nil {
			_, err = s.wdb.Exec(`DELETE FROM meta WHERE key = 'threads_dirty'`)
		}
	}()
	// Changed ids go into a temporary table with multi-row INSERTs, then
	// one UPDATE ... FROM per batch (a statement per row is far slower).
	for start := 0; start < len(changed); start += threadBatch {
		end := min(start+threadBatch, len(changed))
		if err := s.writeThreadIDs(changed[start:end]); err != nil {
			return err
		}
	}
	return nil
}

type threadUpdate struct{ id, tid int64 }

func (s *Store) writeThreadIDs(ups []threadUpdate) error {
	tx, err := s.wdb.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`CREATE TEMP TABLE IF NOT EXISTS thread_updates(id INTEGER PRIMARY KEY, tid INTEGER NOT NULL)`); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM temp.thread_updates`); err != nil {
		return err
	}
	ins := multiInsert{tx: tx, head: `INSERT INTO temp.thread_updates(id, tid) VALUES`, cols: 2}
	for _, u := range ups {
		if err := ins.add(u.id, u.tid); err != nil {
			return err
		}
	}
	if err := ins.flush(); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE messages SET thread_id = u.tid FROM temp.thread_updates u WHERE messages.id = u.id`); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM temp.thread_updates`); err != nil {
		return err
	}
	return tx.Commit()
}

// Thread returns the messages in the thread of message id, oldest first
// (unknown dates last, ties by id); Deleted messages other than id itself
// are left out unless Options.IncludeDeleted. A message threading has not
// reached yet is its own thread. Unknown ids give ErrNotFound.
func (s *Store) Thread(id int64) ([]Row, error) {
	m, err := s.Message(id)
	if err != nil {
		return nil, err
	}
	if m.ThreadID == 0 {
		return []Row{m.Row}, nil
	}
	hide := ""
	if !s.opts.IncludeDeleted {
		hide = ` AND (` + deletedCond + ` OR m.id = ?)`
	}
	args := []any{m.ThreadID}
	if hide != "" {
		args = append(args, id)
	}
	out, _, err := s.queryRows(`SELECT `+rowCols+` FROM messages m WHERE m.thread_id = ?`+hide+`
		ORDER BY m.date_unix IS NULL, m.date_unix, m.id`, args, nil)
	return out, err
}

// threadCounts fills counts[messageID] = size of that message's thread
// for the given items: its visible messages (Deleted ones hidden unless
// Options.IncludeDeleted), plus the item itself when it is a hidden
// Deleted message shown by is:deleted.
func (s *Store) threadCounts(items []Row, counts map[int64]int) error {
	tids := map[int64]bool{}
	for _, m := range items {
		if m.ThreadID != 0 {
			tids[m.ThreadID] = true
		}
	}
	if len(tids) == 0 {
		return nil
	}
	args := make([]any, 0, len(tids))
	for t := range tids {
		args = append(args, t)
	}
	hide := ""
	if !s.opts.IncludeDeleted {
		hide = " AND " + strings.ReplaceAll(deletedCond, "m.", "")
	}
	rows, err := s.rdb.Query(`SELECT thread_id, COUNT(*) FROM messages WHERE thread_id IN (?`+
		strings.Repeat(",?", len(args)-1)+`)`+hide+` GROUP BY thread_id`, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	size := map[int64]int{}
	for rows.Next() {
		var t int64
		var n int
		if err := rows.Scan(&t, &n); err != nil {
			return err
		}
		size[t] = n
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, m := range items {
		n := size[m.ThreadID]
		if hide != "" && m.Flags.Has(source.Deleted) {
			n++ // a deleted row shown (is:deleted) counts itself
		}
		if n > 0 {
			counts[m.ID] = n
		}
	}
	return nil
}
