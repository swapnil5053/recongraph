package store

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/swapnil5053/recongraph/pkg/sitegraph"
)

func sample(t *testing.T, target string, when time.Time) *sitegraph.Graph {
	t.Helper()
	g := sitegraph.New(target)
	g.Tool = "recongraph"
	g.StartedAt = when.Add(-time.Minute)
	g.FinishedAt = when
	g.Status = "complete"

	root, _ := g.EnsureNode(target+"/", sitegraph.KindPage, 0, false)
	g.SetNodeResult(root, 200, "text/html", 120, "abc123", "Home", "")
	g.SetTechs(root, []sitegraph.Tech{{Name: "Nginx", Version: "1.24", Confidence: 90}})

	about, _ := g.EnsureNode(target+"/about", sitegraph.KindPage, 1, false)
	g.SetNodeResult(about, 200, "text/html", 90, "def456", "About", "")

	ext, _ := g.EnsureNode("https://cdn.example.net/a.js", sitegraph.KindScript, 1, true)

	g.AddEdge(root, about, sitegraph.RelHref, "About")
	g.AddEdge(root, ext, sitegraph.RelScript, "")
	g.AddFinding(sitegraph.Finding{NodeID: root, Kind: "email", Value: "a@b.com"})
	return g
}

// A saved graph comes back equivalent, adjacency included. If this loses
// anything, every diff against a stored crawl is wrong.
func TestSaveLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	st, err := OpenSnapshot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	ctx := context.Background()
	orig := sample(t, "https://example.com", time.Now().UTC().Truncate(time.Second))

	id, err := st.Save(ctx, orig, "baseline")
	if err != nil {
		t.Fatal(err)
	}

	got, err := st.Load(ctx, id)
	if err != nil {
		t.Fatal(err)
	}

	if got.NumNodes() != orig.NumNodes() {
		t.Errorf("nodes = %d, want %d", got.NumNodes(), orig.NumNodes())
	}
	if got.NumEdges() != orig.NumEdges() {
		t.Errorf("edges = %d, want %d", got.NumEdges(), orig.NumEdges())
	}
	if got.Target != orig.Target || got.Status != orig.Status {
		t.Errorf("metadata lost: %+v", got)
	}
	if len(got.Findings()) != len(orig.Findings()) {
		t.Errorf("findings = %d, want %d", len(got.Findings()), len(orig.Findings()))
	}

	// Adjacency must be rebuilt, not just the flat lists.
	rootID, ok := got.Lookup("https://example.com/")
	if !ok {
		t.Fatal("root node missing after load")
	}
	if got.OutDegree(rootID) != 2 {
		t.Errorf("out-degree = %d, want 2 (adjacency was not rebuilt)", got.OutDegree(rootID))
	}
	n := got.Node(rootID)
	if n.Title != "Home" || n.StatusCode != 200 || n.ContentHash != "abc123" {
		t.Errorf("node fields lost: %+v", n)
	}
	if len(n.Techs) != 1 || n.Techs[0].Name != "Nginx" {
		t.Errorf("techs lost: %+v", n.Techs)
	}
	if len(got.Orphans()) != len(orig.Orphans()) {
		t.Errorf("orphan analysis differs after load")
	}
}

func TestListNewestFirst(t *testing.T) {
	dir := t.TempDir()
	st, _ := OpenSnapshot(dir)
	ctx := context.Background()

	base := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	for i := 0; i < 3; i++ {
		g := sample(t, "https://example.com", base.Add(time.Duration(i)*time.Hour))
		if _, err := st.Save(ctx, g, ""); err != nil {
			t.Fatal(err)
		}
	}
	// Different target, so the filter has something to exclude.
	if _, err := st.Save(ctx, sample(t, "https://other.org", base.Add(9*time.Hour)), ""); err != nil {
		t.Fatal(err)
	}

	all, err := st.List(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 4 {
		t.Fatalf("List = %d crawls, want 4", len(all))
	}
	for i := 1; i < len(all); i++ {
		if all[i-1].FinishedAt.Before(all[i].FinishedAt) {
			t.Errorf("not sorted newest first: %v", all)
		}
	}

	filtered, _ := st.List(ctx, "other.org")
	if len(filtered) != 1 {
		t.Errorf("target filter returned %d, want 1", len(filtered))
	}
}

func TestResolveReferences(t *testing.T) {
	dir := t.TempDir()
	st, _ := OpenSnapshot(dir)
	ctx := context.Background()

	base := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	var ids []ID
	for i := 0; i < 3; i++ {
		id, _ := st.Save(ctx, sample(t, "https://example.com", base.Add(time.Duration(i)*time.Hour)), "")
		ids = append(ids, id)
	}
	newest := ids[2]

	got, err := st.Resolve(ctx, "latest")
	if err != nil || got != newest {
		t.Errorf("Resolve(latest) = %v (%v), want %v", got, err, newest)
	}

	got, err = st.Resolve(ctx, string(newest))
	if err != nil || got != newest {
		t.Errorf("Resolve(full id) = %v (%v)", got, err)
	}

	got, err = st.Resolve(ctx, string(newest)[:20])
	if err != nil || got != newest {
		t.Errorf("Resolve(prefix) = %v (%v)", got, err)
	}

	if _, err := st.Resolve(ctx, "nope"); err == nil {
		t.Error("Resolve of an unknown reference should fail")
	}
	if _, err := st.Resolve(ctx, ""); err == nil {
		t.Error("Resolve of an empty reference should fail")
	}
	// Shared prefix should be ambiguous, not silently resolved.
	if _, err := st.Resolve(ctx, "2026"); err == nil {
		t.Error("ambiguous prefix should error")
	}
}

func TestResolveLatestByTarget(t *testing.T) {
	dir := t.TempDir()
	st, _ := OpenSnapshot(dir)
	ctx := context.Background()
	base := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)

	wantID, _ := st.Save(ctx, sample(t, "https://alpha.test", base), "")
	_, _ = st.Save(ctx, sample(t, "https://beta.test", base.Add(time.Hour)), "")

	got, err := st.Resolve(ctx, "latest:alpha.test")
	if err != nil {
		t.Fatal(err)
	}
	if got != wantID {
		t.Errorf("latest:alpha.test = %v, want %v", got, wantID)
	}
}

