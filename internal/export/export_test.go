package export

import (
	"encoding/csv"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/swapnil5053/recongraph/pkg/sitegraph"
)

func sampleGraph() *sitegraph.Graph {
	g := sitegraph.New("https://example.com")
	g.Tool = "recongraph"
	g.Status = "complete"

	root, _ := g.EnsureNode("https://example.com/", sitegraph.KindPage, 0, false)
	g.SetNodeResult(root, 200, "text/html", 100, "h1", `Home "quoted" & <tagged>`, "")
	g.SetTechs(root, []sitegraph.Tech{{Name: "Nginx", Version: "1.24", Confidence: 90}})

	about, _ := g.EnsureNode("https://example.com/about", sitegraph.KindPage, 1, false)
	g.SetNodeResult(about, 404, "text/html", 10, "h2", "Missing", "")

	js, _ := g.EnsureNode("https://cdn.example.net/a.js", sitegraph.KindScript, 1, true)

	g.AddEdge(root, about, sitegraph.RelHref, `link "text"`)
	g.AddEdge(root, js, sitegraph.RelScript, "")
	g.AddFinding(sitegraph.Finding{NodeID: root, Kind: "email", Value: "a@b.com", Evidence: "footer"})
	return g
}

func TestWriteJSONRoundTrips(t *testing.T) {
	g := sampleGraph()
	var buf strings.Builder
	if err := g.WriteJSON(&buf, true); err != nil {
		t.Fatal(err)
	}
	var snap sitegraph.Snapshot
	if err := json.Unmarshal([]byte(buf.String()), &snap); err != nil {
		t.Fatalf("output is not valid JSON: %v", err)
	}
	if len(snap.Nodes) != 3 || len(snap.Edges) != 2 {
		t.Errorf("snapshot = %d nodes, %d edges", len(snap.Nodes), len(snap.Edges))
	}
	back := sitegraph.FromSnapshot(&snap)
	if back.NumNodes() != g.NumNodes() || back.NumEdges() != g.NumEdges() {
		t.Error("round trip through JSON lost data")
	}
}

func TestWriteAdjacencyJSON(t *testing.T) {
	var buf strings.Builder
	if err := sampleGraph().WriteAdjacencyJSON(&buf); err != nil {
		t.Fatal(err)
	}
	var adj map[string][]struct {
		To  string `json:"to"`
		Rel string `json:"rel"`
	}
	if err := json.Unmarshal([]byte(buf.String()), &adj); err != nil {
		t.Fatal(err)
	}
	if len(adj["https://example.com/"]) != 2 {
		t.Errorf("root adjacency = %v, want 2 entries", adj["https://example.com/"])
	}
	// Every node must be a key, even leaves, so consumers see isolated nodes.
	if _, ok := adj["https://cdn.example.net/a.js"]; !ok {
		t.Error("leaf node missing from adjacency map")
	}
}

// A title with a quote in it must not break the output.
func TestWriteDOTEscapesQuotes(t *testing.T) {
	var buf strings.Builder
	if err := sampleGraph().WriteDOT(&buf); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if !strings.HasPrefix(out, "digraph recongraph {") || !strings.HasSuffix(out, "}\n") {
		t.Error("DOT output is not a well-formed digraph")
	}
	if strings.Contains(out, `"Home "quoted"`) {
		t.Error("unescaped quote leaked into DOT output")
	}
	// Balanced quotes per line: cheap proxy for valid DOT.
	for _, line := range strings.Split(out, "\n") {
		if n := strings.Count(line, `"`) - strings.Count(line, `\"`)*2; n%2 != 0 {
			t.Errorf("unbalanced quotes in DOT line: %s", line)
		}
	}
	if !strings.Contains(out, "subgraph cluster_") {
		t.Error("expected host clusters in DOT output")
	}
	if !strings.Contains(out, "[404]") {
		t.Error("error status should be visible in the node label")
	}
}

