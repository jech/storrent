package tor

import (
	"context"
	"errors"
	"io"
	"runtime"

	"storrent/config"
)

var errClosedReader = errors.New("closed reader")

type requested struct {
	index uint32
	prio  int8
}

type Reader struct {
	torrent *Torrent
	offset  int64
	length  int64

	position       int64
	requested      []requested
	requestedIndex int
	ch             <-chan struct{}
	context        context.Context
}

func (t *Torrent) NewReader(ctx context.Context, offset int64, length int64) *Reader {
	r := &Reader{
		torrent:        t,
		offset:         offset,
		length:         length,
		context:        ctx,
		requestedIndex: -1,
	}
	runtime.SetFinalizer(r, (*Reader).Close)
	return r
}

func (r *Reader) Seek(o int64, whence int) (n int64, err error) {
	if r.torrent == nil {
		return r.position, errClosedReader
	}
	var pos int64
	switch whence {
	case io.SeekStart:
		pos = o
	case io.SeekCurrent:
		pos = r.position + o
	case io.SeekEnd:
		pos = r.length + o
	default:
		return r.position, errors.New("seek: invalid whence")
	}
	if pos < 0 {
		return r.position, errors.New("seek: negative position")
	}
	r.position = pos
	return pos, nil
}

func (r *Reader) chunks(pos int64, limit int64) []requested {
	if pos < 0 || pos > limit {
		return nil
	}

	t := r.torrent
	ps := int64(t.Pieces.PieceSize())
	index := uint32(pos / ps)
	begin := uint32(pos % ps)
	max := uint32(limit / ps)
	remain := t.Pieces.PieceSize() - begin

	// five second prefetch, but at least one piece
	prefetch :=
		uint32((config.PrefetchRate*5-float64(remain))/float64(ps) + 0.5)
	if prefetch < 1 {
		prefetch = 1
	}

	if index+1+prefetch > max {
		prefetch = max - index - 1
	}

	i := uint32(0)
	c := make([]requested, 0, 2+prefetch)
	c = append(c, requested{index + i, 1})
	i++

	// aggressive prefetch if less than two seconds left
	if float64(remain) < config.PrefetchRate*2 {
		c = append(c, requested{index + i, 0})
		i++
	}

	for i < prefetch+1 {
		c = append(c, requested{index + i, -1})
		i++
	}
	return c
}

func (r *Reader) request(pos int64, limit int64) (<-chan struct{}, error) {
	if r.requestedIndex >= 0 {
		index := uint32(pos / int64(r.torrent.Pieces.PieceSize()))
		if r.requestedIndex == int(index) {
			return r.ch, nil
		}
	}

	chunks := r.chunks(pos, limit)
	old := r.requested
	r.requested = make([]requested, 0, len(chunks))
	var done <-chan struct{}
	var err error

	for i, c := range chunks {
		d, dn, e :=
			r.torrent.Request(c.index, c.prio, true, i == 0)
		if d {
			r.requested = append(r.requested, c)
		}
		if i == 0 {
			done = dn
			err = e
			if err != nil {
				break
			}
		}
	}

	for _, c := range old {
		r.torrent.Request(c.index, c.prio, false, false)
	}

	if len(chunks) > 0 {
		r.requestedIndex = int(chunks[0].index)
	} else {
		r.requestedIndex = -1
	}
	r.ch = done
	return done, err
}

func (r *Reader) Read(a []byte) (n int, err error) {
	t := r.torrent
	if t == nil {
		err = errClosedReader
		return
	}

	if r.position >= r.length {
		r.request(-1, -1)
		err = io.EOF
		return
	}

	err = r.context.Err()
	if err != nil {
		r.request(-1, -1)
		return
	}

	done, err := r.request(r.offset+r.position, r.offset+r.length)
	if err != nil {
		return
	}

	if done != nil {
		select {
		case <-t.Done:
			r.request(-1, -1)
			err = ErrTorrentDead
			return
		case <-r.context.Done():
			r.request(-1, -1)
			err = r.context.Err()
			return
		case <-done:
		}
	}

	if r.position+int64(len(a)) < r.length {
		n, err = t.Pieces.ReadAt(a, r.offset+r.position)
	} else {
		n, err = t.Pieces.ReadAt(a[:r.length-r.position],
			r.offset+r.position)
	}
	if err == nil && int64(n) == r.length-r.position {
		err = io.EOF
	}

	if err != nil {
		r.request(-1, -1)
	}

	r.position += int64(n)
	return
}

func (r *Reader) Close() error {
	r.request(-1, -1)
	r.torrent = nil
	runtime.SetFinalizer(r, nil)
	return nil
}
