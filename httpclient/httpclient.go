package httpclient

import (
	"math/rand"
	"net"
	"net/http"
	"net/url"
	"sync"
	"time"
)

type client struct {
	client *http.Client
	time   time.Time
}

var mu sync.Mutex
var clients = make(map[string]client)

var runExpiry sync.Once

func Get(proxy string) *http.Client {
	runExpiry.Do(func() {
		go expire()
	})

	mu.Lock()
	defer mu.Unlock()
	cl, ok := clients[proxy]
	if ok {
		cl.time = time.Now()
		return cl.client
	}
	transport := &http.Transport{
		Proxy: func(req *http.Request) (*url.URL, error) {
			if proxy == "" {
				return nil, nil
			}
			return url.Parse(proxy)
		},
		DialContext: (&net.Dialer{
			Timeout:   30 * time.Second,
			KeepAlive: 30 * time.Second,
			DualStack: true,
		}).DialContext,
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
	clients[proxy] = cl
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
