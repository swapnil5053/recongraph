package cli

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/swapnil5053/recongraph/internal/diff"
	"github.com/swapnil5053/recongraph/internal/store"
)

func runDiff(args []string) error {
	fs := flag.NewFlagSet("diff", flag.ContinueOnError)
	fs.Usage = func() {
		fmt.Fprint(os.Stderr, `Usage: recongraph diff <old> <new> [flags]

<old> and <new> are crawl references: a full ID, a unique ID prefix,
"latest", "latest:<target>", or "latest~N" for the Nth most recent.

  recongraph diff latest~1 latest
  recongraph diff 20260901T101500Z_example.com latest

`)
		fs.PrintDefaults()
	}
	storeDir := fs.String("store", "", "Crawl store directory.")
	asJSON := fs.Bool("json", false, "Emit the diff as JSON.")
	ignoreContent := fs.Bool("ignore-content", false, "Do not report pages whose body bytes changed.")
	ignoreFindings := fs.Bool("ignore-findings", false, "Do not compare passive findings.")
	explain := fs.Bool("explain", false, "Show the canonical URLs being compared, for debugging noisy diffs.")

	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 2 {
		fs.Usage()
		return fmt.Errorf("need exactly two crawl references")
	}

	ctx := context.Background()
	st, err := store.OpenSnapshot(*storeDir)
	if err != nil {
		return err
	}
	defer st.Close()

	oldID, err := resolveRef(ctx, st, pos[0])
	if err != nil {
		return err
	}
	newID, err := resolveRef(ctx, st, pos[1])
	if err != nil {
		return err
	}
	if oldID == newID {
		return fmt.Errorf("both references resolve to the same crawl (%s)", oldID)
	}

	oldG, err := st.Load(ctx, oldID)
	if err != nil {
		return err
	}
	newG, err := st.Load(ctx, newID)
	if err != nil {
		return err
	}

	res := diff.Compare(oldG, newG, diff.Options{
		IgnoreContentHash: *ignoreContent,
		IgnoreFindings:    *ignoreFindings,
	})

	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(res)
	}

	printDiff(res, string(oldID), string(newID), *explain)
	return nil
}

// resolveRef adds "latest~N" on top of the store's own reference resolution.
func resolveRef(ctx context.Context, st *store.Snapshot, ref string) (store.ID, error) {
	if base, nStr, ok := strings.Cut(ref, "~"); ok && strings.HasPrefix(base, "latest") {
		n, err := strconv.Atoi(nStr)
		if err != nil || n < 0 {
			return "", fmt.Errorf("bad reference %q: expected latest~N", ref)
		}
		target := strings.TrimPrefix(base, "latest:")
		if target == "latest" {
			target = ""
		}
		metas, err := st.List(ctx, target)
		if err != nil {
			return "", err
		}
		if n >= len(metas) {
			return "", fmt.Errorf("only %d stored crawls%s, cannot go back %d", len(metas), targetNote(target), n)
		}
		return metas[n].ID, nil
	}
	return st.Resolve(ctx, ref)
}

func targetNote(t string) string {
	if t == "" {
		return ""
	}
	return " for " + t
}

func printDiff(r *diff.Result, oldID, newID string, explain bool) {
	fmt.Printf("-- diff -----------------------------------------------\n")
	fmt.Printf("  old  %s\n", oldID)
	fmt.Printf("  new  %s\n\n", newID)

	if r.Empty() {
		fmt.Println("  No changes.")
		fmt.Printf("-------------------------------------------------------\n")
		return
	}

	section("APPEARED", len(r.AppearedNodes), func() {
		for _, n := range r.AppearedNodes {
			status := ""
			if n.StatusCode != 0 {
				status = fmt.Sprintf(" [%d]", n.StatusCode)
			}
			fmt.Printf("  + %s%s\n", n.URL, status)
		}
	})

	section("DISAPPEARED", len(r.DisappearedNodes), func() {
		for _, n := range r.DisappearedNodes {
			fmt.Printf("  - %s\n", n.URL)
		}
	})

	section("CHANGED", len(r.ChangedNodes), func() {
		for _, c := range r.ChangedNodes {
			fmt.Printf("  ~ %s\n", c.URL)
			for i, f := range c.Fields {
				fmt.Printf("      %-12s %s -> %s\n", f, short(c.OldValue[i]), short(c.NewValue[i]))
			}
		}
	})

	// The class of change that only exists because this is a graph.
	restructured := 0
	for _, rs := range r.Restructured {
		if len(rs.AddedOut)+len(rs.RemovedOut) > 0 {
			restructured++
		}
	}
	section("RESTRUCTURED (same page, different references)", restructured, func() {
		for _, rs := range r.Restructured {
			if len(rs.AddedOut)+len(rs.RemovedOut) == 0 {
				continue
			}
			fmt.Printf("  * %s\n", rs.URL)
			for _, e := range rs.AddedOut {
				fmt.Printf("      + now links to %s [%s]\n", e.Dst, e.Rel)
			}
			for _, e := range rs.RemovedOut {
				fmt.Printf("      - no longer links to %s [%s]\n", e.Dst, e.Rel)
			}
		}
	})

	section("NEW THIRD-PARTY HOSTS", len(r.AppearedHosts), func() {
		for _, h := range r.AppearedHosts {
			fmt.Printf("  + %s\n", h)
		}
	})
	section("THIRD-PARTY HOSTS GONE", len(r.DisappearedHosts), func() {
		for _, h := range r.DisappearedHosts {
			fmt.Printf("  - %s\n", h)
		}
	})

	section("NEW FINDINGS", len(r.AppearedFindings), func() {
		for _, f := range r.AppearedFindings {
			fmt.Printf("  + [%s] %s\n", f.Kind, f.Value)
		}
	})
	section("FINDINGS GONE", len(r.DisappearedFindings), func() {
		for _, f := range r.DisappearedFindings {
			fmt.Printf("  - [%s] %s\n", f.Kind, f.Value)
		}
	})

	if explain {
		fmt.Printf("\n  edges: +%d / -%d (compared as canonical src->dst[rel] triples)\n",
			len(r.AppearedEdges), len(r.DisappearedEdges))
	}
	fmt.Printf("-------------------------------------------------------\n")
}

func section(title string, n int, body func()) {
	if n == 0 {
		return
	}
	fmt.Printf("%s (%d)\n", title, n)
	body()
	fmt.Println()
}

func short(s string) string {
	if s == "" {
		return "(none)"
	}
	if len(s) > 60 {
		return s[:57] + "..."
	}
	return s
}
