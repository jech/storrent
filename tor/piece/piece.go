package piece

import (
	"crypto/sha1"
	"errors"
	"io"
	"math/rand"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"storrent/alloc"
	"storrent/bitmap"
	"storrent/config"
	"storrent/hash"
	"storrent/mono"
)

// ErrHashMismatch is returned by AddData when hash validation failed.
var ErrHashMismatch = errors.New("hash mismatch")

// ErrDeleted indicates that the pieces structure has been deleted.
var ErrDeleted = errors.New("pieces deleted")

// A Piece represents a single piece of a torrent.  It can be in one of
// three states: incomplete, busy (hash is being computed), and complete.
type Piece struct {
	data   []byte
	bitmap bitmap.Bitmap
	state  uint32
	time   mono.Time
	peers  []uint32
}

// Values for Piece.state
const (
	stateComplete uint32 = 1 // piece is complete, hash has been verified
	stateBusy     uint32 = 2 // hash is currently being computed
)

func (p *Piece) complete() bool {
	return p.state == stateComplete
}

func (p *Piece) Complete() bool {
	state := atomic.LoadUint32(&p.state)
	return state == stateComplete
}

func (p *Piece) busy() bool {
	return p.state == stateBusy
}

func (p *Piece) Busy() bool {
	state := atomic.LoadUint32(&p.state)
	return state == stateBusy
}

func (p *Piece) busyOrComplete() bool {
	return p.state != 0
}

func (p *Piece) BusyOrComplete() bool {
	state := atomic.LoadUint32(&p.state)
	return state != 0
}

func (p *Piece) setState(oldstate, state uint32) {
	ok := atomic.CompareAndSwapUint32(&p.state, oldstate, state)
	if !ok {
		panic("wrong piece state")
	}
}

type Pieces struct {
	sync.RWMutex
	deleted bool // torrent destroyed, don't add new data
	pieces  []Piece
	count   int // count of non-empty pieces

	// set by Complete(), immutable afterwards
	pieceSize uint32
	length    int64
}

func (ps *Pieces) Length() int64 {
	return ps.length
}

func (ps *Pieces) PieceSize() uint32 {
	return ps.pieceSize
}

func (ps *Pieces) Num() int {
	return len(ps.pieces)
}

func (ps *Pieces) Complete(psize uint32, length int64) {
	if ps.length > 0 {
		panic("Pieces.Complete() called twice")
	}
	ps.pieces = make([]Piece, (length+int64(psize-1))/int64(psize))
	ps.pieceSize = psize
	ps.length = length
}

// Bitmap returns the a bitmap with a bit set for each complete piece.
func (ps *Pieces) Bitmap() bitmap.Bitmap {
	b := bitmap.New(len(ps.pieces))
	ps.RLock()
	defer ps.RUnlock()
	for i, p := range ps.pieces {
		if p.complete() {
			b.Set(i)
		}
	}
	return b
}

func (ps *Pieces) PieceComplete(n uint32) bool {
	return ps.pieces[n].Complete()
}

func (ps *Pieces) PieceEmpty(n uint32) bool {
	ps.RLock()
	v := ps.pieces[n].bitmap.Empty()
	ps.RUnlock()
	return v
}

func (ps *Pieces) PieceBitmap(n uint32) (int, bitmap.Bitmap) {
	var v bitmap.Bitmap
	chunks := ps.pieceChunks(n)
	ps.RLock()
	v = ps.pieces[n].bitmap.Copy()
	ps.RUnlock()
	return chunks, v
}

func (ps *Pieces) pieceLength(index uint32) uint32 {
	last := uint32(ps.length / int64(ps.pieceSize))
	if index < last {
		return ps.pieceSize
	} else if index == last {
		return uint32(ps.length % int64(ps.pieceSize))
	}
	return 0
}

func (ps *Pieces) PieceLength(index uint32) uint32 {
	ps.RLock()
	v := ps.pieceLength(index)
	ps.RUnlock()
	return v
}

