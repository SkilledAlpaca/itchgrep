package fetcher

import (
	"itchgrep/pkg/models"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// itemsPerPage as itch.io actually serves it, so ceiling == 7200.
const testItemsPerPage = 36

const testCeiling = MaxPagesPerView * testItemsPerPage

func smallTag(slug string) models.Tag { return models.Tag{Slug: slug, Count: 1000} }
func bigTag(slug string) models.Tag   { return models.Tag{Slug: slug, Count: testCeiling + 1} }

// countSlices counts slices matching a predicate.
func countSlices(slices []Slice, pred func(Slice) bool) int {
	n := 0
	for _, s := range slices {
		if pred(s) {
			n++
		}
	}
	return n
}

func TestPlanSlicesKeepsASmallTagAsOneSlice(t *testing.T) {
	got := PlanSlices([]models.Tag{smallTag("fonts")}, testItemsPerPage)

	n := countSlices(got, func(s Slice) bool {
		return len(s.Tags) == 1 && s.Tags[0] == "fonts"
	})
	assert.Equal(t, 1, n, "a tag that fits in one view needs exactly one slice")
}

func TestPlanSlicesExpandsABigTagIntoFourSortVariants(t *testing.T) {
	got := PlanSlices([]models.Tag{bigTag("pixel-art")}, testItemsPerPage)

	sorts := map[string]bool{}
	for _, s := range got {
		if len(s.Tags) == 1 && s.Tags[0] == "pixel-art" {
			sorts[s.Sort] = true
		}
	}
	assert.Equal(t,
		map[string]bool{SortDefault: true, SortNewest: true, SortNewAndPopular: true, SortTopRated: true},
		sorts,
		"a tag too large to page through must be taken under every ordering")
}

func TestEveryPlannedSliceSatisfiesTheURLGrammar(t *testing.T) {
	// This is the constraint that fails as a 403 in production rather than as
	// a test failure, so it is asserted over the entire output.
	tags := []models.Tag{
		bigTag("pixel-art"), bigTag("sprites"), bigTag("characters"), bigTag("2d"),
		smallTag("fonts"), smallTag("icons"), smallTag("8x8"),
	}

	got := PlanSlices(tags, testItemsPerPage)
	require.NotEmpty(t, got)

	for _, s := range got {
		assert.True(t, s.Valid(), "slice %q violates the browse URL grammar", s.Path())
		assert.LessOrEqual(t, len(s.Tags), 2, "three tags is a 403: %q", s.Path())
		if len(s.Tags) == 2 {
			assert.Equal(t, SortDefault, s.Sort, "sort with two tags is a 403: %q", s.Path())
			assert.Less(t, s.Tags[0], s.Tags[1], "non-lexicographic tags 301: %q", s.Path())
		}
	}
}

func TestPlanSlicesPairsOnlyBigTags(t *testing.T) {
	tags := []models.Tag{bigTag("pixel-art"), bigTag("sprites"), smallTag("fonts")}
	got := PlanSlices(tags, testItemsPerPage)

	pairs := 0
	for _, s := range got {
		if len(s.Tags) != 2 {
			continue
		}
		pairs++
		assert.NotContains(t, s.Tags, "fonts",
			"a small tag is already fully covered by its own slice; pairing it explodes the plan")
	}
	assert.Equal(t, 1, pairs, "two big tags produce exactly one pair")
}

func TestPlanSlicesLeadsWithTheRootView(t *testing.T) {
	got := PlanSlices([]models.Tag{bigTag("pixel-art"), smallTag("fonts")}, testItemsPerPage)

	require.NotEmpty(t, got)
	assert.Empty(t, got[0].Tags, "the root view must be crawled first")
	assert.Equal(t, SortDefault, got[0].Sort)
	assert.Equal(t, "root", got[0].Label())
}

func TestPlanSlicesIncludesTheOnlyViewThatCanRankRecency(t *testing.T) {
	// Slice.IsNewestRoot is true for exactly one view, and the crawl records a
	// recency rank only from that view. Unplanned, every asset keeps InvRecency
	// 0, the webserver reads the dataset as unrankable and hides the "recently
	// added" ordering - a whole feature absent with nothing reporting a fault.
	got := PlanSlices([]models.Tag{bigTag("pixel-art"), smallTag("fonts")}, testItemsPerPage)

	newest := 0
	for _, s := range got {
		if s.IsNewestRoot() {
			newest++
			assert.True(t, s.PageInFull,
				"its first pages are assets the tag views already hold, so the yield heuristic would abandon it before it ranked anything")
			assert.EqualValues(t, MaxPagesPerView, s.PagesToFetch(testItemsPerPage),
				"the whole point is depth: a short count would rank only the first page")
		}
	}
	assert.Equal(t, 1, newest, "exactly one untagged newest view")
}

func TestPlanSlicesOrdersRemainderLargestFirst(t *testing.T) {
	tags := []models.Tag{
		{Slug: "small", Count: 100},
		{Slug: "medium", Count: 5000},
		{Slug: "large", Count: 7000},
	}

	got := PlanSlices(tags, testItemsPerPage)
	require.Len(t, got, 2+len(allFilters)+3) // root + newest root + filter-only views + three small tags

	rest := got[2:]
	for i := 1; i < len(rest); i++ {
		assert.GreaterOrEqual(t, rest[i-1].Count, rest[i].Count,
			"slices must run largest-first so the coverage target is reached soonest")
	}
}

func TestPlanSlicesTerminatesWhenEveryTagIsBig(t *testing.T) {
	var tags []models.Tag
	for _, s := range []string{"a", "b", "c", "d", "e"} {
		tags = append(tags, bigTag(s))
	}

	got := PlanSlices(tags, testItemsPerPage)

	// 5 tags x (4 sorts + every filter) + C(5,2) pairs + filter-only views
	// + root + newest root
	assert.Len(t, got, 5*(4+len(allFilters))+10+len(allFilters)+2)
}

func TestPlanSlicesIgnoresEmptySlugs(t *testing.T) {
	got := PlanSlices([]models.Tag{{Slug: "", Count: 500}, smallTag("fonts")}, testItemsPerPage)

	for _, s := range got {
		assert.NotContains(t, s.Tags, "", "an empty slug would build /game-assets/tag-")
	}
}

func TestSlicePath(t *testing.T) {
	tests := []struct {
		name string
		s    Slice
		want string
	}{
		{"root", Slice{}, "/game-assets"},
		{"sort only", Slice{Sort: SortNewest}, "/game-assets/newest"},
		{"one tag", Slice{Tags: []string{"pixel-art"}}, "/game-assets/tag-pixel-art"},
		{"sort and tag", Slice{Sort: SortTopRated, Tags: []string{"pixel-art"}}, "/game-assets/top-rated/tag-pixel-art"},
		{"two tags", Slice{Tags: []string{"16x16", "pixel-art"}}, "/game-assets/tag-16x16/tag-pixel-art"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, tt.s.Path())
		})
	}
}

