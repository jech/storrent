// +build !unix,!linux

package alloc

import (
	"log"
	"sync/atomic"
)

func init() {
	log.Printf("Using generic memory allocator")
}

var allocated int64

func Alloc(size int) ([]byte, error) {
	atomic.AddInt64(&allocated, int64(size))
	return make([]byte, size), nil
}

func Free(p []byte) error {
	atomic.AddInt64(&allocated, -int64(len(p)))
	return nil
}

func Bytes() int64 {
	return atomic.LoadInt64(&allocated)
}
