package msf

import (
	"errors"
	"strings"
	"testing"
	"time"
)

const header = "// <!-- <mdb:mork:z v=\"1.4\"/> -->\n"

func cells(r *Row) map[string]string {
	m := map[string]string{}
	for _, c := range r.Cells {
		m[c.Col] = c.Val
	}
	return m
}

func mustParse(t *testing.T, s string) *File {
	t.Helper()
	f, err := Parse([]byte(s))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return f
}

func TestParseRows(t *testing.T) {
	tests := []struct {
		name  string
		body  string
		scope string
		id    uint64
		want  map[string]string
	}{
		{"refs and literal", "< <(a=c)> (80=flags)(81=subject)(82=hello world)(83=scope:x)>\n{1:^83 [A:^83(^80=9)(^81^82)(plain=lit)]}\n", "scope:x", 0xA, map[string]string{"flags": "9", "subject": "hello world", "plain": "lit"}},
		{"hex and escapes", "<(80=c)(81=a$29b\\)c\\\\d$C3$A9)>[1:^80(^80^81)]", "c", 1, map[string]string{"c": "a)b)c\\dé"}},
		{"continuation", "<(80=c)>[1:^80(^80=ab\\\ncd)]", "c", 1, map[string]string{"c": "abcd"}},
		{"latin1 fallback", "<(80=c)(81=caf$E9)>[1:^80(^80^81)]", "c", 1, map[string]string{"c": "café"}},
		{"table scope inherited", "<(80=sc)(81=k)(82=v)>{1:^80 {(k:^80)(s=9)} [5(^81^82)]}", "sc", 5, map[string]string{"k": "v"}},
		{"unscoped", "[7(x=y)]", "", 7, map[string]string{"x": "y"}},
		{"literal scope", "[7:c(x=y)]", "c", 7, map[string]string{"x": "y"}},
		{"comments", "// c\n/* x */ [7(x=y) // t\n(z=w)]", "", 7, map[string]string{"x": "y", "z": "w"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := mustParse(t, header+tc.body)
			if len(f.Rows) != 1 {
				t.Fatalf("rows = %d, want 1: %+v", len(f.Rows), f.Rows)
			}
			r := f.Rows[0]
			if r.Scope != tc.scope || r.ID != tc.id {
				t.Errorf("row = (%q,%x), want (%q,%x)", r.Scope, r.ID, tc.scope, tc.id)
			}
			got := cells(r)
			if len(got) != len(tc.want) {
				t.Errorf("cells = %v, want %v", got, tc.want)
			}
			for k, v := range tc.want {
				if got[k] != v {
					t.Errorf("cell %q = %q, want %q", k, got[k], v)
				}
			}
		})
	}
}

func TestParseMergeAndDelete(t *testing.T) {
	f := mustParse(t, header+"<(80=a)(81=b)>[1:s(^80=1)(^81=2)][2:s(^80=x)][1:s(^80=9)-(^81)][-2:s][3:s]")
	if len(f.Rows) != 2 {
		t.Fatalf("rows = %+v", f.Rows)
	}
	if got := cells(f.Rows[0]); len(got) != 1 || got["a"] != "9" {
		t.Errorf("row 1 cells = %v", got)
	}
	if f.Rows[1].ID != 3 || len(f.Rows[1].Cells) != 0 {
		t.Errorf("row 3 = %+v", f.Rows[1])
	}
}

func TestParseGroups(t *testing.T) {
	body := "<(80=a)>[1:s(^80=old)]\n" +
		"@$${5{@\n[1:s(^80=new)][2:s(^80=x)]\n@$$}5}@\n" +
		"@$${6{@\n[1:s(^80=lost)][3:s(^80=y)]<(81=late)>\n@$$}~~}@\n" +
		"@$${7{@\n[4:s(^80=cut)]\n" // never committed
	f := mustParse(t, header+body)
	var ids []uint64
	for _, r := range f.Rows {
		ids = append(ids, r.ID)
	}
	if len(ids) != 2 || ids[0] != 1 || ids[1] != 2 {
		t.Fatalf("rows = %v", ids)
	}
	if v := cells(f.Rows[0])["a"]; v != "new" {
		t.Errorf("row 1 a = %q, want new", v)
	}
}

func TestParseErrors(t *testing.T) {
	if _, err := Parse([]byte("From a@example.com\nhello")); !errors.Is(err, ErrNotMork) {
		t.Errorf("err = %v, want ErrNotMork", err)
	}
	old := maxInput
	maxInput = 10
	defer func() { maxInput = old }()
	if _, err := Parse([]byte(header + "[1(a=b)]")); !errors.Is(err, ErrTooLarge) {
		t.Errorf("err = %v, want ErrTooLarge", err)
	}
}

func TestParseHostile(t *testing.T) {
	inputs := map[string]string{
		"deep braces":   strings.Repeat("{", 200000) + "[1(a=b)]",
		"deep brackets": strings.Repeat("[1(", 100000),
		"unterminated":  "<(80=" + strings.Repeat("x", 3<<20),
		"groups":        strings.Repeat("@$${1{@", 10000),
		"parens":        strings.Repeat("(", 100000),
		"huge id":       "[" + strings.Repeat("F", 1000) + ":" + strings.Repeat("s", 1000) + "(a=b)]",
		"dict meta":     "<" + strings.Repeat("<", 100000),
	}
	for name, in := range inputs {
		t.Run(name, func(t *testing.T) {
			start := time.Now()
			f, err := Parse([]byte(header + in))
			if err != nil || f == nil {
				t.Fatalf("Parse = %v, %v", f, err)
			}
			if d := time.Since(start); d > 5*time.Second {
				t.Errorf("took %v", d)
			}
		})
	}
}

func TestParseLimits(t *testing.T) {
	old := maxRows
	maxRows = 3
	defer func() { maxRows = old }()
	f := mustParse(t, header+"[1(a=b)][2(a=b)][3(a=b)][4(a=b)][5(a=b)]")
	if len(f.Rows) != 3 || f.Problems == 0 {
		t.Errorf("rows=%d problems=%d", len(f.Rows), f.Problems)
	}
}

func FuzzParse(f *testing.F) {
	f.Add([]byte(header + "< <(a=c)> (80=flags)(81=x$20y)>{1:^80 {(k:^80)}[1:^80(^80^81)(q=r\\)s)]}\n@$${1{@[2(a=b)]@$$}~~}@"))
	f.Add([]byte(header + "[-1:c-(a)]{[(]}<<(a=v)>>"))
	f.Add([]byte("\x00\xff@$$}@@$${"))
	f.Fuzz(func(t *testing.T, b []byte) {
		fl, err := Parse(append([]byte(header), b...))
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range fl.Rows {
			if len(r.Cells) > maxCells {
				t.Fatalf("row with %d cells", len(r.Cells))
			}
		}
	})
}
