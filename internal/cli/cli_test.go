package cli

import (
	"bytes"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
)

func TestParseArgsAcceptsFlagsAfterPositionals(t *testing.T) {
	cases := []struct {
		args    []string
		wantPos []string
	}{
		{[]string{"latest", "--orphans"}, []string{"latest"}},
		{[]string{"--orphans", "latest"}, []string{"latest"}},
		{[]string{"a", "--orphans", "b"}, []string{"a", "b"}},
	}
	for _, c := range cases {
		fs := flag.NewFlagSet("t", flag.ContinueOnError)
		orphans := fs.Bool("orphans", false, "")
		pos, err := parseArgs(fs, c.args)
		if err != nil {
			t.Fatalf("%v: %v", c.args, err)
		}
		if !*orphans {
			t.Errorf("%v: --orphans was dropped", c.args)
		}
		if !reflect.DeepEqual(pos, c.wantPos) {
			t.Errorf("%v: positionals = %v, want %v", c.args, pos, c.wantPos)
		}
	}
}

func TestParseArgsRejectsUnknownFlag(t *testing.T) {
	fs := flag.NewFlagSet("t", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	if _, err := parseArgs(fs, []string{"latest", "--nope"}); err == nil {
		t.Error("unknown flag after a positional should be an error, not ignored")
	}
}

func TestParseHeaders(t *testing.T) {
	h, err := parseHeaders([]string{"Cookie: a=b; c=d", "X-Test:1"})
	if err != nil {
		t.Fatal(err)
	}
	if h["Cookie"] != "a=b; c=d" || h["X-Test"] != "1" {
		t.Errorf("got %v", h)
	}
	if _, err := parseHeaders([]string{"no colon"}); err == nil {
		t.Error("header without a colon should be rejected")
	}
}

func TestShortAndTruncate(t *testing.T) {
	if short("") != "(none)" {
		t.Error("empty value should render as (none)")
	}
	long := strings.Repeat("x", 80)
	if got := short(long); len(got) != 60 || !strings.HasSuffix(got, "...") {
		t.Errorf("short(80 chars) = %d chars", len(got))
	}
	if truncate("abc", 5) != "abc" || truncate("abcdefgh", 6) != "abc..." {
		t.Error("truncate")
	}
}

// capture runs Main with stdout and stderr redirected and returns both.
func capture(t *testing.T, args ...string) (stdout, stderr string, code int) {
	t.Helper()
	oldOut, oldErr := os.Stdout, os.Stderr
	ro, wo, _ := os.Pipe()
	re, we, _ := os.Pipe()
	os.Stdout, os.Stderr = wo, we

	var bo, be bytes.Buffer
	done := make(chan struct{}, 2)
	go func() { io.Copy(&bo, ro); done <- struct{}{} }()
	go func() { io.Copy(&be, re); done <- struct{}{} }()

	code = Main(append([]string{"recongraph"}, args...))

	wo.Close()
	we.Close()
	<-done
	<-done
	os.Stdout, os.Stderr = oldOut, oldErr
	return bo.String(), be.String(), code
}

// twoVersionSite serves one of two versions of a small site; flip the returned
// switch to move from the first to the second.
func twoVersionSite(t *testing.T) (*httptest.Server, *atomic.Bool) {
	t.Helper()
	var v2 atomic.Bool
	mux := http.NewServeMux()
	page := func(title, body string) string {
		return fmt.Sprintf(`<html><head><title>%s</title></head><body>%s</body></html>`, title, body)
	}
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		links := `<a href="/about">about</a> <a href="/old">old</a>`
		if v2.Load() {
			links = `<a href="/about">about</a> <a href="/pricing">pricing</a>`
		}
		fmt.Fprint(w, page("Home", links+`<script src="/app.js"></script>`))
	})
	mux.HandleFunc("/about", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, page("About", `<a href="/">home</a>`))
	})
	mux.HandleFunc("/old", func(w http.ResponseWriter, r *http.Request) {
		if v2.Load() {
			http.NotFound(w, r)
			return
		}
		fmt.Fprint(w, page("Old", ""))
	})
	mux.HandleFunc("/pricing", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, page("Pricing", ""))
	})
	mux.HandleFunc("/app.js", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/javascript")
		fmt.Fprint(w, `fetch("/api/internal/stats")`)
	})
	mux.HandleFunc("/sitemap.xml", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `<?xml version="1.0"?><urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9"><url><loc>http://%s/landing</loc></url></urlset>`, r.Host)
	})
	mux.HandleFunc("/landing", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, page("Landing", ""))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, &v2
}

