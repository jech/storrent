// Package httpclient implements caching for net/http.Client.

package httpclient

import (
	"context"
	"errors"
	"math/rand"
	"net"
	"net/http"
	"net/url"
	"sync"
	"time"
)

var ErrWrongNetwork = errors.New("wrong network")

type key struct {
	proxy, network string
}

type client struct {
	client *http.Client
	time   time.Time
}

var mu sync.Mutex
var clients = make(map[key]client)

var runExpiry sync.Once

// Get returns an http.Client that goes through the given proxy.  If
// network is not empty, it restricts connections to the given protocol
// ("tcp" or "tcp6").
func Get(network, proxy string) *http.Client {
	runExpiry.Do(func() {
		go expire()
	})

	mu.Lock()
	defer mu.Unlock()
	cl, ok := clients[key{network: network, proxy: proxy}]
	if ok {
		cl.time = time.Now()
		return cl.client
	}
	dialer := net.Dialer{
		Timeout:   30 * time.Second,
		KeepAlive: 30 * time.Second,
		DualStack: true,
	}
	transport := &http.Transport{
		Proxy: func(req *http.Request) (*url.URL, error) {
			if proxy == "" {
				return nil, nil
			}
			return url.Parse(proxy)
		},
		DialContext: func(ctx context.Context, n, a string) (net.Conn, error) {
			if n == "" || n == "tcp" {
				if network != "" && network != "tcp" {
					n = network
				}
			} else if n != network {
				return nil, ErrWrongNetwork
			}
			return dialer.DialContext(ctx, n, a)
		},
		MaxIdleConns:          30,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	}
	cl = client{
		client: &http.Client{
			Transport: transport,
			Timeout:   50 * time.Second,
		},
		time: time.Now(),
	}
	clients[key{network: network, proxy: proxy}] = cl
	return cl.client
}

func expire() {
	for {
		time.Sleep(time.Minute +
			time.Duration(rand.Int63n(int64(time.Minute))))
		now := time.Now()
		func() {
			mu.Lock()
			defer mu.Unlock()
			for k, cl := range clients {
				if now.Sub(cl.time) > 10*time.Minute {
					delete(clients, k)
				}
			}
		}()
	}
}
