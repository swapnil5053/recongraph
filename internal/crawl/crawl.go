// Package crawl wires the pipeline: frontier -> workers -> builder -> frontier.
//
// Backpressure is just the bounded channels. A slow builder fills the result
// channel, workers block on send, they stop pulling tasks, the frontier backs
// up. No explicit coordination needed.
package crawl

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/swapnil5053/recongraph/internal/builder"
	"github.com/swapnil5053/recongraph/internal/fetch"
	"github.com/swapnil5053/recongraph/internal/fingerprint"
	"github.com/swapnil5053/recongraph/internal/frontier"
	"github.com/swapnil5053/recongraph/internal/parse"
	"github.com/swapnil5053/recongraph/internal/passive"
	"github.com/swapnil5053/recongraph/internal/scope"
	"github.com/swapnil5053/recongraph/pkg/sitegraph"
)

// Options configures a crawl.
type Options struct {
	Seeds       []string
	Rules       *scope.Rules
	Fetch       fetch.Config
	Canon       sitegraph.CanonOpts
	Workers     int
	MaxPages    int
	MaxQueue    int
	Fingerprint bool
	Passive     bool
	FollowJS    bool
	UseSitemap  bool
	Verbose     bool
	// Progress, if set, is called from the builder goroutine as pages complete.
	Progress func(done int, url string, status int)
}

// Report summarises a completed crawl.
type Report struct {
	Frontier frontier.Stats
	Builder  builder.Stats
	Duration time.Duration
	Status   string
}

// Run executes a crawl and returns the graph. On cancellation it still returns
// whatever was built, tagged "interrupted".
func Run(ctx context.Context, opts Options) (*sitegraph.Graph, Report, error) {
	start := time.Now()
	if opts.Workers < 1 {
		opts.Workers = 8
	}
	if opts.Rules == nil {
		return nil, Report{}, errors.New("crawl: no scope rules")
	}
	if len(opts.Seeds) == 0 {
		return nil, Report{}, errors.New("crawl: no seed URLs")
	}

	client, err := fetch.New(opts.Fetch)
	if err != nil {
		return nil, Report{}, err
	}

	var engine *fingerprint.Engine
	if opts.Fingerprint {
		engine, err = fingerprint.Load()
		if err != nil {
			return nil, Report{}, fmt.Errorf("loading signatures: %w", err)
		}
	}

	g := sitegraph.New(opts.Seeds[0])
	g.Tool = "recongraph"
	g.StartedAt = time.Now().UTC()

	b := builder.New(g, opts.Rules)
	f := frontier.New(frontier.Config{
		Workers:  opts.Workers,
		MaxPages: opts.MaxPages,
		MaxQueue: opts.MaxQueue,
	})

	g.Seeds = append([]string(nil), opts.Seeds...)
	f.Seed(b.SeedCandidates(opts.Seeds))
	if opts.UseSitemap {
		f.Seed(b.SitemapCandidates(discoverFromSitemaps(ctx, client, opts)))
	}

	results := make(chan *builder.Result, opts.Workers*2)

	var wg sync.WaitGroup
	for i := 0; i < opts.Workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			w := &worker{client: client, engine: engine, opts: opts}
			for task := range f.Ready() {
				res := w.process(ctx, task)
				select {
				case results <- res:
				case <-ctx.Done():
					return
				}
			}
		}()
	}

	// Builder gets its own goroutine and is the only writer to the graph.
	builderDone := make(chan struct{})
	go func() {
		defer close(builderDone)
		b.Run(ctx, results, f)
	}()

	fstats := f.Run(ctx)
	wg.Wait()
	close(results)
	<-builderDone

	g.FinishedAt = time.Now().UTC()
	status := "complete"
	if ctx.Err() != nil {
		status = "interrupted"
	} else if fstats.DroppedBudget > 0 || fstats.DroppedQueue > 0 {
		status = "budget-exceeded"
	}
	g.Status = status

	return g, Report{
		Frontier: fstats,
		Builder:  b.Stats(),
		Duration: time.Since(start),
		Status:   status,
	}, nil
}

// worker handles one task at a time and holds no shared state; everything it
// learns leaves over the result channel.
type worker struct {
	client *fetch.Client
	engine *fingerprint.Engine
	opts   Options
}

func (w *worker) process(ctx context.Context, task frontier.Task) *builder.Result {
	res := &builder.Result{Task: task}

	resp, err := w.client.Get(ctx, task.URL)
	if err != nil {
		res.Err = err
		return res
	}
	res.Response = resp
	res.FinalURL = resp.URL
	res.Redirects = w.canonicalizeAll(resp.Redirects)

	base, err := url.Parse(resp.URL)
	if err != nil {
		base, _ = url.Parse(task.URL)
	}

	switch {
	case parse.IsHTML(resp.ContentType):
		w.processHTML(res, resp, base)
	case parse.IsScript(resp.ContentType) || isJSPath(base.Path):
		w.processScript(res, resp, base)
	case parse.IsXML(resp.ContentType):
		w.processXML(res, resp, base)
	case strings.Contains(resp.ContentType, "css") || strings.HasSuffix(base.Path, ".css"):
		if w.opts.Passive {
			res.Findings = passive.ExtractCSS(resp.Body)
		}
	default:
		if w.opts.Passive {
			res.Findings = passive.Extract(resp.Body, false)
		}
	}

	if w.engine != nil {
		in := fingerprint.Input{Header: resp.Header, URLPath: base.Path}
		// Body matchers only mean something on pages, scripts and styles. A
		// sitemap listing a package called "fontawesome" isn't Font Awesome.
		if parse.IsHTML(resp.ContentType) || parse.IsScript(resp.ContentType) ||
			strings.Contains(resp.ContentType, "css") || isJSPath(base.Path) {
			in.Body = resp.Body
		}
		if res.Page != nil {
			in.Metas = res.Page.Metas
			in.ScriptSrcs = res.Page.ScriptSrcs
			in.Classes = res.Page.CSSClasses
		}
		res.Techs = w.engine.Match(in)
	}

	return res
}

