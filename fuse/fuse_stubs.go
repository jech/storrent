// +build !unix,!linux

package fuse

import (
	"errors"
)

var ErrNotImplemented = errors.New("not implemented")

func Serve(mountpoint string) error {
	return ErrNotImplemented
}

func Close(mountpoint string) error {
	return ErrNotImplemented
}
