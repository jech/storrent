// Package piece implements a data structure used for storing the actual
// contents of a torrent.
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

	"github.com/jech/storrent/alloc"
	"github.com/jech/storrent/bitmap"
	"github.com/jech/storrent/config"
	"github.com/jech/storrent/hash"
	"github.com/jech/storrent/mono"
)

// ErrHashMismatch is returned by AddData when hash validation fails.
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

// Complete returns true if the piece is complete and its hash has been verified.
func (p *Piece) Complete() bool {
	state := atomic.LoadUint32(&p.state)
	return state == stateComplete
}

func (p *Piece) busy() bool {
	return p.state == stateBusy
}

// Busy returns true if the piece's hash is currently being verified.
func (p *Piece) Busy() bool {
	state := atomic.LoadUint32(&p.state)
	return state == stateBusy
}

func (p *Piece) busyOrComplete() bool {
	return p.state != 0
}

// BusyOrComplete returns true if the piece is either busy or complete.
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

// Time returns the piece's access time.
func (p *Piece) Time() mono.Time {
	return mono.LoadAtomic(&p.time)
}

// Time sets the piece's access time.
func (p *Piece) SetTime(tm mono.Time) {
	mono.StoreAtomic(&p.time, tm)
}

// Pieces represents the contents of a torrent.
type Pieces struct {
	mu      sync.RWMutex
	deleted bool // torrent destroyed, don't add new data
	pieces  []Piece
	count   int // count of non-empty pieces

	// set by Complete(), immutable afterwards
	pieceSize uint32
	length    int64
}

// Length returns the length in bytes of a torrent's contents.
func (ps *Pieces) Length() int64 {
	return ps.length
}

// PieceSize returns a torrent's piece size.
func (ps *Pieces) PieceSize() uint32 {
	return ps.pieceSize
}

// Num returns the number of pieces in a torrent.
func (ps *Pieces) Num() int {
	return len(ps.pieces)
}

// Complete is called when a torrent's metadata becomes known.
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
	ps.mu.RLock()
	defer ps.mu.RUnlock()
	for i, p := range ps.pieces {
		if p.complete() {
			b.Set(i)
		}
	}
	return b
}

// PieceComplete returns true if a given piece is complete.
func (ps *Pieces) PieceComplete(n uint32) bool {
	return ps.pieces[n].Complete()
}

// PieceEmpty returns true if no data is available for a given piece.
func (ps *Pieces) PieceEmpty(n uint32) bool {
	ps.mu.RLock()
	defer ps.mu.RUnlock()
	return ps.pieces[n].bitmap.Empty()
}

// PieceBitmap returns a bitmap of complete pieces.
func (ps *Pieces) PieceBitmap(n uint32) (int, bitmap.Bitmap) {
	var v bitmap.Bitmap
	chunks := ps.pieceChunks(n)
	ps.mu.RLock()
	defer ps.mu.RUnlock()
	v = ps.pieces[n].bitmap.Copy()
	return chunks, v
}

// PieceLength returns the length in bytes of a given piece.
func (ps *Pieces) PieceLength(index uint32) uint32 {
	last := uint32(ps.length / int64(ps.pieceSize))
	if index < last {
		return ps.pieceSize
	} else if index == last {
		return uint32(ps.length % int64(ps.pieceSize))
	}
	return 0
}

func (ps *Pieces) pieceChunks(index uint32) int {
	return int((ps.PieceLength(index) + config.ChunkSize - 1) /
		config.ChunkSize)
}

// ReadAt copies a range of bytes out of a torrent.  It only returns data
// whose hash has been validated.
func (ps *Pieces) ReadAt(p []byte, off int64) (int, error) {
	if off >= ps.length {
		return 0, io.EOF
	}
	index := int(off / int64(ps.pieceSize))
	begin := int(off % int64(ps.pieceSize))

	ps.mu.RLock()
	defer ps.mu.RUnlock()

	if !ps.pieces[index].complete() || len(ps.pieces[index].data) <= begin {
		return 0, nil
	}

	n := copy(p, ps.pieces[index].data[begin:])
	return n, nil
}

// Update sets the last accessed time of a piece.  It returns true if the
// piece is complete.
func (ps *Pieces) Update(index uint32) bool {
	wh := mono.Now()

	if ps.pieces[index].Time().Before(wh) {
		ps.pieces[index].SetTime(wh)
	}

	return ps.pieces[index].Complete()
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

// AddData adds data to a torrent.
func (ps *Pieces) AddData(index uint32, begin uint32, data []byte, peer uint32) (count uint32, complete bool, err error) {

	if ps.pieces[index].BusyOrComplete() {
		return
	}

	ps.mu.Lock()
	defer ps.mu.Unlock()

	// test again
	if ps.pieces[index].busyOrComplete() {
		return
	}

	if ps.deleted {
		err = ErrDeleted
		return
	}
	cs := config.ChunkSize
	pl := ps.PieceLength(index)

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

// Finalise validates the hash of a piece and either marks it as complete
// or discards its data.  This is a time-consuming operation, and should
// be called from its own goroutine.
func (ps *Pieces) Finalise(index uint32, h hash.Hash) (done bool, peers []uint32, err error) {
	if ps.pieces[index].BusyOrComplete() {
		return
	}

	ps.mu.Lock()
	defer ps.mu.Unlock()

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
	ps.mu.Unlock()

	hsh := sha1.Sum(data)
	hh := hash.Hash(hsh[:])

	ps.mu.Lock()
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
		ps.mu.Unlock()
		t := 10 * time.Microsecond
		for ps.pieces[p].Busy() {
			time.Sleep(t)
			if t < 10*time.Millisecond {
				t = t * 2
			}
		}
		ps.mu.Lock()
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

// Count returns the number of non-empty pieces.
func (ps *Pieces) Count() int {
	ps.mu.RLock()
	defer ps.mu.RUnlock()
	return ps.count
}

// Hole returns the size of the hole starting at a given position.
func (ps *Pieces) Hole(index, offset uint32) (uint32, uint32) {
	ps.mu.RLock()
	defer ps.mu.RUnlock()
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
		return o, ps.PieceLength(index) - uint32(first)*config.ChunkSize
	}
	return o, uint32(count) * config.ChunkSize
}

// Bytes returns an overestimate the amount of memory used up by ps.
func (ps *Pieces) Bytes() int64 {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	return int64(ps.count) * int64(ps.pieceSize)
}

// Expire deletes pieces until the amount of space consumed is smaller than
// the value given by bytes.  It calls f whenever it deletes a complete piece.
func (ps *Pieces) Expire(bytes int64, available []uint16, f func(index uint32)) int {
	now := mono.Now()

	npieces := len(ps.pieces)
	t := make([]uint32, npieces)
	for i := range t {
		t[i] = now.Sub(ps.pieces[i].Time())
	}

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
		ps.mu.Lock()
		done, complete := ps.del(index, false)
		ps.mu.Unlock()
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

// Del discards the contents of a torrent from memory.
func (ps *Pieces) Del() {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	for i := uint32(0); i < uint32(len(ps.pieces)); i++ {
		ps.del(i, true)
	}
	ps.deleted = true
}
