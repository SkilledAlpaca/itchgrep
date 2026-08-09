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

func TestPlanSlicesOrdersRemainderLargestFirst(t *testing.T) {
	tags := []models.Tag{
		{Slug: "small", Count: 100},
		{Slug: "medium", Count: 5000},
		{Slug: "large", Count: 7000},
	}

	got := PlanSlices(tags, testItemsPerPage)
	require.Len(t, got, 1+len(allFilters)+3) // root + filter-only views + three small tags

	rest := got[1:]
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

	// 5 tags x (4 sorts + every filter) + C(5,2) pairs + filter-only views + root
	assert.Len(t, got, 5*(4+len(allFilters))+10+len(allFilters)+1)
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