func TestSliceValidRejectsWhatItchIoRefuses(t *testing.T) {
	tests := []struct {
		name string
		s    Slice
	}{
		{"three tags (403)", Slice{Tags: []string{"a", "b", "c"}}},
		{"sort with two tags (403)", Slice{Sort: SortNewest, Tags: []string{"a", "b"}}},
		{"tags out of order (301)", Slice{Tags: []string{"pixel-art", "16x16"}}},
		{"duplicate tags", Slice{Tags: []string{"a", "a"}}},
		{"unknown sort", Slice{Sort: "most-downloaded"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.False(t, tt.s.Valid())
		})
	}
}

func TestPagesToFetchCapsAtTheViewLimit(t *testing.T) {
	// The whole catalogue implies ~3017 pages, but no view serves past 200.
	// Trusting the count produced ~2800 requests that could only 404.
	whole := Slice{Count: 108578}
	assert.EqualValues(t, MaxPagesPerView, whole.PagesToFetch(testItemsPerPage))

	assert.EqualValues(t, 28, Slice{Count: 1000}.PagesToFetch(testItemsPerPage))
	assert.EqualValues(t, 1, Slice{Count: 0}.PagesToFetch(testItemsPerPage))
	assert.EqualValues(t, 0, Slice{Count: 1000}.PagesToFetch(0), "guard against divide-by-zero")
}

func TestFilterSlicePathPutsFilterBeforeTag(t *testing.T) {
	// /game-assets/tag-pixel-art/free is a 301; the filter must lead.
	s := Slice{Filter: FilterFree, Tags: []string{"pixel-art"}}
	assert.Equal(t, "/game-assets/free/tag-pixel-art", s.Path())
	assert.True(t, s.Valid())
}

func TestFilterIsInvalidAlongsideASortOrASecondTag(t *testing.T) {
	// Both of these are 403s from itch.io, so emitting one is a planner bug.
	assert.False(t, Slice{Sort: SortNewest, Filter: FilterFree, Tags: []string{"a"}}.Valid(),
		"sort plus filter is a 403")
	assert.False(t, Slice{Filter: FilterFree, Tags: []string{"a", "b"}}.Valid(),
		"filter plus two tags is a 403")
	assert.False(t, Slice{Filter: "on-sale", Tags: []string{"a"}}.Valid(),
		"filters outside allFilters are deliberately not crawlable")
}

