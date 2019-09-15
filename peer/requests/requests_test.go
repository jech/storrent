package requests

import (
	"testing"
	"time"

	"storrent/bitmap"
)

func in(x uint32, a []uint32) bool {
	for _, v := range a {
		if v == x {
			return true
		}
	}
	return false
}

func rsequal(rs Requests, a []uint32) bool {
	var b bitmap.Bitmap

	for _, v := range a {
		b.Set(int(v))
	}

	for _, r := range rs.queue {
		if !b.Get(int(r.index)) {
			return false
		}
		b.Reset(int(r.index))
	}

	for _, r := range rs.requested {
		v := r.index
		if !b.Get(int(v)) {
			return false
		}
		b.Reset(int(v))
	}
	return b.Empty()
}

func check(rs *Requests, t *testing.T) {
	var b bitmap.Bitmap
	for _, q := range rs.queue {
		if b.Get(int(q.index)) {
			t.Errorf("Duplicate enqueued")
		}
		b.Set(int(q.index))
	}
	for _, r := range rs.requested {
		if b.Get(int(r.index)) {
			t.Errorf("Duplicate request")
		}
		b.Set(int(r.index))
	}
	if !b.EqualValue(rs.bitmap) {
		t.Errorf("Incorrect bitmap")
	}
}

func TestRequest(t *testing.T) {
	a := []uint32{3, 8, 17, 99, 13, 2, 9, 18, 44, 17}
	b := []uint32{3, 8, 99, 13, 2, 18, 44}
	var rs Requests
	check(&rs, t)

	for _, v := range a {
		rs.Enqueue(v)
		check(&rs, t)
	}

	if !rsequal(rs, a) {
		t.Errorf("Enqueue failed, expected %v, got %v", a, rs)
	}

	rs.DelRequested(17)
	check(&rs, t)
	rs.DelRequested(9)
	check(&rs, t)
	if !rsequal(rs, a) {
		t.Errorf("DelRequest failed, expected %v, got %v", a, rs)
	}

	for len(rs.queue) > 0 {
		r, _ := rs.Dequeue()
		check(&rs, t)
		rs.EnqueueRequest(r)
		check(&rs, t)
		if !rsequal(rs, a) {
			t.Errorf("Request failed, expected %v, got %v", a, rs)
		}
	}

	rs.Del(17)
	check(&rs, t)
	rs.DelRequested(9)
	check(&rs, t)
	if !rsequal(rs, b) {
		t.Errorf("Del failed, expected %v, got %v", b, rs)
	}

	if rs.requested[0].Cancelled() {
		t.Errorf("Cancelled: expected false")
	}
	index := rs.requested[0].index
	rs.Cancel(index)
	check(&rs, t)
	if !rs.requested[0].Cancelled() {
		t.Errorf("Cancelled: expected true")
	}

	var c []uint32
	for len(rs.requested) > 0 {
		index := rs.requested[0].index
		rs.Del(index)
		c = append(c, index)
		check(&rs, t)
	}
	for _, v := range c {
		rs.Enqueue(v)
		check(&rs, t)
	}
	if !rsequal(rs, b) {
		t.Errorf("DequeueRequest failed, expected %v, got %v", b, rs)
	}

	rs.Del(17)
	check(&rs, t)
	rs.Del(9)
	check(&rs, t)
	if !rsequal(rs, b) {
		t.Errorf("Del failed, expected %v, got %v", b, rs)
	}

	rs = Requests{}
	check(&rs, t)
	for _, v := range a {
		rs.Enqueue(v)
		check(&rs, t)
	}
	for len(rs.queue) != 0 {
		q, _ := rs.Dequeue()
		rs.EnqueueRequest(q)
		check(&rs, t)
		if !rsequal(rs, a) {
			t.Errorf("Request failed, expected %v, got %v", a, rs)
		}
	}
}

func TestExpire(t *testing.T) {
	now := time.Now()
	ta := now.Add(-180 * time.Second)
	tb := now.Add(-30 * time.Second)
	tc := now.Add(-15 * time.Second)
	td := now.Add(-5 * time.Second)
	rs := Requests{
		queue: nil,
		requested: []Request{
			Request{index: 0, qtime: ta, rtime: ta},
			Request{index: 1, qtime: ta, rtime: tc},
			Request{index: 2, qtime: ta, rtime: ta, ctime: now},
			Request{index: 3, qtime: ta, rtime: tc, ctime: now},
			Request{index: 4, qtime: ta, rtime: tc, ctime: tc},
		},
	}
	for _, r := range rs.requested {
		rs.bitmap.Set(int(r.index))
	}

	dropped := 0
	canceled := 0
	rs.Expire(tb, td,
		func(index uint32) { dropped++ },
		func(index uint32) { canceled++ },
	)
	if dropped != 1 {
		t.Errorf("Dropped is %v", dropped)
	}
	if canceled != 1 {
		t.Errorf("Canceled is %v", canceled)
	}

	if len(rs.requested) != 4 {
		t.Errorf("len(requested) is %v", len(rs.requested))
	}

	for i, r := range rs.requested {
		if r.index != uint32(i) {
			t.Errorf("requested[%v].index is %v",
				i, r.index)
		}
		if r.Cancelled() != (i != 1) {
			t.Errorf("Cancelled %v for %v",
				r.Cancelled(), i)
		}
	}
}
