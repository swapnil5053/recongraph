package store

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/swapnil5053/recongraph/pkg/sitegraph"
)

// Snapshot is the default backend: one gzipped JSON file per crawl, metadata
// carried in the file so the directory is the index. No database, no cgo, no
// migrations, and `gunzip | jq` works on the output.
type Snapshot struct {
	dir string
}

type snapshotFile struct {
	Meta  Meta                `json:"meta"`
	Graph *sitegraph.Snapshot `json:"graph"`
}

// DefaultDir is where crawls are kept when no path is given.
func DefaultDir() string {
	if d := os.Getenv("RECONGRAPH_HOME"); d != "" {
		return d
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ".recongraph"
	}
	return filepath.Join(home, ".recongraph", "crawls")
}

// OpenSnapshot opens (creating if needed) a snapshot store rooted at dir.
func OpenSnapshot(dir string) (*Snapshot, error) {
	if dir == "" {
		dir = DefaultDir()
	}
	// The directory is created on first Save, not here, so a mistyped
	// --store on a read-only command doesn't leave empty folders behind.
	return &Snapshot{dir: dir}, nil
}

// Dir is the store's root directory.
func (s *Snapshot) Dir() string { return s.dir }

var slugRe = regexp.MustCompile(`[^a-zA-Z0-9._-]+`)

func slug(target string) string {
	t := strings.TrimPrefix(strings.TrimPrefix(target, "https://"), "http://")
	t = slugRe.ReplaceAllString(t, "-")
	t = strings.Trim(t, "-")
	if len(t) > 48 {
		t = t[:48]
	}
	if t == "" {
		t = "crawl"
	}
	return strings.ToLower(t)
}

func (s *Snapshot) path(id ID) string {
	return filepath.Join(s.dir, string(id)+".json.gz")
}

// Save writes the graph. The ID embeds timestamp and target so the directory
// sorts chronologically and reads sensibly in `ls`.
func (s *Snapshot) Save(ctx context.Context, g *sitegraph.Graph, label string) (ID, error) {
	if g == nil {
		return "", fmt.Errorf("store: nil graph")
	}
	ts := g.FinishedAt
	if ts.IsZero() {
		ts = time.Now().UTC()
	}
	base := fmt.Sprintf("%s_%s", ts.UTC().Format("20060102T150405Z"), slug(g.Target))
	id := ID(base)
	// Two crawls of the same target can finish inside the same second (a
	// script running them back to back, or the tests). Without this the
	// second silently replaced the first.
	for n := 2; ; n++ {
		if _, err := os.Stat(s.path(id)); os.IsNotExist(err) {
			break
		}
		id = ID(fmt.Sprintf("%s-%d", base, n))
	}

	meta := Meta{
		ID:         id,
		Target:     g.Target,
		Label:      label,
		StartedAt:  g.StartedAt,
		FinishedAt: ts,
		Status:     g.Status,
		Nodes:      g.NumNodes(),
		Edges:      g.NumEdges(),
		Findings:   len(g.Findings()),
	}

	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return "", fmt.Errorf("creating store dir: %w", err)
	}
	// Temp file then rename, so an interrupted save can't leave a half-written
	// crawl that fails to load later.
	tmp, err := os.CreateTemp(s.dir, ".tmp-*")
	if err != nil {
		return "", err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	zw := gzip.NewWriter(tmp)
	enc := json.NewEncoder(zw)
	if err := enc.Encode(snapshotFile{Meta: meta, Graph: g.ToSnapshot()}); err != nil {
		tmp.Close()
		return "", err
	}
	if err := zw.Close(); err != nil {
		tmp.Close()
		return "", err
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	if err := os.Rename(tmpName, s.path(id)); err != nil {
		return "", err
	}
	return id, nil
}

// List returns stored crawls, newest first.
func (s *Snapshot) List(ctx context.Context, target string) ([]Meta, error) {
	entries, err := os.ReadDir(s.dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []Meta
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json.gz") {
			continue
		}
		id := ID(strings.TrimSuffix(e.Name(), ".json.gz"))
		meta, err := s.readMeta(id)
		if err != nil {
			continue // one bad file shouldn't break the whole listing
		}
		if target != "" && !strings.Contains(meta.Target, target) {
			continue
		}
		out = append(out, meta)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].FinishedAt.After(out[j].FinishedAt) })
	return out, nil
}

func (s *Snapshot) readMeta(id ID) (Meta, error) {
	sf, err := s.read(id)
	if err != nil {
		return Meta{}, err
	}
	return sf.Meta, nil
}

func (s *Snapshot) read(id ID) (*snapshotFile, error) {
	f, err := os.Open(s.path(id))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	zr, err := gzip.NewReader(f)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", id, err)
	}
	defer zr.Close()

	var sf snapshotFile
	if err := json.NewDecoder(zr).Decode(&sf); err != nil {
		return nil, fmt.Errorf("%s: %w", id, err)
	}
	return &sf, nil
}

// Load rebuilds a graph, adjacency indexes included.
func (s *Snapshot) Load(ctx context.Context, id ID) (*sitegraph.Graph, error) {
	sf, err := s.read(id)
	if err != nil {
		return nil, err
	}
	if sf.Graph == nil {
		return nil, fmt.Errorf("%s: no graph in snapshot", id)
	}
	return sitegraph.FromSnapshot(sf.Graph), nil
}

// Resolve accepts a full ID, a unique prefix, "latest", or "latest:<target>".
func (s *Snapshot) Resolve(ctx context.Context, ref string) (ID, error) {
	if ref == "" {
		return "", fmt.Errorf("empty crawl reference")
	}

	if ref == "latest" || strings.HasPrefix(ref, "latest:") {
		target := strings.TrimPrefix(ref, "latest:")
		if target == "latest" {
			target = ""
		}
		metas, err := s.List(ctx, target)
		if err != nil {
			return "", err
		}
		if len(metas) == 0 {
			return "", fmt.Errorf("no stored crawls%s", targetSuffix(target))
		}
		return metas[0].ID, nil
	}

	if _, err := os.Stat(s.path(ID(ref))); err == nil {
		return ID(ref), nil
	}

	metas, err := s.List(ctx, "")
	if err != nil {
		return "", err
	}
	var matches []ID
	for _, m := range metas {
		if strings.HasPrefix(string(m.ID), ref) {
			matches = append(matches, m.ID)
		}
	}
	switch len(matches) {
	case 0:
		return "", fmt.Errorf("no crawl matching %q", ref)
	case 1:
		return matches[0], nil
	default:
		return "", fmt.Errorf("%q is ambiguous: matches %d crawls", ref, len(matches))
	}
}

// Close is a no-op for the snapshot backend.
func (s *Snapshot) Close() error { return nil }

func targetSuffix(target string) string {
	if target == "" {
		return ""
	}
	return " for target " + target
}
