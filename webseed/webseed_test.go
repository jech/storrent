package webseed

import (
	"testing"
)

type crTest struct {
	s string
	o, l, fl int64
}

var good = []crTest{
	crTest{"bytes 10-20/30", 10, 11, 30},
	crTest{"bytes 10-29/30", 10, 20, 30},
	crTest{"bytes 10-20/*", 10, 11, -1},
	crTest{"bytes */30", -1, -1, 30},
}

var bad = []string{
	"octet 10-20/30",
	"bytes 10-20/30 foo",
	"bytes 10-20/",
	"bytes *-20/30",
	"bytes 10-*/30",
	"bytes 20-10/30",
	"bytes 10-20/15",
	"bytes 10-20/20",
}

func TestContentRange(t *testing.T) {
	for _, test := range good {
		o, l, fl, err := parseContentRange(test.s)
		if err != nil {
			t.Errorf("Parse %v: %v", test.s, err)
		}
		if o != test.o || l != test.l || fl != test.fl {
			t.Errorf("Parse %v: got %v, %v, %v expected %v, %v, %v",
				test.s, o, l, fl, test.o, test.l, test.fl)
		}
	}
	for _, test := range bad {
		o, l, fl, err := parseContentRange(test)
		if err == nil {
			t.Errorf("Parse %v: got %v, %v, %v expected error",
				test, o, l, fl)
		}
	}
}
