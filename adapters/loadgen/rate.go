// Package loadgen holds pieces shared by the load generator adapters (command, http, kv, tool).
package loadgen

import (
	"math"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/goxang/rig/core"
)

// Rate is a target requests/s that SetRate may change while the generator keeps running,
// with no restart needed: callers just re-read it (e.g. on their next tick or Status call).
type Rate struct{ bits atomic.Uint64 }

func (r *Rate) Set(rps float64) { r.bits.Store(math.Float64bits(rps)) }
func (r *Rate) Get() float64    { return math.Float64frombits(r.bits.Load()) }

// Reservoir keeps the most recent latencies for percentiles.
type Reservoir struct {
	mu   sync.Mutex
	buf  []time.Duration
	next int
	full bool
}

func NewReservoir(n int) *Reservoir { return &Reservoir{buf: make([]time.Duration, n)} }

func (r *Reservoir) Add(d time.Duration) {
	r.mu.Lock()
	r.buf[r.next] = d
	r.next = (r.next + 1) % len(r.buf)
	if r.next == 0 {
		r.full = true
	}
	r.mu.Unlock()
}

func (r *Reservoir) Percentiles() core.Latency {
	r.mu.Lock()
	n := r.next
	if r.full {
		n = len(r.buf)
	}
	s := append([]time.Duration(nil), r.buf[:n]...)
	r.mu.Unlock()
	if len(s) == 0 {
		return core.Latency{}
	}
	slices.Sort(s)
	at := func(p float64) time.Duration { return s[min(len(s)-1, int(p*float64(len(s))))] }
	return core.Latency{P50: at(.50), P95: at(.95), P99: at(.99)}
}
