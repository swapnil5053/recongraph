package cli

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"net/url"
	"os"
	"os/signal"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/swapnil5053/recongraph/internal/crawl"
	"github.com/swapnil5053/recongraph/internal/export"
	"github.com/swapnil5053/recongraph/internal/fetch"
	"github.com/swapnil5053/recongraph/internal/scope"
	"github.com/swapnil5053/recongraph/internal/store"
	"github.com/swapnil5053/recongraph/pkg/sitegraph"
)

func runCrawl(args []string) error {
	fs := flag.NewFlagSet("crawl", flag.ContinueOnError)
	fs.Usage = func() {
		fmt.Fprint(os.Stderr, `Usage: recongraph crawl [flags]

Seed URLs come from -u (repeatable) or from stdin, one per line.

`)
		fs.PrintDefaults()
	}

	var seeds stringList
	var headers stringList
	var include stringList
	var exclude stringList

	fs.Var(&seeds, "u", "Seed URL (repeatable). Omit to read seeds from stdin.")
	fs.Var(&headers, "H", "Extra request header, \"Name: value\" (repeatable).")
	fs.Var(&include, "include", "Only crawl URLs matching this regex (repeatable).")
	fs.Var(&exclude, "exclude", "Never crawl URLs matching this regex (repeatable).")

	depth := fs.Int("d", 3, "Maximum link depth from a seed. -1 for unlimited.")
	workers := fs.Int("c", 8, "Concurrent workers.")
	rate := fs.Float64("rate", 2, "Requests per second, per host.")
	burst := fs.Int("burst", 4, "Per-host burst allowance.")
	maxPages := fs.Int("max-pages", 2000, "Stop after this many pages. 0 for unlimited.")
	maxQueue := fs.Int("max-queue", 100000, "Maximum pending queue length.")
	subs := fs.Bool("subs", false, "Include subdomains of the seed hosts in scope.")
	pathPrefix := fs.String("path", "", "Restrict crawling to this path prefix.")
	timeout := fs.Duration("timeout", 15*time.Second, "Per-request timeout.")
	deadline := fs.Duration("deadline", 0, "Give up on the whole crawl after this long. 0 for no limit.")
	ua := fs.String("ua", fetch.DefaultUserAgent, "User-Agent header.")
	proxy := fs.String("proxy", "", "Proxy URL, e.g. http://127.0.0.1:8080")
	insecure := fs.Bool("insecure", false, "Skip TLS certificate verification.")
	ignoreRobots := fs.Bool("ignore-robots", false, "Ignore robots.txt. Only with authorisation.")
	noPassive := fs.Bool("no-passive", false, "Disable passive extraction (emails, buckets, endpoints).")
	noFinger := fs.Bool("no-fingerprint", false, "Disable technology fingerprinting.")
	noJS := fs.Bool("no-js", false, "Do not extract endpoints from JavaScript.")
	noSitemap := fs.Bool("no-sitemap", false, "Do not seed from robots.txt / sitemap.xml.")
	maxBody := fs.Int64("max-body", 5<<20, "Maximum response body to read, in bytes.")
	noQuery := fs.Bool("no-query", false, "Drop query strings entirely when canonicalising URLs.")
	stripParams := fs.String("strip-params", "", "Extra comma-separated query params to strip.")
	out := fs.String("o", "", "Write output to this file instead of stdout.")
	format := fs.String("f", "urls", "Output format: "+strings.Join(export.Formats(), ", "))
	noSave := fs.Bool("no-save", false, "Do not persist the crawl for later diffing.")
	storeDir := fs.String("store", "", "Crawl store directory (default ~/.recongraph/crawls).")
	label := fs.String("label", "", "Label to record with the stored crawl.")
	quiet := fs.Bool("q", false, "Suppress the summary on stderr.")

	if err := fs.Parse(args); err != nil {
		return err
	}

	seedURLs, err := collectSeeds(seeds)
	if err != nil {
		return err
	}
	if len(seedURLs) == 0 {
		fs.Usage()
		return fmt.Errorf("no seed URLs (use -u, or pipe them on stdin)")
	}

	canon := sitegraph.DefaultCanonOpts()
	canon.KeepQuery = !*noQuery
	for _, p := range strings.Split(*stripParams, ",") {
		if p = strings.TrimSpace(strings.ToLower(p)); p != "" {
			canon.StripParams[p] = true
		}
	}

	canonSeeds := make([]string, 0, len(seedURLs))
	rules := &scope.Rules{
		MaxDepth:        *depth,
		AllowSubdomains: *subs,
		PathPrefix:      *pathPrefix,
		RecordExternal:  true,
		AllowedSchemes:  []string{"http", "https"},
	}
	for _, s := range seedURLs {
		c, err := sitegraph.Canonicalize(s, canon)
		if err != nil {
			fmt.Fprintf(os.Stderr, "skipping seed %q: %v\n", s, err)
			continue
		}
		u, err := url.Parse(c)
		if err != nil {
			continue
		}
		rules.AddSeedHost(u.Hostname())
		canonSeeds = append(canonSeeds, c)
	}
	if len(canonSeeds) == 0 {
		return fmt.Errorf("no usable seed URLs (they must be absolute http/https URLs)")
	}

	if rules.Include, err = scope.CompilePatterns(include); err != nil {
		return err
	}
	if rules.Exclude, err = scope.CompilePatterns(exclude); err != nil {
		return err
	}

	hdr, err := parseHeaders(headers)
	if err != nil {
		return err
	}

	fc := fetch.DefaultConfig()
	fc.UserAgent = *ua
	fc.Headers = hdr
	fc.Timeout = *timeout
	fc.MaxBodyBytes = *maxBody
	fc.RateLimit = *rate
	fc.Burst = *burst
	fc.Proxy = *proxy
	fc.Insecure = *insecure
	fc.RespectRobots = !*ignoreRobots

	// Ctrl-C cancels the crawl but keeps whatever graph exists. Losing forty
	// minutes of crawling to an interrupt would make the tool untrustworthy.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if *deadline > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, *deadline)
		defer cancel()
	}

	if !*quiet {
		fmt.Fprintf(os.Stderr, "recongraph: crawling %d seed(s), depth %d, %d workers, %.1f req/s per host\n",
			len(canonSeeds), *depth, *workers, *rate)
		if *ignoreRobots {
			fmt.Fprintln(os.Stderr, "recongraph: robots.txt is being IGNORED - make sure you are authorised")
		}
	}

	g, report, err := crawl.Run(ctx, crawl.Options{
		Seeds:       canonSeeds,
		Rules:       rules,
		Fetch:       fc,
		Canon:       canon,
		Workers:     *workers,
		MaxPages:    *maxPages,
		MaxQueue:    *maxQueue,
		Fingerprint: !*noFinger,
		Passive:     !*noPassive,
		FollowJS:    !*noJS,
		UseSitemap:  !*noSitemap,
	})
	if err != nil {
		return err
	}

	if err := emit(g, *out, export.Format(*format)); err != nil {
		return err
	}

	if !*noSave {
		st, err := store.OpenSnapshot(*storeDir)
		if err != nil {
			return err
		}
		defer st.Close()
		id, err := st.Save(ctx, g, *label)
		if err != nil {
			return fmt.Errorf("saving crawl: %w", err)
		}
		if !*quiet {
			fmt.Fprintf(os.Stderr, "recongraph: saved as %s (in %s)\n", id, st.Dir())
		}
	}

	if !*quiet {
		printSummary(os.Stderr, g, report)
	}
	return nil
}

