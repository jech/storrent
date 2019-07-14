package crypto

import (
	"crypto/rc4"
	"io"
	"net"
	"sync"
	"time"
)

type Conn struct {
	conn     net.Conn
	enc, dec *rc4.Cipher
	sync.Mutex
}

func (c *Conn) Read(b []byte) (n int, err error) {
	n, err = c.conn.Read(b)
	c.dec.XORKeyStream(b[:n], b[:n])
	return
}

var pool sync.Pool = sync.Pool{
	New: func() interface{} {
		buf := make([]byte, 32*1024)
		return buf
	},
}

func (c *Conn) Write(b []byte) (n int, err error) {
	buf := pool.Get().([]byte)
	c.Lock()
	defer func() {
		c.Unlock()
		pool.Put(buf)
	}()

	for n < len(b) {
		m := len(b) - n
		if m > len(buf) {
			m = len(buf)
		}
		c.enc.XORKeyStream(buf[:m], b[n:n+m])
		var l int
		l, err = c.conn.Write(buf[:m])
		n += l
		if err == nil && l < m {
			err = io.ErrShortWrite
		}
		if err != nil {
			return
		}
	}
	return
}

func (c *Conn) Close() error {
	return c.conn.Close()
}

func (c *Conn) LocalAddr() net.Addr {
	return c.conn.LocalAddr()
}

func (c *Conn) RemoteAddr() net.Addr {
	return c.conn.RemoteAddr()
}

func (c *Conn) SetDeadline(t time.Time) error {
	return c.conn.SetDeadline(t)
}

func (c *Conn) SetReadDeadline(t time.Time) error {
	return c.conn.SetReadDeadline(t)
}

func (c *Conn) SetWriteDeadline(t time.Time) error {
	return c.conn.SetWriteDeadline(t)
}