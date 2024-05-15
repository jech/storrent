// Package path implements file paths represented as lists of strings.

package path

import (
	"strings"
)

type Path []string

func (p Path) String() string {
	return strings.Join(p, "/")
}

// Parse converts a Unix-style path to a path.  It treats absolute and
// relative paths identically.
func Parse(f string) Path {
	path := strings.Split(f, "/")
	for len(path) > 0 && path[0] == "" {
		path = path[1:]
	}
	for len(path) > 0 && path[len(path)-1] == "" {
		path = path[0 : len(path)-1]
	}
	return path
}

func (p Path) Equal(q Path) bool {
	if len(p) != len(q) {
		return false
	}
	for i := range p {
		if p[i] != q[i] {
			return false
		}
	}
	return true
}

// Within returns true if path p is within directory d.
func (p Path) Within(d Path) bool {
	if len(p) <= len(d) {
		return false
	}
	for i := range d {
		if p[i] != d[i] {
			return false
		}
	}
	return true
}

// Compare implements lexicographic ordering on paths.
func (a Path) Compare(b Path) int {
	for i := range a {
		if i >= len(b) {
			return 1
		}
		if a[i] < b[i] {
			return -1
		}
		if a[i] > b[i] {
			return +1
		}
	}

	if len(a) < len(b) {
		return -1
	}
	return 0
}