func (w *worker) processHTML(res *builder.Result, resp *fetch.Response, base *url.URL) {
	page, err := parse.HTML(resp.Body, base)
	if err != nil {
		return
	}
	res.Page = page

	effective := page.Base
	if effective == nil {
		effective = base
	}

	for _, c := range page.Candidates {
		canon, err := sitegraph.Resolve(effective, c.Ref, w.opts.Canon)
		if err != nil {
			continue
		}
		res.Refs = append(res.Refs, builder.Resolved{URL: canon, Rel: c.Rel, Kind: c.Kind})
	}

	if w.opts.Passive {
		res.Findings = append(res.Findings, passive.Extract(resp.Body, false)...)
		res.Findings = append(res.Findings, passive.ExtractFromComments(page.Comments)...)
		// Inline scripts, not just external ones.
		for _, s := range page.InlineScripts {
			res.Findings = append(res.Findings, passive.Extract([]byte(s), true)...)
			if w.opts.FollowJS {
				w.addJSRefs(res, effective, []byte(s))
			}
		}
	}
}

func (w *worker) processScript(res *builder.Result, resp *fetch.Response, base *url.URL) {
	if w.opts.Passive {
		res.Findings = passive.Extract(resp.Body, true)
	}
	if w.opts.FollowJS {
		w.addJSRefs(res, base, resp.Body)
	}
}

func (w *worker) processXML(res *builder.Result, resp *fetch.Response, base *url.URL) {
	pages, indexes := parse.Sitemap(resp.Body)
	// A big site's sitemap index can list hundreds of thousands of URLs
	// (pypi.org lists 300k+). Recording all of them as nodes blows up the
	// graph for pages the page budget will never reach anyway.
	limit := w.opts.MaxPages
	if limit <= 0 {
		limit = 10000
	}
	refs := append(pages, indexes...)
	if len(refs) > limit {
		refs = refs[:limit]
	}
	for _, raw := range refs {
		canon, err := sitegraph.Resolve(base, raw, w.opts.Canon)
		if err != nil {
			continue
		}
		res.Refs = append(res.Refs, builder.Resolved{
			URL: canon, Rel: sitegraph.RelSitemap, Kind: sitegraph.KindPage,
		})
	}
	if w.opts.Passive {
		res.Findings = append(res.Findings, passive.Extract(resp.Body, false)...)
	}
}

// addJSRefs turns endpoints found in JavaScript into edges, so an endpoint no
// anchor points at still lands in the map.
func (w *worker) addJSRefs(res *builder.Result, base *url.URL, body []byte) {
	for _, ep := range passive.JSEndpoints(body) {
		canon, err := sitegraph.Resolve(base, ep, w.opts.Canon)
		if err != nil {
			continue
		}
		res.Refs = append(res.Refs, builder.Resolved{
			URL: canon, Rel: sitegraph.RelJSEndpoint, Kind: sitegraph.KindAPI,
		})
	}
}

func (w *worker) canonicalizeAll(raws []string) []string {
	out := make([]string, 0, len(raws))
	for _, r := range raws {
		if c, err := sitegraph.Canonicalize(r, w.opts.Canon); err == nil {
			out = append(out, c)
		}
	}
	return out
}

// discoverFromSitemaps collects page URLs from /sitemap.xml and any
// Sitemap: lines in robots.txt before the crawl starts.
//
// Sitemap indexes are followed here, not handed to the frontier, and only a
// few files deep: on a site like pypi.org the index points at dozens of
// files with 300k+ URLs, and fetching them as ordinary pages used up the
// whole page budget on XML. Sitemap pages get at most half the budget so
// the link crawl still has room.
func discoverFromSitemaps(ctx context.Context, client *fetch.Client, opts Options) []string {
	const maxSitemapFiles = 4
	limit := 1000
	if opts.MaxPages > 0 {
		limit = opts.MaxPages / 2
		if limit < 1 {
			limit = 1
		}
	}

	var extra []string
	seen := map[string]bool{}
	fetched := 0

	for _, seed := range opts.Seeds {
		u, err := url.Parse(seed)
		if err != nil {
			continue
		}
		queue := append([]string{u.Scheme + "://" + u.Host + "/sitemap.xml"}, client.Sitemaps(ctx, u)...)

		for len(queue) > 0 && fetched < maxSitemapFiles && len(extra) < limit {
			sm := queue[0]
			queue = queue[1:]
			if seen[sm] {
				continue
			}
			seen[sm] = true
			fetched++
			resp, err := client.Get(ctx, sm)
			if err != nil || resp.StatusCode != 200 {
				continue
			}
			pages, indexes := parse.Sitemap(resp.Body)
			queue = append(queue, indexes...)
			for _, p := range pages {
				canon, err := sitegraph.Canonicalize(p, opts.Canon)
				if err != nil || seen[canon] {
					continue
				}
				if opts.Rules.Check(canon, 1) != scope.Crawl {
					continue
				}
				seen[canon] = true
				extra = append(extra, canon)
				if len(extra) >= limit {
					break
				}
			}
		}
	}
	return extra
}

func isJSPath(p string) bool {
	return len(p) > 3 && p[len(p)-3:] == ".js"
}
