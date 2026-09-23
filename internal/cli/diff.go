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
  recongraph diff latest~1 latest --fail-on new-external-host,new-secret

Exit codes: 0 no rule tripped, 1 the diff could not run, 2 a --fail-on rule
tripped.

`)
		fs.PrintDefaults()
	}
	storeDir := fs.String("store", "", "Crawl store directory.")
	asJSON := fs.Bool("json", false, "Emit the diff as JSON.")
	ignoreContent := fs.Bool("ignore-content", false, "Do not report pages whose body bytes changed.")
	ignoreFindings := fs.Bool("ignore-findings", false, "Do not compare passive findings.")
	explain := fs.Bool("explain", false, "Show the canonical URLs being compared, for debugging noisy diffs.")
	failOn := fs.String("fail-on", "", "Exit 2 if any of these changed (comma separated): "+strings.Join(ruleNames(), ", ")+".")

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

	rules, err := parseRules(*failOn)
	if err != nil {
		return err
	}

	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(res); err != nil {
			return err
		}
	} else {
		printDiff(res, string(oldID), string(newID), *explain)
	}
	return checkRules(res, rules)
}

// Rules for `--fail-on`. The point is a scheduled CI job: crawl the site,
// diff against yesterday, and fail the build when something moved that
// nobody meant to move. A new third-party host on your pages is the one
// worth waking up for.
var failRules = []struct {
	name  string
	what  string
	count func(*diff.Result) int
}{
	{"new-external-host", "third-party hosts appeared", func(r *diff.Result) int { return len(r.AppearedHosts) }},
	{"new-secret", "secret-shaped strings appeared", func(r *diff.Result) int { return countFindings(r, "secret") }},
	{"new-finding", "passive findings appeared", func(r *diff.Result) int { return len(r.AppearedFindings) }},
	{"appeared", "pages appeared", func(r *diff.Result) int { return len(r.AppearedNodes) }},
	{"disappeared", "pages disappeared", func(r *diff.Result) int { return len(r.DisappearedNodes) }},
	{"changed", "pages changed", func(r *diff.Result) int { return len(r.ChangedNodes) }},
	{"restructured", "pages changed what they reference", func(r *diff.Result) int {
		n := 0
		for _, rs := range r.Restructured {
			if len(rs.AddedOut)+len(rs.RemovedOut) > 0 {
				n++
			}
		}
		return n
	}},
	{"any", "anything changed", func(r *diff.Result) int {
		if r.Empty() {
			return 0
		}
		return 1
	}},
}

func ruleNames() []string {
	out := make([]string, 0, len(failRules))
	for _, r := range failRules {
		out = append(out, r.name)
	}
	return out
}

func countFindings(r *diff.Result, kind string) int {
	n := 0
	for _, f := range r.AppearedFindings {
		if strings.Contains(f.Kind, kind) {
			n++
		}
	}
	return n
}

func parseRules(list string) ([]int, error) {
	if strings.TrimSpace(list) == "" {
		return nil, nil
	}
	var out []int
	for _, raw := range strings.Split(list, ",") {
		name := strings.ToLower(strings.TrimSpace(raw))
		if name == "" {
			continue
		}
		found := -1
		for i, r := range failRules {
			if r.name == name {
				found = i
				break
			}
		}
		if found < 0 {
			return nil, fmt.Errorf("unknown --fail-on rule %q; valid rules are %s", name, strings.Join(ruleNames(), ", "))
		}
		out = append(out, found)
	}
	return out, nil
}

// checkRules reports tripped rules on stderr and returns exit code 2, so the
// diff itself still goes to stdout for the build log.
func checkRules(res *diff.Result, rules []int) error {
	var tripped []string
	for _, i := range rules {
		r := failRules[i]
		if n := r.count(res); n > 0 {
			tripped = append(tripped, fmt.Sprintf("%s (%d %s)", r.name, n, r.what))
		}
	}
	if len(tripped) == 0 {
		return nil
	}
	return &ExitError{Code: 2, Err: fmt.Errorf("failing on %s", strings.Join(tripped, "; "))}
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
