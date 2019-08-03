package mono

import (
	"time"
	"sync/atomic"
)

var origin time.Time

func init() {
	origin = time.Now().Add(-time.Second)
}

type Time uint32

func new(tm time.Time) Time {
	d := time.Since(origin)
	if d < time.Second || d > time.Duration(^uint32(0))*time.Second {
		panic("time overflow")
	}
	return Time(d / time.Second)
}

func (t1 Time) Sub(t2 Time) uint32 {
	if t1 < t2 {
		return 0
	}
	return uint32(t1) - uint32(t2)
}

func (t1 Time) Before(t2 Time) bool {
	return t1 < t2
}

func Now() Time {
	return new(time.Now())
}

func Since(t Time) uint32 {
	return Now().Sub(t)
}

func LoadAtomic(addr *Time) Time {
	return Time(atomic.LoadUint32((*uint32)(addr)))
}

func StoreAtomic(addr *Time, val Time) {
	atomic.StoreUint32((*uint32)(addr), uint32(val))
}
