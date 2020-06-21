//+build !linux,!windows windows,!cgo

package physmem

import "errors"

func Total() (int64, error) {
	return -1, errors.New("cannot compute physical memory on this platform")
}
