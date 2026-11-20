package pstcrypt

import (
	"bytes"
	"testing"
)

func TestTables(t *testing.T) {
	var seenR, seenS [256]bool
	for i := 0; i < 256; i++ {
		seenR[mpbbR[i]] = true
		seenS[mpbbS[i]] = true
		if mpbbI[mpbbR[i]] != byte(i) {
			t.Fatalf("mpbbI is not the inverse of mpbbR at %d", i)
		}
		if mpbbS[mpbbS[i]] != byte(i) {
			t.Fatalf("mpbbS is not an involution at %d", i)
		}
	}
	for i := range seenR {
		if !seenR[i] || !seenS[i] {
			t.Fatalf("table is not a permutation: %d missing", i)
		}
	}
	// Spot values of the [MS-PST] 5.1 table.
	if mpbbR[0] != 65 || mpbbR[255] != 61 || mpbbI[65] != 0 {
		t.Fatalf("unexpected table values")
	}
}

func TestRoundTrip(t *testing.T) {
	plain := []byte("Hello, permute and cyclic encodings! \x00\x01\xfe\xff")
	for _, tc := range []struct {
		name     string
		enc, dec func([]byte)
	}{
		{"permute", func(b []byte) { Permute(b, true) }, func(b []byte) { Permute(b, false) }},
		{"cyclic", func(b []byte) { Cyclic(b, 0x12345678) }, func(b []byte) { Cyclic(b, 0x12345678) }},
	} {
		b := append([]byte(nil), plain...)
		tc.enc(b)
		if bytes.Equal(b, plain) {
			t.Errorf("%s: encoding changed nothing", tc.name)
		}
		tc.dec(b)
		if !bytes.Equal(b, plain) {
			t.Errorf("%s: round trip = %q", tc.name, b)
		}
	}
}

func TestPermuteKnown(t *testing.T) {
	b := []byte{0, 1, 255}
	Permute(b, true)
	if !bytes.Equal(b, []byte{65, 54, 61}) {
		t.Fatalf("Permute encode = %v", b)
	}
}
