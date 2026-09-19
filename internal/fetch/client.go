// Package fetch is the HTTP layer: one shared client, per-host rate limiting,
// bounded retries, redirect tracking, robots.txt.
package fetch

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// DefaultUserAgent identifies the crawler. Claiming to respect robots.txt
// while pretending to be Firefox doesn't add up.
const DefaultUserAgent = "ReconGraph/0.1 (+https://github.com/swapnil5053/recongraph)"

// Config configures the client.
type Config struct {
	UserAgent      string
	Headers        map[string]string
	Timeout        time.Duration // per request
	MaxBodyBytes   int64
	MaxRetries     int
	RateLimit      float64 // requests per second per host
	Burst          int
	Jitter         time.Duration
	MaxRedirects   int
	Proxy          string
	Insecure       bool
	RespectRobots  bool
	FollowRedirect bool
}

// DefaultConfig returns polite defaults: 2 req/s per host maps a site in
// minutes and is low enough that the target shouldn't notice.
func DefaultConfig() Config {
	return Config{
		UserAgent:      DefaultUserAgent,
		Timeout:        15 * time.Second,
		MaxBodyBytes:   5 << 20, // 5 MiB
		MaxRetries:     2,
		RateLimit:      2,
		Burst:          4,
		Jitter:         100 * time.Millisecond,
		MaxRedirects:   5,
		RespectRobots:  true,
		FollowRedirect: true,
	}
}

// Response is one fetched resource.
type Response struct {
	URL         string // final URL after redirects, canonicalised by the caller
	RequestURL  string // URL originally requested
	StatusCode  int
	Header      http.Header
	Body        []byte
	ContentType string
	Elapsed     time.Duration
	Redirects   []string // intermediate Location targets, in order
	Truncated   bool
	Attempts    int
}

// ContentHash lets diff spot "same URL, different content" without keeping the
// body around.
func (r *Response) ContentHash() string {
	if len(r.Body) == 0 {
		return ""
	}
	sum := sha256.Sum256(r.Body)
	return hex.EncodeToString(sum[:8])
}

// ErrDisallowed is returned when robots.txt forbids a URL.
var ErrDisallowed = errors.New("disallowed by robots.txt")

// Client is a rate-limited, retrying HTTP client, safe for concurrent use.
// One client and one connection pool for the whole crawl.
type Client struct {
	cfg     Config
	http    *http.Client
	limiter *Limiter
	robots  *RobotsCache
}

// New builds a Client.
func New(cfg Config) (*Client, error) {
	if cfg.UserAgent == "" {
		cfg.UserAgent = DefaultUserAgent
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 15 * time.Second
	}
	if cfg.MaxBodyBytes <= 0 {
		cfg.MaxBodyBytes = 5 << 20
	}
	if cfg.RateLimit <= 0 {
		cfg.RateLimit = 2
	}
	if cfg.Burst <= 0 {
		cfg.Burst = 1
	}

	tr := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   10 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		MaxIdleConns:          200,
		MaxIdleConnsPerHost:   16,
		IdleConnTimeout:       60 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		ForceAttemptHTTP2:     true,
		TLSClientConfig:       &tls.Config{InsecureSkipVerify: cfg.Insecure}, //nolint:gosec // opt-in via --insecure
	}
	if cfg.Proxy != "" {
		pu, err := url.Parse(cfg.Proxy)
		if err != nil {
			return nil, fmt.Errorf("invalid proxy URL %q: %w", cfg.Proxy, err)
		}
		tr.Proxy = http.ProxyURL(pu)
	}

	c := &Client{
		cfg:     cfg,
		limiter: NewLimiter(cfg.RateLimit, cfg.Burst, cfg.Jitter),
	}
	c.http = &http.Client{
		Transport: tr,
		Timeout:   cfg.Timeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if !cfg.FollowRedirect {
				return http.ErrUseLastResponse
			}
			if len(via) >= cfg.MaxRedirects {
				return fmt.Errorf("stopped after %d redirects", cfg.MaxRedirects)
			}
			return nil
		},
	}
	c.robots = NewRobotsCache(cfg.UserAgent, c.getOnce)
	return c, nil
}

// Limiter exposes the rate limiter so the crawler can apply robots crawl-delay.
func (c *Client) Limiter() *Limiter { return c.limiter }

// Allowed reports whether robots.txt permits this URL. True when robots
// support is off.
func (c *Client) Allowed(ctx context.Context, u *url.URL) bool {
	if !c.cfg.RespectRobots {
		return true
	}
	rules := c.robots.Rules(ctx, u)
	if d := rules.CrawlDelay(); d > 0 {
		c.limiter.SetCrawlDelay(u.Host, d)
	}
	return rules.Allowed(u.EscapedPath())
}

