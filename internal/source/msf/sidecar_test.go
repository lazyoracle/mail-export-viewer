package msf

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/lazyoracle/mail-export-viewer/internal/source"
)

// buildMork renders a synthetic .msf with Thunderbird's column names: a
// column dictionary (a=c) holding scope and column names, a plain value
// dictionary (whose ids deliberately collide with the column ids), a
// dbfolderinfo row and one message row per map. Key "_id" is the hex row id;
// "_cut" writes the row as "[-id ...]". Spaces in values are written as $20.
func buildMork(rows ...map[string]string) string {
	var cols []string
	seen := map[string]bool{}
	for _, r := range rows {
		for k := range r {
			if k != "_id" && k != "_cut" && !seen[k] {
				seen[k] = true
				cols = append(cols, k)
			}
		}
	}
	sort.Strings(cols)
	var b strings.Builder
	b.WriteString("// <!-- <mdb:mork:z v=\"1.4\"/> -->\n< <(a=c)> // (f=iso-8859-1)\n")
	b.WriteString("  (80=ns:msg:db:row:scope:dbfolderinfo:all)\n  (81=ns:msg:db:row:scope:msgs:all)\n")
	id := map[string]int{}
	for i, c := range cols {
		id[c] = 0x90 + i
		fmt.Fprintf(&b, "  (%X=%s)\n", 0x90+i, c)
	}
	b.WriteString(">\n<(80=0)(81=lit)>\n{1:^80 {(k^BE:c)(s=9)} [7F:^80(flags=1)]}\n{2:^81 {(k^BE:c)(s=9)}\n")
	for _, r := range rows {
		neg := ""
		if r["_cut"] != "" {
			neg = "-"
		}
		fmt.Fprintf(&b, "  [%s%s:^81", neg, r["_id"])
		for _, c := range cols {
			if v, ok := r[c]; ok {
				fmt.Fprintf(&b, "(^%X=%s)", id[c], strings.ReplaceAll(v, " ", "$20"))
			}
		}
		b.WriteString("]\n")
	}
	b.WriteString("}\n")
	return b.String()
}

func write(t *testing.T, path, s string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
}

// profile lays out <tmp>/Mail/pop.example.com/Inbox and returns the mbox
// path and profile dir.
func profile(t *testing.T, msf string, prefs string) (mbox, prof string) {
	t.Helper()
	prof = t.TempDir()
	mbox = filepath.Join(prof, "Mail", "pop.example.com", "Inbox")
	write(t, mbox, "From a@example.com Mon Jan  1 00:00:00 2024\n\nbody\n")
	if msf != "" {
		write(t, mbox+".msf", msf)
	}
	if prefs != "" {
		write(t, filepath.Join(prof, "prefs.js"), prefs)
	}
	return mbox, prof
}

const prefs = `// comment
user_pref("mailnews.tags.work.tag", "Work \"Stuff\"");
user_pref("mailnews.tags.work.color", "#FF9900");
user_pref("mailnews.tags.$label1.tag", "Urgent");
user_pref("mailnews.tags.proj.tag", "Proj é");
user_pref("mailnews.tags.version", 2);
user_pref("other.thing", "x");
`

