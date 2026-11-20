package mbox

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/lazyoracle/mail-export-viewer/internal/source"
	"github.com/lazyoracle/mail-export-viewer/internal/source/sourcetest"
)

// fakeSidecars lets tests attach a fake sidecar to mbox paths; the provider
// registry has no unregister, so one provider is registered for all tests.
var (
	sidecarMu   sync.Mutex
	fakeSidecar = map[string]source.Sidecar{}
	fakeErr     = map[string]error{}
)

func init() {
	source.RegisterSidecarProvider(func(p string) (source.Sidecar, error) {
		sidecarMu.Lock()
		defer sidecarMu.Unlock()
		return fakeSidecar[p], fakeErr[p]
	})
}

func setSidecar(t *testing.T, path string, sc source.Sidecar) {
	t.Helper()
	setProvider(t, path, sc, nil)
}

func setProvider(t *testing.T, path string, sc source.Sidecar, err error) {
	t.Helper()
	sidecarMu.Lock()
	fakeSidecar[path], fakeErr[path] = sc, err
	sidecarMu.Unlock()
	t.Cleanup(func() {
		sidecarMu.Lock()
		delete(fakeSidecar, path)
		delete(fakeErr, path)
		sidecarMu.Unlock()
	})
}

type stubSidecar struct {
	byID   map[string]source.SidecarMeta
	inputs []string
	tags   map[string]source.TagStyle
	seen   []lookupArgs
}

type lookupArgs struct {
	id     string
	offset int64
}

func (s *stubSidecar) Lookup(id string, off int64) (source.SidecarMeta, bool) {
	s.seen = append(s.seen, lookupArgs{id, off})
	m, ok := s.byID[id]
	return m, ok
}
func (s *stubSidecar) Inputs() []string                    { return s.inputs }
func (s *stubSidecar) TagInfo() map[string]source.TagStyle { return s.tags }

const (
	msgPlain = "Subject: plain\nMessage-ID: <plain@example.com>\nFrom: Ann <ann@example.com>\nDate: Mon, 1 Jan 2024 10:00:00 +0000\n\nhello\n>From quoted\n"
	msgAtt   = "Subject: with attachment\nMessage-ID: <att@example.com>\nMIME-Version: 1.0\nContent-Type: multipart/mixed; boundary=B\n\n--B\nContent-Type: text/plain\n\nsee file\n--B\nContent-Type: text/csv; name=\"a.csv\"\nContent-Disposition: attachment; filename=\"a.csv\"\n\nx,y\n--B--\n"
	msgGm    = "Subject: labelled\nMessage-ID: <gm@example.com>\nX-Gmail-Labels: Inbox,Unread,Starred,Trips\nX-Mailer: Fake 1\n\nlabels\n"
	msgTB    = "Subject: deleted\nMessage-ID: <tb@example.com>\nX-Mozilla-Status: 0009\nX-Mozilla-Keys: $label1 proj\n\ngone\n"
)

func mboxOf(msgs ...string) string {
	var b strings.Builder
	for i, m := range msgs {
		b.WriteString("From sender@example.com Mon Jan  " + string(rune('1'+i)) + " 10:00:00 2024\n" + m + "\n")
	}
	return b.String()
}

func writeFile(t *testing.T, path, data string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func openMbox(t *testing.T, path, rel string) source.Source {
	t.Helper()
	src, err := source.Candidate{Format: source.Mbox, Path: path, Rel: rel}.Open()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { src.Close() })
	return src
}

func scan(t *testing.T, src source.Source) (entries []source.Entry, warnings []error) {
	t.Helper()
	err := src.Scan(context.Background(), func(e source.Entry) error {
		e.Resolve()
		entries = append(entries, e)
		return nil
	}, func(err error) { warnings = append(warnings, err) })
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	return
}