// One corrupt file shouldn't break list for everything else.
func TestCorruptFileIsSkipped(t *testing.T) {
	dir := t.TempDir()
	st, _ := OpenSnapshot(dir)
	ctx := context.Background()
	if _, err := st.Save(ctx, sample(t, "https://example.com", time.Now().UTC()), ""); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "20260101T000000Z_broken.json.gz"), []byte("not gzip"), 0o644); err != nil {
		t.Fatal(err)
	}

	metas, err := st.List(ctx, "")
	if err != nil {
		t.Fatalf("List failed because of one corrupt file: %v", err)
	}
	if len(metas) != 1 {
		t.Errorf("List = %d, want 1 (the corrupt file should be skipped)", len(metas))
	}
}

func TestSaveIsAtomic(t *testing.T) {
	dir := t.TempDir()
	st, _ := OpenSnapshot(dir)
	ctx := context.Background()
	if _, err := st.Save(ctx, sample(t, "https://example.com", time.Now().UTC()), ""); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if len(e.Name()) > 4 && e.Name()[:4] == ".tmp" {
			t.Errorf("temp file %s left behind", e.Name())
		}
	}
}

func TestSlugIsFilesystemSafe(t *testing.T) {
	cases := map[string]string{
		"https://example.com/":       "example.com",
		"http://a.b.c:8080/path?q=1": "a.b.c-8080-path-q-1",
		"https://EXAMPLE.COM":        "example.com",
		"":                           "crawl",
	}
	for in, want := range cases {
		if got := slug(in); got != want {
			t.Errorf("slug(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSameSecondSavesDoNotCollide(t *testing.T) {
	st, err := OpenSnapshot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	when := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)

	a, err := st.Save(ctx, sample(t, "https://example.com", when), "")
	if err != nil {
		t.Fatal(err)
	}
	b, err := st.Save(ctx, sample(t, "https://example.com", when.Add(300*time.Millisecond)), "")
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Fatalf("both saves got ID %s; the second overwrote the first", a)
	}
	metas, _ := st.List(ctx, "")
	if len(metas) != 2 {
		t.Fatalf("List = %d crawls, want 2", len(metas))
	}
	if metas[0].ID != b {
		t.Errorf("newest first: got %s, want %s", metas[0].ID, b)
	}
}

func TestLoadRestoresTimestamps(t *testing.T) {
	st, err := OpenSnapshot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	when := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)

	id, err := st.Save(ctx, sample(t, "https://example.com", when), "")
	if err != nil {
		t.Fatal(err)
	}
	g, err := st.Load(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if !g.FinishedAt.Equal(when) || !g.StartedAt.Equal(when.Add(-time.Minute)) {
		t.Errorf("timestamps lost on load: %v / %v", g.StartedAt, g.FinishedAt)
	}
}

// Read-only commands open the store too; a mistyped --store must not create
// directories.
func TestOpenDoesNotCreateDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "typo", "crawls")
	st, err := OpenSnapshot(dir)
	if err != nil {
		t.Fatal(err)
	}
	metas, err := st.List(context.Background(), "")
	if err != nil || len(metas) != 0 {
		t.Fatalf("List on a missing dir = %v, %v", metas, err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("OpenSnapshot/List created %s", dir)
	}
	if _, err := st.Save(context.Background(), sample(t, "https://example.com", time.Now()), ""); err != nil {
		t.Fatalf("Save should create the directory: %v", err)
	}
}
