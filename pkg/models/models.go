package models

import (
	"fmt"
	"strings"
	"time"
)

// Asset represents a game asset. The whole collection is serialised to one
// JSON file in the shared data directory; see internal/storage.
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
	Price string

	// PayWhatYouWant marks an asset itch.io labels "Pay $X or more".
	//
	// Orthogonal to Price rather than a third pricing state: a pay-what-you-want
	// asset may have a zero minimum (free, but tippable) or a non-zero one
	// ($4.95 or more). Read from the price_tag's title attribute, which is the
	// only place the listing says so.
	PayWhatYouWant bool

	InvPopularity int64 // inverse popularity, derived from page number of the asset

	// InvRecency is the page an asset appeared on in the root newest view, and
	// is therefore an ordering by recency in the same way InvPopularity is one
	// by popularity: smaller is newer.
	//
	// Zero means "not seen in that view", which is most of the catalogue - the
	// view stops at itch.io's 200-page ceiling, so only the newest ~7,200
	// assets can have one. Anything without a rank sorts after everything with
	// one; see RecencyRank.
	//
	// Free information: the crawl already fetches those pages as one of four
	// orderings and previously discarded their sequence.
	InvRecency int64
}

// unranked is where assets with no recency rank sort: after every asset that
// has one. Larger than any reachable page number by a wide margin.
const unranked = int64(1) << 40

// RecencyRank is InvRecency with "unknown" mapped to a value that sorts last,
// so a recency ordering does not silently present unranked assets as the newest
// things in the catalogue.
func (a Asset) RecencyRank() int64 {
	if a.InvRecency <= 0 {
		return unranked
	}
	return a.InvRecency
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

// The values Pricing returns, and the values the ?price= parameter accepts.
//
// Pricing itself only ever returns the first two: free and paid partition the
// catalogue exactly. The two ceilings are query-side narrowings of "paid", not
// a third and fourth state an asset can be in, which is why they are listed
// here but never stored on a document.
const (
	PricingFree    = "free"
	PricingPaid    = "paid"
	PricingUnder5  = "under-5"
	PricingUnder20 = "under-20"
)

// The ceilings the two bounded price filters apply, in US dollars. Compared
// against the converted PriceUSD rather than the listed price, so a €4 asset
// is under $5 only if the exchange rate says it is.
const (
	CeilingUnder5  = 5.0
	CeilingUnder20 = 20.0
)

// The orderings the ?sort= parameter accepts.
//
// SortRecent is not a sort by publication date, because itch.io's listing
// markup carries no date to sort by. It orders by position in itch.io's own
// newest view, which is an ordering by recency without ever claiming to be a
// timestamp - and which only covers the ~7,200 assets that view reaches. The
// control is hidden entirely when the loaded dataset has no such ranks, so it
// is never offered as a promise the data cannot keep.
//
// These live here, beside the pricing vocabulary, because three packages need
// to agree on them - the query layer that applies them, the handler that parses
// them, and the templates that render the control.
const (
	SortRelevance = "relevance" // best match first; meaningless without a query
	SortPopular   = "popular"   // itch.io's own popularity ordering
	SortTitle     = "title"     // A-Z
	SortPrice     = "price"     // cheapest first, across currencies
	SortRecent    = "recent"    // itch.io's own newest ordering, as far as it reaches
)

func (a Asset) String() string {
	return fmt.Sprintf("GameId: %s, Title: %s, Author: %s, Description: %s, Link: %s, ThumbUrl: %s, Price: %s, InvPopularity: %d", a.GameId, a.Title, a.Author, a.Description, a.Link, a.ThumbUrl, a.Price, a.InvPopularity)
}

// Stats is what a crawl measured about its own completeness.
//
// Recorded because the number cannot be recovered afterwards: the webserver can
// count what it loaded, but only the crawl ever saw how big itch.io said the
// catalogue was at the time. Without it the site could report "96,903 assets"
// and leave a visitor to assume that is all of them.
type Stats struct {
	// Indexed is how many assets the published index holds.
	Indexed int64
	// Catalogue is how many itch.io reported having when the crawl started.
	Catalogue int64
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
	AuthorKey     string // the author folded to one keyword, for exact filtering
	Description   string
	Tags          []string
	TagSlugs      []string
	Pricing       string // PricingFree or PricingPaid
	SortTitle     string
	InvPopularity int64
	RecencyRank   int64

	// PriceUSD is what the asset costs in dollars, per the exchange-rate
	// snapshot in force when the index was built. It exists because "under $5"
	// and "cheapest first" both need one number that compares across the
	// dozen-odd currencies sellers price in.
	//
	// Unreadable prices are stored as UnknownPrice rather than 0, so that an
	// asset whose currency this build did not recognise sorts last and matches
	// no ceiling, instead of presenting itself as the cheapest thing on offer.
	PriceUSD float64
}

// UnknownPrice is where assets with an unparseable price sort and filter: past
// any ceiling anyone would set, and last in a cheapest-first ordering.
const UnknownPrice = 1e9

// AuthorKey folds an author's name to the form the index filters on. Exported
// because the query side has to derive the same key from a URL parameter, and
// two subtly different foldings would mean an author link that matches nothing.
func AuthorKey(author string) string {
	return strings.ToLower(strings.Join(strings.Fields(author), " "))
}

// NewIndexedAsset derives the indexable form of an asset, including the
// duplicated fields described above.
//
// priceUSD is passed in rather than computed here because it depends on an
// exchange-rate snapshot, which is a thing the crawl fetches and this package
// has no business knowing about.
func NewIndexedAsset(a Asset, priceUSD float64) IndexedAsset {
	return IndexedAsset{
		GameId:        a.GameId,
		Title:         a.Title,
		Author:        a.Author,
		AuthorKey:     AuthorKey(a.Author),
		Description:   a.Description,
		Tags:          a.Tags,
		TagSlugs:      a.Tags,
		Pricing:       a.Pricing(),
		SortTitle:     strings.ToLower(strings.TrimSpace(a.Title)),
		InvPopularity: a.InvPopularity,
		RecencyRank:   a.RecencyRank(),
		PriceUSD:      priceUSD,
	}
}

func (a IndexedAsset) String() string {
	return fmt.Sprintf("GameId: %s, Title: %s, Author: %s, Description: %s, Tags: %v, Pricing: %s, InvPopularity: %d", a.GameId, a.Title, a.Author, a.Description, a.Tags, a.Pricing, a.InvPopularity)
}

// HourBucket is one slot of a 24-hour ring: how many searches landed in a
// given wall-clock hour. EpochHour, not a time.Time, so a bucket compares
// with plain integer equality when deciding whether it is stale.
type HourBucket struct {
	EpochHour int64
	Searches  uint64
}

// Traffic is the persisted form of internal/metrics.Counters: what survives a
// restart. It carries only aggregate counts - no addresses, no user agents,
// no query text - because it is read back out onto a public page.
type Traffic struct {
	// FirstSeen is when counting began, kept separate from process start so a
	// restart does not reset "counting since" on the public page.
	FirstSeen time.Time

	Total, Index, Results, About, Stats, Static uint64
	Searches                                    uint64

	Hours [24]HourBucket
}
