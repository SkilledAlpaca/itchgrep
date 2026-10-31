package templates

import "encoding/xml"

// urlset is the document encoding/xml renders for /sitemap.xml. Typed
// structs rather than a hand-built string, for the same reason atomFeed is
// (see feed.go): the reader is somebody else's XML parser.
type urlset struct {
	XMLName xml.Name     `xml:"http://www.sitemaps.org/schemas/sitemap/0.9 urlset"`
	URLs    []sitemapURL `xml:"url"`
}

type sitemapURL struct {
	Loc string `xml:"loc"`
}

// sitemapPages are the site's fixed, non-tag destinations. /new is here
// because it is a genuine page; /results, /feed and every filtered /?... URL
// are not, because robots.txt disallows query strings and this must not
// advertise paths it tells crawlers to skip.
var sitemapPages = []string{"/", "/about", "/stats", "/new"}

// NewSitemap builds the sitemap document: the fixed pages above, plus one
// /tag/{slug} entry per slug in tags. tags is expected to already be the
// cache's bounded tag universe (cache.TagSlugs) - this only shapes it into
// XML, it does not itself impose the cap.
func NewSitemap(tags []string) urlset {
	doc := urlset{URLs: make([]sitemapURL, 0, len(sitemapPages)+len(tags))}
	for _, p := range sitemapPages {
		doc.URLs = append(doc.URLs, sitemapURL{Loc: BaseURL + p})
	}
	for _, slug := range tags {
		doc.URLs = append(doc.URLs, sitemapURL{Loc: BaseURL + "/tag/" + slug})
	}
	return doc
}

// Render serializes the sitemap as a complete XML document, including the
// declaration a consumer expects at the top of the response body.
func (doc urlset) Render() ([]byte, error) {
	body, err := xml.MarshalIndent(doc, "", "  ")
	if err != nil {
		return nil, err
	}
	return append([]byte(xml.Header), body...), nil
}
