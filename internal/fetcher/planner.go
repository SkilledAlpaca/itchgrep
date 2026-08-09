package fetcher

import (
	"fmt"
	"itchgrep/pkg/models"
	"sort"
	"strings"
)

// MaxPagesPerView is the hard pagination cap itch.io applies to every browse
// view. Page 200 serves data; page 202 returns 404. The total asset count
// implies ~3000 pages, so deriving a page count from it alone produces ~2800
// requests that can only 404.
const MaxPagesPerView = 200

// Sort orders. The zero value (SortDefault) is the ordering itch.io serves
// when no sort segment is present, and is distinct from all three named ones.
const (
	SortDefault       = ""
	SortNewest        = "newest"
	SortNewAndPopular = "new-and-popular"
	SortTopRated      = "top-rated"
)

// allSorts is every ordering a single-tag or root view can be fetched under.
// Each is a different window onto the same result set, so a tag too large to
// page through under one ordering yields different assets under another.
var allSorts = []string{SortDefault, SortNewest, SortNewAndPopular, SortTopRated}

// Filter segments. These occupy the same slot as a sort but are not orderings:
// they narrow the result set, so each one is a genuinely different set of
// assets rather than a different window onto the same set.
const (
	FilterNone = ""

	// FilterFree and FilterPaid partition the catalogue. Measured 2026-08-09:
	// free 53,021 + store 55,907 = 108,928 against a catalogue of 108,585, so
	// every asset is in exactly one. This is the only true partition itch.io
	// exposes - tag facets are a set cover, with no "NOT tag-X" to split on -
	// which makes it the most valuable filter by a wide margin: it gives every
	// oversized tag two disjoint windows instead of one.
	//
	// /game-assets/paid 301s to /game-assets/store, so store is the canonical
	// spelling of "not free".
	FilterFree = "free"
	FilterPaid = "store"

	// FilterRecent surfaces newly published assets that popularity-ordered
	// views bury. It shifts daily, so unlike the others it is not stable
	// between runs.
	FilterRecent = "last-30-days"
)

// allFilters is every filter applied to oversized tags.
//
// Deliberately excluded as volatile: on-sale, top-sellers, 5-dollars-or-less
// and 15-dollars-or-less all track pricing that changes underneath us, so
// slices built on them would not be reproducible between runs.
//
// There is no price ordering to add here. itch.io ignores ?sort= entirely -
// ?sort=price and ?sort=newest both return byte-identical results to the
// default - and the path-segment sorts are newest, top-rated and
// new-and-popular only.
//
// The genre entries are each a small, stable subset - measured against tag-2d:
// action 1,741, adventure 4,640, shooter 658, puzzle 472, against the tag's own
// 40,272. Being under the 7,200 ceiling is exactly what makes them valuable:
// such a view can be paged to its end, so it reaches assets sitting far below
// the depth any window on the parent tag can show.
//
// genre-survival, genre-fighting, genre-racing and genre-card-game all 301:
// they exist for games but not for assets.
var allFilters = []string{
	FilterFree,
	FilterPaid,
	FilterRecent,
	"genre-platformer",
	"genre-rpg",
	"genre-action",
	"genre-adventure",
	"genre-shooter",
	"genre-puzzle",
}

// Slice is one browsable view of the catalogue: a URL that can be paged
// through up to MaxPagesPerView times.
//
// The URL grammar itch.io accepts is narrow, and violating it fails loudly:
//
//	/game-assets                    root
//	/game-assets/<sort>             sort only
//	/game-assets/tag-A              one tag
//	/game-assets/<sort>/tag-A       sort plus one tag
//	/game-assets/tag-A/tag-B        two tags, default sort only, A < B
//	/game-assets/<filter>/tag-A     filter plus one tag
//
// Three tags is a 403. A sort together with two tags is a 403. A sort together
// with a filter is a 403. A filter with two tags is a 403. The filter segment
// must precede the tag - /game-assets/tag-pixel-art/free is a 301 - and tags
// out of lexicographic order is a 301 too. There is no deeper subdivision
// available, which is why PlanSlices cannot simply recurse until every view
// fits.
type Slice struct {
	Sort   string   // SortDefault, or one of the named orderings
	Filter string   // FilterNone, or one of allFilters; never set alongside Sort
	Tags   []string // canonical (lexicographic) order; empty means the root view
	Count  int64    // reported result count, for ordering the crawl

	// PageInFull marks a view that must be paged to its end rather than
	// abandoned once its yield drops off.
	//
	// This distinction is the whole ballgame for coverage. Browse results are
	// popularity-ordered, so the first pages of EVERY view are the assets
	// already collected and the yield heuristic reads near-zero on all of them.
	// Applied to a view reaching assets nothing else reaches, that abandons
	// precisely the deep, unpopular tail - which is how small tags came to be
	// sampled at 5% of their catalogue count while the crawl called itself
	// healthy.
	//
	// Set for the two view shapes that pay for the pages they cost:
	//
	//   - a tag under the ceiling, whose view therefore contains every asset
	//     carrying it. This is the plan's entire premise.
	//   - a filter applied to an oversized tag, which either narrows it under
	//     the ceiling (the genre filters do: action/2d is 1,741 of 40,272) or
	//     splits it into halves each far deeper than any ordering reaches.
	//
	// Not set for re-orderings, tag pairs or untagged filter views. Those are
	// windows onto sets already covered elsewhere, they cannot be exhausted
	// anyway, and left uncapped they would each run the full 200 pages - about
	// 50,000 pages between them.
	PageInFull bool
}

