// Package ringbuf provides a fixed-size circular buffer of float64 samples
// with associated timestamps.
package ringbuf

import (
	"sync"
	"time"
)

// Sample is a single timestamped value.
type Sample struct {
	Ts    int64   // Unix timestamp seconds
	Value float64 // metric value; 0 means "not yet collected"
}

// Ring is a thread-safe circular buffer.
type Ring struct {
	mu   sync.RWMutex
	buf  []Sample
	size int
	head int // next write position
	fill int // how many slots are filled
}

// New creates a Ring that holds exactly size samples.
func New(size int) *Ring {
	return &Ring{
		buf:  make([]Sample, size),
		size: size,
	}
}

// Push appends a new sample, overwriting the oldest entry when full.
func (r *Ring) Push(ts time.Time, val float64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.buf[r.head] = Sample{Ts: ts.Unix(), Value: val}
	r.head = (r.head + 1) % r.size
	if r.fill < r.size {
		r.fill++
	}
}

// Fill returns the number of slots that have been written at least once.
func (r *Ring) Fill() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.fill
}

// Snapshot returns all current samples in chronological order (oldest first).
// Unfilled slots are returned as zero-value Samples (Ts=0, Value=0).
func (r *Ring) Snapshot() []Sample {
	r.mu.RLock()
	defer r.mu.RUnlock()

	out := make([]Sample, r.size)
	if r.fill < r.size {
		// buffer not yet full: slots [0..fill-1] exist starting at oldest=0
		// head points to next write which equals fill
		// oldest data starts at 0, head is also 0 when empty
		// actually: we always write at head, head advances; oldest is head when full
		// when not full, oldest is 0 (positions 0..fill-1 are valid)
		for i := 0; i < r.size; i++ {
			out[i] = r.buf[i] // unfilled slots have zero Sample value
		}
	} else {
		// full: oldest is at r.head
		for i := 0; i < r.size; i++ {
			out[i] = r.buf[(r.head+i)%r.size]
		}
	}
	return out
}
