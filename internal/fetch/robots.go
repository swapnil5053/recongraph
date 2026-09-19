package fetch

import (
	"bufio"
	"context"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// robots.txt is respected by default here, unlike most tools in this space.
// Polite by default with --ignore-robots for authorised work is the easier
// posture to defend.

type robotsRules struct {
	disallow   []string
	allow      []string
	crawlDelay time.Duration
	sitemaps   []string
	// fetched is false when robots.txt was missing or unreachable, which by
	// convention means no restrictions.
	fetched bool
}

// RobotsCache fetches and caches robots.txt per host.
type RobotsCache struct {
	mu        sync.Mutex
	byHost    map[string]*robotsRules
	inflight  map[string]chan struct{}
	userAgent string
	fetcher   func(ctx context.Context, rawurl string) (*Response, error)
}

// NewRobotsCache builds a cache. fetcher performs the actual HTTP GET.
func NewRobotsCache(userAgent string, fetcher func(ctx context.Context, rawurl string) (*Response, error)) *RobotsCache {
	return &RobotsCache{
		byHost:    map[string]*robotsRules{},
		inflight:  map[string]chan struct{}{},
		userAgent: userAgent,
		fetcher:   fetcher,
	}
}

// Rules returns the cached rules for a URL's host, fetching robots.txt once.
func (rc *RobotsCache) Rules(ctx context.Context, u *url.URL) *robotsRules {
	key := u.Scheme + "://" + u.Host
	for {
		rc.mu.Lock()
		if r, ok := rc.byHost[key]; ok {
			rc.mu.Unlock()
			return r
		}
		if ch, ok := rc.inflight[key]; ok {
			rc.mu.Unlock()
			select {
			case <-ch:
				continue // someone else finished it; re-read the map
			case <-ctx.Done():
				return &robotsRules{}
			}
		}
		ch := make(chan struct{})
		rc.inflight[key] = ch
		rc.mu.Unlock()

		rules := rc.load(ctx, key)

		rc.mu.Lock()
		rc.byHost[key] = rules
		delete(rc.inflight, key)
		rc.mu.Unlock()
		close(ch)
		return rules
	}
}

func (rc *RobotsCache) load(ctx context.Context, origin string) *robotsRules {
	resp, err := rc.fetcher(ctx, origin+"/robots.txt")
	if err != nil || resp == nil || resp.StatusCode != 200 {
		return &robotsRules{}
	}
	r := parseRobots(string(resp.Body), rc.userAgent)
	r.fetched = true
	return r
}

// parseRobots returns the rules for userAgent, falling back to the "*" group.
func parseRobots(body, userAgent string) *robotsRules {
	ua := strings.ToLower(userAgent)
	out := &robotsRules{}

	type group struct {
		agents  []string
		rules   *robotsRules
		matched bool
	}
	var groups []*group
	var cur *group
	lastWasAgent := false

	sc := bufio.NewScanner(strings.NewReader(body))
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Text()
		if i := strings.IndexByte(line, '#'); i >= 0 {
			line = line[:i]
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		key, val, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		key = strings.ToLower(strings.TrimSpace(key))
		val = strings.TrimSpace(val)

		switch key {
		case "user-agent":
			if !lastWasAgent || cur == nil {
				cur = &group{rules: &robotsRules{}}
				groups = append(groups, cur)
			}
			cur.agents = append(cur.agents, strings.ToLower(val))
			lastWasAgent = true
			continue
		case "sitemap":
			// Global, not per group.
			out.sitemaps = append(out.sitemaps, val)
		}
		lastWasAgent = false
		if cur == nil {
			continue
		}
		switch key {
		case "disallow":
			if val != "" {
				cur.rules.disallow = append(cur.rules.disallow, val)
			} else {
				// Empty Disallow means allow everything.
				cur.rules.allow = append(cur.rules.allow, "/")
			}
		case "allow":
			if val != "" {
				cur.rules.allow = append(cur.rules.allow, val)
			}
		case "crawl-delay":
			if f, err := strconv.ParseFloat(val, 64); err == nil && f > 0 {
				cur.rules.crawlDelay = time.Duration(f * float64(time.Second))
			}
		}
	}

	// Our own UA token beats "*".
	var starGroup, uaGroup *group
	for _, g := range groups {
		for _, a := range g.agents {
			if a == "*" && starGroup == nil {
				starGroup = g
			}
			if a != "*" && strings.Contains(ua, a) {
				uaGroup = g
			}
		}
	}
	chosen := uaGroup
	if chosen == nil {
		chosen = starGroup
	}
	if chosen != nil {
		out.disallow = chosen.rules.disallow
		out.allow = chosen.rules.allow
		out.crawlDelay = chosen.rules.crawlDelay
	}
	return out
}

// Allowed reports whether a path may be fetched. Longest match wins; Allow
// beats Disallow at equal length.
func (r *robotsRules) Allowed(path string) bool {
	if r == nil || (!r.fetched && len(r.disallow) == 0) {
		return true
	}
	if path == "" {
		path = "/"
	}
	bestLen, bestAllow := -1, true
	for _, p := range r.disallow {
		if n := matchRobotsPattern(p, path); n > bestLen {
			bestLen, bestAllow = n, false
		}
	}
	for _, p := range r.allow {
		if n := matchRobotsPattern(p, path); n > bestLen {
			bestLen, bestAllow = n, true
		} else if n == bestLen && n >= 0 {
			bestAllow = true
		}
	}
	if bestLen < 0 {
		return true
	}
	return bestAllow
}

// matchRobotsPattern returns the pattern length on a match, else -1.
// Handles the * wildcard and the $ anchor.
func matchRobotsPattern(pattern, path string) int {
	if pattern == "" {
		return -1
	}
	anchored := strings.HasSuffix(pattern, "$")
	p := strings.TrimSuffix(pattern, "$")

	if !strings.Contains(p, "*") {
		if anchored {
			if path == p {
				return len(pattern)
			}
			return -1
		}
		if strings.HasPrefix(path, p) {
			return len(pattern)
		}
		return -1
	}

	parts := strings.Split(p, "*")
	pos := 0
	for i, part := range parts {
		if part == "" {
			continue
		}
		if i == 0 {
			if !strings.HasPrefix(path[pos:], part) {
				return -1
			}
			pos += len(part)
			continue
		}
		idx := strings.Index(path[pos:], part)
		if idx < 0 {
			return -1
		}
		pos += idx + len(part)
	}
	if anchored && pos != len(path) {
		return -1
	}
	return len(pattern)
}

// Sitemaps returns the sitemap URLs declared in robots.txt.
func (r *robotsRules) Sitemaps() []string {
	if r == nil {
		return nil
	}
	return r.sitemaps
}

// CrawlDelay returns the host's declared crawl delay, if any.
func (r *robotsRules) CrawlDelay() time.Duration {
	if r == nil {
		return 0
	}
	return r.crawlDelay
}
