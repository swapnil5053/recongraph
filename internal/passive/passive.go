// Package passive pulls contact addresses, bucket references, API endpoints,
// social accounts and secret-shaped strings out of response bodies.
//
// Operates on bytes the crawler already fetched, so it costs no extra requests.
package passive

import (
	"regexp"
	"sort"
	"strings"

	"github.com/swapnil5053/recongraph/internal/jsscan"
)

// Finding kinds.
const (
	KindEmail    = "email"
	KindBucket   = "bucket"
	KindAPI      = "api-endpoint"
	KindSocial   = "social"
	KindSecret   = "secret-like"
	KindInternal = "internal-host"
)

// Result is one extracted value plus the kind it was classified as.
type Result struct {
	Kind     string
	Value    string
	Evidence string
}

var (
	reEmail = regexp.MustCompile(`(?i)\b[a-z0-9._%+\-]+@[a-z0-9.\-]+\.[a-z]{2,24}\b`)

	// Cloud storage references, in their common spellings.
	reS3     = regexp.MustCompile(`(?i)(?://|\s)([a-z0-9][a-z0-9.\-]{1,61}[a-z0-9])\.s3[.\-](?:[a-z0-9\-]+\.)?amazonaws\.com\b`)
	reS3Path = regexp.MustCompile(`(?i)//s3[.\-](?:[a-z0-9\-]+\.)?amazonaws\.com/([a-z0-9][a-z0-9.\-]{1,61}[a-z0-9])\b`)
	reGCS    = regexp.MustCompile(`(?i)//(?:storage\.googleapis\.com/|storage\.cloud\.google\.com/)([a-z0-9][a-z0-9._\-]{1,61}[a-z0-9])\b`)
	reAzure  = regexp.MustCompile(`(?i)\b([a-z0-9]{3,24})\.blob\.core\.windows\.net\b`)
	reDO     = regexp.MustCompile(`(?i)\b([a-z0-9][a-z0-9\-]{1,61})\.(?:[a-z0-9\-]+\.)?digitaloceanspaces\.com\b`)

	// API-shaped paths inside JS and HTML.
	reAPIPath = regexp.MustCompile(`(?i)["'` + "`" + `](/(?:api|rest|graphql|v[0-9]{1,2}|_next/data|wp-json|admin/api)(?:/[a-z0-9_\-./{}:]*)?)["'` + "`" + `]`)

	// Social handles / profile links.
	reSocial = regexp.MustCompile(`(?i)https?://(?:www\.)?(twitter\.com|x\.com|linkedin\.com|github\.com|facebook\.com|instagram\.com|youtube\.com|t\.me|discord\.gg|medium\.com|stackoverflow\.com)/[A-Za-z0-9_\-./@]{1,80}`)

	// High-signal patterns only. A generic "long base64 string" matcher is all
	// noise.
	reSecrets = []*regexp.Regexp{
		regexp.MustCompile(`\bAKIA[0-9A-Z]{16}\b`),              // AWS access key id
		regexp.MustCompile(`\bASIA[0-9A-Z]{16}\b`),              // AWS temp key id
		regexp.MustCompile(`\bAIza[0-9A-Za-z_\-]{35}\b`),        // Google API key
		regexp.MustCompile(`\bgh[pousr]_[A-Za-z0-9]{36,}\b`),    // GitHub token
		regexp.MustCompile(`\bxox[baprs]-[A-Za-z0-9\-]{10,}\b`), // Slack token
		regexp.MustCompile(`\bsk_live_[0-9a-zA-Z]{24,}\b`),      // Stripe live key
		regexp.MustCompile(`-----BEGIN (?:RSA |EC |OPENSSH )?PRIVATE KEY-----`),
	}

	// Private / internal hostnames leaking in comments and config blobs.
	reInternal  = regexp.MustCompile(`(?i)\b(?:https?://)?((?:[a-z0-9\-]+\.)*(?:internal|intranet|corp|local|localdomain|test|staging|stage|dev|uat|qa)(?:\.[a-z0-9\-]+)*)\b(?::\d{2,5})?`)
	rePrivateIP = regexp.MustCompile(`\b(?:10\.\d{1,3}\.\d{1,3}\.\d{1,3}|192\.168\.\d{1,3}\.\d{1,3}|172\.(?:1[6-9]|2\d|3[01])\.\d{1,3}\.\d{1,3})\b`)
)

