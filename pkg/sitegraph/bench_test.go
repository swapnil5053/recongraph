package sitegraph

import (
	"fmt"
	"runtime"
	"testing"
)

// buildGraph makes a graph shaped like a real crawl: a home page, section
// pages, and leaf pages that link back up and sideways, plus one shared
// stylesheet and script that every page references.
func buildGraph(pages, fanout int) *Graph {
	g := New("https://bench.example")
	home, _ := g.EnsureNode("https://bench.example/", KindPage, 0, false)
	css, _ := g.EnsureNode("https://bench.example/s.css", KindStylesheet, 1, false)
	js, _ := g.EnsureNode("https://bench.example/app.js", KindScript, 1, false)

	sections := make([]NodeID, 0, pages/fanout+1)
	for i := 0; i < pages/fanout+1; i++ {
		id, _ := g.EnsureNode(fmt.Sprintf("https://bench.example/section/%d/index.html", i), KindPage, 1, false)
		g.AddEdge(home, id, RelHref, "")
		g.AddEdge(id, home, RelHref, "")
		sections = append(sections, id)
	}
	for i := 0; i < pages; i++ {
		sec := sections[i%len(sections)]
		id, _ := g.EnsureNode(fmt.Sprintf("https://bench.example/section/%d/page-%d.html?ref=nav", i%len(sections), i), KindPage, 2, false)
		g.SetNodeResult(id, 200, "text/html", 4096, "0123456789abcdef", "A page", "")
		g.AddEdge(sec, id, RelHref, "")
		g.AddEdge(id, sec, RelHref, "")
		g.AddEdge(id, home, RelHref, "")
		g.AddEdge(id, css, RelStylesheet, "")
		g.AddEdge(id, js, RelScript, "")
	}
	return g
}

// BenchmarkBuildGraph reports the time and the heap cost per node, which is
// what decides how large a crawl fits in memory.
func BenchmarkBuildGraph(b *testing.B) {
	const pages, fanout = 20000, 40
	for i := 0; i < b.N; i++ {
		g := buildGraph(pages, fanout)
		if g.NumNodes() < pages {
			b.Fatal("graph did not build")
		}
	}
	b.StopTimer()

	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	g := buildGraph(pages, fanout)
	runtime.GC()
	runtime.ReadMemStats(&after)
	b.ReportMetric(float64(after.HeapAlloc-before.HeapAlloc)/float64(g.NumNodes()), "B/node")
	b.ReportMetric(float64(g.NumEdges()), "edges")
	runtime.KeepAlive(g)
}

func BenchmarkQueries(b *testing.B) {
	g := buildGraph(20000, 40)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		g.Hubs(10)
		g.Orphans()
		g.Components()
	}
}
