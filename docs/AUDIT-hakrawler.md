# Phase 1 — Inventory & Audit: `hakluke/hakrawler`

**Audited:** 13 Sep 2026 · local clone at `Downloads/hakrawler` · module `github.com/hakluke/hakrawler` · license MIT

---

## 1. Size and shape

| Metric | Value |
|---|---|
| Tracked files (excl. `.git`) | **6** |
| Total lines, all files | **599** |
| Go source files | **1** (`hakrawler.go`) |
| Go lines (raw) | **298** |
| Go lines (non-blank, non-comment) | **231** |
| Go packages | **1** (`package main`) |
| Exported types | **1** (`Result`) |
| Functions | **5** (`main`, `parseHeaders`, `extractHostname`, `printResult`, `isUnique`) |
| Tests | **0** |
| CI config | none |
| Makefile / lint config | none |

Supporting files: `go.mod` (16 L), `go.sum` (147 L), `README.md` (127 L), `Dockerfile` (10 L), `.gitignore` (1 L).

**Read this number carefully: 231 lines of real logic.** That is the whole tool. It is not a framework you are forking — it is a 231-line wrapper around Colly. That matters for Phase 2: there is almost nothing here worth preserving as code, and you should say exactly that, in those terms, when someone asks "so what did you actually build?"

---

## 2. How the crawler works

**Entry point:** `main()` in `hakrawler.go:33`. No `cmd/`, no subcommands, no library surface. `flag` package, 14 boolean/int/string flags, all global.

**Input:** stdin only, and it is enforced — `hakrawler.go:64-68` stats stdin and exits 1 if it is a character device (i.e. a terminal). There is no `-u`/`-url` flag. `echo https://x | hakrawler` is the only invocation model.

**The crawl loop** (`hakrawler.go:71-199`), which is the part worth understanding:

```
goroutine {
  for each line of stdin {
      hostname := extractHostname(line)
      c := colly.NewCollector(...)      // <-- a NEW collector, per input URL
      register OnHTML callbacks
      c.Visit(line); c.Wait()           // <-- fully blocking, one target at a time
  }
  close(results)
}
main goroutine: for res := range results { fmt.Fprintln(w, res) }
```

Two structural consequences:

- **One collector per input line.** Targets are processed strictly serially — target *N+1* is not touched until target *N*'s entire crawl tree has drained. The `-t` flag parallelises *within* one target, never *across* them. Feeding it 500 hosts from `httpx` is 500 sequential crawls.
- **No shared state between targets.** New transport, new connection pool, new visited-set, new rate limiter (such as it is) each time. Nothing is amortised.

**URL discovery:** three Colly `OnHTML` callbacks and nothing else:

| Selector | Line | Emitted? | Crawled? |
|---|---|---|---|
| `a[href]` | 125 | yes | **yes** |
| `script[src]` | 136 | yes | no |
| `form[action]` | 141 | yes | no |

Only anchors are recursed into. Scripts and form actions are printed and dropped. Recursion is `e.Request.Visit(link)`, so depth is Colly's `MaxDepth` (default 2), tracked per-request internally.

**Output:** one line per discovered URL to stdout through a `bufio.Writer`, produced in `printResult` (`hakrawler.go:257`). Composition is string concatenation:

- default: `https://target/path`
- `-s`: `[href] https://target/path`
- `-w`: `[https://parent/page] https://target/path`
- `-json`: `{"Source":"href","URL":"...","Where":"..."}` — one object per line, **not** a JSON array, and the field names are the Go field names (capitalised, no struct tags).

---

## 3. Concurrency model

**There isn't one of hakrawler's own.** All concurrency is delegated to Colly:

- `colly.Async(true)` (line 101) — Colly runs its own internal worker set.
- `c.Limit(&colly.LimitRule{DomainGlob: "*", Parallelism: *threads})` (line 122) — caps in-flight requests at `-t` (default 8), globally, not per host.
- `c.Wait()` blocks until Colly's WaitGroup drains.

hakrawler itself owns exactly three concurrency primitives:

1. One producer goroutine (the stdin reader + crawl driver).
2. `results := make(chan string, *threads)` — a buffered string channel, capacity 8 by default.
3. `var sm sync.Map` — package-level global, used only by `isUnique`.

**There is no frontier, no worker pool, no backpressure, and no cancellation.** No `context.Context` appears anywhere in the file. Colly owns the queue; hakrawler cannot see it, size it, prioritise it, or stop it.

### Defects found in the concurrency code

These are worth listing because they are exactly the failure modes your bounded-worker-pool rewrite is supposed to eliminate — and because "I audited the original and found N concurrency bugs" is a better interview line than "I rewrote it."

