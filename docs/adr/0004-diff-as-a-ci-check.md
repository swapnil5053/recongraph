# ADR-0004: `diff --fail-on` as a CI check

- **Status:** Accepted
- **Date:** 2026-09-23

## Context

The graph makes one class of change visible that a list of URLs cannot: a page
that keeps its URL and status code but starts referencing something new. The
obvious case is a script from a host that wasn't there yesterday, which is how
Magecart-style compromises and the Polyfill.io incident looked from outside.

Reading that out of a diff by eye means somebody has to run the diff and read
it. A check that runs on a schedule and fails when the graph moves is worth
more than a report nobody opens.

## Decision

`recongraph diff` takes `--fail-on <rules>` and exits 2 when any named rule
matched. Rules are named classes of change (`new-external-host`, `new-secret`,
`new-finding`, `appeared`, `disappeared`, `changed`, `restructured`, `any`),
not thresholds.

Three details worth stating:

1. **Exit 2, not 1.** 1 already means the diff could not run: bad reference,
   unreadable store. A CI step needs to tell "the check tripped" from "the
   tool broke", because the second one is not a finding about the site.
2. **The diff still prints to stdout.** The failure message on stderr names
   the rules and counts; the build log holds what actually moved. A red build
   that doesn't say what changed wastes the person who opens it.
3. **No thresholds.** "Fail if more than 5 pages appeared" invites tuning a
   number until the check is quiet. Pick the classes you care about; if a
   class is noisy on your site, the honest fix is `--ignore-content` or not
   watching that class.

`action.yml` wraps this for GitHub Actions. The crawl store is the state the
check compares against, so it goes in `actions/cache` keyed by URL, with
`restore-keys` picking up the newest previous run. The first run has nothing
to compare against and says so instead of failing.

## Consequences

The tool now has a use that fits in one sentence, and the graph model pays for
itself: `restructured` is only detectable because edges are stored, and it is
the rule this is really for.

Cached state is the weak point. If the cache is evicted, the next run has no
baseline and passes silently; that is the right failure direction but it means
a quiet check isn't proof of a quiet site. A crawl store kept in a repository
or object store would be firmer, and the store interface from ADR-0002 is
where that would go.
