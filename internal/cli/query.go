package cli

import (
	"context"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/swapnil5053/recongraph/internal/store"
	"github.com/swapnil5053/recongraph/pkg/sitegraph"
)

// The query subcommand exists because the whole premise of ReconGraph is that a
// crawl leaves behind a queryable artifact. If the only way to interrogate the
// graph were to open the file yourself, that premise would be unproven by the
// tool that makes the claim.

func runQuery(args []string) error {
	fs := flag.NewFlagSet("query", flag.ContinueOnError)
	fs.Usage = func() {
		fmt.Fprint(os.Stderr, `Usage: recongraph query <crawl> [question]

<crawl> is a full ID, a unique prefix, "latest", or "latest:<target>".
Pick exactly one question.

  recongraph query latest --orphans
  recongraph query latest --hubs 20
  recongraph query latest --external
  recongraph query latest --tech wordpress
  recongraph query latest --status 404
  recongraph query latest --findings secret-like
  recongraph query latest --path-to /admin
  recongraph query latest --links-to https://example.com/login
  recongraph query latest --components

`)
		fs.PrintDefaults()
	}

	storeDir := fs.String("store", "", "Crawl store directory.")
	orphans := fs.Bool("orphans", false, "Pages and endpoints with no inbound link.")
	hubs := fs.Int("hubs", 0, "Top N nodes by inbound references.")
	external := fs.Bool("external", false, "Third-party hosts, by reference count.")
	tech := fs.String("tech", "", "Nodes running a technology (substring match).")
	status := fs.Int("status", 0, "Nodes with this HTTP status code.")
	findings := fs.String("findings", "", "Passive findings, optionally filtered by kind.")
	allFindings := fs.Bool("all-findings", false, "Every passive finding.")
	pathTo := fs.String("path-to", "", "Shortest click path from a seed to a URL or path suffix.")
	linksTo := fs.String("links-to", "", "What references this URL.")
	components := fs.Bool("components", false, "Weakly-connected components (distinct applications).")
	summary := fs.Bool("summary", false, "Overview of the crawl.")

	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(pos) < 1 {
		fs.Usage()
		return fmt.Errorf("need a crawl reference")
	}

	ctx := context.Background()
	st, err := store.OpenSnapshot(*storeDir)
	if err != nil {
		return err
	}
	defer st.Close()

	id, err := resolveRef(ctx, st, pos[0])
	if err != nil {
		return err
	}
	g, err := st.Load(ctx, id)
	if err != nil {
		return err
	}

	switch {
	case *orphans:
		return queryOrphans(g)
	case *hubs > 0:
		return queryHubs(g, *hubs)
	case *external:
		return queryExternal(g)
	case *tech != "":
		return queryTech(g, *tech)
	case *status != 0:
		return queryStatus(g, *status)
	case *findings != "" || *allFindings:
		return queryFindings(g, *findings)
	case *pathTo != "":
		return queryPathTo(g, *pathTo)
	case *linksTo != "":
		return queryLinksTo(g, *linksTo)
	case *components:
		return queryComponents(g)
	default:
		_ = summary
		return querySummary(g, string(id))
	}
}

func querySummary(g *sitegraph.Graph, id string) error {
	fmt.Printf("crawl      %s\n", id)
	fmt.Printf("target     %s\n", g.Target)
	fmt.Printf("status     %s\n", g.Status)
	fmt.Printf("graph      %d nodes, %d edges\n", g.NumNodes(), g.NumEdges())

	kinds := map[sitegraph.NodeKind]int{}
	internal, fetched := 0, 0
	for _, n := range g.Nodes() {
		kinds[n.Kind]++
		if !n.External {
			internal++
		}
		if n.Fetched {
			fetched++
		}
	}
	fmt.Printf("internal   %d (%d fetched)\n", internal, fetched)
	fmt.Printf("orphans    %d\n", len(g.Orphans()))
	fmt.Printf("3rd party  %d hosts\n", len(g.ExternalHosts()))
	fmt.Printf("findings   %d\n", len(g.Findings()))

	names := make([]string, 0, len(kinds))
	for k := range kinds {
		names = append(names, string(k))
	}
	sort.Strings(names)
	fmt.Printf("\nby kind\n")
	for _, k := range names {
		fmt.Printf("  %-12s %d\n", k, kinds[sitegraph.NodeKind(k)])
	}
	return nil
}

func queryOrphans(g *sitegraph.Graph) error {
	orph := g.Orphans()
	if len(orph) == 0 {
		fmt.Println("No orphans: every page is reachable by at least one anchor.")
		return nil
	}
	fmt.Printf("%d node(s) with no inbound anchor:\n\n", len(orph))
	for _, n := range orph {
		via := referenceKinds(g, n.ID)
		fmt.Printf("  %s\n", n.URL)
		fmt.Printf("      status %s · kind %s · reachable via %s\n",
			statusStr(n.StatusCode), n.Kind, via)
	}
	return nil
}

func referenceKinds(g *sitegraph.Graph, id sitegraph.NodeID) string {
	seen := map[sitegraph.EdgeRel]bool{}
	for _, e := range g.InEdges(id) {
		seen[e.Rel] = true
	}
	if len(seen) == 0 {
		return "nothing (discovered from a seed or sitemap)"
	}
	rels := make([]string, 0, len(seen))
	for r := range seen {
		rels = append(rels, string(r))
	}
	sort.Strings(rels)
	return strings.Join(rels, ", ")
}

func queryHubs(g *sitegraph.Graph, n int) error {
	fmt.Printf("Top %d nodes by inbound references:\n\n", n)
	for _, node := range g.Hubs(n) {
		fmt.Printf("  %4d  %s\n", g.InDegree(node.ID), node.URL)
	}
	return nil
}

