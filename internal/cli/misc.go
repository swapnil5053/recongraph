package cli

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/swapnil5053/recongraph/internal/export"
	"github.com/swapnil5053/recongraph/internal/fetch"
	"github.com/swapnil5053/recongraph/internal/fingerprint"
	"github.com/swapnil5053/recongraph/internal/parse"
	"github.com/swapnil5053/recongraph/internal/store"
)

func runList(args []string) error {
	fs := flag.NewFlagSet("list", flag.ContinueOnError)
	storeDir := fs.String("store", "", "Crawl store directory.")
	target := fs.String("target", "", "Only crawls whose target contains this string.")
	count := fs.Bool("count", false, "Print how many crawls are stored and nothing else.")
	if err := fs.Parse(args); err != nil {
		return err
	}

	st, err := store.OpenSnapshot(*storeDir)
	if err != nil {
		return err
	}
	defer st.Close()

	metas, err := st.List(context.Background(), *target)
	if err != nil {
		return err
	}
	if *count {
		// For scripts deciding whether there is anything to diff against.
		fmt.Println(len(metas))
		return nil
	}
	if len(metas) == 0 {
		fmt.Printf("No stored crawls in %s.\n", st.Dir())
		return nil
	}
	fmt.Printf("%-34s %-28s %7s %7s %9s  %s\n", "ID", "TARGET", "NODES", "EDGES", "STATUS", "WHEN")
	for _, m := range metas {
		fmt.Printf("%-34s %-28s %7d %7d %9s  %s\n",
			m.ID, truncate(m.Target, 28), m.Nodes, m.Edges, m.Status,
			m.FinishedAt.Local().Format("2006-01-02 15:04"))
	}
	return nil
}

func runExport(args []string) error {
	fs := flag.NewFlagSet("export", flag.ContinueOnError)
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, `Usage: recongraph export <crawl> [flags]

Re-export a stored crawl. Formats: %s

  recongraph export latest -f html -o map.html
  recongraph export latest -f dot | dot -Tpng -o map.png

`, strings.Join(export.Formats(), ", "))
		fs.PrintDefaults()
	}
	storeDir := fs.String("store", "", "Crawl store directory.")
	format := fs.String("f", "json", "Output format.")
	out := fs.String("o", "", "Output file (default stdout).")
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
	return emit(g, *out, export.Format(*format))
}

func runFingerprint(args []string) error {
	fs := flag.NewFlagSet("fingerprint", flag.ContinueOnError)
	fs.Usage = func() {
		fmt.Fprint(os.Stderr, `Usage: recongraph fingerprint <url> [flags]
       recongraph fingerprint --list

Fingerprints a single URL without crawling it. This is a convenience wrapper:
during a crawl, fingerprinting runs inline and attaches to every node.

`)
		fs.PrintDefaults()
	}
	list := fs.Bool("list", false, "List the signature database and exit.")
	threshold := fs.Int("threshold", fingerprint.DefaultThreshold, "Confidence floor for reporting.")
	ua := fs.String("ua", fetch.DefaultUserAgent, "User-Agent header.")
	insecure := fs.Bool("insecure", false, "Skip TLS verification.")
	timeout := fs.Duration("timeout", 15*time.Second, "Request timeout.")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}

	engine, err := fingerprint.Load()
	if err != nil {
		return err
	}
	engine.SetThreshold(*threshold)

	if *list {
		sigs := engine.Signatures()
		fmt.Printf("%d signatures loaded\n\n", len(sigs))
		byCat := map[string][]string{}
		for _, s := range sigs {
			cat := "other"
			if len(s.Categories) > 0 {
				cat = s.Categories[0]
			}
			byCat[cat] = append(byCat[cat], s.Name)
		}
		cats := make([]string, 0, len(byCat))
		for c := range byCat {
			cats = append(cats, c)
		}
		sort.Strings(cats)
		for _, c := range cats {
			sort.Strings(byCat[c])
			fmt.Printf("  %-20s %s\n", c, strings.Join(byCat[c], ", "))
		}
		return nil
	}

	if len(pos) < 1 {
		fs.Usage()
		return fmt.Errorf("need a URL")
	}
	target := pos[0]
	if !strings.Contains(target, "://") {
		target = "https://" + target
	}
	u, err := url.Parse(target)
	if err != nil {
		return err
	}

	fc := fetch.DefaultConfig()
	fc.UserAgent = *ua
	fc.Insecure = *insecure
	fc.Timeout = *timeout
	fc.RespectRobots = false // a single explicit URL the operator asked for
	client, err := fetch.New(fc)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), *timeout+5*time.Second)
	defer cancel()
	resp, err := client.Get(ctx, target)
	if err != nil {
		return err
	}

	in := fingerprint.Input{Header: resp.Header, Body: resp.Body, URLPath: u.Path}
	if parse.IsHTML(resp.ContentType) {
		if page, err := parse.HTML(resp.Body, u); err == nil {
			in.Metas = page.Metas
			in.ScriptSrcs = page.ScriptSrcs
			in.Classes = page.CSSClasses
		}
	}

	techs := engine.Match(in)
	fmt.Printf("%s  [%d %s]\n\n", resp.URL, resp.StatusCode, http.StatusText(resp.StatusCode))
	if len(techs) == 0 {
		fmt.Println("No technologies detected above the confidence threshold.")
		return nil
	}
	for _, t := range techs {
		v := ""
		if t.Version != "" {
			v = " " + t.Version
		}
		fmt.Printf("  %-22s %3d%%  %s\n", t.Name+v, t.Confidence, strings.Join(t.Categories, ", "))
		for _, e := range t.Evidence {
			fmt.Printf("      %s\n", e)
		}
	}
	return nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-3] + "..."
}
