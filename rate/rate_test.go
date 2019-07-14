package rate

import (
	"testing"
	"time"
)

func TestEstimator(t *testing.T) {
	e := &Estimator{}
	e.Init(10 * time.Second)
	e.Start()

	t1 := e.time.Add(2 * time.Second)
	t2 := e.time.Add(5 * time.Second)

	e.accumulate(10, t1)

	ok := func(v float64) bool {
		return v > 0.5 && v <= 1.0
	}

	r1 := e.rate(e.value)
	if !ok(r1) {
		t.Errorf("Got %v", r1)
	}

	e.advance(t2)
	r2 := e.rate(e.value)
	if !ok(r2) {
		t.Errorf("Got %v", r2)
	}
	if r1 <= r2 {
		t.Errorf("Got %v then %v", r1, r2)
	}
}

func BenchmarkNow(b *testing.B) {
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			time.Now()
		}
	})
}

func BenchmarkAdvance(b *testing.B) {
	e := &Estimator{}
	e.Init(10 * time.Second)
	e.Start()
	now := time.Now()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		now = now.Add(time.Microsecond)
		e.advance(now)
	}
}

func BenchmarkAdvanceNow(b *testing.B) {
	e := &Estimator{}
	e.Init(10 * time.Second)
	e.Start()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		e.advance(time.Now())
	}
}

func BenchmarkAccumulate(b *testing.B) {
	e := &Estimator{}
	e.Init(10 * time.Second)
	e.Start()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		e.Accumulate(42)
	}
}

func BenchmarkAccumulateAtomic(b *testing.B) {
	e := &AtomicEstimator{}
	e.Init(10 * time.Second)
	e.Start()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			e.Accumulate(42)
		}
	})
}

func BenchmarkAllow(b *testing.B) {
	e := &Estimator{}
	e.Init(10 * time.Second)
	e.Start()
	e.Accumulate(10000)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if e.Allow(10000, 100.0) {
			b.Errorf("Eek, value=%v", e.value)
		}
	}
}

func BenchmarkAllowAtomic(b *testing.B) {
	e := &AtomicEstimator{}
	e.Init(10 * time.Second)
	e.Start()
	e.Accumulate(10000)
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			if e.Allow(10000, 100.0) {
				b.Errorf("Eek, value=%v", e.e.value)
			}
		}
	})
}

func BenchmarkAllowAccumulate(b *testing.B) {
	e := &Estimator{}
	e.Init(10 * time.Second)
	e.Start()
	e.Accumulate(10000)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if e.Allow(100, 10000.0) {
			e.Accumulate(100)
		} else {
			e.Accumulate(50)
		}
	}
}

func BenchmarkAllowAccumulateAtomic(b *testing.B) {
	e := &AtomicEstimator{}
	e.Init(10 * time.Second)
	e.Start()
	e.Accumulate(10000)
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			if e.Allow(100, 10000.0) {
				e.Accumulate(100)
			} else {
				e.Accumulate(50)
			}
		}
	})
}