**(a) Goroutine leak on timeout — `hakrawler.go:172-192`.** When `-timeout` fires, the code logs and `continue`s to the next stdin line. The inner goroutine running `c.Visit`/`c.Wait` is *not* cancelled — Colly has no stop mechanism here — so its workers keep fetching in the background against a target the tool has already abandoned. The `finished` channel on that branch is never closed. Over a long input list these accumulate.

**(b) `recover()` used as flow control — `hakrawler.go:281-285`.** `printResult` wraps its channel send in a deferred `recover()` with a comment admitting why: the timeout path can close `results` while leaked workers are still sending, which panics on send-to-closed-channel. The panic is swallowed rather than prevented. This is (a)'s symptom, treated instead of the cause.

**(c) Unreachable second drain loop — `hakrawler.go:204-215`.** 

```go
if *unique { for res := range results { if isUnique(res) {...} } }
for res := range results { ... }          // <-- always runs
```

The `-u` loop drains `results` to close; the unconditional loop that follows then ranges over a closed, empty channel and does nothing. It happens to produce correct output, but only by accident of ordering — the intent was clearly `if/else`. A reviewer reads this as "nobody has looked at this code closely."

**(d) Unbounded global dedup set.** `sm sync.Map` never evicts and is package-scoped, so it accumulates every unique URL across *all* stdin targets for the process lifetime.

---

## 4. HTTP client & rate limiting

**Client:** Colly's default `http.Client`, with the `Transport` replaced by hakrawler (`hakrawler.go:154-165`) to inject the proxy and/or `InsecureSkipVerify`.

**Rate limiting: none.** This is the flat answer. What exists:

- `Parallelism` — a concurrency cap, not a rate. 8 concurrent requests against one host is 8 concurrent requests as fast as the network allows.
- Colly's `LimitRule` supports `Delay` and `RandomDelay`; hakrawler **exposes neither**.

What is entirely absent:

| | Present |
|---|---|
| Per-host token bucket / RPS cap | ✗ |
| Request delay or jitter | ✗ |
| Retries | ✗ |
| Exponential backoff | ✗ |
| 429 / 503 handling | ✗ |
| `Retry-After` header respect | ✗ |
| Per-request timeout (`http.Client.Timeout`) | ✗ — only a whole-target wall clock |
| Redirect cap | ✗ (only on/off via `-dr`) |
| `robots.txt` | ✗ — Colly's `IgnoreRobotsTxt` defaults to `true` and is never changed |
| Connection-pool tuning | ✗ — default `Transport`, and it's rebuilt per target anyway |

Practical effect: point it at a small site with `-t 32` and you are running an unthrottled 32-way hammer. Get a 429 and it is treated as an ordinary non-2xx — the URL is simply lost, silently, with no retry.

---

## 5. Link discovery mechanism

**Correcting a common assumption:** hakrawler does **not** use regex for link extraction. Colly parses with `goquery`, which sits on `golang.org/x/net/html` — a real, spec-compliant tokeniser. Extraction is CSS-selector based. That part is sound.

Regex appears in exactly one place, and it is the wrong place: **scope enforcement** (`hakrawler.go:112`).

```go
c.AllowedDomains = nil
c.URLFilters = []*regexp.Regexp{
    regexp.MustCompile(".*(\\.|\\/\\/)" + escapedHost + "((#|\\/|\\?).*)?"),
}
```

When `-subs` is passed, structural domain checking is thrown away (`AllowedDomains = nil`) and replaced with an **unanchored** regex over the full URL string. That regex matches any URL containing `//example.com` or `.example.com` followed by an optional delimiter — so `https://example.com.attacker.net/` matches, because `//example.com` appears and the trailing group is optional. **That is a scope escape.** A crawler that can be walked off-target by a hostname an attacker controls is a real finding, not a nitpick, and it is the single strongest argument in the repo for parsing URLs into a `*url.URL` and comparing host labels structurally instead of matching strings.

Second scope weakness: `-i` ("only crawl inside path") is implemented as `strings.Contains(abs_link, url)` (line 128) — substring containment on raw URL strings, with no path-boundary awareness.

**What is never parsed at all:**

- JavaScript — `.js` files are listed but never fetched or analysed. No endpoint extraction, no `jsluice`-style analysis. This is hakrawler's largest functional gap versus katana.
- Inline `<script>` bodies, JSON/XHR responses, source maps.
- `link[href]`, `img[src]`, `srcset`, `iframe[src]`, `video`/`audio`/`source`, `object`/`embed`, `area[href]`, `<base href>`, `meta http-equiv=refresh`, `Location` headers as findings.
- `sitemap.xml`, `robots.txt` (as a *source* of paths — gospider does this).
- Comments (`<!-- -->`), which routinely leak staging URLs and internal hostnames.

