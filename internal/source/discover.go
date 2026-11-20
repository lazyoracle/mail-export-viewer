package source

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"
)

// ErrUnknownFormat is returned by Discover when a file given explicitly is
// not in any registered format.
var ErrUnknownFormat = errors.New("source: unrecognised mail format")

// Default limits for Discover.
const (
	DefaultMaxDepth   = 32
	DefaultMaxEntries = 1_000_000
)

// Limits bound a directory walk. Zero fields mean the defaults.
type Limits struct {
	MaxDepth   int // directory levels below the root that are entered
	MaxEntries int // directory entries examined in total
}

// Candidate is an input Discover found, ready to Open.
type Candidate struct {
	Format string // registered format name
	// Path is the absolute path of the file, or of the directory for
	// PerDir formats.
	Path string
	// Files are the absolute paths of the matching files in Path, sorted,
	// for PerDir formats; nil otherwise.
	Files []string
	// Rel is Path relative to the discovery root, '/'-separated, as raw
	// names (no format-specific rewriting such as Thunderbird ".sbd"),
	// made valid UTF-8 (each invalid byte becomes U+FFFD). It is "" for the
	// root directory itself, and the base name of Path when a single file
	// was given. Formats derive folder names from it.
	Rel string
}

// Open opens c with its registered format.
func (c Candidate) Open() (Source, error) {
	f, ok := lookup(c.Format)
	if !ok {
		return nil, fmt.Errorf("source: format %q not registered", c.Format)
	}
	return f.Open(c)
}

// Discovery is the result of Discover.
type Discovery struct {
	Root       string      // absolute input path (a .msf input resolved to its mbox)
	Candidates []Candidate // sorted by Rel, then Path
	Warnings   []string    // non-fatal problems (limits hit, unreadable entries)
}

// Discover finds the inputs under path using the registered formats.
//
// A file is sniffed and must match a registered format (else an error
// wrapping ErrUnknownFormat); a Thunderbird summary (Mork, "X.msf") stands
// for its mbox "X". A directory is walked recursively in name order,
// following symlinks whose targets resolve under the root (others are
// skipped with a warning; the root itself may be a symlink), never
// entering a directory twice (so loops end; the first path visited wins)
// and reporting each real file once. Hidden entries (".name"), Mork
// sidecars, special files (FIFOs, devices: never opened) and files no
// format claims are skipped silently; unreadable entries and the limits are
// reported as warnings. Errors are returned only for an unusable root.
func Discover(path string, lim Limits) (*Discovery, error) {
	if lim.MaxDepth <= 0 {
		lim.MaxDepth = DefaultMaxDepth
	}
	if lim.MaxEntries <= 0 {
		lim.MaxEntries = DefaultMaxEntries
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	fi, err := os.Stat(abs)
	if err != nil {
		return nil, err
	}
	fs := formats()
	if !fi.IsDir() {
		return discoverFile(abs, fs)
	}
	realRoot, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return nil, err
	}
	w := &walker{lim: lim, fs: fs, realRoot: realRoot, seenDirs: map[string]bool{}, seenFiles: map[string]bool{}, d: &Discovery{Root: abs}}
	if err := w.walk(abs, nil, 0); err != nil {
		return nil, err
	}
	sort.SliceStable(w.d.Candidates, func(i, j int) bool {
		a, b := w.d.Candidates[i], w.d.Candidates[j]
		if a.Rel != b.Rel {
			return a.Rel < b.Rel
		}
		if a.Path != b.Path {
			return a.Path < b.Path
		}
		return a.Format < b.Format
	})
	return w.d, nil
}

func discoverFile(abs string, fs []Format) (*Discovery, error) {
	var f Format
	ok, mork := false, false
	sniff := func(p string) error {
		return withHead(p, func(h *Head) {
			mork = IsMork(h)
			if !mork {
				f, ok = match(fs, h)
			}
		})
	}
	if err := sniff(abs); err != nil {
		return nil, err
	}
	if mork {
		mbox := strings.TrimSuffix(abs, filepath.Ext(abs))
		if !strings.EqualFold(filepath.Ext(abs), ".msf") {
			return nil, fmt.Errorf("source: %s is a Thunderbird summary file, not a mailbox", abs)
		}
		if err := sniff(mbox); err != nil {
			return nil, fmt.Errorf("source: %s is a Thunderbird summary file but its mailbox is unusable: %w", abs, err)
		}
		abs = mbox
	}
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrUnknownFormat, abs)
	}
	c := Candidate{Format: f.Name, Path: abs, Rel: displayName(filepath.Base(abs))}
	if f.PerDir {
		dir := filepath.Dir(abs)
		c = Candidate{Format: f.Name, Path: dir, Files: []string{abs}, Rel: displayName(filepath.Base(dir))}
	}
	return &Discovery{Root: abs, Candidates: []Candidate{c}}, nil
}