// Common false-positive emails baked into libraries and licence headers.
var emailNoise = map[string]bool{
	"example@example.com": true,
	"you@example.com":     true,
	"user@example.com":    true,
	"name@email.com":      true,
	"email@example.com":   true,
	"someone@example.com": true,
}

// Extract runs every extractor over a body. isScript enables the JS-only ones.
func Extract(body []byte, isScript bool) []Result {
	s := string(body)
	seen := map[string]bool{}
	var out []Result

	add := func(kind, value, evidence string) {
		value = strings.TrimSpace(value)
		if value == "" {
			return
		}
		key := kind + "|" + strings.ToLower(value)
		if seen[key] {
			return
		}
		seen[key] = true
		out = append(out, Result{Kind: kind, Value: value, Evidence: evidence})
	}

	for _, m := range reEmail.FindAllString(s, 200) {
		low := strings.ToLower(m)
		if emailNoise[low] || strings.HasSuffix(low, ".png") || strings.HasSuffix(low, ".jpg") {
			continue
		}
		add(KindEmail, m, "")
	}

	for _, re := range []*regexp.Regexp{reS3, reS3Path, reGCS, reAzure, reDO} {
		for _, m := range re.FindAllStringSubmatch(s, 100) {
			if len(m) > 1 {
				add(KindBucket, m[1], m[0])
			}
		}
	}

	for _, m := range reSocial.FindAllString(s, 100) {
		add(KindSocial, strings.TrimRight(m, "/."), "")
	}

	for _, re := range reSecrets {
		for _, m := range re.FindAllString(s, 20) {
			add(KindSecret, m, "")
		}
	}

	for _, m := range rePrivateIP.FindAllString(s, 50) {
		add(KindInternal, m, "")
	}
	for _, m := range reInternal.FindAllStringSubmatch(s, 80) {
		if len(m) > 1 && strings.Contains(m[1], ".") {
			add(KindInternal, m[1], "")
		}
	}

	if isScript {
		for _, e := range jsscan.Endpoints(body) {
			// Absolute URLs found as plain literals are mostly CDNs and docs
			// links; they become edges, not findings.
			if e.Source == jsscan.SourceLiteral && !strings.HasPrefix(e.Value, "/") {
				continue
			}
			add(KindAPI, e.Value, endpointEvidence(e))
		}
	} else {
		for _, m := range reAPIPath.FindAllStringSubmatch(s, 120) {
			if len(m) > 1 {
				add(KindAPI, m[1], "")
			}
		}
	}

	sort.Slice(out, func(i, j int) bool {
		if out[i].Kind != out[j].Kind {
			return out[i].Kind < out[j].Kind
		}
		return out[i].Value < out[j].Value
	})
	return out
}

// JSEndpoints pulls crawlable URLs out of JavaScript. These become
// rel="js-endpoint" edges. Endpoints built at runtime are left out, since
// there's nothing to fetch; Extract still reports them as findings.
func JSEndpoints(body []byte) []string {
	var out []string
	for _, e := range jsscan.Endpoints(body) {
		if !e.Dynamic {
			out = append(out, e.Value)
		}
	}
	return out
}

func endpointEvidence(e jsscan.Endpoint) string {
	var ev string
	switch e.Source {
	case jsscan.SourceCall:
		ev = e.Callee + "()"
	case jsscan.SourceProperty:
		ev = "url property"
	default:
		ev = "string literal"
	}
	if e.Dynamic {
		ev += ", built at runtime"
	}
	return ev
}

// ExtractFromComments runs the extractors over HTML comment text only.
func ExtractFromComments(comments []string) []Result {
	if len(comments) == 0 {
		return nil
	}
	return Extract([]byte(strings.Join(comments, "\n")), false)
}
