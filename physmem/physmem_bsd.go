//go:build darwin || dragonfly || freebsd || netbsd || openbsd
// +build darwin dragonfly freebsd netbsd openbsd

package physmem

import "golang.org/x/sys/unix"

func Total() (int64, error) {
	v, err := unix.SysctlUint64("hw.memsize")
	return int64(v), err
}
