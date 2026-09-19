package fetch

import (
	"context"
	"math/rand"
	"sync"
	"time"
)

// A token bucket, one per host.
//
// Worker count and rate are separate knobs on purpose: workers bound our own
// resource use, the limiter bounds how hard we lean on one server. With only a
// worker count you can't crawl fifty hosts quickly while staying gentle on
// each.
type bucket struct {
	mu         sync.Mutex
	tokens     float64
	max        float64
	refillRate float64 // tokens per second
	last       time.Time
	// crawlDelay comes from robots.txt and wins if it is stricter.
	crawlDelay time.Duration
}

func newBucket(rps float64, burst int) *bucket {
	if rps <= 0 {
		rps = 1
	}
	if burst < 1 {
		burst = 1
	}
	return &bucket{
		tokens:     float64(burst),
		max:        float64(burst),
		refillRate: rps,
		last:       time.Now(),
	}
}

// reserve returns how long the caller must wait before its request may go out.
func (b *bucket) reserve() time.Duration {
	b.mu.Lock()
	defer b.mu.Unlock()

	now := time.Now()
	elapsed := now.Sub(b.last).Seconds()
	b.last = now
	b.tokens += elapsed * b.refillRate
	if b.tokens > b.max {
		b.tokens = b.max
	}

	// Consume a token even into debt, then wait for the debt to clear.
	// Sleeping without decrementing lets the next caller find the token already
	// refilled and go straight through, which doubles the sustained rate.
	b.tokens--

	var wait time.Duration
	if b.tokens < 0 {
		wait = time.Duration(-b.tokens / b.refillRate * float64(time.Second))
	}
	if b.crawlDelay > wait {
		return b.crawlDelay
	}
	return wait
}

func (b *bucket) setCrawlDelay(d time.Duration) {
	b.mu.Lock()
	b.crawlDelay = d
	b.mu.Unlock()
}

// Limiter holds one bucket per host.
type Limiter struct {
	mu      sync.RWMutex
	buckets map[string]*bucket
	rps     float64
	burst   int
	jitter  time.Duration
}

// NewLimiter creates a per-host limiter. rps is requests per second per host.
func NewLimiter(rps float64, burst int, jitter time.Duration) *Limiter {
	return &Limiter{
		buckets: make(map[string]*bucket),
		rps:     rps,
		burst:   burst,
		jitter:  jitter,
	}
}

func (l *Limiter) bucketFor(host string) *bucket {
	l.mu.RLock()
	b, ok := l.buckets[host]
	l.mu.RUnlock()
	if ok {
		return b
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if b, ok := l.buckets[host]; ok {
		return b
	}
	b = newBucket(l.rps, l.burst)
	l.buckets[host] = b
	return b
}

// Wait blocks until the host's budget allows another request, or ctx is done.
func (l *Limiter) Wait(ctx context.Context, host string) error {
	d := l.bucketFor(host).reserve()
	if l.jitter > 0 {
		d += time.Duration(rand.Int63n(int64(l.jitter) + 1))
	}
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// SetCrawlDelay applies a robots.txt Crawl-delay to a host.
func (l *Limiter) SetCrawlDelay(host string, d time.Duration) {
	l.bucketFor(host).setCrawlDelay(d)
}

// Penalise halves a host's rate after it pushes back (429/503), with a floor.
// Not restored for the rest of the crawl.
func (l *Limiter) Penalise(host string) {
	b := l.bucketFor(host)
	b.mu.Lock()
	b.refillRate /= 2
	if b.refillRate < 0.1 {
		b.refillRate = 0.1
	}
	b.tokens = 0
	b.mu.Unlock()
}