func TestConformance(t *testing.T) {
	dir := t.TempDir()
	path := writeFile(t, filepath.Join(dir, "Inbox.mbox"), mboxOf(msgPlain, msgAtt, msgGm, msgTB))
	src := openMbox(t, path, "Inbox.mbox")
	es, _ := scan(t, src)
	if len(es) != 4 {
		t.Fatalf("%d entries", len(es))
	}
	stale := []source.Locator{
		{Offset: es[0].Loc.Offset + 1, Length: es[0].Loc.Length - 1}, // not at a From_ line
		{Offset: es[0].Loc.Offset, Length: es[0].Loc.Length - 5},     // ends inside a message
		{Offset: es[3].Loc.Offset, Length: es[3].Loc.Length + 10},    // past EOF
		{Offset: 1 << 40, Length: 10},
		{Offset: 0, Length: 0},
		{Offset: -1, Length: 5},
	}
	sourcetest.Check(t, src, stale...)
}

func TestEntryContents(t *testing.T) {
	dir := t.TempDir()
	path := writeFile(t, filepath.Join(dir, "Archive.sbd", "2019"), mboxOf(msgPlain, msgAtt, msgGm, msgTB))
	src := openMbox(t, path, "Archive.sbd/2019")
	if src.Kind() != "mbox" || src.Path() != path {
		t.Fatalf("Kind %q Path %q", src.Kind(), src.Path())
	}
	es, warns := scan(t, src)
	if len(warns) != 0 || len(es) != 4 {
		t.Fatalf("%d entries, warnings %v", len(es), warns)
	}
	for _, e := range es {
		if e.Folder != "Archive/2019" {
			t.Errorf("folder %q", e.Folder)
		}
	}
	if es[0].Msg.Subject != "plain" || es[0].Flags != 0 || es[0].Labels != nil || es[0].Tags != nil {
		t.Errorf("plain: %+v flags %q", es[0].Msg, es[0].Flags)
	}
	if es[1].Flags != source.HasAttachment || !es[1].Msg.HasAttachment {
		t.Errorf("attachment flags %q", es[1].Flags)
	}
	if want := source.Unread | source.Starred; es[2].Flags != want ||
		!reflect.DeepEqual(es[2].Labels, []string{"Inbox", "Unread", "Starred", "Trips"}) ||
		!reflect.DeepEqual(es[2].Props, []source.Prop{{Key: "X-Mailer", Value: "Fake 1"}}) {
		t.Errorf("gmail: flags %q labels %q props %q", es[2].Flags, es[2].Labels, es[2].Props)
	}
	// Thunderbird-deleted messages are listed with the Deleted flag.
	if es[3].Flags != source.Deleted || !reflect.DeepEqual(es[3].Tags, []string{"Important", "proj"}) {
		t.Errorf("thunderbird: flags %q tags %q", es[3].Flags, es[3].Tags)
	}
	if es[0].Msg.Date.IsZero() {
		t.Error("no date")
	}
	// The From_ line date is the fallback for a message without Date.
	if es[1].Msg.Date.Day() != 2 || es[1].Msg.Date.Year() != 2024 {
		t.Errorf("fallback date %v", es[1].Msg.Date)
	}
	// Load unquotes and Raw returns the message without its From_ line.
	m, err := src.Load(es[0].Loc)
	if err != nil || !strings.Contains(m.Text, "From quoted") || strings.Contains(m.Text, ">From") {
		t.Errorf("Load: %v %+v", err, m)
	}
	raw, err := src.Raw(es[0].Loc)
	if err != nil || string(raw) != strings.Replace(msgPlain, ">From", "From", 1) {
		t.Errorf("Raw = %q, %v", raw, err)
	}
	a, data, err := src.Attachment(es[1].Loc, 0)
	if err != nil || a.Filename != "a.csv" || strings.TrimSpace(string(data)) != "x,y" {
		t.Errorf("Attachment = %+v %q %v", a, data, err)
	}
}

