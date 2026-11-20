package msf

import (
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/lazyoracle/mail-export-viewer/internal/source"
)

func init() { source.RegisterSidecarProvider(provider) }

// Thunderbird message flags (nsMsgMessageFlags) that map to source.Flags.
const (
	tbRead        = 0x1
	tbReplied     = 0x2
	tbMarked      = 0x4
	tbExpunged    = 0x8
	tbForwarded   = 0x1000
	tbImapDeleted = 0x200000
)

// junkScoreJunk is nsIJunkMailPlugin::IS_SPAM_SCORE.
const junkScoreJunk = 100

// ignoredKeywords are bookkeeping keywords Thunderbird and IMAP servers set
// that are not user tags.
var ignoredKeywords = map[string]bool{
	"junk": true, "nonjunk": true, "$junk": true, "$nonjunk": true, "$notjunk": true,
	"$mdnsent": true, "$forwarded": true, "$replied": true, "$seen": true,
	"notjunk": true, "$phishing": true,
}

// priorityNames maps nsMsgPriority (notSet 0, none 1, lowest 2, low 3,
// normal 4, high 5, highest 6) to Prop values; none and normal are omitted.
var priorityNames = map[int]string{2: "lowest", 3: "low", 5: "high", 6: "highest"}

// sidecar implements source.Sidecar over a parsed .msf.
type sidecar struct {
	inputs   []string
	tags     map[string]source.TagStyle // as exposed by TagInfo
	byLower  map[string]source.TagStyle // lower-cased keys, for keyword lookup
	byOffset map[int64]*Row
	byID     map[string]*Row // nil value: ambiguous Message-ID
	legacy   map[int64]*Row  // rows without storeToken/msgOffset, by row id
	msgs     int
}

// readFile reads a regular file of at most limit bytes (ErrTooLarge
// otherwise). The size is enforced while reading, not from a stat.
func readFile(path string, limit int) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !st.Mode().IsRegular() {
		return nil, fmt.Errorf("msf: %s is not a regular file", path)
	}
	data, err := io.ReadAll(io.LimitReader(f, int64(limit)+1))
	if err != nil {
		return nil, err
	}
	if len(data) > limit {
		return nil, ErrTooLarge
	}
	return data, nil
}

// provider returns the sidecar for the mbox at path: (nil, nil) when there
// is no "<mbox>.msf" or it lists no messages; (nil, err) when it is
// unreadable, oversized or not Mork (the mbox keeps header-derived
// metadata); (sidecar, err) when usable but the parser had to skip garbage
// or roll back an interrupted write (err is a warning).
func provider(mbox string) (source.Sidecar, error) {
	msf := mbox + ".msf"
	if _, err := os.Lstat(msf); os.IsNotExist(err) {
		return nil, nil
	}
	data, err := readFile(msf, maxInput)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", msf, err)
	}
	f, err := Parse(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", msf, err)
	}
	s := &sidecar{
		inputs:   []string{msf},
		byOffset: map[int64]*Row{},
		byID:     map[string]*Row{},
		legacy:   map[int64]*Row{},
		msgs:     0,
	}
	for _, r := range f.Rows {
		if isMessage(r) {
			s.index(r)
		}
	}
	if s.msgs == 0 {
		return nil, nil
	}
	prefs, prefsPath := findPrefs(filepath.Dir(mbox))
	s.tags = tagStyles(prefs)
	if prefsPath != "" {
		s.inputs = append(s.inputs, prefsPath)
	}
	s.byLower = make(map[string]source.TagStyle, len(s.tags))
	for k, v := range s.tags {
		s.byLower[strings.ToLower(k)] = v
	}
	if f.Problems > 0 {
		return s, fmt.Errorf("%s: %d malformed or uncommitted parts were skipped", msf, f.Problems)
	}
	return s, nil
}

// isMessage reports whether r is a message row (not folder info or a thread).
func isMessage(r *Row) bool {
	if strings.Contains(r.Scope, "dbfolderinfo") || strings.Contains(r.Scope, "thread") {
		return false
	}
	for _, c := range []string{"message-id", "storeToken", "msgOffset", "flags"} {
		if _, ok := r.Get(c); ok {
			return true
		}
	}
	return false
}

func normID(id string) string {
	return strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(strings.TrimSpace(id), "<"), ">"))
}