func TestWriteCSVSet(t *testing.T) {
	dir := t.TempDir()
	prefix := filepath.Join(dir, "out")
	if err := WriteCSVSet(prefix, sampleGraph()); err != nil {
		t.Fatal(err)
	}

	for _, suffix := range []string{"-nodes.csv", "-edges.csv", "-findings.csv"} {
		f, err := os.Open(prefix + suffix)
		if err != nil {
			t.Fatalf("%s: %v", suffix, err)
		}
		records, err := csv.NewReader(f).ReadAll()
		f.Close()
		if err != nil {
			t.Fatalf("%s is not valid CSV: %v", suffix, err)
		}
		if len(records) < 2 {
			t.Errorf("%s has no data rows", suffix)
		}
		width := len(records[0])
		for i, r := range records {
			if len(r) != width {
				t.Errorf("%s row %d has %d columns, header has %d", suffix, i, len(r), width)
			}
		}
	}
}

func TestWriteURLsSorted(t *testing.T) {
	var buf strings.Builder
	if err := WriteURLs(&buf, sampleGraph()); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 3 {
		t.Fatalf("got %d lines, want 3", len(lines))
	}
	for i := 1; i < len(lines); i++ {
		if lines[i-1] > lines[i] {
			t.Errorf("output is not sorted: %v", lines)
		}
	}
}

// No external references: opening the report offline is the normal case.
func TestWriteHTMLIsSelfContained(t *testing.T) {
	var buf strings.Builder
	if err := WriteHTML(&buf, sampleGraph()); err != nil {
		t.Fatal(err)
	}
	out := buf.String()

	if !strings.HasPrefix(out, "<!doctype html>") {
		t.Error("missing doctype")
	}
	for _, forbidden := range []string{"src=\"http", "href=\"http://cdn", "cdnjs", "unpkg", "googleapis"} {
		if strings.Contains(out, forbidden) {
			t.Errorf("HTML export references an external resource (%q); it must be self-contained", forbidden)
		}
	}

	m := regexp.MustCompile(`(?s)const D = (\{.*?\});\n\n// Kinds are grouped`).FindStringSubmatch(out)
	if m == nil {
		t.Fatal("embedded payload not found")
	}
	var payload struct {
		Nodes []struct {
			URL string `json:"u"`
		} `json:"nodes"`
		Edges []struct {
			S int `json:"s"`
			T int `json:"t"`
		} `json:"edges"`
	}
	if err := json.Unmarshal([]byte(m[1]), &payload); err != nil {
		t.Fatalf("embedded payload is not valid JSON: %v", err)
	}
	if len(payload.Nodes) != 3 || len(payload.Edges) != 2 {
		t.Errorf("payload = %d nodes, %d edges", len(payload.Nodes), len(payload.Edges))
	}
	// Edge indexes must be in range, or the viewer throws on load.
	for _, e := range payload.Edges {
		if e.S < 0 || e.S >= len(payload.Nodes) || e.T < 0 || e.T >= len(payload.Nodes) {
			t.Errorf("edge index out of range: %+v", e)
		}
	}
}

func TestWriteUnknownFormat(t *testing.T) {
	var buf strings.Builder
	err := Write(&buf, sampleGraph(), Format("bogus"), "")
	if err == nil {
		t.Fatal("expected an error for an unknown format")
	}
	// The error should name the valid options rather than just complaining.
	for _, f := range Formats() {
		if !strings.Contains(err.Error(), f) {
			t.Errorf("error message does not mention format %q: %v", f, err)
		}
	}
}

func TestEmptyGraphExportsCleanly(t *testing.T) {
	g := sitegraph.New("https://example.com")
	for _, f := range []Format{FormatJSON, FormatAdjacency, FormatDOT, FormatURLs, FormatHTML} {
		var buf strings.Builder
		if err := Write(&buf, g, f, ""); err != nil {
			t.Errorf("format %s on an empty graph: %v", f, err)
		}
		// urls is a stream: zero URLs is zero lines. Structured formats still
		// have to emit a valid document.
		if f != FormatURLs && buf.Len() == 0 {
			t.Errorf("format %s produced no output", f)
		}
	}
}
