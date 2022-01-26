package physmem

import "syscall"

// Total returns the amount of memory on the local machine in bytes.
func Total() (int64, error) {
	var info syscall.Sysinfo_t
	err := syscall.Sysinfo(&info)
	if err != nil {
		return -1, err
	}
	return int64(info.Totalram) * int64(info.Unit), nil
}
