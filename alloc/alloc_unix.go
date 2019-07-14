// +build unix linux

package alloc

import (
	"sync/atomic"

	"golang.org/x/sys/unix"
)

const cutoff = 128 * 1024

var allocated int64

func Alloc(size int) ([]byte, error) {
	if size < cutoff {
		atomic.AddInt64(&allocated, int64(size))
		return make([]byte, size), nil
	}
	p, err := unix.Mmap(-1, 0, size,
		unix.PROT_READ|unix.PROT_WRITE,
		unix.MAP_PRIVATE|unix.MAP_ANONYMOUS)
	if err != nil {
		return nil, err
	}
	atomic.AddInt64(&allocated, int64(cap(p)))
	return p[:size], err
}

func Free(p []byte) error {
	if len(p) < cutoff {
		atomic.AddInt64(&allocated, -int64(cap(p)))
		return nil
	}
	err := unix.Munmap(p)
	atomic.AddInt64(&allocated, -int64(cap(p)))
	return err
}

func Bytes() int64 {
	return atomic.LoadInt64(&allocated)
}
