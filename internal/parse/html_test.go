package parse

import (
	"net/url"
	"strings"
	"testing"

	"github.com/swapnil5053/recongraph/pkg/sitegraph"
)

func mustParse(t *testing.T, body string) *Page {
	t.Helper()
	base, _ := url.Parse("https://example.com/dir/page.html")
	p, err := HTML([]byte(body), base)
	if err != nil {
		t.Fatalf("HTML: %v", err)
	}
	return p
}

func refs(p *Page) map[string]sitegraph.EdgeRel {
	out := map[string]sitegraph.EdgeRel{}
	for _, c := range p.Candidates {
		out[c.Ref] = c.Rel
	}
	return out
}

// Coverage across every element we extract from.
func TestExtractionCoverage(t *testing.T) {
	p := mustParse(t, `<!doctype html><html><head>
<title>  Test   Page </title>
<link rel="stylesheet" href="/style.css">
<link rel="icon" href="/favicon.ico">
<link rel="canonical" href="/canonical">
<link rel="manifest" href="/app.webmanifest">
<script src="/app.js"></script>
<meta name="generator" content="TestCMS 1.0">
<meta property="og:title" content="OG">
<meta http-equiv="refresh" content="0; url=/redirected">
</head><body>
<a href="/anchor">anchor</a>
<area href="/area">
<img src="/img.png" srcset="/img-2x.png 2x, /img-3x.png 3x" data-src="/lazy.png">
<picture><source srcset="/pic.webp"></picture>
<iframe src="/frame"></iframe>
<embed src="/thing.swf">
<object data="/obj.pdf"></object>
<video src="/v.mp4" poster="/poster.jpg"><track src="/subs.vtt"></video>
<audio src="/a.mp3"></audio>
<form action="/submit" method="post">
  <input name="user" type="text"><input name="pass" type="password">
  <select name="role"></select>
</form>
<div class="card wp-block">x</div>
<script>var apiBase = "/api/v2";</script>
<!-- internal note: see https://staging.example.com/deploy -->
</body></html>`)

	if p.Title != "Test Page" {
		t.Errorf("Title = %q, want %q (whitespace should collapse)", p.Title, "Test Page")
	}

	r := refs(p)
	want := map[string]sitegraph.EdgeRel{
		"/style.css":       sitegraph.RelStylesheet,
		"/favicon.ico":     sitegraph.RelImage,
		"/canonical":       sitegraph.RelHref,
		"/app.webmanifest": sitegraph.RelHref,
		"/app.js":          sitegraph.RelScript,
		"/redirected":      sitegraph.RelRedirect,
		"/anchor":          sitegraph.RelHref,
		"/area":            sitegraph.RelHref,
		"/img.png":         sitegraph.RelImage,
		"/img-2x.png":      sitegraph.RelImage,
		"/img-3x.png":      sitegraph.RelImage,
		"/lazy.png":        sitegraph.RelImage,
		"/pic.webp":        sitegraph.RelImage,
		"/frame":           sitegraph.RelIFrame,
		"/thing.swf":       sitegraph.RelObject,
		"/obj.pdf":         sitegraph.RelObject,
		"/v.mp4":           sitegraph.RelMedia,
		"/poster.jpg":      sitegraph.RelImage,
		"/subs.vtt":        sitegraph.RelMedia,
		"/a.mp3":           sitegraph.RelMedia,
		"/submit":          sitegraph.RelFormAction,
	}
	for ref, rel := range want {
		got, ok := r[ref]
		if !ok {
			t.Errorf("missing reference %s", ref)
			continue
		}
		if got != rel {
			t.Errorf("%s: rel = %s, want %s", ref, got, rel)
		}
	}

	if p.Metas["generator"] != "TestCMS 1.0" {
		t.Errorf("meta generator = %q", p.Metas["generator"])
	}
	if p.Metas["og:title"] != "OG" {
		t.Errorf("og:title not captured (property= should work like name=)")
	}
	if len(p.ScriptSrcs) != 1 || p.ScriptSrcs[0] != "/app.js" {
		t.Errorf("ScriptSrcs = %v", p.ScriptSrcs)
	}
	if len(p.InlineScripts) != 1 || !strings.Contains(p.InlineScripts[0], "/api/v2") {
		t.Errorf("InlineScripts = %v", p.InlineScripts)
	}
	if len(p.Comments) != 1 || !strings.Contains(p.Comments[0], "staging.example.com") {
		t.Errorf("Comments = %v (comments are where staging hosts leak)", p.Comments)
	}
	if len(p.Forms) != 1 {
		t.Fatalf("Forms = %v", p.Forms)
	}
	f := p.Forms[0]
	if f.Method != "POST" || f.Action != "/submit" {
		t.Errorf("form = %+v", f)
	}
	if len(f.Inputs) != 3 {
		t.Errorf("form inputs = %v, want 3", f.Inputs)
	}
}

