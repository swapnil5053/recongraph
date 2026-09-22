package jsscan

import (
	"reflect"
	"strings"
	"testing"
)

func kinds(toks []Token) []string {
	out := make([]string, len(toks))
	for i, t := range toks {
		out[i] = t.Kind.String() + ":" + t.Value
	}
	return out
}

func TestLexBasics(t *testing.T) {
	src := "let a = 'x\\'y' // gone\n/* gone */ b(1.5e-3, `t${c}u`)"
	streams := Lex(src)
	want := []string{
		"ident:let", "ident:a", "punct:=", "string:x'y",
		"ident:b", "punct:(", "number:1.5e-3", "punct:,", "template:t{}u", "punct:)",
	}
	if got := kinds(streams[0]); !reflect.DeepEqual(got, want) {
		t.Errorf("top level\n got %v\nwant %v", got, want)
	}
	if len(streams) != 2 || !reflect.DeepEqual(kinds(streams[1]), []string{"ident:c"}) {
		t.Errorf("substitution stream = %v", streams[1:])
	}
}

func TestLexRegexVersusDivision(t *testing.T) {
	cases := []struct {
		src  string
		want Kind // kind of the token starting with '/'
	}{
		{`x = /ab+c/g`, Regex},
		{`return /a"b/.test(s)`, Regex},
		{`if (ok) /re/.exec(s)`, Punct}, // ambiguous; ')' means division here
		{`total / count`, Punct},
		{`arr[0] / 2`, Punct},
		{`f(/[/"]/)`, Regex}, // slash and quote inside a class
	}
	for _, c := range cases {
		var got Kind
		for _, tok := range Lex(c.src)[0] {
			if strings.HasPrefix(tok.Value, "/") {
				got = tok.Kind
				break
			}
		}
		if got != c.want {
			t.Errorf("%q: '/' lexed as %v, want %v", c.src, got, c.want)
		}
	}
}

func TestLexEscapes(t *testing.T) {
	cases := map[string]string{
		`"\/api\/v2"`:          "/api/v2",
		`"\u002Fapi"`:          "/api",
		`"\u{1F600}"`:          "\U0001F600",
		`"\x2Fx"`:              "/x",
		`'it\'s'`:              "it's",
		"\"a\\\nb\"":           "ab", // line continuation
		`"https:\/\/e.com\/a"`: "https://e.com/a",
	}
	for src, want := range cases {
		toks := Lex(src)[0]
		if len(toks) != 1 || toks[0].Kind != String || toks[0].Value != want {
			t.Errorf("%s: got %v, want %q", src, kinds(toks), want)
		}
	}
}

func find(eps []Endpoint, v string) (Endpoint, bool) {
	for _, e := range eps {
		if e.Value == v {
			return e, true
		}
	}
	return Endpoint{}, false
}

func TestEndpoints(t *testing.T) {
	js := `
// fetch("/api/commented-out")
/* var old = "/api/v1/dead"; */
fetch("\/api\/v2\/users");
fetch('/api/users/' + id);
fetch(` + "`/api/v1/${id}/orders`" + `);
fetch(` + "`/api/health`" + `);
axios.get("/data/report.json");
$.post('/cart/add', {sku: 1});
xhr.open("POST", "/upload/chunk", true);
const u = new URL("/search", location.origin);
window.open("/help/contact");
cache.get("/not-a-request");
const cfg = {url: "/internal/export", path: "/users/:id", method: "POST"};
const cdn = "https:\/\/cdn.example.net\/lib.js";
const re = /"\/api\/fake"/;
const html = ` + "`<div>${fetch(\"/api/nested\")}</div>`" + `;
x = total / count; fetch("/api/after-division");
`
	eps := Endpoints([]byte(js))

	want := map[string]Source{
		"/api/v2/users":                  SourceCall,
		"/api/health":                    SourceCall,
		"/data/report.json":              SourceCall,
		"/cart/add":                      SourceCall,
		"/upload/chunk":                  SourceCall,
		"/search":                        SourceCall,
		"/help/contact":                  SourceCall,
		"/internal/export":               SourceProperty,
		"https://cdn.example.net/lib.js": SourceLiteral,
		"/api/nested":                    SourceCall,
		"/api/after-division":            SourceCall,
	}
	for v, src := range want {
		e, ok := find(eps, v)
		if !ok {
			t.Errorf("missing %s", v)
			continue
		}
		if e.Source != src {
			t.Errorf("%s: source %s, want %s", v, e.Source, src)
		}
		if e.Dynamic {
			t.Errorf("%s should be static", v)
		}
	}

	for _, v := range []string{"/api/users/{}", "/api/v1/{}/orders"} {
		if e, ok := find(eps, v); !ok || !e.Dynamic {
			t.Errorf("want dynamic endpoint %s, got %+v (found=%v)", v, e, ok)
		}
	}

	for _, v := range []string{"/api/commented-out", "/api/v1/dead", "/not-a-request", "/users/:id", "/api/fake"} {
		if _, ok := find(eps, v); ok {
			t.Errorf("%s should not be reported", v)
		}
	}
}

func TestEndpointsDedupPrefersCall(t *testing.T) {
	eps := Endpoints([]byte(`const A = "/api/x"; fetch("/api/x");`))
	if len(eps) != 1 || eps[0].Source != SourceCall || eps[0].Callee != "fetch" {
		t.Errorf("got %+v", eps)
	}
}

func TestUnterminatedStringDoesNotSwallowTheFile(t *testing.T) {
	eps := Endpoints([]byte("var s = 'oops\nfetch('/api/still-found')"))
	if _, ok := find(eps, "/api/still-found"); !ok {
		t.Errorf("got %+v", eps)
	}
}

func FuzzEndpoints(f *testing.F) {
	for _, s := range []string{
		"fetch('/a')", "`${`${x}`}`", "/[/]/", "'\\", "/*", "a/b/c", "\"\\u{", "x=`${",
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		_ = Endpoints([]byte(s)) // must not panic or hang
	})
}
