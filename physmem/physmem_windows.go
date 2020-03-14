package physmem

import (
	"errors"
)

/*
#include "windows.h"

DWORDLONG total() {
    MEMORYSTATUSEX meminfo;
    BOOL rc;
    memset(&meminfo, 0, sizeof(meminfo));
    meminfo.dwLength = sizeof(meminfo);
    rc = GlobalMemoryStatusEx(&meminfo);
    if(!rc)
        return 0;
    return meminfo.ullTotalPhys;
}
*/
import "C"

func Total() (int64, error) {
        v := C.total()
	if v <= 0 {
		return 0, errors.New("GlobalMemoryStatusEx failed")
	}
	return int64(v), nil
}
