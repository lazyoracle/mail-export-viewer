package index

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	_ "github.com/lazyoracle/mail-export-viewer/internal/source/msf" // the real sidecar provider
	"github.com/lazyoracle/mail-export-viewer/internal/testutil"
)

// morkSidecar renders a minimal synthetic .msf: rows are {message-id,
// storeToken (From_ offset), flags (hex), keywords}.
func morkSidecar(rows [][4]string) string {
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

// A Thunderbird tag colour from prefs.js travels mbox -> msf sidecar ->
// source.TagStyler -> index facets and rows.
func TestIntegrationThunderbirdTagColour(t *testing.T) {
	root := t.TempDir()
	inbox := testutil.WriteMbox(t, filepath.Join(root, "Mail", "Local Folders", "Inbox"),
		"From: Carol <carol@example.net>\nSubject: tagged\nMessage-ID: <c1@example.net>\n\nbody\n")
	os.WriteFile(inbox+".msf", []byte(morkSidecar([][4]string{{"c1@example.net", "0", "1", "work"}})), 0o644)
	os.WriteFile(filepath.Join(root, "prefs.js"), []byte(`user_pref("mailnews.tags.work.tag", "Work Stuff");
user_pref("mailnews.tags.work.color", "#FF9900");
`), 0o644)
	srcs := openAll(t, root)
	s := openStore(t)
	index(t, s, list(srcs)...)
	var found bool
	for _, tg := range facets(t, s).Tags {
		if tg.Name == "Work Stuff" {
			found = true
			if tg.Color != "#FF9900" || tg.Count != 1 {
				t.Errorf("tag = %+v", tg)
			}
		}
	}
	if !found {
		t.Fatalf("tags = %+v", facets(t, s).Tags)
	}
	r := search(t, s, SearchParams{Q: `tag:"work stuff"`}).Items
	if len(r) != 1 || len(r[0].Tags) != 1 || r[0].Tags[0].Color != "#FF9900" {
		t.Errorf("rows = %+v", r)
	}
}
