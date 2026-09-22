# ADR-0001 — ReconGraph architecture

- **Status:** Accepted, partially superseded by [ADR-0002](0002-persistence-and-dependencies.md) (storage backend, CLI framework, signature format)
- **Date:** 2026-09-13
- **Context:** Successor to `hakluke/hakrawler` (notes in [`AUDIT-hakrawler.md`](../AUDIT-hakrawler.md))
- **Author:** Swapnil Kumar

---

## Part A: what to keep, what to rebuild

### Clean-room, not a fork

The audit found 231 lines of real logic in one `package main` file, a 2022 pseudo-versioned Colly pin, and four defects. There is nothing much to inherit, and a fork would carry its history and dependency choices for no benefit.

**Decision:** new module, empty history, and a "Prior art" section in the README crediting hakrawler for the stdin-composability model and the crawl-loop shape.

### Keeping (as concept, rewritten)

| Concept | What survives | What changes |
|---|---|---|
| Crawl loop | fetch → parse → enqueue discovered → repeat | becomes an explicit frontier + bounded worker pool, not Colly's opaque internal queue |
| stdin composability | `cat urls.txt \| recongraph crawl` must work, and default stdout must stay greppable one-URL-per-line | but stdin is now *one* input mode, not the only one; `-u/--url`, `--input-file`, and a config profile are added. hakrawler's hard exit when stdin is a TTY is a usability bug, not a philosophy |

### Rebuilding

My starting list held up except for one wrong assumption:

> **"Link/asset discovery (proper HTML parser, not regex)"**

hakrawler already uses a proper HTML parser: Colly → goquery → `x/net/html`. Regex appears only in its `-subs` scope filter, where it produces the scope-escape bug. So the accurate statement is that the original parses HTML correctly but enforces scope with an unanchored regex you can walk it out of. The work here is replacing string matching with structural URL comparison, and widening extraction from 3 selectors to ~15 plus passive content extraction.

Everything else on the rebuild list stands: proper Go layout, bounded worker pool with backpressure, real HTTP client with rate limiting and retries, structured output system.

### Adding

**1. Add a fifth subcommand: `query`.** The original plan was `crawl`, `diff`, `fingerprint`, `export`. But if a crawl is supposed to leave something queryable behind, the tool has to be able to query it; opening the database by hand doesn't count. `query --orphans`, `--hubs`, `--external`, `--tech`, `--path-to`, plus a `--sql` escape hatch. Small amount of code.

**2. `fingerprint` as a standalone subcommand is borderline.** Fingerprinting is a property of a fetched response, so it should run inline during `crawl` and attach to nodes. Keeping the subcommand only for "fingerprint one URL without crawling" is fine, but it has to be a thin wrapper. Two fingerprinting code paths would be a mistake.

**3. SVG export: no layout engine.** Graph layout is a hard, solved problem and not what this project is about. Emit DOT as the primary graph format and shell out to Graphviz for SVG, with a clear error if `dot` is missing. A self-contained HTML export with a small embedded force-directed view covers the case where Graphviz is not installed. Zero engineering budget for layout algorithms.

### Out of scope for v1

- **Headless / JS-rendered crawling.** Large lift: browser lifecycle, resource limits, XHR interception. Deferred, and the README should say so rather than leave it implied.
- **Passive source integration** (Wayback, CommonCrawl, VirusTotal). gospider does this; it is API plumbing, not architecture. Low signal for the effort.
- **Distributed crawling.** No.

---

## Part B: decisions

## 1. Project structure and Go package layout

```
recongraph/
├── cmd/recongraph/
│   └── main.go                  # cobra root; wires subcommands, nothing else
├── internal/
│   ├── cli/                     # subcommand definitions: crawl, diff, fingerprint, export, query
│   ├── config/                  # flag + file config, profile resolution, validation
│   ├── scope/                   # ScopeRule: host/subdomain/path/regex include-exclude, depth
│   ├── frontier/                # priority queue, visited set, depth accounting, termination detection
│   ├── fetch/                   # http.Client, per-host limiter, retry/backoff, robots cache
│   ├── parse/                   # HTML → []Candidate + PageMeta (x/net/html)
│   ├── passive/                 # regex extractors: email, S3/GCS/Azure, API paths, socials, comments
│   ├── fingerprint/             # signature engine + go:embed signature DB
│   ├── builder/                 # single-owner graph writer goroutine
│   ├── store/                   # SQLite persistence, migrations, queries
│   ├── diff/                    # snapshot comparison
│   └── export/                  # dot, json, csv, html writers
├── pkg/
│   └── sitegraph/               # Node, Edge, Graph, canonical URL, DOT/JSON encoders
├── signatures/                  # *.yaml, embedded at build time
├── testdata/                    # golden HTML fixtures, golden DOT/JSON outputs
└── docs/adr/
```

