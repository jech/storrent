package pex

import (
	"bytes"
	"testing"
)

func TestParseV4(t *testing.T) {
	a := []byte("123456abcdef")
	f := []byte{1, 0x12}
	peers := ParseCompact(a, f, false)
	if len(peers) != 2 {
		t.Errorf("bad length %v", len(peers))
	}
	a4, f4, a6, f6 := FormatCompact(peers)
	if !bytes.Equal(a4, a) {
		t.Errorf("bad value")
	}
	if !bytes.Equal(f4, f) {
		t.Errorf("bad flags")
	}
	if len(a6) != 0 || len(f6) != 0 {
		t.Errorf("creation ex nihilo")
	}
}

func TestParseV6(t *testing.T) {
	a := []byte("123456789012345678abcdefghijklmnopqr")
	f := []byte{1, 2}
	peers := ParseCompact(a, f, true)
	if len(peers) != 2 {
		t.Errorf("bad length %v", len(peers))
	}
	a4, f4, a6, f6 := FormatCompact(peers)
	if !bytes.Equal(a6, a) {
		t.Errorf("bad value")
	}
	if !bytes.Equal(f6, f) {
		t.Errorf("bad flags")
	}
	if len(a4) != 0 || len(f4) != 0 {
		t.Errorf("creation ex nihilo")
	}
}
