package msf

import (
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
}

var priorityNames = map[int]string{1: "lowest", 2: "low", 4: "high", 5: "highest"}

// sidecar implements source.Sidecar over a parsed .msf.
type sidecar struct {
	inputs   []string
	tags     map[string]source.TagStyle // as exposed by TagInfo
	byLower  map[string]source.TagStyle // lower-cased keys, for keyword lookup
	byOffset map[int64]*Row
	byID     map[string]*Row // nil value: ambiguous Message-ID
	byKey    map[uint64]*Row
}

// provider returns the sidecar for the mbox at path, or nil when there is no
// usable "<mbox>.msf". A corrupt, oversized or message-less summary is
// ignored (the mbox falls back to header-derived metadata).
func provider(mbox string) source.Sidecar {
	msf := mbox + ".msf"
	st, err := os.Stat(msf)
	if err != nil || !st.Mode().IsRegular() || st.Size() > int64(maxInput) {
		return nil
	}
	data, err := os.ReadFile(msf)
	if err != nil {
		return nil
	}
	f, err := Parse(data)
	if err != nil {
		return nil
	}
	s := &sidecar{
		inputs:   []string{msf},
		byOffset: map[int64]*Row{},
		byID:     map[string]*Row{},
		byKey:    map[uint64]*Row{},
	}
	for _, r := range f.Rows {
		if isMessage(r) {
			s.index(r)
		}
	}
	if len(s.byKey) == 0 {
		return nil
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
	return s
}

// isMessage reports whether r is a message row (not folder info or a thread).
func isMessage(r *Row) bool {
	if strings.Contains(r.Scope, "dbfolderinfo") || strings.Contains(r.Scope, "thread") {
		return false
	}
	for _, c := range []string{"messageId", "storeToken", "msgOffset", "flags"} {
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
	s.byKey[r.ID] = r
	if v, ok := r.Get("messageId"); ok {
		if id := normID(v); id != "" {
			if _, dup := s.byID[id]; dup {
				s.byID[id] = nil
			} else {
				s.byID[id] = r
			}
		}
	}
	if v, ok := r.Get("storeToken"); ok {
		if n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64); err == nil && n >= 0 {
			s.byOffset[n] = r
		}
	}
	if v, ok := r.Get("msgOffset"); ok {
		if n, err := strconv.ParseInt(strings.TrimSpace(v), 16, 64); err == nil && n >= 0 {
			s.byOffset[n] = r
		}
	}
}

func (s *sidecar) Inputs() []string { return s.inputs }

func (s *sidecar) TagInfo() map[string]source.TagStyle { return s.tags }

// Lookup matches by byte offset (storeToken or msgOffset), then Message-ID
// (when unique), then message key (the row id, given in decimal). A match
// whose own Message-ID contradicts msgID is rejected, so a stale summary
// (mbox compacted since) never attaches metadata to the wrong message.
func (s *sidecar) Lookup(msgID string, offset int64, statusKey string) (source.SidecarMeta, bool) {
	msgID = normID(msgID)
	consistent := func(r *Row) bool {
		have, _ := r.Get("messageId")
		have = normID(have)
		return r != nil && (msgID == "" || have == "" || have == msgID)
	}
	var row *Row
	if r := s.byOffset[offset]; offset >= 0 && r != nil && consistent(r) {
		row = r
	}
	if row == nil && msgID != "" {
		if r := s.byID[msgID]; r != nil {
			row = r
		}
	}
	if row == nil {
		if n, err := strconv.ParseUint(strings.TrimSpace(statusKey), 10, 64); err == nil {
			if r := s.byKey[n]; r != nil && consistent(r) {
				row = r
			}
		}
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
		n, _ := strconv.ParseInt(strings.TrimSpace(v), 16, 32)
		if n >= 4 {
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
