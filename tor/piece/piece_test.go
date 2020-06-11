package piece

import (
	"io"
	"testing"
	"sync/atomic"

	"github.com/jech/storrent/alloc"
	"github.com/jech/storrent/hash"
)

var zeroChunkHash = hash.Hash([]byte{
	0x2e, 0x00, 0x0f, 0xa7, 0xe8, 0x57, 0x59, 0xc7, 0xf4, 0xc2,
	0x54, 0xd4, 0xd9, 0xc3, 0x3e, 0xf4, 0x81, 0xe4, 0x59, 0xa7,
})
var zeroHash = hash.Hash([]byte{
	0, 0, 0, 0, 0, 0, 0, 0, 0, 0,
	0, 0, 0, 0, 0, 0, 0, 0, 0, 0,
})

func TestPieces(t *testing.T) {
	ps := &Pieces{}
	ps.Complete(256*1024, 10*256*1024+133*1024)
	defer func() {
		ps.Del()
		if a := alloc.Bytes(); a != 0 {
			t.Errorf("Memory leak (%v bytes)", a)
		}
	}()

	if !ps.Bitmap().Empty() {
		t.Errorf("Bitmap not empty")
	}
	if ps.Length() != 10*256*1024+133*1024 {
		t.Errorf("Bad length")
	}
	if ps.PieceSize() != 256*1024 {
		t.Errorf("Bad pice size")
	}
	if ps.PieceLength(4) != 256*1024 {
		t.Errorf("Bad length (inner piece)")
	}
	if ps.PieceLength(10) != 133*1024 {
		t.Errorf("Bad length (last piece)")
	}
	if ps.PieceLength(11) != 0 {
		t.Errorf("Bad length (after end)")
	}

	if !ps.PieceEmpty(4) {
		t.Errorf("Piece is not empty")
	}

	o, l := ps.Hole(0, 0)
	if o != 0 || l != 256*1024 {
		t.Errorf("Hole (empty): %v %v", o, l)
	}

	buf := make([]byte, 88)

	n, err := ps.ReadAt(buf, 42*1024)
	if n != 0 || err != nil {
		t.Errorf("ReadAt: %v %v", n, err)
	}

	n, err = ps.ReadAt(buf, ps.Length())
	if n != 0 || err != io.EOF {
		t.Errorf("ReadAt: %v %v", n, err)
	}

	n, err = ps.ReadAt(buf, ps.Length()+42)
	if n != 0 || err != io.EOF {
		t.Errorf("ReadAt: %v %v", n, err)
	}

	zeroes := make([]byte, 256*1024)
	count, complete, err := ps.AddData(1, 0, zeroes[:16384], 1)
	if count != 16384 || complete || err != nil {
		t.Errorf("Adddata: %v %v %v", count, complete, err)
	}
	if ps.PieceComplete(1) {
		t.Errorf("Piece is complete")
	}
	if _, bm := ps.PieceBitmap(1); bm.Count() != 1 {
		t.Errorf("Piece bitmap not 1")
	}

	o, l = ps.Hole(1, 0)
	if o != 16*1024 || l != 256*1024-16*1024 {
		t.Errorf("Hole (partial): %v %v", o, l)
	}

	count, complete, err = ps.AddData(1, 16384, zeroes[16384:], 2)
	if count != 256*1024-16384 || !complete || err != nil {
		t.Errorf("Adddata: %v %v %v", count, complete, err)
	}
	if ps.PieceComplete(1) {
		t.Errorf("Piece is complete")
	}
	if _, bm := ps.PieceBitmap(1); bm.Count() != 256/16 {
		t.Errorf("Piece bitmap not %v", 256/16)
	}

	o, l = ps.Hole(1, 0)
	if o != ^uint32(0) || l != ^uint32(0) {
		t.Errorf("Hole (partial): %v %v", o, l)
	}

	n, err = ps.ReadAt(buf, 256*1024+20000)
	if n != 0 || err != nil {
		t.Errorf("ReadAt: %v %v", n, err)
	}

	done, peers, err := ps.Finalise(1, zeroChunkHash)
	if !done || len(peers) != 2 || err != nil {
		t.Errorf("Finalise: %v %v", done, err)
	}
	if !ps.PieceComplete(1) {
		t.Errorf("Piece is not complete")
	}

	n, err = ps.ReadAt(buf, 256*1024+20000)
	if n != len(buf) || err != nil {
		t.Errorf("ReadAt: %v %v", n, err)
	}

	count, complete, err = ps.AddData(2, 0, zeroes, 3)
	if count != 256*1024 || !complete || err != nil {
		t.Errorf("AddData: %v %v %v", count, complete, err)
	}

	done, _, err = ps.Finalise(2, zeroHash)
	if done || err != ErrHashMismatch {
		t.Errorf("Finalise (bad): %v %v", done, err)
	}

	count, complete, err =
		ps.AddData(3, 16*1024, zeroes[:16*1024], 4)
	if count != 16*1024 || complete || err != nil {
		t.Errorf("AddData: %v %v %v", count, complete, err)
	}

	if b := ps.Bytes(); b != 2*256*1024 {
		t.Errorf("Bytes: %v", b)
	}

	ps.Expire(256*1024, nil, func(index  uint32) {})

	if b := ps.Bytes(); b != 256*1024 {
		t.Errorf("Bytes: %v", b)
	}

	ps.Del()
	if bytes := alloc.Bytes(); bytes != 0 {
		t.Errorf("%v bytes allocated", bytes)
	}
}