// Sitemaps returns sitemap URLs declared in a host's robots.txt.
func (c *Client) Sitemaps(ctx context.Context, u *url.URL) []string {
	return c.robots.Rules(ctx, u).Sitemaps()
}

// Get fetches a URL with rate limiting and bounded retries over transport
// errors, 429 and 5xx. A 429 or 503 also halves that host's rate for the rest
// of the crawl.
func (c *Client) Get(ctx context.Context, rawurl string) (*Response, error) {
	u, err := url.Parse(rawurl)
	if err != nil {
		return nil, err
	}
	if c.cfg.RespectRobots && !c.Allowed(ctx, u) {
		return nil, ErrDisallowed
	}

	var lastErr error
	attempts := c.cfg.MaxRetries + 1
	for attempt := 1; attempt <= attempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if err := c.limiter.Wait(ctx, u.Host); err != nil {
			return nil, err
		}

		resp, err := c.getOnce(ctx, rawurl)
		if err == nil {
			resp.Attempts = attempt
			if !retryableStatus(resp.StatusCode) || attempt == attempts {
				return resp, nil
			}
			// Pushed back: slow the host down, then wait.
			c.limiter.Penalise(u.Host)
			wait := retryAfter(resp.Header)
			if wait <= 0 {
				wait = backoff(attempt)
			}
			if err := sleepCtx(ctx, wait); err != nil {
				return nil, err
			}
			lastErr = fmt.Errorf("status %d", resp.StatusCode)
			continue
		}

		lastErr = err
		if ctx.Err() != nil || !retryableError(err) || attempt == attempts {
			return nil, err
		}
		if err := sleepCtx(ctx, backoff(attempt)); err != nil {
			return nil, err
		}
	}
	return nil, lastErr
}

// getOnce does a single request, no retry, no rate limiting. Also the fetcher
// for robots.txt itself, which must not recurse through Allowed().
func (c *Client) getOnce(ctx context.Context, rawurl string) (*Response, error) {
	start := time.Now()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawurl, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", c.cfg.UserAgent)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	for k, v := range c.cfg.Headers {
		req.Header.Set(k, v)
	}

	var redirects []string
	trace := &http.Client{
		Transport: c.http.Transport,
		Timeout:   c.http.Timeout,
		CheckRedirect: func(r *http.Request, via []*http.Request) error {
			redirects = append(redirects, r.URL.String())
			if !c.cfg.FollowRedirect {
				return http.ErrUseLastResponse
			}
			if len(via) >= c.cfg.MaxRedirects {
				return fmt.Errorf("stopped after %d redirects", c.cfg.MaxRedirects)
			}
			for k, v := range c.cfg.Headers {
				r.Header.Set(k, v)
			}
			r.Header.Set("User-Agent", c.cfg.UserAgent)
			return nil
		},
	}

	resp, err := trace.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	limited := io.LimitReader(resp.Body, c.cfg.MaxBodyBytes+1)
	body, err := io.ReadAll(limited)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	truncated := int64(len(body)) > c.cfg.MaxBodyBytes
	if truncated {
		body = body[:c.cfg.MaxBodyBytes]
	}
	// Drain a little so the connection can be reused.
	_, _ = io.CopyN(io.Discard, resp.Body, 4096)

	return &Response{
		URL:         resp.Request.URL.String(),
		RequestURL:  rawurl,
		StatusCode:  resp.StatusCode,
		Header:      resp.Header,
		Body:        body,
		ContentType: resp.Header.Get("Content-Type"),
		Elapsed:     time.Since(start),
		Redirects:   redirects,
		Truncated:   truncated,
	}, nil
}

func retryableStatus(code int) bool {
	return code == http.StatusTooManyRequests ||
		code == http.StatusRequestTimeout ||
		(code >= 500 && code != http.StatusNotImplemented)
}

func retryableError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	var ne net.Error
	if errors.As(err, &ne) {
		return true
	}
	s := err.Error()
	return strings.Contains(s, "connection reset") ||
		strings.Contains(s, "EOF") ||
		strings.Contains(s, "broken pipe")
}

// retryAfter handles both forms of the header: seconds and HTTP-date.
func retryAfter(h http.Header) time.Duration {
	v := h.Get("Retry-After")
	if v == "" {
		return 0
	}
	if secs, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && secs >= 0 {
		return time.Duration(secs) * time.Second
	}
	if t, err := http.ParseTime(v); err == nil {
		if d := time.Until(t); d > 0 {
			return d
		}
	}
	return 0
}

// backoff is exponential with full jitter, capped. Without the jitter, workers
// that back off together retry together and arrive as a wave.
func backoff(attempt int) time.Duration {
	base := time.Duration(1<<uint(attempt-1)) * 500 * time.Millisecond
	if base > 15*time.Second {
		base = 15 * time.Second
	}
	return time.Duration(rand.Int63n(int64(base) + 1))
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
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
