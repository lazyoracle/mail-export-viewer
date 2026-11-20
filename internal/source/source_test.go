package source

import (
	"reflect"
	"testing"
	"time"

	"github.com/lazyoracle/mail-export-viewer/internal/mail"
)

func TestFlagNames(t *testing.T) {
	all := Unread | Starred | Answered | Forwarded | Draft | Deleted | Junk | HasAttachment | Important
	want := []string{"unread", "starred", "answered", "forwarded", "draft", "deleted", "junk", "attachment", "important"}
	if got := all.Names(); !reflect.DeepEqual(got, want) {
		t.Errorf("Names = %q", got)
	}
	if got := Flags(0).Names(); got != nil {
		t.Errorf("zero Names = %q", got)
	}
	if got := (Starred | Junk).String(); got != "starred,junk" {
		t.Errorf("String = %q", got)
	}
	for i, n := range want {
		f, ok := ParseFlag(n)
		if !ok || f != Flags(1)<<i {
			t.Errorf("ParseFlag(%q) = %v %v", n, f, ok)
		}
	}
	for alias, f := range map[string]Flags{"FLAGGED": Starred, "replied": Answered, "spam": Junk, "has-attachment": HasAttachment} {
		if got, ok := ParseFlag(alias); !ok || got != f {
			t.Errorf("ParseFlag(%q) = %v %v", alias, got, ok)
		}
	}
	if _, ok := ParseFlag("bogus"); ok {
		t.Error("ParseFlag(bogus) ok")
	}
	if !(Starred | Junk).Has(Junk) || (Starred).Has(Junk) {
		t.Error("Has")
	}
}

func TestLocatorText(t *testing.T) {
	locs := []Locator{
		{},
		{Offset: 1234, Length: 5678},
		{ID: 0x200024},
		{Name: "Local/Messages/Inbox/message_00001.xml"},
		{Name: "a:b%c d/é\xff", Offset: 1, Length: 2, ID: 3},
	}
	for _, l := range locs {
		b, err := l.MarshalText()
		if err != nil {
			t.Fatal(err)
		}
		s := string(b)
		for _, c := range s {
			if c < 0x21 || c > 0x7e {
				t.Errorf("%+v: text %q not printable ASCII", l, s)
				break
			}
		}
		if l.String() != s {
			t.Errorf("String %q != text %q", l.String(), s)
		}
		var back Locator
		if err := back.UnmarshalText(b); err != nil || back != l {
			t.Errorf("round trip %+v -> %q -> %+v, %v", l, s, back, err)
		}
	}
	for _, bad := range []string{"", "x", "1:2:3", "-1:0:0:", "1:-2:0:", "a:0:0:", "0:0:z:", "0:0:0:%zz"} {
		var l Locator
		if err := l.UnmarshalText([]byte(bad)); err == nil {
			t.Errorf("UnmarshalText(%q) = %+v, want error", bad, l)
		}
	}
}

func TestEntryResolve(t *testing.T) {
	env := time.Date(2020, 5, 6, 7, 8, 9, 0, time.UTC)
	e := Entry{
		Raw:  []byte("Subject: hi\nContent-Type: multipart/mixed; boundary=B\n\n--B\n\nbody\n--B\nContent-Type: application/pdf; name=a.pdf\n\nx\n--B--\n"),
		Date: env,
	}
	e.Resolve()
	if e.Msg == nil || e.Msg.Subject != "hi" || !e.Msg.Date.Equal(env) || e.Msg.Text != "body" {
		t.Fatalf("Msg = %+v", e.Msg)
	}
	if e.Raw != nil {
		t.Error("Raw not released")
	}
	if !e.Flags.Has(HasAttachment) {
		t.Error("HasAttachment flag not set")
	}
	// Idempotent; a preset Msg is kept.
	msg := e.Msg
	e.Resolve()
	if e.Msg != msg {
		t.Error("Resolve replaced Msg")
	}
	preset := Entry{Msg: &mail.Summary{Envelope: mail.Envelope{Subject: "olm"}}, Flags: Unread}
	preset.Resolve()
	if preset.Msg.Subject != "olm" || preset.Flags != Unread {
		t.Errorf("preset = %+v", preset)
	}
	// Neither set: an empty summary carrying the fallback date.
	empty := Entry{Date: env}
	empty.Resolve()
	if empty.Msg == nil || !empty.Msg.Date.Equal(env) {
		t.Errorf("empty = %+v", empty.Msg)
	}
}
