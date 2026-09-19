package fetch

import (
	"context"
	"net/http"
	"testing"
	"time"
)

func TestRobotsParsingAndMatching(t *testing.T) {
	body := `
# comment line
User-agent: *
Disallow: /private/
Disallow: /tmp
Allow: /private/public/
Crawl-delay: 2

User-agent: EvilBot
Disallow: /

Sitemap: https://example.com/sitemap.xml
Sitemap: https://example.com/sitemap-news.xml
`
	r := parseRobots(body, "ReconGraph/0.1")
	r.fetched = true

	cases := map[string]bool{
		"/":                  true,
		"/public/page":       true,
		"/private/":          false,
		"/private/secret":    false,
		"/private/public/ok": true, // longer Allow beats shorter Disallow
		"/tmp":               false,
		"/tmpfile":           false, // prefix match, per the de-facto standard
		"/other":             true,
	}
	for path, want := range cases {
		if got := r.Allowed(path); got != want {
			t.Errorf("Allowed(%q) = %v, want %v", path, got, want)
		}
	}

	if r.CrawlDelay() != 2*time.Second {
		t.Errorf("CrawlDelay = %v, want 2s", r.CrawlDelay())
	}
	if len(r.Sitemaps()) != 2 {
		t.Errorf("Sitemaps = %v, want 2 (they are global, not per group)", r.Sitemaps())
	}
}

// A group naming our UA beats the wildcard group.
func TestRobotsSpecificAgentWins(t *testing.T) {
	body := `
User-agent: *
Disallow: /

User-agent: ReconGraph
Disallow: /admin/
`
	r := parseRobots(body, "ReconGraph/0.1 (+https://example)")
	r.fetched = true
	if !r.Allowed("/anything") {
		t.Error("our own group should allow /anything")
	}
	if r.Allowed("/admin/x") {
		t.Error("our own group disallows /admin/")
	}
}

func TestRobotsWildcardsAndAnchors(t *testing.T) {
	r := parseRobots("User-agent: *\nDisallow: /*.pdf$\nDisallow: /a/*/b\n", "x")
	r.fetched = true
	cases := map[string]bool{
		"/doc.pdf":     false,
		"/x/y/doc.pdf": false,
		"/doc.pdf?v=1": true, // $ anchors to end of path
		"/a/mid/b":     false,
		"/a/b":         true,
		"/other":       true,
	}
	for path, want := range cases {
		if got := r.Allowed(path); got != want {
			t.Errorf("Allowed(%q) = %v, want %v", path, got, want)
		}
	}
}

// Missing robots.txt means no restrictions.
func TestRobotsEmptyAllowsEverything(t *testing.T) {
	var r *robotsRules
	if !r.Allowed("/anything") {
		t.Error("nil rules must allow everything")
	}
	empty := &robotsRules{}
	if !empty.Allowed("/anything") {
		t.Error("empty rules must allow everything")
	}
	// "Disallow:" with no value explicitly means allow all.
	r2 := parseRobots("User-agent: *\nDisallow:\n", "x")
	r2.fetched = true
	if !r2.Allowed("/anything") {
		t.Error("empty Disallow means allow everything")
	}
}

// The limiter has to actually pace requests.
func TestLimiterPaces(t *testing.T) {
	l := NewLimiter(10, 1, 0) // 10/s, burst 1
	ctx := context.Background()

	start := time.Now()
	for i := 0; i < 4; i++ {
		if err := l.Wait(ctx, "example.com"); err != nil {
			t.Fatal(err)
		}
	}
	// Burst 1 covers the first request; the next three cost ~100ms each.
	if elapsed := time.Since(start); elapsed < 250*time.Millisecond {
		t.Errorf("4 requests at 10/s took %v, expected at least ~300ms", elapsed)
	}
}

