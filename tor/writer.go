package tor

import (
	"errors"
	"io"

	"storrent/peer"
)

var errClosedWriter = errors.New("closed writer")

type writer struct {
	t      *Torrent
	index  uint32
	offset uint32
	count  uint32
	buf    []byte
}

func NewWriter(t *Torrent, index, offset, length uint32) *writer {
	return &writer{t, index, offset, length, nil}
}

func (w *writer) writeEvent(e peer.TorEvent) {
	select {
	case w.t.Event <- e:
	case <-w.t.Done:
	}
}

func (w *writer) write(data []byte) (int, error) {
	count, complete, err :=
		w.t.Pieces.AddData(w.index, w.offset, data, ^uint32(0))
	if count > 0 {
		w.writeEvent(peer.TorData{nil,
			w.index, w.offset, count, complete,
		})
		w.count -= count
		w.offset += count
	}
	return int(count), err
}

var ErrShortWrite = errors.New("short write")

func (w *writer) Write(p []byte) (int, error) {
	if w.t == nil {
		return 0, errClosedWriter
	}
	max := int(w.count) - len(w.buf)
	if max < 0 {
		return 0, ErrShortWrite
	}

	q := p
	if len(q) > max {
		q = q[:max]
	}

	var data []byte
	if len(w.buf) == 0 {
		data = q
	} else {
		w.buf = append(w.buf, q...)
		data = w.buf
	}

	n, err := w.write(data)
	data = data[n:]
	if cap(w.buf) < len(data) {
		w.buf = make([]byte, len(data))
	} else {
		w.buf = w.buf[:len(data)]
	}
	copy(w.buf, data)

	peer.DownloadEstimator.Accumulate(len(q))

	if err == nil && len(q) < len(p) {
		err = ErrShortWrite
	}
	return len(q), err
}

func (w *writer) ReadFrom(r io.Reader) (int64, error) {
	if w.t == nil {
		return 0, errClosedWriter
	}
	if w.count < uint32(len(w.buf)) {
		return 0, io.EOF
	}

	if cap(w.buf) < 32768 {
		l := len(w.buf)
		w.buf = append(w.buf, make([]byte, 32768-l)...)
		w.buf = w.buf[:l]
	}

	var count int64
	var err error
	for {
		max := 32768
		if int(w.count) < max {
			max = int(w.count)
		}
		n, er := r.Read(w.buf[len(w.buf):max])
		w.buf = w.buf[:len(w.buf)+n]
		peer.DownloadEstimator.Accumulate(n)
		m, ew := w.write(w.buf)
		copy(w.buf, w.buf[m:])
		w.buf = w.buf[:len(w.buf) - m]
		count += int64(n)
		if er != nil {
			if er != io.EOF {
				err = er
			}
			break
		}
		if ew != nil {
			err = ew
			break
		}
	}
	return count, err
}

func (w *writer) Close() error {
	if w.t == nil {
		return errClosedWriter
	}
	if w.count > 0 {
		w.writeEvent(peer.TorDrop{w.index, w.offset, w.count})
		w.count = 0
	}
	w.t = nil
	return nil
}
