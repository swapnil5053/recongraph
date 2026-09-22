# ADR-0003: A JavaScript lexer for endpoint extraction

- **Status:** Accepted
- **Date:** 2026-09-22

## Context

Endpoint extraction from scripts was three regexes over the raw file: one for
`fetch(`-style calls, one for API-shaped paths, one for absolute URLs. Checking it
against how minified bundles are actually written showed where it breaks:

- URLs inside comments were reported and crawled (`// fetch("/api/v1/old")`).
- Minified bundles escape slashes (`"\/api\/v2\/users"`), which the regexes
  didn't match at all.
- A regex literal containing a quote (`/"[^"]*"/`) threw off every match after
  it in the file.
- `fetch("/api/users/" + id)` was dropped entirely, although "there is a
  per-user endpoint under /api/users/" is useful to know.

## Options

1. **Keep patching the regexes.** Each fix adds a case; none of them fixes the
   underlying problem, which is that a regex can't tell whether it's inside a
   string, a comment or a regex literal.
2. **A real parser** (esbuild via a Go binding, goja, tdewolff/parse). Correct,
   but each is either a large dependency or cgo, and ADR-0002 argued for one
   dependency.
3. **A lexer.** Tokenising JavaScript is small compared to parsing it. The only
   hard part is telling a regex literal from division, and the usual
   previous-token heuristic handles real code.

## Decision

Option 3: `internal/jsscan`, about 600 lines, no dependencies. It produces token
streams (template substitutions get their own stream so a `fetch` inside
`${...}` is still found) and a second pass looks for:

- the URL argument of `fetch`, `axios`, `axios.get` and friends, `$.ajax`,
  `xhr.open(method, url)`, `navigator.sendBeacon`, and `new URL/Request/
  EventSource/WebSocket`;
- values of `url:`/`href:`/`endpoint:`-style keys;
- string literals that look like API paths or absolute URLs.

Strings joined with `+` or template literals with substitutions are kept as
*dynamic* endpoints with `{}` for the unknown parts. They become findings, not
crawl edges.

## Consequences

It still doesn't follow variables: `const base = "/api"; fetch(base + "/x")`
gives `/api` and a partial `{}/x`. Doing better means data flow, which is where
a real parser starts to pay off. Throughput is around 15 MB/s on a minified
bundle, well above what the rate limiter lets through. A fuzz test
(`FuzzEndpoints`) guards against panics and hangs on malformed input.
