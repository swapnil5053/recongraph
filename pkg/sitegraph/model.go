// Package sitegraph is the data model: a directed graph of a target's web
// surface. Nodes are URLs, edges are references between them.
//
// Standard library only, so anything reading ReconGraph output can import it
// without dragging in the crawler.
package sitegraph

import (
	"sort"
	"time"
)

// NodeKind classifies what a node represents.
type NodeKind string

const (
	KindPage       NodeKind = "page"
	KindScript     NodeKind = "script"
	KindStylesheet NodeKind = "stylesheet"
	KindImage      NodeKind = "image"
	KindMedia      NodeKind = "media"
	KindDocument   NodeKind = "document"
	KindForm       NodeKind = "form"
	KindAPI        NodeKind = "api"
	KindBucket     NodeKind = "bucket"
	KindExternal   NodeKind = "external"
	KindOther      NodeKind = "other"
)

// EdgeRel describes why one node references another.
type EdgeRel string

const (
	RelHref       EdgeRel = "href"
	RelScript     EdgeRel = "script"
	RelStylesheet EdgeRel = "stylesheet"
	RelImage      EdgeRel = "image"
	RelMedia      EdgeRel = "media"
	RelIFrame     EdgeRel = "iframe"
	RelObject     EdgeRel = "object"
	RelFormAction EdgeRel = "form-action"
	RelRedirect   EdgeRel = "redirect"
	RelJSEndpoint EdgeRel = "js-endpoint"
	RelComment    EdgeRel = "comment"
	RelSitemap    EdgeRel = "sitemap"
	RelRobots     EdgeRel = "robots"
)

// NodeID is graph-local. URLs are interned to ints to keep adjacency compact.
type NodeID uint32

// Tech is a detected technology on a specific node.
type Tech struct {
	Name       string   `json:"name"`
	Version    string   `json:"version,omitempty"`
	Categories []string `json:"categories,omitempty"`
	Confidence int      `json:"confidence"`
	Evidence   []string `json:"evidence,omitempty"`
}

// Node is a single URL in the graph.
type Node struct {
	ID            NodeID    `json:"id"`
	URL           string    `json:"url"` // canonical form
	Kind          NodeKind  `json:"kind"`
	Scheme        string    `json:"scheme"`
	Host          string    `json:"host"`
	Path          string    `json:"path"`
	Depth         int       `json:"depth"`
	External      bool      `json:"external"`
	Fetched       bool      `json:"fetched"`
	StatusCode    int       `json:"status_code,omitempty"`
	ContentType   string    `json:"content_type,omitempty"`
	ContentLength int64     `json:"content_length,omitempty"`
	ContentHash   string    `json:"content_hash,omitempty"`
	Title         string    `json:"title,omitempty"`
	Error         string    `json:"error,omitempty"`
	Techs         []Tech    `json:"techs,omitempty"`
	FirstSeen     time.Time `json:"first_seen"`
}

// Edge is a directed reference from Src to Dst.
type Edge struct {
	Src     NodeID  `json:"src"`
	Dst     NodeID  `json:"dst"`
	Rel     EdgeRel `json:"rel"`
	Context string  `json:"context,omitempty"`
}

// Finding is a passive-discovery result attached to the node it was found on.
type Finding struct {
	NodeID   NodeID `json:"node_id"`
	Kind     string `json:"kind"` // email, s3-bucket, api-endpoint, social, comment, ...
	Value    string `json:"value"`
	Evidence string `json:"evidence,omitempty"`
}

// Graph is the in-memory site graph. Not safe for concurrent use: the builder
// goroutine owns it for the duration of a crawl.
type Graph struct {
	Target     string    `json:"target"`
	StartedAt  time.Time `json:"started_at"`
	FinishedAt time.Time `json:"finished_at"`
	Status     string    `json:"status"` // complete | interrupted | budget-exceeded
	Tool       string    `json:"tool"`

	nodes    []*Node
	index    map[string]NodeID
	edges    []Edge
	edgeSeen map[edgeKey]struct{}
	out      map[NodeID][]int // node -> indices into edges
	in       map[NodeID][]int

	findings []Finding
}

type edgeKey struct {
	src, dst NodeID
	rel      EdgeRel
}