// Valid reports whether this slice satisfies the URL grammar above. A slice
// that fails this is a planner bug: itch.io answers it with 403 or 301, not
// with data.
func (s Slice) Valid() bool {
	if len(s.Tags) > 2 {
		return false
	}
	if len(s.Tags) == 2 && s.Sort != SortDefault {
		return false
	}
	if len(s.Tags) == 2 && s.Tags[0] >= s.Tags[1] {
		return false
	}
	if s.Filter != FilterNone {
		// A filter occupies the same segment slot as a sort, and cannot share
		// the view with a second tag.
		if s.Sort != SortDefault || len(s.Tags) > 1 {
			return false
		}
		known := false
		for _, f := range allFilters {
			if f == s.Filter {
				known = true
				break
			}
		}
		if !known {
			return false
		}
	}
	switch s.Sort {
	case SortDefault, SortNewest, SortNewAndPopular, SortTopRated:
		return true
	default:
		return false
	}
}

// Path returns the URL path for this slice, in canonical form.
func (s Slice) Path() string {
	parts := []string{"/game-assets"}
	if s.Sort != SortDefault {
		parts = append(parts, s.Sort)
	}
	// The filter precedes the tag; the reverse ordering is a 301.
	if s.Filter != FilterNone {
		parts = append(parts, s.Filter)
	}
	for _, t := range s.Tags {
		parts = append(parts, "tag-"+t)
	}
	return strings.Join(parts, "/")
}

// baseURL is the origin every browse request is made against. A package-level
// var so tests can point it at an httptest.Server.
var baseURL = "https://itch.io"

// PageURL returns the JSON endpoint for one page of this slice.
func (s Slice) PageURL(pageNum int64) string {
	return fmt.Sprintf("%s%s?page=%d&format=json", baseURL, s.Path(), pageNum)
}

// IsRoot reports whether this is the unfiltered, unsorted, untagged view.
//
// It has to test all three fields. A filter-only slice such as /game-assets/free
// is also sortless and tagless, and treating one as the root would be wrong in
// two separate ways: its page numbers would be taken as a global popularity
// rank (they rank only within the filtered subset), and it would be exempted
// from early abandonment, costing 200 pages of mostly-duplicate results.
func (s Slice) IsRoot() bool {
	return s.Sort == SortDefault && s.Filter == FilterNone && len(s.Tags) == 0
}

// Label is a short human-readable name for logs, and the key under which a
// finished slice is recorded in a crawl checkpoint - so it must be unique per
// slice, or resuming skips views that were never crawled.
func (s Slice) Label() string {
	if s.IsRoot() {
		return "root"
	}
	return strings.TrimPrefix(s.Path(), "/game-assets/")
}

// PagesToFetch is how many pages of this slice are worth requesting: the
// number its count implies, capped at the view limit.
func (s Slice) PagesToFetch(itemsPerPage int64) int64 {
	if itemsPerPage <= 0 {
		return 0
	}
	pages := (s.Count + itemsPerPage - 1) / itemsPerPage
	if pages > MaxPagesPerView {
		return MaxPagesPerView
	}
	if pages < 1 {
		return 1
	}
	return pages
}