func (ps *Pieces) pieceChunks(index uint32) int {
	return int((ps.pieceLength(index) + config.ChunkSize - 1) /
		config.ChunkSize)
}

func (ps *Pieces) ReadAt(p []byte, off int64) (int, error) {
	if off >= ps.length {
		return 0, io.EOF
	}
	index := int(off / int64(ps.pieceSize))
	begin := int(off % int64(ps.pieceSize))

	ps.RLock()
	defer ps.RUnlock()

	if !ps.pieces[index].complete() || len(ps.pieces[index].data) <= begin {
		return 0, nil
	}

	n := copy(p, ps.pieces[index].data[begin:])
	return n, nil
}

// Update sets the timestamp of a piece to a given time.  It returns true
// if the piece is complete.
func (ps *Pieces) Update(index uint32, when time.Time) bool {
	wh := mono.New(when)

	ps.Lock()
	defer ps.Unlock()

	if ps.deleted {
		return false
	}

	if ps.pieces[index].time.Before(wh) {
		ps.pieces[index].time = wh
	}
	return ps.pieces[index].complete()
}

func (p *Piece) addPeer(peer uint32) {
	if peer == 0 {
		return
	}

	for i := 0; i < len(p.peers); i++ {
		if p.peers[i] == peer {
			return
		}
	}
	p.peers = append(p.peers, peer)
}

func (ps *Pieces) AddData(index uint32, begin uint32, data []byte, peer uint32) (count uint32, complete bool, err error) {

	if ps.pieces[index].BusyOrComplete() {
		return
	}

	ps.Lock()
	defer ps.Unlock()

	// test again
	if ps.pieces[index].busyOrComplete() {
		return
	}

	if ps.deleted {
		err = ErrDeleted
		return
	}
	cs := config.ChunkSize
	pl := ps.pieceLength(index)

	if begin%cs != 0 {
		err = errors.New("adding data at odd offset")
		return
	}
	if begin >= uint32(pl) {
		err = errors.New("adding data beyond end of piece")
		return
	}

	if ps.pieces[index].data == nil {
		var new []byte
		new, err = alloc.Alloc(int(pl))
		if err != nil {
			return
		}
		ps.pieces[index].data = new
		ps.count++
	}

	offset := begin
	added := false
	for count < uint32(len(data)) {
		c := int(offset / config.ChunkSize)
		l := pl - offset
		if l > cs {
			l = cs
		}
		if l <= 0 || uint32(len(data)) < count+l {
			break
		}
		if !ps.pieces[index].bitmap.Get(c) {
			copy(ps.pieces[index].data[int(offset):],
				data[count:count+l])
			ps.pieces[index].bitmap.Set(c)
			added = true
		}
		offset += l
		count += l
		if l%cs != 0 {
			break
		}
	}
	if added && peer != ^uint32(0) {
		ps.pieces[index].addPeer(peer)
	}
	complete = ps.pieces[index].bitmap.All(ps.pieceChunks(index))
	return
}

func (ps *Pieces) Finalise(index uint32, h hash.Hash) (done bool, peers []uint32, err error) {
	if ps.pieces[index].BusyOrComplete() {
		return
	}

	ps.Lock()
	defer ps.Unlock()

	// test again
	if ps.pieces[index].busyOrComplete() {
		return
	}

	if ps.deleted {
		err = ErrDeleted
		return
	}

	if ps.pieces[index].bitmap.Count() != ps.pieceChunks(index) {
		return
	}

	data := ps.pieces[index].data

	ps.pieces[index].setState(0, stateBusy)
	ps.Unlock()

	hsh := sha1.Sum(data)
	hh := hash.Hash(hsh[:])

	ps.Lock()
	peers = ps.pieces[index].peers
	ps.pieces[index].peers = nil
	if !hh.Equals(h) {
		ps.pieces[index].setState(stateBusy, 0)
		ps.del(index, true)
		err = ErrHashMismatch
		return
	}
	ps.pieces[index].setState(stateBusy, stateComplete)
	done = true
	return
}