func TestPlanSlicesGivesBigTagsBothHalvesOfThePricePartition(t *testing.T) {
	// free and store partition the catalogue, so together they contain every
	// asset carrying an oversized tag - the one place the 200-page cap is
	// genuinely escapable.
	tags := []models.Tag{{Slug: "pixel-art", Count: 36324}}
	out := PlanSlices(tags, 36)

	var free, paid bool
	for _, s := range out {
		if len(s.Tags) == 1 && s.Tags[0] == "pixel-art" {
			switch s.Filter {
			case FilterFree:
				free = true
			case FilterPaid:
				paid = true
			}
		}
	}
	assert.True(t, free, "big tags must be crawled under the free filter")
	assert.True(t, paid, "big tags must be crawled under the paid filter")
}

func TestPlanSlicesDoesNotFilterSmallTags(t *testing.T) {
	// A small tag is already fully pageable, so filtering it only re-fetches
	// assets already collected.
	out := PlanSlices([]models.Tag{{Slug: "isometric", Count: 1549}}, 36)
	for _, s := range out {
		if len(s.Tags) == 0 {
			continue // filter-only views carry no tag to over-fetch
		}
		assert.Equal(t, FilterNone, s.Filter,
			"slice %q filters a tag that needs no filtering", s.Label())
	}
}

func TestPlanSlicesIncludesUntaggedFilterViews(t *testing.T) {
	// The one class of view that can reach an asset carrying no tag we know
	// about. Without these the plan has exactly one untagged view - the root -
	// and it stops at 7,200 assets.
	got := PlanSlices([]models.Tag{smallTag("fonts")}, testItemsPerPage)

	seen := map[string]bool{}
	for _, s := range got {
		if len(s.Tags) == 0 && s.Filter != FilterNone {
			seen[s.Filter] = true
			assert.True(t, s.Valid(), "filter-only slice %q is not a valid URL", s.Path())
			assert.Equal(t, int64(MaxPagesPerView), s.PagesToFetch(testItemsPerPage),
				"a filter-only view must page to the cap")
		}
	}
	for _, f := range allFilters {
		assert.True(t, seen[f], "filter %q has no untagged view in the plan", f)
	}
}

func TestFilterOnlySlicesAreNotMistakenForTheRoot(t *testing.T) {
	// Two distinct failures ride on this. Label() is the checkpoint key, so a
	// collision makes a resumed crawl skip views it never crawled; and IsRoot
	// drives both global popularity ranking and the exemption from early
	// abandonment in the dataservice.
	free := Slice{Filter: FilterFree}
	root := Slice{}

	assert.True(t, root.IsRoot())
	assert.False(t, free.IsRoot(), "a filter-only view ranks only within its own subset")
	assert.Equal(t, "root", root.Label())
	assert.Equal(t, "free", free.Label())

	labels := map[string]bool{}
	for _, s := range PlanSlices([]models.Tag{bigTag("pixel-art"), smallTag("fonts")}, testItemsPerPage) {
		assert.False(t, labels[s.Label()], "duplicate slice label %q collides in the checkpoint", s.Label())
		labels[s.Label()] = true
	}
}

func TestEveryPlannedSliceIsValid(t *testing.T) {
	// Asserted over the whole output, not a sample: an invalid slice is a 403
	// at crawl time, and the filter dimension multiplies the ways to get it
	// wrong.
	tags := []models.Tag{
		{Slug: "pixel-art", Count: 36324},
		{Slug: "sprites", Count: 15486},
		{Slug: "2d", Count: 40389},
		{Slug: "isometric", Count: 1549},
		{Slug: "fonts", Count: 405},
	}
	for _, s := range PlanSlices(tags, 36) {
		assert.True(t, s.Valid(), "planner emitted an invalid slice: %q", s.Label())
	}
}

func TestRootSliceIsPagedToTheCap(t *testing.T) {
	// The root view is the only source of a global popularity rank. A Count of
	// 0 here rounds PagesToFetch down to a single page, which leaves
	// InvPopularity meaning "page within whichever slice found it first" for
	// the entire catalogue - a silent failure with no error anywhere.
	got := PlanSlices([]models.Tag{smallTag("fonts")}, testItemsPerPage)

	root := got[0]
	assert.Empty(t, root.Tags, "the root view must lead the plan")
	assert.Equal(t, int64(MaxPagesPerView), root.PagesToFetch(testItemsPerPage),
		"the root view must page to the cap, not to one page")
}

