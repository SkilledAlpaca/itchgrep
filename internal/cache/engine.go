package cache

import (
	"cmp"
	"itchgrep/internal/logging"
	"itchgrep/pkg/models"
	"itchgrep/pkg/money"
	"math"
	"slices"
	"sort"
	"strings"
)

// engine is the in-memory answer to a request with no text query: orderings
// computed once per refresh, so that serving a sort is slicing a list rather
// than a pass through the search index.
//
// bleve earns its keep on text relevance. A request with no text asks for
// nothing but a filter and an ordering, and answering that through the index
// meant visiting doc values for every match (the tag facet and the sort keys
// were 62% of the collector's time on the 127,000-asset benchmark). Everything
// the engine holds is derived from the asset list the cache already has, in the
// positions that list already has: an asset is its index into Cache.data.
//
// Built once per refresh outside the write lock and read-only afterwards, so
// request goroutines share it without locking.
type engine struct {
	n int

	// firstSeen is Asset.FirstSeenUnix() per asset.
	firstSeen []int64

	// usd is the price in dollars per asset, exactly as the index stores it
	// (Asset.PriceUSD with the snapshot the index was built with): zero for
	// free, models.UnknownPrice for an unreadable price. Kept as float64 rather
	// than integer cents because the band edges are compared against the same
	// float64 the index holds; a rounded copy could disagree on the cent.
	usd []float64
	// free is Asset.Free(). Kept beside usd because a paid asset can convert to
	// zero dollars and must still be paid.
	free []bool

	// Tags are interned: tagSlugs[id] is the slug, tagIDs the reverse. The
	// vocabulary is a few hundred, so an id is a uint16, and each asset's ids
	// are one run of tagList delimited by tagOff (tagOff[i]:tagOff[i+1]),
	// sorted and without duplicates.
	tagSlugs []string
	tagIDs   map[string]uint16
	tagOff   []int32
	tagList  []uint16

	// authorOf is the asset's author as an id into authorIDs (keyed by
	// models.AuthorKey), 0 for none. Authors number in the tens of thousands.
	authorOf  []uint32
	authorIDs map[string]uint32

	// orders holds the positions of every asset sorted by one key, for the four
	// orderings that are not the data's own. Popularity is the data's own
	// order (the list is sorted by it at refresh), so it needs no list.
	//
	// Each ends in the same tiebreak bleve applies: InvPopularity, then the
	// order the assets were indexed in. Position in the data is both, because
	// the data is stable-sorted by InvPopularity from the order of assets.json,
	// which is the order the dataservice indexed them in.
	orders [numOrders][]int32
}

type orderKind int

const (
	orderTitle orderKind = iota
	orderPrice
	orderRecent
	orderNew
	numOrders
)

// buildEngine derives the engine from the asset list, which must already be in
// popularity order. rates must be the snapshot the index was built with, so
// that the price ordering is the one the index would give.
func buildEngine(data []models.Asset, rates money.Rates) *engine {
	n := len(data)
	e := &engine{
		n:         n,
		firstSeen: make([]int64, n),
		usd:       make([]float64, n),
		free:      make([]bool, n),
		tagIDs:    make(map[string]uint16, 1024),
		tagOff:    make([]int32, n+1),
		authorOf:  make([]uint32, n),
		authorIDs: make(map[string]uint32, n/4),
	}

	// Only needed while sorting; dropped afterwards.
	titles := make([]string, n)
	recency := make([]int64, n)
	var ids []uint16
	for i := range data {
		a := &data[i]
		titles[i] = strings.ToLower(strings.TrimSpace(a.Title))
		e.usd[i], _ = a.PriceUSD(rates)
		e.free[i] = a.Free()
		recency[i] = a.RecencyRank()
		e.firstSeen[i] = a.FirstSeenUnix()

		ids = ids[:0]
		for _, t := range a.Tags {
			id, ok := e.tagIDs[t]
			if !ok {
				if len(e.tagSlugs) > math.MaxUint16 {
					// More tags than an id holds. The real vocabulary is a few
					// hundred; refusing to build leaves every request on the
					// search index, which is slower but right.
					logging.Warning("More than %d distinct tags; the in-memory engine is disabled", math.MaxUint16)
					return nil
				}
				id = uint16(len(e.tagSlugs))
				e.tagIDs[t] = id
				e.tagSlugs = append(e.tagSlugs, t)
			}
			ids = append(ids, id)
		}
		slices.Sort(ids)
		ids = slices.Compact(ids)
		e.tagList = append(e.tagList, ids...)
		e.tagOff[i+1] = int32(len(e.tagList))

		if key := models.AuthorKey(a.Author); key != "" {
			id, ok := e.authorIDs[key]
			if !ok {
				id = uint32(len(e.authorIDs) + 1)
				e.authorIDs[key] = id
			}
			e.authorOf[i] = id
		}
	}
	prices := e.usd

	sorted := func(less func(a, b int32) int) []int32 {
		out := make([]int32, n)
		for i := range out {
			out[i] = int32(i)
		}
		slices.SortFunc(out, func(a, b int32) int {
			if c := less(a, b); c != 0 {
				return c
			}
			// Equal on the key: popularity order, which is position.
			return cmp.Compare(a, b)
		})
		return out
	}
	e.orders[orderTitle] = sorted(func(a, b int32) int { return strings.Compare(titles[a], titles[b]) })
	e.orders[orderPrice] = sorted(func(a, b int32) int { return cmp.Compare(prices[a], prices[b]) })
	e.orders[orderRecent] = sorted(func(a, b int32) int { return cmp.Compare(recency[a], recency[b]) })
	// Descending: a later FirstSeenUnix is a newer arrival.
	e.orders[orderNew] = sorted(func(a, b int32) int { return cmp.Compare(e.firstSeen[b], e.firstSeen[a]) })
	return e
}

