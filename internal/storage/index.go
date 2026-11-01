package storage

import (
	"github.com/blevesearch/bleve/v2"
	"github.com/blevesearch/bleve/v2/analysis/analyzer/keyword"
	"github.com/blevesearch/bleve/v2/mapping"
)

// IndexMapping is the bleve mapping the published index is built with.
//
// It lives here, beside the index's location and publishing, because it is a
// contract between two processes rather than a detail of either: the
// dataservice writes documents with it and the webserver queries fields that
// only behave as expected because of it. Held privately by the writer, a
// change to it would silently stop the reader's filters matching anything.
//
// bleve's dynamic defaults store three copies of every field nobody reads:
// the "_all" composite field (every query in internal/cache/search.go names
// its own field; nothing ever searches _all), the stored value (a hit is
// resolved back to a models.Asset through cache.dataMap by GameId -
// SearchRequest.Fields is never set, so nothing is ever read back out of the
// index itself) and doc values on every field (only the handful that are
// sorted or faceted need the column store that enables). Those three
// defaults, applied to every field of a small document, are most of a
// ~740MB index for ~97k assets. Every field below turns off whichever of the
// three it does not need, with a comment naming the query in search.go that
// is the reason it keeps the ones it does.
func IndexMapping() *mapping.IndexMappingImpl {
	m := bleve.NewIndexMapping()

	// Store and doc values default to on for every field bleve maps
	// dynamically; turned off globally so a field added to IndexedAsset
	// without a matching entry below fails safe - unsearchable via a stray
	// dynamic mapping is a bug someone notices, silently reintroducing a
	// stored copy or a doc-values column nobody reads is not. IndexDynamic is
	// deliberately left at its default (on): a field that fell through to the
	// dynamic mapping should still be findable, even if this file forgot to
	// give it a real entry.
	m.StoreDynamic = false
	m.DocValuesDynamic = false

	doc := bleve.NewDocumentMapping()

	// The composite "_all" field bleve builds by concatenating every other
	// field's terms exists for a query that names no field of its own.
	// Nothing in search.go issues one - textQuery and
	// phraseQuery all call SetField - so it is disabled outright rather than
	// merely excluded field by field.
	doc.AddSubDocumentMapping("_all", bleve.NewDocumentDisabledMapping())

	// GameId is never queried as a field: buildQuery's exclusions and the
	// ?like= source lookup use bleve.NewDocIDQuery, which reads the
	// document's own ID, not a field indexed alongside it. Disabled rather
	// than left to the dynamic defaults, so it costs nothing rather than one
	// needless dynamically-analyzed term per document.
	doc.AddSubDocumentMapping("GameId", bleve.NewDocumentDisabledMapping())

	// text is the shape every searched-as-prose field shares: MatchQuery
	// (fuzzy and exact) and MatchPhraseQuery both need the field split into
	// terms with positions, which is what IncludeTermVectors buys. Store and
	// DocValues stay off - nothing sorts or facets on free text.
	text := func() *mapping.FieldMapping {
		f := bleve.NewTextFieldMapping()
		f.Store = false
		f.DocValues = false
		f.IncludeInAll = false
		f.IncludeTermVectors = true // phrase queries need term positions
		return f
	}

	// kw is the exact-match counterpart: the keyword analyzer leaves a value
	// as one indivisible term, so a filter for "pixel-art" cannot also match
	// everything merely tagged "art" the way the default analyzer would by
	// splitting it into "pixel" and "art". No term vectors - nothing
	// phrase-queries a keyword field - and searchable unless told otherwise
	// below.
	kw := func() *mapping.FieldMapping {
		f := bleve.NewTextFieldMapping()
		f.Analyzer = keyword.Name
		f.Store = false
		f.DocValues = false
		f.IncludeInAll = false
		f.IncludeTermVectors = false
		return f
	}

	// num is a plain numeric field: searchable by default (for the range
	// filters that need it), never stored, doc-valued only where a caller
	// below turns it on. Sorting was measured to require both Index and
	// DocValues together - a field mapped DocValues-only sorts on a
	// meaningless sentinel, not merely slower - so every numeric field kept
	// below stays indexed even where nothing ever filters a range of it.
	num := func() *mapping.FieldMapping {
		f := bleve.NewNumericFieldMapping()
		f.Store = false
		f.DocValues = false
		f.IncludeInAll = false
		return f
	}

	// Title, Description, Tags and Author are the fields textQuery
	// and phraseQuery search. All four keep term vectors for
	// the same reason: phraseQuery runs a MatchPhraseQuery against Title,
	// Description and Tags, and Author is searched by the same fuzzy/exact
	// machinery as the other three - leaving it without term vectors would
	// make it the one field to behave differently under a future phrase
	// search.
	doc.AddFieldMappingsAt("Title", text())
	doc.AddFieldMappingsAt("Description", text())
	doc.AddFieldMappingsAt("Tags", text())
	doc.AddFieldMappingsAt("Author", text())

	// TagSlugs backs both the exact tag filter (a TermQuery in buildQuery)
	// and the sidebar's tag facet (search() adds a FacetRequest on it) -
	// facets read doc values, so this is the one keyword field that needs
	// them on.
	tagSlugs := kw()
	tagSlugs.DocValues = true
	doc.AddFieldMappingsAt("TagSlugs", tagSlugs)

	// SortTitle backs sortOrder()'s "title" ordering (models.SortTitle) and
	// is never filtered on, so it needs doc values to sort by but answers no
	// query of its own.
	sortTitle := kw()
	sortTitle.DocValues = true
	doc.AddFieldMappingsAt("SortTitle", sortTitle)

	// AuthorKey is the ?author= filter's TermQuery target in buildQuery,
	// matched exactly rather than searched. Never sorted or faceted, so it
	// stops at being searchable; no doc values to add.
	doc.AddFieldMappingsAt("AuthorKey", kw())

	// Pricing is the free/paid TermQuery target in priceQuery. Also never
	// sorted or faceted - the two bounded price bands filter PriceUSD
	// instead, below.
	doc.AddFieldMappingsAt("Pricing", kw())

	// InvPopularity is every sortOrder()'s tiebreak, and models.SortPopular's
	// whole ordering, but no query ever filters a range of it - it needs doc
	// values to sort by and nothing else.
	invPopularity := num()
	invPopularity.DocValues = true
	doc.AddFieldMappingsAt("InvPopularity", invPopularity)

	// RecencyRank backs sortOrder()'s "recent" ordering the same way
	// InvPopularity backs every ordering: sorted only, never filtered.
	recencyRank := num()
	recencyRank.DocValues = true
	doc.AddFieldMappingsAt("RecencyRank", recencyRank)

	// PriceUSD is both filtered (priceQuery's two ceilings, and
	// indexHasPriceUSD's own probe - all NumericRangeInclusiveQuery) and
	// sorted (sortOrder's "price" ordering), so it is one of the two numeric
	// fields that needs both a search index and doc values.
	priceUSD := num()
	priceUSD.DocValues = true
	doc.AddFieldMappingsAt("PriceUSD", priceUSD)

	// FirstSeenUnix is the other field doing double duty: buildQuery's
	// MinFirstSeen clause range-filters it, and sortOrder's "new" ordering
	// sorts by it descending. Same shape as PriceUSD, same reason.
	firstSeenUnix := num()
	firstSeenUnix.DocValues = true
	doc.AddFieldMappingsAt("FirstSeenUnix", firstSeenUnix)

	m.DefaultMapping = doc
	return m
}
