package tracker

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"math/rand"
	"net"
	nurl "net/url"
	"sync"
	"time"

	"golang.org/x/net/proxy"
)

// UDP represents a tracker using the protocol defined in BEP-15.
type UDP struct {
	base
}

// Announce performs a UDP announce over IPv4 and Ipv6 in parallel.
func (tracker *UDP) Announce(ctx context.Context, hash []byte, myid []byte,
	want int, size int64, port4, port6 int, proxy string,
	f func(net.IP, int) bool) error {
	ok := tracker.tryLock()
	if !ok {
		return ErrNotReady
	}
	defer tracker.unlock()

	if !tracker.ready() {
		return ErrNotReady
	}

	url, err := nurl.Parse(tracker.url)
	if err != nil {
		tracker.updateInterval(0, err)
		return err
	}

	tracker.time = time.Now()

	var i4, i6 time.Duration
	var e4, e6 error
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		i4, e4 = announceUDP(ctx, "udp4", f,
			url, hash, myid, want, size, port4, proxy)
		wg.Done()
	}()
	go func() {
		i6, e6 = announceUDP(ctx, "udp6", f,
			url, hash, myid, want, size, port6, proxy)
		wg.Done()
	}()
	wg.Wait()
	if e4 != nil && e6 != nil {
		err = e4
	}
	interval := i4
	if interval < i6 {
		interval = i6
	}

	tracker.updateInterval(interval, err)
	return err
}

func announceUDP(ctx context.Context, prot string,
	f func(ip net.IP, port int) bool, url *nurl.URL, hash, myid []byte,
	want int, size int64, port int, prox string) (time.Duration, error) {

	var conn net.Conn
	var err error

	if prox == "" {
		var dialer net.Dialer
		conn, err = dialer.DialContext(ctx, prot,
			net.JoinHostPort(url.Hostname(), url.Port()))
	} else {
		var u *nurl.URL
		u, err = url.Parse(prox)
		if err != nil {
			return 0, err
		}
		var dialer proxy.Dialer
		dialer, err = proxy.FromURL(u, proxy.Direct)
		if err != nil {
			return 0, err
		}
		conn, err = dialer.Dial(prot,
			net.JoinHostPort(url.Hostname(), url.Port()))
	}

	if err != nil {
		return 0, err
	}

	defer conn.Close()

	w := new(bytes.Buffer)
	err = binary.Write(w, binary.BigEndian, uint64(0x41727101980))
	if err != nil {
		return 0, err
	}
	err = binary.Write(w, binary.BigEndian, uint32(0))
	if err != nil {
		return 0, err
	}

	tid := uint32(rand.Uint32())
	err = binary.Write(w, binary.BigEndian, tid)
	if err != nil {
		return 0, err
	}

	r, err := udpRequestReply(ctx, conn, w.Bytes(), 16, 0, tid)
	if err != nil {
		return 0, err
	}

	var cid uint64
	err = binary.Read(r, binary.BigEndian, &cid)
	if err != nil {
		return 0, err
	}

	w = new(bytes.Buffer)
	err = binary.Write(w, binary.BigEndian, cid)
	if err != nil {
		return 0, err
	}
	err = binary.Write(w, binary.BigEndian, uint32(1)) // action
	if err != nil {
		return 0, err
	}
	tid = uint32(rand.Uint32())
	err = binary.Write(w, binary.BigEndian, tid)
	if err != nil {
		return 0, err
	}
	_, err = w.Write(hash)
	if err != nil {
		return 0, err
	}
	_, err = w.Write(myid)
	if err != nil {
		return 0, err
	}
	err = binary.Write(w, binary.BigEndian, uint64(size/2)) // downloaded
	if err != nil {
		return 0, err
	}
	err = binary.Write(w, binary.BigEndian, int64(size/2)) // left
	if err != nil {
		return 0, err
	}
	err = binary.Write(w, binary.BigEndian, uint64(2*size)) // uploaded
	if err != nil {
		return 0, err
	}
	err = binary.Write(w, binary.BigEndian, uint32(0)) // event
	if err != nil {
		return 0, err
	}
	err = binary.Write(w, binary.BigEndian, uint32(0)) // IP
	if err != nil {
		return 0, err
	}
	err = binary.Write(w, binary.BigEndian, uint32(0)) // key
	if err != nil {
		return 0, err
	}
	err = binary.Write(w, binary.BigEndian, int32(want)) // num_want
	if err != nil {
		return 0, err
	}

	err = binary.Write(w, binary.BigEndian, uint16(port)) // port
	if err != nil {
		return 0, err
	}

	r, err = udpRequestReply(ctx, conn, w.Bytes(), 20, 1, tid)
	if err != nil {
		return 0, err
	}

	var intvl, leechers, seeders uint32
	err = binary.Read(r, binary.BigEndian, &intvl)
	if err != nil {
		return 0, err
	}
	interval := time.Duration(intvl) * time.Second

	err = binary.Read(r, binary.BigEndian, &leechers)
	if err != nil {
		return 0, err
	}
	err = binary.Read(r, binary.BigEndian, &seeders)
	if err != nil {
		return 0, err
	}

	var len int
	switch prot {
	case "udp4":
		len = 4
	case "udp6":
		len = 6
	default:
		panic("Eek")
	}
	buf := make([]byte, len+2)
	i := 0
	for {
		_, err = io.ReadFull(r, buf)
		if err != nil {
			break
		}
		ip := net.IP(make([]byte, len))
		copy(ip, buf)
		port := 256*int(buf[len]) + int(buf[len+1])
		f(ip, port)
		i++
	}

	if err == io.EOF {
		err = nil
	}

	return interval, err
}

// udpRequestReply sends a UDP request and waits for a reply.  If no reply
// is received, it resends the request with exponential backoff up to four
// times.
func udpRequestReply(ctx context.Context, conn net.Conn, request []byte,
	min int, action uint32, tid uint32) (*bytes.Reader, error) {
	var err error
	timeout := 5 * time.Second
	for i := 0; i < 4; i++ {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}

		err = conn.SetDeadline(time.Now().Add(timeout))
		if err != nil {
			return nil, err
		}
		timeout *= 2
		_, err = conn.Write(request)
		if err != nil {
			continue
		}

		if ctx.Err() != nil {
			return nil, ctx.Err()
		}

		buf := make([]byte, 4096)
		var n int
		n, err = conn.Read(buf)
		if err == nil && n < min {
			err = ErrParse
		}
		if err != nil {
			continue
		}

		r := bytes.NewReader(buf[:n])
		var a, t uint32

		err = binary.Read(r, binary.BigEndian, &a)
		if err != nil {
			continue
		}
		err = binary.Read(r, binary.BigEndian, &t)
		if err != nil {
			continue
		}

		if t != tid {
			continue
		}

		if a == 3 {
			message, err := io.ReadAll(r)
			if err != nil {
				return nil, err
			}
			return nil, errors.New(string(message))
		}

		if a != action {
			return nil, errors.New("action mismatch")
		}

		return r, nil
	}
	if err == nil {
		panic("eek")
	}
	return nil, err
}