// order returns the positions in the order of one sort, or nil for popularity,
// which is the identity.
func (e *engine) order(sortKey string) []int32 {
	switch sortKey {
	case models.SortTitle:
		return e.orders[orderTitle]
	case models.SortPrice:
		return e.orders[orderPrice]
	case models.SortRecent:
		return e.orders[orderRecent]
	case models.SortNew:
		return e.orders[orderNew]
	}
	return nil
}

// engineServes reports whether the engine can answer opts: any request with no
// text query. Text relevance is what the search index is for.
func engineServes(opts SearchOptions) bool { return opts.Query == "" }

// engineFilter is a request's filters translated into the engine's terms.
type engineFilter struct {
	// none is set when a filter can match nothing at all (a required tag no
	// asset carries), so the pass can be skipped.
	none bool
	// req are the required tags (AND), excl the excluded ones (any), and any the
	// ?like= source's tags (at least one of them).
	req, excl, any []uint16
	hasAny         bool
	// skip is the position of the ?like= source, which is not its own
	// neighbor; -1 for none.
	skip      int
	author    uint32
	hasAuthor bool
	price     string
	minFirst  int64
}

// pure reports whether nothing but MinFirstSeen narrows the catalog, which
// makes the matches a prefix of an ordering and the pass unnecessary.
func (f *engineFilter) pure() bool {
	return len(f.req) == 0 && len(f.excl) == 0 && !f.hasAny && !f.hasAuthor && f.price == "" && f.skip < 0
}

func (e *engine) filterFor(opts SearchOptions, likeTags []string, likeIdx int) *engineFilter {
	f := &engineFilter{skip: -1, price: opts.Price, minFirst: opts.MinFirstSeen}
	for _, t := range opts.Tags {
		id, ok := e.tagIDs[t]
		if !ok {
			f.none = true
			continue
		}
		f.req = append(f.req, id)
	}
	for _, t := range opts.ExcludeTags {
		// A tag no asset carries excludes nothing.
		if id, ok := e.tagIDs[t]; ok {
			f.excl = append(f.excl, id)
		}
	}
	if opts.Author != "" {
		f.hasAuthor = true
		f.author = e.authorIDs[models.AuthorKey(opts.Author)]
		if f.author == 0 {
			f.none = true
		}
	}
	if opts.Like != "" {
		f.skip = likeIdx
		f.hasAny = true
		for _, t := range likeTags {
			if id, ok := e.tagIDs[t]; ok {
				f.any = append(f.any, id)
			}
		}
		if len(f.any) == 0 {
			// A source with no tags has nothing to be similar by.
			f.none = true
		}
	}
	switch opts.Price {
	case "", models.PricingFree, models.PricingPaid, models.PricingUnder5, models.PricingUnder20:
	default:
		// priceQuery ignores a value it does not know.
		f.price = ""
	}
	return f
}

