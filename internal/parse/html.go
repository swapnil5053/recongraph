// Package parse extracts references and metadata from HTML.
//
// Takes bytes and a base URL, returns candidates. No I/O, so it tests against
// fixtures without a network.
package parse

import (
	"bytes"
	"net/url"
	"strings"

	"golang.org/x/net/html"

	"github.com/swapnil5053/recongraph/pkg/sitegraph"
)

// Candidate is a reference found on a page, before scope or canonicalisation.
type Candidate struct {
	Ref     string
	Rel     sitegraph.EdgeRel
	Kind    sitegraph.NodeKind
	Context string
}

// Form is an HTML form discovered on a page.
type Form struct {
	Action string
	Method string
	Inputs []string
}

// Page is everything one HTML document yielded.
type Page struct {
	Title         string
	Base          *url.URL
	Candidates    []Candidate
	Metas         map[string]string // lowercased name/property -> content
	Comments      []string
	Forms         []Form
	InlineScripts []string
	ScriptSrcs    []string
	CSSClasses    []string
}

// HTML parses a document. base is used only to honour a <base href> override;
// resolution of relative references is the caller's job.
func HTML(body []byte, base *url.URL) (*Page, error) {
	doc, err := html.Parse(bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	p := &Page{Base: base, Metas: map[string]string{}}
	p.walk(doc)
	return p, nil
}

func attr(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if strings.EqualFold(a.Key, key) {
			return strings.TrimSpace(a.Val)
		}
	}
	return ""
}

func hasAttr(n *html.Node, key string) bool {
	for _, a := range n.Attr {
		if strings.EqualFold(a.Key, key) {
			return true
		}
	}
	return false
}

func (p *Page) add(ref string, rel sitegraph.EdgeRel, kind sitegraph.NodeKind, ctx string) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return
	}
	p.Candidates = append(p.Candidates, Candidate{Ref: ref, Rel: rel, Kind: kind, Context: ctx})
}

func (p *Page) walk(n *html.Node) {
	switch n.Type {
	case html.CommentNode:
		// Comments leak staging hosts and dead admin paths often enough to be
		// worth keeping.
		text := strings.TrimSpace(n.Data)
		if text != "" {
			p.Comments = append(p.Comments, text)
		}
	case html.ElementNode:
		p.element(n)
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		p.walk(c)
	}
}

func (p *Page) element(n *html.Node) {
	switch strings.ToLower(n.Data) {
	case "base":
		if href := attr(n, "href"); href != "" && p.Base != nil {
			if u, err := p.Base.Parse(href); err == nil {
				p.Base = u
			}
		}

	case "title":
		if p.Title == "" {
			// Collapse whitespace, or reformatting a template shows up as a
			// title change in every diff.
			p.Title = strings.Join(strings.Fields(textOf(n)), " ")
		}

	case "a":
		p.add(attr(n, "href"), sitegraph.RelHref, sitegraph.KindPage, truncate(textOf(n), 60))
	case "area":
		p.add(attr(n, "href"), sitegraph.RelHref, sitegraph.KindPage, "area")

	case "link":
		href := attr(n, "href")
		if href == "" {
			return
		}
		rel := strings.ToLower(attr(n, "rel"))
		switch {
		case strings.Contains(rel, "stylesheet"):
			p.add(href, sitegraph.RelStylesheet, sitegraph.KindStylesheet, rel)
		case strings.Contains(rel, "icon"):
			p.add(href, sitegraph.RelImage, sitegraph.KindImage, rel)
		case strings.Contains(rel, "canonical"), strings.Contains(rel, "alternate"):
			p.add(href, sitegraph.RelHref, sitegraph.KindPage, rel)
		case strings.Contains(rel, "manifest"):
			p.add(href, sitegraph.RelHref, sitegraph.KindDocument, rel)
		default:
			// preload, prefetch, preconnect, dns-prefetch, ...
			p.add(href, sitegraph.RelStylesheet, sitegraph.KindOther, rel)
		}

	case "script":
		if src := attr(n, "src"); src != "" {
			p.ScriptSrcs = append(p.ScriptSrcs, src)
			p.add(src, sitegraph.RelScript, sitegraph.KindScript, attr(n, "type"))
		} else if body := textOf(n); strings.TrimSpace(body) != "" {
			p.InlineScripts = append(p.InlineScripts, body)
		}

	case "img":
		p.add(attr(n, "src"), sitegraph.RelImage, sitegraph.KindImage, "img")
		p.addSrcset(attr(n, "srcset"))
		if ds := attr(n, "data-src"); ds != "" {
			p.add(ds, sitegraph.RelImage, sitegraph.KindImage, "data-src")
		}
	case "source":
		p.add(attr(n, "src"), sitegraph.RelMedia, sitegraph.KindMedia, "source")
		p.addSrcset(attr(n, "srcset"))

	case "iframe", "frame":
		p.add(attr(n, "src"), sitegraph.RelIFrame, sitegraph.KindPage, "iframe")
	case "embed":
		p.add(attr(n, "src"), sitegraph.RelObject, sitegraph.KindOther, "embed")
	case "object":
		p.add(attr(n, "data"), sitegraph.RelObject, sitegraph.KindOther, "object")

	case "video", "audio":
		p.add(attr(n, "src"), sitegraph.RelMedia, sitegraph.KindMedia, n.Data)
		p.add(attr(n, "poster"), sitegraph.RelImage, sitegraph.KindImage, "poster")
	case "track":
		p.add(attr(n, "src"), sitegraph.RelMedia, sitegraph.KindMedia, "track")

	case "form":
		f := Form{Action: attr(n, "action"), Method: strings.ToUpper(attr(n, "method"))}
		if f.Method == "" {
			f.Method = "GET"
		}
		f.Inputs = formInputs(n)
		p.Forms = append(p.Forms, f)
		if f.Action != "" {
			p.add(f.Action, sitegraph.RelFormAction, sitegraph.KindForm, f.Method)
		}

	case "meta":
		name := strings.ToLower(attr(n, "name"))
		if name == "" {
			name = strings.ToLower(attr(n, "property"))
		}
		content := attr(n, "content")
		if name != "" && content != "" {
			p.Metas[name] = content
		}
		if strings.EqualFold(attr(n, "http-equiv"), "refresh") {
			if u := refreshURL(content); u != "" {
				p.add(u, sitegraph.RelRedirect, sitegraph.KindPage, "meta-refresh")
			}
		}
	}

	// Weak fingerprint signal, but wp- and elementor- prefixes are worth it.
	if c := attr(n, "class"); c != "" && len(p.CSSClasses) < 400 {
		p.CSSClasses = append(p.CSSClasses, c)
	}
}

