package parse

import (
	"encoding/xml"
	"strings"
)

// Sitemaps list pages nothing on the site links to, which a link-only crawler
// never sees.

type sitemapIndex struct {
	XMLName  xml.Name `xml:"sitemapindex"`
	Sitemaps []struct {
		Loc string `xml:"loc"`
	} `xml:"sitemap"`
}

type urlSet struct {
	XMLName xml.Name `xml:"urlset"`
	URLs    []struct {
		Loc string `xml:"loc"`
	} `xml:"url"`
}

// Sitemap parses either a <urlset> or a <sitemapindex> document.
// pages holds page URLs; indexes holds nested sitemap URLs to fetch next.
func Sitemap(body []byte) (pages, indexes []string) {
	var idx sitemapIndex
	if err := xml.Unmarshal(body, &idx); err == nil && len(idx.Sitemaps) > 0 {
		for _, s := range idx.Sitemaps {
			if loc := strings.TrimSpace(s.Loc); loc != "" {
				indexes = append(indexes, loc)
			}
		}
		return nil, indexes
	}

	var set urlSet
	if err := xml.Unmarshal(body, &set); err == nil {
		for _, u := range set.URLs {
			if loc := strings.TrimSpace(u.Loc); loc != "" {
				pages = append(pages, loc)
			}
		}
	}
	return pages, nil
}

// IsXML reports whether a Content-Type names an XML document.
func IsXML(contentType string) bool {
	ct := strings.ToLower(contentType)
	return strings.Contains(ct, "xml")
}

// IsScript reports whether a Content-Type names JavaScript.
func IsScript(contentType string) bool {
	ct := strings.ToLower(contentType)
	return strings.Contains(ct, "javascript") || strings.Contains(ct, "ecmascript")
}
