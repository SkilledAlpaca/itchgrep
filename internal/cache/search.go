package cache

import (
	"fmt"
	"itchgrep/internal/logging"
	"itchgrep/pkg/models"
	"sort"
	"strings"

	"github.com/blevesearch/bleve"
	"github.com/blevesearch/bleve/search"
	"github.com/blevesearch/bleve/search/query"
)

// maxFacetTags is how many tags the sidebar asks bleve to count. The vocabulary
// is ~600 tags and the sidebar shows a few dozen, but the facet has to be wide
// enough that the displayed head is the real head rather than the head of an
// arbitrary truncation.
const maxFacetTags = 200

// SearchOptions is one request for results. The zero value is the unfiltered
// first page in popularity order, which is the site's front page.
type SearchOptions struct {
	Query string
	// Tags are combined with AND: an asset must carry every one of them. OR
	// would have been the cheaper implementation and the wrong behaviour -
	// adding a second tag to a filter is how a person narrows a search, and a
	// filter that grows the result set when you add to it reads as broken.
	Tags  []string
	Price string // "", models.PricingFree or models.PricingPaid
	Sort  string // one of the Sort* constants; empty picks a sensible default
	Page  int64
}

// Results is one page of assets plus the context the UI needs around them: how
// many matched in total, and which tags those matches carry.
type Results struct {
	Assets []models.Asset
	Total  int64
	// Tags are the most common tags across the whole match set, not just this
	// page, so the sidebar is a map of where the rest of the results are rather
	// than a description of the 36 on screen.
	Tags []models.Tag
}

// resolvedSort is the ordering to apply, filling in the default. Relevance is
// only meaningful when something was searched for; with no query every document
// scores identically and "best match" degenerates into index order.
func (o SearchOptions) resolvedSort() string {
	switch o.Sort {
	case models.SortRelevance, models.SortPopular, models.SortTitle:
		if o.Sort == models.SortRelevance && o.Query == "" {
			return models.SortPopular
		}
		return o.Sort
	default:
		if o.Query == "" {
			return models.SortPopular
		}
		return models.SortRelevance
	}
}

// filtered reports whether anything narrows the catalogue. An unfiltered,
// unsorted, unsearched request is the front page, which has a much cheaper
// answer than a search.
func (o SearchOptions) filtered() bool {
	return o.Query != "" || len(o.Tags) > 0 || o.Price != ""
}

// Find answers a search, a filtered browse, or the plain front page.
//
// One entry point rather than the separate browse and search calls it replaces:
// once tags, price and ordering can be combined with a query, "browse" is just
// a search with an empty query, and two code paths would only differ in which
// filters they forgot to apply.
func (c *Cache) Find(opts SearchOptions) (Results, error) {
	if opts.Page < 1 {
		return Results{}, fmt.Errorf("cache: page must be >= 1, got %d", opts.Page)
	}

	// check for stale cache, refresh if needed. If the refresh fails we still
	// fall through and serve whatever was previously loaded (if anything)
	// rather than failing the request.
	if c.IsCacheExpired() {
		if err := c.RefreshDataCache(); err != nil {
			logging.Error("Failed to refresh cache, serving previous data if available: %v", err)
		}
	}

	if !opts.filtered() && opts.resolvedSort() == models.SortPopular {
		return c.browse(opts.Page)
	}
	return c.search(opts)
}

// browse serves the unfiltered listing straight from the popularity-sorted
// slice built at refresh time. No index involved: this is the front page and
// every scroll of it, and a match-all query against bleve would be work done
// solely to arrive back at the order the slice is already in.
func (c *Cache) browse(page int64) (Results, error) {
	c.cacheLock.RLock()
	defer c.cacheLock.RUnlock()

	if c.data == nil {
		return Results{}, ErrNotReady
	}

	start := (page - 1) * c.pageSize
	end := start + c.pageSize
	if start >= int64(len(c.data)) {
		// Out of range: an empty page (not an error) is how infinite scroll on
		// the client knows to stop requesting further pages.
		return Results{Total: int64(len(c.data)), Tags: c.tagCounts}, nil
	}
	if end > int64(len(c.data)) {
		end = int64(len(c.data))
	}
	return Results{
		Assets: c.data[start:end],
		Total:  int64(len(c.data)),
		Tags:   c.tagCounts,
	}, nil
}

func (c *Cache) search(opts SearchOptions) (Results, error) {
	c.cacheLock.RLock()
	defer c.cacheLock.RUnlock()

	if c.index == nil {
		return Results{}, ErrNotReady
	}

	from := int((opts.Page - 1) * c.pageSize)
	searchRequest := bleve.NewSearchRequestOptions(buildQuery(opts), int(c.pageSize), from, false)
	searchRequest.SortBy(sortOrder(opts.resolvedSort()))
	searchRequest.AddFacet("tags", bleve.NewFacetRequest("TagSlugs", maxFacetTags))

	searchResult, err := c.index.Search(searchRequest)
	if err != nil {
		return Results{}, err
	}

	results := Results{
		Total: int64(searchResult.Total),
		Tags:  facetTags(searchResult.Facets["tags"], opts.Tags),
	}
	for _, hit := range searchResult.Hits {
		results.Assets = append(results.Assets, c.dataMap[hit.ID])
	}

	logging.Info("Got %d hits for %s", searchResult.Total, describe(opts))
	return results, nil
}