func TestCrawlQueryDiffExport(t *testing.T) {
	srv, v2 := twoVersionSite(t)
	store := t.TempDir()
	crawlArgs := []string{"crawl", "-u", srv.URL + "/", "--store", store,
		"--rate", "1000", "--burst", "64", "-q"}

	out, errOut, code := capture(t, crawlArgs...)
	if code != 0 {
		t.Fatalf("first crawl exited %d: %s", code, errOut)
	}
	if !strings.Contains(out, srv.URL+"/about") {
		t.Errorf("urls output missing /about:\n%s", out)
	}

	v2.Store(true)
	if _, errOut, code := capture(t, crawlArgs...); code != 0 {
		t.Fatalf("second crawl exited %d: %s", code, errOut)
	}

	t.Run("list", func(t *testing.T) {
		out, _, _ := capture(t, "list", "--store", store)
		if n := strings.Count(out, "complete"); n != 2 {
			t.Errorf("want 2 stored crawls, got %d:\n%s", n, out)
		}
	})

	t.Run("orphans include sitemap-only pages", func(t *testing.T) {
		out, _, _ := capture(t, "query", "latest", "--orphans", "--store", store)
		for _, want := range []string{"/landing", "/api/internal/stats"} {
			if !strings.Contains(out, want) {
				t.Errorf("orphans missing %s:\n%s", want, out)
			}
		}
		if strings.Contains(out, srv.URL+"/\n") {
			t.Errorf("the seed should never be an orphan:\n%s", out)
		}
	})

	t.Run("path-to", func(t *testing.T) {
		out, _, _ := capture(t, "query", "latest", "--path-to", "/api/internal/stats", "--store", store)
		if !strings.Contains(out, "2 hop(s)") || !strings.Contains(out, "/app.js") {
			t.Errorf("unexpected path:\n%s", out)
		}
	})

	t.Run("diff", func(t *testing.T) {
		out, errOut, code := capture(t, "diff", "latest~1", "latest", "--store", store)
		if code != 0 {
			t.Fatalf("diff exited %d: %s", code, errOut)
		}
		for _, want := range []string{"+ " + srv.URL + "/pricing", "/old", "RESTRUCTURED"} {
			if !strings.Contains(out, want) {
				t.Errorf("diff missing %q:\n%s", want, out)
			}
		}
	})

	t.Run("diff same crawl", func(t *testing.T) {
		_, errOut, code := capture(t, "diff", "latest", "latest", "--store", store)
		if code == 0 || !strings.Contains(errOut, "same crawl") {
			t.Errorf("diffing a crawl against itself should fail clearly, got %d %q", code, errOut)
		}
	})

	t.Run("latest~N past the end", func(t *testing.T) {
		_, errOut, code := capture(t, "query", "latest~5", "--store", store)
		if code == 0 || !strings.Contains(errOut, "only 2 stored crawls") {
			t.Errorf("got %d %q", code, errOut)
		}
	})

	t.Run("export", func(t *testing.T) {
		dir := t.TempDir()
		for _, f := range []string{"json", "dot", "html", "adjacency", "urls"} {
			path := filepath.Join(dir, "out."+f)
			if _, errOut, code := capture(t, "export", "latest", "-f", f, "-o", path, "--store", store); code != 0 {
				t.Errorf("export -f %s exited %d: %s", f, code, errOut)
				continue
			}
			if fi, err := os.Stat(path); err != nil || fi.Size() == 0 {
				t.Errorf("export -f %s wrote nothing", f)
			}
		}
	})
}

func TestUnknownCommand(t *testing.T) {
	_, errOut, code := capture(t, "crawll")
	if code != 2 || !strings.Contains(errOut, "unknown command") {
		t.Errorf("got %d %q", code, errOut)
	}
}

func TestCrawlOfDeadHostFails(t *testing.T) {
	store := t.TempDir()
	// Port 1 on loopback refuses connections.
	_, errOut, code := capture(t, "crawl", "-u", "http://127.0.0.1:1/", "--store", store, "-q", "--no-sitemap")
	if code == 0 || !strings.Contains(errOut, "nothing could be fetched") {
		t.Errorf("got exit %d, stderr %q", code, errOut)
	}
	if out, _, _ := capture(t, "list", "--store", store); !strings.Contains(out, "No stored crawls") {
		t.Errorf("a failed crawl was saved:\n%s", out)
	}
}
