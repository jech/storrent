package bufconn

import (
	"errors"
	"io"
	"net"
	"runtime"
	"sync"
	"time"
)

type errTimeout int

var ErrTimeout = errTimeout(0)

func (e errTimeout) Error() string {
	return "timeout"
}

func (e errTimeout) Timeout() bool {
	return true
}

var ErrClosed = errors.New("use of closed WebRTC connection")
var ErrTruncated = errors.New("message truncated")

const bufsize = 64 * 1024

var pool sync.Pool = sync.Pool{
	New: func() interface{} {
		return make([]byte, bufsize+1)
	},
}

type deadline struct {
	timer *time.Timer
	ch    chan struct{}
}

type conn struct {
	rw    io.ReadWriter
	close func() error

	mu     sync.Mutex
	buf    []byte
	r, w   int
	err    error
	rd, wd deadline
}

func New(rw io.ReadWriter, cl func() error) net.Conn {
	c := &conn{
		rw: rw, close: cl,
		rd: deadline{ch: make(chan struct{})},
		wd: deadline{ch: make(chan struct{})},
	}

	runtime.SetFinalizer(c, (*conn).Close)
	c.buf = pool.Get().([]byte)[:bufsize]

	return c
}

type ioresult struct {
	n   int
	err error
}

func (c *conn) fill() (int, error) {
	if c.err != nil {
		return 0, c.err
	}

	if c.r != c.w {
		panic("Eek!")
	}

	dch := c.rd.ch

	c.mu.Unlock()
	var r ioresult
	buf := pool.Get().([]byte)
		ch := make(chan ioresult)
		go func() {
			n, err := c.rw.Read(buf)
			ch <- ioresult{n, err}
		}()
		select {
		case r = <-ch:
		case <-dch:
			r.err = ErrTimeout
		}
	c.mu.Lock()

	if r.err == nil && r.n >= len(buf) {
		r.err = ErrTruncated
	}
	c.r = 0
	c.w = copy(c.buf, buf[:r.n])
	pool.Put(buf)
	c.err = r.err
	if c.w == 0 {
		return 0, c.err
	}
	return c.w, nil
}

func (c *conn) Read(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	n := len(p)
	if n == 0 {
		return 0, c.err
	}

	if c.r == c.w {
		n, _ := c.fill()
		if n == 0 {
			return 0, c.err
		}
	}

	n = copy(p, c.buf[c.r:c.w])
	c.r += n
	return n, nil
}

func (c *conn) Write(p []byte) (int, error) {
	c.mu.Lock()
	err := c.err
	dch := c.wd.ch
	c.mu.Unlock()

	if err != nil {
		return 0, err
	}

	ch := make(chan ioresult)
	go func() {
		i, err := c.rw.Write(p)
		ch <- ioresult{i, err}
	}()
	var r ioresult
	select {
	case r = <-ch:
	case <-dch:
		r.err = ErrTimeout
	}
	if r.err != nil {
		c.mu.Lock()
		c.err = r.err
		c.mu.Unlock()
	}
	return r.n, r.err
}

func (c *conn) Close() error {
	c.mu.Lock()
	c.r = 0
	c.w = 0
	pool.Put(c.buf[:cap(c.buf)])
	c.buf = nil
	c.err = ErrClosed
	c.mu.Unlock()

	runtime.SetFinalizer(c, nil)
	return c.close()
}

func (c *conn) LocalAddr() net.Addr {
	return nil
}

func (c *conn) RemoteAddr() net.Addr {
	return nil
}

func (d *deadline) set(t time.Time) {
	if d.timer != nil && !d.timer.Stop() {
		<-d.ch
	}
	d.timer = nil

	var closed bool

	select {
	case <-d.ch:
		closed = true
	default:
	}

	var zero time.Time
	if t == zero {
		if closed {
			d.ch = make(chan struct{})
		}
		return
	}

	duration := time.Until(t)
	if duration < 0 {
		if !closed {
			close(d.ch)
		}
		return
	}

	if closed {
		d.ch = make(chan struct{})
	}
	d.timer = time.AfterFunc(duration, func() {
		close(d.ch)
	})
}

func (c *conn) SetDeadline(t time.Time) error {
	c.mu.Lock()
	c.rd.set(t)
	c.wd.set(t)
	c.mu.Unlock()
	return nil
}

func (c *conn) SetReadDeadline(t time.Time) error {
	c.mu.Lock()
	c.rd.set(t)
	c.mu.Unlock()
	return nil
}

func (c *conn) SetWriteDeadline(t time.Time) error {
	c.mu.Lock()
	c.wd.set(t)
	c.mu.Unlock()
	return nil
}
