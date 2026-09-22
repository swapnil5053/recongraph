# ReconGraph

[![CI](https://github.com/swapnil5053/recongraph/actions/workflows/ci.yml/badge.svg)](https://github.com/swapnil5053/recongraph/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/swapnil5053/recongraph)](https://github.com/swapnil5053/recongraph/releases)
[![Go](https://img.shields.io/badge/go-1.24-00ADD8)](go.mod)
[![License: MIT](https://img.shields.io/badge/license-MIT-green)](LICENSE)

A web crawler that maps a site as a directed graph, saves every crawl, and tells you what changed between them.

![HTML export of a 150-page crawl of books.toscrape.com](docs/images/graph.png)

Crawlers like hakrawler, gospider and katana print a stream of URLs you grep once and throw away. ReconGraph keeps the structure: which page links to which, what's only reachable from a script, which third-party hosts load code, and what moved since last week.

## Quick start

```sh
go install github.com/swapnil5053/recongraph/cmd/recongraph@latest
# or grab a binary from the releases page

recongraph crawl -u https://books.toscrape.com/ --max-pages 150
recongraph query latest --hubs 5          # most-linked pages
recongraph query latest --orphans         # pages nothing links to
recongraph query latest --path-to /admin  # click path from the home page
recongraph diff latest~1 latest           # what changed since the last crawl
recongraph export latest -f html -o map.html
```

## What it does

- **Graph, not a list.** Pages, scripts, images, forms and API endpoints are nodes; links, redirects, script loads and JS calls are typed edges.
- **Stored and diffable.** Every crawl is saved as gzipped JSON. `diff` reports pages that appeared, disappeared or changed, plus *restructured* pages: same URL and status, different outgoing links.
- **Queries.** Hubs, orphans, shortest click path, third-party hosts, connected components, tech stack, status codes.
- **Finds endpoints in JavaScript** with a small lexer rather than regex, so comments, escaped slashes and regex literals don't fool it. URLs built at runtime come back as partials like `/api/users/{}`.
- **Fingerprints 64 technologies** per page, with a confidence score and the evidence for each match.
- **Passive findings:** emails, cloud buckets, secret-shaped strings, internal hostnames.
- **Exports** to JSON, CSV, Graphviz DOT/SVG, and a self-contained interactive HTML map.
- **Polite by default:** respects robots.txt, rate-limits per host, backs off on 429/503, caps pages.

## How it works

```
seeds ─▶ FRONTIER ──tasks──▶ WORKERS (N) ──results──▶ BUILDER ─┐
          queue, dedup        fetch, parse,            owns the  │
          in-flight count     fingerprint              graph     │
             ▲                                                   │
             └──────────────── new URLs ─────────────────────────┘
```

The pipeline is a cycle of bounded channels, which deadlocks if the frontier ever waits on a single operation. It never does: its `select` offers the send and the receive together, with a nil channel disabling the send when the queue is empty. The crawl ends when the queue is empty and nothing is in flight; a `WaitGroup` can't express that because the crawl creates its own work. The builder is the only goroutine that touches the graph, so the graph has no locks.

## Engineering notes

- **One dependency** (`golang.org/x/net/html`), no cgo, static binaries for five platforms.
- **URL canonicalisation** is the most heavily tested code, because diff quality depends on it: normalise too little and every crawl looks changed, too much and real changes vanish.
- **122 tests and a fuzz target**, run with the race detector in CI, including an end-to-end test that crawls a two-version test site and checks the diff.
- **Decisions are written down:** [ADRs](docs/adr/) cover the architecture, dropping SQLite mid-build, and choosing a lexer over a JS parser.
- **Bugs found by running it end to end and against a real site** (sitemap pages hidden from orphans, assets ranked as hubs, library comments reported as findings) each have a regression test.

## Limitations

- No headless browser, so single-page apps come back nearly empty. [katana](https://github.com/projectdiscovery/katana) handles those.
- JS analysis doesn't follow variables: `fetch(base + "/x")` isn't resolved.
- No passive sources (Wayback, certificate transparency), and the graph lives in memory.

## More

[Full usage](docs/usage.md) · [Design decisions](docs/adr/) · [Notes on hakrawler](docs/AUDIT-hakrawler.md) · [Project page](https://swapnil5053.github.io/recongraph/)

Started as a rewrite of [hakrawler](https://github.com/hakluke/hakrawler): it keeps the crawl-loop shape and stdin input, but no code. Only crawl sites you own or are allowed to test. MIT licensed.
