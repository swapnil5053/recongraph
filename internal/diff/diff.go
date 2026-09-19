// Package diff compares two crawl graphs.
//
// Two sets of URLs can only tell you "3 appeared, 1 disappeared". Two graphs
// can tell you the checkout page now references a host it didn't last month,
// and that nothing links to /legacy/upload any more even though it still 200s.
package diff

import (
	"sort"

	"github.com/swapnil5053/recongraph/pkg/sitegraph"
)

// NodeChange describes how one URL changed between crawls.
type NodeChange struct {
	URL      string   `json:"url"`
	Fields   []string `json:"fields"`
	OldValue []string `json:"old"`
	NewValue []string `json:"new"`
}

// EdgeRef is an edge in URL terms. Node IDs are graph-local, so comparing them
// across crawls is meaningless.
type EdgeRef struct {
	Src string            `json:"src"`
	Dst string            `json:"dst"`
	Rel sitegraph.EdgeRel `json:"rel"`
}

// Restructured is a node whose references changed even though the node itself
// did not.
type Restructured struct {
	URL        string    `json:"url"`
	AddedOut   []EdgeRef `json:"added_out,omitempty"`
	RemovedOut []EdgeRef `json:"removed_out,omitempty"`
	AddedIn    []EdgeRef `json:"added_in,omitempty"`
	RemovedIn  []EdgeRef `json:"removed_in,omitempty"`
}

// Result is a full comparison.
type Result struct {
	OldTarget string `json:"old_target"`
	NewTarget string `json:"new_target"`

	AppearedNodes    []*sitegraph.Node `json:"appeared_nodes"`
	DisappearedNodes []*sitegraph.Node `json:"disappeared_nodes"`
	ChangedNodes     []NodeChange      `json:"changed_nodes"`

	AppearedEdges    []EdgeRef `json:"appeared_edges"`
	DisappearedEdges []EdgeRef `json:"disappeared_edges"`

	Restructured []Restructured `json:"restructured"`

	AppearedFindings    []sitegraph.Finding `json:"appeared_findings"`
	DisappearedFindings []sitegraph.Finding `json:"disappeared_findings"`

	AppearedHosts    []string `json:"appeared_external_hosts"`
	DisappearedHosts []string `json:"disappeared_external_hosts"`
}

// Empty reports whether nothing changed at all.
func (r *Result) Empty() bool {
	return len(r.AppearedNodes) == 0 && len(r.DisappearedNodes) == 0 &&
		len(r.ChangedNodes) == 0 && len(r.AppearedEdges) == 0 &&
		len(r.DisappearedEdges) == 0 && len(r.AppearedFindings) == 0 &&
		len(r.DisappearedFindings) == 0
}

// Options tunes what counts as a change.
type Options struct {
	// IgnoreContentHash suppresses byte-level churn. Useful against sites with
	// a build hash or timestamp in every page.
	IgnoreContentHash bool
	// IgnoreFindings skips passive-finding comparison.
	IgnoreFindings bool
}

// Compare diffs two graphs.
func Compare(oldG, newG *sitegraph.Graph, opts Options) *Result {
	res := &Result{OldTarget: oldG.Target, NewTarget: newG.Target}

	oldNodes := nodesByURL(oldG)
	newNodes := nodesByURL(newG)

	for url, n := range newNodes {
		if _, ok := oldNodes[url]; !ok {
			res.AppearedNodes = append(res.AppearedNodes, n)
		}
	}
	for url, n := range oldNodes {
		if _, ok := newNodes[url]; !ok {
			res.DisappearedNodes = append(res.DisappearedNodes, n)
		}
	}
	for url, newN := range newNodes {
		oldN, ok := oldNodes[url]
		if !ok {
			continue
		}
		if ch := compareNode(oldN, newN, opts); ch != nil {
			res.ChangedNodes = append(res.ChangedNodes, *ch)
		}
	}

	oldEdges := edgeSet(oldG)
	newEdges := edgeSet(newG)
	for e := range newEdges {
		if _, ok := oldEdges[e]; !ok {
			res.AppearedEdges = append(res.AppearedEdges, e)
		}
	}
	for e := range oldEdges {
		if _, ok := newEdges[e]; !ok {
			res.DisappearedEdges = append(res.DisappearedEdges, e)
		}
	}

	res.Restructured = restructured(res.AppearedEdges, res.DisappearedEdges, oldNodes, newNodes)

	if !opts.IgnoreFindings {
		oldF := findingSet(oldG)
		newF := findingSet(newG)
		for k, f := range newF {
			if _, ok := oldF[k]; !ok {
				res.AppearedFindings = append(res.AppearedFindings, f)
			}
		}
		for k, f := range oldF {
			if _, ok := newF[k]; !ok {
				res.DisappearedFindings = append(res.DisappearedFindings, f)
			}
		}
	}

	oldHosts := oldG.ExternalHosts()
	newHosts := newG.ExternalHosts()
	for h := range newHosts {
		if _, ok := oldHosts[h]; !ok {
			res.AppearedHosts = append(res.AppearedHosts, h)
		}
	}
	for h := range oldHosts {
		if _, ok := newHosts[h]; !ok {
			res.DisappearedHosts = append(res.DisappearedHosts, h)
		}
	}

	sortResult(res)
	return res
}