func emit(g *sitegraph.Graph, out string, format export.Format) error {
	// CSV writes a set of files, so it needs a path prefix rather than a stream.
	if format == export.FormatCSV && out != "" {
		prefix := strings.TrimSuffix(out, ".csv")
		if err := export.WriteCSVSet(prefix, g); err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "recongraph: wrote %s-{nodes,edges,findings}.csv\n", prefix)
		return nil
	}

	w := os.Stdout
	if out != "" {
		f, err := os.Create(out)
		if err != nil {
			return err
		}
		defer f.Close()
		w = f
	}
	if err := export.Write(w, g, format, ""); err != nil {
		return err
	}
	if out != "" {
		fmt.Fprintf(os.Stderr, "recongraph: wrote %s\n", out)
	}
	return nil
}

func printSummary(w *os.File, g *sitegraph.Graph, r crawl.Report) {
	fmt.Fprintf(w, "\n-- crawl summary --------------------------------------\n")
	fmt.Fprintf(w, "  target      %s\n", g.Target)
	fmt.Fprintf(w, "  status      %s in %s\n", r.Status, r.Duration.Round(time.Millisecond))
	fmt.Fprintf(w, "  graph       %d nodes, %d edges\n", g.NumNodes(), g.NumEdges())
	fmt.Fprintf(w, "  fetched     %d pages, %d errors\n", r.Builder.Pages, r.Builder.Errors)

	if len(r.Builder.StatusHist) > 0 {
		codes := make([]int, 0, len(r.Builder.StatusHist))
		for c := range r.Builder.StatusHist {
			codes = append(codes, c)
		}
		sort.Ints(codes)
		parts := make([]string, 0, len(codes))
		for _, c := range codes {
			parts = append(parts, fmt.Sprintf("%dx%d", r.Builder.StatusHist[c], c))
		}
		fmt.Fprintf(w, "  statuses    %s\n", strings.Join(parts, "  "))
	}

	if orph := g.Orphans(); len(orph) > 0 {
		fmt.Fprintf(w, "  orphans     %d (no inbound link - see `query --orphans`)\n", len(orph))
	}
	if hosts := g.ExternalHosts(); len(hosts) > 0 {
		fmt.Fprintf(w, "  3rd party   %d external hosts\n", len(hosts))
	}
	if n := len(g.Findings()); n > 0 {
		fmt.Fprintf(w, "  findings    %d passive findings\n", n)
	}
	if techs := g.TechSummary(); len(techs) > 0 {
		names := make([]string, 0, len(techs))
		for t := range techs {
			names = append(names, t)
		}
		sort.Strings(names)
		if len(names) > 8 {
			names = append(names[:8], fmt.Sprintf("+%d more", len(names)-8))
		}
		fmt.Fprintf(w, "  tech        %s\n", strings.Join(names, ", "))
	}

	// Truncation must be loud. A recon tool that silently drops work is lying.
	if r.Frontier.DroppedBudget > 0 {
		fmt.Fprintf(w, "  TRUNCATED   %d URLs dropped by --max-pages; the map is incomplete\n",
			r.Frontier.DroppedBudget)
	}
	if r.Frontier.DroppedQueue > 0 {
		fmt.Fprintf(w, "  TRUNCATED   %d URLs dropped by --max-queue; the map is incomplete\n",
			r.Frontier.DroppedQueue)
	}
	fmt.Fprintf(w, "-------------------------------------------------------\n")
}