// New returns an empty graph for target.
func New(target string) *Graph {
	return &Graph{
		Target:   target,
		Status:   "complete",
		index:    make(map[string]NodeID),
		edgeSeen: make(map[edgeKey]struct{}),
		out:      make(map[NodeID][]int),
		in:       make(map[NodeID][]int),
	}
}

// EnsureNode returns the node for a canonical URL, creating it if absent.
func (g *Graph) EnsureNode(canonURL string, kind NodeKind, depth int, external bool) (NodeID, bool) {
	if id, ok := g.index[canonURL]; ok {
		n := g.nodes[id]
		// Shallowest route wins.
		if depth < n.Depth {
			n.Depth = depth
		}
		// "other" can be upgraded once we know more (e.g. after a fetch).
		if n.Kind == KindOther && kind != KindOther {
			n.Kind = kind
		}
		return id, false
	}
	id := NodeID(len(g.nodes))
	scheme, host, path := splitURL(canonURL)
	g.nodes = append(g.nodes, &Node{
		ID:        id,
		URL:       canonURL,
		Kind:      kind,
		Scheme:    scheme,
		Host:      host,
		Path:      path,
		Depth:     depth,
		External:  external,
		FirstSeen: time.Now().UTC(),
	})
	g.index[canonURL] = id
	return id, true
}

// AddEdge records a directed reference. Duplicate (src,dst,rel) triples
// collapse, so a nav link on 500 pages is one edge per source, not 500.
func (g *Graph) AddEdge(src, dst NodeID, rel EdgeRel, context string) bool {
	k := edgeKey{src, dst, rel}
	if _, ok := g.edgeSeen[k]; ok {
		return false
	}
	g.edgeSeen[k] = struct{}{}
	idx := len(g.edges)
	g.edges = append(g.edges, Edge{Src: src, Dst: dst, Rel: rel, Context: context})
	g.out[src] = append(g.out[src], idx)
	g.in[dst] = append(g.in[dst], idx)
	return true
}

// AddFinding attaches a passive-discovery result to a node.
func (g *Graph) AddFinding(f Finding) { g.findings = append(g.findings, f) }

// Lookup returns the node ID for a canonical URL.
func (g *Graph) Lookup(canonURL string) (NodeID, bool) {
	id, ok := g.index[canonURL]
	return id, ok
}

// Node returns the node with the given ID, or nil.
func (g *Graph) Node(id NodeID) *Node {
	if int(id) >= len(g.nodes) {
		return nil
	}
	return g.nodes[id]
}

func (g *Graph) Nodes() []*Node      { return g.nodes }
func (g *Graph) Edges() []Edge       { return g.edges }
func (g *Graph) Findings() []Finding { return g.findings }
func (g *Graph) NumNodes() int       { return len(g.nodes) }
func (g *Graph) NumEdges() int       { return len(g.edges) }

// InDegree is the cheapest importance signal available: nav and hub pages
// score high, forgotten corners score one.
func (g *Graph) InDegree(id NodeID) int  { return len(g.in[id]) }
func (g *Graph) OutDegree(id NodeID) int { return len(g.out[id]) }

// OutEdges returns the edges leaving a node.
func (g *Graph) OutEdges(id NodeID) []Edge {
	idxs := g.out[id]
	es := make([]Edge, 0, len(idxs))
	for _, i := range idxs {
		es = append(es, g.edges[i])
	}
	return es
}

// InEdges returns the edges arriving at a node.
func (g *Graph) InEdges(id NodeID) []Edge {
	idxs := g.in[id]
	es := make([]Edge, 0, len(idxs))
	for _, i := range idxs {
		es = append(es, g.edges[i])
	}
	return es
}

// SetNodeResult records the outcome of fetching a node.
func (g *Graph) SetNodeResult(id NodeID, status int, ctype string, length int64, hash, title string, err string) {
	n := g.Node(id)
	if n == nil {
		return
	}
	n.Fetched = true
	n.StatusCode = status
	n.ContentType = ctype
	n.ContentLength = length
	n.ContentHash = hash
	if title != "" {
		n.Title = title
	}
	n.Error = err
}