func TestSidecarLookup(t *testing.T) {
	msf := buildMork(
		map[string]string{"_id": "1", "message-id": "m1@example.com", "storeToken": "0", "flags": "0",
			"size": "1F4", "keywords": "work $label2 junk nonjunk custom", "label": "2", "priority": "5", "junkscore": "100"},
		map[string]string{"_id": "2", "message-id": "m2@example.com", "msgOffset": "1000", "flags": "1003"},
		map[string]string{"_id": "3", "message-id": "dup@example.com", "storeToken": "5000", "flags": "1"},
		map[string]string{"_id": "4", "message-id": "dup@example.com", "storeToken": "6000", "flags": "5"},
		map[string]string{"_id": "B", "message-id": "m5@example.com", "flags": "1", "keywords": "$label1", "priority": "4"},
		// Legacy folder without storeToken: the row id is the offset (0x7D0).
		map[string]string{"_id": "7D0", "message-id": "m7@example.com", "flags": "1"},
		// Written twice; the "[-" form cuts the earlier cells first.
		map[string]string{"_id": "9", "message-id": "m9@example.com", "storeToken": "9000", "flags": "5", "keywords": "stale"},
		map[string]string{"_id": "9", "_cut": "1", "message-id": "m9@example.com", "storeToken": "9000", "flags": "1"},
	)
	mbox, prof := profile(t, msf, prefs)
	sc, err := source.SidecarFor(mbox)
	if sc == nil || err != nil {
		t.Fatalf("SidecarFor = %v, %v; want the registered msf provider, no error", sc, err)
	}
	highProps := []source.Prop{{Key: "priority", Value: "high"}, {Key: "junkscore", Value: "100"}}
	row1 := source.Unread | source.Important | source.Junk
	tags1 := []string{"Work \"Stuff\"", "Work", "custom"}
	tests := []struct {
		name  string
		id    string
		off   int64
		ok    bool
		flags source.Flags
		tags  []string
		props []source.Prop
	}{
		{"by storeToken", "", 0, true, row1, tags1, highProps},
		{"storeToken and id", "m1@example.com", 0, true, row1, tags1, highProps},
		{"by id with bracket", "<m1@example.com>", -1, true, row1, tags1, highProps},
		{"by msgOffset hex", "", 0x1000, true, source.Answered | source.Forwarded, nil, nil},
		{"offset hit with other id is rejected", "other@example.com", 0x1000, false, 0, nil, nil},
		{"stale offset falls back to id", "m2@example.com", 77, true, source.Answered | source.Forwarded, nil, nil},
		{"ambiguous id not used alone", "dup@example.com", -1, false, 0, nil, nil},
		{"ambiguous id with offset", "dup@example.com", 6000, true, source.Starred, nil, nil},
		{"normal priority adds nothing", "m5@example.com", -1, true, 0, []string{"Urgent"}, nil},
		{"legacy row id is the offset", "m7@example.com", 0x7D0, true, 0, nil, nil},
		{"legacy needs a matching id", "", 0x7D0, false, 0, nil, nil},
		{"legacy rejects other id", "other@example.com", 0x7D0, false, 0, nil, nil},
		{"row id is not an offset when storeToken exists", "m1@example.com", 1, true, row1, tags1, highProps},
		{"cut row", "m9@example.com", 9000, true, 0, nil, nil},
		{"unknown", "nope@example.com", 999, false, 0, nil, nil},
		{"folder info row is not a message", "", 0x7F, false, 0, nil, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m, ok := sc.Lookup(tc.id, tc.off)
			if ok != tc.ok {
				t.Fatalf("ok = %v, want %v", ok, tc.ok)
			}
			if !ok {
				return
			}
			if m.Flags != tc.flags {
				t.Errorf("flags = %v, want %v", m.Flags, tc.flags)
			}
			if !reflect.DeepEqual(m.Tags, tc.tags) {
				t.Errorf("tags = %q, want %q", m.Tags, tc.tags)
			}
			if !reflect.DeepEqual(m.Props, tc.props) {
				t.Errorf("props = %v, want %v", m.Props, tc.props)
			}
		})
	}

	if got, want := sc.Inputs(), []string{mbox + ".msf", filepath.Join(prof, "prefs.js")}; !reflect.DeepEqual(got, want) {
		t.Errorf("Inputs = %v, want %v", got, want)
	}
	ti := sc.TagInfo()
	want := map[string]source.TagStyle{
		"$label1": {Name: "Urgent", Color: "#FF0000"},
		"$label2": {Name: "Work", Color: "#FF9900"},
		"$label3": {Name: "Personal", Color: "#009900"},
		"$label4": {Name: "To Do", Color: "#3333FF"},
		"$label5": {Name: "Later", Color: "#993399"},
		"work":    {Name: "Work \"Stuff\"", Color: "#FF9900"},
		"proj":    {Name: "Proj é", Color: ""},
	}
	if !reflect.DeepEqual(ti, want) {
		t.Errorf("TagInfo = %v\nwant %v", ti, want)
	}
}

func TestPriorityMapping(t *testing.T) {
	var rows []map[string]string
	for p := 0; p <= 6; p++ {
		rows = append(rows, map[string]string{"_id": fmt.Sprintf("%X", p+1), "message-id": fmt.Sprintf("p%d@example.com", p), "priority": fmt.Sprintf("%X", p), "flags": "1"})
	}
	mbox, _ := profile(t, buildMork(rows...), "")
	sc, _ := source.SidecarFor(mbox)
	want := []struct {
		f source.Flags
		p string
	}{{0, ""}, {0, ""}, {0, "lowest"}, {0, "low"}, {0, ""}, {source.Important, "high"}, {source.Important, "highest"}}
	for p, w := range want {
		m, ok := sc.Lookup(fmt.Sprintf("p%d@example.com", p), -1)
		if !ok || m.Flags != w.f {
			t.Errorf("priority %d: flags = %v ok=%v, want %v", p, m.Flags, ok, w.f)
		}
		got := ""
		if len(m.Props) > 0 {
			got = m.Props[0].Value
		}
		if got != w.p {
			t.Errorf("priority %d: prop = %q, want %q", p, got, w.p)
		}
	}
}

func TestSidecarNoPrefs(t *testing.T) {
	mbox, _ := profile(t, buildMork(map[string]string{"_id": "1", "message-id": "m@example.com", "keywords": "$label4 $phishing notjunk"}), "")
	sc, err := source.SidecarFor(mbox)
	if sc == nil || err != nil {
		t.Fatalf("sidecar = %v, %v", sc, err)
	}
	m, ok := sc.Lookup("m@example.com", -1)
	if !ok || !reflect.DeepEqual(m.Tags, []string{"To Do"}) {
		t.Errorf("meta = %+v, %v", m, ok)
	}
	if got := sc.Inputs(); len(got) != 1 || got[0] != mbox+".msf" {
		t.Errorf("Inputs = %v", got)
	}
}

