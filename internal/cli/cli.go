// Package cli implements ReconGraph's command-line interface.
//
// Subcommands are dispatched by hand on top of the standard library's flag
// package rather than a CLI framework. That keeps the module at a single
// third-party dependency (golang.org/x/net/html), which for a security tool
// people are asked to install and run is worth more than the convenience.
package cli

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
)

// Version is overridden at build time:
//
//	go build -ldflags "-X github.com/swapnil5053/recongraph/internal/cli.Version=v0.2.0"
var Version = "0.1.2"

type command struct {
	name    string
	summary string
	run     func(args []string) error
}

func commands() []command {
	return []command{
		{"crawl", "Crawl a target and build its site graph", runCrawl},
		{"diff", "Compare two stored crawls", runDiff},
		{"query", "Ask questions of a stored crawl graph", runQuery},
		{"export", "Re-export a stored crawl in another format", runExport},
		{"fingerprint", "Fingerprint a single URL, or list the signature database", runFingerprint},
		{"list", "List stored crawls", runList},
		{"version", "Print the version", runVersion},
	}
}

// Main is the entry point. It returns a process exit code.
func Main(args []string) int {
	if len(args) < 2 {
		usage(os.Stderr)
		return 2
	}
	name := args[1]
	if name == "-h" || name == "--help" || name == "help" {
		usage(os.Stdout)
		return 0
	}
	if name == "-v" || name == "--version" {
		name = "version"
	}

	for _, c := range commands() {
		if c.name == name {
			if err := c.run(args[2:]); err != nil {
				fmt.Fprintf(os.Stderr, "recongraph %s: %v\n", name, err)
				return 1
			}
			return 0
		}
	}

	fmt.Fprintf(os.Stderr, "recongraph: unknown command %q\n\n", name)
	usage(os.Stderr)
	return 2
}

func usage(w io.Writer) {
	fmt.Fprintf(w, `ReconGraph %s - web reconnaissance and asset mapping

Most crawlers hand you a stream of URLs to grep once and throw away. ReconGraph
builds a directed graph of a target's web surface, persists it, and lets you
diff it against last month's.

USAGE
  recongraph <command> [flags]

COMMANDS
`, Version)
	for _, c := range commands() {
		fmt.Fprintf(w, "  %-12s %s\n", c.name, c.summary)
	}
	fmt.Fprint(w, `
EXAMPLES
  # Crawl a site; URLs to stdout, graph saved for later
  recongraph crawl -u https://example.com

  # Stay in a shell pipeline, hakrawler style
  cat hosts.txt | recongraph crawl | httpx

  # Subdomains in scope, depth 4, save an interactive report
  recongraph crawl -u https://example.com --subs -d 4 -o map.html -f html

  # What changed since last time?
  recongraph diff latest~1 latest

  # Ask the saved graph
  recongraph query latest --orphans
  recongraph query latest --path-to /admin

Run 'recongraph <command> -h' for the flags of a single command.

Use this only against systems you are authorised to test.
`)
}

func runVersion(args []string) error {
	fmt.Printf("recongraph %s\n", Version)
	return nil
}

// stringList is a repeatable string flag (-H a -H b).
type stringList []string

func (s *stringList) String() string { return strings.Join(*s, ",") }
func (s *stringList) Set(v string) error {
	*s = append(*s, v)
	return nil
}

// parseArgs parses flags that may appear before OR after positional arguments.
//
// The standard library's flag package stops at the first non-flag token, so
// `recongraph query latest --orphans` would silently ignore --orphans. Users
// reasonably expect subcommand-style tools to accept either order, and a flag
// that is quietly dropped is worse than one that errors.
func parseArgs(fs *flag.FlagSet, args []string) ([]string, error) {
	var positional []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		rest := fs.Args()
		if len(rest) == 0 {
			return positional, nil
		}
		positional = append(positional, rest[0])
		args = rest[1:]
	}
}
