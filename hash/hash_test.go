package hash

import (
	"crypto/rand"
	"testing"
)

func TestRoundtrip(t *testing.T) {
	for i := 0; i < 100; i++ {
		var h Hash = make([]byte, 20)
		rand.Read(h)
		h2 := Parse(h.String())
		if !h.Equal(h2) {
			t.Errorf("Mismatch (%v != %v)", h, h2)
		}
	}
}

func TestParse(t *testing.T) {
	h1 := Parse("WRN7ZT6NKMA6SSXYKAFRUGDDIFJUNKI2")
	h2 := Parse("b45bfccfcd5301e94af8500b1a1863415346a91a")
	if !h1.Equal(h2) {
		t.Errorf("Mismatch (%v != %v)", h1.String(), h2.String())
	}
}

func TestParseFail(t *testing.T) {
	if Parse("toto") != nil {
		t.Errorf("Parse successful 1")
	}
	if Parse("zzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzz") != nil {
		t.Errorf("Parse successful 2")
	}
}
