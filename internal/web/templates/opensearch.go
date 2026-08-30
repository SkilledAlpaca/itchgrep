package templates

import "encoding/xml"

// openSearchDescription is the document encoding/xml renders for
// /opensearch.xml. Typed structs for the same reason atomFeed and urlset are
// (see feed.go, sitemap.go): the reader is somebody else's XML parser.
type openSearchDescription struct {
	XMLName     xml.Name        `xml:"http://a9.com/-/spec/opensearch/1.1/ OpenSearchDescription"`
	ShortName   string          `xml:"ShortName"`
	Description string          `xml:"Description"`
	URLs        []openSearchURL `xml:"Url"`
}

type openSearchURL struct {
	Type     string `xml:"type,attr"`
	Template string `xml:"template,attr"`
	Rel      string `xml:"rel,attr,omitempty"`
}

// NewOpenSearch builds the descriptor at /opensearch.xml. Unlike the feed and
// the sitemap this never varies with the request or the loaded dataset - it
// only tells a browser how to build a search URL for this site - so it takes
// no arguments.
//
// The template's q param has to match the search form's actual field name
// (index.templ's SearchBar, name="q"), or a browser offering this site as a
// search engine would submit a query nothing on the other end reads.
func NewOpenSearch() openSearchDescription {
	return openSearchDescription{
		ShortName:   "ITCHGREP",
		Description: "Full-text search over itch.io game assets, by title, author, description and tag.",
		URLs: []openSearchURL{
			{Type: "text/html", Template: BaseURL + "/?q={searchTerms}"},
			// rel="self": a client that already has this descriptor can confirm
			// it is looking at the canonical copy rather than a mirror of it.
			{Type: "application/opensearchdescription+xml", Rel: "self", Template: BaseURL + "/opensearch.xml"},
		},
	}
}

// Render serialises the descriptor as a complete XML document, including the
// declaration a consumer expects at the top of the response body.
func (doc openSearchDescription) Render() ([]byte, error) {
	body, err := xml.MarshalIndent(doc, "", "  ")
	if err != nil {
		return nil, err
	}
	return append([]byte(xml.Header), body...), nil
}
