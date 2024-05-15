package path

import (
	"testing"
)

func TestRoundtrip(t *testing.T) {
	paths := []Path{
		[]string{}, []string{"a"}, []string{"a","b","c"},
	}
	for _, p := range paths {
		f := p.String()
		q := Parse(f)
		if !p.Equal(q) {
			t.Errorf("Not equal: %#v -> %#v -> %#v", p, f, q)
		}
	}

	files := []string{
		"", "a", "a/b/c",
	}
	for _, f := range files {
		p := Parse(f)
		g := p.String()
		if f != g {
			t.Errorf("Not equal: %#v -> %#v -> %#v", g, p, g)
		}
	}
}

func testEqual(t *testing.T, p, q Path, expected bool) {
	if result := p.Equal(q); result != expected {
		t.Errorf("Unexpected: %#v %#v -> %v (expected %v)",
			p, q, result, expected,
		)
	}
}

func TestEqualNil(t *testing.T) {
	testEqual(t, Path{}, nil, true)
	testEqual(t, nil, Path{}, true)
	testEqual(t, Path{"a"}, nil, false)
	testEqual(t, nil, Path{"a"}, false)
}

func testWithin(t *testing.T, f, g string, expected bool) {
	if result := Parse(f).Within(Parse(g)); result != expected {
		t.Errorf("Unexpected: %#v %#v -> %v (expected %v)",
			Parse(f), Parse(g), result, expected,
		)
	}
}

func TestWithin(t *testing.T) {
	testWithin(t, "", "", false)
	testWithin(t, "", "a", false)
	testWithin(t, "a", "a", false)
	testWithin(t, "a", "b", false)
	testWithin(t, "a", "", true)
	testWithin(t, "a", "a/b", false)
	testWithin(t, "a/b", "a", true)
	testWithin(t, "a", "b/a", false)
	testWithin(t, "b/a", "a", false)
}

func testCompare(t *testing.T, f, g string, expected int) {
	if result := Parse(f).Compare(Parse(g)); result != expected {
		t.Errorf("Unexpected: %#v %#v -> %v (expected %v)",
			Parse(f), Parse(g), result, expected,
		)
	}
}

func TestCompare(t *testing.T) {
	testCompare(t, "", "", 0)
	testCompare(t, "", "a", -1)
	testCompare(t, "a", "", 1)
	testCompare(t, "a", "b", -1)
	testCompare(t, "b", "a", 1)
	testCompare(t, "a/a", "a/b", -1)
	testCompare(t, "a/b", "a/a", 1)
	testCompare(t, "a/a", "b/a", -1)
	testCompare(t, "b/a", "a/a", 1)
}

