//go:build !linux && !windows && !darwin && !dragonfly && !freebsd && !netbsd && !openbsd
// +build !linux,!windows,!darwin,!dragonfly,!freebsd,!netbsd,!openbsd

package physmem

import "errors"

// Total is not implemented on this platform.  On other platforms, it
// returns the amount of memory on the local machine in bytes.
func Total() (int64, error) {
	return -1, errors.New("cannot compute physical memory on this platform")
}
