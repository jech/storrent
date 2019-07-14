package alloc

import (
	"fmt"
	"testing"
)

const totalSize = 256 * 1024 * 1024

func doalloc(size int, a [][]byte) error {
	for j := 0; j < len(a); j++ {
		p, err := Alloc(size)
		if err != nil {
			return err
		}
		a[j] = p
	}
	return nil
}

func dofree(a [][]byte) error {
	for j := 0; j < len(a); j++ {
		err := Free(a[j])
		if err != nil {
			return err
		}
		a[j] = nil
	}
	return nil
}

var sizes = []int{
	32 * 1024, 64 * 1024, 128 * 1024, 256 * 1024, 512 * 1024,
	10294 * 1024, 2048 * 1024, 4096 * 1024, 8192 * 1024, 16384 * 1024}

func TestAlloc(t *testing.T) {
	for _, size := range sizes {
		t.Run(fmt.Sprintf("%v", size), func(t *testing.T) {
			a := make([][]byte, totalSize/size)
			err := doalloc(size, a)
			if err != nil {
				t.Error(err)
			}
			err = dofree(a)
			if err != nil {
				t.Error(err)
			}
			if Bytes() != 0 {
				t.Error("Memory leak")
			}
		})
	}
}

func BenchmarkAllocFree(b *testing.B) {
	for _, size := range sizes {
		b.Run(fmt.Sprintf("%v", size), func(b *testing.B) {
			b.SetBytes(totalSize)
			a := make([][]byte, totalSize/size)
			for i := 0; i < b.N; i++ {
				err := doalloc(size, a)
				if err != nil {
					b.Error(err)
				}
				err = dofree(a)
				if err != nil {
					b.Error(err)
				}
			}
			if Bytes() != 0 {
				b.Error("Memory leak")
			}
		})
	}
}

func BenchmarkAllocParallel(b *testing.B) {
	for _, size := range sizes {
		b.Run(fmt.Sprintf("%v", size), func(b *testing.B) {
			b.SetBytes(totalSize)
			b.RunParallel(func (pb *testing.PB) {
				a := make([][]byte, totalSize/size)
				for pb.Next() {
					err := doalloc(size, a)
					if err != nil {
						b.Error(err)
					}
					err = dofree(a)
					if err != nil {
						b.Error(err)
					}
				}
			})
			if Bytes() != 0 {
				b.Error("Memory leak")
			}
		})
	}
}
