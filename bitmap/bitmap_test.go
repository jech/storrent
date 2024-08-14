package bitmap

import (
	"math/rand/v2"
	"testing"
)

func TestString(t *testing.T) {
	var b Bitmap
	for i := 0; i < 14; i++ {
		b.Set(i*2 + 1)
	}
	a := b.String()
	e := "[01010101010101010101010101010000]"
	if a != e {
		t.Errorf("Got %v, expected %v for %v", a, e, []byte(b))
	}
}

func count(a []bool) int {
	count := 0
	for _, v := range a {
		if v {
			count++
		}
	}
	return count
}

func TestBitmap(t *testing.T) {
	for i := 0; i < 100; i++ {
		a := make([]bool, 1000)
		b := New(1000)

		if !b.Empty() {
			t.Errorf("Empty failed")
		}
		if b.Count() != 0 {
			t.Errorf("Count failed")
		}
		for i := 0; i < 500; i++ {
			index := rand.IntN(1000)
			if rand.IntN(3) == 0 {
				a[index] = true
				b.Set(index)
				if b.Empty() {
					t.Errorf("Empty failed")
				}
			} else {
				a[index] = false
				b.Reset(index)
			}
			if b.Count() != count(a) {
				t.Errorf("Count failed: got %v, expected %v",
					b.Count(), count(a))
			}
		}
		for i := 0; i < 1000; i++ {
			if a[i] != b.Get(i) {
				t.Errorf("Mismatch at %v: %v != %v",
					i, a[i], b.Get(i))
			}
		}
		c := b.Copy()
		for i := 0; i < 1000; i++ {
			if b.Get(i) != c.Get(i) {
				t.Errorf("Copy mismatch at %v: %v != %v",
					i, a[i], b.Get(i))
			}
		}
	}
}

func TestLength(t *testing.T) {
	for i := 0; i < 1024; i++ {
		var b Bitmap
		b.Set(i)
		if len(b) != i / 8 + 1 {
			t.Errorf("%v: got %v", i, len(b))
		}
	}
}

func TestExtend(t *testing.T) {
	for i := 0; i < 1024; i++ {
		var b Bitmap
		b.Extend(i)
		if len(b) != i / 8 + 1 {
			t.Errorf("%v: got %v", i, len(b))
		}
	}
}

func TestLen(t *testing.T) {
	var b Bitmap
	if b.Len() != 0 {
		t.Errorf("Len failed, expected 0, got %v", b.Len())
	}
	for i := 0; i < 20; i++ {
		b.Set(i)
		if b.Len() != i+1 {
			t.Errorf("Len failed, expected %v + 1, got %v",
				i, b.Len())
		}
	}
	b.Reset(18)
	if b.Len() != 20 {
		t.Errorf("Len failed after Reset, expected %v, got %v",
			20, b.Len())
	}
}

func TestMultiple(t *testing.T) {
	var b Bitmap
	b.SetMultiple(47)
	for i := 0; i < 47; i++ {
		if !b.Get(i) {
			t.Errorf("Set Multiple failed, %v is not set", i)
		}
	}
	for i := 47; i < 58; i++ {
		if b.Get(i) {
			t.Errorf("Set Multiple failed, %v is set", i)
		}
	}
}

func TestEqualValue(t *testing.T) {
	var b1, b2 Bitmap
	if !b1.EqualValue(b2) {
		t.Errorf("EqualValue failed")
	}

	if !b1.EqualValue([]byte{0, 0, 0}) {
		t.Errorf("EqualValue failed")
	}
	if !Bitmap([]byte{0, 0, 0}).EqualValue(b1) {
		t.Errorf("EqualValue failed")
	}

	b1.Set(42)
	if b1.EqualValue(b2) {
		t.Errorf("EqualValue failed")
	}
	if b2.EqualValue(b1) {
		t.Errorf("EqualValue failed")
	}
}