func compareNode(o, n *sitegraph.Node, opts Options) *NodeChange {
	ch := NodeChange{URL: n.URL}
	add := func(field, oldV, newV string) {
		ch.Fields = append(ch.Fields, field)
		ch.OldValue = append(ch.OldValue, oldV)
		ch.NewValue = append(ch.NewValue, newV)
	}

	if o.StatusCode != n.StatusCode {
		add("status", itoa(o.StatusCode), itoa(n.StatusCode))
	}
	if o.ContentType != n.ContentType {
		add("content_type", o.ContentType, n.ContentType)
	}
	if o.Title != n.Title {
		add("title", o.Title, n.Title)
	}
	if !opts.IgnoreContentHash && o.ContentHash != n.ContentHash &&
		o.ContentHash != "" && n.ContentHash != "" {
		add("content", o.ContentHash, n.ContentHash)
	}
	if ot, nt := techString(o.Techs), techString(n.Techs); ot != nt {
		add("tech", ot, nt)
	}
	if len(ch.Fields) == 0 {
		return nil
	}
	return &ch
}

func nodesByURL(g *sitegraph.Graph) map[string]*sitegraph.Node {
	out := make(map[string]*sitegraph.Node, g.NumNodes())
	for _, n := range g.Nodes() {
		out[n.URL] = n
	}
	return out
}

func edgeSet(g *sitegraph.Graph) map[EdgeRef]struct{} {
	out := make(map[EdgeRef]struct{}, g.NumEdges())
	for _, e := range g.Edges() {
		src, dst := g.Node(e.Src), g.Node(e.Dst)
		if src == nil || dst == nil {
			continue
		}
		out[EdgeRef{Src: src.URL, Dst: dst.URL, Rel: e.Rel}] = struct{}{}
	}
	return out
}

func findingSet(g *sitegraph.Graph) map[string]sitegraph.Finding {
	out := map[string]sitegraph.Finding{}
	for _, f := range g.Findings() {
		node := g.Node(f.NodeID)
		u := ""
		if node != nil {
			u = node.URL
		}
		out[f.Kind+"|"+f.Value+"|"+u] = f
	}
	return out
}

// restructured groups edge changes by node, so the output reads as "this page
// changed what it points at" rather than a flat edge list.
func restructured(added, removed []EdgeRef, oldNodes, newNodes map[string]*sitegraph.Node) []Restructured {
	byURL := map[string]*Restructured{}
	get := func(u string) *Restructured {
		if r, ok := byURL[u]; ok {
			return r
		}
		r := &Restructured{URL: u}
		byURL[u] = r
		return r
	}
	// Only nodes in both crawls. A new page with new edges is just new.
	both := func(u string) bool {
		_, a := oldNodes[u]
		_, b := newNodes[u]
		return a && b
	}
	for _, e := range added {
		if both(e.Src) {
			r := get(e.Src)
			r.AddedOut = append(r.AddedOut, e)
		}
		if both(e.Dst) {
			r := get(e.Dst)
			r.AddedIn = append(r.AddedIn, e)
		}
	}
	for _, e := range removed {
		if both(e.Src) {
			r := get(e.Src)
			r.RemovedOut = append(r.RemovedOut, e)
		}
		if both(e.Dst) {
			r := get(e.Dst)
			r.RemovedIn = append(r.RemovedIn, e)
		}
	}
	out := make([]Restructured, 0, len(byURL))
	for _, r := range byURL {
		out = append(out, *r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].URL < out[j].URL })
	return out
}

func techString(ts []sitegraph.Tech) string {
	if len(ts) == 0 {
		return ""
	}
	names := make([]string, 0, len(ts))
	for _, t := range ts {
		s := t.Name
		if t.Version != "" {
			s += " " + t.Version
		}
		names = append(names, s)
	}
	sort.Strings(names)
	return joinComma(names)
}

func joinComma(ss []string) string {
	out := ""
	for i, s := range ss {
		if i > 0 {
			out += ", "
		}
		out += s
	}
	return out
}

func itoa(i int) string {
	if i == 0 {
		return "-"
	}
	digits := ""
	neg := i < 0
	if neg {
		i = -i
	}
	for i > 0 {
		digits = string(rune('0'+i%10)) + digits
		i /= 10
	}
	if neg {
		digits = "-" + digits
	}
	return digits
}

func sortResult(r *Result) {
	sort.Slice(r.AppearedNodes, func(i, j int) bool { return r.AppearedNodes[i].URL < r.AppearedNodes[j].URL })
	sort.Slice(r.DisappearedNodes, func(i, j int) bool {
		return r.DisappearedNodes[i].URL < r.DisappearedNodes[j].URL
	})
	sort.Slice(r.ChangedNodes, func(i, j int) bool { return r.ChangedNodes[i].URL < r.ChangedNodes[j].URL })
	sort.Slice(r.AppearedEdges, func(i, j int) bool { return edgeLess(r.AppearedEdges[i], r.AppearedEdges[j]) })
	sort.Slice(r.DisappearedEdges, func(i, j int) bool {
		return edgeLess(r.DisappearedEdges[i], r.DisappearedEdges[j])
	})
	sort.Strings(r.AppearedHosts)
	sort.Strings(r.DisappearedHosts)
	sort.Slice(r.AppearedFindings, func(i, j int) bool {
		if r.AppearedFindings[i].Kind != r.AppearedFindings[j].Kind {
			return r.AppearedFindings[i].Kind < r.AppearedFindings[j].Kind
		}
		return r.AppearedFindings[i].Value < r.AppearedFindings[j].Value
	})
	sort.Slice(r.DisappearedFindings, func(i, j int) bool {
		if r.DisappearedFindings[i].Kind != r.DisappearedFindings[j].Kind {
			return r.DisappearedFindings[i].Kind < r.DisappearedFindings[j].Kind
		}
		return r.DisappearedFindings[i].Value < r.DisappearedFindings[j].Value
	})
}

func edgeLess(a, b EdgeRef) bool {
	if a.Src != b.Src {
		return a.Src < b.Src
	}
	if a.Dst != b.Dst {
		return a.Dst < b.Dst
	}
	return a.Rel < b.Rel
}