// collectSeeds reads seeds from -u, or from stdin when no -u was given.
//
// Two deliberate differences from the tool this replaces: an interactive
// terminal is not a fatal error, and stdin is only consulted when there are no
// -u seeds. Reading stdin unconditionally means `recongraph crawl -u URL` hangs
// forever whenever stdin happens to be an open pipe, which is the common case
// inside scripts and CI.
func collectSeeds(flagSeeds []string) ([]string, error) {
	if len(flagSeeds) > 0 {
		return append([]string{}, flagSeeds...), nil
	}
	var seeds []string

	stat, err := os.Stdin.Stat()
	if err != nil {
		return seeds, nil
	}
	if (stat.Mode() & os.ModeCharDevice) != 0 {
		return seeds, nil // interactive terminal, nothing piped in
	}

	sc := bufio.NewScanner(os.Stdin)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		seeds = append(seeds, line)
	}
	return seeds, sc.Err()
}

func parseHeaders(list []string) (map[string]string, error) {
	if len(list) == 0 {
		return nil, nil
	}
	out := make(map[string]string, len(list))
	for _, h := range list {
		name, value, ok := strings.Cut(h, ":")
		if !ok {
			return nil, fmt.Errorf("bad header %q: expected \"Name: value\"", h)
		}
		out[strings.TrimSpace(name)] = strings.TrimSpace(value)
	}
	return out, nil
}
