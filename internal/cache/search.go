package cache

import (
	"fmt"
	"itchgrep/internal/logging"
	"itchgrep/pkg/models"
	"sort"
	"strings"

	"github.com/blevesearch/bleve/v2"
	"github.com/blevesearch/bleve/v2/search"
	"github.com/blevesearch/bleve/v2/search/query"
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
	// would have been the cheaper implementation and the wrong behavior -
	// adding a second tag to a filter is how a person narrows a search, and a
	// filter that grows the result set when you add to it reads as broken.
	Tags []string
	// ExcludeTags are the mirror image: an asset carrying any of them is out,
	// regardless of what else it matches. Narrowing by what you do not want is
	// often easier than naming what you do - "2d, not pixel-art" has no
	// positive phrasing.
	ExcludeTags []string
	// Author restricts to one creator, matched exactly rather than searched.
	Author string
	Price  string // "", or one of the models.Pricing* values
	Sort   string // one of the Sort* constants; empty picks a sensible default
	Page   int64

	// MinFirstSeen filters to assets whose FirstSeenUnix is at least this
	// value. Zero means no filter - most callers do not care when an asset
	// first appeared, and a document stamped FirstSeenUnix 0 (unknown) would
	// otherwise need special-casing to stay included by default.
	MinFirstSeen int64

	// Limit, when set, is the exact number of hits to fetch in one pass,
	// overriding pageSize for the size of the single bleve request. It exists
	// for /new, which shows a fixed-size page with no "next" to ask for -
	// every other caller pages through pageSize and leaves this zero.
	Limit int64

	// Like, when set, is the GameId of an asset to find similar items to. The
	// query narrows to a disjunction over that asset's own tags and excludes
	// the asset itself; an unknown id yields an empty result set rather than
	// an error, so a stale or hand-edited link degrades the same way an
	// unknown tag does.
	Like string

	// NoFacets skips the tag facet bleve would otherwise compute over the
	// whole match set. It exists for /new and /feed, which render a fixed
	// list with no sidebar to fill in - the facet costs a pass over up to
	// maxFacetTags terms regardless of whether anything reads Results.Tags
	// afterwards, so a caller that never will should say so up front rather
	// than pay for it and discard the answer.
	NoFacets bool
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

	// LikeTitle is the title of the ?like= source asset, threaded through so
	// the caller can render "similar to X" without a second lookup. Empty
	// when Like was not set, or was set to an id nothing was found for.
	LikeTitle string
}

// resolvedSort is the ordering to apply, filling in the default. Relevance is
// only meaningful when something was searched for; with no query every document
// scores identically and "best match" degenerates into index order.
func (o SearchOptions) resolvedSort() string {
	switch o.Sort {
	case models.SortPopular, models.SortTitle, models.SortPrice, models.SortRecent, models.SortNew:
		return o.Sort
	case models.SortRelevance:
		if o.Query == "" {
			return models.SortPopular
		}
		return models.SortRelevance
	default:
		if o.Query == "" {
			return models.SortPopular
		}
		return models.SortRelevance
	}
}

