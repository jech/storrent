package tracker

import (
	"context"
	"errors"
	"fmt"
	"net"
	nurl "net/url"
	"sync/atomic"
	"time"
)

var (
	ErrNotReady = errors.New("tracker not ready")
	ErrParse    = errors.New("couldn't parse tracker reply")
)

type base struct {
	url      string
	time     time.Time
	interval time.Duration
	err      error
	locked   int32
}

type Tracker interface {
	URL() string
	GetState() (State, error)
	Announce(ctx context.Context, hash []byte, myid []byte,
		want int, size int64, port4, port6 int, proxy string,
		f func(net.IP, int) bool) error
	Kill() error
}

func New(url string) Tracker {
	u, err := nurl.Parse(url)
	if err != nil {
		return nil
	}
	switch u.Scheme {
	case "http", "https":
		return &HTTP{base: base{url: url}}
	case "udp":
		return &UDP{base: base{url: url}}
	default:
		return &Unknown{base: base{url: url}}
	}
}

func (tracker *base) URL() string {
	return tracker.url
}

func (tracker *base) tryLock() bool {
	return atomic.CompareAndSwapInt32(&tracker.locked, 0, 1)
}

func (tracker *base) unlock() {
	ok := atomic.CompareAndSwapInt32(&tracker.locked, 1, 0)
	if !ok {
		panic("unlocking unlocked torrent")
	}
}

func (tracker *base) ready() bool {
	interval := tracker.interval
	if interval <= 0 {
		interval = 30 * time.Minute
	}
	if interval < 5*time.Minute {
		interval = 5 * time.Minute
	}
	return tracker.time.Add(interval).Before(time.Now())
}

type State int

const (
	Disabled State = -3
	Error    State = -2
	Busy     State = -1
	Idle     State = 0
	Ready    State = 1
)

func (state State) String() string {
	switch state {
	case Disabled:
		return "disabled"
	case Error:
		return "error"
	case Busy:
		return "busy"
	case Idle:
		return "idle"
	case Ready:
		return "ready"
	default:
		return fmt.Sprintf("unknown state %d", int(state))
	}
}

func (tracker *base) GetState() (State, error) {
	ok := tracker.tryLock()
	if !ok {
		return Busy, nil
	}
	ready := tracker.ready()
	tracker.unlock()
	if ready {
		return Ready, nil
	}
	if tracker.err != nil {
		return Error, tracker.err
	}
	return Idle, nil
}

func (tracker *base) updateInterval(interval time.Duration, err error) {
	tracker.err = err
	if interval > time.Minute {
		tracker.interval = interval
	} else if tracker.interval < 15*time.Minute {
		tracker.interval = 15 * time.Minute
	}
}

func (tracker *base) Announce(ctx context.Context, hash []byte, myid []byte,
	want int, size int64, port4, port6 int, proxy string,
	f func(net.IP, int) bool) error {
	return errors.New("not implemented")
}

func (tracker *base) Kill() error {
	return nil
}

type Unknown struct {
	base
}

func (tracker *Unknown) GetState() (State, error) {
	return Disabled, nil
}
