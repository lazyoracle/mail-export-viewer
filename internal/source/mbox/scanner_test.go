package mbox

import (
	"errors"
	"io"
	"strings"
	"testing"
	"testing/iotest"
)

const sep = "From sender@example.com Mon Jan  1 00:00:00 2024\n"

func scanAll(t *testing.T, data string, prefixCap int) []message {
	t.Helper()
	var out []message
	err := scanMessages(strings.NewReader(data), prefixCap, func(m *message) error {
		out = append(out, *m)
		return nil
	})
	if err != nil {
		t.Fatalf("scanMessages: %v", err)
	}
	return out
}

func TestTwoMessageOffsets(t *testing.T) {
	first := sep + "Subject: a\n\nhello\n\n"
	data := first + "From b@example.org Tue Jan  2 00:00:00 2024\nSubject: b\n\nbye\n"
	es := scanAll(t, data, 1<<20)
	if len(es) != 2 {
		t.Fatalf("want 2 messages, got %d", len(es))
	}
	if es[0].Offset != 0 || es[0].Length != int64(len(first)) {
		t.Errorf("entry0 = %d/%d, want 0/%d", es[0].Offset, es[0].Length, len(first))
	}
	if es[1].Offset != int64(len(first)) || es[1].Offset+es[1].Length != int64(len(data)) {
		t.Errorf("entry1 = %d/%d", es[1].Offset, es[1].Length)
	}
	if string(es[0].Raw) != "Subject: a\n\nhello\n" || string(es[1].Raw) != "Subject: b\n\nbye\n" {
		t.Errorf("raw = %q, %q", es[0].Raw, es[1].Raw)
	}
	if es[0].Date.Year() != 2024 || es[0].Date.Day() != 1 || es[1].Date.Day() != 2 {
		t.Errorf("dates = %v, %v", es[0].Date, es[1].Date)
	}
}

func TestUnquoteMboxrd(t *testing.T) {
	es := scanAll(t, sep+"Subject: a\n\nFrom x\n>From y\n>>From z\nFrom\n>Frome\n", 1<<20)
	want := "Subject: a\n\nFrom x\nFrom y\n>From z\nFrom\n>Frome\n"
	if len(es) != 1 || string(es[0].Raw) != want {
		t.Fatalf("got %q, want %q", es[0].Raw, want)
	}
}

func TestCRLF(t *testing.T) {
	data := "From a@example.com Mon Jan  1 00:00:00 2024\r\nSubject: a\r\n\r\n>From q\r\nhi\r\n\r\n" +
		"From d@example.org Tue Jan  2 00:00:00 2024\r\nSubject: b\r\n\r\nyo\r\n\r\n"
	es := scanAll(t, data, 1<<20)
	if len(es) != 2 {
		t.Fatalf("want 2, got %d", len(es))
	}
	if string(es[0].Raw) != "Subject: a\r\n\r\nFrom q\r\nhi\r\n" || string(es[1].Raw) != "Subject: b\r\n\r\nyo\r\n" {
		t.Errorf("raw = %q, %q", es[0].Raw, es[1].Raw)
	}
	if es[0].Offset+es[0].Length != es[1].Offset || es[1].Offset+es[1].Length != int64(len(data)) {
		t.Errorf("offsets not contiguous: %+v", es)
	}
}

func TestBOMAndLeadingBlankLines(t *testing.T) {
	es := scanAll(t, "\xef\xbb\xbf"+sep+"Subject: a\n\nx\n", 1<<20)
	if len(es) != 1 || es[0].Offset != 3 || es[0].Length != int64(len(sep+"Subject: a\n\nx\n")) {
		t.Fatalf("BOM: %+v", es)
	}
	es = scanAll(t, "\n\r\n\n"+sep+"Subject: a\n\nx\n", 1<<20)
	if len(es) != 1 || es[0].Offset != 4 {
		t.Fatalf("leading blanks: %+v", es)
	}
}

func TestNoTrailingNewline(t *testing.T) {
	data := sep + "Subject: a\n\nlast"
	es := scanAll(t, data, 1<<20)
	if len(es) != 1 || string(es[0].Raw) != "Subject: a\n\nlast" || es[0].Length != int64(len(data)) {
		t.Fatalf("got %+v", es)
	}
}

