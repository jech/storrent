package bufconn

import (
	"bytes"
	"errors"
	"io"
	"net"
	"sync"
	"testing"
	"time"
)

var ErrUnimplemented = errors.New("unimplemented")

type writer struct {
	io.Writer
}

func (w *writer) Read(p []byte) (int, error) {
	return 0, ErrUnimplemented
}

type reader struct {
	io.Reader
}

func (w *reader) Write(p []byte) (int, error) {
	return 0, ErrUnimplemented
}

var data = make([]byte, 412345)

func init() {
	for i := range data {
		data[i] = byte(i % 97)
	}
}

func TestRead(t *testing.T) {
	r, w := io.Pipe()
	c := New(&reader{r}, func() error { return nil })
	go func() {
		n := 0
		for n < len(data) {
			end := n + 31234
			if end > len(data) {
				end = len(data)
			}
			m, err := w.Write(data[n:end])
			if err != nil {
				t.Errorf("Write error: %v", err)
			}
			n += m
		}
	}()
	p := make([]byte, len(data)+10)
	n := 0
	for n < len(data) {
		m, err := c.Read(p[n:])
		if err != nil {
			t.Fatalf("Read error: %v", err)
		}
		n += m
	}
	p = p[:n]
	if !bytes.Equal(p, data) {
		t.Fatalf("Got corrupted data (%v, %v)", len(p), len(data))
	}
}

func TestWrite(t *testing.T) {
	r, w := io.Pipe()
	c := New(&writer{w}, func() error { return nil })
	go func() {
		n := 0
		for n < len(data) {
			end := n + 31234
			if end > len(data) {
				end = len(data)
			}
			m, err := c.Write(data[n:end])
			if err != nil {
				t.Errorf("Write error: %v", err)
			}
			n += m
		}
	}()
	p := make([]byte, len(data)+10)
	n := 0
	for n < len(data) {
		m, err := r.Read(p[n:])
		if err != nil {
			t.Fatalf("Read error: %v", err)
		}
		n += m
	}
	p = p[:n]
	if !bytes.Equal(p, data) {
		t.Fatalf("Got corrupted data (%v, %v)", len(p), len(data))
	}
}

func TestReadTimeout(t *testing.T) {
	r, _ := io.Pipe()
	c := New(&reader{r}, func() error { return nil })
	c.SetReadDeadline(time.Now().Add(time.Millisecond))
	begin := time.Now()
	n, err := c.Read(make([]byte, 10))
	if n != 0 || err != ErrTimeout {
		t.Errorf("Got %v %v, expected timeout", n, err)
	}
	if time.Since(begin) > time.Second {
		t.Errorf("Timeout took too long (%v)", time.Since(begin))
	}
}

func TestWriteTimeout(t *testing.T) {
	_, w := io.Pipe()
	c := New(&writer{w}, func() error { return nil })
	c.SetWriteDeadline(time.Now().Add(time.Millisecond))
	begin := time.Now()
	n, err := c.Write(make([]byte, 10))
	if n != 0 || err != ErrTimeout {
		t.Errorf("Got %v %v, expected timeout", n, err)
	}
	if time.Since(begin) > time.Second {
		t.Errorf("Timeout took too long (%v)", time.Since(begin))
	}
}

type zeroReader int

func (r *zeroReader) Read(p []byte) (int, error) {
	for i := range(p) {
		p[i] = 0
	}
	return len(p), nil
}

func TestTruncate(t *testing.T) {
	r := zeroReader(0)
	c := New(&reader{&r}, func() error { return nil })
	p := make([]byte, 1234)
	n := 0
	for n <= bufsize {
		m, err := c.Read(p)
		if err == ErrTruncated {
			if n != bufsize {
				t.Fatalf("Truncation happened at %v", n)
			}
			return
		}
		if err != nil {
			t.Fatalf("Got %v %v, expected truncation", n, err)
		}
		n += m
	}
	t.Fatalf("Truncation didn't happen after %v bytes", n)
}

