package passive

import (
	"strings"
	"testing"
)

func values(rs []Result, kind string) []string {
	var out []string
	for _, r := range rs {
		if r.Kind == kind {
			out = append(out, r.Value)
		}
	}
	return out
}

func has(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

func TestExtractEmails(t *testing.T) {
	got := values(Extract([]byte(`
		Contact security@example.com or ops.team+alerts@sub.example.co.uk.
		Placeholder: user@example.com should be filtered as library noise.
	`), false), KindEmail)

	for _, want := range []string{"security@example.com", "ops.team+alerts@sub.example.co.uk"} {
		if !has(got, want) {
			t.Errorf("missing %s in %v", want, got)
		}
	}
	if has(got, "user@example.com") {
		t.Errorf("placeholder email should be filtered: %v", got)
	}
}

func TestExtractBuckets(t *testing.T) {
	body := []byte(`
		https://my-assets.s3.amazonaws.com/x.png
		https://other-bucket.s3-eu-west-1.amazonaws.com/y.png
		https://s3.amazonaws.com/path-style-bucket/z.png
		https://storage.googleapis.com/gcs-bucket/a.png
		https://myaccount.blob.core.windows.net/c/d.png
		https://spaces-bucket.nyc3.digitaloceanspaces.com/e.png
	`)
	got := values(Extract(body, false), KindBucket)
	for _, want := range []string{
		"my-assets", "other-bucket", "path-style-bucket",
		"gcs-bucket", "myaccount", "spaces-bucket",
	} {
		if !has(got, want) {
			t.Errorf("missing bucket %q in %v", want, got)
		}
	}
}

func TestExtractSecrets(t *testing.T) {
	// Built from pieces so this file doesn't set off secret scanners itself.
	j := func(parts ...string) string { return strings.Join(parts, "") }
	body := []byte(strings.Join([]string{
		`const a = "AKIAIOSFODNN7EXAMPLE";`,
		`const g = "` + j("AI", "za", "SyD-1234567890abcdefghijklmnopqrstu") + `";`,
		`const t = "` + j("gh", "p_", "1234567890abcdefghijklmnopqrstuvwxyzAB") + `";`,
		`const s = "` + j("xo", "xb-", "123456789012-abcdefghijklmnop") + `";`,
		`const k = "` + j("-----BEGIN RSA ", "PRIVATE KEY-----") + `";`,
	}, "\n"))
	got := values(Extract(body, true), KindSecret)
	if len(got) < 5 {
		t.Errorf("expected 5 secret-shaped strings, got %d: %v", len(got), got)
	}
}

// Bundled libraries credit their authors in comments. On a crawl of
// books.toscrape.com one datepicker file produced 31 emails and a dozen
// GitHub profile links this way, and "h.test(" in minified code came out
// as a hostname.
func TestScriptCommentsAndCodeAreNotFindings(t *testing.T) {
	js := []byte(`/*! datepicker | (c) Jane Doe <jane@example.org>, https://github.com/janedoe */
		// Japanese translation by @suzuki https://github.com/suzuki
		var h=/x/;if(h.test(v))n.push(1);
		var support = "help@shop.example.com";`)
	got := Extract(js, true)
	if emails := values(got, KindEmail); len(emails) != 1 || emails[0] != "help@shop.example.com" {
		t.Errorf("emails = %v, want only the one in a string literal", emails)
	}
	if s := values(got, KindSocial); len(s) != 0 {
		t.Errorf("social links from comments: %v", s)
	}
	if h := values(got, KindInternal); len(h) != 0 {
		t.Errorf("internal hosts from code: %v", h)
	}
}

func TestExtractCSSSkipsComments(t *testing.T) {
	css := []byte(`/* Font Awesome by Dave Gandy - http://twitter.com/davegandy */
		.x{background:url(https://assets.s3.amazonaws.com/a.png)}`)
	got := ExtractCSS(css)
	if s := values(got, KindSocial); len(s) != 0 {
		t.Errorf("social link from a comment: %v", s)
	}
	if b := values(got, KindBucket); len(b) != 1 {
		t.Errorf("buckets = %v, want the one in the rule", b)
	}
}

// A noisy secret scanner gets ignored, and the real finding with it.
func TestNoSecretFalsePositives(t *testing.T) {
	body := []byte(`
		const hash = "d41d8cd98f00b204e9800998ecf8427e";
		const nonce = "aGVsbG8gd29ybGQgdGhpcyBpcyBiYXNlNjQ=";
		const id = "550e8400-e29b-41d4-a716-446655440000";
		const cls = "AKIAbutTooShort";
	`)
	if got := values(Extract(body, true), KindSecret); len(got) != 0 {
		t.Errorf("false positives: %v", got)
	}
}

func TestExtractAPIEndpoints(t *testing.T) {
	js := []byte(`
		fetch("/api/v1/users");
		axios.get("/rest/orders");
		$.post("/graphql");
		const x = "/wp-json/wp/v2/posts";
	`)
	got := values(Extract(js, true), KindAPI)
	for _, want := range []string{"/api/v1/users", "/rest/orders", "/graphql", "/wp-json/wp/v2/posts"} {
		if !has(got, want) {
			t.Errorf("missing endpoint %q in %v", want, got)
		}
	}
}

func TestExtractInternalHosts(t *testing.T) {
	body := []byte(`
		<!-- deploys to admin.internal.acme.io, db at 10.0.4.12 -->
		staging.corp.example runs the preview build
		192.168.1.50 is the office printer
	`)
	got := values(Extract(body, false), KindInternal)
	for _, want := range []string{"10.0.4.12", "192.168.1.50"} {
		if !has(got, want) {
			t.Errorf("missing private IP %q in %v", want, got)
		}
	}
	joined := strings.Join(got, " ")
	if !strings.Contains(joined, "internal") && !strings.Contains(joined, "corp") {
		t.Errorf("no internal hostname extracted: %v", got)
	}
}

func TestExtractSocials(t *testing.T) {
	got := values(Extract([]byte(`
		<a href="https://twitter.com/acmecorp">x</a>
		<a href="https://www.linkedin.com/company/acme">in</a>
		<a href="https://github.com/acme/repo">gh</a>
	`), false), KindSocial)
	if len(got) != 3 {
		t.Errorf("socials = %v, want 3", got)
	}
}

func TestExtractDeduplicates(t *testing.T) {
	body := []byte(strings.Repeat("contact a@b.com today. ", 20))
	got := values(Extract(body, false), KindEmail)
	if len(got) != 1 {
		t.Errorf("same email extracted %d times, want 1: %v", len(got), got)
	}
}

func TestJSEndpointsSkipsUnfetchable(t *testing.T) {
	js := []byte("" +
		"fetch(\"/api/v1/real\");\n" +
		"fetch(`/api/v1/${userId}/dynamic`);\n" +
		"fetch(\"/api/\" + path);\n" +
		"const u = \"https://api.example.com/v2/items\";\n")
	got := JSEndpoints(js)

	if !has(got, "/api/v1/real") {
		t.Errorf("static endpoint missing: %v", got)
	}
	if !has(got, "https://api.example.com/v2/items") {
		t.Errorf("absolute URL literal missing: %v", got)
	}
	for _, g := range got {
		if strings.ContainsAny(g, "${}") || strings.Contains(g, "+") {
			t.Errorf("template literal or concatenation should be skipped, got %q", g)
		}
	}
}

func TestExtractOnEmptyInput(t *testing.T) {
	if got := Extract(nil, false); len(got) != 0 {
		t.Errorf("nil body produced %v", got)
	}
	if got := ExtractFromComments(nil); got != nil {
		t.Errorf("nil comments produced %v", got)
	}
}