func queryExternal(g *sitegraph.Graph) error {
	hosts := g.ExternalHosts()
	if len(hosts) == 0 {
		fmt.Println("No third-party hosts referenced.")
		return nil
	}
	type hc struct {
		host string
		n    int
	}
	list := make([]hc, 0, len(hosts))
	for h, c := range hosts {
		list = append(list, hc{h, c})
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].n != list[j].n {
			return list[i].n > list[j].n
		}
		return list[i].host < list[j].host
	})
	fmt.Printf("%d third-party host(s):\n\n", len(list))
	for _, h := range list {
		fmt.Printf("  %4d  %s\n", h.n, h.host)
	}
	return nil
}

func queryTech(g *sitegraph.Graph, want string) error {
	want = strings.ToLower(want)
	found := 0
	for _, n := range g.Nodes() {
		for _, t := range n.Techs {
			if !strings.Contains(strings.ToLower(t.Name), want) {
				continue
			}
			found++
			v := ""
			if t.Version != "" {
				v = " " + t.Version
			}
			fmt.Printf("  %s\n      %s%s (confidence %d) — %s\n",
				n.URL, t.Name, v, t.Confidence, strings.Join(t.Evidence, "; "))
			break
		}
	}
	if found == 0 {
		fmt.Printf("No nodes matching technology %q.\n", want)
	}
	return nil
}

func queryStatus(g *sitegraph.Graph, code int) error {
	found := 0
	for _, n := range g.Nodes() {
		if n.StatusCode == code {
			found++
			fmt.Printf("  %s\n", n.URL)
		}
	}
	if found == 0 {
		fmt.Printf("No nodes returned %d.\n", code)
	} else {
		fmt.Printf("\n%d node(s) returned %d.\n", found, code)
	}
	return nil
}

func queryFindings(g *sitegraph.Graph, kind string) error {
	kind = strings.ToLower(kind)
	byKind := map[string][]sitegraph.Finding{}
	for _, f := range g.Findings() {
		if kind != "" && !strings.Contains(strings.ToLower(f.Kind), kind) {
			continue
		}
		byKind[f.Kind] = append(byKind[f.Kind], f)
	}
	if len(byKind) == 0 {
		fmt.Println("No matching findings.")
		return nil
	}
	kinds := make([]string, 0, len(byKind))
	for k := range byKind {
		kinds = append(kinds, k)
	}
	sort.Strings(kinds)
	for _, k := range kinds {
		fmt.Printf("%s (%d)\n", strings.ToUpper(k), len(byKind[k]))
		for _, f := range byKind[k] {
			on := ""
			if n := g.Node(f.NodeID); n != nil {
				on = n.URL
			}
			fmt.Printf("  %s\n      on %s\n", f.Value, on)
		}
		fmt.Println()
	}
	return nil
}

// queryPathTo answers "how many clicks from the front door is this".
func queryPathTo(g *sitegraph.Graph, target string) error {
	var dst *sitegraph.Node
	for _, n := range g.Nodes() {
		if n.URL == target || strings.HasSuffix(n.URL, target) {
			dst = n
			break
		}
	}
	if dst == nil {
		return fmt.Errorf("no node matching %q in this crawl", target)
	}

	var seeds []*sitegraph.Node
	for _, n := range g.Nodes() {
		if n.Depth == 0 && !n.External {
			seeds = append(seeds, n)
		}
	}
	for _, seed := range seeds {
		path := g.ShortestPath(seed.ID, dst.ID)
		if path == nil {
			continue
		}
		fmt.Printf("%d hop(s) from %s:\n\n", len(path)-1, seed.URL)
		for i, id := range path {
			n := g.Node(id)
			prefix := strings.Repeat("   ", i)
			fmt.Printf("  %s%s %s\n", prefix, arrow(i), n.URL)
		}
		return nil
	}
	fmt.Printf("%s exists in the graph but is not reachable from any seed by following references.\n", dst.URL)
	fmt.Println("Usually that means a sitemap, a script, or a comment.")
	return nil
}

func arrow(i int) string {
	if i == 0 {
		return "•"
	}
	return "└→"
}

func queryLinksTo(g *sitegraph.Graph, target string) error {
	var node *sitegraph.Node
	for _, n := range g.Nodes() {
		if n.URL == target || strings.HasSuffix(n.URL, target) {
			node = n
			break
		}
	}
	if node == nil {
		return fmt.Errorf("no node matching %q", target)
	}
	in := g.InEdges(node.ID)
	if len(in) == 0 {
		fmt.Printf("Nothing references %s.\n", node.URL)
		return nil
	}
	fmt.Printf("%d reference(s) to %s:\n\n", len(in), node.URL)
	for _, e := range in {
		src := g.Node(e.Src)
		fmt.Printf("  [%-11s] %s\n", e.Rel, src.URL)
	}
	return nil
}

func queryComponents(g *sitegraph.Graph) error {
	comps := g.Components()
	fmt.Printf("%d weakly-connected component(s).\n", len(comps))
	fmt.Println("More than one usually means several apps share this surface.")
	fmt.Println()
	for i, c := range comps {
		if i >= 10 {
			fmt.Printf("  ... and %d smaller component(s)\n", len(comps)-10)
			break
		}
		fmt.Printf("  component %d — %d node(s)\n", i+1, len(c))
		for j, id := range c {
			if j >= 5 {
				fmt.Printf("      ... +%d more\n", len(c)-5)
				break
			}
			fmt.Printf("      %s\n", g.Node(id).URL)
		}
	}
	return nil
}

func statusStr(code int) string {
	if code == 0 {
		return "not fetched"
	}
	return fmt.Sprintf("%d", code)
}
