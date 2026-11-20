package index

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/lazyoracle/mail-export-viewer/internal/mail"
	"github.com/lazyoracle/mail-export-viewer/internal/source"
)

const (
	// batchSize is the number of messages written per transaction;
	// batchBytes ends a batch early so large messages do not pile up.
	batchSize  = 2000
	batchBytes = 16 << 20
	// budgetBytes bounds the entry bytes (Raw, or the summary for formats
	// that build it; see entryUnits) queued, being summarised or waiting
	// to be written in order, in budgetUnit units; an entry takes at least
	// one unit and a larger-than-budget entry all.
	budgetBytes = 64 << 20
	budgetUnit  = 64 << 10
	budgetUnits = budgetBytes / budgetUnit
	// maxStoredWarnings warnings of maxWarningBytes each are kept per
	// source (all are counted).
	maxStoredWarnings = 50
	maxWarningBytes   = 1000
)

type sourceState struct {
	id           int64
	kind, fprint string
}

// IndexSources is synchronous: it returns when the run is over (callers
// that serve while indexing run it in a goroutine and wait for it to
// return). Only one run at a time: a concurrent call returns ErrBusy.
//
// IndexSources brings the index in line with srcs (one per Source.Path;
// later duplicates are ignored): sources whose inputs (Source.Inputs: the
// set of paths and each one's size and mtime) match a completed earlier
// run are reused, changed, new and incomplete ones are (re)indexed from
// scratch, and indexed sources not in srcs are removed. Entries are
// summarised (Entry.Resolve) by runtime.NumCPU workers under a memory
// budget and written in scan order by one writer, in transactions.
//
// Scan warnings are counted and the first maxStoredWarnings kept per
// source (SourceInfo.Warnings). A Scan error marks the source failed
// (SourceInfo.Error): its messages read so far stay listed and it is
// retried on the next run. onProgress (may be nil) is called with a copy
// of the status after every written batch and state change; calls are
// never concurrent and all happen before IndexSources returns. Status
// reports the same from any goroutine.
//
// On cancellation IndexSources returns ctx.Err(); the source being indexed
// keeps the messages written so far and is redone on the next run. Only
// database errors and cancellation are returned.
func (s *Store) IndexSources(ctx context.Context, srcs []source.Source, onProgress func(Progress)) (err error) {
	if !s.runMu.TryLock() {
		return ErrBusy
	}
	defer s.runMu.Unlock()
	if onProgress == nil {
		onProgress = func(Progress) {}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	r := &run{s: s, ctx: ctx, report: onProgress}
	defer func() { r.finish(err) }()

	existing, err := s.loadSources()
	if err != nil {
		return err
	}
	type workItem struct {
		src    source.Source
		i      int
		fprint string
		fpErr  error
	}
	var work []workItem
	keep := map[string]bool{}
	var prog Progress
	prog.Running = true
	for _, src := range srcs {
		path := src.Path()
		if keep[path] {
			continue
		}
		keep[path] = true
		sp := SourceProgress{Path: path, Kind: src.Kind(), Unit: UnitMessages}
		fp, fpErr := fingerprint(src)
		if st, ok := existing[path]; ok && fp != "" && st.fprint == fp && st.kind == src.Kind() {
			sp.State = StateCached
		} else {
			sp.State = StateQueued
			work = append(work, workItem{src, len(prog.Sources), fp, fpErr})
		}
		prog.Sources = append(prog.Sources, sp)
	}
	r.set(func(p *Progress) { *p = prog })
	r.publish()

	removed := false
	for path, st := range existing {
		if !keep[path] {
			removed = true
			if err := s.removeSource(st.id); err != nil {
				return err
			}
		}
	}
	if len(work) > 0 {
		if _, err := s.wdb.Exec(dropSortIndexSQL); err != nil {
			return err
		}
	}
	lastFacets := time.Now()
	for _, w := range work {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := r.indexOne(w.src, w.i, w.fprint, w.fpErr); err != nil {
			return err
		}
		// Keep label/tag counts roughly current during a long first run.
		if time.Since(lastFacets) >= facetInterval {
			if err := s.rebuildFacetsIfDirty(); err != nil {
				return err
			}
			lastFacets = time.Now()
		}
	}
	// Also heal rows left unthreaded by a cancelled or crashed earlier run.
	need := len(work) > 0 || removed
	if !need {
		if err := s.wdb.QueryRow(`SELECT EXISTS(SELECT 1 FROM messages WHERE thread_id IS NULL)`).Scan(&need); err != nil {
			return err
		}
	}
	if need {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := s.assignThreads(); err != nil {
			return err
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := s.rebuildFacetsIfDirty(); err != nil {
		return err
	}
	_, err = s.wdb.Exec(sortIndexSQL)
	return err
}

// run is the state of one IndexSources call.
type run struct {
	s      *Store
	ctx    context.Context
	report func(Progress)
}

// set changes the shared status under the lock.
func (r *run) set(f func(*Progress)) {
	r.s.mu.Lock()
	f(&r.s.status)
	r.s.mu.Unlock()
}

func (r *run) publish() { r.report(r.s.Status()) }

func (r *run) finish(err error) {
	r.set(func(p *Progress) {
		p.Running = false
		p.Finished = err == nil
		if err != nil {
			p.Err = err.Error()
		}
	})
	r.publish()
}

// fingerprint hashes the sorted input paths of src with each one's size
// and mtime (a missing input counts as size -1), plus the source path. A
// panicking Inputs gives "" (never matches: the source is always
// re-scanned) and the panic as error.
func fingerprint(src source.Source) (fp string, err error) {
	defer func() {
		if r := recover(); r != nil {
			fp, err = "", fmt.Errorf("%s: Inputs panic: %v", src.Path(), r)
		}
	}()
	paths := slices.Clone(src.Inputs())
	if len(paths) == 0 {
		paths = []string{src.Path()}
	}
	slices.Sort(paths)
	paths = slices.Compact(paths)
	h := sha256.New()
	fmt.Fprintf(h, "%s\x00%s\x00", src.Kind(), src.Path())
	for _, p := range paths {
		size, mtime := int64(-1), int64(0)
		if fi, err := os.Stat(p); err == nil {
			size, mtime = fi.Size(), fi.ModTime().UnixNano()
		}
		fmt.Fprintf(h, "%s\x00%d\x00%d\x00", p, size, mtime)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// safely runs f, turning a panic into an error.
func safely(what string, f func() error) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("%s panic: %v", what, r)
		}
	}()
	return f()
}

func (s *Store) loadSources() (map[string]sourceState, error) {
	rows, err := s.wdb.Query(`SELECT id, path, kind, fingerprint FROM sources`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]sourceState{}
	for rows.Next() {
		var st sourceState
		var path string
		if err := rows.Scan(&st.id, &path, &st.kind, &st.fprint); err != nil {
			return nil, err
		}
		out[path] = st
	}
	return out, rows.Err()
}

// deleteSourceRows deletes the messages and folders of source id.
func deleteSourceRows(tx *sql.Tx, id int64) error {
	for _, q := range []string{
		`DELETE FROM messages_fts WHERE rowid IN (SELECT id FROM messages WHERE source_id = ?)`,
		`DELETE FROM message_terms WHERE message_id IN (SELECT id FROM messages WHERE source_id = ?)`,
		`DELETE FROM props WHERE message_id IN (SELECT id FROM messages WHERE source_id = ?)`,
		`DELETE FROM messages WHERE source_id = ?`,
		`DELETE FROM folders WHERE source_id = ?`,
		`DELETE FROM tag_colors WHERE source_id = ?`,
	} {
		if _, err := tx.Exec(q, id); err != nil {
			return err
		}
	}
	_, err := tx.Exec(setFacetsDirtySQL)
	return err
}

func (s *Store) removeSource(id int64) error {
	tx, err := s.wdb.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := deleteSourceRows(tx, id); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM sources WHERE id = ?`, id); err != nil {
		return err
	}
	return tx.Commit()
}

// resetSource creates or empties the source row for path and marks it
// incomplete (no fingerprint) until indexing finishes.
func (s *Store) resetSource(path, kind string) (int64, error) {
	tx, err := s.wdb.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	var id int64
	err = tx.QueryRow(`INSERT INTO sources(path, kind) VALUES(?, ?)
		ON CONFLICT(path) DO UPDATE SET kind = excluded.kind RETURNING id`, path, kind).Scan(&id)
	if err != nil {
		return 0, err
	}
	if err := deleteSourceRows(tx, id); err != nil {
		return 0, err
	}
	if _, err := tx.Exec(`UPDATE sources SET fingerprint = '', warn_count = 0, warnings = '', error = ''
		WHERE id = ?`, id); err != nil {
		return 0, err
	}
	return id, tx.Commit()
}

// warnings collects a source's scan warnings (Scan may call warn from its
// own goroutine).
type warnings struct {
	mu    sync.Mutex
	count int64
	first []string
}

func (w *warnings) add(err error) {
	if err == nil {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.count++
	if len(w.first) < maxStoredWarnings {
		msg := strings.Join(strings.Fields(clean(err.Error(), maxWarningBytes)), " ")
		w.first = append(w.first, msg)
	}
}

func (w *warnings) get() (int64, []string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.count, slices.Clone(w.first)
}

// entryUnits is the budget an entry takes: its Raw bytes or, for formats
// that build the summary themselves (Raw empty), the summary text and
// names, plus labels, tags and props; at least one unit, at most all.
func entryUnits(e *source.Entry) int {
	n := len(e.Raw)
	if m := e.Msg; m != nil {
		n += len(m.Text) + len(m.Subject) + len(m.MessageID) + len(m.InReplyTo)
		for _, r := range m.References {
			n += len(r)
		}
		for _, a := range m.AttachmentNames {
			n += len(a)
		}
		for _, l := range [][]mail.Address{m.To, m.Cc, m.Bcc} {
			for _, a := range l {
				n += len(a.Name) + len(a.Email)
			}
		}
	}
	for _, l := range [][]string{e.Labels, e.Tags} {
		for _, x := range l {
			n += len(x)
		}
	}
	for _, p := range e.Props {
		n += len(p.Key) + len(p.Value)
	}
	return min(budgetUnits, max(1, (n+budgetUnit-1)/budgetUnit))
}

type job struct {
	seq   int
	e     source.Entry
	units int
}

type result struct {
	seq   int
	r     row
	units int
}

// indexOne (re)indexes one source (progress slot i). Source problems go to
// the source's state; only database errors and cancellation are returned.
func (r *run) indexOne(src source.Source, i int, fprint string, fpErr error) error {
	s := r.s
	path := src.Path()
	srcID, err := s.resetSource(path, src.Kind())
	if err != nil {
		return err
	}
	var total int64
	if fi, err := os.Stat(path); err == nil && fi.Mode().IsRegular() {
		total = fi.Size()
	}
	r.set(func(p *Progress) { p.Sources[i].State = StateIndexing })
	r.publish()

	ctx, cancel := context.WithCancel(r.ctx)
	defer cancel()
	budget := make(chan struct{}, budgetUnits)
	jobs := make(chan job, 2*runtime.NumCPU())
	results := make(chan result, 2*runtime.NumCPU())
	var warns warnings

	// Reader: Scan, bounded by the byte budget. A panicking Scan fails
	// the source (what was emitted stays).
	warns.add(fpErr)
	var scanErr error
	go func() {
		defer close(jobs)
		seq := 0
		emit := func(e source.Entry) error {
			units := entryUnits(&e)
			for range units {
				select {
				case budget <- struct{}{}:
				case <-ctx.Done():
					return ctx.Err()
				}
			}
			select {
			case jobs <- job{seq: seq, e: e, units: units}:
				seq++
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		scanErr = safely("Scan", func() error { return src.Scan(ctx, emit, warns.add) })
	}()

	// Summarising workers.
	name := filepath.Base(path)
	var wg sync.WaitGroup
	for range runtime.NumCPU() {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range jobs {
				perr := resolveEntry(&j.e)
				warns.add(perr)
				res := result{seq: j.seq, r: buildRow(&j.e, name), units: j.units}
				res.r.parseErr = perr != nil
				select {
				case results <- res:
				case <-ctx.Done():
				}
			}
		}()
	}
	go func() { wg.Wait(); close(results) }()

	// Collector (this goroutine): rows in scan order, cut into batches for
	// the writer goroutine, so summarising goes on while a batch is written.
	labelKind := kindLabel
	if isCategoryKind(src.Kind()) {
		labelKind = kindCategory
	}
	w := writer{s: s, srcID: srcID, labelKind: labelKind, folders: map[string]int64{}}
	var written, maxEnd int64
	unit := "" // set by the collector before the first batch is sent
	var werr error
	batches := make(chan []row, 1)
	wdone := make(chan struct{})
	go func() {
		defer close(wdone)
		for b := range batches {
			if werr != nil {
				continue
			}
			if werr = w.write(b); werr != nil {
				cancel()
				continue
			}
			written += int64(len(b))
			for _, x := range b {
				maxEnd = max(maxEnd, x.end)
			}
			n, _ := warns.get()
			r.set(func(p *Progress) {
				sp := &p.Sources[i]
				sp.Unit, sp.Messages, sp.Warnings = unit, written, n
				if unit == UnitBytes {
					sp.Total, sp.Done = total, min(maxEnd, total)
				} else {
					sp.Done = written
				}
			})
			r.publish()
		}
	}()
	pending := map[int]result{}
	next := 0
	batch := make([]row, 0, batchSize)
	batchLen := 0
	for res := range results {
		if ctx.Err() != nil {
			continue // drain
		}
		pending[res.seq] = res
		for {
			p, ok := pending[next]
			if !ok {
				break
			}
			delete(pending, next)
			next++
			for range p.units {
				<-budget
			}
			if unit == "" {
				unit = UnitMessages
				if p.r.end > 0 && total > 0 {
					unit = UnitBytes
				}
			}
			batch = append(batch, p.r)
			batchLen += p.r.size
			if len(batch) >= batchSize || batchLen >= batchBytes {
				batches <- batch
				batch, batchLen = make([]row, 0, batchSize), 0
			}
		}
	}
	if ctx.Err() == nil && len(batch) > 0 {
		batches <- batch
	}
	close(batches)
	<-wdone
	if werr != nil {
		return werr
	}
	if err := r.ctx.Err(); err != nil {
		return err
	}
	if unit == "" {
		unit = UnitMessages
	}
	if scanErr == nil {
		// Optional interfaces: a panic there is a warning, not a failure.
		if fl, ok := src.(source.FolderLister); ok {
			var folders []string
			warns.add(safely("Folders", func() error { folders = fl.Folders(); return nil }))
			for _, f := range folders {
				if _, err := w.folder(cleanFolder(f, name)); err != nil {
					return err
				}
			}
		}
		if ts, ok := src.(source.TagStyler); ok {
			var styles []source.TagStyle
			warns.add(safely("TagStyles", func() error { styles = ts.TagStyles(); return nil }))
			if err := s.setTagColors(srcID, styles); err != nil {
				return err
			}
		}
	}
	n, first := warns.get()
	state, errText, fp := StateDone, "", fprint
	if scanErr != nil {
		state, errText, fp = StateFailed, clean(scanErr.Error(), maxWarningBytes), ""
	}
	if _, err := s.wdb.Exec(`UPDATE sources SET fingerprint = ?, warn_count = ?, warnings = ?, error = ? WHERE id = ?`,
		fp, n, strings.Join(first, "\n"), errText, srcID); err != nil {
		return err
	}
	r.set(func(p *Progress) {
		sp := &p.Sources[i]
		sp.State, sp.Error, sp.Unit, sp.Messages, sp.Warnings = state, errText, unit, written, n
		if unit == UnitBytes {
			sp.Total, sp.Done = total, total
		} else {
			sp.Done = written
		}
		p.Messages += written
		p.Warnings += n
	})
	r.publish()
	return nil
}

// writer inserts rows of one source.
type writer struct {
	s         *Store
	srcID     int64
	labelKind int              // kindLabel or kindCategory for Entry.Labels
	folders   map[string]int64 // path -> folder id
	terms     map[[2]string]int64
}

func (w *writer) folder(path string) (int64, error) {
	if id, ok := w.folders[path]; ok {
		return id, nil
	}
	var id int64
	err := w.s.wdb.QueryRow(`INSERT INTO folders(source_id, path) VALUES(?, ?)
		ON CONFLICT(source_id, path) DO UPDATE SET path = excluded.path RETURNING id`, w.srcID, path).Scan(&id)
	if err != nil {
		return 0, err
	}
	w.folders[path] = id
	return id, nil
}

func (w *writer) term(tx *sql.Tx, kind int, name string) (int64, error) {
	if w.terms == nil {
		w.terms = map[[2]string]int64{}
	}
	k := [2]string{strconv.Itoa(kind), name}
	if id, ok := w.terms[k]; ok {
		return id, nil
	}
	var id int64
	err := tx.QueryRow(`INSERT INTO terms(kind, name) VALUES(?, ?)
		ON CONFLICT(kind, name) DO UPDATE SET name = excluded.name RETURNING id`, kind, name).Scan(&id)
	if err != nil {
		return 0, err
	}
	w.terms[k] = id
	return id, nil
}

type folderDelta struct{ total, unread, deleted, deletedUnread int64 }

func (w *writer) write(batch []row) (err error) {
	// Folder rows first (outside the batch transaction: single writer).
	for i := range batch {
		if _, err := w.folder(batch[i].folder); err != nil {
			return err
		}
	}
	tx, err := w.s.wdb.Begin()
	if err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			tx.Rollback()
			w.terms = nil // ids of rolled-back inserts are void
		}
	}()
	// Explicit ids (single writer) let every table be filled with
	// multi-row INSERTs: the driver parses each statement on every Exec, so
	// one statement per row would dominate the cost.
	var nextID int64
	if err := tx.QueryRow(`SELECT COALESCE(MAX(id), 0) + 1 FROM messages`).Scan(&nextID); err != nil {
		return err
	}
	msgs := multiInsert{tx: tx, head: `INSERT INTO messages(id, source_id, folder_id, loc, message_id, in_reply_to,
		refs, subject, from_name, from_email, to_text, cc_text, date_unix, flags, att_names, labels, tags, snippet,
		parse_error) VALUES`, cols: 19}
	fts := multiInsert{tx: tx, head: `INSERT INTO messages_fts(rowid, subject, from_text, to_text, body, att_names) VALUES`, cols: 6}
	links := multiInsert{tx: tx, head: `INSERT OR IGNORE INTO message_terms(term_id, message_id) VALUES`, cols: 2}
	props := multiInsert{tx: tx, head: `INSERT INTO props(message_id, data) VALUES`, cols: 2}
	deltas := map[int64]*folderDelta{}
	for i, r := range batch {
		id := nextID + int64(i)
		fid := w.folders[r.folder]
		if err := msgs.add(id, w.srcID, fid, r.loc, r.messageID, r.inReplyTo, r.refs, r.subject, r.fromName,
			r.fromEmail, r.toText, r.ccText, r.date, r.flags, r.attNames, strings.Join(r.labels, "\n"),
			strings.Join(r.tags, "\n"), r.snippet, r.parseErr); err != nil {
			return fmt.Errorf("index: insert message: %w", err)
		}
		if err := fts.add(id, r.subject, r.ftsFrom, r.ftsTo, r.body, r.attNames); err != nil {
			return fmt.Errorf("index: insert fts: %w", err)
		}
		for _, l := range [2]struct {
			kind  int
			names []string
		}{{w.labelKind, r.labels}, {kindTag, r.tags}} {
			for _, n := range l.names {
				tid, err := w.term(tx, l.kind, n)
				if err != nil {
					return err
				}
				if err := links.add(tid, id); err != nil {
					return err
				}
			}
		}
		if r.props != "" {
			if err := props.add(id, r.props); err != nil {
				return err
			}
		}
		d := deltas[fid]
		if d == nil {
			d = &folderDelta{}
			deltas[fid] = d
		}
		unread := int64(0)
		if r.flags.Has(source.Unread) {
			unread = 1
		}
		d.total++
		d.unread += unread
		if r.flags.Has(source.Deleted) {
			d.deleted++
			d.deletedUnread += unread
		}
	}
	for _, m := range []*multiInsert{&msgs, &fts, &links, &props} {
		if err := m.flush(); err != nil {
			return err
		}
	}
	for fid, d := range deltas {
		if _, err := tx.Exec(`UPDATE folders SET total = total + ?, unread = unread + ?, deleted = deleted + ?,
			deleted_unread = deleted_unread + ? WHERE id = ?`, d.total, d.unread, d.deleted, d.deletedUnread, fid); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(setFacetsDirtySQL); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	committed = true
	return nil
}

// multiInsert batches rows into multi-row INSERT statements of at most
// multiRows rows.
type multiInsert struct {
	tx   *sql.Tx
	head string
	cols int
	args []any
	n    int
}

const multiRows = 64

func (m *multiInsert) add(vals ...any) error {
	m.args = append(m.args, vals...)
	m.n++
	if m.n == multiRows {
		return m.flush()
	}
	return nil
}

func (m *multiInsert) flush() error {
	if m.n == 0 {
		return nil
	}
	row := "(?" + strings.Repeat(",?", m.cols-1) + ")"
	q := m.head + row + strings.Repeat(","+row, m.n-1)
	_, err := m.tx.Exec(q, m.args...)
	m.args, m.n = m.args[:0], 0
	return err
}
