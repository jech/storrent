package physmem

import (
	"syscall"
	"unsafe"
)

var kernel32 = syscall.NewLazyDLL("kernel32.dll")
var globalMemoryStatusEx = kernel32.NewProc("GlobalMemoryStatusEx")

type memoryStatusEx struct {
	dwLength                uint32
	dwMemoryLoad            uint32
	ullTotalPhys            uint64
	ullAvailPhys            uint64
	ullTotalPageFile        uint64
	ullAvailPageFile        uint64
	ullTotalVirtual         uint64
	ullAvailVirtual         uint64
	ullAvailExtendedVirtual uint64
}

// Total returns the amount of memory on the local machine in bytes.
func Total() (int64, error) {
	var info memoryStatusEx
	info.dwLength = uint32(unsafe.Sizeof(info))
	rc, _, err := globalMemoryStatusEx.Call(uintptr(unsafe.Pointer(&info)))
	if rc == 0 {
		return 0, err
	}
	return int64(info.ullTotalPhys), nil
}
