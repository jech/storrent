// Package tracker implements the HTTP and UDP BitTorrent tracker protocols,
// as defined in BEP-3, BEP-7, BEP-23 and BEP-15.
package tracker

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	nurl "net/url"
	"sync/atomic"
	"time"
)

var (
	ErrNotReady = errors.New("tracker not ready")
	ErrParse    = errors.New("couldn't parse tracker reply")
)

// Type base implements the tracker lifetime.
type base struct {
	url      string
	time     time.Time
	interval time.Duration
	err      error
	locked   int32
}

// Type Tracker represents a BitTorrent tracker.
type Tracker interface {
	// URL returns the tracker URL
	URL() string
	// GetState returns the tracker's state.  If the state is Error,
	// then GetState also returns an error value.
	GetState() (State, error)
	// Announce performs parallel announces over both IPv4 and IPv6.
	Announce(ctx context.Context, hash []byte, myid []byte,
		want int, size int64, port4, port6 int, proxy string,
		f func(netip.AddrPort) bool) error
}

// New creates a new tracker from a URL.
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

// Type State represtents the state of a tracker.
type State int

const (
	Disabled State = -3 // the tracker is disabled
	Error    State = -2 // last request failed
	Busy     State = -1 // a request is in progress
	Idle     State = 0  // it's not time yet for a new request
	Ready    State = 1  // a request may be scheduled now
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

// GetState returns a tracker's state.
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

// updateInterval updates a tracker's announce interval.
func (tracker *base) updateInterval(interval time.Duration, err error) {
	tracker.err = err
	if interval > time.Minute {
		tracker.interval = interval
	} else if tracker.interval < 15*time.Minute {
		tracker.interval = 15 * time.Minute
	}
}

// Announce performs parallel announces over both IPv4 and IPv6.
func (tracker *base) Announce(ctx context.Context, hash []byte, myid []byte,
	want int, size int64, port4, port6 int, proxy string,
	f func(netip.AddrPort) bool) error {
	return errors.New("not implemented")
}

// Unknown represents a tracker with an unknown scheme.
type Unknown struct {
	base
}

// GetState for an tracker with an unknown scheme always returns Disabled.
func (tracker *Unknown) GetState() (State, error) {
	return Disabled, nil
}
