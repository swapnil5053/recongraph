package diff

import (
	"testing"

	"github.com/swapnil5053/recongraph/pkg/sitegraph"
)

// build makes a graph from (src, dst, rel) triples.
func build(target string, edges [][3]string) *sitegraph.Graph {
	g := sitegraph.New(target)
	for _, e := range edges {
		s, _ := g.EnsureNode(e[0], sitegraph.KindPage, 0, false)
		d, _ := g.EnsureNode(e[1], sitegraph.KindPage, 1, false)
		g.AddEdge(s, d, sitegraph.EdgeRel(e[2]), "")
	}
	return g
}

func TestAppearedAndDisappearedNodes(t *testing.T) {
	old := build("e.com", [][3]string{
		{"https://e.com/", "https://e.com/a", "href"},
		{"https://e.com/", "https://e.com/legacy", "href"},
	})
	new := build("e.com", [][3]string{
		{"https://e.com/", "https://e.com/a", "href"},
		{"https://e.com/", "https://e.com/brand-new", "href"},
	})

	r := Compare(old, new, Options{})

	if len(r.AppearedNodes) != 1 || r.AppearedNodes[0].URL != "https://e.com/brand-new" {
		t.Errorf("AppearedNodes = %v, want /brand-new", urls(r.AppearedNodes))
	}
	if len(r.DisappearedNodes) != 1 || r.DisappearedNodes[0].URL != "https://e.com/legacy" {
		t.Errorf("DisappearedNodes = %v, want /legacy", urls(r.DisappearedNodes))
	}
}

func TestChangedStatusAndContent(t *testing.T) {
	old := sitegraph.New("e.com")
	id, _ := old.EnsureNode("https://e.com/a", sitegraph.KindPage, 0, false)
	old.SetNodeResult(id, 200, "text/html", 100, "aaaa", "Old title", "")

	new := sitegraph.New("e.com")
	id2, _ := new.EnsureNode("https://e.com/a", sitegraph.KindPage, 0, false)
	new.SetNodeResult(id2, 404, "text/html", 50, "bbbb", "New title", "")

	r := Compare(old, new, Options{})
	if len(r.ChangedNodes) != 1 {
		t.Fatalf("ChangedNodes = %d, want 1", len(r.ChangedNodes))
	}
	fields := map[string]bool{}
	for _, f := range r.ChangedNodes[0].Fields {
		fields[f] = true
	}
	for _, want := range []string{"status", "title", "content"} {
		if !fields[want] {
			t.Errorf("expected %q in changed fields, got %v", want, r.ChangedNodes[0].Fields)
		}
	}
}

func TestIgnoreContentHash(t *testing.T) {
	old := sitegraph.New("e.com")
	id, _ := old.EnsureNode("https://e.com/a", sitegraph.KindPage, 0, false)
	old.SetNodeResult(id, 200, "text/html", 100, "aaaa", "Same", "")

	new := sitegraph.New("e.com")
	id2, _ := new.EnsureNode("https://e.com/a", sitegraph.KindPage, 0, false)
	new.SetNodeResult(id2, 200, "text/html", 100, "bbbb", "Same", "")

	if r := Compare(old, new, Options{}); len(r.ChangedNodes) != 1 {
		t.Errorf("content hash change should be reported by default")
	}
	if r := Compare(old, new, Options{IgnoreContentHash: true}); len(r.ChangedNodes) != 0 {
		t.Errorf("IgnoreContentHash should suppress it, got %v", r.ChangedNodes)
	}
}

// Same pages, but one now points somewhere new.
func TestRestructuredSamePagesNewEdge(t *testing.T) {
	old := build("e.com", [][3]string{
		{"https://e.com/checkout", "https://e.com/pay", "href"},
	})
	new := build("e.com", [][3]string{
		{"https://e.com/checkout", "https://e.com/pay", "href"},
	})
	// Edge between two nodes that exist in both crawls.
	s, _ := new.EnsureNode("https://e.com/checkout", sitegraph.KindPage, 0, false)
	d, _ := new.EnsureNode("https://e.com/pay", sitegraph.KindPage, 1, false)
	new.AddEdge(s, d, sitegraph.RelJSEndpoint, "")

	r := Compare(old, new, Options{})

	if len(r.AppearedNodes) != 0 {
		t.Errorf("no new pages expected, got %v", urls(r.AppearedNodes))
	}
	if len(r.AppearedEdges) != 1 {
		t.Fatalf("AppearedEdges = %d, want 1", len(r.AppearedEdges))
	}
	if len(r.Restructured) == 0 {
		t.Fatal("expected a restructured entry: same pages, changed references")
	}
	found := false
	for _, rs := range r.Restructured {
		if rs.URL == "https://e.com/checkout" && len(rs.AddedOut) == 1 {
			found = true
		}
	}
	if !found {
		t.Errorf("checkout should report an added outbound edge: %+v", r.Restructured)
	}
}

