package sitegraph

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"
)

// Snapshot is the serialisable form of a Graph. The in-memory Graph keeps
// private adjacency indexes; a snapshot is the flat, portable record.
type Snapshot struct {
	Tool       string    `json:"tool"`
	Target     string    `json:"target"`
	StartedAt  string    `json:"started_at"`
	FinishedAt string    `json:"finished_at"`
	Status     string    `json:"status"`
	Seeds      []string  `json:"seeds,omitempty"`
	Nodes      []*Node   `json:"nodes"`
	Edges      []Edge    `json:"edges"`
	Findings   []Finding `json:"findings,omitempty"`
}

const timeLayout = "2006-01-02T15:04:05Z"

// ToSnapshot flattens the graph for serialisation.
func (g *Graph) ToSnapshot() *Snapshot {
	return &Snapshot{
		Tool:       g.Tool,
		Target:     g.Target,
		StartedAt:  g.StartedAt.UTC().Format(timeLayout),
		FinishedAt: g.FinishedAt.UTC().Format(timeLayout),
		Status:     g.Status,
		Seeds:      g.Seeds,
		Nodes:      g.nodes,
		Edges:      g.edges,
		Findings:   g.findings,
	}
}

// FromSnapshot rebuilds a Graph (including adjacency indexes) from a snapshot,
// so that a graph loaded from disk supports the same analysis as a fresh crawl.
func FromSnapshot(s *Snapshot) *Graph {
	g := New(s.Target)
	g.Tool = s.Tool
	g.Status = s.Status
	g.Seeds = s.Seeds
	g.StartedAt, _ = time.Parse(timeLayout, s.StartedAt)
	g.FinishedAt, _ = time.Parse(timeLayout, s.FinishedAt)
	g.nodes = s.Nodes
	// Adjacency is indexed by node ID, so it has to exist before AddEdge.
	g.out = make([][]int32, len(s.Nodes))
	g.in = make([][]int32, len(s.Nodes))
	for _, n := range s.Nodes {
		g.index[n.URL] = n.ID
	}
	for _, e := range s.Edges {
		g.AddEdge(e.Src, e.Dst, e.Rel, e.Context)
	}
	g.findings = s.Findings
	return g
}

// WriteJSON writes the graph as a JSON document.
func (g *Graph) WriteJSON(w io.Writer, indent bool) error {
	enc := json.NewEncoder(w)
	if indent {
		enc.SetIndent("", "  ")
	}
	return enc.Encode(g.ToSnapshot())
}

// WriteAdjacencyJSON writes a compact adjacency-list view: url -> [{to, rel}].
// This is the shape most graph libraries want to ingest.
func (g *Graph) WriteAdjacencyJSON(w io.Writer) error {
	type ref struct {
		To  string  `json:"to"`
		Rel EdgeRel `json:"rel"`
	}
	adj := make(map[string][]ref, len(g.nodes))
	for _, n := range g.nodes {
		adj[n.URL] = []ref{}
	}
	for _, e := range g.edges {
		src, dst := g.Node(e.Src), g.Node(e.Dst)
		if src == nil || dst == nil {
			continue
		}
		adj[src.URL] = append(adj[src.URL], ref{To: dst.URL, Rel: e.Rel})
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(adj)
}

var kindColor = map[NodeKind]string{
	KindPage:       "#2563eb",
	KindScript:     "#d97706",
	KindStylesheet: "#7c3aed",
	KindImage:      "#059669",
	KindMedia:      "#0891b2",
	KindDocument:   "#4b5563",
	KindForm:       "#dc2626",
	KindAPI:        "#db2777",
	KindBucket:     "#ca8a04",
	KindExternal:   "#94a3b8",
	KindOther:      "#94a3b8",
}

// WriteDOT writes the graph in Graphviz DOT format.
//
// We deliberately do not implement graph layout: DOT is the output, and SVG is
// produced by shelling out to `dot` if it is installed. Layout algorithms are a
// solved problem and not what this tool is demonstrating.
func (g *Graph) WriteDOT(w io.Writer) error {
	bw := &errWriter{w: w}
	bw.printf("digraph recongraph {\n")
	bw.printf("  rankdir=LR;\n")
	bw.printf("  graph [fontname=\"Helvetica\", bgcolor=\"white\", splines=true, overlap=false];\n")
	bw.printf("  node  [fontname=\"Helvetica\", fontsize=9, shape=box, style=\"rounded,filled\", fillcolor=\"#f8fafc\", color=\"#cbd5e1\"];\n")
	bw.printf("  edge  [fontname=\"Helvetica\", fontsize=7, color=\"#94a3b8\", arrowsize=0.6];\n")
	bw.printf("  labelloc=\"t\";\n")
	bw.printf("  label=%s;\n", dotQuote(fmt.Sprintf("ReconGraph: %s (%d nodes, %d edges)", g.Target, len(g.nodes), len(g.edges))))

	// Group internal nodes by host so a multi-host crawl reads as clusters.
	hosts := map[string][]*Node{}
	var order []string
	for _, n := range g.nodes {
		h := n.Host
		if n.External {
			h = "external"
		}
		if _, ok := hosts[h]; !ok {
			order = append(order, h)
		}
		hosts[h] = append(hosts[h], n)
	}
	sort.Strings(order)

	for i, h := range order {
		bw.printf("  subgraph cluster_%d {\n", i)
		bw.printf("    label=%s;\n", dotQuote(h))
		bw.printf("    color=\"#e2e8f0\";\n")
		for _, n := range hosts[h] {
			color := kindColor[n.Kind]
			if color == "" {
				color = "#94a3b8"
			}
			label := dotNodeLabel(n)
			tooltip := n.URL
			if n.StatusCode != 0 {
				tooltip = fmt.Sprintf("%s (%d)", n.URL, n.StatusCode)
			}
			bw.printf("    n%d [label=%s, color=%s, tooltip=%s];\n",
				n.ID, dotQuote(label), dotQuote(color), dotQuote(tooltip))
		}
		bw.printf("  }\n")
	}

	for _, e := range g.edges {
		style := ""
		switch e.Rel {
		case RelHref:
			style = ""
		case RelRedirect:
			style = ", style=dashed, color=\"#dc2626\""
		case RelFormAction:
			style = ", color=\"#dc2626\""
		default:
			style = ", style=dotted"
		}
		bw.printf("  n%d -> n%d [label=%s%s];\n", e.Src, e.Dst, dotQuote(string(e.Rel)), style)
	}
	bw.printf("}\n")
	return bw.err
}

func dotNodeLabel(n *Node) string {
	label := n.Path
	if label == "" {
		label = n.URL
	}
	if len(label) > 44 {
		label = label[:20] + "..." + label[len(label)-20:]
	}
	if n.External {
		label = n.Host
	}
	if n.StatusCode >= 400 {
		label = fmt.Sprintf("%s [%d]", label, n.StatusCode)
	}
	return label
}

func dotQuote(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString("\\\"")
		case '\\':
			b.WriteString("\\\\")
		case '\n':
			b.WriteString("\\n")
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

type errWriter struct {
	w   io.Writer
	err error
}

func (e *errWriter) printf(format string, args ...any) {
	if e.err != nil {
		return
	}
	_, e.err = fmt.Fprintf(e.w, format, args...)
}
