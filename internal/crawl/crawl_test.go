package crawl

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/swapnil5053/recongraph/internal/fetch"
	"github.com/swapnil5053/recongraph/internal/scope"
	"github.com/swapnil5053/recongraph/pkg/sitegraph"
)

// A deliberately awkward little site: orphan page reachable only from JS, a
// redirect chain, an external reference, a form, a sitemap listing an unlinked
// page, and a robots.txt that disallows one path.
func testSite(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html")
		w.Header().Set("Server", "nginx/1.24.0")
		w.Header().Set("X-Powered-By", "PHP/8.1.2")
		fmt.Fprint(w, `<!doctype html>
<html><head>
<title>Home</title>
<meta name="generator" content="WordPress 6.3">
<link rel="stylesheet" href="/assets/site.css">
<script src="/assets/app.js"></script>
</head><body>
<a href="/about">About</a>
<a href="/contact/">Contact</a>
<a href="/about?utm_source=nav">About again, tracked</a>
<a href="https://external.example.org/partner">Partner</a>
<a href="/private/secret">Disallowed by robots</a>
<a href="mailto:hello@testsite.local">Mail us</a>
<img src="/img/logo.png" alt="logo">
<form action="/search" method="get"><input name="q"></form>
<!-- TODO: remove staging.internal.example before launch -->
</body></html>`)
	})

	mux.HandleFunc("/about", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<html><head><title>About</title></head><body>
<a href="/">Home</a>
<a href="/team">Team</a>
<p>Contact ops@testsite.local or see s3 bucket assets-prod.s3.amazonaws.com</p>
</body></html>`)
	})

	mux.HandleFunc("/team", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<html><head><title>Team</title></head><body><a href="/">Home</a></body></html>`)
	})

	// A redirect chain: /contact/ -> /contact-us
	mux.HandleFunc("/contact/", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/contact-us", http.StatusFound)
	})
	mux.HandleFunc("/contact-us", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<html><head><title>Contact</title></head><body><a href="/">Home</a></body></html>`)
	})

	// Reachable ONLY from app.js. No anchor anywhere points here, which is the
	// classic forgotten-endpoint shape a flat crawler cannot surface.
	mux.HandleFunc("/api/v1/users", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"users":[]}`)
	})

	mux.HandleFunc("/assets/app.js", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/javascript")
		fmt.Fprint(w, `
			const KEY = "AKIAIOSFODNN7EXAMPLE";
			fetch("/api/v1/users").then(r => r.json());
			const admin = "/api/v1/admin";
		`)
	})

	mux.HandleFunc("/assets/site.css", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/css")
		fmt.Fprint(w, "body{margin:0}")
	})
	mux.HandleFunc("/img/logo.png", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		w.Write([]byte{0x89, 'P', 'N', 'G'})
	})
	mux.HandleFunc("/search", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<html><head><title>Search</title></head><body>no results</body></html>`)
	})
	mux.HandleFunc("/private/secret", func(w http.ResponseWriter, r *http.Request) {
		t.Error("crawler fetched a path disallowed by robots.txt")
		w.WriteHeader(403)
	})

	mux.HandleFunc("/robots.txt", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		fmt.Fprintf(w, "User-agent: *\nDisallow: /private/\nSitemap: %s/sitemap.xml\n", baseOf(r))
	})
	mux.HandleFunc("/sitemap.xml", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		fmt.Fprintf(w, `<?xml version="1.0"?>
<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">
  <url><loc>%s/</loc></url>
  <url><loc>%s/unlinked-from-sitemap</loc></url>
</urlset>`, baseOf(r), baseOf(r))
	})
	mux.HandleFunc("/unlinked-from-sitemap", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<html><head><title>Sitemap only</title></head><body>hi</body></html>`)
	})

	return httptest.NewServer(mux)
}

func baseOf(r *http.Request) string { return "http://" + r.Host }

