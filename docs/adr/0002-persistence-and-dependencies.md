# ADR-0002 — Persistence behind an interface; near-zero dependencies

- **Status:** Accepted
- **Date:** 2026-09-19
- **Supersedes:** the storage section of [ADR-0001](0001-recongraph-architecture.md) (§4), and its CLI-framework assumption (§1)
- **Context:** written during implementation, after the decisions in ADR-0001 met the code

---

## Decision 1 — `store.Store` is an interface; the default backend has no dependencies

ADR-0001 said: in-memory graph during the crawl, serialised to **SQLite**, with diff expressed as SQL `EXCEPT` over node and edge tables.

The shipped design keeps the first half and changes the second. `internal/store` defines an interface:

```go
type Store interface {
    Save(ctx, *sitegraph.Graph, label string) (ID, error)
    List(ctx, target string) ([]Meta, error)
    Load(ctx, ID) (*sitegraph.Graph, error)
    Resolve(ctx, ref string) (ID, error)
    Close() error
}
```

The default implementation (`store.Snapshot`) writes one gzipped JSON document per crawl into a directory, with an atomic temp-file-and-rename write. Diff operates over the **graph model**, not over SQL.

### Why

**The honest trigger first.** The build environment for this project could reach `github.com` but not the `golang.org`, `gopkg.in`, `gitlab.com` or `modernc.org` vanity-import hosts. `modernc.org/sqlite` — the pure-Go driver ADR-0001 selected — resolves through hosts that were unreachable, and its dependency tree spans roughly a dozen more modules on the same hosts. That forced the question early rather than at the end. An ADR that hides the thing that prompted a decision is worth less than one that names it, so it is named here.

**But the reasoning stands on its own, and is better than the original.** Having been forced to look at it:

1. **Diff over the model beats diff over SQL.** ADR-0001 put the marquee feature — the thing this tool exists for — inside the one component with a heavy external dependency. Comparing graphs in Go instead means `internal/diff` is pure, has no I/O, runs in 3ms, and is tested with ten table-driven cases and no database. It also means diff works identically across every backend, present and future. Tying the differentiating feature to one storage engine was the weaker design; that only became obvious when the engine was in question.

2. **The distribution story survives intact.** ADR-0001 already chose pure-Go SQLite over the faster CGO driver specifically to preserve `CGO_ENABLED=0`, static binaries and clean cross-compilation. Dropping the dependency entirely serves that same goal more completely: the module now has **one** third-party dependency (`golang.org/x/net/html`), and the CI matrix cross-compiles to five platforms with no toolchain at all.

3. **The artifact stays inspectable.** A crawl is `gunzip -c crawl.json.gz | jq`. For a security artifact that an analyst may need to read, diff by hand, or commit to a case folder, that is worth more than query speed over data that is measured in thousands of rows, not millions.

### What is given up, honestly

No ad-hoc SQL over crawl history. Two mitigations: the `query` subcommand covers the questions the graph is actually for (orphans, hubs, reachability, third-party surface, tech, findings), and the interface means a SQLite backend can be added later without touching `diff`, `crawl`, or `export` — which is the point of putting it behind an interface rather than shipping one and hoping.

Loading is also all-or-nothing: the whole graph comes into memory. At the `--max-pages 2000` default that is single-digit megabytes, so it is not a constraint yet. If it becomes one, that is the signal to implement the SQLite backend, not a reason to pre-build it.

---

## Decision 2 — No CLI framework

ADR-0001's layout assumed a `cli` package built on a framework (Cobra was the unstated default). The shipped CLI is the standard library's `flag` package plus a ~40-line subcommand dispatcher.

Cobra pulls `spf13/pflag`, `inconshreveable/mousetrap` and `gopkg.in/yaml.v3`. For a security tool that strangers are asked to `go install` and then run against infrastructure, "one dependency, audit it in an afternoon" is a feature of the product, not just of the build. The dispatcher costs about forty lines and a `parseArgs` helper.

That helper is worth noting, because the standard library has a real ergonomic trap here: `flag.Parse` stops at the first non-flag token, so `recongraph query latest --orphans` silently ignores `--orphans`. This was caught by manual testing, not by a unit test, and it is exactly the class of bug a framework would have prevented for free. `parseArgs` permutes arguments so flags may appear before or after positionals. A flag that is quietly dropped is worse than one that errors.

---

## Decision 3 — Hand-written rate limiter

ADR-0001 §2 specified `golang.org/x/time/rate` for the per-host limiter. It is
about forty lines of token bucket, and given decisions 1 and 2 there was no
reason to add a dependency for it. Behaviour is unchanged: token bucket, burst,
jitter, robots `Crawl-delay` override.

One thing the hand-written version got wrong at first, caught by
`TestLimiterPaces`: the original `reserve()` slept until a token was due but
never decremented the balance, so the next caller found it already refilled and
went straight through. Sustained throughput was double the configured rate.
Consuming into a negative balance fixes it.

## Decision 4 — Signatures are JSON, not YAML

ADR-0001 sketched the signature database in YAML. It ships as JSON, embedded with `go:embed`, for one reason: YAML would have meant a parser dependency, and the schema is simple enough that JSON costs only some quotation marks. The structure, matcher types, weighting and implication cascade are unchanged from ADR-0001 §5.

---

## Consequences

**Positive.** One third-party dependency. `CGO_ENABLED=0` everywhere, five-platform cross-compile in CI, no schema migrations, no database file to corrupt. `internal/diff` is pure and fast to test. Crawl artifacts are readable with standard tools.

**Negative.** No SQL. A future SQLite backend is real work, not a flag — though the interface is what keeps it from being a rewrite. The `flag`-based CLI has rougher help output than Cobra's, and `parseArgs` is a workaround for a standard-library behaviour that a framework handles.

**Follow-up.** `go.mod` currently carries `replace golang.org/x/net => github.com/golang/net` for the build-environment reason described above. It is not part of the design and should be removed on any machine with normal network access: `make unpin`.
