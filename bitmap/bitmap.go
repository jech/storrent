// Package bitmap implements bitmaps that maintain their length modulo 8,
// and are therefore compatible with BitTorrent bitfields.
package bitmap

import (
	"math/bits"
	"strings"
)

type Bitmap []uint8

func New(length int) Bitmap {
	return Bitmap(make([]uint8, (length+7)/8))
}

// Get returns true if the ith bit is set.
func (b Bitmap) Get(i int) bool {
	if b == nil || i>>3 >= len(b) {
		return false
	}
	return (b[i>>3] & (1 << (7 - uint8(i&7)))) != 0
}

func (b Bitmap) String() string {
	var buf strings.Builder
	buf.Grow(len(b)*8 + 2)
	buf.WriteByte('[')
	for i := 0; i < len(b)*8; i++ {
		if b.Get(i) {
			buf.WriteByte('1')
		} else {
			buf.WriteByte('0')
		}
	}
	buf.WriteByte(']')
	return buf.String()
}

// Extend sets the length of the bitmap to at least the smallest multiple
// of 8 that is strictly larger than i.
func (b *Bitmap) Extend(i int) {
	if i>>3 >= len(*b) {
		*b = append(*b, make([]uint8, (i>>3)+1-len(*b))...)
	}
}

// Set sets the ith bit of the bitmap, extending it if necessery.
func (b *Bitmap) Set(i int) {
	b.Extend(i)
	(*b)[i>>3] |= (1 << (7 - uint8(i&7)))
}

// Reset resets the ith bit of the bitmap.
func (b *Bitmap) Reset(i int) {
	if i>>3 >= len(*b) {
		return
	}
	(*b)[i>>3] &= ^(1 << (7 - uint8(i&7)))
}

func (b Bitmap) Copy() Bitmap {
	if b == nil {
		return nil
	}
	c := make([]uint8, len(b))
	copy(c, b)
	return c
}

// Sets all bits from 0 up to n - 1.
func (b *Bitmap) SetMultiple(n int) {
	b.Extend(n)
	for i := 0; i < (n >> 3); i++ {
		(*b)[i] = 0xFF
	}
	for i := (n & ^7); i < n; i++ {
		b.Set(i)
	}
}

// Empty returns true if no bits are set.
func (b Bitmap) Empty() bool {
	if b == nil {
		return true
	}
	for _, v := range b {
		if v != 0 {
			return false
		}
	}
	return true
}

// All returns true if all bits from 0 up to n - 1 are set.
func (b Bitmap) All(n int) bool {
	if n == 0 {
		return true
	}
	if len(b) < n>>3 {
		return false
	}
	for i := 0; i < n>>3; i++ {
		if b[i] != 0xFF {
			return false
		}
	}
	if n&7 == 0 {
		return true
	}
	if len(b) < n>>3+1 || b[n>>3] != (0xFF<<(8-uint8(n&7))) {
		return false
	}
	return true
}

// Count returns the number of bits set.
func (b Bitmap) Count() int {
	if b == nil {
		return 0
	}
	count := 0
	for _, v := range b {
		count += bits.OnesCount8(v)
	}
	return count
}

// Len returns the index of the highest bit set, plus one.
func (b Bitmap) Len() int {
	if len(b) == 0 {
		return 0
	}

	for i := len(b) - 1; i >= 0; i-- {
		if b[i] != 0 {
			return i*8 + 8 - bits.TrailingZeros8(b[i])
		}
	}
	return 0
}

// EqualValue returns true if two bitmaps have the same bits set
// (ignoring any trailing zeroes).
func (b1 Bitmap) EqualValue(b2 Bitmap) bool {
	n := len(b1)
	if n > len(b2) {
		n = len(b2)
	}

	for i := 0; i < n; i++ {
		if b1[i] != b2[i] {
			return false
		}
	}

	for i := n; i < len(b1); i++ {
		if b1[i] != 0 {
			return false
		}
	}

	for i := n; i < len(b2); i++ {
		if b2[i] != 0 {
			return false
		}
	}

	return true
}

func (b Bitmap) Range(f func(index int) bool) {
	for i, v := range b {
		if v == 0 {
			continue
		}
		for j := uint8(0); j < 8; j++ {
			if (v & (1 << (7 - j))) != 0 {
				c := f((i << 3) + int(j))
				if !c {
					return
				}
			}
		}
	}
}
