// Package fingerprint detects what a page runs on.
//
// Signatures live in signatures/*.json and are embedded at build time, so
// adding a technology is a data change. Everything except probes reads
// responses the crawler already fetched, so it costs no extra requests.
//
// Results attach per node, not per site: a marketing page behind a CDN and a
// Django admin on the same host have different stacks.
package fingerprint

import (
	"embed"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strings"

	"github.com/swapnil5053/recongraph/pkg/sitegraph"
)

//go:embed signatures/*.json
var signatureFS embed.FS

// MatcherType names where a signature looks.
const (
	MatchHeader    = "header"     // response header value
	MatchCookie    = "cookie"     // Set-Cookie name
	MatchMeta      = "meta"       // <meta name=...> content
	MatchScriptSrc = "script-src" // <script src> value
	MatchBody      = "body"       // response body
	MatchURLPath   = "url-path"   // request path
	MatchClass     = "class"      // CSS class attribute
)

// Matcher is one piece of evidence for a technology.
type Matcher struct {
	Type string `json:"type"`
	// Name scopes header and meta matchers (e.g. "x-powered-by", "generator").
	Name string `json:"name,omitempty"`
	// Pattern is a Go regexp, matched case-insensitively.
	Pattern string `json:"pattern"`
	// Version, when > 0, is the capture group holding a version string.
	Version int `json:"version,omitempty"`
	// Weight contributes to the confidence score when this matcher hits.
	Weight int `json:"weight"`

	re *regexp.Regexp
}

// Signature declares one detectable technology.
type Signature struct {
	Name       string    `json:"name"`
	Categories []string  `json:"categories,omitempty"`
	Matchers   []Matcher `json:"matchers"`
	// Implies names what follows from this one (WordPress -> PHP).
	Implies []string `json:"implies,omitempty"`
	// Probes are paths that would confirm this but need requests to URLs
	// nothing linked to. Opt-in, never run by default.
	Probes []string `json:"probes,omitempty"`
}

// Input is everything a fingerprint pass can look at for one node.
type Input struct {
	Header     http.Header
	Metas      map[string]string
	ScriptSrcs []string
	Classes    []string
	Body       []byte
	URLPath    string
}

// Engine holds the compiled signature set.
type Engine struct {
	sigs      []Signature
	byName    map[string]*Signature
	threshold int
}

// DefaultThreshold is the minimum summed weight for a detection to be reported.
const DefaultThreshold = 40

// Load reads the embedded signature database.
func Load() (*Engine, error) {
	entries, err := signatureFS.ReadDir("signatures")
	if err != nil {
		return nil, err
	}
	e := &Engine{byName: map[string]*Signature{}, threshold: DefaultThreshold}
	for _, ent := range entries {
		if ent.IsDir() || !strings.HasSuffix(ent.Name(), ".json") {
			continue
		}
		data, err := signatureFS.ReadFile("signatures/" + ent.Name())
		if err != nil {
			return nil, err
		}
		var sigs []Signature
		if err := json.Unmarshal(data, &sigs); err != nil {
			return nil, fmt.Errorf("%s: %w", ent.Name(), err)
		}
		e.sigs = append(e.sigs, sigs...)
	}
	if err := e.compile(); err != nil {
		return nil, err
	}
	return e, nil
}

func (e *Engine) compile() error {
	// Sort before taking pointers into the slice. Sorting after reorders the
	// backing array and every pointer in byName ends up on the wrong signature.
	// Cost me an afternoon.
	sort.Slice(e.sigs, func(i, j int) bool { return e.sigs[i].Name < e.sigs[j].Name })

	for i := range e.sigs {
		sig := &e.sigs[i]
		if sig.Name == "" {
			return fmt.Errorf("signature %d has no name", i)
		}
		for j := range sig.Matchers {
			m := &sig.Matchers[j]
			if m.Pattern == "" {
				return fmt.Errorf("%s: matcher %d has no pattern", sig.Name, j)
			}
			re, err := regexp.Compile("(?i)" + m.Pattern)
			if err != nil {
				return fmt.Errorf("%s: matcher %d: %w", sig.Name, j, err)
			}
			m.re = re
			if m.Weight == 0 {
				m.Weight = 25
			}
		}
		e.byName[strings.ToLower(sig.Name)] = sig
	}
	return nil
}

// SetThreshold overrides the confidence floor.
func (e *Engine) SetThreshold(t int) { e.threshold = t }

// Count is how many signatures are loaded.
func (e *Engine) Count() int { return len(e.sigs) }

// Signatures exposes the loaded set (for `recongraph fingerprint --list`).
func (e *Engine) Signatures() []Signature { return e.sigs }

