// Package rate implements a rate estimator.
package rate

import (
	"math"
	"sync"
	"time"
)

// Estimator is a rate estimator using exponential decay.  It is not
// thread-safe.
type Estimator struct {
	interval time.Duration
	seconds  float64
	value    float64
	base     float64
	time     time.Time
	running  bool
}

// Init initialises a rate estimator with the given time constant.
func (e *Estimator) Init(interval time.Duration) {
	e.interval = interval
	e.time = time.Now()
	e.seconds = float64(interval) / float64(time.Second)
	e.base = -1.0 / e.seconds
}

// Start starts a rate estimator.
func (e *Estimator) Start() {
	if !e.running {
		e.time = time.Now()
		e.running = true
	}
}

// Stop stops a rate estimator.
func (e *Estimator) Stop() {
	e.running = false
}

// Time returns the time at which the estimator was advanced.
func (e *Estimator) Time() time.Time {
	return e.time
}

func (e *Estimator) advance(now time.Time) {
	if !e.running {
		panic("Cannot advance stopped rate estimator")
	}
	delay := now.Sub(e.time)
	e.time = now
	if delay <= time.Duration(0) {
		return
	}
	seconds := float64(delay) * (1 / float64(time.Second))
	e.value = e.value * math.Exp(e.base*seconds)
}

func (e *Estimator) accumulate(value int, now time.Time) {
	if !e.running {
		return
	}
	e.value += float64(value)
	if e.value < 0 {
		e.value = 0
	}
}

func (e *Estimator) rate(value float64) float64 {
	return float64(value) / e.seconds
}

// Estimate returns an estimate of the current rate.
func (e *Estimator) Estimate() float64 {
	if e.running {
		e.advance(time.Now())
	}
	return e.rate(e.value)
}

// Accumulate notifies the estimator that the given number of bytes has
// been sent or received.
func (e *Estimator) Accumulate(value int) {
	if e.running {
		now := time.Now()
		e.advance(now)
		e.accumulate(value, now)
	}
}

// Allow returns true if sending or receiving the given number of bytes
// would not exceed the given target.
func (e *Estimator) Allow(value int, target float64) bool {
	if (e.value+float64(value)) <= target*e.seconds {
		return true
	}
	if e.running {
		e.advance(time.Now())
		if (e.value + float64(value)) <= target*e.seconds {
			return true
		}
	}
	return false
}

// AtomicEstimator is a thread-save rate estimator.
type AtomicEstimator struct {
	sync.Mutex
	e Estimator
}

func (e *AtomicEstimator) Init(interval time.Duration) {
	e.Lock()
	e.e.Init(interval)
	e.Unlock()
}

func (e *AtomicEstimator) Start() {
	e.Lock()
	e.e.Start()
	e.Unlock()
}

func (e *AtomicEstimator) Stop() {
	e.Lock()
	e.e.Stop()
	e.Unlock()
}

func (e *AtomicEstimator) Time() time.Time {
	e.Lock()
	v := e.e.Time()
	e.Unlock()
	return v
}

func (e *AtomicEstimator) Estimate() float64 {
	e.Lock()
	v := e.e.Estimate()
	e.Unlock()
	return v
}

func (e *AtomicEstimator) Accumulate(value int) {
	e.Lock()
	e.e.Accumulate(value)
	e.Unlock()
}

func (e *AtomicEstimator) Allow(value int, target float64) bool {
	e.Lock()
	v := e.e.Allow(value, target)
	e.Unlock()
	return v
}
