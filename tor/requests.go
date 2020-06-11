package tor

import (
	"math"
	"time"
)

// IdlePriority is returned for pieces that have been requested for
// reasons other than a client requiring them.
const IdlePriority = int8(math.MinInt8)

type RequestedPiece struct {
	prio []int8
	done chan struct{}
}

// Requested is a set of pieces that are requested for a torrent.
type Requested struct {
	pieces map[uint32]*RequestedPiece
	time   time.Time
}

// Add adds a piece to a set of requested pieces.  It returns a channel
// that will be closed when the piece is complete, as well as a boolean
// that indicates if the priority of the piece has been increased.
func (rs *Requested) Add(index uint32, prio int8, want bool) (<-chan struct{}, bool) {
	added := false
	r := rs.pieces[index]
	if r == nil {
		r = &RequestedPiece{}
		rs.pieces[index] = r
		added = true
	}
	if prio > IdlePriority {
		r.prio = append(r.prio, prio)
		added = true
	}

	if want && r.done == nil {
		r.done = make(chan struct{})
	}
	return r.done, added
}

func (rs *Requested) del(index uint32) {
	if rs.pieces[index].done != nil {
		close(rs.pieces[index].done)
		rs.pieces[index].done = nil
	}
	delete(rs.pieces, index)
}

// Del deletes a request for a piece.  It returns true if the piece has
// been cancelled (no other clients are requesting this piece).
func (rs *Requested) Del(index uint32, prio int8) bool {
	r := rs.pieces[index]
	if r == nil {
		return false
	}
	for i, p := range r.prio {
		if p == prio {
			r.prio = append(r.prio[:i], r.prio[i+1:]...)
			if len(r.prio) == 0 {
				rs.del(index)
				return true
			}
			return false
		}
	}
	return false
}

// Count counts the number of request pieces that satisfy a given predicate.
func (rs *Requested) Count(f func(uint32) bool) int {
	count := 0
	for i := range rs.pieces {
		if f(i) {
			count++
		}
	}
	return count
}

func hasPriority(r *RequestedPiece, prio int8) bool {
	if prio == IdlePriority {
		return true
	}
	for _, p := range r.prio {
		if p == prio {
			return true
		}
	}
	return false
}

// Done is called to indicate that a piece has finished downloading.
func (rs *Requested) Done(index uint32) {
	r := rs.pieces[index]
	if r == nil {
		return
	}

	if r.done != nil {
		close(r.done)
		r.done = nil
	}

	rs.DelIdlePiece(index)
}

// DelIdle cancels all pieces that are not currently requested by any client.
func (rs *Requested) DelIdle() {
	for index := range rs.pieces {
		rs.DelIdlePiece(index)
	}
}

// DelIdlePiece deletes a given piece only if it is not currently
// requested by a client.
func (rs *Requested) DelIdlePiece(index uint32) {
	r := rs.pieces[index]
	if r == nil {
		return
	}
	if len(r.prio) == 0 {
		rs.del(index)
	}
}
