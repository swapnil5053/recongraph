package scope

import "testing"

func TestCheckHostScope(t *testing.T) {
	r := DefaultRules("example.com")
	cases := []struct {
		url  string
		want Decision
	}{
		{"https://example.com/", Crawl},
		{"https://example.com/a/b", Crawl},
		{"https://www.example.com/", Record},          // subdomains off by default
		{"https://example.com.attacker.net/", Record}, // never crawled
		{"https://cdn.other.net/x.js", Record},
	}
	for _, tc := range cases {
		if got := r.Check(tc.url, 1); got != tc.want {
			t.Errorf("Check(%q) = %v, want %v", tc.url, got, tc.want)
		}
	}
}

func TestCheckSubdomains(t *testing.T) {
	r := DefaultRules("example.com")
	r.AllowSubdomains = true
	cases := []struct {
		url  string
		want Decision
	}{
		{"https://www.example.com/", Crawl},
		{"https://a.b.example.com/", Crawl},
		{"https://example.com/", Crawl},
		// Subdomains of someone else.
		{"https://example.com.attacker.net/", Record},
		{"https://notexample.com/", Record},
	}
	for _, tc := range cases {
		if got := r.Check(tc.url, 1); got != tc.want {
			t.Errorf("Check(%q) = %v, want %v", tc.url, got, tc.want)
		}
	}
}

func TestCheckDepth(t *testing.T) {
	r := DefaultRules("example.com")
	r.MaxDepth = 2
	if got := r.Check("https://example.com/a", 2); got != Crawl {
		t.Errorf("depth 2 with max 2 = %v, want Crawl", got)
	}
	if got := r.Check("https://example.com/a", 3); got != Record {
		t.Errorf("depth 3 with max 2 = %v, want Record", got)
	}
	r.MaxDepth = -1
	if got := r.Check("https://example.com/a", 99); got != Crawl {
		t.Errorf("unlimited depth = %v, want Crawl", got)
	}
}

func TestCheckExcludeWinsOverSeedHost(t *testing.T) {
	r := DefaultRules("example.com")
	pats, err := CompilePatterns([]string{`/logout`, `\.(png|jpg)$`})
	if err != nil {
		t.Fatal(err)
	}
	r.Exclude = pats
	if got := r.Check("https://example.com/logout", 1); got != Reject {
		t.Errorf("excluded URL = %v, want Reject", got)
	}
	if got := r.Check("https://example.com/img/a.png", 1); got != Reject {
		t.Errorf("excluded extension = %v, want Reject", got)
	}
	if got := r.Check("https://example.com/ok", 1); got != Crawl {
		t.Errorf("non-excluded = %v, want Crawl", got)
	}
}

func TestCheckIncludeFilter(t *testing.T) {
	r := DefaultRules("example.com")
	pats, _ := CompilePatterns([]string{`/api/`})
	r.Include = pats
	if got := r.Check("https://example.com/api/v1/users", 1); got != Crawl {
		t.Errorf("included = %v, want Crawl", got)
	}
	if got := r.Check("https://example.com/about", 1); got != Record {
		t.Errorf("not included = %v, want Record", got)
	}
}

func TestPathPrefixSegmentBoundary(t *testing.T) {
	r := DefaultRules("example.com")
	r.PathPrefix = "/admin"
	cases := []struct {
		url  string
		want Decision
	}{
		{"https://example.com/admin", Crawl},
		{"https://example.com/admin/", Crawl},
		{"https://example.com/admin/users", Crawl},
		// /administrator must not match /admin.
		{"https://example.com/administrator", Record},
		{"https://example.com/adminpanel", Record},
		{"https://example.com/other", Record},
	}
	for _, tc := range cases {
		if got := r.Check(tc.url, 1); got != tc.want {
			t.Errorf("Check(%q) = %v, want %v", tc.url, got, tc.want)
		}
	}
}

func TestSchemeRejected(t *testing.T) {
	r := DefaultRules("example.com")
	if got := r.Check("ftp://example.com/x", 1); got != Reject {
		t.Errorf("ftp = %v, want Reject", got)
	}
}

func TestRecordExternalOff(t *testing.T) {
	r := DefaultRules("example.com")
	r.RecordExternal = false
	if got := r.Check("https://other.net/x", 1); got != Reject {
		t.Errorf("external with RecordExternal=false = %v, want Reject", got)
	}
}