// SetTechs attaches fingerprint results to a node.
func (g *Graph) SetTechs(id NodeID, techs []Tech) {
	if n := g.Node(id); n != nil {
		n.Techs = techs
	}
}

// --- Analysis -------------------------------------------------------------

// Orphans are internal pages and endpoints reachable only through a non-anchor
// edge: a script, a sitemap, a comment. The forgotten-admin-panel shape.
func (g *Graph) Orphans() []*Node {
	// A stylesheet with no anchor pointing at it is normal. A page or an API
	// endpoint with none is worth looking at.
	navigable := map[NodeKind]bool{
		KindPage: true, KindAPI: true, KindForm: true, KindDocument: true,
	}
	var out []*Node
	for _, n := range g.nodes {
		if n.External || !navigable[n.Kind] {
			continue
		}
		// Seeds have no inbound edges by construction.
		if n.Depth == 0 {
			continue
		}
		anchored := false
		for _, e := range g.InEdges(n.ID) {
			if e.Rel == RelHref {
				anchored = true
				break
			}
		}
		if !anchored {
			out = append(out, n)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].URL < out[j].URL })
	return out
}

// Hubs returns internal nodes sorted by in-degree, highest first.
func (g *Graph) Hubs(limit int) []*Node {
	cp := make([]*Node, 0, len(g.nodes))
	for _, n := range g.nodes {
		if !n.External {
			cp = append(cp, n)
		}
	}
	sort.Slice(cp, func(i, j int) bool {
		di, dj := g.InDegree(cp[i].ID), g.InDegree(cp[j].ID)
		if di != dj {
			return di > dj
		}
		return cp[i].URL < cp[j].URL
	})
	if limit > 0 && len(cp) > limit {
		cp = cp[:limit]
	}
	return cp
}

// ExternalHosts counts third-party hosts by how many nodes reference them.
func (g *Graph) ExternalHosts() map[string]int {
	counts := map[string]int{}
	for _, n := range g.nodes {
		if n.External && n.Host != "" {
			counts[n.Host] += g.InDegree(n.ID)
		}
	}
	return counts
}

// ShortestPath is a BFS from src to dst, nil if unreachable. Answers "how many
// clicks from the front door is this".
func (g *Graph) ShortestPath(src, dst NodeID) []NodeID {
	if src == dst {
		return []NodeID{src}
	}
	prev := map[NodeID]NodeID{}
	seen := map[NodeID]bool{src: true}
	queue := []NodeID{src}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		for _, e := range g.OutEdges(cur) {
			if seen[e.Dst] {
				continue
			}
			seen[e.Dst] = true
			prev[e.Dst] = cur
			if e.Dst == dst {
				path := []NodeID{dst}
				for at := dst; at != src; {
					at = prev[at]
					path = append([]NodeID{at}, path...)
				}
				return path
			}
			queue = append(queue, e.Dst)
		}
	}
	return nil
}

// Components returns weakly-connected components, largest first. More than one
// under a single host usually means more than one application.
func (g *Graph) Components() [][]NodeID {
	adj := make(map[NodeID][]NodeID, len(g.nodes))
	for _, e := range g.edges {
		adj[e.Src] = append(adj[e.Src], e.Dst)
		adj[e.Dst] = append(adj[e.Dst], e.Src)
	}
	seen := make(map[NodeID]bool, len(g.nodes))
	var comps [][]NodeID
	for _, n := range g.nodes {
		if seen[n.ID] {
			continue
		}
		var comp []NodeID
		stack := []NodeID{n.ID}
		seen[n.ID] = true
		for len(stack) > 0 {
			cur := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			comp = append(comp, cur)
			for _, nb := range adj[cur] {
				if !seen[nb] {
					seen[nb] = true
					stack = append(stack, nb)
				}
			}
		}
		comps = append(comps, comp)
	}
	sort.Slice(comps, func(i, j int) bool { return len(comps[i]) > len(comps[j]) })
	return comps
}

// TechSummary counts detected technologies across all nodes.
func (g *Graph) TechSummary() map[string]int {
	out := map[string]int{}
	for _, n := range g.nodes {
		for _, t := range n.Techs {
			key := t.Name
			if t.Version != "" {
				key += " " + t.Version
			}
			out[key]++
		}
	}
	return out
}