func TestSidecarUnusable(t *testing.T) {
	tests := []struct {
		name    string
		setup   func(t *testing.T) string
		wantErr bool
	}{
		{"missing", func(t *testing.T) string { m, _ := profile(t, "", ""); return m }, false},
		{"corrupt", func(t *testing.T) string { m, _ := profile(t, "this is not a mork file", ""); return m }, true},
		{"directory", func(t *testing.T) string {
			m, _ := profile(t, "", "")
			if err := os.Mkdir(m+".msf", 0o755); err != nil {
				t.Fatal(err)
			}
			return m
		}, true},
		{"empty folder", func(t *testing.T) string { m, _ := profile(t, buildMork(), ""); return m }, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			sc, err := source.SidecarFor(tc.setup(t))
			if sc != nil || (err != nil) != tc.wantErr {
				t.Errorf("SidecarFor = %v, %v; want nil sidecar, error=%v", sc, err, tc.wantErr)
			}
		})
	}
	t.Run("oversized", func(t *testing.T) {
		mbox, _ := profile(t, buildMork(map[string]string{"_id": "1", "message-id": "m@example.com"}), "")
		old := maxInput
		maxInput = 20
		defer func() { maxInput = old }()
		if sc, err := source.SidecarFor(mbox); sc != nil || err == nil {
			t.Errorf("SidecarFor = %v, %v; want nil sidecar and an error", sc, err)
		}
	})
}

// Without a usable .msf the folder still gets the profile's tag names
// (issue #14): a sidecar that knows no message but has TagInfo.
func TestSidecarPrefsWithoutMsf(t *testing.T) {
	for _, tc := range []struct {
		name, msf string
		wantErr   bool
	}{
		{"missing", "", false},
		{"empty", buildMork(), false},
		{"corrupt", "this is not a mork file", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mbox, prof := profile(t, tc.msf, prefs)
			sc, err := source.SidecarFor(mbox)
			if sc == nil || (err != nil) != tc.wantErr {
				t.Fatalf("SidecarFor = %v, %v; want a tags-only sidecar, error=%v", sc, err, tc.wantErr)
			}
			if got := sc.TagInfo()["work"].Name; got != `Work "Stuff"` {
				t.Errorf("TagInfo[work] = %q", got)
			}
			if _, ok := sc.Lookup("m1@example.com", 0); ok {
				t.Errorf("Lookup found a message without a .msf")
			}
			if !slices.Contains(sc.Inputs(), filepath.Join(prof, "prefs.js")) {
				t.Errorf("Inputs %v lack prefs.js", sc.Inputs())
			}
		})
	}
}

func TestSidecarDegraded(t *testing.T) {
	// An interrupted (uncommitted) transaction is rolled back: usable, with a warning.
	msf := buildMork(map[string]string{"_id": "1", "message-id": "m@example.com", "flags": "1"}) + "@$${9{@[1:^81(flags=0)]\n"
	mbox, _ := profile(t, msf, "")
	sc, err := source.SidecarFor(mbox)
	if sc == nil || err == nil {
		t.Fatalf("SidecarFor = %v, %v; want sidecar and warning", sc, err)
	}
	if m, ok := sc.Lookup("m@example.com", -1); !ok || m.Flags != 0 {
		t.Errorf("Lookup = %+v, %v (rolled-back write must not show)", m, ok)
	}
}

func TestPrefsFoundFromSubfolder(t *testing.T) {
	prof := t.TempDir()
	write(t, filepath.Join(prof, "prefs.js"), prefs)
	mbox := filepath.Join(prof, "ImapMail", "imap.example.com", "INBOX.sbd", "Sub")
	write(t, mbox, "From a@example.com Mon Jan  1 00:00:00 2024\n\nx\n")
	write(t, mbox+".msf", buildMork(map[string]string{"_id": "1", "message-id": "m@example.com"}))
	sc, _ := source.SidecarFor(mbox)
	if sc == nil {
		t.Fatal("nil sidecar")
	}
	if got := sc.TagInfo()["$label1"].Name; got != "Urgent" {
		t.Errorf("label1 = %q", got)
	}
}

func TestPrefsPreferProfileRoot(t *testing.T) {
	// A stray prefs.js nearer the mbox must not shadow the profile's, which
	// is recognised by its Mail/ or ImapMail/ child.
	prof := t.TempDir()
	write(t, filepath.Join(prof, "prefs.js"), prefs)
	mbox := filepath.Join(prof, "Mail", "pop.example.com", "Inbox")
	write(t, mbox, "From a@example.com Mon Jan  1 00:00:00 2024\n\nx\n")
	write(t, mbox+".msf", buildMork(map[string]string{"_id": "1", "message-id": "m@example.com"}))
	write(t, filepath.Join(prof, "Mail", "prefs.js"), `user_pref("mailnews.tags.$label1.tag", "Wrong");`)
	sc, _ := source.SidecarFor(mbox)
	if sc == nil {
		t.Fatal("nil sidecar")
	}
	if got := sc.TagInfo()["$label1"].Name; got != "Urgent" {
		t.Errorf("label1 = %q, want Urgent from the profile root", got)
	}
}
