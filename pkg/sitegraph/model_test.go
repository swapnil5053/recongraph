package sitegraph

import (
	"testing"
)

func newTestGraph() *Graph {
	// Hub, chain, orphan, two third-party hosts.
	//
	//   /  ──href──> /a ──href──> /b
	//   /  ──href──> /c
	//   /a ──href──> /
	//   /c ──href──> /
	//   /  ──script─> cdn.net/x.js
	//   /a ──script─> cdn.net/x.js
	//   /  ──image──> tracker.io/p.gif
	//   /b ──js─────> /api/hidden      (no anchor ever points here)
	g := New("https://e.com")
	root, _ := g.EnsureNode("https://e.com/", KindPage, 0, false)
	a, _ := g.EnsureNode("https://e.com/a", KindPage, 1, false)
	b, _ := g.EnsureNode("https://e.com/b", KindPage, 2, false)
	c, _ := g.EnsureNode("https://e.com/c", KindPage, 1, false)
	api, _ := g.EnsureNode("https://e.com/api/hidden", KindAPI, 3, false)
	js, _ := g.EnsureNode("https://cdn.net/x.js", KindScript, 1, true)
	px, _ := g.EnsureNode("https://tracker.io/p.gif", KindImage, 1, true)

	g.AddEdge(root, a, RelHref, "")
	g.AddEdge(a, b, RelHref, "")
	g.AddEdge(root, c, RelHref, "")
	g.AddEdge(a, root, RelHref, "")
	g.AddEdge(c, root, RelHref, "")
	g.AddEdge(root, js, RelScript, "")
	g.AddEdge(a, js, RelScript, "")
	g.AddEdge(root, px, RelImage, "")
	g.AddEdge(b, api, RelJSEndpoint, "")
	return g
}

func TestEnsureNodeDeduplicatesAndKeepsShallowestDepth(t *testing.T) {
	g := New("https://e.com")
	id1, created1 := g.EnsureNode("https://e.com/x", KindPage, 5, false)
	id2, created2 := g.EnsureNode("https://e.com/x", KindPage, 2, false)

	if id1 != id2 {
		t.Errorf("same URL produced two IDs: %d and %d", id1, id2)
	}
	if !created1 || created2 {
		t.Errorf("created flags = %v, %v; want true, false", created1, created2)
	}
	if got := g.Node(id1).Depth; got != 2 {
		t.Errorf("depth = %d, want 2 (the shallowest route wins)", got)
	}
	if g.NumNodes() != 1 {
		t.Errorf("NumNodes = %d, want 1", g.NumNodes())
	}
}

// Same (src,dst,rel) triple collapses to one edge.
func TestAddEdgeDeduplicates(t *testing.T) {
	g := New("https://e.com")
	s, _ := g.EnsureNode("https://e.com/", KindPage, 0, false)
	d, _ := g.EnsureNode("https://e.com/a", KindPage, 1, false)

	if !g.AddEdge(s, d, RelHref, "first") {
		t.Error("first AddEdge should report a new edge")
	}
	if g.AddEdge(s, d, RelHref, "second") {
		t.Error("duplicate (src,dst,rel) should not create a second edge")
	}
	if !g.AddEdge(s, d, RelScript, "") {
		t.Error("a different rel between the same nodes is a distinct edge")
	}
	if g.NumEdges() != 2 {
		t.Errorf("NumEdges = %d, want 2", g.NumEdges())
	}
}

func TestDegrees(t *testing.T) {
	g := newTestGraph()
	root, _ := g.Lookup("https://e.com/")
	js, _ := g.Lookup("https://cdn.net/x.js")

	if got := g.InDegree(root); got != 2 {
		t.Errorf("root in-degree = %d, want 2", got)
	}
	if got := g.OutDegree(root); got != 4 {
		t.Errorf("root out-degree = %d, want 4", got)
	}
	if got := g.InDegree(js); got != 2 {
		t.Errorf("shared script in-degree = %d, want 2", got)
	}
	if got := len(g.OutEdges(root)); got != 4 {
		t.Errorf("OutEdges = %d, want 4", got)
	}
	if got := len(g.InEdges(root)); got != 2 {
		t.Errorf("InEdges = %d, want 2", got)
	}
}

func TestHubsRankedByInDegree(t *testing.T) {
	g := newTestGraph()
	hubs := g.Hubs(3)
	if len(hubs) != 3 {
		t.Fatalf("Hubs(3) returned %d", len(hubs))
	}
	if hubs[0].URL != "https://e.com/" {
		t.Errorf("top hub = %s, want the root", hubs[0].URL)
	}
	for i := 1; i < len(hubs); i++ {
		if g.InDegree(hubs[i-1].ID) < g.InDegree(hubs[i].ID) {
			t.Error("hubs are not sorted by in-degree")
		}
	}
	// External nodes aren't hubs on our surface.
	for _, h := range hubs {
		if h.External {
			t.Errorf("external node %s should not appear in Hubs", h.URL)
		}
	}
}

func TestOrphansFindsUnlinkedEndpoint(t *testing.T) {
	g := newTestGraph()
	orph := g.Orphans()
	if len(orph) != 1 {
		t.Fatalf("Orphans = %v, want exactly the hidden API", urlsOf(orph))
	}
	if orph[0].URL != "https://e.com/api/hidden" {
		t.Errorf("orphan = %s, want /api/hidden", orph[0].URL)
	}
}

