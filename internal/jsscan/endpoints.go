package jsscan

import (
	"regexp"
	"sort"
	"strings"
)

// Source says how an endpoint was found.
type Source string

const (
	// SourceCall is the URL argument of fetch(), axios.get(), xhr.open() etc.
	SourceCall Source = "call"
	// SourceProperty is the value of a url:/href:/endpoint: style key.
	SourceProperty Source = "property"
	// SourceLiteral is a free-standing string that looks like an API path or
	// an absolute URL.
	SourceLiteral Source = "literal"
)

// Endpoint is a URL-ish string found in a script.
//
// Dynamic endpoints were built at runtime: a template literal with
// substitutions, or a string with "+" next to it. Value then holds the static
// parts with each hole written as "{}", e.g. "/api/users/{}/orders". They can't
// be fetched but they're still worth reporting.
type Endpoint struct {
	Value   string
	Source  Source
	Callee  string // for SourceCall: "fetch", "axios.get", ...
	Dynamic bool
}

var (
	reAPIish = regexp.MustCompile(`(?i)^/(?:api|rest|graphql|v[0-9]{1,2}|_next/data|wp-json|admin/api)(?:/|$|\?)`)
	reAbsURL = regexp.MustCompile(`(?i)^https?://[a-z0-9.\-]+\.[a-z]{2,24}(?::\d{2,5})?(?:[/?#]|$)`)
)

// HTTP client objects whose .get/.post/... take a URL first. Anything else
// with a .get() is far more likely a Map or a query builder.
var httpReceivers = map[string]bool{
	"axios": true, "$": true, "jQuery": true, "http": true, "$http": true,
	"api": true, "client": true, "ky": true, "superagent": true,
	"request": true, "httpClient": true, "instance": true,
}

var httpMethods = map[string]bool{
	"get": true, "post": true, "put": true, "patch": true, "delete": true,
	"head": true, "options": true, "request": true, "getJSON": true, "ajax": true,
}

var urlCtors = map[string]bool{
	"Request": true, "URL": true, "EventSource": true, "WebSocket": true,
}

var urlKeys = map[string]bool{
	"url": true, "href": true, "endpoint": true, "action": true, "src": true,
	"uri": true, "baseURL": true, "baseUrl": true, "apiUrl": true, "apiURL": true,
	"path": true,
}

// Endpoints lexes a script and returns every endpoint it can find, deduplicated
// and sorted. A value seen from several sources is reported once, preferring
// the most specific source (call, then property, then literal).
func Endpoints(src []byte) []Endpoint {
	best := map[string]Endpoint{}
	rank := map[Source]int{SourceCall: 3, SourceProperty: 2, SourceLiteral: 1}
	add := func(e Endpoint) {
		if !plausible(e.Value) {
			return
		}
		if cur, ok := best[e.Value]; ok && rank[cur.Source] >= rank[e.Source] {
			return
		}
		best[e.Value] = e
	}

	for _, toks := range Lex(string(src)) {
		scanStream(toks, add)
	}

	out := make([]Endpoint, 0, len(best))
	for _, e := range best {
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Value < out[j].Value })
	return out
}

func scanStream(toks []Token, add func(Endpoint)) {
	for i, t := range toks {
		switch {
		case t.Kind == Punct && t.Value == "(":
			if callee, argIdx, ok := callTarget(toks, i); ok {
				if v, dyn, ok := argValue(toks, i, argIdx); ok {
					add(Endpoint{Value: v, Source: SourceCall, Callee: callee, Dynamic: dyn})
				}
			}

		case (t.Kind == Ident || t.Kind == String) && urlKeys[t.Value]:
			if i+2 < len(toks) && toks[i+1].Kind == Punct && (toks[i+1].Value == ":" || toks[i+1].Value == "=") {
				if v, dyn, ok := valueAt(toks, i+2); ok && (strings.HasPrefix(v, "/") || reAbsURL.MatchString(v)) {
					add(Endpoint{Value: v, Source: SourceProperty, Dynamic: dyn})
				}
			}

		case t.Kind == String || t.Kind == Template:
			v, dyn, ok := valueAt(toks, i)
			if !ok {
				continue
			}
			// A concatenated or templated string only counts as a literal if
			// its static prefix already looks like an API path.
			if reAPIish.MatchString(v) || (!dyn && reAbsURL.MatchString(v)) {
				add(Endpoint{Value: v, Source: SourceLiteral, Dynamic: dyn})
			}
		}
	}
}

