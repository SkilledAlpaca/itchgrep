package models

import (
	"fmt"
	"strings"
)

// Asset represents a game asset.
// Assets are serialised to JSON and stored as a single object in the
// Google Cloud Storage bucket named by storage.BucketName.
type Asset struct {
	GameId      string
	Title       string
	Author      string
	Description string
	Link        string
	ThumbUrl    string
	// Tags an asset was found under, sorted. itch.io's listing JSON does not
	// carry tags, so these are inferred from which tag-filtered views the asset
	// appeared in during the crawl: free information, since the crawl already
	// visits many such views and would otherwise discard everything but the
	// first sighting. Necessarily a subset of the asset's real tags - only tags
	// the crawl actually visited can appear.
	Tags []string
	// Price as itch.io displays it, e.g. "$4.95" or "9.97€". Empty means the
	// asset costs nothing to download.
	//
	// Kept as the displayed string rather than a number because itch.io prices
	// in whichever currency the seller chose, and there is no exchange rate in
	// the listing to normalise them with. A number would therefore be either
	// wrong or unit-less; the string is at least exactly what a buyer sees.
	Price         string
	InvPopularity int64 // inverse popularity, derived from page number of the asset
}

// Free reports whether the asset can be had for nothing.
//
// itch.io's own /game-assets/free view is the reference, and it matches this
// exactly: across a sampled page of it, all 36 cells carried no price element.
// Eight of them did carry a price_tag element, but those are sale and bundle
// badges ("-35%", "In bundle") whose title reads "Pay $0 or more" - pay-what-
// you-want with a zero minimum, which itch.io counts as free. So the presence
// of a price element, not of a price tag, is what marks an asset as paid.
func (a Asset) Free() bool { return a.Price == "" }

// Pricing is the indexable form of Free: a keyword the search index can filter
// on. A bool would do as well in bleve, but a keyword keeps the query, the URL
// parameter and the facet all speaking the same two words.
func (a Asset) Pricing() string {
	if a.Free() {
		return PricingFree
	}
	return PricingPaid
}

// The two values Pricing returns, and the two the ?price= parameter accepts.
const (
	PricingFree = "free"
	PricingPaid = "paid"
)

// The orderings the ?sort= parameter accepts.
//
// There is no "newest" among them, and cannot be: itch.io's listing markup
// carries no publication date, so the crawl has nothing to record. Offering the
// option and quietly ordering by something else would be worse than not
// offering it.
//
// These live here, beside the pricing vocabulary, because three packages need
// to agree on them - the query layer that applies them, the handler that parses
// them, and the templates that render the control.
const (
	SortRelevance = "relevance" // best match first; meaningless without a query
	SortPopular   = "popular"   // itch.io's own popularity ordering
	SortTitle     = "title"     // A-Z
)

func (a Asset) String() string {
	return fmt.Sprintf("GameId: %s, Title: %s, Author: %s, Description: %s, Link: %s, ThumbUrl: %s, Price: %s, InvPopularity: %d", a.GameId, a.Title, a.Author, a.Description, a.Link, a.ThumbUrl, a.Price, a.InvPopularity)
}

// Tag is an itch.io asset facet and the number of assets carrying it.
// It lives here rather than in internal/fetcher because internal/storage
// caches the discovered tag universe and must not depend on the fetcher.
type Tag struct {
	Slug  string // e.g. "pixel-art", without the "tag-" prefix
	Count int64
}

// IndexedAsset is a smaller version of Asset, used for indexing.
//
// Three of its fields duplicate information already present in the others.
// That is deliberate: searching and filtering want the same data analysed two
// different ways, and bleve applies one analyser per field.
//
//   - Tags is analysed normally, so "pixel-art" is searchable as "pixel art".
//   - TagSlugs holds the identical values as untouched keywords, so a filter can
//     ask for exactly tag-2d and not also match every asset mentioning "2d".
//   - SortTitle is the lowercased title as a single keyword, so an A-Z sort
//     orders by the whole title rather than by its first analysed term.
type IndexedAsset struct {
	GameId        string
	Title         string
	Author        string
	Description   string
	Tags          []string
	TagSlugs      []string
	Pricing       string // PricingFree or PricingPaid
	SortTitle     string
	InvPopularity int64
}

// NewIndexedAsset derives the indexable form of an asset, including the
// duplicated fields described above.
func NewIndexedAsset(a Asset) IndexedAsset {
	return IndexedAsset{
		GameId:        a.GameId,
		Title:         a.Title,
		Author:        a.Author,
		Description:   a.Description,
		Tags:          a.Tags,
		TagSlugs:      a.Tags,
		Pricing:       a.Pricing(),
		SortTitle:     strings.ToLower(strings.TrimSpace(a.Title)),
		InvPopularity: a.InvPopularity,
	}
}

func (a IndexedAsset) String() string {
	return fmt.Sprintf("GameId: %s, Title: %s, Author: %s, Description: %s, Tags: %v, Pricing: %s, InvPopularity: %d", a.GameId, a.Title, a.Author, a.Description, a.Tags, a.Pricing, a.InvPopularity)
}