// Still returns 200, no longer linked from anywhere.
func TestPageStillLiveButNoLongerLinked(t *testing.T) {
	old := build("e.com", [][3]string{
		{"https://e.com/", "https://e.com/legacy/upload", "href"},
	})
	new := build("e.com", [][3]string{
		{"https://e.com/", "https://e.com/other", "href"},
	})
	// Still a node in the new crawl (sitemap listed it), just no inbound href.
	n, _ := new.EnsureNode("https://e.com/legacy/upload", sitegraph.KindPage, 1, false)
	new.SetNodeResult(n, 200, "text/html", 10, "x", "Upload", "")

	r := Compare(old, new, Options{})

	for _, dn := range r.DisappearedNodes {
		if dn.URL == "https://e.com/legacy/upload" {
			t.Error("node should not be reported as disappeared: it still exists")
		}
	}
	gone := false
	for _, e := range r.DisappearedEdges {
		if e.Dst == "https://e.com/legacy/upload" && e.Rel == sitegraph.RelHref {
			gone = true
		}
	}
	if !gone {
		t.Error("the href edge to /legacy/upload should be reported as disappeared")
	}
	orphaned := false
	for _, o := range new.Orphans() {
		if o.URL == "https://e.com/legacy/upload" {
			orphaned = true
		}
	}
	if !orphaned {
		t.Error("/legacy/upload should now be an orphan in the new crawl")
	}
}

func TestTechDrift(t *testing.T) {
	old := sitegraph.New("e.com")
	id, _ := old.EnsureNode("https://e.com/", sitegraph.KindPage, 0, false)
	old.SetNodeResult(id, 200, "text/html", 1, "h", "T", "")
	old.SetTechs(id, []sitegraph.Tech{{Name: "WordPress", Version: "6.3"}})

	new := sitegraph.New("e.com")
	id2, _ := new.EnsureNode("https://e.com/", sitegraph.KindPage, 0, false)
	new.SetNodeResult(id2, 200, "text/html", 1, "h", "T", "")
	new.SetTechs(id2, []sitegraph.Tech{{Name: "WordPress", Version: "6.4.2"}})

	r := Compare(old, new, Options{})
	if len(r.ChangedNodes) != 1 {
		t.Fatalf("expected a tech change, got %v", r.ChangedNodes)
	}
	if r.ChangedNodes[0].Fields[0] != "tech" {
		t.Errorf("fields = %v, want tech", r.ChangedNodes[0].Fields)
	}
}

func TestExternalHostAppeared(t *testing.T) {
	old := sitegraph.New("e.com")
	s, _ := old.EnsureNode("https://e.com/", sitegraph.KindPage, 0, false)
	d, _ := old.EnsureNode("https://cdn-old.net/a.js", sitegraph.KindScript, 1, true)
	old.AddEdge(s, d, sitegraph.RelScript, "")

	new := sitegraph.New("e.com")
	s2, _ := new.EnsureNode("https://e.com/", sitegraph.KindPage, 0, false)
	d2, _ := new.EnsureNode("https://tracker-new.io/t.js", sitegraph.KindScript, 1, true)
	new.AddEdge(s2, d2, sitegraph.RelScript, "")

	r := Compare(old, new, Options{})
	if len(r.AppearedHosts) != 1 || r.AppearedHosts[0] != "tracker-new.io" {
		t.Errorf("AppearedHosts = %v, want [tracker-new.io]", r.AppearedHosts)
	}
	if len(r.DisappearedHosts) != 1 || r.DisappearedHosts[0] != "cdn-old.net" {
		t.Errorf("DisappearedHosts = %v, want [cdn-old.net]", r.DisappearedHosts)
	}
}

// Self-diff must be empty, or the comparison is unstable.
func TestIdenticalGraphsProduceEmptyDiff(t *testing.T) {
	g := build("e.com", [][3]string{
		{"https://e.com/", "https://e.com/a", "href"},
		{"https://e.com/a", "https://e.com/b", "href"},
		{"https://e.com/", "https://cdn.net/x.js", "script"},
	})
	r := Compare(g, g, Options{})
	if !r.Empty() {
		t.Errorf("self-diff not empty: %+v", r)
	}
}

func TestFindingsDiff(t *testing.T) {
	old := sitegraph.New("e.com")
	id, _ := old.EnsureNode("https://e.com/", sitegraph.KindPage, 0, false)
	old.AddFinding(sitegraph.Finding{NodeID: id, Kind: "email", Value: "a@e.com"})

	new := sitegraph.New("e.com")
	id2, _ := new.EnsureNode("https://e.com/", sitegraph.KindPage, 0, false)
	new.AddFinding(sitegraph.Finding{NodeID: id2, Kind: "email", Value: "a@e.com"})
	new.AddFinding(sitegraph.Finding{NodeID: id2, Kind: "secret-like", Value: "AKIA..."})

	r := Compare(old, new, Options{})
	if len(r.AppearedFindings) != 1 || r.AppearedFindings[0].Kind != "secret-like" {
		t.Errorf("AppearedFindings = %v", r.AppearedFindings)
	}
	if len(r.DisappearedFindings) != 0 {
		t.Errorf("DisappearedFindings = %v, want none", r.DisappearedFindings)
	}
}

func urls(ns []*sitegraph.Node) []string {
	out := make([]string, 0, len(ns))
	for _, n := range ns {
		out = append(out, n.URL)
	}
	return out
}
