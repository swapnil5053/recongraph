// Package builder owns the site graph during a crawl.
//
// One goroutine runs the builder, so the graph needs no mutex. It is also the
// only place scope is applied, so there is one answer to "is this in bounds".
package builder

import (
	"context"
	"net/url"
	"time"

	"github.com/swapnil5053/recongraph/internal/fetch"
	"github.com/swapnil5053/recongraph/internal/frontier"
	"github.com/swapnil5053/recongraph/internal/parse"
	"github.com/swapnil5053/recongraph/internal/passive"
	"github.com/swapnil5053/recongraph/internal/scope"
	"github.com/swapnil5053/recongraph/pkg/sitegraph"
)

// Resolved is a reference that a worker has already resolved and canonicalised.
type Resolved struct {
	URL  string
	Rel  sitegraph.EdgeRel
	Kind sitegraph.NodeKind
}

// Result is one completed unit of crawl work.
type Result struct {
	Task      frontier.Task
	Response  *fetch.Response
	Page      *parse.Page
	Refs      []Resolved
	Findings  []passive.Result
	Techs     []sitegraph.Tech
	FinalURL  string
	Redirects []string
	Err       error
}

// Stats summarise what the builder recorded.
type Stats struct {
	Pages      int
	Errors     int
	Edges      int
	Findings   int
	External   int
	StatusHist map[int]int
}

type findingKey struct {
	node  sitegraph.NodeID
	kind  string
	value string
}

// Builder consumes results and maintains the graph.
type Builder struct {
	graph        *sitegraph.Graph
	rules        *scope.Rules
	stats        Stats
	seenFindings map[findingKey]bool
}

// New creates a builder over a fresh graph.
func New(g *sitegraph.Graph, rules *scope.Rules) *Builder {
	return &Builder{
		graph:        g,
		rules:        rules,
		stats:        Stats{StatusHist: map[int]int{}},
		seenFindings: map[findingKey]bool{},
	}
}

// Graph returns the graph under construction.
func (b *Builder) Graph() *sitegraph.Graph { return b.graph }

// Stats returns counters collected so far.
func (b *Builder) Stats() Stats { return b.stats }

// SeedCandidates admits the starting URLs to the graph and returns the frontier
// candidates for them.
func (b *Builder) SeedCandidates(urls []string) []frontier.Candidate {
	out := make([]frontier.Candidate, 0, len(urls))
	for _, u := range urls {
		id, _ := b.graph.EnsureNode(u, sitegraph.KindPage, 0, false)
		_ = id
		out = append(out, frontier.Candidate{URL: u, Depth: 0, Kind: sitegraph.KindPage})
	}
	return out
}

// Run consumes results until the channel closes or ctx is cancelled.
//
// Order matters: graph, then candidates, then the completion tick. If the tick
// went first the frontier could see an empty queue with nothing in flight and
// end the crawl before the new work arrived.
func (b *Builder) Run(ctx context.Context, results <-chan *Result, f *frontier.Frontier) {
	for {
		select {
		case <-ctx.Done():
			return
		case res, ok := <-results:
			if !ok {
				return
			}
			cands := b.apply(res)

			if len(cands) > 0 {
				select {
				case f.Candidates() <- cands:
				case <-ctx.Done():
					return
				}
			}
			select {
			case f.Complete() <- 1:
			case <-ctx.Done():
				return
			}
		}
	}
}

// apply folds one result into the graph and returns new crawl candidates.
func (b *Builder) apply(res *Result) []frontier.Candidate {
	srcID, _ := b.graph.EnsureNode(res.Task.URL, res.Task.Kind, res.Task.Depth, false)

	if res.Err != nil {
		b.graph.SetNodeResult(srcID, 0, "", 0, "", "", res.Err.Error())
		b.stats.Errors++
		return nil
	}
	if res.Response == nil {
		return nil
	}

	resp := res.Response
	kind := res.Task.Kind
	if k := parse.KindForContentType(resp.ContentType, kind); k != "" {
		kind = k
	}
	title := ""
	if res.Page != nil {
		title = res.Page.Title
	}
	b.graph.SetNodeResult(srcID, resp.StatusCode, resp.ContentType,
		int64(len(resp.Body)), resp.ContentHash(), title, "")
	if n := b.graph.Node(srcID); n != nil {
		n.Kind = kind
		if n.FirstSeen.IsZero() {
			n.FirstSeen = time.Now().UTC()
		}
	}
	b.stats.Pages++
	b.stats.StatusHist[resp.StatusCode]++

	if len(res.Techs) > 0 {
		b.graph.SetTechs(srcID, res.Techs)
	}

	// Keep the redirect chain as edges rather than collapsing to the final URL;
	// that is how you spot /login quietly becoming /sso/login elsewhere.
	prev := srcID
	for _, r := range res.Redirects {
		rid, _ := b.graph.EnsureNode(r, sitegraph.KindPage, res.Task.Depth, !b.internal(r))
		if b.graph.AddEdge(prev, rid, sitegraph.RelRedirect, "") {
			b.stats.Edges++
		}
		prev = rid
	}

	// The comment scanner re-reads text the body scanner already saw, so the
	// same value can arrive twice. Dedupe here rather than making every
	// extractor aware of the others.
	for _, fnd := range res.Findings {
		key := findingKey{srcID, fnd.Kind, fnd.Value}
		if b.seenFindings[key] {
			continue
		}
		b.seenFindings[key] = true
		b.graph.AddFinding(sitegraph.Finding{
			NodeID:   srcID,
			Kind:     fnd.Kind,
			Value:    fnd.Value,
			Evidence: fnd.Evidence,
		})
		b.stats.Findings++
	}

	var cands []frontier.Candidate
	childDepth := res.Task.Depth + 1
	for _, ref := range res.Refs {
		decision := b.rules.Check(ref.URL, childDepth)
		if decision == scope.Reject {
			continue
		}
		external := decision == scope.Record && !b.internal(ref.URL)
		dstID, _ := b.graph.EnsureNode(ref.URL, ref.Kind, childDepth, external)
		if b.graph.AddEdge(srcID, dstID, ref.Rel, ref.Context()) {
			b.stats.Edges++
		}
		if external {
			b.stats.External++
		}
		if decision == scope.Crawl {
			cands = append(cands, frontier.Candidate{
				URL:   ref.URL,
				Depth: childDepth,
				Kind:  ref.Kind,
			})
		}
	}
	return cands
}

func (b *Builder) internal(rawurl string) bool {
	u, err := parseHost(rawurl)
	if err != nil {
		return false
	}
	return b.rules.InScopeHost(u)
}

// Context is the edge context for a resolved reference. A method so it can
// start carrying link text without touching call sites.
func (r Resolved) Context() string { return "" }

func parseHost(rawurl string) (string, error) {
	u, err := url.Parse(rawurl)
	if err != nil {
		return "", err
	}
	return u.Hostname(), nil
}