// buildQuery assembles the text query and the filters into one bleve query.
//
// The filters are separate conjuncts rather than extra clauses on the fuzzy
// disjunction, and that separation is the point: a filter must decide whether a
// document is in the result set at all, whereas the text query decides where in
// it the document ranks. Folding "free" into the disjunction would merely make
// free assets score higher, which is not what a toggle labelled Free means.
func buildQuery(opts SearchOptions) query.Query {
	var conjuncts []query.Query

	if opts.Query != "" {
		veryFuzzyQuery := buildFuzzyQuery(opts.Query, 1, 2)
		veryFuzzyQuery.SetBoost(2)
		fuzzyQuery := buildFuzzyQuery(opts.Query, 1, 4)
		fuzzyQuery.SetBoost(4)
		exactQuery := buildExactQuery(opts.Query)
		exactQuery.SetBoost(6)
		conjuncts = append(conjuncts, bleve.NewDisjunctionQuery(veryFuzzyQuery, fuzzyQuery, exactQuery))
	}

	// TermQuery, not MatchQuery: TagSlugs is indexed with the keyword analyser,
	// so the stored term is the whole slug. A MatchQuery would run the query
	// string through the default analyser and look for "pixel" and "art" as
	// separate terms, neither of which exists in that field.
	for _, tag := range opts.Tags {
		tq := bleve.NewTermQuery(tag)
		tq.SetField("TagSlugs")
		conjuncts = append(conjuncts, tq)
	}
	if opts.Price != "" {
		pq := bleve.NewTermQuery(opts.Price)
		pq.SetField("Pricing")
		conjuncts = append(conjuncts, pq)
	}

	if len(conjuncts) == 0 {
		return bleve.NewMatchAllQuery()
	}
	if len(conjuncts) == 1 {
		return conjuncts[0]
	}
	return bleve.NewConjunctionQuery(conjuncts...)
}

// sortOrder maps an ordering onto the bleve sort fields that implement it.
// Every ordering ends in a deterministic tiebreak, so that paging through a
// result set cannot show the same asset twice or skip one: bleve's default
// tiebreak is document ID order, which is stable, but only after the fields we
// asked for - and InvPopularity is unique enough to settle almost everything.
func sortOrder(sort string) []string {
	switch sort {
	case models.SortTitle:
		return []string{"SortTitle", "InvPopularity"}
	case models.SortPopular:
		return []string{"InvPopularity"}
	default:
		return []string{"-_score", "InvPopularity"}
	}
}

// facetTags turns a bleve facet into the sidebar's list, dropping the tags
// already applied. Those are shown as removable chips above the results
// instead, and every one of them necessarily has a count equal to the total,
// so leaving them in the sidebar would put a row of useless entries at the top
// of it.
func facetTags(facet *search.FacetResult, applied []string) []models.Tag {
	if facet == nil {
		return nil
	}
	skip := make(map[string]struct{}, len(applied))
	for _, t := range applied {
		skip[t] = struct{}{}
	}

	out := make([]models.Tag, 0, len(facet.Terms))
	for _, term := range facet.Terms {
		if _, ok := skip[term.Term]; ok {
			continue
		}
		out = append(out, models.Tag{Slug: term.Term, Count: int64(term.Count)})
	}
	return out
}

// countTags builds the whole-dataset tag histogram used for the unfiltered
// browse, in the same descending-count order bleve returns a facet in.
func countTags(assets []models.Asset) []models.Tag {
	counts := make(map[string]int64, 1024)
	for _, a := range assets {
		for _, t := range a.Tags {
			counts[t]++
		}
	}

	out := make([]models.Tag, 0, len(counts))
	for slug, n := range counts {
		out = append(out, models.Tag{Slug: slug, Count: n})
	}
	// Count descending, then slug, so the order is stable across refreshes
	// rather than following Go's randomised map iteration.
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Slug < out[j].Slug
	})
	if len(out) > maxFacetTags {
		out = out[:maxFacetTags]
	}
	return out
}

// describe renders a request for the log line, so a slow or empty search can be
// traced back to everything that shaped it rather than just its query string.
func describe(opts SearchOptions) string {
	parts := make([]string, 0, 4)
	if opts.Query != "" {
		parts = append(parts, fmt.Sprintf("query %q", opts.Query))
	}
	if len(opts.Tags) > 0 {
		parts = append(parts, "tags "+strings.Join(opts.Tags, "+"))
	}
	if opts.Price != "" {
		parts = append(parts, opts.Price)
	}
	parts = append(parts, "sorted by "+opts.resolvedSort())
	return strings.Join(parts, ", ")
}