func (p *Page) addSrcset(v string) {
	if v == "" {
		return
	}
	for _, part := range strings.Split(v, ",") {
		fields := strings.Fields(strings.TrimSpace(part))
		if len(fields) > 0 {
			p.add(fields[0], sitegraph.RelImage, sitegraph.KindImage, "srcset")
		}
	}
}

func formInputs(n *html.Node) []string {
	var names []string
	var walk func(*html.Node)
	walk = func(x *html.Node) {
		if x.Type == html.ElementNode {
			switch strings.ToLower(x.Data) {
			case "input", "select", "textarea":
				if name := attr(x, "name"); name != "" {
					t := attr(x, "type")
					if t != "" {
						name += ":" + t
					}
					names = append(names, name)
				}
			}
		}
		for c := x.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return names
}

// refreshURL pulls the target out of `0;url=/next` style meta-refresh content.
func refreshURL(content string) string {
	lower := strings.ToLower(content)
	i := strings.Index(lower, "url=")
	if i < 0 {
		return ""
	}
	v := strings.TrimSpace(content[i+4:])
	v = strings.Trim(v, `'"`)
	return v
}

func textOf(n *html.Node) string {
	var b strings.Builder
	var walk func(*html.Node)
	walk = func(x *html.Node) {
		if x.Type == html.TextNode {
			b.WriteString(x.Data)
		}
		for c := x.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return b.String()
}

func truncate(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// IsHTML reports whether a Content-Type header names an HTML document.
func IsHTML(contentType string) bool {
	ct := strings.ToLower(contentType)
	return strings.Contains(ct, "text/html") || strings.Contains(ct, "application/xhtml")
}

// KindForContentType upgrades a node's kind once we've fetched it.
func KindForContentType(ct string, fallback sitegraph.NodeKind) sitegraph.NodeKind {
	ct = strings.ToLower(ct)
	switch {
	case strings.Contains(ct, "html"):
		return sitegraph.KindPage
	case strings.Contains(ct, "javascript"), strings.Contains(ct, "ecmascript"):
		return sitegraph.KindScript
	case strings.Contains(ct, "css"):
		return sitegraph.KindStylesheet
	case strings.Contains(ct, "image/"):
		return sitegraph.KindImage
	case strings.Contains(ct, "video/"), strings.Contains(ct, "audio/"):
		return sitegraph.KindMedia
	case strings.Contains(ct, "json"), strings.Contains(ct, "xml"):
		return sitegraph.KindAPI
	case strings.Contains(ct, "pdf"), strings.Contains(ct, "msword"),
		strings.Contains(ct, "officedocument"), strings.Contains(ct, "text/plain"):
		return sitegraph.KindDocument
	}
	return fallback
}
