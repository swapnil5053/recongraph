package sitegraph

import (
	"errors"
	"net/url"
	"testing"
)

func TestCanonicalize(t *testing.T) {
	opts := DefaultCanonOpts()
	cases := []struct {
		name string
		in   string
		want string
		err  error
	}{
		// scheme + host normalisation
		{"lowercase scheme", "HTTP://example.com/", "http://example.com/", nil},
		{"lowercase host", "https://EXAMPLE.COM/a", "https://example.com/a", nil},
		{"strip trailing dot host", "https://example.com./a", "https://example.com/a", nil},
		{"strip default port https", "https://example.com:443/a", "https://example.com/a", nil},
		{"strip default port http", "http://example.com:80/a", "http://example.com/a", nil},
		{"keep nondefault port", "https://example.com:8443/a", "https://example.com:8443/a", nil},

		// path normalisation
		{"empty path becomes slash", "https://example.com", "https://example.com/", nil},
		{"dot segments resolved", "https://example.com/a/b/../c", "https://example.com/a/c", nil},
		{"leading dot segments", "https://example.com/./a", "https://example.com/a", nil},
		{"trailing slash preserved", "https://example.com/a/", "https://example.com/a/", nil},
		{"trailing slash not invented", "https://example.com/a", "https://example.com/a", nil},
		{"path case preserved", "https://example.com/CaseSensitive", "https://example.com/CaseSensitive", nil},
		{"double slash collapsed", "https://example.com/a//b", "https://example.com/a/b", nil},

		// fragments
		{"fragment dropped", "https://example.com/a#section", "https://example.com/a", nil},
		{"fragment only path", "https://example.com/#top", "https://example.com/", nil},

		// query handling
		{"query preserved", "https://example.com/a?x=1", "https://example.com/a?x=1", nil},
		{"query sorted", "https://example.com/a?b=2&a=1", "https://example.com/a?a=1&b=2", nil},
		{"utm stripped", "https://example.com/a?utm_source=x&id=7", "https://example.com/a?id=7", nil},
		{"all params stripped leaves none", "https://example.com/a?fbclid=z", "https://example.com/a", nil},
		{"session id stripped case insensitive", "https://example.com/a?PHPSESSID=abc&p=1", "https://example.com/a?p=1", nil},
		{"empty value kept", "https://example.com/a?flag=", "https://example.com/a?flag=", nil},
		{"valueless param kept", "https://example.com/a?flag", "https://example.com/a?flag", nil},

		// rejections
		{"mailto rejected", "mailto:a@b.com", "", ErrNotHTTP},
		{"javascript rejected", "javascript:alert(1)", "", ErrNotHTTP},
		{"ftp rejected", "ftp://example.com/x", "", ErrNotHTTP},
		{"data uri rejected", "data:text/html,hi", "", ErrNotHTTP},
		{"no host rejected", "https:///path", "", ErrNoHost},
		{"empty rejected", "", "", ErrNoHost},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Canonicalize(tc.in, opts)
			if tc.err != nil {
				if !errors.Is(err, tc.err) {
					t.Fatalf("Canonicalize(%q) error = %v, want %v", tc.in, err, tc.err)
				}
				return
			}
			if err != nil {
				t.Fatalf("Canonicalize(%q) unexpected error: %v", tc.in, err)
			}
			if got != tc.want {
				t.Errorf("Canonicalize(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// Must be idempotent, or one URL found by two routes becomes two nodes.
func TestCanonicalizeIdempotent(t *testing.T) {
	opts := DefaultCanonOpts()
	inputs := []string{
		"https://Example.com:443/a/b/../c/?b=2&a=1&utm_source=x#frag",
		"http://example.com",
		"https://example.com/a//b/",
	}
	for _, in := range inputs {
		once, err := Canonicalize(in, opts)
		if err != nil {
			t.Fatalf("first pass %q: %v", in, err)
		}
		twice, err := Canonicalize(once, opts)
		if err != nil {
			t.Fatalf("second pass %q: %v", once, err)
		}
		if once != twice {
			t.Errorf("not idempotent: %q -> %q -> %q", in, once, twice)
		}
	}
}

func TestCanonicalizeNoQuery(t *testing.T) {
	opts := DefaultCanonOpts()
	opts.KeepQuery = false
	got, err := Canonicalize("https://example.com/search?q=a&page=2", opts)
	if err != nil {
		t.Fatal(err)
	}
	if want := "https://example.com/search"; got != want {
		t.Errorf("got %q want %q", got, want)
	}
}

func TestResolve(t *testing.T) {
	base, _ := url.Parse("https://example.com/dir/page.html")
	opts := DefaultCanonOpts()
	cases := []struct {
		ref  string
		want string
		err  error
	}{
		{"other.html", "https://example.com/dir/other.html", nil},
		{"/root.html", "https://example.com/root.html", nil},
		{"../up.html", "https://example.com/up.html", nil},
		{"//cdn.example.net/x.js", "https://cdn.example.net/x.js", nil},
		{"https://other.com/x", "https://other.com/x", nil},
		{"?q=1", "https://example.com/dir/page.html?q=1", nil},
		{"#frag", "", ErrNotHTTP},
		{"javascript:void(0)", "", ErrNotHTTP},
		{"JavaScript:void(0)", "", ErrNotHTTP},
		{"mailto:me@example.com", "", ErrNotHTTP},
		{"  spaced.html  ", "https://example.com/dir/spaced.html", nil},
		{"", "", ErrNoHost},
	}
	for _, tc := range cases {
		got, err := Resolve(base, tc.ref, opts)
		if tc.err != nil {
			if !errors.Is(err, tc.err) {
				t.Errorf("Resolve(%q) err = %v, want %v", tc.ref, err, tc.err)
			}
			continue
		}
		if err != nil {
			t.Errorf("Resolve(%q) unexpected error %v", tc.ref, err)
			continue
		}
		if got != tc.want {
			t.Errorf("Resolve(%q) = %q, want %q", tc.ref, got, tc.want)
		}
	}
}

// Regression test for the scope-escape described in docs/AUDIT-hakrawler.md:
// an unanchored regex over the URL string matches "example.com.attacker.net".
func TestSameOrSubdomainRejectsScopeEscape(t *testing.T) {
	cases := []struct {
		host, target string
		want         bool
	}{
		{"example.com", "example.com", true},
		{"www.example.com", "example.com", true},
		{"a.b.example.com", "example.com", true},
		{"example.com:8080", "example.com", true},

		// The escapes.
		{"example.com.attacker.net", "example.com", false},
		{"notexample.com", "example.com", false},
		{"example.community", "example.com", false},
		{"attacker.net", "example.com", false},
		{"fakeexample.com", "example.com", false},
		{"example.com.evil", "example.com", false},
	}
	for _, tc := range cases {
		if got := SameOrSubdomain(tc.host, tc.target); got != tc.want {
			t.Errorf("SameOrSubdomain(%q, %q) = %v, want %v", tc.host, tc.target, got, tc.want)
		}
	}
}

func TestRootDomain(t *testing.T) {
	cases := map[string]string{
		"example.com":       "example.com",
		"www.example.com":   "example.com",
		"a.b.c.example.com": "example.com",
		"localhost":         "localhost",
		"EXAMPLE.COM":       "example.com",
		"example.com:8080":  "example.com",
	}
	for in, want := range cases {
		if got := RootDomain(in); got != want {
			t.Errorf("RootDomain(%q) = %q, want %q", in, got, want)
		}
	}
}