func TestFolderName(t *testing.T) {
	for _, tc := range []struct {
		rel, want string
		ext       bool
	}{
		{"Inbox", "Inbox", true},
		{"Inbox.mbox", "Inbox", true},
		{"All mail Including Spam and Trash.MBOX", "All mail Including Spam and Trash", true},
		{"x.mbx", "x", true},
		{"x.mbs", "x", true},
		{"x.txt", "x.txt", true},
		{".mbox", ".mbox", true},
		{"Archive.sbd/2019", "Archive/2019", true},
		{"Local Folders/Archive.sbd/2019.sbd/Q1", "Local Folders/Archive/2019/Q1", true},
		{"Mail/Archive.sbd/2019.mbox", "Mail/Archive/2019", true},
		{"a.sbd/b.sbd", "a/b.sbd", true},
		{"a.sbd/b.sbd", "a/b", false},
		{"dir.mbox", "dir.mbox", false},
		{"caf�.mbox", "caf�", true},
	} {
		if got := folderName(tc.rel, tc.ext); got != tc.want {
			t.Errorf("folderName(%q, %v) = %q, want %q", tc.rel, tc.ext, got, tc.want)
		}
	}
}

func TestFolderFallsBackToBaseName(t *testing.T) {
	dir := t.TempDir()
	path := writeFile(t, filepath.Join(dir, "Sent.mbox"), mboxOf(msgPlain))
	es, _ := scan(t, openMbox(t, path, ""))
	if es[0].Folder != "Sent" {
		t.Errorf("folder %q", es[0].Folder)
	}
}

func TestSidecar(t *testing.T) {
	dir := t.TempDir()
	path := writeFile(t, filepath.Join(dir, "Inbox"), mboxOf(msgGm, msgTB, msgPlain))
	sc := &stubSidecar{
		byID: map[string]source.SidecarMeta{
			"gm@example.com": {Flags: source.Answered, Tags: []string{"Work", "Inbox"}, Labels: []string{"Inbox", "Extra"},
				Props: []source.Prop{{Key: "X-Mailer", Value: "Fake 1"}, {Key: "Priority", Value: "High"}}},
		},
		inputs: []string{filepath.Join(dir, "Inbox.msf")},
		tags:   map[string]source.TagStyle{"proj": {Name: "Project X", Color: "#00FF00"}},
	}
	setSidecar(t, path, sc)
	src := openMbox(t, path, "Inbox")
	if want := []string{path, filepath.Join(dir, "Inbox.msf")}; !reflect.DeepEqual(src.Inputs(), want) {
		t.Errorf("Inputs = %q, want %q", src.Inputs(), want)
	}
	es, _ := scan(t, src)
	// Sidecar flags replace header flags (Unread/Starred dropped); header
	// tags/labels/props are kept and sidecar ones appended without duplicates.
	if es[0].Flags != source.Answered ||
		!reflect.DeepEqual(es[0].Tags, []string{"Work", "Inbox"}) ||
		!reflect.DeepEqual(es[0].Labels, []string{"Inbox", "Unread", "Starred", "Trips", "Extra"}) ||
		!reflect.DeepEqual(es[0].Props, []source.Prop{{Key: "X-Mailer", Value: "Fake 1"}, {Key: "Priority", Value: "High"}}) {
		t.Errorf("sidecar row: flags %q tags %q labels %q props %q", es[0].Flags, es[0].Tags, es[0].Labels, es[0].Props)
	}
	// Without a row the header data stays, and TagInfo names its keys.
	if es[1].Flags != source.Deleted || !reflect.DeepEqual(es[1].Tags, []string{"Important", "Project X"}) {
		t.Errorf("no row: flags %q tags %q", es[1].Flags, es[1].Tags)
	}
	if es[2].Flags != 0 {
		t.Errorf("third: %q", es[2].Flags)
	}
	// Lookup got the message-id and the From_ offset.
	if len(sc.seen) < 3 || sc.seen[0] != (lookupArgs{"gm@example.com", 0}) ||
		sc.seen[1] != (lookupArgs{"tb@example.com", es[1].Loc.Offset}) {
		t.Errorf("Lookup calls: %+v", sc.seen)
	}
	// HasAttachment (set by Resolve) survives the replacement of flags.
	path2 := writeFile(t, filepath.Join(dir, "Other"), mboxOf(msgAtt))
	setSidecar(t, path2, &stubSidecar{byID: map[string]source.SidecarMeta{"att@example.com": {Flags: source.Starred}}})
	es, _ = scan(t, openMbox(t, path2, "Other"))
	if es[0].Flags != source.Starred|source.HasAttachment {
		t.Errorf("attachment + sidecar: %q", es[0].Flags)
	}
}

