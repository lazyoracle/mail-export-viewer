package msf

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/lazyoracle/mail-export-viewer/internal/source"
)

// buildMork renders a synthetic .msf: a dictionary of column names, a
// dbfolderinfo row and one message row per map (key "_id" is the hex row id).
func buildMork(rows ...map[string]string) string {
	var cols []string
	seen := map[string]bool{}
	for _, r := range rows {
		for k := range r {
			if k != "_id" && !seen[k] {
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
	b.WriteString(">\n<(a=v)(F0=x)>\n{1:^80 {(k:^80:c)(s=9u)} [7F:^80(flags=1)]}\n{2:^81 {(k:^81:c)(s=9u)}\n")
	for _, r := range rows {
		fmt.Fprintf(&b, "  [%s:^81", r["_id"])
		for _, c := range cols {
			if v, ok := r[c]; ok {
				fmt.Fprintf(&b, "(^%X=%s)", id[c], v)
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

// profile lays out <tmp>/profile/Mail/pop.example.com/Inbox and returns the
// mbox path and profile dir.
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
		map[string]string{"_id": "1", "messageId": "m1@example.com", "storeToken": "0", "flags": "0",
			"size": "1F4", "keywords": "work $label2 junk nonjunk custom", "label": "2", "priority": "4", "junkscore": "100"},
		map[string]string{"_id": "2", "messageId": "m2@example.com", "msgOffset": "1000", "flags": "1003"},
		map[string]string{"_id": "3", "messageId": "dup@example.com", "storeToken": "5000", "flags": "1"},
		map[string]string{"_id": "4", "messageId": "dup@example.com", "storeToken": "6000", "flags": "5"},
		map[string]string{"_id": "A", "messageId": "m5@example.com", "flags": "1", "keywords": "$label1"},
	)
	mbox, prof := profile(t, msf, prefs)
	sc := source.SidecarFor(mbox)
	if sc == nil {
		t.Fatal("SidecarFor = nil, want the registered msf provider")
	}
	tests := []struct {
		name  string
		id    string
		off   int64
		key   string
		ok    bool
		flags source.Flags
		tags  []string
		props []source.Prop
	}{
		{"by storeToken", "", 0, "", true, source.Unread | source.Important | source.Junk,
			[]string{"Work \"Stuff\"", "Work", "custom"}, []source.Prop{{Key: "priority", Value: "high"}, {Key: "junkscore", Value: "100"}}},
		{"by id with bracket", "<m1@example.com>", -1, "", true, source.Unread | source.Important | source.Junk,
			[]string{"Work \"Stuff\"", "Work", "custom"}, []source.Prop{{Key: "priority", Value: "high"}, {Key: "junkscore", Value: "100"}}},
		{"by msgOffset hex", "", 0x1000, "", true, source.Answered | source.Forwarded, nil, nil},
		{"offset wins, id mismatch ignored", "other@example.com", 0x1000, "", false, 0, nil, nil},
		{"offset id agree", "m2@example.com", 0x1000, "", true, source.Answered | source.Forwarded, nil, nil},
		{"stale offset falls back to id", "m2@example.com", 77, "", true, source.Answered | source.Forwarded, nil, nil},
		{"ambiguous id not used alone", "dup@example.com", -1, "", false, 0, nil, nil},
		{"ambiguous id with offset", "dup@example.com", 6000, "", true, source.Starred, nil, nil},
		{"by key, id consistent", "m5@example.com", -1, "10", true, 0, []string{"Urgent"}, nil},
		{"by key, id differs", "zzz@example.com", -1, "10", false, 0, nil, nil},
		{"by key, no id", "", -1, "10", true, 0, []string{"Urgent"}, nil},
		{"unknown", "nope@example.com", 999, "99", false, 0, nil, nil},
		{"folder info row is not a message", "", -1, "127", false, 0, nil, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m, ok := sc.Lookup(tc.id, tc.off, tc.key)
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

func TestSidecarNoPrefs(t *testing.T) {
	mbox, _ := profile(t, buildMork(map[string]string{"_id": "1", "messageId": "m@example.com", "keywords": "$label4"}), "")
	sc := source.SidecarFor(mbox)
	if sc == nil {
		t.Fatal("nil sidecar")
	}
	m, ok := sc.Lookup("m@example.com", -1, "")
	if !ok || !reflect.DeepEqual(m.Tags, []string{"To Do"}) {
		t.Errorf("meta = %+v, %v", m, ok)
	}
	if got := sc.Inputs(); len(got) != 1 || got[0] != mbox+".msf" {
		t.Errorf("Inputs = %v", got)
	}
}

func TestSidecarUnusable(t *testing.T) {
	t.Run("missing", func(t *testing.T) {
		mbox, _ := profile(t, "", prefs)
		if sc := source.SidecarFor(mbox); sc != nil {
			t.Errorf("sidecar = %v, want nil", sc)
		}
	})
	t.Run("corrupt", func(t *testing.T) {
		mbox, _ := profile(t, "this is not a mork file", "")
		if sc := source.SidecarFor(mbox); sc != nil {
			t.Errorf("sidecar = %v, want nil", sc)
		}
	})
	t.Run("directory", func(t *testing.T) {
		mbox, _ := profile(t, "", "")
		if err := os.Mkdir(mbox+".msf", 0o755); err != nil {
			t.Fatal(err)
		}
		if sc := source.SidecarFor(mbox); sc != nil {
			t.Errorf("sidecar = %v, want nil", sc)
		}
	})
	t.Run("no message rows", func(t *testing.T) {
		mbox, _ := profile(t, buildMork(), "")
		if sc := source.SidecarFor(mbox); sc != nil {
			t.Errorf("sidecar = %v, want nil", sc)
		}
	})
}

func TestPrefsFoundFromSubfolder(t *testing.T) {
	prof := t.TempDir()
	write(t, filepath.Join(prof, "prefs.js"), prefs)
	mbox := filepath.Join(prof, "ImapMail", "imap.example.com", "INBOX.sbd", "Sub")
	write(t, mbox, "From a@example.com Mon Jan  1 00:00:00 2024\n\nx\n")
	write(t, mbox+".msf", buildMork(map[string]string{"_id": "1", "messageId": "m@example.com"}))
	sc := source.SidecarFor(mbox)
	if sc == nil {
		t.Fatal("nil sidecar")
	}
	if got := sc.TagInfo()["$label1"].Name; got != "Urgent" {
		t.Errorf("label1 = %q", got)
	}
}
