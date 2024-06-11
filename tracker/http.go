package tracker

import (
	"context"
	"errors"
	"net/http"
	"net/netip"
	nurl "net/url"
	"strconv"
	"sync"
	"time"

	"github.com/zeebo/bencode"

	"github.com/jech/storrent/httpclient"
)

// HTTP represents a tracker accessed over HTTP or HTTPS.
type HTTP struct {
	base
}

// httpReply is an HTTP tracker's reply.
type httpReply struct {
	FailureReason string             `bencode:"failure reason"`
	RetryIn       string             `bencode:"retry in"`
	Interval      int                `bencode:"interval"`
	Peers         bencode.RawMessage `bencode:"peers,omitempty"`
	Peers6        []byte             `bencode:"peers6,omitempty"`
}

// peer is a peer returned by a tracker.
type peer struct {
	IP   string `bencode:"ip"`
	Port uint16 `bencode:"port"`
}

// Announce performs an HTTP announce over both IPv4 and IPv6 in parallel.
func (tracker *HTTP) Announce(ctx context.Context, hash []byte, myid []byte,
	want int, size int64, port4, port6 int, proxy string,
	f func(netip.AddrPort) bool) error {

	ok := tracker.tryLock()
	if !ok {
		return ErrNotReady
	}
	defer tracker.unlock()

	if !tracker.ready() {
		return ErrNotReady
	}

	tracker.time = time.Now()

	var interval int
	var err error

	if proxy != "" {
		port := port6
		if port == 0 {
			port = port4
		}
		interval, err = announceHTTP(
			ctx, "", tracker, hash, myid, want, size, port, proxy, f,
		)
	} else {
		var i4, i6 int
		var e4, e6 error
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			i4, e4 = announceHTTP(
				ctx, "tcp4", tracker, hash, myid,
				want, size, port4, proxy, f,
			)
			wg.Done()
		}()
		go func() {
			i6, e6 = announceHTTP(
				ctx, "tcp6", tracker, hash, myid,
				want, size, port6, proxy, f,
			)
			wg.Done()
		}()
		wg.Wait()
		if e4 != nil && e6 != nil {
			err = e4
		}
		interval = i4
		if interval < i6 {
			interval = i6
		}
	}

	tracker.updateInterval(time.Duration(interval)*time.Second, err)
	return err
}

// announceHTTP performs a single HTTP announce
func announceHTTP(ctx context.Context, protocol string, tracker *HTTP,
	hash []byte, myid []byte, want int, size int64, port int, proxy string,
	f func(netip.AddrPort) bool) (int, error) {
	url, err := nurl.Parse(tracker.url)
	if err != nil {
		return 0, err
	}

	v := nurl.Values{}
	v.Set("info_hash", string(hash))
	v.Set("peer_id", string(myid))
	v.Set("numwant", strconv.Itoa(want))
	if port > 0 {
		v.Set("port", strconv.Itoa(port))
	}
	v.Set("downloaded", strconv.FormatInt(size/2, 10))
	v.Set("uploaded", strconv.FormatInt(2*size, 10))
	v.Set("left", strconv.FormatInt(size/2, 10))
	v.Set("compact", "1")
	url.RawQuery = v.Encode()

	req, err := http.NewRequest("GET", url.String(), nil)
	if err != nil {
		return 0, err
	}
	req.Close = true
	req.Header.Set("Cache-Control", "max-age=0")
	req.Header["User-Agent"] = nil

	client := httpclient.Get(protocol, proxy)
	if client == nil {
		return 0, errors.New("couldn't get HTTP client")
	}

	r, err := client.Do(req.WithContext(ctx))
	if err != nil {
		return 0, err
	}

	defer r.Body.Close()

	if r.StatusCode != 200 {
		return 0, errors.New(r.Status)
	}

	decoder := bencode.NewDecoder(r.Body)
	var reply httpReply
	err = decoder.Decode(&reply)
	if err != nil {
		return 0, err
	}

	if reply.FailureReason != "" {
		retry := time.Duration(0)
		if reply.RetryIn == "never" {
			retry = time.Duration(time.Hour * 2400)
		} else if reply.RetryIn != "" {
			min, err := strconv.Atoi(reply.RetryIn)
			if err == nil && min > 0 {
				retry = time.Duration(min) * time.Minute
			}
		}
		tracker.interval = retry
		err = errors.New(reply.FailureReason)
		return 0, err
	}

	var peers []byte
	err = bencode.DecodeBytes(reply.Peers, &peers)
	if err == nil && len(peers)%6 == 0 {
		// compact format
		for i := 0; i < len(peers); i += 6 {
			ip, ok := netip.AddrFromSlice(peers[i : i+4])
			if ok {
				port := 256*uint16(peers[i+4]) +
					uint16(peers[i+5])
				f(netip.AddrPortFrom(ip, port))
			}
		}
	} else {
		// original format
		var peers []peer
		err = bencode.DecodeBytes(reply.Peers, &peers)
		if err == nil {
			for _, p := range peers {
				ip, err := netip.ParseAddr(p.IP)
				if err == nil {
					f(netip.AddrPortFrom(ip, p.Port))
				}
			}
		}
	}

	if len(reply.Peers6)%18 == 0 {
		// peers6 is always in compact format
		for i := 0; i < len(reply.Peers6); i += 18 {
			ip, ok := netip.AddrFromSlice(reply.Peers6[i : i+16])
			if ok {
				port := 256*uint16(reply.Peers6[i+16]) +
					uint16(reply.Peers6[i+17])
				f(netip.AddrPortFrom(ip, port))
			}
		}
	}
	return reply.Interval, nil
}