func TestClose(t *testing.T) {
	r := zeroReader(0)
	c := New(&reader{&r}, func() error { return nil })
	p := make([]byte, 16)
	n, err := c.Read(p)
	if n != 16 || err != nil {
		t.Errorf("Read: %v %v", n, err)
	}
	err = c.Close()
	if err != nil {
		t.Errorf("Close: %v", err)
	}
	n, err = c.Read(p)
	if n != 0 || err != ErrClosed {
		t.Errorf("Read: %v %v", n, err)
	}
	n, err = c.Write(p)
	if n != 0 || err != ErrClosed {
		t.Errorf("Write: %v %v", n, err)
	}
}


func BenchmarkPipe(b *testing.B) {
	r, w := net.Pipe()
	data := make([]byte, 4096)
	b.SetBytes(int64(len(data)))

	var wg sync.WaitGroup
	wg.Add(1)

	go func(count int) {
		defer wg.Done()
		for i := 0; i < count; i++ {
			_, err := w.Write(data)
			if err != nil {
				b.Errorf("Write: %v", err)
			}
		}
	}(b.N)

	buf := make([]byte, 4096)
	for i := 0; i < b.N; i++ {
		_, err := io.ReadFull(r, buf)
		if err != nil {
			b.Fatalf("Read: %v", err)
		}
	}
	wg.Wait()
}

func BenchmarkRead(b *testing.B) {
	r, w := net.Pipe()
	c := New(&reader{r}, func() error { return nil })
	data := make([]byte, 4096)
	b.SetBytes(int64(len(data)))

	var wg sync.WaitGroup
	wg.Add(1)

	go func(count int) {
		defer wg.Done()
		for i := 0; i < count; i++ {
			_, err := w.Write(data)
			if err != nil {
				b.Errorf("Write: %v", err)
			}
		}
	}(b.N)

	buf := make([]byte, 4096)
	for i := 0; i < b.N; i++ {
		_, err := io.ReadFull(c, buf)
		if err != nil {
			b.Fatalf("Read: %v", err)
		}
	}
	wg.Wait()
}

func BenchmarkWrite(b *testing.B) {
	r, w := net.Pipe()
	c := New(&writer{w}, func() error { return nil })
	data := make([]byte, 4096)
	b.SetBytes(int64(len(data)))

	var wg sync.WaitGroup
	wg.Add(1)

	go func(count int) {
		defer wg.Done()
		for i := 0; i < count; i++ {
			_, err := c.Write(data)
			if err != nil {
				b.Errorf("Write: %v", err)
			}
		}
	}(b.N)

	buf := make([]byte, 4096)
	for i := 0; i < b.N; i++ {
		_, err := io.ReadFull(r, buf)
		if err != nil {
			b.Fatalf("Read: %v", err)
		}
	}
	wg.Wait()
}

func BenchmarkReadTimeout(b *testing.B) {
	r, w := net.Pipe()
	c := New(&reader{r}, func() error { return nil })
	data := make([]byte, 4096)
	b.SetBytes(int64(len(data)))

	var wg sync.WaitGroup
	wg.Add(1)

	go func(count int) {
		defer wg.Done()
		for i := 0; i < count; i++ {
			_, err := w.Write(data)
			if err != nil {
				b.Errorf("Write: %v", err)
			}
		}
	}(b.N)

	buf := make([]byte, 4096)
	for i := 0; i < b.N; i++ {
		c.SetReadDeadline(time.Now().Add(time.Second))
		_, err := io.ReadFull(c, buf)
		if err != nil {
			b.Fatalf("Read: %v", err)
		}
	}
	wg.Wait()
}

func BenchmarkWriteTimeout(b *testing.B) {
	r, w := net.Pipe()
	c := New(&writer{w}, func() error { return nil })
	data := make([]byte, 4096)
	b.SetBytes(int64(len(data)))

	var wg sync.WaitGroup
	wg.Add(1)

	go func(count int) {
		defer wg.Done()
		for i := 0; i < count; i++ {
			c.SetWriteDeadline(time.Now().Add(time.Second))
			_, err := c.Write(data)
			if err != nil {
				b.Errorf("Write: %v", err)
			}
		}
	}(b.N)

	buf := make([]byte, 4096)
	for i := 0; i < b.N; i++ {
		_, err := io.ReadFull(r, buf)
		if err != nil {
			b.Fatalf("Read: %v", err)
		}
	}
	wg.Wait()
}
