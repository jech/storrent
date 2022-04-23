package webseed

import (
	nurl "net/url"
	"sync"
	"time"

	"github.com/jech/storrent/rate"
)

type Webseed interface {
	URL() string
	Ready(idle bool) bool
	Rate() float64
	Count() int
}

type base struct {
	url string

	mu     sync.Mutex
	count  int
	errors int
	time   time.Time
	e      rate.Estimator
}

func (ws *base) URL() string {
	return ws.url
}

func (ws *base) start() {
	ws.mu.Lock()
	defer ws.mu.Unlock()
	ws.count++
	if ws.count < 1 {
		panic("Eek")
	}
	if ws.count == 1 {
		ws.e.Start()
	}
	ws.time = time.Now()
}

func (ws *base) stop() {
	ws.mu.Lock()
	defer ws.mu.Unlock()
	ws.count--
	if ws.count < 0 {
		panic("Eek")
	}
	if ws.count == 0 {
		ws.e.Stop()
	}
}

func New(url string, getright bool) Webseed {
	u, err := nurl.Parse(url)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return nil
	}
	if getright {
		ws := &GetRight{base{url: url}}
		ws.e.Init(10 * time.Second)
		return ws
	} else {
		ws := &Hoffman{base{url: url}}
		ws.e.Init(10 * time.Second)
		return ws
	}
}

func (ws *base) error(e bool) {
	ws.mu.Lock()
	if e {
		ws.errors++
	} else {
		ws.errors = 0
	}
	ws.mu.Unlock()
}

func (ws *base) Accumulate(value int) {
	ws.mu.Lock()
	ws.e.Accumulate(value)
	ws.mu.Unlock()
}

func (ws *base) Rate() float64 {
	ws.mu.Lock()
	v := ws.e.Estimate()
	ws.mu.Unlock()
	return v
}

func (ws *base) Ready(idle bool) bool {
	ws.mu.Lock()
	defer ws.mu.Unlock()
	if ws.errors >= 2 {
		t := 10 * time.Second * time.Duration(1<<uint(ws.errors))
		if time.Since(ws.time) < t {
			return false
		}
	}
	rate := ws.e.Estimate()
	max := 4
	if idle {
		max = 1
	} else if rate < 100*1024 {
		max = 1
	} else if rate < 500*1024 {
		max = 2
	}
	if ws.count >= max {
		return false
	}
	return true
}

func (ws *base) Count() int {
	ws.mu.Lock()
	v := ws.count
	ws.mu.Unlock()
	return v
}