---

## 6. Output formats

Four presentation variants of a **single format: one URL per line of stdout.**

1. plain URL
2. `[source] URL` (`-s`)
3. `[where] URL` (`-w`)
4. NDJSON object with `Source` / `URL` / `Where` (`-json`)

No file output flag. No CSV. No DOT. No database. No sitemap. No HTTP metadata in the output at all — **no status code, no content type, no content length, no response headers, no timing, no depth, no discovery timestamp**. Once the pipe closes, everything the crawl learned is gone. There is no run identity, so there is nothing to compare two runs against.

This is the gap ReconGraph is actually built to fill, and it is worth being precise about it: hakrawler's output is a *stream of strings*, not a *record of a crawl*.

---

## 7. Dependencies (`go.mod`)

Declares `go 1.16`. **One direct dependency:**

- `github.com/gocolly/colly/v2 v2.1.1-0.20220308084714-a61109486557` — pinned to a **pseudo-version**, i.e. an untagged commit from 8 Mar 2022, not a release.

Everything else is marked `// indirect`: `PuerkitoBio/goquery v1.8.0`, `antchfx/htmlquery v1.2.4`, `antchfx/xmlquery v1.3.9`, `golang/groupcache`, `golang/protobuf v1.5.2`, `temoto/robotstxt v1.1.2`, `golang.org/x/net`, `google.golang.org/appengine v1.6.7`, `google.golang.org/protobuf v1.27.1`. `go.sum` covers ~22 modules.

Two observations:

- `temoto/robotstxt` is compiled in via Colly but never activated — robots support is present as dead weight.
- `google.golang.org/appengine` and protobuf are pulled in transitively for a 231-line CLI crawler. Colly is a heavy dependency for what is being used of it (three CSS selectors and a work queue).

**Dockerfile:** `FROM golang:1.17`, single stage, `go get -d -v ./...` (deprecated since Go 1.17), runs as root, ships the entire Go toolchain in the final image. A multi-stage build to `scratch`/`distroless` would take this from ~900 MB to ~15 MB.

---

## 8. What it does NOT do that a real recon tool should

Grouped by how much each one costs you to fix, because this is your build backlog.

**Correctness / safety (must fix, cheap):**
1. Structural scope enforcement — the `-subs` regex is escapable (§5).
2. `context.Context` end-to-end; cancellable crawls; no goroutine leaks.
3. Retries with exponential backoff; honour 429/503 and `Retry-After`.
4. Per-host rate limiting and jitter — the difference between a recon tool and a DoS.
5. Per-request timeouts, redirect caps, response-size caps enforced consistently.
6. Error surfacing — right now `c.Visit` errors, proxy-URL parse errors, and every non-2xx are all discarded silently. You cannot tell "no links found" from "everything 403'd".

**Data model (the actual thesis of ReconGraph):**
7. No persistence — nothing survives the pipe.
8. No response metadata (status, type, size, headers, timing, depth, parent).
9. No relationship model — you get URLs, never *what linked to what*.
10. No run identity, therefore no diff, no history, no change detection.
11. No queryability — grep is the only interface.

**Coverage:**
12. No JavaScript analysis (the biggest single coverage gap).
13. No `sitemap.xml` / `robots.txt` as discovery sources.
14. No passive extraction — emails, API paths, S3/GCS/Azure bucket refs, socials, API keys, internal hostnames in comments.
15. No technology fingerprinting.
16. Thin asset coverage — `img`, `link`, `iframe`, `srcset`, meta-refresh, comments all ignored.
17. No headless/JS-rendered crawling, so SPAs return near-nothing.
18. No auth flow beyond static headers — no cookie jar management, no login, no session refresh.

**Engineering hygiene:**
19. Zero tests, no CI, no linter, no `-version`, no reproducible release builds.
20. Single-file `package main` — nothing is importable as a library.
21. Global mutable state (`headers`, `sm`).
22. No config file; 14 flags with no subcommand grouping and no way to save a profile.

---

## Audit verdict

hakrawler is a well-scoped, honest little tool that does one thing: turn a URL into a list of URLs. It has 5.1k stars because it composes cleanly in a shell pipeline, not because it is deep. The code is **231 lines with four identifiable defects** (goroutine leak, `recover()`-as-control-flow, dead drain loop, scope-escape regex) and **one architecturally correct decision** — parsing HTML with a real parser rather than regex.

For your purposes: **do not fork this.** There is no code here to inherit. Take the two ideas you named — the crawl loop shape and stdin composability — start an empty module, and cite hakrawler as prior art in the README. A fork carries a 2022 pseudo-versioned Colly pin and a `package main` you would delete in the first commit. A clean-room build with honest attribution is both more defensible in an interview and less work.