`internal/` is the default. A package goes in `pkg/` only if I would accept a bug report about its API from a stranger, and that is true of exactly one thing here: the graph model and its encoders, which someone might import to consume ReconGraph output. Everything else is implementation detail.

`cmd/recongraph/main.go` should be under 30 lines. All command logic in `internal/cli/`, so commands are unit-testable without spawning a process.

**Dependency direction is strictly inward:** `cli → crawl orchestration → {frontier, fetch, parse, builder, store}`, and `pkg/sitegraph` depends on nothing but stdlib. `parse` must not import `fetch`; it takes `[]byte` + a base URL and returns candidates. That constraint is what makes the parser testable against golden fixtures with no network.

---

## 2. Concurrency architecture

Three goroutine roles, connected by four bounded channels. One rule governs the whole design: **exactly one goroutine owns the graph, so the graph needs no lock.**

```
                    ┌──────────────────────────────────────────┐
                    │            FRONTIER (1 goroutine)        │
   seeds ──────────▶│  owns: priority queue, visited set,      │
                    │        depth map, in-flight counter      │
                    └───┬──────────────────────────────▲───────┘
                        │ readyCh                      │ candidateCh
                        │ (bounded, cap = 4×N)         │ (bounded)
                        ▼                              │
        ┌───────────────────────────────────┐          │
        │        WORKER POOL (N)            │          │
        │  fetch → parse → passive →        │──────────┘
        │  fingerprint                      │
        └───────────────┬───────────────────┘
                        │ resultCh (bounded)
                        ▼
        ┌───────────────────────────────────┐
        │     GRAPH BUILDER (1 goroutine)   │
        │  sole owner of *sitegraph.Graph   │
        │  → periodic flush to store        │
        └───────────────────────────────────┘
```

**Channel contract:**

| Channel | Direction | Payload | Capacity |
|---|---|---|---|
| `readyCh` | frontier → workers | `Task{URL, Depth, ParentID}` | `4 × workers` |
| `resultCh` | workers → builder | `*PageResult` (node meta, edges, findings, techs) | `2 × workers` |
| `candidateCh` | workers → frontier | `[]Candidate` (batched per page) | `2 × workers` |
| `doneCh` | frontier → all | close-only, termination signal | — |

### Two details

**(a) Deadlock avoidance in a cyclic pipeline.** Workers feed the frontier, and the frontier feeds workers. With bounded channels on both legs, the naive implementation deadlocks. Every worker blocks sending to a full `candidateCh` while the frontier blocks sending to a full `readyCh`. The fix is that **the frontier goroutine never blocks on a single operation**: its main loop is a `select` over *send-to-`readyCh`* and *receive-from-`candidateCh`* simultaneously:

```go
for {
    var out chan Task
    var next Task
    if q.Len() > 0 { out, next = readyCh, q.Peek() }   // nil channel disables the case
    select {
    case out <- next:            q.Pop(); inflight++
    case c := <-candidateCh:     q.PushAll(f.admit(c))  // scope + dedup + depth
    case d := <-doneNotifyCh:    inflight--; f.checkTermination()
    case <-ctx.Done():           return
    }
}
```

The nil-channel trick (a `nil` channel in a `select` case blocks forever, disabling that arm) means an empty queue simply stops offering work without a separate state machine.

**(b) Termination detection.** A `sync.WaitGroup` cannot terminate this pipeline, because work is generated *by* the work. The graph is cyclic and you don't know the total in advance. Use an explicit in-flight counter owned by the frontier: incremented when a task is dispatched, decremented when the builder confirms a page fully processed. **Crawl is complete when `queue.Len() == 0 && inflight == 0`.** The frontier then closes `doneCh`; workers drain and exit; the builder flushes and closes.