type walker struct {
	lim                 Limits
	fs                  []Format
	realRoot            string // the root with symlinks resolved
	seenDirs, seenFiles map[string]bool
	entries             int
	stopped             bool
	d                   *Discovery
}

func (w *walker) warn(format string, args ...any) {
	w.d.Warnings = append(w.d.Warnings, fmt.Sprintf(format, args...))
}

// walk visits dir at depth, whose path components below the root are rel.
// Only an unreadable root is an error.
func (w *walker) walk(dir string, rel []string, depth int) error {
	real, err := filepath.EvalSymlinks(dir)
	if err != nil || w.seenDirs[real] {
		return nil
	}
	w.seenDirs[real] = true
	entries, err := os.ReadDir(dir) // sorted by name
	if err != nil {
		if depth == 0 {
			return err
		}
		w.warn("cannot read directory %s: %v", dir, err)
		return nil
	}
	groups := map[string][]string{} // PerDir format name -> files
	for _, e := range entries {
		if w.stopped {
			break
		}
		if w.entries++; w.entries > w.lim.MaxEntries {
			w.stopped = true
			w.warn("stopped after %d directory entries; the rest of %s is not included", w.lim.MaxEntries, w.d.Root)
			break
		}
		name := e.Name()
		if strings.HasPrefix(name, ".") {
			continue
		}
		p := filepath.Join(dir, name)
		if e.Type()&os.ModeSymlink != 0 {
			target, err := filepath.EvalSymlinks(p)
			if err != nil {
				continue // dangling
			}
			if !within(w.realRoot, target) {
				w.warn("skipping symlink %s: it points outside %s", p, w.d.Root)
				continue
			}
		}
		fi, err := os.Stat(p) // follows symlinks
		if err != nil {
			continue // dangling symlink, permission problem, ...
		}
		sub := append(rel[:len(rel):len(rel)], name)
		switch {
		case fi.IsDir():
			if depth+1 > w.lim.MaxDepth {
				w.warn("not entering %s: directory depth limit (%d) reached", p, w.lim.MaxDepth)
				continue
			}
			w.walk(p, sub, depth+1)
		case fi.Mode().IsRegular():
			realFile, err := filepath.EvalSymlinks(p)
			if err != nil || w.seenFiles[realFile] {
				continue
			}
			w.seenFiles[realFile] = true
			var f Format
			ok := false
			if err := withHead(p, func(h *Head) {
				if !IsMork(h) {
					f, ok = match(w.fs, h)
				}
			}); err != nil {
				w.warn("cannot read %s: %v", p, err)
				continue
			}
			switch {
			case !ok:
			case f.PerDir:
				groups[f.Name] = append(groups[f.Name], p)
			default:
				w.d.Candidates = append(w.d.Candidates, Candidate{Format: f.Name, Path: p, Rel: relName(sub)})
			}
		}
	}
	for name, files := range groups {
		w.d.Candidates = append(w.d.Candidates, Candidate{Format: name, Path: dir, Files: files, Rel: relName(rel)})
	}
	return nil
}

// within reports whether path is root or below it (both resolved).
func within(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func relName(parts []string) string { return displayName(strings.Join(parts, "/")) }

// displayName makes a file-derived name valid UTF-8 the way encoding/json
// shows it (each invalid byte becomes U+FFFD), so stored, displayed and
// searched names agree.
func displayName(s string) string {
	if utf8.ValidString(s) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); {
		r, n := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && n == 1 {
			b.WriteRune(utf8.RuneError)
		} else {
			b.WriteString(s[i : i+n])
		}
		i += n
	}
	return b.String()
}
