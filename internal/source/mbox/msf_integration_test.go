package mbox

import (
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/lazyoracle/mail-export-viewer/internal/source"
	_ "github.com/lazyoracle/mail-export-viewer/internal/source/msf" // the real sidecar provider
)

// mork renders a minimal synthetic .msf: rows are {message-id, storeToken,
// flags (hex), keywords}.
func mork(rows [][4]string) string {
	var b strings.Builder
	b.WriteString("// <!-- <mdb:mork:z v=\"1.4\"/> -->\n< <(a=c)> // (f=iso-8859-1)\n")
	b.WriteString("  (80=ns:msg:db:row:scope:dbfolderinfo:all)\n  (81=ns:msg:db:row:scope:msgs:all)\n")
	b.WriteString("  (90=message-id)\n  (91=storeToken)\n  (92=flags)\n  (93=keywords)\n>\n")
	b.WriteString("<(80=0)(81=lit)>\n{1:^80 {(k^BE:c)(s=9)} [7F:^80(flags=1)]}\n{2:^81 {(k^BE:c)(s=9)}\n")
	for i, r := range rows {
		fmt.Fprintf(&b, "  [%X:^81(^90=%s)(^91=%s)(^92=%s)", i+1, r[0], r[1], r[2])
		if r[3] != "" {
			fmt.Fprintf(&b, "(^93=%s)", strings.ReplaceAll(r[3], " ", "$20"))
		}
		b.WriteString("]\n")
	}
	b.WriteString("}\n")
	return b.String()
}

func TestRealMsfSidecar(t *testing.T) {
	m1 := "Subject: one\nMessage-ID: <m1@example.com>\n\nfirst\n"
	m2 := "Subject: two\nMessage-ID: <m2@example.com>\nX-Mozilla-Status: 0000\n\nsecond\n"
	m3 := "Subject: three\nMessage-ID: <m3@example.com>\nX-Mozilla-Keys: work\n\nthird\n"
	data := mboxOf(m1, m2, m3)
	off2 := int64(len(mboxOf(m1)))
	off1 := int64(0)

	root := t.TempDir()
	path := writeFile(t, filepath.Join(root, "Mail", "pop.example.com", "Inbox"), data)
	msf := mork([][4]string{
		// found by offset (storeToken) and Message-ID; unread, tag "work"
		{"m1@example.com", fmt.Sprint(off1), "0", "work"},
		// found by offset only (a different, wrong id elsewhere must not
		// matter): read + marked, label key 1
		{"m2@example.com", fmt.Sprint(off2), "5", "$label1"},
	})
	writeFile(t, path+".msf", msf)
	writeFile(t, filepath.Join(root, "prefs.js"), `user_pref("mailnews.tags.work.tag", "Work Stuff");
user_pref("mailnews.tags.work.color", "#FF9900");
user_pref("mailnews.tags.$label1.tag", "Urgent");
`)

	src := openMbox(t, path, "Mail/pop.example.com/Inbox")
	wantInputs := []string{path, path + ".msf", filepath.Join(root, "prefs.js")}
	if !reflect.DeepEqual(src.Inputs(), wantInputs) {
		t.Errorf("Inputs = %q, want %q", src.Inputs(), wantInputs)
	}
	es, warns := scan(t, src)
	if len(warns) != 0 || len(es) != 3 {
		t.Fatalf("%d entries, warnings %v", len(es), warns)
	}
	if es[0].Msg.Subject != "one" || es[0].Flags != source.Unread || !reflect.DeepEqual(es[0].Tags, []string{"Work Stuff"}) {
		t.Errorf("m1: flags %q tags %q", es[0].Flags, es[0].Tags)
	}
	// Header said unread (Status 0000); the sidecar is the truth: starred.
	if es[1].Msg.Subject != "two" || es[1].Flags != source.Starred || !reflect.DeepEqual(es[1].Tags, []string{"Urgent"}) {
		t.Errorf("m2: flags %q tags %q", es[1].Flags, es[1].Tags)
	}
	// No row: header keys, named through the sidecar's prefs.js TagInfo.
	if es[2].Msg.Subject != "three" || es[2].Flags != 0 || !reflect.DeepEqual(es[2].Tags, []string{"Work Stuff"}) {
		t.Errorf("m3: flags %q tags %q", es[2].Flags, es[2].Tags)
	}
}