### Backpressure

Backpressure is the bounded channels themselves. A slow builder fills `resultCh`, which blocks workers on send, which stops them pulling from `readyCh`, which fills the queue in the frontier, which stops it dispatching. The system self-throttles to the speed of its slowest stage with no explicit coordination. Additionally the frontier enforces a hard `--max-queue` cap and a `--max-pages` budget; past the cap it drops lowest-priority candidates and increments a counter that is reported at the end (silently dropping work is how recon tools lie to you).

### Rate limiting placement

Per-host rate limiting lives **inside `fetch`, not in the pool**, as a `map[string]*rate.Limiter` (`golang.org/x/time/rate`) behind a `sync.RWMutex`. This is deliberate: worker count controls *parallelism* (a resource-consumption bound), the limiter controls *politeness per target* (a behavioural bound). Conflating them, as hakrawler does, means you cannot crawl 50 hosts fast while staying gentle on each one. Workers block in `limiter.Wait(ctx)`, which is correct: a blocked worker is applying backpressure, not wasting anything.

### Cancellation and partial results

`context.Context` threads from `cmd` through every layer. SIGINT triggers `cancel()`, and the builder **still flushes what it has to SQLite before exiting**, tagged `status='interrupted'`. A recon tool that throws away 40 minutes of crawl because you hit Ctrl-C is a broken recon tool. This covers audit defects (a) and (b): no leaked workers, no `recover()`-as-flow-control, because everything has one shared cancellation root.

---

## 3. Why a graph, not a flat URL list

A flat list answers one question: *what URLs exist?* A directed graph answers the question that actually matters in reconnaissance: *how is this application put together?*

The model: `Node = {URL, kind ∈ (page, script, stylesheet, image, form-target, external, api, bucket), status, content-type, size, depth, content-hash, techs[]}`. `Edge = {src, dst, rel ∈ (href, script, stylesheet, img, iframe, form-action, redirect, js-endpoint), context}`.

Structure that a list physically cannot represent:

- **In-degree ranking.** Pages linked from everywhere are nav/hubs; pages linked from exactly one place are usually the interesting ones. On a flat list every URL has equal weight.
- **Orphans and islands.** Nodes reachable only via a script or a sitemap, never via an anchor: the forgotten-admin-panel shape. Only detectable if you know what links to what.
- **Shortest path to a sensitive node.** "`/admin` is three clicks from the homepage, through `/dashboard`" is an exploitability statement. A list can only say `/admin` exists.
- **Connected components.** One domain often hosts several distinct applications; they show up as weakly-connected components with few edges between them. That is an architecture map, derived automatically.
- **External fan-out.** Every third-party host loaded as a script is supply-chain surface. The graph gives you the count, the pages affected, and the exact injection points.
- **Redirect chains** as first-class edges rather than a collapsed final URL.

And the decisive one: **diff over a graph is a different thing from diff over a set.** Set diff says *"3 URLs appeared, 1 disappeared."* Graph diff says *"the checkout page now references `api-v2.internal.corp`, and nothing links to `/legacy/upload` any more even though it still returns 200."* The second is a finding. The first is a changelog.

**Costs:** memory grows with edges not just pages, and a large site is edge-heavy, hence `--max-pages`, `--max-queue`, and interning URL strings into integer node IDs at insert. Cycles mean every traversal needs a visited set. And a graph is over-engineering for the "just give me a URL list" use case, which is why default stdout stays a plain URL stream and the graph is what's *persisted*, not what's *printed*.

---

## 4. Storage: in-memory graph → SQLite

**Decision:** authoritative graph in memory during the crawl (`map[NodeID]*Node` + adjacency slices, owned by the builder goroutine), incrementally flushed to SQLite in batched transactions, with the DB as the durable artifact.

**Why not SQLite-during-crawl?** Every discovered link would become a synchronous write on the hot path, and SQLite's single-writer model would serialise the crawl behind disk I/O.

**Why not in-memory only, dumped at the end?** Nothing survives a crash.