// filtered reports whether anything narrows the catalog. An unfiltered,
// unsorted, unsearched request is the front page, which has a much cheaper
// answer than a search.
func (o SearchOptions) filtered() bool {
	return o.Query != "" || len(o.Tags) > 0 || len(o.ExcludeTags) > 0 ||
		o.Price != "" || o.Author != "" || o.MinFirstSeen > 0 || o.Like != ""
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

	// A recency ordering is only offered when the loaded dataset actually
	// carries ranks; asking for one anyway - from a stale bookmark, or from an
	// index built before the crawl started collecting them - falls back rather
	// than returning everything in the arbitrary order of "no rank at all".
	if opts.Sort == models.SortRecent && !c.HasRecency() {
		opts.Sort = models.SortPopular
	}

	// Same treatment for the bounded price filters, and for the same reason: a
	// bookmark carrying ?price=under-5 against an index with no converted prices
	// would otherwise return an empty page rather than the results the rest of
	// the URL asks for. Dropping the clause is the reading a visitor would
	// recognize; an empty page is not.
	if !c.HasPriceBands() && (opts.Price == models.PricingUnder5 || opts.Price == models.PricingUnder20) {
		opts.Price = ""
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
	// start < 0 covers a page number large enough to overflow this
	// multiplication and wrap negative - pageParam rejects that before a
	// request ever reaches here (see maxPage), but this guard is what keeps a
	// negative start from reaching the slice below and panicking, for any
	// caller that did not go through pageParam.
	if start < 0 || start >= int64(len(c.data)) {
		// Out of range: an empty page (not an error) is how infinite scroll on
		// the client knows to stop requesting further pages.
		return Results{Total: int64(len(c.data)), Tags: c.tagCounts}, nil
	}
	end := start + c.pageSize
	if end < start || end > int64(len(c.data)) {
		// end < start is the same overflow, one multiplication later: start
		// was small enough to pass the guard above, but adding pageSize wrapped
		// past int64's range.
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

	// The source asset's tags come from dataMap rather than a bleve lookup:
	// dataMap already indexes every asset in data by GameId (see doRefresh),
	// which is exactly what search() below resolves each hit against - so
	// this reuses that lookup instead of adding a second, index-side way of
	// reading the same document.
	var likeTitle string
	var likeTags []string
	if opts.Like != "" {
		idx, ok := c.dataMap[opts.Like]
		if !ok {
			// Unknown id: nothing to be similar to. Answered as a normal
			// empty result rather than an error, the same way an unknown tag
			// or author matches nothing instead of failing the request.
			return Results{}, nil
		}
		source := c.data[idx]
		likeTitle = source.Title
		likeTags = source.Tags
	}

	from64 := (opts.Page - 1) * c.pageSize
	if from64 < 0 {
		// A page number large enough to overflow that multiplication wraps
		// negative. pageParam rejects it before a request reaches here (see
		// maxPage), but bleve's SearchRequestOptions has no defined behavior
		// for a negative from, so any other caller gets the same "out of
		// range" empty page browse() gives rather than whatever bleve would
		// do with it.
		return Results{LikeTitle: likeTitle}, nil
	}
	from := int(from64)
	size := int(c.pageSize)
	if opts.Limit > 0 {
		size = int(opts.Limit)
	}
	searchRequest := bleve.NewSearchRequestOptions(buildQuery(opts, likeTags), size, from, false)
	searchRequest.SortBy(sortOrder(opts.resolvedSort()))
	// A narrowed search ranks its tags by lift, which needs every tag the
	// matches carry: the niche ones that make the ranking useful sit far below
	// the 200 most common. bleve counts every term whatever the size, so asking
	// for them all costs only the longer sort, not another pass.
	byLift := opts.filtered()
	facetSize := maxFacetTags
	if byLift && len(c.tagCatalog) > facetSize {
		facetSize = len(c.tagCatalog)
	}
	if !opts.NoFacets {
		searchRequest.AddFacet("tags", bleve.NewFacetRequest("TagSlugs", facetSize))
	}

	searchResult, err := c.index.Search(searchRequest)
	if err != nil {
		return Results{}, err
	}

	results := Results{
		Total:     int64(searchResult.Total),
		LikeTitle: likeTitle,
	}
	if byLift {
		results.Tags = c.liftTags(searchResult.Facets["tags"], opts.Tags, int64(searchResult.Total))
	} else {
		results.Tags = facetTags(searchResult.Facets["tags"], opts.Tags)
	}
	// A hit with no asset behind it is dropped rather than rendered. The index
	// and the asset list are published together and loaded together, but not
	// atomically with respect to each other: a crawl that publishes between
	// this refresh reading assets.json and opening the index leaves the two
	// describing slightly different datasets. The map lookup would then return
	// a zero Asset, which renders as a card with no title, no link and no
	// image - worse than one fewer result on the page.
	missing := 0
	for _, hit := range searchResult.Hits {
		idx, ok := c.dataMap[hit.ID]
		if !ok {
			missing++
			continue
		}
		results.Assets = append(results.Assets, c.data[idx])
	}
	if missing > 0 {
		logging.Warning("Dropped %d hit(s) with no asset behind them; the index and the asset list disagree", missing)
	}

	logging.Info("Got %d hits for %s", searchResult.Total, describe(opts))
	return results, nil
}

// buildQuery assembles the text query and the filters into one bleve query.
// likeTags is the ?like= source asset's own tags, already resolved by the
// caller - buildQuery has no dataMap access of its own, and takes them as
// input the same way it takes every other filter value.
//
// The filters are separate conjuncts rather than extra clauses on the fuzzy
// disjunction, and that separation is the point: a filter must decide whether a
// document is in the result set at all, whereas the text query decides where in
// it the document ranks. Folding "free" into the disjunction would merely make
// free assets score higher, which is not what a toggle labeled Free means.
func buildQuery(opts SearchOptions, likeTags []string) query.Query {
	var conjuncts []query.Query

	// Quoted runs are pulled out first and matched as phrases, which is the one
	// thing the fuzzy machinery below cannot do: it scores documents containing
	// the words, in any order, anywhere. Someone who typed quotation marks has
	// said they want them adjacent and in that order.
	phrases, rest := splitPhrases(opts.Query)
	for _, phrase := range phrases {
		conjuncts = append(conjuncts, phraseQuery(phrase))
	}

	if rest != "" {
		// Three passes over the same text, layered by how literally they read
		// it, each boosted higher than the last so a better-matching pass
		// always outranks a looser one when both match the same document.
		// Both fuzzy passes allow a single-character edit (fuzzyness 1:
		// one insertion, deletion, substitution or transposition); what
		// differs between them is the prefix, the number of leading
		// characters that must match exactly before the edit is allowed -
		// veryFuzzy requires only the first 2 characters exact, so it drifts
		// further from what was typed, while fuzzy requires the first 4 and
		// so stays closer. exact requires the term precisely as typed.
		veryFuzzyQuery := buildFuzzyQuery(rest, 1, 2)
		veryFuzzyQuery.SetBoost(2)
		fuzzyQuery := buildFuzzyQuery(rest, 1, 4)
		fuzzyQuery.SetBoost(4)
		exactQuery := buildExactQuery(rest)
		exactQuery.SetBoost(6)
		conjuncts = append(conjuncts, bleve.NewDisjunctionQuery(veryFuzzyQuery, fuzzyQuery, exactQuery))
	}

	// TermQuery, not MatchQuery: TagSlugs is indexed with the keyword analyzer,
	// so the stored term is the whole slug. A MatchQuery would run the query
	// string through the default analyzer and look for "pixel" and "art" as
	// separate terms, neither of which exists in that field.
	for _, tag := range opts.Tags {
		tq := bleve.NewTermQuery(tag)
		tq.SetField("TagSlugs")
		conjuncts = append(conjuncts, tq)
	}
	if opts.Author != "" {
		aq := bleve.NewTermQuery(models.AuthorKey(opts.Author))
		aq.SetField("AuthorKey")
		conjuncts = append(conjuncts, aq)
	}
	if pq := priceQuery(opts.Price); pq != nil {
		conjuncts = append(conjuncts, pq)
	}
	if opts.MinFirstSeen > 0 {
		min := float64(opts.MinFirstSeen)
		inclusive := true
		rq := bleve.NewNumericRangeInclusiveQuery(&min, nil, &inclusive, nil)
		rq.SetField("FirstSeenUnix")
		conjuncts = append(conjuncts, rq)
	}
	if opts.Like != "" {
		if len(likeTags) == 0 {
			// A source asset with no tags at all has nothing to be similar
			// by. Matching nothing here, rather than falling through to an
			// unfiltered search, keeps this the same reading an unknown id
			// gets: "similar to X" with no basis for "similar" is not "show
			// everything".
			conjuncts = append(conjuncts, bleve.NewMatchNoneQuery())
		} else {
			var likeDisjuncts []query.Query
			for _, tag := range likeTags {
				tq := bleve.NewTermQuery(tag)
				tq.SetField("TagSlugs")
				likeDisjuncts = append(likeDisjuncts, tq)
			}
			conjuncts = append(conjuncts, bleve.NewDisjunctionQuery(likeDisjuncts...))
		}
	}

	var exclusions []query.Query
	for _, tag := range opts.ExcludeTags {
		tq := bleve.NewTermQuery(tag)
		tq.SetField("TagSlugs")
		exclusions = append(exclusions, tq)
	}
	if opts.Like != "" {
		// Without this the source asset would appear in its own "similar"
		// list - it necessarily shares every one of its own tags.
		exclusions = append(exclusions, bleve.NewDocIDQuery([]string{opts.Like}))
	}

	if len(exclusions) > 0 {
		// A boolean query, because a conjunction has no way to say "not this".
		// With nothing positive to match, everything is a candidate and the
		// exclusions do the whole job - hence the explicit match-all, without
		// which bleve would answer an all-negative query with nothing at all.
		b := bleve.NewBooleanQuery()
		if len(conjuncts) == 0 {
			b.AddMust(bleve.NewMatchAllQuery())
		} else {
			b.AddMust(conjuncts...)
		}
		b.AddMustNot(exclusions...)
		return b
	}

	if len(conjuncts) == 0 {
		return bleve.NewMatchAllQuery()
	}
	if len(conjuncts) == 1 {
		return conjuncts[0]
	}
	return bleve.NewConjunctionQuery(conjuncts...)
}

// priceQuery turns a price filter into a query, or nil for "no filter".
//
// Free and paid are a keyword match on what the asset is. The two ceilings are
// numeric ranges over the converted dollar value instead, and deliberately
// exclude zero: "under $5" sitting beside a "free" button should mean the cheap
// paid things, not silently contain everything the free button already shows.
func priceQuery(price string) query.Query {
	ceiling := 0.0
	switch price {
	case "":
		return nil
	case models.PricingFree, models.PricingPaid:
		pq := bleve.NewTermQuery(price)
		pq.SetField("Pricing")
		return pq
	case models.PricingUnder5:
		ceiling = models.CeilingUnder5
	case models.PricingUnder20:
		ceiling = models.CeilingUnder20
	default:
		return nil
	}

	min, max := 0.0, ceiling
	inclusive := false
	rq := bleve.NewNumericRangeInclusiveQuery(&min, &max, &inclusive, &inclusive)
	rq.SetField("PriceUSD")
	return rq
}

// indexHasPriceUSD reports whether an index carries the converted dollar value
// at all, by asking it the widest version of the question the price bands ask.
//
// Free assets store 0 and unparseable ones store UnknownPrice, so every document
// written by a current dataservice falls inside this range; an index built
// before the field existed has no document in it whatsoever. Errors count as
// absent, which is the safe direction - it hides two controls rather than
// offering two that cannot work.
func indexHasPriceUSD(index bleve.Index) bool {
	min := 0.0
	inclusive := true
	rq := bleve.NewNumericRangeInclusiveQuery(&min, nil, &inclusive, nil)
	rq.SetField("PriceUSD")

	res, err := index.Search(bleve.NewSearchRequestOptions(rq, 0, 0, false))
	if err != nil {
		logging.Error("Could not probe the index for PriceUSD: %v", err)
		return false
	}
	return res.Total > 0
}

// phraseQuery matches an exact run of words across the fields worth searching,
// with the same field weighting the loose query uses.
func phraseQuery(phrase string) query.Query {
	title := bleve.NewMatchPhraseQuery(phrase)
	title.SetField("Title")
	title.SetBoost(titleBoost)
	description := bleve.NewMatchPhraseQuery(phrase)
	description.SetField("Description")
	description.SetBoost(descriptionBoost)
	tags := bleve.NewMatchPhraseQuery(phrase)
	tags.SetField("Tags")
	tags.SetBoost(tagBoost)
	return bleve.NewDisjunctionQuery(title, description, tags)
}

// splitPhrases separates "quoted runs" from the rest of a query.
//
// An unclosed quote is treated as ordinary text rather than as a phrase running
// to the end of the input: half-typed queries arrive constantly, and turning
// one into a strict phrase match makes the results collapse to nothing at the
// moment the user opens a quote.
func splitPhrases(q string) (phrases []string, rest string) {
	var loose []string
	for {
		open := strings.Index(q, `"`)
		if open < 0 {
			break
		}
		close := strings.Index(q[open+1:], `"`)
		if close < 0 {
			break
		}
		close += open + 1

		loose = append(loose, q[:open])
		if phrase := strings.TrimSpace(q[open+1 : close]); phrase != "" {
			phrases = append(phrases, phrase)
		}
		q = q[close+1:]
	}
	loose = append(loose, q)
	return phrases, strings.TrimSpace(strings.Join(loose, " "))
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
	case models.SortPrice:
		// Free first, then cheapest upward, with the unpriced parked at
		// UnknownPrice where they belong: last, and visibly so.
		return []string{"PriceUSD", "InvPopularity"}
	case models.SortRecent:
		// Everything without a newest-view rank shares one enormous value, so
		// the popularity tiebreak is doing real work here rather than settling
		// the occasional draw.
		return []string{"RecencyRank", "InvPopularity"}
	case models.SortNew:
		// Descending, unlike RecencyRank above: a later FirstSeenUnix is a
		// newer arrival, whereas a smaller InvRecency is. Assets stamped on the
		// same publish tie on FirstSeenUnix, hence the same popularity tiebreak.
		return []string{"-FirstSeenUnix", "InvPopularity"}
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

	// v2's FacetResult.Terms is a *TermFacets accessor type rather than a plain
	// slice (it also backs sorting/trimming internally), so the terms come out
	// through .Terms() instead of ranging the field directly.
	terms := facet.Terms.Terms()
	out := make([]models.Tag, 0, len(terms))
	for _, term := range terms {
		if _, ok := skip[term.Term]; ok {
			continue
		}
		out = append(out, models.Tag{Slug: term.Term, Count: int64(term.Count)})
	}
	return out
}

// minFacetCount is the fewest results a tag must be on to be offered for a
// narrowed search: a tag on one or two results is noise however high its lift.
// The floor is three, or one percent of the matches for a large result set. It
// never exceeds total-1, because a tag on every result narrows nothing and is
// dropped separately, and a floor above that would leave a small result set
// with no sidebar at all.
func minFacetCount(total int64) int64 {
	min := (total + 99) / 100
	if min < 3 {
		min = 3
	}
	if min > total-1 {
		min = total - 1
	}
	if min < 1 {
		min = 1
	}
	return min
}

// liftTags ranks the tags of a narrowed result set by how over-represented
// they are against the whole catalog: (count/total) / (catalogCount/catalogSize).
// Ranked by raw count, the sidebar is the same forty catalog-wide favorites
// (2d, pixel-art, ...) for every search, which is a description of the
// catalog rather than of the search.
//
// A tag on every result is dropped, as are the applied ones (shown as chips),
// and anything under minFacetCount. Ties break by count, then slug, so the
// order is stable. Lift is compared by cross-multiplying the integers, which
// is exact and allocates no floats: total and the catalog size are common to
// every tag, so a beats b exactly when count_a*cat_b > count_b*cat_a.
//
// Caller holds cacheLock.
func (c *Cache) liftTags(facet *search.FacetResult, applied []string, total int64) []models.Tag {
	if len(c.tagCatalog) == 0 {
		// No catalog histogram (a cache built without one): the raw order is
		// the only one available.
		return facetTags(facet, applied)
	}
	if facet == nil {
		return nil
	}
	floor := minFacetCount(total)

	type ranked struct {
		tag models.Tag
		cat int64
	}
	terms := facet.Terms.Terms()
	cands := make([]ranked, 0, len(terms))
terms:
	for _, term := range terms {
		count := int64(term.Count)
		if count >= total || count < floor {
			continue
		}
		for _, a := range applied {
			if a == term.Term {
				continue terms
			}
		}
		cat := c.tagCatalog[term.Term]
		if cat < count {
			// The index and the asset list can disagree for a moment around a
			// publish; a catalog count below the match count cannot be right.
			cat = count
		}
		cands = append(cands, ranked{models.Tag{Slug: term.Term, Count: count}, cat})
	}
	sort.Slice(cands, func(i, j int) bool {
		a, b := cands[i], cands[j]
		if l, r := a.tag.Count*b.cat, b.tag.Count*a.cat; l != r {
			return l > r
		}
		if a.tag.Count != b.tag.Count {
			return a.tag.Count > b.tag.Count
		}
		return a.tag.Slug < b.tag.Slug
	})
	if len(cands) > maxFacetTags {
		cands = cands[:maxFacetTags]
	}
	out := make([]models.Tag, len(cands))
	for i, cand := range cands {
		out[i] = cand.tag
	}
	return out
}

// tagHistogram counts how many assets carry each tag across the whole dataset.
func tagHistogram(assets []models.Asset) map[string]int64 {
	counts := make(map[string]int64, 1024)
	for _, a := range assets {
		for _, t := range a.Tags {
			counts[t]++
		}
	}
	return counts
}

// countTags builds the whole-dataset tag histogram used for the unfiltered
// browse, in the same descending-count order bleve returns a facet in.
func countTags(assets []models.Asset) []models.Tag {
	return topTags(tagHistogram(assets))
}

// topTags orders a histogram by count and keeps the sidebar's head.
func topTags(counts map[string]int64) []models.Tag {
	out := make([]models.Tag, 0, len(counts))
	for slug, n := range counts {
		out = append(out, models.Tag{Slug: slug, Count: n})
	}
	// Count descending, then slug, so the order is stable across refreshes
	// rather than following Go's randomized map iteration.
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

// describe renders a request's shape for the log line, so a slow or empty
// search can be traced back to what shaped it - without ever writing what a
// visitor typed. The query and the author are visitor-entered content, so
// only their presence is logged, never the value; tags, price, sort and page
// come from a fixed vocabulary or the site's own controls, not free text, and
// are logged as-is the same way the access log's query-present boolean (see
// internal/web/logmiddleware.go) draws the same line for the same reason.
func describe(opts SearchOptions) string {
	parts := make([]string, 0, 6)
	if opts.Query != "" {
		parts = append(parts, "query present")
	}
	if len(opts.Tags) > 0 {
		parts = append(parts, "tags "+strings.Join(opts.Tags, "+"))
	}
	if len(opts.ExcludeTags) > 0 {
		parts = append(parts, "without "+strings.Join(opts.ExcludeTags, "+"))
	}
	if opts.Author != "" {
		parts = append(parts, "author filter present")
	}
	if opts.Price != "" {
		parts = append(parts, opts.Price)
	}
	if opts.MinFirstSeen > 0 {
		parts = append(parts, fmt.Sprintf("first seen >= %d", opts.MinFirstSeen))
	}
	if opts.Like != "" {
		parts = append(parts, "like "+opts.Like)
	}
	parts = append(parts, fmt.Sprintf("page %d", opts.Page))
	parts = append(parts, "sorted by "+opts.resolvedSort())
	return strings.Join(parts, ", ")
}
