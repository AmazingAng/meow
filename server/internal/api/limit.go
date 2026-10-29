package api

import (
	"container/heap"
	"math"
	"sync"
	"time"
)

const maxBuckets = 100_000

// limiter is an in-memory token bucket per key. It is per-process, which is
// sufficient for the single-instance V0 deployment.
type limiter struct {
	mu      sync.Mutex
	rate    float64 // tokens per second
	burst   float64
	buckets map[string]*bucket
	expiry  bucketExpiry
	now     func() time.Time
}

type bucket struct {
	key    string
	index  int
	fullAt time.Time
	tokens float64
	last   time.Time
}

func newLimiter(rate, burst float64) *limiter {
	return &limiter{rate: rate, burst: burst, buckets: map[string]*bucket{}, now: time.Now}
}

// allow consumes a token for key, or reports how long until one is available.
func (l *limiter) allow(key string) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	b, ok := l.buckets[key]
	if !ok {
		if len(l.buckets) >= maxBuckets {
			l.evictFull(now)
			if len(l.buckets) >= maxBuckets {
				return false, max(time.Nanosecond, l.expiry[0].fullAt.Sub(now))
			}
		}
		b = &bucket{key: key, tokens: l.burst, last: now}
		l.buckets[key] = b
		heap.Push(&l.expiry, b)
	}
	b.tokens = min(l.burst, b.tokens+now.Sub(b.last).Seconds()*l.rate)
	b.last = now
	allowed := b.tokens >= 1
	if allowed {
		b.tokens--
	}
	b.fullAt = now.Add(time.Duration(math.Ceil((l.burst - b.tokens) / l.rate * float64(time.Second))))
	heap.Fix(&l.expiry, b.index)
	if allowed {
		return true, 0
	}
	return false, time.Duration((1 - b.tokens) / l.rate * float64(time.Second))
}

// evictFull drops only fully replenished buckets, with bounded work per request.
func (l *limiter) evictFull(now time.Time) {
	for n := 0; n < 64 && len(l.expiry) > 0 && !l.expiry[0].fullAt.After(now); n++ {
		b := heap.Pop(&l.expiry).(*bucket)
		delete(l.buckets, b.key)
	}
}

type bucketExpiry []*bucket

func (h bucketExpiry) Len() int           { return len(h) }
func (h bucketExpiry) Less(i, j int) bool { return h[i].fullAt.Before(h[j].fullAt) }
func (h bucketExpiry) Swap(i, j int)      { h[i], h[j] = h[j], h[i]; h[i].index = i; h[j].index = j }
func (h *bucketExpiry) Push(x any)        { b := x.(*bucket); b.index = len(*h); *h = append(*h, b) }
func (h *bucketExpiry) Pop() any {
	old := *h
	b := old[len(old)-1]
	old[len(old)-1] = nil
	*h = old[:len(old)-1]
	return b
}