// del deletes piece p.  Called locked, it may temporarily release the lock.
func (ps *Pieces) del(p uint32, force bool) (done bool, complete bool) {
	if ps.pieces[p].data == nil {
		return
	}

	for ps.pieces[p].busy() {
		if !force {
			return
		}
		ps.Unlock()
		t := 10 * time.Microsecond
		for ps.pieces[p].Busy() {
			time.Sleep(t)
			if t < 10*time.Millisecond {
				t = t * 2
			}
		}
		ps.Lock()
	}

	done = true
	complete = ps.pieces[p].complete()

	err := alloc.Free(ps.pieces[p].data)
	if err != nil {
		panic(err)
	}
	ps.pieces[p].data = nil
	ps.pieces[p].peers = nil
	ps.pieces[p].bitmap = nil
	if complete {
		ps.pieces[p].setState(stateComplete, 0)
	}
	ps.count--
	if ps.count < 0 {
		panic("Negative pieces count")
	}

	return
}

func (ps *Pieces) Count() int {
	ps.RLock()
	v := ps.count
	ps.RUnlock()
	return v
}

func (ps *Pieces) Hole(index, offset uint32) (uint32, uint32) {
	ps.RLock()
	defer ps.RUnlock()
	p := ps.pieces[index]
	if p.busyOrComplete() {
		return ^uint32(0), ^uint32(0)
	}
	chunks := ps.pieceChunks(index)

	first := -1
	for i := int(offset / config.ChunkSize); i < chunks; i++ {
		if !p.bitmap.Get(i) {
			first = i
			break
		}
	}
	if first < 0 {
		return ^uint32(0), ^uint32(0)
	}
	count := 0
	for count = 1; count < chunks; count++ {
		if p.bitmap.Get(first + count) {
			break
		}
	}

	o := uint32(first) * config.ChunkSize
	if count == chunks {
		return o, ps.pieceLength(index) - uint32(first)*config.ChunkSize
	}
	return o, uint32(count) * config.ChunkSize
}

// Bytes returns an overestimate the amount of memory used up by ps.
func (ps *Pieces) Bytes() int64 {
	ps.Lock()
	defer ps.Unlock()
	return int64(ps.count) * int64(ps.pieceSize)
}

// Expire deletes pieces until the amount of space consumed is smaller than
// the value given by bytes.  It calls f whenever it deletes a complete piece.
func (ps *Pieces) Expire(bytes int64, available []uint16, f func(index uint32)) int {
	now := mono.Now()

	ps.RLock()
	npieces := len(ps.pieces)
	t := make([]uint32, npieces)
	for i := range t {
		t[i] = now.Sub(ps.pieces[i].time)
	}
	ps.RUnlock()

	a := rand.Perm(npieces)
	sort.Slice(a, func(i, j int) bool {
		if t[a[i]] >= 7200 && t[a[j]] >= 7200 {
			// commonest first
			var ai, aj uint16
			if a[i] < len(available) {
				ai = available[a[i]]
			}
			if a[j] < len(available) {
				aj = available[a[j]]
			}
			if ai > aj {
				return true
			}
			if ai < aj {
				return false
			}
		}
		// least recently requested
		return t[a[i]] > t[a[j]]
	})

	todo := ps.Bytes() - bytes

	count := 0
	for _, i := range a {
		index := uint32(i)
		if todo <= 0 {
			break
		}
		ps.Lock()
		done, complete := ps.del(index, false)
		ps.Unlock()
		if done {
			if complete {
				f(index)
			}
			count++
			todo -= int64(ps.pieceSize)
		}
	}
	return count
}

func (ps *Pieces) Del() {
	ps.Lock()
	defer ps.Unlock()
	for i := uint32(0); i < uint32(len(ps.pieces)); i++ {
		ps.del(i, true)
	}
	ps.deleted = true
}
