package physmem

import "golang.org/x/sys/unix"

func Total() (int64, error) {
	var info unix.Sysinfo_t
	err := unix.Sysinfo(&info)
	if err != nil {
		return -1, err
	}
	return int64(info.Totalram) * int64(info.Unit), nil
}
