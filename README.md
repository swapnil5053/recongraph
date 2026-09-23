# ReconGraph

A web crawler that maps a site as a graph, saves every crawl, and tells you what changed between them. Written in Go.

![HTML export of a 150-page crawl of books.toscrape.com](docs/images/graph.png)

Most recon crawlers (hakrawler, gospider, katana) print a list of URLs you grep once and throw away. ReconGraph keeps the structure: which page links to which, what's only reachable from a script, which third-party hosts load code, and what moved since the last crawl.

## Quick start

```sh
go install github.com/swapnil5053/recongraph/cmd/recongraph@latest

recongraph crawl -u https://books.toscrape.com/ --max-pages 150
recongraph query latest --hubs 5          # most-linked pages
recongraph query latest --orphans         # pages nothing links to
recongraph query latest --path-to /admin  # click path from the start page
recongraph diff latest~1 latest           # what changed since the last crawl
recongraph export latest -f html -o map.html
```

Prebuilt binaries for Windows, macOS and Linux are on the [releases page](https://github.com/swapnil5053/recongraph/releases).

## Features

- **Graph model.** Pages, scripts, images, forms and API endpoints are nodes. Links, redirects, script loads and JS calls are typed edges.
- **Crawl history and diff.** Each crawl is saved as gzipped JSON. `diff` reports pages that appeared, disappeared or changed, and pages that kept their URL and status but changed what they link to or load.
- **Queries** over the saved graph: hubs, orphans, shortest click path, third-party hosts, connected components, tech stack, status codes.
- **JavaScript endpoint extraction** with a small lexer instead of regex. Comments, escaped slashes and regex literals don't cause false hits; string constants are folded, so `const base = "/api"; fetch(base + "/users")` resolves to `/api/users`; what's still assembled at runtime is reported as a partial like `/api/users/{}`.
- **Technology fingerprinting** for 64 technologies, per page, with a confidence score and the evidence behind each match.
- **Passive findings:** emails, cloud storage buckets, secret-shaped strings, internal hostnames.
- **Exports:** JSON, CSV, Graphviz DOT and SVG, and a self-contained interactive HTML map.
- **Safe defaults:** respects robots.txt, rate-limits per host, backs off on 429 and 503, caps crawl size.

## How it works

```
seeds ─▶ FRONTIER ──tasks──▶ WORKERS (N) ──results──▶ BUILDER ─┐
          queue, dedup        fetch, parse,            owns the  │
          in-flight count     fingerprint              graph     │
             ▲                                                   │
             └──────────────── new URLs ─────────────────────────┘
```

The stages form a cycle of bounded channels, which deadlocks if the frontier ever blocks on a single operation. Its `select` offers the send and the receive together, with a nil channel disabling the send when the queue is empty, so it always drains. The crawl ends when the queue is empty and nothing is in flight, since a `WaitGroup` can't track work that creates more work. Only the builder goroutine writes to the graph, so the graph needs no locks.

## Scope

Crawling is HTTP only: ReconGraph reads what the server sends, and doesn't run
a headless browser, so a single-page app that builds its DOM at runtime comes
back thin ([katana](https://github.com/projectdiscovery/katana) covers that
case). Discovery is limited to what crawling reaches, with no Wayback or
certificate-transparency lookups, and the graph is held in memory, which the
`--max-pages` default keeps to single-digit megabytes.

## Engineering

- One third-party dependency (`golang.org/x/net/html`), no cgo, static binaries for five platforms built by a release workflow.
- 131 tests and a fuzz target, run under the race detector in CI. The heaviest coverage is on URL canonicalisation, because diff accuracy depends on it.
- An end-to-end test crawls a test site, changes it, crawls again, and checks the list, query, diff and export output.
- Crawling two real sites (books.toscrape.com and pypi.org) turned up bugs the unit tests missed, each now covered by a regression test: shared assets outranking pages as hubs, library comments reported as findings, a 300,000-URL sitemap swallowing a 40-page crawl, and `.dev` domains flagged as internal hosts.
- Design decisions, including reversed ones, are written up as [ADRs](docs/adr/).

## Links

[Usage reference](docs/usage.md) · [Design decisions](docs/adr/) · [Notes on hakrawler](docs/AUDIT-hakrawler.md) · [Project page](https://swapnil5053.github.io/recongraph/)

ReconGraph began as a rewrite of [hakrawler](https://github.com/hakluke/hakrawler). It keeps the shape of the crawl loop and reading seeds from stdin, but none of the code. Only crawl sites you own or have permission to test. MIT licence.