// <base href> overrides the document URL, or relative refs land on the wrong
// node.
func TestBaseHrefOverride(t *testing.T) {
	p := mustParse(t, `<html><head><base href="https://cdn.example.net/assets/"></head>
<body><a href="x.html">x</a></body></html>`)
	if p.Base == nil || p.Base.String() != "https://cdn.example.net/assets/" {
		t.Fatalf("Base = %v, want the <base href>", p.Base)
	}
	resolved, err := sitegraph.Resolve(p.Base, "x.html", sitegraph.DefaultCanonOpts())
	if err != nil {
		t.Fatal(err)
	}
	if want := "https://cdn.example.net/assets/x.html"; resolved != want {
		t.Errorf("resolved = %q, want %q", resolved, want)
	}
}

func TestMetaRefreshVariants(t *testing.T) {
	cases := map[string]string{
		"0; url=/a":    "/a",
		"5;URL=/b":     "/b",
		`0; url='/c'`:  "/c",
		`10; url="/d"`: "/d",
		"3":            "",
		"":             "",
	}
	for content, want := range cases {
		if got := refreshURL(content); got != want {
			t.Errorf("refreshURL(%q) = %q, want %q", content, got, want)
		}
	}
}

func TestMalformedHTMLDoesNotPanic(t *testing.T) {
	inputs := []string{
		``,
		`<html`,
		`<a href=>x</a>`,
		`<<<>>>`,
		`<html><body><a href="/ok">ok</a>`,
		strings.Repeat(`<div>`, 500) + `<a href="/deep">d</a>`,
	}
	base, _ := url.Parse("https://example.com/")
	for i, in := range inputs {
		if _, err := HTML([]byte(in), base); err != nil {
			t.Errorf("input %d returned error: %v", i, err)
		}
	}
}

func TestKindForContentType(t *testing.T) {
	cases := map[string]sitegraph.NodeKind{
		"text/html; charset=utf-8": sitegraph.KindPage,
		"application/javascript":   sitegraph.KindScript,
		"text/css":                 sitegraph.KindStylesheet,
		"image/png":                sitegraph.KindImage,
		"video/mp4":                sitegraph.KindMedia,
		"application/json":         sitegraph.KindAPI,
		"application/pdf":          sitegraph.KindDocument,
		"application/octet-stream": sitegraph.KindOther,
	}
	for ct, want := range cases {
		if got := KindForContentType(ct, sitegraph.KindOther); got != want {
			t.Errorf("KindForContentType(%q) = %q, want %q", ct, got, want)
		}
	}
}

func TestSitemapParsing(t *testing.T) {
	pages, indexes := Sitemap([]byte(`<?xml version="1.0"?>
<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">
  <url><loc>https://example.com/a</loc></url>
  <url><loc>https://example.com/b</loc></url>
</urlset>`))
	if len(pages) != 2 || len(indexes) != 0 {
		t.Errorf("urlset: pages=%v indexes=%v", pages, indexes)
	}

	pages, indexes = Sitemap([]byte(`<?xml version="1.0"?>
<sitemapindex xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">
  <sitemap><loc>https://example.com/sitemap-1.xml</loc></sitemap>
</sitemapindex>`))
	if len(pages) != 0 || len(indexes) != 1 {
		t.Errorf("sitemapindex: pages=%v indexes=%v", pages, indexes)
	}

	if p, i := Sitemap([]byte("not xml at all")); len(p)+len(i) != 0 {
		t.Errorf("garbage input produced results: %v %v", p, i)
	}
}
