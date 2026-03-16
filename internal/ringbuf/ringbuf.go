// Package ringbuf provides a fixed-size circular buffer of float64 samples
// with associated timestamps.
package ringbuf

import (
	"sync"
	"time"
)

type Sample struct {
	Ts    int64
	Value float64
}

type Ring struct {
	mu   sync.RWMutex
	buf  []Sample
	size int
	head int
	fill int
}

func New(size int) *Ring {
	return &Ring{
		buf:  make([]Sample, size),
		size: size,
	}
}

func (r *Ring) Push(ts time.Time, val float64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.buf[r.head] = Sample{Ts: ts.Unix(), Value: val}
	r.head = (r.head + 1) % r.size
	if r.fill < r.size {
		r.fill++
	}
}

func (r *Ring) Fill() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.fill
}

func (r *Ring) Snapshot() []Sample {
	r.mu.RLock()
	defer r.mu.RUnlock()

	out := make([]Sample, r.size)
	if r.fill < r.size {
		for i := 0; i < r.size; i++ {
			out[i] = r.buf[i]
		}
	} else {
		for i := 0; i < r.size; i++ {
			out[i] = r.buf[(r.head+i)%r.size]
		}
	}
	return out
}
