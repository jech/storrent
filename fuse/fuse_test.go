// +build unix linux

package fuse

import (
	"testing"

	"bazil.org/fuse/fs"
)

func TestHashable(t *testing.T) {
	table := make(map[fs.Node]int)

	table[root(0)] = 42
	table[directory{}] = 57
	table[file{}] = 12
}