**The compromise:** builder accumulates and flushes every N nodes or T seconds inside one transaction. Crash or Ctrl-C loses at most one batch. Batched inserts in a single transaction are the difference between ~1k and ~100k rows/sec in SQLite; this is not a micro-optimisation, it is the whole reason the design works.

### Driver choice

Use **`modernc.org/sqlite`** (pure Go), not `mattn/go-sqlite3` (CGO). `mattn` is faster. But CGO breaks `CGO_ENABLED=0` static builds and makes cross-compilation to linux/darwin/windows-arm64 painful. For a Go recon tool the distribution story is "download one static binary, or `go install`", so I'd rather give up insert speed.

### Schema

```sql
crawls(id, target, started_at, finished_at, status, tool_version, config_json)
nodes(id, crawl_id, url_canonical, kind, scheme, host, path,
      status_code, content_type, content_length, content_hash,
      title, depth, first_seen)
edges(id, crawl_id, src_node_id, dst_node_id, rel, context)
findings(id, crawl_id, node_id, kind, value, evidence)      -- passive discovery
techs(id, crawl_id, node_id, name, version, confidence, evidence)
```

Indexes on `(crawl_id, url_canonical)`, `(crawl_id, host)`, `edges(crawl_id, src_node_id)`, `edges(crawl_id, dst_node_id)`. Migrations as numbered embedded SQL via `go:embed`, applied on open, never destructively.

### Diff mode

Two `crawl_id`s in one database. Three classes, all expressible in SQL:

- **appeared / disappeared**: `EXCEPT` on the `url_canonical` sets, both for nodes and for `(src_url, dst_url, rel)` edge triples.
- **changed**: same canonical URL, different `status_code`, `content_type`, `content_hash`, or tech set.
- **restructured**: same node, different in/out edge set. This is the class no flat tool can report.

**Everything here depends on URL canonicalisation.** Diff quality is almost entirely a function of it: lowercase scheme and host, strip default ports, resolve dot segments, drop fragments, sort query parameters, strip a configurable session/tracking list (`--strip-params`). Get it wrong and every crawl diffs as 100% changed. It lives in `pkg/sitegraph` and needs table-driven tests, 40+ cases.

---

## 5. Technology fingerprinting

**Decision:** declarative signature database, embedded at build time, evaluated against data the crawl already has.

```yaml
- name: WordPress
  categories: [cms]
  matchers:
    - {type: header,     name: x-powered-by, pattern: 'W3 Total Cache',  weight: 30}
    - {type: meta,       name: generator,    pattern: 'WordPress ([\d.]+)', version: 1, weight: 80}
    - {type: cookie,     name: 'wordpress_logged_in_.*',                  weight: 70}
    - {type: script-src, pattern: '/wp-(content|includes)/',              weight: 60}
    - {type: url-path,   pattern: '^/wp-json/',                           weight: 50}
  probes: ['/wp-admin/', '/wp-login.php']     # active only, opt-in
  implies: [PHP, MySQL]
```

**Matcher types:** response header, cookie name, `<meta name=generator>`, script `src` pattern, body regex, URL path, favicon hash. **Scoring:** weights sum per technology, report above a threshold with a confidence value; `implies` cascades (WordPress → PHP); version captured via a named capture group. Report a confidence and the evidence string rather than a boolean; "detected X because header Y matched Z" is something you can check.

**Passive by default, active behind a flag.** Everything above except `probes` runs on responses the crawler already fetched, so no extra requests. Path probes (`/wp-admin/`, `/.git/HEAD`, `/server-status`) are extra requests to paths you were not linked to, which is a different legal and ethical posture. `--probe` opt-in, rate-limited through the same per-host limiter, with a hard per-host probe budget. 

**Fingerprints attach to nodes, not to the site.** A CDN-fronted marketing page and a Django admin under the same host are different nodes with different tech. Per-node fingerprints are what make `recongraph query --tech` and tech-drift diffing possible; a site-level verdict cannot do either.

**Signature sourcing: check the licence.** Wappalyzer's dataset went proprietary in 2023, so it cannot be copied. `projectdiscovery/wappalyzergo` is MIT and fine as a reference for schema shape. Hand-writing 30-40 signatures is a weekend's work and avoids the question entirely.

