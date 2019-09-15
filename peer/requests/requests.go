package requests

import (
	"bytes"
	"fmt"
	"time"

	"storrent/bitmap"
)

// Request represents an outgoing request.
type Request struct {
	index uint32
	qtime time.Time // queue time
	rtime time.Time // request time, zero if not requested yet
	ctime time.Time // cancel time, zero if not cancelled
}

func (r *Request) Cancel() {
	r.ctime = time.Now()
}

func (r Request) Cancelled() bool {
	return !r.ctime.Equal(time.Time{})
}

func (r Request) String() string {
	c := ""
	if r.Cancelled() {
		c = ", cancelled"
	}
	return fmt.Sprintf("[%v at %v,%v%v]", r.index, r.qtime, r.rtime, c)
}

// Requests represents a request queue with fast membership test.
type Requests struct {
	queue     []Request
	requested []Request
	bitmap    bitmap.Bitmap
}

func (rs *Requests) String() string {
	b := new(bytes.Buffer)

	fmt.Fprintf(b, "[[")
	for _, v := range rs.queue {
		fmt.Fprintf(b, "%v,", v.index)
	}
	fmt.Fprintf(b, "],[")
	for _, v := range rs.requested {
		fmt.Fprintf(b, "%v,", v.index)
	}
	fmt.Fprintf(b, "]]")
	return b.String()
}

func (rs *Requests) Queue() int {
	return len(rs.queue)
}

func (rs *Requests) Requested() int {
	return len(rs.requested)
}

func (rs *Requests) Cancel(index uint32) (bool, bool) {
	if !rs.bitmap.Get(int(index)) {
		return false, false
	}
	for i, r := range rs.requested {
		if index == r.index {
			cancelled := r.Cancelled()
			if !cancelled {
				rs.requested[i].Cancel()
			}
			return true, !cancelled
		}
	}
	return false, false
}

func (rs *Requests) del(index uint32, reqonly bool) (bool, bool, time.Time) {
	if !rs.bitmap.Get(int(index)) {
		return false, false, time.Time{}
	}
	for i, r := range rs.requested {
		if index == r.index {
			l := len(rs.requested)
			rr := rs.requested[i]
			if l == 1 {
				rs.requested = nil
			} else {
				rs.requested[i] = rs.requested[l-1]
				rs.requested = rs.requested[:l-1]
			}
			if len(rs.requested) == 0 {
				rs.requested = nil
			}
			rs.bitmap.Reset(int(index))
			return false, true, rr.rtime
		}
	}
	if !reqonly {
		for i, q := range rs.queue {
			if index == q.index {
				l := len(rs.queue)
				if l == 1 {
					rs.queue = nil
				} else {
					rs.queue[i] = rs.queue[l-1]
					rs.queue = rs.queue[:l-1]
				}
				rs.bitmap.Reset(int(index))
				return true, false, time.Time{}
			}
		}
	}
	if !reqonly {
		panic("Requests is broken!")
	}
	return false, false, time.Time{}
}

func (rs *Requests) Del(index uint32) (bool, bool, time.Time) {
	return rs.del(index, false)
}

func (rs *Requests) DelRequested(index uint32) bool {
	_, r, _ := rs.del(index, true)
	return r
}

func (rs *Requests) enqueue(r Request, atend bool) bool {
	if rs.bitmap.Get(int(r.index)) {
		return false
	}
	rs.bitmap.Set(int(r.index))
	r.ctime = time.Time{}
	if atend {
		rs.queue = append(rs.queue, r)
	} else {
		rs.queue = append([]Request{r}, rs.queue...)
	}
	return true
}

func (rs *Requests) Enqueue(index uint32) bool {
	return rs.enqueue(Request{index: index, qtime: time.Now()}, true)
}

func (rs *Requests) Dequeue() (request Request, index uint32) {
	request = rs.queue[0]
	index = request.index
	rs.bitmap.Reset(int(request.index))
	rs.queue = rs.queue[1:]
	if len(rs.queue) == 0 {
		rs.queue = nil
	}
	return
}

func (rs *Requests) EnqueueRequest(r Request) {
	if rs.bitmap.Get(int(r.index)) {
		panic("Incorrect use of Requests.EnqueueRequest")
	}
	r.ctime = time.Time{}
	rs.bitmap.Set(int(r.index))
	r.rtime = time.Now()
	rs.requested = append(rs.requested, r)
}

func (rs *Requests) Clear(both bool, f func(uint32)) {
	oldr := rs.requested
	oldq := rs.queue
	rs.queue = nil
	rs.requested = nil
	rs.bitmap = nil
	if !both {
		rs.requested = oldr
		for _, r := range oldr {
			rs.bitmap.Set(int(r.index))
		}
	} else {
		for _, r := range oldr {
			f(r.index)
		}
	}
	for _, q := range oldq {
		f(q.index)
	}
}

func (rs *Requests) Expire(t0, t1 time.Time,
	drop func(index uint32),
	cancel func(index uint32)) bool {

	dropped := false

	i := 0
	for i < len(rs.requested) {
		r := rs.requested[i]
		if r.Cancelled() && r.ctime.Before(t1) {
			found := rs.DelRequested(r.index)
			if !found {
				panic("Couldn't delete request")
			}
			drop(r.index)
			dropped = true
			// don't increment i
			continue
		} else if !r.Cancelled() && r.rtime.Before(t0) {
			rs.requested[i].Cancel()
			cancel(r.index)
		}
		i++
	}
	return dropped
}