func (s *sidecar) index(r *Row) {
	s.msgs++
	if v, ok := r.Get("message-id"); ok {
		if id := normID(v); id != "" {
			if _, dup := s.byID[id]; dup {
				s.byID[id] = nil
			} else {
				s.byID[id] = r
			}
		}
	}
	explicit := false
	if v, ok := r.Get("storeToken"); ok {
		if n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64); err == nil && n >= 0 {
			s.byOffset[n] = r
			explicit = true
		}
	}
	if v, ok := r.Get("msgOffset"); ok {
		if n, err := strconv.ParseInt(strings.TrimSpace(v), 16, 64); err == nil && n >= 0 {
			s.byOffset[n] = r
			explicit = true
		}
	}
	if !explicit && r.ID <= math.MaxInt64 {
		s.legacy[int64(r.ID)] = r
	}
}

func (s *sidecar) Inputs() []string { return s.inputs }

func (s *sidecar) TagInfo() map[string]source.TagStyle { return s.tags }

// Lookup matches by byte offset: a row whose storeToken (decimal) or
// msgOffset (hex) is offset, or, in old folders that have neither, whose id
// is offset. Failing that it matches a Message-ID unique in the folder.
// Every hit other than an explicit offset must also match msgID, and an
// explicit hit is rejected when its own Message-ID contradicts msgID, so a
// stale summary (mbox compacted since) never attaches metadata to the
// wrong message.
func (s *sidecar) Lookup(msgID string, offset int64) (source.SidecarMeta, bool) {
	msgID = normID(msgID)
	have := func(r *Row) string {
		v, _ := r.Get("message-id")
		return normID(v)
	}
	var row *Row
	if r := s.byOffset[offset]; offset >= 0 && r != nil {
		if h := have(r); msgID == "" || h == "" || h == msgID {
			row = r
		}
	}
	if r := s.legacy[offset]; row == nil && offset >= 0 && r != nil && msgID != "" && have(r) == msgID {
		row = r
	}
	if row == nil && msgID != "" {
		row = s.byID[msgID]
	}
	if row == nil {
		return source.SidecarMeta{}, false
	}
	return s.meta(row), true
}

func (s *sidecar) meta(r *Row) source.SidecarMeta {
	var m source.SidecarMeta
	if v, ok := r.Get("flags"); ok {
		n, _ := strconv.ParseUint(strings.TrimSpace(v), 16, 32)
		if n&tbRead == 0 {
			m.Flags |= source.Unread
		}
		for _, f := range []struct {
			bit uint64
			to  source.Flags
		}{{tbReplied, source.Answered}, {tbMarked, source.Starred}, {tbExpunged, source.Deleted},
			{tbImapDeleted, source.Deleted}, {tbForwarded, source.Forwarded}} {
			if n&f.bit != 0 {
				m.Flags |= f.to
			}
		}
	}
	if v, ok := r.Get("priority"); ok {
		// Stored in hex as the nsMsgPriority value; see priorityNames.
		n, _ := strconv.ParseInt(strings.TrimSpace(v), 16, 32)
		if n >= 5 {
			m.Flags |= source.Important
		}
		if name := priorityNames[int(n)]; name != "" {
			m.Props = append(m.Props, source.Prop{Key: "priority", Value: name})
		}
	}
	if v, ok := r.Get("junkscore"); ok && strings.TrimSpace(v) != "" {
		v = strings.TrimSpace(v)
		if n, err := strconv.Atoi(v); err == nil && n >= junkScoreJunk {
			m.Flags |= source.Junk
		}
		m.Props = append(m.Props, source.Prop{Key: "junkscore", Value: v})
	}
	var keys []string
	if v, ok := r.Get("keywords"); ok {
		keys = strings.Fields(v)
	}
	if v, ok := r.Get("label"); ok {
		if n, err := strconv.ParseInt(strings.TrimSpace(v), 16, 32); err == nil && n >= 1 && n <= 5 {
			keys = append(keys, "$label"+strconv.FormatInt(n, 10))
		}
	}
	seen := map[string]bool{}
	for _, k := range keys {
		lk := strings.ToLower(k)
		if lk == "$forwarded" {
			m.Flags |= source.Forwarded
		}
		if ignoredKeywords[lk] {
			continue
		}
		name := k
		if st, ok := s.byLower[lk]; ok && st.Name != "" {
			name = st.Name
		}
		if !seen[name] {
			seen[name] = true
			m.Tags = append(m.Tags, name)
		}
	}
	return m
}