// Assets come in on script/image edges. Reporting every stylesheet as an
// orphan would bury the one that matters.
func TestOrphansIgnoresAssetsAndSeeds(t *testing.T) {
	g := New("https://e.com")
	root, _ := g.EnsureNode("https://e.com/", KindPage, 0, false)
	css, _ := g.EnsureNode("https://e.com/s.css", KindStylesheet, 1, false)
	img, _ := g.EnsureNode("https://e.com/i.png", KindImage, 1, false)
	g.AddEdge(root, css, RelStylesheet, "")
	g.AddEdge(root, img, RelImage, "")

	if orph := g.Orphans(); len(orph) != 0 {
		t.Errorf("Orphans = %v; assets and seeds must not be reported", urlsOf(orph))
	}
}

func TestShortestPath(t *testing.T) {
	g := newTestGraph()
	root, _ := g.Lookup("https://e.com/")
	b, _ := g.Lookup("https://e.com/b")
	api, _ := g.Lookup("https://e.com/api/hidden")

	path := g.ShortestPath(root, b)
	if len(path) != 3 {
		t.Fatalf("path root->b = %v, want 3 nodes", urlPath(g, path))
	}
	if path[0] != root || path[2] != b {
		t.Errorf("path endpoints wrong: %v", urlPath(g, path))
	}

	// Reachable only via a js-endpoint edge.
	if p := g.ShortestPath(root, api); len(p) != 4 {
		t.Errorf("path root->api = %v, want 4 nodes", urlPath(g, p))
	}

	if p := g.ShortestPath(root, root); len(p) != 1 {
		t.Errorf("self path = %v, want 1 node", urlPath(g, p))
	}

	// Unreachable returns nil, not an empty slice.
	iso, _ := g.EnsureNode("https://e.com/island", KindPage, 9, false)
	if p := g.ShortestPath(root, iso); p != nil {
		t.Errorf("unreachable node returned %v", urlPath(g, p))
	}
}

func TestComponents(t *testing.T) {
	g := newTestGraph()
	// Second, disconnected app on the same host.
	x, _ := g.EnsureNode("https://e.com/app2/", KindPage, 0, false)
	y, _ := g.EnsureNode("https://e.com/app2/page", KindPage, 1, false)
	g.AddEdge(x, y, RelHref, "")

	comps := g.Components()
	if len(comps) != 2 {
		t.Fatalf("Components = %d, want 2 (two disconnected applications)", len(comps))
	}
	if len(comps[0]) < len(comps[1]) {
		t.Error("components should be sorted largest first")
	}
	if len(comps[1]) != 2 {
		t.Errorf("second component has %d nodes, want 2", len(comps[1]))
	}
}

func TestExternalHosts(t *testing.T) {
	g := newTestGraph()
	hosts := g.ExternalHosts()
	if len(hosts) != 2 {
		t.Fatalf("ExternalHosts = %v, want 2", hosts)
	}
	if hosts["cdn.net"] != 2 {
		t.Errorf("cdn.net referenced %d times, want 2", hosts["cdn.net"])
	}
	if hosts["tracker.io"] != 1 {
		t.Errorf("tracker.io referenced %d times, want 1", hosts["tracker.io"])
	}
	if _, ok := hosts["e.com"]; ok {
		t.Error("the target host must not be listed as third party")
	}
}

func TestTechSummary(t *testing.T) {
	g := newTestGraph()
	root, _ := g.Lookup("https://e.com/")
	a, _ := g.Lookup("https://e.com/a")
	g.SetTechs(root, []Tech{{Name: "Nginx", Version: "1.24"}, {Name: "PHP"}})
	g.SetTechs(a, []Tech{{Name: "Nginx", Version: "1.24"}})

	sum := g.TechSummary()
	if sum["Nginx 1.24"] != 2 {
		t.Errorf("Nginx counted %d times, want 2", sum["Nginx 1.24"])
	}
	if sum["PHP"] != 1 {
		t.Errorf("PHP counted %d times, want 1", sum["PHP"])
	}
}

func TestSetNodeResultAndMissingNode(t *testing.T) {
	g := New("https://e.com")
	id, _ := g.EnsureNode("https://e.com/", KindPage, 0, false)
	g.SetNodeResult(id, 200, "text/html", 42, "hash", "Title", "")
	n := g.Node(id)
	if !n.Fetched || n.StatusCode != 200 || n.ContentLength != 42 || n.Title != "Title" {
		t.Errorf("node not updated: %+v", n)
	}

	// Out-of-range access is safe, not a panic.
	if g.Node(NodeID(999)) != nil {
		t.Error("Node on an unknown ID should return nil")
	}
	g.SetNodeResult(NodeID(999), 200, "", 0, "", "", "") // must not panic
	g.SetTechs(NodeID(999), []Tech{{Name: "x"}})         // must not panic
}

func TestSnapshotRoundTripRebuildsAdjacency(t *testing.T) {
	g := newTestGraph()
	g.AddFinding(Finding{NodeID: 0, Kind: "email", Value: "a@b.com"})

	back := FromSnapshot(g.ToSnapshot())
	if back.NumNodes() != g.NumNodes() || back.NumEdges() != g.NumEdges() {
		t.Fatalf("round trip: %d/%d nodes, %d/%d edges",
			back.NumNodes(), g.NumNodes(), back.NumEdges(), g.NumEdges())
	}
	root, ok := back.Lookup("https://e.com/")
	if !ok {
		t.Fatal("lookup index not rebuilt")
	}
	if back.InDegree(root) != g.InDegree(0) {
		t.Error("adjacency not rebuilt after round trip")
	}
	if len(back.Orphans()) != len(g.Orphans()) {
		t.Error("analysis differs after round trip")
	}
	if len(back.Findings()) != 1 {
		t.Error("findings lost in round trip")
	}
}

func urlsOf(ns []*Node) []string {
	out := make([]string, 0, len(ns))
	for _, n := range ns {
		out = append(out, n.URL)
	}
	return out
}

func urlPath(g *Graph, ids []NodeID) []string {
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		out = append(out, g.Node(id).URL)
	}
	return out
}