// matches reports whether asset i passes every filter. The conditions are
// ordered cheapest and most selective first; they are all conjuncts, so the
// order changes nothing but the time.
func (e *engine) matches(f *engineFilter, i int) bool {
	if i == f.skip {
		return false
	}
	if f.minFirst > 0 && e.firstSeen[i] < f.minFirst {
		return false
	}
	if f.hasAuthor && e.authorOf[i] != f.author {
		return false
	}
	switch f.price {
	case "":
	case models.PricingFree:
		if !e.free[i] {
			return false
		}
	case models.PricingPaid:
		if e.free[i] {
			return false
		}
	case models.PricingUnder5:
		// Exclusive at both ends, as the index's range is: zero is what the
		// free filter is for, and the ceiling is "under".
		if u := e.usd[i]; !(u > 0 && u < models.CeilingUnder5) {
			return false
		}
	case models.PricingUnder20:
		if u := e.usd[i]; !(u > 0 && u < models.CeilingUnder20) {
			return false
		}
	}
	tags := e.tagList[e.tagOff[i]:e.tagOff[i+1]]
	for _, t := range f.req {
		if !slices.Contains(tags, t) {
			return false
		}
	}
	if f.hasAny {
		found := false
		for _, t := range f.any {
			if slices.Contains(tags, t) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	for _, t := range f.excl {
		if slices.Contains(tags, t) {
			return false
		}
	}
	return true
}

// searchEngine answers opts from the engine. handled is false when the engine
// does not take this request (or has not been built), and the caller falls
// through to the search index.
//
// It is the filtering pass bleve would make, minus the machinery: one
// sequential sweep over the assets that tests each against the filters, counts
// the match total and, when a sidebar is wanted, every match's tags; then a walk
// down the requested ordering that stops once the page is full. The ordering
// is a precomputed list, so no sort happens per request.
func (c *Cache) searchEngine(opts SearchOptions) (res Results, handled bool, err error) {
	c.cacheLock.RLock()
	defer c.cacheLock.RUnlock()

	if c.eng == nil || !engineServes(opts) {
		return Results{}, false, nil
	}
	if c.data == nil {
		return Results{}, true, ErrNotReady
	}
	e := c.eng

	// The source asset's tags come from the data, as in the index path.
	var likeTitle string
	var likeTags []string
	likeIdx := -1
	if opts.Like != "" {
		idx, ok := c.dataMap[opts.Like]
		if !ok {
			return Results{}, true, nil
		}
		likeIdx = idx
		likeTitle = c.data[idx].Title
		likeTags = c.data[idx].Tags
	}

	// start < 0 and end < start cover page numbers large enough to overflow the
	// multiplication; see browse.
	start := (opts.Page - 1) * c.pageSize
	if start < 0 {
		return Results{LikeTitle: likeTitle}, true, nil
	}
	size := c.pageSize
	if opts.Limit > 0 {
		size = opts.Limit
	}

	f := e.filterFor(opts, likeTags, likeIdx)
	order := e.order(opts.resolvedSort())
	byLift := opts.filtered()
	wantTags := !opts.NoFacets

	var total int64
	var positions []int32
	var counts []int32

	switch {
	case f.none:
		// Nothing can match; the sidebar is empty too.
	case f.pure() && opts.MinFirstSeen == 0 && !(wantTags && byLift):
		// Everything matches. (byLift with nothing to filter on is a request
		// that excludes a tag no asset carries: its sidebar is still ranked, and
		// that needs the pass below.)
		total = int64(e.n)
	case f.pure() && opts.resolvedSort() == models.SortNew && !wantTags:
		// A prefix of the newest-first ordering, and no sidebar to fill in.
		total = int64(sort.Search(e.n, func(k int) bool { return e.firstSeen[order[k]] < opts.MinFirstSeen }))
	default:
		bits := make([]uint64, (e.n+63)/64)
		if wantTags {
			counts = make([]int32, len(e.tagSlugs))
		}
		for i := 0; i < e.n; i++ {
			if !e.matches(f, i) {
				continue
			}
			bits[i>>6] |= 1 << (uint(i) & 63)
			total++
			if wantTags {
				for _, t := range e.tagList[e.tagOff[i]:e.tagOff[i+1]] {
					counts[t]++
				}
			}
		}
		// Walk the ordering to the page, skipping what did not match.
		if start < total {
			need := start + size
			var seen int64
			for k := 0; k < e.n && seen < need; k++ {
				i := k
				if order != nil {
					i = int(order[k])
				}
				if bits[i>>6]&(1<<(uint(i)&63)) == 0 {
					continue
				}
				if seen >= start {
					positions = append(positions, int32(i))
				}
				seen++
			}
		}
	}

	res = Results{Total: total, LikeTitle: likeTitle}
	if positions == nil && start < total && size > 0 {
		// The unfiltered shapes: the page is a slice of the ordering.
		end := start + size
		if end < start || end > total {
			end = total
		}
		res.Assets = make([]models.Asset, 0, end-start)
		for k := start; k < end; k++ {
			i := k
			if order != nil {
				i = int64(order[k])
			}
			res.Assets = append(res.Assets, c.data[i])
		}
	} else {
		res.Assets = make([]models.Asset, 0, len(positions))
		for _, p := range positions {
			res.Assets = append(res.Assets, c.data[p])
		}
	}

	switch {
	case !wantTags:
	case !byLift:
		res.Tags = c.tagCounts
	default:
		terms := make([]models.Tag, 0, len(counts))
		for id, n := range counts {
			if n > 0 {
				terms = append(terms, models.Tag{Slug: e.tagSlugs[id], Count: int64(n)})
			}
		}
		res.Tags = c.rankLift(terms, opts.Tags, total)
	}
	return res, true, nil
}
