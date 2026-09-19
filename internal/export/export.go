// Package export writes a graph out: JSON and DOT come from pkg/sitegraph,
// plus CSV, a self-contained HTML view, and SVG via Graphviz if installed.
package export

import (
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"

	"github.com/swapnil5053/recongraph/pkg/sitegraph"
)

// Format names a supported output format.
type Format string

const (
	FormatJSON      Format = "json"
	FormatAdjacency Format = "adjacency"
	FormatDOT       Format = "dot"
	FormatSVG       Format = "svg"
	FormatCSV       Format = "csv"
	FormatHTML      Format = "html"
	FormatURLs      Format = "urls"
)

// Formats lists every supported format, for CLI help and validation.
func Formats() []string {
	return []string{"json", "adjacency", "dot", "svg", "csv", "html", "urls"}
}

// ErrGraphvizMissing is returned when SVG export is requested without Graphviz.
var ErrGraphvizMissing = errors.New(
	"svg export needs the Graphviz `dot` binary on PATH " +
		"(apt install graphviz / brew install graphviz); " +
		"alternatively export --format dot, or --format html for a self-contained interactive view")

// Write emits the graph in the requested format. CSV is the odd one out: three
// tables, so it writes a set of files at the given prefix rather than a stream.
func Write(w io.Writer, g *sitegraph.Graph, format Format, pathPrefix string) error {
	switch format {
	case FormatJSON:
		return g.WriteJSON(w, true)
	case FormatAdjacency:
		return g.WriteAdjacencyJSON(w)
	case FormatDOT:
		return g.WriteDOT(w)
	case FormatURLs:
		return WriteURLs(w, g)
	case FormatHTML:
		return WriteHTML(w, g)
	case FormatSVG:
		return WriteSVG(w, g)
	case FormatCSV:
		if pathPrefix == "" {
			return WriteCSVNodes(w, g)
		}
		return WriteCSVSet(pathPrefix, g)
	default:
		return fmt.Errorf("unknown format %q (want one of: %s)", format, strings.Join(Formats(), ", "))
	}
}

// WriteURLs emits one URL per line. The default, so the tool stays usable in a
// pipeline; everything else is opt-in.
func WriteURLs(w io.Writer, g *sitegraph.Graph) error {
	urls := make([]string, 0, g.NumNodes())
	for _, n := range g.Nodes() {
		urls = append(urls, n.URL)
	}
	sort.Strings(urls)
	for _, u := range urls {
		if _, err := fmt.Fprintln(w, u); err != nil {
			return err
		}
	}
	return nil
}

// WriteSVG renders the DOT output through Graphviz. No layout engine of our
// own; that problem is solved and isn't what this tool is for.
func WriteSVG(w io.Writer, g *sitegraph.Graph) error {
	dotBin, err := exec.LookPath("dot")
	if err != nil {
		return ErrGraphvizMissing
	}
	var buf strings.Builder
	if err := g.WriteDOT(&buf); err != nil {
		return err
	}
	cmd := exec.Command(dotBin, "-Tsvg")
	cmd.Stdin = strings.NewReader(buf.String())
	cmd.Stdout = w
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// WriteCSVNodes writes the node table.
func WriteCSVNodes(w io.Writer, g *sitegraph.Graph) error {
	cw := csv.NewWriter(w)
	defer cw.Flush()
	if err := cw.Write([]string{
		"url", "kind", "host", "path", "depth", "external", "fetched",
		"status", "content_type", "content_length", "content_hash",
		"title", "in_degree", "out_degree", "techs", "error",
	}); err != nil {
		return err
	}
	for _, n := range g.Nodes() {
		if err := cw.Write([]string{
			n.URL, string(n.Kind), n.Host, n.Path, strconv.Itoa(n.Depth),
			strconv.FormatBool(n.External), strconv.FormatBool(n.Fetched),
			strconv.Itoa(n.StatusCode), n.ContentType, strconv.FormatInt(n.ContentLength, 10),
			n.ContentHash, n.Title,
			strconv.Itoa(g.InDegree(n.ID)), strconv.Itoa(g.OutDegree(n.ID)),
			techList(n.Techs), n.Error,
		}); err != nil {
			return err
		}
	}
	return cw.Error()
}

// WriteCSVEdges writes the edge table in URL terms.
func WriteCSVEdges(w io.Writer, g *sitegraph.Graph) error {
	cw := csv.NewWriter(w)
	defer cw.Flush()
	if err := cw.Write([]string{"src", "dst", "rel", "context"}); err != nil {
		return err
	}
	for _, e := range g.Edges() {
		src, dst := g.Node(e.Src), g.Node(e.Dst)
		if src == nil || dst == nil {
			continue
		}
		if err := cw.Write([]string{src.URL, dst.URL, string(e.Rel), e.Context}); err != nil {
			return err
		}
	}
	return cw.Error()
}

// WriteCSVFindings writes the passive-discovery table.
func WriteCSVFindings(w io.Writer, g *sitegraph.Graph) error {
	cw := csv.NewWriter(w)
	defer cw.Flush()
	if err := cw.Write([]string{"kind", "value", "found_on", "evidence"}); err != nil {
		return err
	}
	for _, f := range g.Findings() {
		u := ""
		if n := g.Node(f.NodeID); n != nil {
			u = n.URL
		}
		if err := cw.Write([]string{f.Kind, f.Value, u, f.Evidence}); err != nil {
			return err
		}
	}
	return cw.Error()
}

// WriteCSVSet writes nodes, edges and findings as three files.
func WriteCSVSet(prefix string, g *sitegraph.Graph) error {
	files := []struct {
		suffix string
		fn     func(io.Writer, *sitegraph.Graph) error
	}{
		{"-nodes.csv", WriteCSVNodes},
		{"-edges.csv", WriteCSVEdges},
		{"-findings.csv", WriteCSVFindings},
	}
	for _, f := range files {
		file, err := os.Create(prefix + f.suffix)
		if err != nil {
			return err
		}
		if err := f.fn(file, g); err != nil {
			file.Close()
			return err
		}
		if err := file.Close(); err != nil {
			return err
		}
	}
	return nil
}

func techList(ts []sitegraph.Tech) string {
	if len(ts) == 0 {
		return ""
	}
	parts := make([]string, 0, len(ts))
	for _, t := range ts {
		s := t.Name
		if t.Version != "" {
			s += " " + t.Version
		}
		parts = append(parts, s)
	}
	sort.Strings(parts)
	return strings.Join(parts, "; ")
}