func TestSmallTagSlicesArePagedInFull(t *testing.T) {
	// The plan's entire premise is that a tag small enough to page through
	// gives complete coverage of the assets carrying it. Leaving one
	// abandonable lets the crawler cut it short, which forfeits exactly the
	// deep, unpopular assets no other view reaches. Measured before this was
	// fixed: tag-icons yielded 288 of its 5,867 assets.
	out := PlanSlices([]models.Tag{smallTag("icons"), smallTag("fonts")}, testItemsPerPage)

	seen := 0
	for _, s := range out {
		if len(s.Tags) == 1 && s.Count <= testCeiling {
			seen++
			assert.True(t, s.PageInFull,
				"small tag slice %q must be paged to its end", s.Label())
		}
	}
	assert.Equal(t, 2, seen)
}

func TestFilteredBigTagViewsArePagedInFull(t *testing.T) {
	// A filter either narrows an oversized tag under the ceiling or splits it
	// into halves each far deeper than any ordering reaches, so its tail is
	// where the otherwise-unreachable assets are. A re-ordering is a different
	// window onto a set already covered, so its tail really is spent.
	out := PlanSlices([]models.Tag{bigTag("pixel-art"), bigTag("sprites")}, testItemsPerPage)

	filtered, sorted := 0, 0
	for _, s := range out {
		switch {
		case s.Filter != FilterNone && len(s.Tags) > 0:
			filtered++
			assert.True(t, s.PageInFull, "filtered view %q reaches assets nothing else does", s.Label())
		case s.IsNewestRoot():
			// Exempt: it is planned to collect recency ranks rather than to
			// cover assets, and ranking only the first pages would be pointless.
		case s.Sort != SortDefault:
			sorted++
			assert.False(t, s.PageInFull, "sort view %q re-orders a covered set", s.Label())
		}
	}
	assert.Equal(t, 2*len(allFilters), filtered)
	assert.Equal(t, 2*3, sorted)
}

func TestExpensiveViewsStayAbandonable(t *testing.T) {
	// Untagged filter views and tag pairs cannot be exhausted and overlap
	// heavily with what is already collected. Paging all of them in full would
	// cost roughly 50,000 pages - about seven hours at the default rate - for
	// the +82 assets two full crawls measured from the filter views.
	out := PlanSlices([]models.Tag{bigTag("a"), bigTag("b"), smallTag("fonts")}, testItemsPerPage)

	filterOnly, pairs := 0, 0
	for _, s := range out {
		if len(s.Tags) == 0 && s.Filter != FilterNone {
			filterOnly++
			assert.False(t, s.PageInFull, "filter-only view %q must stay abandonable", s.Label())
		}
		if len(s.Tags) == 2 {
			pairs++
			assert.False(t, s.PageInFull, "tag pair %q must stay abandonable", s.Label())
		}
	}
	assert.Equal(t, len(allFilters), filterOnly)
	assert.Equal(t, 1, pairs)
}

func TestGenreFiltersAreCrawlable(t *testing.T) {
	// Verified against itch.io: these four return 200 with counts well under
	// the page ceiling, which is what makes them worth having. survival,
	// fighting, racing and card-game all 301 and must not be added.
	for _, f := range []string{"genre-action", "genre-adventure", "genre-shooter", "genre-puzzle"} {
		assert.True(t, Slice{Filter: f, Tags: []string{"2d"}}.Valid(),
			"%s should be an accepted filter", f)
	}
	for _, f := range []string{"genre-survival", "genre-fighting", "genre-racing", "genre-card-game"} {
		assert.False(t, Slice{Filter: f, Tags: []string{"2d"}}.Valid(),
			"%s 301s on itch.io and must not be planned", f)
	}
}

func TestOnlyTheUntaggedNewestViewRanksRecency(t *testing.T) {
	// A page number is a catalogue-wide recency position only in the view that
	// covers the whole catalogue. The same care IsRoot takes, for the same
	// reason: a filtered or tagged view ranks within its own subset.
	assert.True(t, Slice{Sort: SortNewest}.IsNewestRoot())

	assert.False(t, Slice{Sort: SortNewest, Tags: []string{"fonts"}}.IsNewestRoot())
	assert.False(t, Slice{Sort: SortNewest, Filter: FilterFree}.IsNewestRoot())
	assert.False(t, Slice{Sort: SortDefault}.IsNewestRoot(), "the root view ranks popularity, not recency")
	assert.False(t, Slice{Sort: SortTopRated}.IsNewestRoot())
}