func TestLoadValidatesOffsets(t *testing.T) {
	dir := t.TempDir()
	path := writeFile(t, filepath.Join(dir, "Inbox"), mboxOf(msgPlain, msgGm))
	src := openMbox(t, path, "Inbox")
	es, _ := scan(t, src)
	// The file is rewritten (a message removed): the old locator of the
	// second message must not serve whatever is there now.
	writeFile(t, path, mboxOf(msgGm))
	if _, err := src.Load(es[1].Loc); !errors.Is(err, source.ErrStale) {
		t.Errorf("Load after rewrite = %v, want ErrStale", err)
	}
	if _, err := src.Raw(source.Locator{Offset: es[0].Loc.Offset, Length: es[0].Loc.Length + 1}); !errors.Is(err, source.ErrStale) {
		t.Errorf("Raw with bad length = %v", err)
	}
	// File gone.
	os.Remove(path)
	if _, err := src.Load(es[0].Loc); err == nil {
		t.Error("Load of a removed file succeeded")
	}
}

func TestTooLarge(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "Big")
	writeFile(t, path, "From a@example.com Mon Jan  1 00:00:00 2024\nSubject: x\n\n")
	size := int64(source.MaxMessageBytes) + 1024
	if err := os.Truncate(path, size); err != nil { // sparse
		t.Fatal(err)
	}
	src := openMbox(t, path, "Big")
	loc := source.Locator{Length: size}
	if _, err := src.Load(loc); !errors.Is(err, source.ErrTooLarge) {
		t.Errorf("Load = %v", err)
	}
	if _, err := src.Raw(loc); !errors.Is(err, source.ErrTooLarge) {
		t.Errorf("Raw = %v", err)
	}
	if _, _, err := src.Attachment(loc, 0); !errors.Is(err, source.ErrTooLarge) {
		t.Errorf("Attachment = %v", err)
	}
}

func TestScanListsHugeMessageFromPrefix(t *testing.T) {
	old := scanPrefix
	scanPrefix = 256
	t.Cleanup(func() { scanPrefix = old })
	dir := t.TempDir()
	body := strings.Repeat("0123456789\n", 1000)
	path := writeFile(t, filepath.Join(dir, "Inbox"), mboxOf("Subject: huge\nMessage-ID: <huge@example.com>\n\n"+body, msgPlain))
	src := openMbox(t, path, "Inbox")
	es, _ := scan(t, src)
	if len(es) != 2 || es[0].Msg.Subject != "huge" || es[1].Msg.Subject != "plain" {
		t.Fatalf("entries %+v", es)
	}
	m, err := src.Load(es[0].Loc) // Load is not limited by the scan prefix
	if err != nil || !strings.HasSuffix(m.Text, "0123456789\n") || len(m.Text) < 10000 {
		t.Errorf("Load: %v, %d bytes", err, len(m.Text))
	}
}

func TestScanErrorsAndWarnings(t *testing.T) {
	dir := t.TempDir()
	if _, err := (source.Candidate{Format: source.Mbox, Path: filepath.Join(dir, "missing")}).Open(); err == nil {
		t.Error("Open of a missing file succeeded")
	}
	if _, err := (source.Candidate{Format: source.Mbox, Path: dir}).Open(); err == nil {
		t.Error("Open of a directory succeeded")
	}
	// A file without messages yields nothing and no error.
	path := writeFile(t, filepath.Join(dir, "Empty"), "")
	es, warns := scan(t, openMbox(t, path, "Empty"))
	if len(es) != 0 || len(warns) != 0 {
		t.Errorf("empty: %v %v", es, warns)
	}
	// Scan fails when the file vanished after Open.
	src := openMbox(t, writeFile(t, filepath.Join(dir, "Gone"), mboxOf(msgPlain)), "Gone")
	os.Remove(filepath.Join(dir, "Gone"))
	if err := src.Scan(context.Background(), func(source.Entry) error { return nil }, func(error) {}); err == nil {
		t.Error("Scan of a removed file succeeded")
	}
}