// A slow host must not throttle a fast one.
func TestLimiterIsPerHost(t *testing.T) {
	l := NewLimiter(1, 1, 0)
	ctx := context.Background()
	start := time.Now()
	for _, host := range []string{"a.com", "b.com", "c.com", "d.com"} {
		if err := l.Wait(ctx, host); err != nil {
			t.Fatal(err)
		}
	}
	if elapsed := time.Since(start); elapsed > 200*time.Millisecond {
		t.Errorf("four different hosts took %v; the limiter is not per-host", elapsed)
	}
}

func TestLimiterRespectsContext(t *testing.T) {
	l := NewLimiter(0.1, 1, 0) // very slow
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	_ = l.Wait(ctx, "slow.com") // consumes the burst token
	start := time.Now()
	err := l.Wait(ctx, "slow.com")
	if err == nil {
		t.Error("Wait should return the context error rather than sleeping through cancellation")
	}
	if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
		t.Errorf("Wait ignored cancellation for %v", elapsed)
	}
}

func TestLimiterPenaliseHalvesRate(t *testing.T) {
	l := NewLimiter(100, 1, 0)
	before := l.bucketFor("x.com").refillRate
	l.Penalise("x.com")
	after := l.bucketFor("x.com").refillRate
	if after >= before {
		t.Errorf("Penalise did not slow the host: %v -> %v", before, after)
	}
	// And it must have a floor, so a host that 429s repeatedly does not end up
	// at an effectively infinite delay.
	for i := 0; i < 50; i++ {
		l.Penalise("x.com")
	}
	if got := l.bucketFor("x.com").refillRate; got < 0.1 {
		t.Errorf("rate fell below the floor: %v", got)
	}
}

func TestRetryAfterHeader(t *testing.T) {
	h := http.Header{}
	h.Set("Retry-After", "7")
	if got := retryAfter(h); got != 7*time.Second {
		t.Errorf("numeric Retry-After = %v, want 7s", got)
	}

	h.Set("Retry-After", time.Now().Add(3*time.Second).UTC().Format(http.TimeFormat))
	if got := retryAfter(h); got <= 0 || got > 4*time.Second {
		t.Errorf("HTTP-date Retry-After = %v, want ~3s", got)
	}

	h.Set("Retry-After", "nonsense")
	if got := retryAfter(h); got != 0 {
		t.Errorf("unparseable Retry-After = %v, want 0", got)
	}

	if got := retryAfter(http.Header{}); got != 0 {
		t.Errorf("absent Retry-After = %v, want 0", got)
	}
}

func TestRetryableStatus(t *testing.T) {
	retry := []int{429, 408, 500, 502, 503, 504}
	noRetry := []int{200, 301, 400, 401, 403, 404, 410, 501}
	for _, c := range retry {
		if !retryableStatus(c) {
			t.Errorf("%d should be retryable", c)
		}
	}
	for _, c := range noRetry {
		if retryableStatus(c) {
			t.Errorf("%d should NOT be retryable", c)
		}
	}
}

// Without jitter, workers backing off together retry together.
func TestBackoffIsJitteredAndCapped(t *testing.T) {
	seen := map[time.Duration]bool{}
	for i := 0; i < 50; i++ {
		seen[backoff(3)] = true
	}
	if len(seen) < 5 {
		t.Errorf("backoff produced only %d distinct values; jitter is not working", len(seen))
	}
	for i := 1; i <= 20; i++ {
		if d := backoff(i); d > 15*time.Second {
			t.Errorf("backoff(%d) = %v, exceeds the cap", i, d)
		}
	}
}

func TestNewClientRejectsBadProxy(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Proxy = "://not a url"
	if _, err := New(cfg); err == nil {
		t.Error("expected an error for an invalid proxy URL")
	}
}

func TestContentHashStableAndEmpty(t *testing.T) {
	a := &Response{Body: []byte("hello")}
	b := &Response{Body: []byte("hello")}
	c := &Response{Body: []byte("world")}
	if a.ContentHash() != b.ContentHash() {
		t.Error("identical bodies must hash the same")
	}
	if a.ContentHash() == c.ContentHash() {
		t.Error("different bodies must hash differently")
	}
	if (&Response{}).ContentHash() != "" {
		t.Error("empty body must hash to the empty string")
	}
}
