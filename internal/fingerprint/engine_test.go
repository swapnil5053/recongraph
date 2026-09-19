package fingerprint

import (
	"net/http"
	"strings"
	"testing"

	"github.com/swapnil5053/recongraph/pkg/sitegraph"
)

func load(t *testing.T) *Engine {
	t.Helper()
	e, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return e
}

// Catches a typo in the signature database at build time rather than mid-crawl.
func TestSignatureDatabaseIsValid(t *testing.T) {
	e := load(t)
	if e.Count() < 40 {
		t.Errorf("only %d signatures loaded, expected the full database", e.Count())
	}
	seen := map[string]bool{}
	for _, s := range e.Signatures() {
		if seen[s.Name] {
			t.Errorf("duplicate signature name %q", s.Name)
		}
		seen[s.Name] = true
		if len(s.Matchers) == 0 {
			t.Errorf("%s has no matchers", s.Name)
		}
		for i, m := range s.Matchers {
			switch m.Type {
			case MatchHeader, MatchCookie, MatchMeta, MatchScriptSrc, MatchBody, MatchURLPath, MatchClass:
			default:
				t.Errorf("%s matcher %d: unknown type %q", s.Name, i, m.Type)
			}
			if m.Weight < 0 || m.Weight > 100 {
				t.Errorf("%s matcher %d: weight %d out of range", s.Name, i, m.Weight)
			}
		}
	}
	// Implications must point at signatures that exist, or the cascade emits
	// technologies that can never be detected on their own.
	for _, s := range e.Signatures() {
		for _, imp := range s.Implies {
			if !seen[imp] {
				t.Errorf("%s implies %q which is not a known signature", s.Name, imp)
			}
		}
	}
}

func TestDetectWordPress(t *testing.T) {
	e := load(t)
	in := Input{
		Header: http.Header{"Set-Cookie": []string{"wordpress_logged_in_abc=1; Path=/"}},
		Metas:  map[string]string{"generator": "WordPress 6.4.2"},
		ScriptSrcs: []string{
			"https://example.com/wp-includes/js/jquery/jquery.min.js?ver=3.7.1",
		},
		Body:    []byte(`<link rel="stylesheet" href="/wp-content/themes/x/style.css">`),
		URLPath: "/",
	}
	techs := e.Match(in)
	got := byName(techs)

	wp, ok := got["WordPress"]
	if !ok {
		t.Fatalf("WordPress not detected; got %v", names(techs))
	}
	if wp.Version != "6.4.2" {
		t.Errorf("version = %q, want 6.4.2", wp.Version)
	}
	if wp.Confidence < 80 {
		t.Errorf("confidence = %d, want >= 80", wp.Confidence)
	}
	if len(wp.Evidence) == 0 {
		t.Error("no evidence recorded")
	}
	// jQuery should be detected independently from the script src.
	if _, ok := got["jQuery"]; !ok {
		t.Errorf("jQuery not detected from script src; got %v", names(techs))
	}
	// PHP should arrive via the implication cascade.
	php, ok := got["PHP"]
	if !ok {
		t.Errorf("PHP not implied by WordPress; got %v", names(techs))
	} else if !strings.Contains(strings.Join(php.Evidence, " "), "implied by WordPress") {
		t.Errorf("implied tech should record why: %v", php.Evidence)
	}
}

func TestDetectNextJS(t *testing.T) {
	e := load(t)
	in := Input{
		Header:     http.Header{"X-Powered-By": []string{"Next.js 14.1.0"}},
		ScriptSrcs: []string{"/_next/static/chunks/main-abc.js"},
		Body:       []byte(`<script id="__NEXT_DATA__" type="application/json">{}</script>`),
	}
	got := byName(e.Match(in))
	nx, ok := got["Next.js"]
	if !ok {
		t.Fatal("Next.js not detected")
	}
	if nx.Version != "14.1.0" {
		t.Errorf("version = %q, want 14.1.0", nx.Version)
	}
	for _, want := range []string{"React", "Node.js"} {
		if _, ok := got[want]; !ok {
			t.Errorf("%s should be implied by Next.js", want)
		}
	}
}

func TestDetectServerHeaders(t *testing.T) {
	e := load(t)
	got := byName(e.Match(Input{
		Header: http.Header{
			"Server":       []string{"nginx/1.25.3"},
			"X-Powered-By": []string{"PHP/8.2.10"},
			"Cf-Ray":       []string{"8a1b2c3d4e5f6789-LHR"},
		},
	}))
	if n, ok := got["Nginx"]; !ok {
		t.Error("Nginx not detected")
	} else if n.Version != "1.25.3" {
		t.Errorf("nginx version = %q, want 1.25.3", n.Version)
	}
	if p, ok := got["PHP"]; !ok {
		t.Error("PHP not detected")
	} else if p.Version != "8.2.10" {
		t.Errorf("php version = %q, want 8.2.10", p.Version)
	}
	if _, ok := got["Cloudflare"]; !ok {
		t.Error("Cloudflare not detected from cf-ray")
	}
}

// A blank page must fingerprint as nothing. False positives send you down a
// wrong path, which is worse than a miss.
func TestNoFalsePositivesOnEmptyPage(t *testing.T) {
	e := load(t)
	techs := e.Match(Input{
		Header:  http.Header{},
		Body:    []byte("<html><head><title>hi</title></head><body><p>hello</p></body></html>"),
		URLPath: "/",
	})
	if len(techs) != 0 {
		t.Errorf("detected %v on a plain page, want none", names(techs))
	}
}

func TestThresholdSuppressesWeakEvidence(t *testing.T) {
	e := load(t)
	e.SetThreshold(95)
	techs := e.Match(Input{
		Header: http.Header{"Server": []string{"nginx"}},
	})
	if len(techs) != 0 {
		t.Errorf("threshold 95 should suppress a single 90-weight match, got %v", names(techs))
	}
}

func TestProbesAreDeclaredButSeparate(t *testing.T) {
	e := load(t)
	probes := e.ProbePaths()
	if len(probes) == 0 {
		t.Fatal("no probe paths declared")
	}
	if _, ok := probes["WordPress"]; !ok {
		t.Error("WordPress should declare probe paths")
	}
	// Match must never consult probes; those are opt-in extra requests.
	techs := e.Match(Input{URLPath: "/", Header: http.Header{}})
	if len(techs) != 0 {
		t.Errorf("Match must not use probes, got %v", names(techs))
	}
}

func byName(ts []sitegraph.Tech) map[string]sitegraph.Tech {
	out := make(map[string]sitegraph.Tech, len(ts))
	for _, t := range ts {
		out[t.Name] = t
	}
	return out
}

func names(ts []sitegraph.Tech) []string {
	out := make([]string, 0, len(ts))
	for _, t := range ts {
		out = append(out, t.Name)
	}
	return out
}