// PlanSlices turns a tag universe into the list of views to crawl.
//
// The plan rests on one observation: an asset is fully reachable if it carries
// at least one tag whose total count fits within a single view. Assets carry
// roughly nine tags each and most tags are small, so a slice per small tag
// covers the large majority of the catalogue. What remains - assets whose
// every tag is too big to page through - is reached by pairing big tags with
// each other.
//
// Pure function: no I/O, no network. All the crawl's cost is decided here.
func PlanSlices(tags []models.Tag, itemsPerPage int64) []Slice {
	ceiling := MaxPagesPerView * itemsPerPage

	var small, big []models.Tag
	for _, t := range tags {
		if t.Slug == "" {
			continue
		}
		if t.Count > ceiling {
			big = append(big, t)
		} else {
			small = append(small, t)
		}
	}

	// Deterministic order regardless of discovery order, so a re-plan of the
	// same tag set produces the same crawl.
	sort.Slice(big, func(i, j int) bool { return big[i].Slug < big[j].Slug })
	sort.Slice(small, func(i, j int) bool { return small[i].Slug < small[j].Slug })

	var out []Slice

	// Filter-only views, carrying no tag at all. Each shows the most popular
	// 7,200 of a set spanning tens of thousands, and the popular end is what
	// every other view has already collected, so these are sampled rather than
	// paged in full. Measured across two full
	// crawls, the tag slices below plateau at 79.5% of the catalogue, and
	// applying free/store - a true partition - to every oversized tag moved
	// that by 0.2 points. So the residual is not "assets whose every tag is too
	// big to page through"; it is assets no tag view reaches at all, because
	// they carry no tag in the discovered vocabulary.
	//
	// The root view is the only other untagged view, and it stops at 7,200.
	// These are the only remaining place such an asset can surface.
	for _, f := range allFilters {
		out = append(out, Slice{Filter: f, Count: ceiling})
	}

	// A small tag fits in one view, so one slice covers it completely - and
	// that is the entire premise the plan rests on. Always PageInFull: paging
	// one to its end is bounded work with guaranteed full coverage of its slice,
	// and it is the only mechanism that reaches an unpopular asset at all.
	for _, t := range small {
		out = append(out, Slice{Tags: []string{t.Slug}, Count: t.Count, PageInFull: true})
	}

	// A big tag cannot be paged through, but each ordering exposes a different
	// window onto it, so take all four. Re-orderings of one result set overlap
	// heavily by construction, so a tail that has stopped yielding really has
	// stopped yielding - these are left abandonable.
	for _, t := range big {
		for _, srt := range allSorts {
			out = append(out, Slice{Sort: srt, Tags: []string{t.Slug}, Count: t.Count})
		}
		// Filters go further than orderings: they narrow the result set rather
		// than reshuffling it, so free/tag-X and store/tag-X are disjoint and
		// between them contain every asset carrying the tag. A big tag that no
		// ordering can exhaust is often fully covered by that pair alone.
		//
		// Count is the tag's total, which is only an upper bound on any filtered
		// subset. That is deliberate: it costs one 404 to discover the real end
		// of a shorter view, which FetchExhausted then handles, and it avoids a
		// request per filter per tag just to size them up front.
		for _, f := range allFilters {
			out = append(out, Slice{Filter: f, Tags: []string{t.Slug}, Count: t.Count, PageInFull: true})
		}
	}

	// The residual: assets every one of whose tags is big. Pairing two big
	// tags narrows the result set enough to page through. Pairs involving a
	// small tag are deliberately not generated - those assets are already
	// covered above, and including them would turn a few hundred slices into
	// hundreds of thousands.
	for i := 0; i < len(big); i++ {
		for j := i + 1; j < len(big); j++ {
			a, b := big[i].Slug, big[j].Slug
			if a > b {
				a, b = b, a
			}
			// The true intersection size is unknown without asking itch.io.
			// The smaller of the two is its upper bound, which is all the
			// ordering below needs.
			count := big[i].Count
			if big[j].Count < count {
				count = big[j].Count
			}
			out = append(out, Slice{Tags: []string{a, b}, Count: count})
		}
	}

	// Largest first, so the coverage target is approached as fast as possible
	// and the cheap tail is what gets dropped when the crawl stops early.
	sort.SliceStable(out, func(i, j int) bool { return out[i].Count > out[j].Count })

	// The root view leads, unconditionally: it is the only view whose page
	// numbers are a global popularity rank (see InvPopularity handling in the
	// dataservice), so it has to be crawled before anything else assigns one.
	//
	// Its Count is the ceiling rather than 0. The root view spans the whole
	// catalogue, so it is worth paging to the cap - and a Count of 0 makes
	// PagesToFetch round down to a single page, which silently reduces the
	// global popularity ranking to one page's worth and leaves every other
	// asset ranked only within whichever slice happened to find it first.
	return append([]Slice{{Count: ceiling}}, out...)
}
