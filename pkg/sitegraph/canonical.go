package sitegraph

import (
	"errors"
	"net/url"
	"path"
	"sort"
	"strings"
)

// Diff quality depends almost entirely on canonicalisation: under-normalise and
// every crawl looks 100% changed, over-normalise and real changes disappear.
// Hence the table tests in canonical_test.go.

var (
	// ErrNotHTTP is returned for schemes we never crawl (mailto:, javascript:,
	// tel:, data:, ...).
	ErrNotHTTP = errors.New("not an http(s) URL")
	// ErrNoHost is returned for URLs that resolve to no host.
	ErrNoHost = errors.New("no host")
)

// DefaultStripParams identify a visitor or a campaign rather than a resource.
// Leaving them in makes every crawl look new.
var DefaultStripParams = []string{
	"utm_source", "utm_medium", "utm_campaign", "utm_term", "utm_content", "utm_id",
	"gclid", "fbclid", "msclkid", "dclid", "yclid", "igshid", "mc_cid", "mc_eid",
	"_ga", "_gl", "ref_src", "ref_url", "sessionid", "session_id",
	"phpsessid", "jsessionid", "aspsessionid", "cfid", "cftoken",
}

// CanonOpts controls canonicalisation.
type CanonOpts struct {
	// StripParams is the set of query parameters to drop (case-insensitive).
	StripParams map[string]bool
	// KeepQuery false drops the query string. Useful against sites that
	// generate unbounded parameter permutations (calendars, faceted search).
	KeepQuery bool
	// SortQuery makes ?a=1&b=2 and ?b=2&a=1 one node rather than two.
	SortQuery bool
}

// DefaultCanonOpts returns the recommended settings.
func DefaultCanonOpts() CanonOpts {
	m := make(map[string]bool, len(DefaultStripParams))
	for _, p := range DefaultStripParams {
		m[p] = true
	}
	return CanonOpts{StripParams: m, KeepQuery: true, SortQuery: true}
}

// Canonicalize normalises an absolute http(s) URL into a node's identity:
// lowercase scheme and host, no default port, no trailing dot, no fragment,
// dot-segments resolved, non-empty path, tracking params dropped, rest sorted.
func Canonicalize(raw string, opts CanonOpts) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", ErrNoHost
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", err
	}
	return CanonicalizeURL(u, opts)
}

// CanonicalizeURL is Canonicalize for an already-parsed URL.
func CanonicalizeURL(u *url.URL, opts CanonOpts) (string, error) {
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return "", ErrNotHTTP
	}

	host := strings.ToLower(u.Hostname())
	host = strings.TrimSuffix(host, ".")
	if host == "" {
		return "", ErrNoHost
	}
	port := u.Port()
	if (scheme == "http" && port == "80") || (scheme == "https" && port == "443") {
		port = ""
	}

	p := u.EscapedPath()
	if p == "" {
		p = "/"
	}
	// Resolve . and .. but keep a meaningful trailing slash.
	trailing := strings.HasSuffix(p, "/")
	cleaned := path.Clean(p)
	if cleaned == "." {
		cleaned = "/"
	}
	if trailing && cleaned != "/" {
		cleaned += "/"
	}
	p = cleaned

	out := &url.URL{Scheme: scheme, Host: host, Path: ""}
	if port != "" {
		out.Host = host + ":" + port
	}
	out.RawPath = p
	out.Opaque = ""

	q := ""
	if opts.KeepQuery && u.RawQuery != "" {
		q = filterQuery(u.RawQuery, opts)
	}

	s := scheme + "://" + out.Host + p
	if q != "" {
		s += "?" + q
	}
	return s, nil
}

func filterQuery(raw string, opts CanonOpts) string {
	parts := strings.Split(raw, "&")
	kept := make([]string, 0, len(parts))
	for _, kv := range parts {
		if kv == "" {
			continue
		}
		name := kv
		if i := strings.IndexByte(kv, '='); i >= 0 {
			name = kv[:i]
		}
		if opts.StripParams != nil && opts.StripParams[strings.ToLower(name)] {
			continue
		}
		kept = append(kept, kv)
	}
	if opts.SortQuery {
		sort.Strings(kept)
	}
	return strings.Join(kept, "&")
}

// Resolve turns a reference found on base into a canonical absolute URL.
// mailto:, javascript:, tel: and friends come back as ErrNotHTTP; callers treat
// those as passive findings, not crawl candidates.
func Resolve(base *url.URL, ref string, opts CanonOpts) (string, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "", ErrNoHost
	}
	// Reject non-navigable schemes before the parser has to guess.
	lower := strings.ToLower(ref)
	for _, bad := range []string{"javascript:", "mailto:", "tel:", "data:", "about:", "blob:", "sms:", "ftp:", "file:"} {
		if strings.HasPrefix(lower, bad) {
			return "", ErrNotHTTP
		}
	}
	if strings.HasPrefix(ref, "#") {
		return "", ErrNotHTTP
	}
	r, err := url.Parse(ref)
	if err != nil {
		return "", err
	}
	abs := base.ResolveReference(r)
	return CanonicalizeURL(abs, opts)
}

// splitURL pulls scheme, host and path out of an already-canonical URL.
// splitURL pulls the three parts out of an already-canonical URL by slicing
// it. url.Parse would allocate a URL and three strings per node; these are
// substrings of one that already exists.
func splitURL(canon string) (scheme, host, p string) {
	i := strings.Index(canon, "://")
	if i < 0 {
		return "", "", ""
	}
	scheme, rest := canon[:i], canon[i+3:]
	end := strings.IndexAny(rest, "/?#")
	if end < 0 {
		return scheme, rest, ""
	}
	host = rest[:end]
	p = rest[end:]
	if q := strings.IndexAny(p, "?#"); q >= 0 {
		p = p[:q]
	}
	return scheme, host, p
}

// RegistrableSuffixDepth is how many labels count as the registrable domain.
const RegistrableSuffixDepth = 2

// RootDomain returns the last two labels of a host ("a.b.example.co" ->
// "example.co").
//
// TODO: this is wrong for multi-part suffixes like foo.co.uk. Needs the Public
// Suffix List. Only used for display; scope decisions go through
// SameOrSubdomain, which is exact.
func RootDomain(host string) string {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	if h, _, ok := strings.Cut(host, ":"); ok {
		host = h
	}
	labels := strings.Split(host, ".")
	if len(labels) <= RegistrableSuffixDepth {
		return host
	}
	return strings.Join(labels[len(labels)-RegistrableSuffixDepth:], ".")
}

// SameOrSubdomain reports whether host is target or a subdomain of it,
// comparing whole labels. Substring and unanchored-regex checks accept
// "example.com.attacker.net"; this does not.
func SameOrSubdomain(host, target string) bool {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	target = strings.ToLower(strings.TrimSuffix(target, "."))
	if h, _, ok := strings.Cut(host, ":"); ok {
		host = h
	}
	if t, _, ok := strings.Cut(target, ":"); ok {
		target = t
	}
	if host == target {
		return true
	}
	return strings.HasSuffix(host, "."+target)
}