func BenchmarkAddDel(b *testing.B) {
	ps := &Pieces{}
	chunk := make([]byte, 16 * 1024)
	chunks := uint32(4096)
	ps.Complete(256*1024, int64(chunks) * 16 * 1024)
	counter := uint32(0)
	b.SetBytes(16 * 1024)
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			n := (atomic.AddUint32(&counter, 1) - 1) % chunks
			index := n / (256 / 16)
			begin := (n % (256 / 16)) * 16 * 1024
			_, complete, err :=
				ps.AddData(index, begin, chunk, ^uint32(0))
			if err != nil {
				b.Errorf("AddData: %v", err)
			}
			if complete {
				ps.Lock()
				ps.del(index, true)
				ps.Unlock()
			}
		}
	})

	ps.Del()
	if bytes := alloc.Bytes(); bytes != 0 {
		b.Errorf("%v bytes allocated", bytes)
	}
}

func BenchmarkAddFinalise(b *testing.B) {
	ps := &Pieces{}
	chunk := make([]byte, 16 * 1024)
	chunks := uint32(4096)
	ps.Complete(256*1024, int64(chunks) * 16 * 1024)
	counter := uint32(0)
	b.SetBytes(16 * 1024)
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			n := (atomic.AddUint32(&counter, 1) - 1) % chunks
			index := n / (256 / 16)
			begin := (n % (256 / 16)) * 16 * 1024
			_, complete, err :=
				ps.AddData(index, begin, chunk, ^uint32(0))
			if err != nil {
				b.Errorf("AddData: %v", err)
			}
			if complete {
				_, _, err := ps.Finalise(index, zeroHash)
				if err != ErrHashMismatch {
					b.Errorf("Finalise: %v", err)
				}
			}
		}
	})

	ps.Del()
	if bytes := alloc.Bytes(); bytes != 0 {
		b.Errorf("%v bytes allocated", bytes)
	}
}

func prepare(chunks uint32) (*Pieces, error) {
	ps := &Pieces{}
	chunk := make([]byte, 16 * 1024)
	ps.Complete(256*1024, int64(chunks) * 16 * 1024)

	for n := uint32(0); n < chunks; n++ {
		index := n / (256 / 16)
		begin := (n % (256 / 16)) * 16 * 1024
		_, complete, err :=
			ps.AddData(index, begin, chunk, ^uint32(0))
		if err != nil {
			return nil, err
		}
		if complete {
			_, _, err := ps.Finalise(index, zeroChunkHash)
			if err != nil {
				return nil, err
			}
		}
	}

	return ps, nil
}

func BenchmarkRead(b *testing.B) {
	chunks := uint32(4096)
	ps, err := prepare(chunks)
	if err != nil {
		b.Fatalf("Prepare: %v", err)
	}

	b.SetBytes(16 * 1024)
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		buf := make([]byte, 16384)
		n := uint32(0)
		for pb.Next() {
			c, err := ps.ReadAt(buf, int64(n) * 16 * 1024)
			if c != 16384 || err != nil {
				b.Errorf("ReadAt: %v %v", c, err)
			}
			n = (n + 1) % chunks
		}
	})

	ps.Del()
	if bytes := alloc.Bytes(); bytes != 0 {
		b.Errorf("%v bytes allocated", bytes)
	}
}

func BenchmarkReadUpdate(b *testing.B) {
	chunks := uint32(4096)
	ps, err := prepare(chunks)
	if err != nil {
		b.Fatalf("Prepare: %v", err)
	}

	b.SetBytes(16 * 1024)
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		buf := make([]byte, 16384)
		n := uint32(0)
		for pb.Next() {
			complete := ps.Update(n / (256 / 16))
			if !complete {
				b.Errorf("Update: %v", complete)
			}
			c, err := ps.ReadAt(buf, int64(n) * 16 * 1024)
			if c != 16384 || err != nil {
				b.Errorf("ReadAt: %v %v", c, err)
			}
			n = (n + 1) % chunks
		}
	})

	ps.Del()
	if bytes := alloc.Bytes(); bytes != 0 {
		b.Errorf("%v bytes allocated", bytes)
	}
}