func runCrawl(t *testing.T, srv *httptest.Server, tweak func(*Options)) (*sitegraph.Graph, Report) {
	t.Helper()
	u, _ := url.Parse(srv.URL)

	rules := scope.DefaultRules(u.Hostname())
	rules.MaxDepth = 4
	rules.RecordExternal = true

	fc := fetch.DefaultConfig()
	fc.RateLimit = 1000 // tests must not sleep; politeness is covered elsewhere
	fc.Burst = 64
	fc.Jitter = 0
	fc.Timeout = 5 * time.Second

	opts := Options{
		Seeds:       []string{srv.URL + "/"},
		Rules:       rules,
		Fetch:       fc,
		Canon:       sitegraph.DefaultCanonOpts(),
		Workers:     4,
		Fingerprint: true,
		Passive:     true,
		FollowJS:    true,
		UseSitemap:  true,
	}
	if tweak != nil {
		tweak(&opts)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	g, rep, err := Run(ctx, opts)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	return g, rep
}

func TestEndToEndCrawl(t *testing.T) {
	srv := testSite(t)
	defer srv.Close()
	g, rep := runCrawl(t, srv, nil)

	if rep.Status != "complete" {
		t.Errorf("status = %q, want complete", rep.Status)
	}
	if g.NumNodes() < 10 {
		t.Errorf("only %d nodes, expected the whole site", g.NumNodes())
	}
	if g.NumEdges() < 10 {
		t.Errorf("only %d edges", g.NumEdges())
	}

	urls := nodeURLs(g)

	for _, want := range []string{"/about", "/team", "/contact-us", "/search", "/assets/app.js", "/img/logo.png"} {
		if !hasSuffixIn(urls, want) {
			t.Errorf("missing node for %s\nhave: %v", want, urls)
		}
	}

	// robots.txt must be obeyed: the disallowed path may be recorded as a node
	// (it was linked) but must never have been fetched.
	for _, n := range g.Nodes() {
		if strings.Contains(n.URL, "/private/secret") && n.Fetched {
			t.Error("disallowed path is marked fetched")
		}
	}
	// And it counts as skipped, not as an error.
	if rep.Builder.Blocked != 1 || rep.Builder.Errors != 0 {
		t.Errorf("blocked=%d errors=%d, want 1 and 0", rep.Builder.Blocked, rep.Builder.Errors)
	}
}

// Tracked and untracked links to /about must collapse to one node, or every
// diff is noise.
func TestTrackingParamsCollapseToOneNode(t *testing.T) {
	srv := testSite(t)
	defer srv.Close()
	g, _ := runCrawl(t, srv, nil)

	count := 0
	for _, n := range g.Nodes() {
		if strings.HasSuffix(n.URL, "/about") || strings.Contains(n.URL, "/about?") {
			count++
		}
	}
	if count != 1 {
		t.Errorf("/about produced %d nodes, want 1 (tracking params must canonicalise away)", count)
	}
}

// An endpoint referenced only from JS, with no anchor anywhere, still lands in
// the graph.
func TestJSOnlyEndpointDiscovered(t *testing.T) {
	srv := testSite(t)
	defer srv.Close()
	g, _ := runCrawl(t, srv, nil)

	id, ok := g.Lookup(srv.URL + "/api/v1/users")
	if !ok {
		t.Fatalf("JS-only endpoint not in graph\nhave: %v", nodeURLs(g))
	}
	var viaJS bool
	for _, e := range g.InEdges(id) {
		if e.Rel == sitegraph.RelJSEndpoint {
			viaJS = true
		}
		if e.Rel == sitegraph.RelHref {
			t.Error("endpoint should not be reachable by an anchor in this fixture")
		}
	}
	if !viaJS {
		t.Error("endpoint present but not via a js-endpoint edge")
	}

	// And it must be reported as an orphan: no anchor points at it.
	foundOrphan := false
	for _, n := range g.Orphans() {
		if strings.HasSuffix(n.URL, "/api/v1/users") {
			foundOrphan = true
		}
	}
	if !foundOrphan {
		t.Error("JS-only endpoint should be reported as an orphan")
	}
}

func TestSitemapDiscovery(t *testing.T) {
	srv := testSite(t)
	defer srv.Close()
	g, _ := runCrawl(t, srv, nil)

	if _, ok := g.Lookup(srv.URL + "/unlinked-from-sitemap"); !ok {
		t.Errorf("page listed only in sitemap.xml was not discovered\nhave: %v", nodeURLs(g))
	}
}

func TestRedirectBecomesAnEdge(t *testing.T) {
	srv := testSite(t)
	defer srv.Close()
	g, _ := runCrawl(t, srv, nil)

	found := false
	for _, e := range g.Edges() {
		if e.Rel == sitegraph.RelRedirect {
			src, dst := g.Node(e.Src), g.Node(e.Dst)
			if strings.Contains(src.URL, "/contact") && strings.Contains(dst.URL, "/contact-us") {
				found = true
			}
		}
	}
	if !found {
		t.Error("redirect /contact/ -> /contact-us was not recorded as an edge")
	}
}

func TestExternalHostRecordedNotCrawled(t *testing.T) {
	srv := testSite(t)
	defer srv.Close()
	g, _ := runCrawl(t, srv, nil)

	var ext *sitegraph.Node
	for _, n := range g.Nodes() {
		if strings.Contains(n.URL, "external.example.org") {
			ext = n
		}
	}
	if ext == nil {
		t.Fatal("external link was not recorded as a node")
	}
	if !ext.External {
		t.Error("external node not marked external")
	}
	if ext.Fetched {
		t.Error("external node was fetched; it is out of scope")
	}
	if len(g.ExternalHosts()) == 0 {
		t.Error("ExternalHosts() empty")
	}
}

func TestPassiveFindings(t *testing.T) {
	srv := testSite(t)
	defer srv.Close()
	g, _ := runCrawl(t, srv, nil)

	kinds := map[string][]string{}
	for _, f := range g.Findings() {
		kinds[f.Kind] = append(kinds[f.Kind], f.Value)
	}

	if !containsSubstr(kinds["email"], "ops@testsite.local") {
		t.Errorf("email not extracted: %v", kinds["email"])
	}
	if !containsSubstr(kinds["bucket"], "assets-prod") {
		t.Errorf("S3 bucket not extracted: %v", kinds["bucket"])
	}
	if !containsSubstr(kinds["secret-like"], "AKIAIOSFODNN7EXAMPLE") {
		t.Errorf("AWS key not extracted from JS: %v", kinds["secret-like"])
	}
	if !containsSubstr(kinds["internal-host"], "staging.internal.example") {
		t.Errorf("internal host in HTML comment not extracted: %v", kinds["internal-host"])
	}
	if !containsSubstr(kinds["api-endpoint"], "/api/v1/admin") {
		t.Errorf("API path in JS not extracted: %v", kinds["api-endpoint"])
	}
}

func TestFingerprintingAttachesPerNode(t *testing.T) {
	srv := testSite(t)
	defer srv.Close()
	g, _ := runCrawl(t, srv, nil)

	id, ok := g.Lookup(srv.URL + "/")
	if !ok {
		t.Fatal("no root node")
	}
	techs := map[string]bool{}
	for _, tech := range g.Node(id).Techs {
		techs[tech.Name] = true
	}
	for _, want := range []string{"Nginx", "PHP", "WordPress"} {
		if !techs[want] {
			t.Errorf("%s not detected on root; got %v", want, keys(techs))
		}
	}
	if len(g.TechSummary()) == 0 {
		t.Error("TechSummary empty")
	}
}

// Budget enforced and reported.
func TestMaxPagesRespected(t *testing.T) {
	srv := testSite(t)
	defer srv.Close()
	g, rep := runCrawl(t, srv, func(o *Options) {
		o.MaxPages = 3
		o.UseSitemap = false
	})
	fetched := 0
	for _, n := range g.Nodes() {
		if n.Fetched {
			fetched++
		}
	}
	if fetched > 3 {
		t.Errorf("fetched %d pages with MaxPages=3", fetched)
	}
	if rep.Status != "budget-exceeded" {
		t.Errorf("status = %q, want budget-exceeded", rep.Status)
	}
}

// Server 429s twice then succeeds; we should back off and still get the page.
func TestRetryOn429(t *testing.T) {
	var hits int32
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/robots.txt" {
			http.NotFound(w, r)
			return
		}
		if atomic.AddInt32(&hits, 1) <= 2 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<html><head><title>ok</title></head><body>fine</body></html>`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	u, _ := url.Parse(srv.URL)
	fc := fetch.DefaultConfig()
	fc.RateLimit = 1000
	fc.Burst = 32
	fc.Jitter = 0
	fc.MaxRetries = 3

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	g, _, err := Run(ctx, Options{
		Seeds:   []string{srv.URL + "/"},
		Rules:   scope.DefaultRules(u.Hostname()),
		Fetch:   fc,
		Canon:   sitegraph.DefaultCanonOpts(),
		Workers: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	id, _ := g.Lookup(srv.URL + "/")
	if n := g.Node(id); n == nil || n.StatusCode != 200 {
		t.Fatalf("expected a 200 after retries, got %+v", n)
	}
	if atomic.LoadInt32(&hits) < 3 {
		t.Errorf("server saw %d requests, expected retries", hits)
	}
}

// Cancelling mid-crawl returns promptly, with whatever graph exists.
func TestCancellationKeepsPartialGraph(t *testing.T) {
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/robots.txt" {
			http.NotFound(w, r)
			return
		}
		time.Sleep(150 * time.Millisecond)
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprintf(w, `<html><body>
			<a href="/a%d">a</a><a href="/b%d">b</a><a href="/c%d">c</a>
		</body></html>`, time.Now().UnixNano(), time.Now().UnixNano(), time.Now().UnixNano())
	}))
	defer slow.Close()

	u, _ := url.Parse(slow.URL)
	fc := fetch.DefaultConfig()
	fc.RateLimit = 1000
	fc.Burst = 32
	fc.Jitter = 0

	rules := scope.DefaultRules(u.Hostname())
	rules.MaxDepth = 10

	ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
	defer cancel()

	done := make(chan struct{})
	var g *sitegraph.Graph
	var rep Report
	go func() {
		defer close(done)
		g, rep, _ = Run(ctx, Options{
			Seeds: []string{slow.URL + "/"}, Rules: rules, Fetch: fc,
			Canon: sitegraph.DefaultCanonOpts(), Workers: 4,
		})
	}()

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("crawl did not stop promptly after cancellation")
	}
	if g == nil || g.NumNodes() == 0 {
		t.Fatal("cancellation discarded the graph")
	}
	if rep.Status != "interrupted" {
		t.Errorf("status = %q, want interrupted", rep.Status)
	}
}

// --- helpers --------------------------------------------------------------

func nodeURLs(g *sitegraph.Graph) []string {
	out := make([]string, 0, g.NumNodes())
	for _, n := range g.Nodes() {
		out = append(out, n.URL)
	}
	return out
}

func hasSuffixIn(list []string, suffix string) bool {
	for _, s := range list {
		if strings.HasSuffix(s, suffix) {
			return true
		}
	}
	return false
}

func containsSubstr(list []string, want string) bool {
	for _, s := range list {
		if strings.Contains(s, want) {
			return true
		}
	}
	return false
}

func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// pypi.org's sitemap index lists 300k+ URLs. They used to all become nodes
// on a 40-page crawl; now a sitemap contributes at most the page budget.
func TestHugeSitemapIsCapped(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `<html><body><a href="/sitemap.xml">map</a></body></html>`)
	})
	mux.HandleFunc("/sitemap.xml", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		fmt.Fprint(w, `<?xml version="1.0"?><urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">`)
		for i := 0; i < 5000; i++ {
			fmt.Fprintf(w, `<url><loc>http://%s/p/%d</loc></url>`, r.Host, i)
		}
		fmt.Fprint(w, `</urlset>`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	g, _ := runCrawl(t, srv, func(o *Options) { o.MaxPages = 20; o.UseSitemap = false })
	if n := g.NumNodes(); n > 100 {
		t.Errorf("graph has %d nodes from a 5000-URL sitemap with a 20-page budget", n)
	}
}
