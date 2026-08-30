package templates

import (
	"encoding/xml"
	"strings"
	"time"

	"itchgrep/pkg/models"
	"itchgrep/pkg/money"
)

// BaseURL is the site's public origin. Atom ids/self links, sitemap
// locations and the OpenSearch descriptor's URLs must all be absolute - none
// of them are read by something that has "relative to wherever this was
// fetched from" the way a browser does - so none of them can be derived from
// the incoming request the way every other link in the templates package is.
//
// A plain const rather than something read from the environment: nothing else
// in this codebase parameterises the public origin (see internal/config,
// which only holds settings the two binaries must agree on), and a
// self-referential feed id, sitemap entry or search descriptor is wrong
// precisely when this drifts from where the site is actually reachable,
// which argues for it being visible in source rather than configurable per
// deployment.
const BaseURL = "https://itchgrep.com"

// atomFeed is the document encoding/xml renders. Typed structs rather than a
// hand-built string: a feed reader is somebody else's XML parser, and the
// surest way to keep it parseable is to never hand-assemble angle brackets.
type atomFeed struct {
	XMLName xml.Name    `xml:"http://www.w3.org/2005/Atom feed"`
	ID      string      `xml:"id"`
	Title   string      `xml:"title"`
	Updated string      `xml:"updated"`
	Links   []atomLink  `xml:"link"`
	Entries []atomEntry `xml:"entry"`
}

type atomLink struct {
	Rel  string `xml:"rel,attr,omitempty"`
	Type string `xml:"type,attr,omitempty"`
	Href string `xml:"href,attr"`
}

type atomAuthor struct {
	Name string `xml:"name"`
}

type atomEntry struct {
	ID      string      `xml:"id"`
	Title   string      `xml:"title"`
	Author  atomAuthor  `xml:"author"`
	Updated string      `xml:"updated"`
	Link    atomLink    `xml:"link"`
	Summary atomSummary `xml:"summary"`
}

type atomSummary struct {
	Type string `xml:"type,attr"`
	Text string `xml:",chardata"`
}

// NewFeed builds the Atom document for one filtered search. assets is
// expected to already be the bounded, newest-first, FirstSeen-stamped set
// HandleFeed asked the cache for - this only shapes it into XML, it does not
// re-filter or re-sort. likeTitle is the ?like= source asset's title, the
// same way cache.Results.LikeTitle threads it to the results page - empty
// when f.Like is unset or matched nothing.
func NewFeed(f Filters, assets []models.Asset, rates money.Rates, newestCrawl time.Time, likeTitle string) atomFeed {
	feed := atomFeed{
		ID:    BaseURL + f.FeedURL(),
		Title: feedTitle(f, likeTitle),
		Links: []atomLink{
			// self: what a reader re-fetches on its own schedule. alternate:
			// the human-readable page the same filters describe, so a client
			// that shows "view on site" has somewhere to send a click.
			{Rel: "self", Type: "application/atom+xml", Href: BaseURL + f.FeedURL()},
			{Rel: "alternate", Type: "text/html", Href: BaseURL + f.ShareURL()},
		},
	}

	// updated is the newest entry's FirstSeen, since that is the most recent
	// thing this document is claiming happened. An empty result set has no
	// entry to take it from, but is still a valid feed - it falls back to
	// when the underlying data was last published, which is the newest true
	// statement available.
	updated := newestCrawl
	if len(assets) > 0 && !assets[0].FirstSeen.IsZero() {
		updated = assets[0].FirstSeen
	}
	feed.Updated = formatAtomTime(updated)

	for _, a := range assets {
		feed.Entries = append(feed.Entries, atomEntry{
			// The itch.io page, not a synthetic itchgrep URL: it is the one
			// stable, dereferenceable identifier this site already has for
			// the asset, and it is also where "read more" should send you.
			ID:      a.Link,
			Title:   a.Title,
			Author:  atomAuthor{Name: a.Author},
			Updated: formatAtomTime(a.FirstSeen),
			Link:    atomLink{Rel: "alternate", Href: a.Link},
			Summary: atomSummary{Type: "text", Text: feedSummary(f, a, rates)},
		})
	}
	return feed
}

// Render serialises the feed as a complete Atom document, including the XML
// declaration a feed reader expects at the top of the response body.
func (feed atomFeed) Render() ([]byte, error) {
	body, err := xml.MarshalIndent(feed, "", "  ")
	if err != nil {
		return nil, err
	}
	return append([]byte(xml.Header), body...), nil
}

// formatAtomTime renders a timestamp as RFC 3339, the profile Atom's
// xsd:dateTime requires. UTC rather than local time, so the document does not
// depend on the server's timezone to be read correctly.
func formatAtomTime(t time.Time) string {
	return t.UTC().Format(time.RFC3339)
}

// feedTitle describes the filters a feed was built from, so a subscriber with
// several itchgrep feeds in a reader can tell them apart without opening one.
func feedTitle(f Filters, likeTitle string) string {
	var parts []string
	if f.Query != "" {
		parts = append(parts, `"`+f.Query+`"`)
	}
	if len(f.Tags) > 0 {
		parts = append(parts, strings.Join(f.Tags, " + "))
	}
	if f.Like != "" {
		if likeTitle != "" {
			parts = append(parts, "similar to "+likeTitle)
		} else {
			// An id that matched nothing - a stale or hand-edited link -
			// still needs a title; naming the id keeps it distinguishable
			// without claiming a source asset this feed could not find.
			parts = append(parts, "similar to game "+f.Like)
		}
	}
	if f.Author != "" {
		parts = append(parts, "by "+f.Author)
	}
	if f.Price != "" {
		parts = append(parts, priceFilterLabel(f.Price))
	}
	if len(f.NotTags) > 0 {
		parts = append(parts, "without "+strings.Join(f.NotTags, " + "))
	}
	if len(parts) == 0 {
		return "ITCHGREP — new assets"
	}
	return "ITCHGREP — " + strings.Join(parts, ", ")
}

// feedSummary is an entry's body: the description, with the price appended so
// a reader shows it without a click through to itch.io. Description first,
// since it is the more interesting sentence and readers tend to truncate long
// summaries from the end.
func feedSummary(f Filters, a models.Asset, rates money.Rates) string {
	price := priceLabel(f, a, rates)
	if a.Description == "" {
		return price
	}
	return a.Description + " — " + price
}
