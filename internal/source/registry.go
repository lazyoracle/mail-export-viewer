package source

import (
	"fmt"
	"sort"
	"sync"
)

// Format is a readable input format. Format packages register one in an
// init function; cmd links them in with blank imports, so this package (and
// Discover) never imports them.
type Format struct {
	// Name is the format's kind ("mbox", "pst", "olm", "eml"); Source.Kind
	// returns it.
	Name string
	// Sniff reports whether a file is in this format, by content. Sniffers
	// of different formats must be mutually exclusive (Discover tries them in
	// name order and takes the first match); use the Is* helpers.
	Sniff func(*Head) bool
	// Open opens a discovered candidate. It should read only what it needs
	// to validate the input (e.g. a PST header); Scan does the work.
	Open func(Candidate) (Source, error)
	// PerDir groups all matching files of one directory into a single
	// Candidate (Path = the directory, Files = the files), for formats whose
	// unit is a file per message (eml).
	PerDir bool
}

var (
	regMu    sync.RWMutex
	registry = map[string]Format{}
)

// Register makes f available to Discover and Candidate.Open. It panics if
// f.Name is empty or already registered, or Sniff or Open is nil.
func Register(f Format) {
	if f.Name == "" || f.Sniff == nil || f.Open == nil {
		panic(fmt.Sprintf("source: Register(%q): incomplete format", f.Name))
	}
	regMu.Lock()
	defer regMu.Unlock()
	if _, dup := registry[f.Name]; dup {
		panic(fmt.Sprintf("source: Register(%q) called twice", f.Name))
	}
	registry[f.Name] = f
}

// Formats returns the registered format names, sorted.
func Formats() []string {
	var out []string
	for _, f := range formats() {
		out = append(out, f.Name)
	}
	return out
}

// formats returns the registered formats sorted by name.
func formats() []Format {
	regMu.RLock()
	defer regMu.RUnlock()
	out := make([]Format, 0, len(registry))
	for _, f := range registry {
		out = append(out, f)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func lookup(name string) (Format, bool) {
	regMu.RLock()
	defer regMu.RUnlock()
	f, ok := registry[name]
	return f, ok
}

// match returns the first registered format (in name order) claiming h.
func match(fs []Format, h *Head) (Format, bool) {
	for _, f := range fs {
		if f.Sniff(h) {
			return f, true
		}
	}
	return Format{}, false
}