// Match runs every signature against one node's evidence.
func (e *Engine) Match(in Input) []sitegraph.Tech {
	found := map[string]*sitegraph.Tech{}

	for i := range e.sigs {
		sig := &e.sigs[i]
		score := 0
		version := ""
		var evidence []string

		for j := range sig.Matchers {
			m := &sig.Matchers[j]
			hit, ver, ev := matchOne(m, in)
			if !hit {
				continue
			}
			score += m.Weight
			if ver != "" && version == "" {
				version = ver
			}
			if len(evidence) < 4 {
				evidence = append(evidence, ev)
			}
		}

		if score < e.threshold {
			continue
		}
		if score > 100 {
			score = 100
		}
		found[sig.Name] = &sitegraph.Tech{
			Name:       sig.Name,
			Version:    version,
			Categories: sig.Categories,
			Confidence: score,
			Evidence:   evidence,
		}
	}

	// Cascade implications at lower confidence; they're inferred, not seen.
	for name := range found {
		sig := e.byName[strings.ToLower(name)]
		if sig == nil {
			continue
		}
		for _, imp := range sig.Implies {
			if _, ok := found[imp]; ok {
				continue
			}
			found[imp] = &sitegraph.Tech{
				Name:       imp,
				Confidence: 50,
				Evidence:   []string{"implied by " + name},
			}
		}
	}

	out := make([]sitegraph.Tech, 0, len(found))
	for _, t := range found {
		out = append(out, *t)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Confidence != out[j].Confidence {
			return out[i].Confidence > out[j].Confidence
		}
		return out[i].Name < out[j].Name
	})
	return out
}

func matchOne(m *Matcher, in Input) (hit bool, version, evidence string) {
	switch m.Type {
	case MatchHeader:
		if in.Header == nil {
			return false, "", ""
		}
		if m.Name != "" {
			v := in.Header.Get(m.Name)
			if v == "" {
				return false, "", ""
			}
			if sub := m.re.FindStringSubmatch(v); sub != nil {
				return true, capture(sub, m.Version), fmt.Sprintf("header %s: %s", m.Name, truncate(v, 60))
			}
			return false, "", ""
		}
		for k, vs := range in.Header {
			for _, v := range vs {
				if sub := m.re.FindStringSubmatch(v); sub != nil {
					return true, capture(sub, m.Version), fmt.Sprintf("header %s: %s", k, truncate(v, 60))
				}
			}
		}

	case MatchCookie:
		if in.Header == nil {
			return false, "", ""
		}
		for _, sc := range in.Header.Values("Set-Cookie") {
			name, _, _ := strings.Cut(sc, "=")
			name = strings.TrimSpace(name)
			if sub := m.re.FindStringSubmatch(name); sub != nil {
				return true, capture(sub, m.Version), "cookie " + truncate(name, 60)
			}
		}

	case MatchMeta:
		if in.Metas == nil {
			return false, "", ""
		}
		if m.Name != "" {
			v, ok := in.Metas[strings.ToLower(m.Name)]
			if !ok {
				return false, "", ""
			}
			if sub := m.re.FindStringSubmatch(v); sub != nil {
				return true, capture(sub, m.Version), fmt.Sprintf("meta %s: %s", m.Name, truncate(v, 60))
			}
			return false, "", ""
		}
		for k, v := range in.Metas {
			if sub := m.re.FindStringSubmatch(v); sub != nil {
				return true, capture(sub, m.Version), fmt.Sprintf("meta %s: %s", k, truncate(v, 60))
			}
		}

	case MatchScriptSrc:
		for _, src := range in.ScriptSrcs {
			if sub := m.re.FindStringSubmatch(src); sub != nil {
				return true, capture(sub, m.Version), "script " + truncate(src, 70)
			}
		}

	case MatchClass:
		for _, c := range in.Classes {
			if sub := m.re.FindStringSubmatch(c); sub != nil {
				return true, capture(sub, m.Version), "class " + truncate(c, 50)
			}
		}

	case MatchBody:
		if len(in.Body) == 0 {
			return false, "", ""
		}
		if sub := m.re.FindSubmatch(in.Body); sub != nil {
			strs := make([]string, len(sub))
			for i, b := range sub {
				strs[i] = string(b)
			}
			return true, capture(strs, m.Version), "body: " + truncate(strs[0], 60)
		}

	case MatchURLPath:
		if in.URLPath == "" {
			return false, "", ""
		}
		if sub := m.re.FindStringSubmatch(in.URLPath); sub != nil {
			return true, capture(sub, m.Version), "path " + truncate(in.URLPath, 60)
		}
	}
	return false, "", ""
}

func capture(sub []string, idx int) string {
	if idx > 0 && idx < len(sub) {
		return strings.TrimSpace(sub[idx])
	}
	return ""
}

func truncate(s string, n int) string {
	s = strings.ReplaceAll(strings.TrimSpace(s), "\n", " ")
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// ProbePaths returns the opt-in probe paths. Separate call because these are
// requests to URLs nothing linked to, which is a different posture from reading
// what we already fetched.
func (e *Engine) ProbePaths() map[string][]string {
	out := map[string][]string{}
	for i := range e.sigs {
		if len(e.sigs[i].Probes) > 0 {
			out[e.sigs[i].Name] = e.sigs[i].Probes
		}
	}
	return out
}