func TestAll(t *testing.T) {
	var b Bitmap
	if !b.All(0) {
		t.Errorf("All failed: empty, 0")
	}
	if b.All(1) {
		t.Errorf("All failed: empty, 1")
	}
	for i := 0; i < 123; i++ {
		b.Set(i)
		if !b.All(i + 1) {
			t.Errorf("All failed: %v %v", b, i+1)
		}
		if b.All(i + 2) {
			t.Errorf("All failed: %v %v + 1", b, i+1)
		}
	}
	b.Reset(83)
	if b.All(123) {
		t.Errorf("All failed")
	}
}

func TestRange(t *testing.T) {
	var b Bitmap
	for i := 0; i < 123; i++ {
		b.Set(i * 3)
	}
	i := 0
	b.Range(func(index int) bool {
		if index != i * 3 {
			t.Errorf("Expected %v, got %v", i * 3, index)
		}
		i++
		return true
	})
}

const size = 0x1000
const mask = 0xFFF

func BenchmarkSet(b *testing.B) {
	var bm Bitmap

	var v int
	for n := 0; n < b.N; n++ {
		bm.Set(v)
		v = (v + 1*3) % mask
	}
	if bm.Count() > b.N {
		b.Errorf("Bad count")
	}
}

func BenchmarkSetReset(b *testing.B) {
	var bm Bitmap

	var v int
	for n := 0; n < b.N; n++ {
		bm.Set(v)
		v = (v + 1*3) % mask
	}
	v = 0
	for n := 0; n < b.N; n++ {
		bm.Reset(v)
		v = (v + 1*3) % mask
	}
	if bm.Count() != 0 {
		b.Errorf("Bad count")
	}
}

func BenchmarkGet(b *testing.B) {
	var bm Bitmap

	for i := 0; i < size/10; i++ {
		bm.Set(rand.IntN(size))
	}

	b.ResetTimer()

	var v, count int
	for n := 0; n < b.N; n++ {
		if bm.Get(v) {
			count++
		}
		v = (v + 1*3) % mask
	}

	if count > b.N {
		b.Errorf("Bad count")
	}
}

func BenchmarkArraySparseCount(b *testing.B) {
	a := make([]bool, size)

	for i := 0; i < size/10; i++ {
		a[rand.IntN(size)] = true
	}

	b.ResetTimer()

	for n := 0; n < b.N; n++ {
		if count(a) > size/10 {
			b.Errorf("Bad count")
		}
	}
}

func BenchmarkSparseCount(b *testing.B) {
	var bm Bitmap

	for i := 0; i < size/10; i++ {
		bm.Set(rand.IntN(size))
	}

	b.ResetTimer()

	for n := 0; n < b.N; n++ {
		if bm.Count() > size/10 {
			b.Errorf("Bad count")
		}
	}
}

func BenchmarkArrayDenseCount(b *testing.B) {
	a := make([]bool, size)

	for i := 0; i < size; i++ {
		if i%8 != 7 {
			a[i] = true
		}
	}

	b.ResetTimer()

	for n := 0; n < b.N; n++ {
		if count(a) != size-size/8 {
			b.Errorf("Bad count")
		}
	}
}

func BenchmarkDenseCount(b *testing.B) {
	var bm Bitmap

	for i := 0; i < size; i++ {
		if i%8 != 7 {
			bm.Set(i)
		}
	}

	b.ResetTimer()

	for n := 0; n < b.N; n++ {
		if bm.Count() != size-size/8 {
			b.Errorf("Bad count")
		}
	}
}

func BenchmarkArrayFullCount(b *testing.B) {
	a := make([]bool, size)

	for i := 0; i < size; i++ {
		a[i] = true
	}

	b.ResetTimer()

	for n := 0; n < b.N; n++ {
		if count(a) != size {
			b.Errorf("Bad count")
		}
	}
}

func BenchmarkFullCount(b *testing.B) {
	var bm Bitmap

	for i := 0; i < size; i++ {
		bm.Set(i)
	}

	b.ResetTimer()

	for n := 0; n < b.N; n++ {
		if bm.Count() != size {
			b.Errorf("Bad count")
		}
	}
}
