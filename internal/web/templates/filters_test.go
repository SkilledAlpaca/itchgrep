package templates

import (
	"itchgrep/pkg/models"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestShareURLEscapesTheQuery(t *testing.T) {
	// A search for "a&b" would otherwise truncate at the ampersand and produce
	// a link that searches for "a".
	f := Filters{Query: "a&b"}
	assert.Equal(t, "/?q=a%26b", f.ShareURL())
}

func TestShareURLOfNothingIsTheRoot(t *testing.T) {
	// Not "/?" - the front page must have exactly one address, or a shared
	// cache holds two copies of it.
	assert.Equal(t, "/", Filters{}.ShareURL())
}

func TestDefaultsAreOmittedFromTheURL(t *testing.T) {
	// Same page, same address. Encoding a sort that matches what the server
	// would have picked anyway doubles the cache keys for no behaviour change.
	assert.Equal(t, "/", Filters{Sort: models.SortPopular}.ShareURL())
	assert.Equal(t, "/?q=x", Filters{Query: "x", Sort: models.SortRelevance}.ShareURL())

	// A non-default sort does have to be carried.
	assert.Equal(t, "/?sort=title", Filters{Sort: models.SortTitle}.ShareURL())
}

func TestTagsAreSortedSoOneStateHasOneURL(t *testing.T) {
	// Arriving at {2d, pixel-art} by clicking them in either order must produce
	// the same URL, or the edge caches the same result set twice.
	a := Filters{}.WithTag("pixel-art").WithTag("2d")
	b := Filters{}.WithTag("2d").WithTag("pixel-art")

	assert.Equal(t, []string{"2d", "pixel-art"}, a.Tags)
	assert.Equal(t, a.ShareURL(), b.ShareURL())
	assert.Equal(t, "/?tags=2d%2Cpixel-art", a.ShareURL())
}

func TestToggleTagAddsThenRemoves(t *testing.T) {
	f := Filters{}.ToggleTag("fonts")
	assert.True(t, f.HasTag("fonts"))
	assert.False(t, f.ToggleTag("fonts").HasTag("fonts"))
}

func TestTagCountIsBounded(t *testing.T) {
	// Each tag is another conjunct in the query, so a hand-written URL with
	// fifty of them must not become a fifty-way intersection.
	f := Filters{}
	for _, tag := range []string{"a", "b", "c", "d", "e", "f", "g", "h", "i", "j"} {
		f = f.WithTag(tag)
	}
	assert.Len(t, f.Tags, MaxTags)
}

func TestFiltersAreImmutable(t *testing.T) {
	// Templates build many variant links off one Filters - a chip per tag, each
	// "current state minus this one" - so a With* that mutated in place would
	// have every link on the page describing a different state than intended.
	base := Filters{}.WithTag("2d").WithTag("fonts")
	_ = base.WithoutTag("2d")
	_ = base.WithTag("rpg")
	_ = base.WithPrice(models.PricingFree)

	assert.Equal(t, []string{"2d", "fonts"}, base.Tags)
	assert.Empty(t, base.Price)
}

func TestPriceIsAToggleNotARadioGroup(t *testing.T) {
	// Clicking "Free" a second time means "stop filtering by price". A radio
	// group would leave no way back to showing everything.
	free := Filters{}.WithPrice(models.PricingFree)
	assert.Equal(t, models.PricingFree, free.Price)
	assert.Empty(t, free.WithPrice(models.PricingFree).Price)
	assert.Equal(t, models.PricingPaid, free.WithPrice(models.PricingPaid).Price)
}

func TestClearedKeepsTheQuery(t *testing.T) {
	// "Clear filters" sits next to the results of a search. Dropping the search
	// itself would empty the page rather than widen it.
	f := Filters{Query: "tileset", Price: models.PricingFree}.WithTag("2d")
	cleared := f.Cleared()

	assert.Equal(t, "tileset", cleared.Query)
	assert.Empty(t, cleared.Tags)
	assert.Empty(t, cleared.Price)
}

func TestFragmentURLCarriesThePage(t *testing.T) {
	f := Filters{Query: "pixel art"}.WithTag("2d")
	assert.Equal(t, "/results?page=3&q=pixel+art&tags=2d", f.FragmentURL(3))
}

func TestResolvedSortDependsOnWhetherAnythingWasSearchedFor(t *testing.T) {
	// With no query every document scores the same, so "best match" would be an
	// arbitrary order presented as a ranking.
	assert.Equal(t, models.SortPopular, Filters{}.ResolvedSort())
	assert.Equal(t, models.SortRelevance, Filters{Query: "x"}.ResolvedSort())
	assert.Equal(t, models.SortTitle, Filters{Sort: models.SortTitle}.ResolvedSort())
}

func TestFormatCountGroupsThousands(t *testing.T) {
	assert.Equal(t, "0", formatCount(0))
	assert.Equal(t, "999", formatCount(999))
	assert.Equal(t, "1,000", formatCount(1000))
	assert.Equal(t, "17,488", formatCount(17488))
	assert.Equal(t, "108,697", formatCount(108697))
}