func TestSeparatorHeuristic(t *testing.T) {
	for _, tc := range []struct {
		name, data string
		want       int
	}{
		{"From line in body", sep + "Subject: a\n\nbody\nFrom the desk of someone\nmore\n\nFrom b@example.org Tue Feb  3 10:11:12 2024\nSubject: b\n\nz\n", 2},
		{"strict separator without blank line", sep + "Subject: a\n\nbody\nFrom b@example.org Tue Feb  3 10:11:12 2024\nmore\n", 2},
		{"loose without blank line is body", sep + "Subject: a\n\nbody\nFrom b@example.org Feb  3 10:11 2024\nmore\n", 1},
		{"loose after blank line", sep + "Subject: a\n\nbody\n\nFrom b@example.org Feb  3 10:11 2024\nSubject: b\n\nmore\n", 2},
		{"not date-shaped after blank", sep + "Subject: a\n\nbody\n\nFrom the desk of someone\n", 1},
		{"timezone forms", sep + "S: a\n\nx\n\nFrom b@example.org Tue Feb  3 10:11:12 +0100 2024\nS: b\n\ny\n\nFrom c@example.org Tue Feb  3 10:11:12 2024 +0100\nS: c\n\nz\n", 3},
		{"overlong line", sep + "S: a\n\n\nFrom " + strings.Repeat("x", 2000) + " Mon Jan  1 00:00:00 2024\n", 1},
		{"no separator", "Subject: a\n\nbody\n", 0},
		{"empty", "", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if es := scanAll(t, tc.data, 1<<20); len(es) != tc.want {
				t.Errorf("got %d messages, want %d", len(es), tc.want)
			}
		})
	}
}

func TestContentLengthIgnored(t *testing.T) {
	// mboxcl: Content-Length is wrong here and must not matter.
	data := sep + "Content-Length: 3\n\nabcdef\nmore\n\nFrom b@example.org Tue Jan  2 00:00:00 2024\nS: b\n\nz\n"
	es := scanAll(t, data, 1<<20)
	if len(es) != 2 || string(es[0].Raw) != "Content-Length: 3\n\nabcdef\nmore\n" {
		t.Fatalf("got %+v", es)
	}
}

func TestPrefixCap(t *testing.T) {
	body := strings.Repeat("0123456789\n", 100)
	data := sep + "Subject: big\n\n" + body + "\n" + "From b@example.org Tue Jan  2 00:00:00 2024\nSubject: small\n\nx\n"
	es := scanAll(t, data, 64)
	if len(es) != 2 {
		t.Fatalf("got %d messages", len(es))
	}
	if !es[0].Truncated || len(es[0].Raw) != 64 || !strings.HasPrefix(string(es[0].Raw), "Subject: big\n\n0123") {
		t.Errorf("big: truncated %v, %d bytes", es[0].Truncated, len(es[0].Raw))
	}
	if es[0].Length != int64(len(sep+"Subject: big\n\n"+body+"\n")) {
		t.Errorf("big length %d not exact", es[0].Length)
	}
	if es[1].Truncated || string(es[1].Raw) != "Subject: small\n\nx\n" {
		t.Errorf("small: %+v", es[1])
	}
}

func TestVeryLongLine(t *testing.T) {
	long := strings.Repeat("a", 200_000)
	data := sep + "Subject: a\n\n" + long + "\n\n" + "From b@example.org Tue Jan  2 00:00:00 2024\nSubject: b\n\nx\n"
	es := scanAll(t, data, 1<<20)
	if len(es) != 2 || !strings.HasSuffix(string(es[0].Raw), long+"\n") {
		t.Fatalf("got %d messages", len(es))
	}
}

func TestReadErrorKeepsCompleted(t *testing.T) {
	data := sep + "S: a\n\nx\n\nFrom b@example.org Tue Jan  2 00:00:00 2024\nS: b\n\ny\n"
	boom := errors.New("boom")
	r := io.MultiReader(strings.NewReader(data), iotest.ErrReader(boom))
	var got []message
	err := scanMessages(r, 1<<20, func(m *message) error { got = append(got, *m); return nil })
	if !errors.Is(err, boom) || len(got) != 1 {
		t.Fatalf("err %v, %d messages (want boom, 1)", err, len(got))
	}
}

func TestCallbackErrorStops(t *testing.T) {
	stop := errors.New("stop")
	n := 0
	err := scanMessages(strings.NewReader(sep+"S: a\n\nx\n\n"+sep+"S: b\n\ny\n"), 1<<20, func(*message) error { n++; return stop })
	if !errors.Is(err, stop) || n != 1 {
		t.Fatalf("err %v, n %d", err, n)
	}
}

func TestUnquoteLines(t *testing.T) {
	for in, want := range map[string]string{
		"": "", "a\n": "a\n", ">From x\n": "From x\n", ">>From x\n": ">From x\n", ">From": ">From", ">From ": "From ",
		"x\n>From a\n>b\n>From b\n": "x\nFrom a\n>b\nFrom b\n", "From x\n": "From x\n",
	} {
		if got := string(unquote([]byte(in))); got != want {
			t.Errorf("unquote(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestParseSeparatorDate(t *testing.T) {
	for line, ok := range map[string]bool{
		"From a Mon Jan  2 15:04:05 2006":       true,
		"From a Mon Jan 2 15:04 2006":           true,
		"From a Mon Jan  2 15:04:05 -0700 2006": true,
		"From a Jan 2 15:04:05 MST 2006":        true,
		"From a Mon, Jan 2 15:04:05 2006":       true,
		"From a Mon Jan  2 15:04:05 2006 +0100": true,
		"From MAILER-DAEMON":                    false,
		"From a nonsense":                       false,
	} {
		_, d := parseSeparator([]byte(line + "\n"))
		if (d.Year() == 2006) != ok {
			t.Errorf("parseSeparator(%q) date %v", line, d)
		}
	}
}