// callTarget looks at the tokens before an opening parenthesis and decides
// whether this is a call that takes a URL, and which argument holds it.
func callTarget(toks []Token, paren int) (callee string, argIdx int, ok bool) {
	j := paren - 1
	if j < 0 || toks[j].Kind != Ident {
		return "", 0, false
	}
	name := toks[j].Value
	receiver := ""
	if j >= 2 && toks[j-1].Kind == Punct && toks[j-1].Value == "." && toks[j-2].Kind == Ident {
		receiver = toks[j-2].Value
		// this.http.get(...) -> receiver "http"
	}
	isNew := j >= 1 && toks[j-1].Kind == Ident && toks[j-1].Value == "new"

	switch {
	case name == "fetch" && (receiver == "" || receiver == "window" || receiver == "globalThis" || receiver == "self"):
		return "fetch", 0, true
	case name == "sendBeacon":
		return "sendBeacon", 0, true
	case name == "axios" && receiver == "":
		return "axios", 0, true
	case httpMethods[name] && httpReceivers[receiver]:
		return receiver + "." + name, 0, true
	case name == "open" && receiver != "":
		// xhr.open("GET", url). Only if the first argument is an HTTP method,
		// otherwise window.open(url) and friends would match as well.
		if v, _, ok := argValue(toks, paren, 0); ok && isMethod(v) {
			return "open", 1, true
		}
		if receiver == "window" {
			return "window.open", 0, true
		}
	case isNew && urlCtors[name]:
		return "new " + name, 0, true
	}
	return "", 0, false
}

func isMethod(s string) bool {
	switch strings.ToUpper(s) {
	case "GET", "POST", "PUT", "PATCH", "DELETE", "HEAD", "OPTIONS":
		return true
	}
	return false
}

// argValue returns the value of the argIdx'th argument of the call whose
// opening parenthesis is at toks[paren].
func argValue(toks []Token, paren, argIdx int) (string, bool, bool) {
	depth, arg := 0, 0
	first := true
	for k := paren + 1; k < len(toks); k++ {
		t := toks[k]
		if depth == 0 && first && arg == argIdx {
			return valueAt(toks, k)
		}
		first = false
		if t.Kind != Punct {
			continue
		}
		switch t.Value {
		case "(", "[", "{":
			depth++
		case ")", "]", "}":
			if depth == 0 {
				return "", false, false
			}
			depth--
		case ",":
			if depth == 0 {
				arg++
				first = true
				if arg > argIdx {
					return "", false, false
				}
			}
		}
	}
	return "", false, false
}

// valueAt reads a string or template at toks[k], noting whether it's
// concatenated with something ("/api/" + id) and so only partly known.
func valueAt(toks []Token, k int) (string, bool, bool) {
	t := toks[k]
	if t.Kind != String && t.Kind != Template {
		return "", false, false
	}
	v := t.Value
	dyn := t.Kind == Template && t.Dynamic
	if k+1 < len(toks) && toks[k+1].Kind == Punct && toks[k+1].Value == "+" {
		dyn = true
		v += "{}"
	}
	if k > 0 && toks[k-1].Kind == Punct && toks[k-1].Value == "+" {
		dyn = true
		v = "{}" + v
	}
	return v, dyn, true
}

// plausible rejects strings that can't be a URL or path: prose, markup, CSS
// selectors, single characters.
func plausible(v string) bool {
	if len(v) < 2 || len(v) > 2048 {
		return false
	}
	if strings.HasPrefix(v, "{}") {
		return false // nothing static at the front, so no idea where it goes
	}
	if strings.ContainsAny(v, " \t\n\r<>\"'`\\") {
		return false
	}
	if strings.HasPrefix(v, "//") && !strings.HasPrefix(v, "///") {
		return true // protocol-relative
	}
	if strings.HasPrefix(v, "/") {
		// Router patterns (/users/:id, /files/*) aren't real paths.
		return !strings.ContainsAny(v, ":*")
	}
	return strings.HasPrefix(v, "http://") || strings.HasPrefix(v, "https://") ||
		strings.HasPrefix(v, "ws://") || strings.HasPrefix(v, "wss://")
}
