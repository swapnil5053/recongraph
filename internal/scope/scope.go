// Package scope decides which discovered URLs are in bounds.
//
// URLs are parsed and host labels compared. Matching the raw URL string with a
// regex is how a crawler gets walked off-target by "example.com.attacker.net".
package scope

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"

	"github.com/swapnil5053/recongraph/pkg/sitegraph"
)

// Decision is the outcome of a scope check.
type Decision int

const (
	// Crawl means fetch it and follow its links.
	Crawl Decision = iota
	// Record means add it to the graph as a node, but never fetch it.
	Record
	// Reject means drop it entirely.
	Reject
)

func (d Decision) String() string {
	switch d {
	case Crawl:
		return "crawl"
	case Record:
		return "record"
	default:
		return "reject"
	}
}

// Rules configures scope for one crawl.
type Rules struct {
	// Hosts are the seed hosts. A URL's host must equal one of these (or be a
	// subdomain of one, when AllowSubdomains is set) to be crawled.
	Hosts []string
	// AllowSubdomains extends scope to subdomains of the seed hosts.
	AllowSubdomains bool
	// PathPrefix restricts crawling to this path prefix, compared on segment
	// boundaries so /admin does not match /administrator.
	PathPrefix string
	// Include, if non-empty, requires a URL to match at least one pattern.
	Include []*regexp.Regexp
	// Exclude rejects any URL matching any pattern.
	Exclude []*regexp.Regexp
	// MaxDepth is the deepest link distance from a seed that will be crawled.
	// Depth 0 is the seed itself. Negative means unlimited.
	MaxDepth int
	// RecordExternal keeps out-of-scope URLs in the graph as leaf nodes, so
	// third-party references stay visible instead of being dropped.
	RecordExternal bool
	// AllowedSchemes defaults to http and https.
	AllowedSchemes []string
}

// DefaultRules returns sensible defaults for a seed host.
func DefaultRules(host string) *Rules {
	return &Rules{
		Hosts:          []string{strings.ToLower(host)},
		MaxDepth:       3,
		RecordExternal: true,
		AllowedSchemes: []string{"http", "https"},
	}
}

// AddSeedHost registers another in-scope host.
func (r *Rules) AddSeedHost(host string) {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	for _, h := range r.Hosts {
		if h == host {
			return
		}
	}
	r.Hosts = append(r.Hosts, host)
}

// CompilePatterns compiles a list of regex strings, returning a useful error.
func CompilePatterns(pats []string) ([]*regexp.Regexp, error) {
	out := make([]*regexp.Regexp, 0, len(pats))
	for _, p := range pats {
		re, err := regexp.Compile(p)
		if err != nil {
			return nil, fmt.Errorf("invalid pattern %q: %w", p, err)
		}
		out = append(out, re)
	}
	return out, nil
}

// InScopeHost reports whether a host is one of the seed hosts (or a subdomain
// of one when subdomains are allowed).
func (r *Rules) InScopeHost(host string) bool {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	if h, _, ok := strings.Cut(host, ":"); ok {
		host = h
	}
	for _, seed := range r.Hosts {
		if r.AllowSubdomains {
			if sitegraph.SameOrSubdomain(host, seed) {
				return true
			}
		} else if host == seed {
			return true
		}
	}
	return false
}

// Check classifies a canonical URL at a given depth.
func (r *Rules) Check(canonURL string, depth int) Decision {
	u, err := url.Parse(canonURL)
	if err != nil {
		return Reject
	}

	if !r.schemeAllowed(u.Scheme) {
		return Reject
	}

	// Exclusions beat everything, seed host included.
	for _, re := range r.Exclude {
		if re.MatchString(canonURL) {
			return Reject
		}
	}

	if !r.InScopeHost(u.Hostname()) {
		if r.RecordExternal {
			return Record
		}
		return Reject
	}

	if len(r.Include) > 0 {
		matched := false
		for _, re := range r.Include {
			if re.MatchString(canonURL) {
				matched = true
				break
			}
		}
		if !matched {
			return Record
		}
	}

	if r.PathPrefix != "" && !pathHasPrefix(u.Path, r.PathPrefix) {
		return Record
	}

	if r.MaxDepth >= 0 && depth > r.MaxDepth {
		return Record
	}

	return Crawl
}

func (r *Rules) schemeAllowed(scheme string) bool {
	allowed := r.AllowedSchemes
	if len(allowed) == 0 {
		allowed = []string{"http", "https"}
	}
	for _, s := range allowed {
		if strings.EqualFold(s, scheme) {
			return true
		}
	}
	return false
}

// pathHasPrefix compares on segment boundaries; plain HasPrefix would let
// /admin match /administrator.
func pathHasPrefix(path, prefix string) bool {
	if prefix == "" || prefix == "/" {
		return true
	}
	path = "/" + strings.Trim(path, "/")
	prefix = "/" + strings.Trim(prefix, "/")
	if path == prefix {
		return true
	}
	return strings.HasPrefix(path, prefix+"/")
}