func TestDiscoverThunderbirdTree(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "Inbox"), mboxOf(msgPlain))
	writeFile(t, filepath.Join(root, "Inbox.msf"), "// <!-- <mdb:mork:z v=\"1.4\"/> -->\n")
	writeFile(t, filepath.Join(root, "Archive"), mboxOf(msgAtt))
	writeFile(t, filepath.Join(root, "Archive.sbd", "2019"), mboxOf(msgGm))
	writeFile(t, filepath.Join(root, "Gmail", "All mail.mbox"), mboxOf(msgTB))
	d, err := source.Discover(root, source.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]int{}
	for _, c := range d.Candidates {
		if c.Format != source.Mbox {
			t.Fatalf("candidate %+v", c)
		}
		src, err := c.Open()
		if err != nil {
			t.Fatal(err)
		}
		es, _ := scan(t, src)
		for _, e := range es {
			got[e.Folder]++
		}
		src.Close()
	}
	want := map[string]int{"Inbox": 1, "Archive": 1, "Archive/2019": 1, "Gmail/All mail": 1}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("folders = %v, want %v", got, want)
	}
}

func TestPanicRecovery(t *testing.T) {
	var warned []error
	e, ok := safeEntry(func() source.Entry { panic("boom") }, func(err error) { warned = append(warned, err) })
	if ok || len(warned) != 1 || !strings.Contains(warned[0].Error(), "boom") {
		t.Errorf("safeEntry = %+v %v, warnings %v", e, ok, warned)
	}
}

func TestSidecarProviderConventions(t *testing.T) {
	dir := t.TempDir()
	boom := errors.New("sidecar broke")
	row := map[string]source.SidecarMeta{"gm@example.com": {Flags: source.Answered}}
	for _, tc := range []struct {
		name      string
		sc        source.Sidecar
		err       error
		wantFlags source.Flags // of the first message (msgGm: Unread|Starred from headers)
		wantWarn  bool
		wantInput bool
	}{
		{"none", nil, nil, source.Unread | source.Starred, false, false},
		{"unusable keeps header metadata", nil, boom, source.Unread | source.Starred, true, false},
		{"usable", &stubSidecar{byID: row, inputs: []string{"/x/Inbox.msf"}}, nil, source.Answered, false, true},
		{"degraded is used and warned", &stubSidecar{byID: row, inputs: []string{"/x/Inbox.msf"}}, boom, source.Answered, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := writeFile(t, filepath.Join(dir, strings.ReplaceAll(tc.name, " ", "_")), mboxOf(msgGm))
			setProvider(t, path, tc.sc, tc.err)
			src := openMbox(t, path, "x")
			var warns []error
			var es []source.Entry
			err := src.Scan(context.Background(), func(e source.Entry) error { e.Resolve(); es = append(es, e); return nil },
				func(err error) { warns = append(warns, err) })
			if err != nil || len(es) != 1 {
				t.Fatalf("Scan: %v, %d entries", err, len(es))
			}
			if es[0].Flags != tc.wantFlags {
				t.Errorf("flags %q, want %q", es[0].Flags, tc.wantFlags)
			}
			if tc.wantWarn != (len(warns) == 1 && errors.Is(warns[0], boom)) || (!tc.wantWarn && len(warns) != 0) {
				t.Errorf("warnings %v, want warn=%v", warns, tc.wantWarn)
			}
			if got := len(src.Inputs()) == 2; got != tc.wantInput {
				t.Errorf("Inputs %q", src.Inputs())
			}
			// Reported once per Scan, not per message; a second Scan warns again.
		})
	}
}
