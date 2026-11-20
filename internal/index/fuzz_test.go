package index

import (
	"reflect"
	"testing"
	"unicode/utf8"
)

// FuzzParseQuery: ParseQuery never panics, keeps every value valid UTF-8
// and within the item cap, and is deterministic.
func FuzzParseQuery(f *testing.F) {
	for _, s := range []string{"", "a", `from:"x \" y" -is:read label:"a\\b"`, "before:2023/02/30 -after:9999",
		"-\"", `"unterminated`, "mailbox:a/b/ -folder:", "is:", ":::", "会议 -subject:会", "\xff\xfe"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		q := ParseQuery(s)
		if !reflect.DeepEqual(q, ParseQuery(s)) {
			t.Fatalf("not deterministic for %q", s)
		}
		n := 0
		v := reflect.ValueOf(q)
		for i := 0; i < v.NumField(); i++ {
			fv := v.Field(i)
			if fv.Kind() != reflect.Slice || fv.Type().Elem().Kind() != reflect.String {
				continue
			}
			for j := 0; j < fv.Len(); j++ {
				n++
				if !utf8.ValidString(fv.Index(j).String()) {
					t.Fatalf("invalid UTF-8 value %q from %q", fv.Index(j).String(), s)
				}
			}
		}
		if n > maxQueryTerms {
			t.Fatalf("%d values > cap %d for %q", n, maxQueryTerms, s)
		}
	})
}