---

## 6. Differentiation vs hakrawler, gospider, katana

First, a correction to my own plan: it listed technology fingerprinting as a novel feature. **Katana already ships technology detection (`-td`).** It is table stakes, not a differentiator.

| | hakrawler | gospider | katana | **ReconGraph** |
|---|---|---|---|---|
| HTML parsing | goquery, 3 selectors | colly, broad | broad + headless DOM | broad, ~15 selectors + comments |
| JS analysis | ✗ | link regex on JS | **jsluice-grade, `-jc`** | regex endpoints only (v1) |
| Headless | ✗ | ✗ | **✓** | ✗ (deferred, documented) |
| Passive sources (Wayback/CC/VT) | ✗ | **✓** | **✓** | ✗ (deferred) |
| Rate limiting | ✗ (parallelism only) | delay/random-delay | **per-host RPS + backoff** | per-host token bucket + backoff |
| Scope control | domain / regex (escapable) | white/blacklist regex | **dn/rdn/fqdn + regex** | structural host+path rules |
| Tech fingerprinting | ✗ | ✗ | **✓ (`-td`)** | ✓ per-node, with evidence + confidence |
| Passive extraction | ✗ | S3, subdomains | forms, page classes | emails, buckets, APIs, socials, comments |
| Output | text / NDJSON | text / JSON | **text / JSONL templated** | JSON, CSV, DOT, HTML, **SQLite** |
| **Relationship model** | ✗ | ✗ | ✗ | **✓ directed graph** |
| **Persistent store** | ✗ | files | files | **✓ SQLite, run-identified** |
| **Diff across runs** | ✗ | ✗ | ✗ | **✓ nodes, edges, tech, content** |
| **Queryable** | grep | grep | grep / jq | **✓ `query` subcommand + SQL** |

### Positioning

Katana is a better crawler than this one: headless rendering, real JS analysis. ReconGraph is not competing on crawl coverage. All four tools emit a stream you grep once and discard; the gap is in the output model, and that is what this builds for.

The bottom three rows of the table are the project. Everything above them is table stakes.

---

## Risks and open questions

1. **Canonicalisation is the diff feature's single point of failure.** Under-normalise → everything looks changed; over-normalise → real changes vanish. Mitigation: exhaustive table-driven tests, `--strip-params` configurable, ship a `diff --explain` that shows the canonical forms it compared.
2. **Graph memory on large targets.** Mitigation: integer node IDs with an interned URL table, `--max-pages` / `--max-queue` defaults set low (10k), explicit reporting when a budget truncates a crawl.
3. **JS coverage gap is real.** Modern SPAs will make ReconGraph look weak against katana. Mitigation: say so in the README; regex endpoint extraction from `.js` bodies in v1 buys back a meaningful fraction for a day's work.
4. **Ethics and legal.** Active probes and unthrottled crawling against hosts I don't own. Mitigation: robots.txt respected by default with a documented flag to disable it, conservative rate limits, a README statement on authorised testing, and probes off by default.
5. **Scope creep.** Five subcommands, four export formats, a signature database and a diff engine is a lot. Sequence: graph and crawl and JSON/DOT export first, solid, before storage; storage before diff; diff before fingerprinting. A finished three-feature tool beats a half-finished seven-feature one.
6. **Naming.** Check "ReconGraph" against pkg.go.dev and GitHub before committing to a module path. Renaming a published Go module is unpleasant.

---

## Consequences

**Positive:** every audit defect is prevented by structure rather than patched: leaks by `context`, races by single-owner-graph, scope escape by structural comparison, silent loss by persistence. The layout is idiomatic and testable without a network. The graph model creates three features (diff, query, structural analysis) that no comparable Go tool has.

**Negative:** a lot more code than hakrawler's 231 lines, realistically 3-5k. The frontier/builder split is more machinery than a `sync.WaitGroup` crawler needs and is only justified by graph ownership and termination. Pure-Go SQLite is slower than CGO. No headless means weak SPA coverage.

**Neutral:** Colly is dropped entirely. That buys correctness and control, and costs the ~600 lines of fetch/queue/robots plumbing it was providing for free.
