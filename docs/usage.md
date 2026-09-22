# Using ReconGraph

Reference for every subcommand. The [README](../README.md) has the short version.

## Trying it without a target

You need a site you're allowed to crawl. The quickest one is your own machine:

```sh
cd some/folder/with/html
python -m http.server 8000
# in another terminal
recongraph crawl -u http://127.0.0.1:8000/ --rate 20
recongraph query latest
```

Crawls are saved to `~/.recongraph/crawls` (`%USERPROFILE%\.recongraph\crawls`
on Windows). Set `RECONGRAPH_HOME` or pass `--store` to put them elsewhere.
Graphviz is only needed for `-f svg`.

## crawl

```sh
# URLs on stdout, graph saved to ~/.recongraph/crawls
recongraph crawl -u https://example.com

# reads stdin too, so it still drops into a pipeline
cat hosts.txt | recongraph crawl | httpx

# subdomains in scope, depth 4, interactive report
recongraph crawl -u https://example.com --subs -d 4 -o map.html -f html

# slow it right down for a production target
recongraph crawl -u https://example.com --rate 0.5 --burst 1 -c 4

# authenticated, restricted to one section
recongraph crawl -u https://example.com/app/ \
  -H "Cookie: session=abc" --path /app --exclude '/logout'
```

Output formats: `urls` (default), `json`, `adjacency`, `dot`, `svg`, `csv`, `html`.

`svg` shells out to Graphviz if you have it. `html` is a single self-contained
file with a force-directed view and no CDN references, which is usually more
useful.

## query

```sh
recongraph query latest                    # summary
recongraph query latest --orphans
recongraph query latest --hubs 20
recongraph query latest --external         # third-party hosts by reference count
recongraph query latest --path-to /admin
recongraph query latest --links-to /login
recongraph query latest --components       # separate apps sharing one host
recongraph query latest --tech wordpress
recongraph query latest --findings secret-like
recongraph query latest --status 403
```

`latest` also works as `latest~1`, `latest:example.com`, a full crawl ID, or any
unique prefix of one.

## diff

```sh
recongraph list
recongraph diff latest~1 latest
recongraph diff latest~1 latest --json --ignore-content
```

Output is grouped into appeared, disappeared, changed (status, content hash,
title or detected stack), restructured, third-party hosts, and findings.

"Restructured" is the one that needed the graph: same page, same status code,
different outbound references. A checkout page that quietly started loading a
script from a host that wasn't there last month shows up here and nowhere else.

## fingerprint

```sh
recongraph fingerprint https://example.com
recongraph fingerprint --list
```

64 signatures, embedded at build time. During a crawl this runs inline and
attaches per node rather than per site, because a marketing page behind a CDN
and a Django admin on the same host don't run the same stack.

Signatures declare optional probe paths (`/wp-admin/`, `/.git/HEAD`). Those are
requests to URLs nothing linked to, so they're off unless you ask for them.

## Defaults

- **robots.txt is respected.** Most tools here default the other way. There's an
  `--ignore-robots` flag for work you're authorised to do, and it warns.
- 2 requests/second per host, burst 4, with jitter. A host that answers 429 or
  503 gets its rate halved for the rest of the crawl.
- `--max-pages 2000`. If a budget truncates a crawl the summary says so and the
  stored status is `budget-exceeded`.
- Ctrl-C keeps the partial graph and tags it `interrupted`.

## Code layout

```
cmd/recongraph/   entry point
internal/
  cli/            subcommands
  scope/          what's in bounds
  frontier/       queue, dedup, termination
  fetch/          HTTP client, per-host limiter, retries, robots.txt
  parse/          HTML and sitemap extraction, no I/O
  passive/        emails, buckets, endpoints, secrets, internal hosts
  jsscan/         JavaScript lexer and endpoint extraction
  fingerprint/    signature engine + embedded database
  builder/        graph construction
  crawl/          pipeline wiring
  store/          persistence
  diff/           crawl comparison
  export/         json, dot, csv, html, svg
pkg/sitegraph/    the graph model, canonical URLs, encoders
site/             project page (GitHub Pages)
```

Only `sitegraph` is in `pkg/`, since it's the one thing you'd import to read
ReconGraph output without wanting the crawler.

## Development

```sh
make test         # go test ./...
make test-race    # go test -race ./...
make cover
make lint         # gofmt + go vet
make release      # static binaries for five platforms in dist/
```

The heaviest tests are on URL canonicalisation (`pkg/sitegraph/canonical.go`),
because diff quality depends on it almost entirely: normalise too little and
every crawl looks 100% changed, too much and real changes vanish.
`internal/cli/cli_test.go` runs the real binary's code path end to end against
a two-version test server: crawl, crawl again, then list, query, diff and export.

## Site

`site/index.html` is the project page: one static file, no build step. `.github/workflows/pages.yml` deploys it to GitHub Pages on any push that touches `site/`. Turn Pages on in the repo settings with the source set to GitHub Actions.
