package index

// Source states in SourceProgress.State.
const (
	StateQueued   = "queued"   // waiting to be (re)indexed in this run
	StateIndexing = "indexing" // being scanned now
	StateDone     = "done"     // indexed in this run
	StateCached   = "cached"   // unchanged since an earlier run: reused
	StateFailed   = "failed"   // unreadable; what was read is kept, retried next run
)

// Progress units in SourceProgress.Unit.
const (
	UnitBytes    = "bytes"    // Done/Total are bytes of Source.Path (offset-located formats: mbox)
	UnitMessages = "messages" // Done counts messages; Total is unknown (0)
)

// Progress is the state of an indexing run (IndexSources), reported to its
// callback and by Store.Status.
type Progress struct {
	// Running is set from the start of IndexSources until it returns;
	// Finished is set by its last report when it completed (not cancelled).
	Running, Finished bool
	// Sources are every source of the run, in the order given.
	Sources []SourceProgress
	// Messages counts messages written in this run; Warnings counts scan
	// warnings over all sources of this run.
	Messages, Warnings int64
	// Err is the error that ended the run, if any.
	Err string
}

// SourceProgress is the progress of one source.
type SourceProgress struct {
	Path, Kind string
	State      string
	Unit       string
	// Done and Total measure the scan in Unit; Total is 0 when unknown.
	Done, Total int64
	// Messages written for this source in this run; Warnings reported by
	// its Scan (all of them, though only the first are stored).
	Messages, Warnings int64
	Error              string
}

// Status returns the progress of the current or last indexing run. It is
// safe to call concurrently with IndexSources.
func (s *Store) Status() Progress {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.status
	p.Sources = append([]SourceProgress(nil), p.Sources...)
	return p
}
