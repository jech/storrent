package physmem

import "golang.org/x/sys/unix"

// Total returns the amount of memory on the local machine in bytes.
func Total() (int64, error) {
	var info unix.Sysinfo_t
	err := unix.Sysinfo(&info)
	if err != nil {
		return -1, err
	}
	return int64(info.Totalram) * int64(info.Unit), nil
}
