package cache

import (
	"testing"
	"time"

	"itchgrep/internal/storage"
	"itchgrep/pkg/models"
	"itchgrep/pkg/money"

	"github.com/blevesearch/bleve"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// These tests run against a real bleve index built with the same mapping the
// dataservice publishes. Nothing here would catch the failure that matters most
// - a filter silently matching everything because the field was analysed the
// wrong way - if the mapping were stubbed out.

func fixtures() []models.Asset {
	return []models.Asset{
		{GameId: "1", Title: "Pixel Art Tileset", Author: "ana", Description: "tiles",
			Tags: []string{"2d", "pixel-art", "tileset"}, InvPopularity: 1},
		{GameId: "2", Title: "Zebra Sprite Pack", Author: "bo", Description: "animals",
			Tags: []string{"2d", "sprites"}, Price: "$4.95", InvPopularity: 2, InvRecency: 7},
		{GameId: "3", Title: "Ambient Music Loops", Author: "cy", Description: "audio",
			Tags: []string{"music"}, Price: "9.97€", InvPopularity: 3},
		{GameId: "4", Title: "Pixel Art Icons", Author: "dee", Description: "icons",
			Tags: []string{"2d", "pixel-art", "icons"}, InvPopularity: 4, InvRecency: 2},
		{GameId: "5", Title: "Art Deco Fonts", Author: "el", Description: "typefaces",
			Tags: []string{"fonts"}, Price: "$1", InvPopularity: 5},
	}
}

// newLoadedCache builds an index over the fixtures in a temp directory and
// returns a cache serving it, in the same shape RefreshDataCache would leave.
func newLoadedCache(t *testing.T, pageSize int64) *Cache {
	t.Helper()
	t.Setenv("DATA_DIR", t.TempDir())

	assets := fixtures()
	// Written so the staleness check has a real timestamp to read rather than
	// logging a failed stat on every call.
	require.NoError(t, storage.PutAssets(assets))

	index, err := bleve.New(storage.IndexPath(), storage.IndexMapping())
	require.NoError(t, err)
	t.Cleanup(func() { index.Close() })

	batch := index.NewBatch()
	for _, a := range assets {
		require.NoError(t, batch.Index(a.GameId, models.NewIndexedAsset(a, priceUSD(a))))
	}
	require.NoError(t, index.Batch(batch))

	c := NewCache(pageSize)
	c.index = index
	c.data = assets
	c.dataMap = make(map[string]models.Asset, len(assets))
	for _, a := range assets {
		c.dataMap[a.GameId] = a
	}
	c.tagCounts = countTags(assets)
	c.rates = storedRates
	c.hasRecency = true
	// Ahead of what is on disk, so the cache is never judged stale and never
	// reloads itself mid-test - which would quietly undo the index a test
	// deliberately removed.
	c.dataUpdatedTime = time.Now().Add(time.Hour)
	return c
}

// priceUSD mirrors what the dataservice bakes into a document, so the fixtures
// filter and sort by price exactly as published data does.
func priceUSD(a models.Asset) float64 {
	if a.Free() {
		return 0
	}
	m, ok := money.Parse(a.Price)
	if !ok {
		return models.UnknownPrice
	}
	usd, ok := storedRates.USD(m)
	if !ok {
		return models.UnknownPrice
	}
	return usd
}

var storedRates = money.Fallback()

func ids(r Results) []string {
	out := make([]string, 0, len(r.Assets))
	for _, a := range r.Assets {
		out = append(out, a.GameId)
	}
	return out
}

func TestTagFilterIsExactNotAnalysed(t *testing.T) {
	// The failure this guards against is silent: with the default analyser
	// "pixel-art" indexes as the two terms "pixel" and "art", so filtering by
	// the pixel-art tag would also return the asset tagged only "fonts" whose
	// title happens to be "Art Deco Fonts".
	c := newLoadedCache(t, 36)

	got, err := c.Find(SearchOptions{Tags: []string{"pixel-art"}, Page: 1})
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"1", "4"}, ids(got))
}

func TestMultipleTagsNarrowRatherThanWiden(t *testing.T) {
	// AND, not OR. A filter that grows the result set as you add to it reads as
	// broken, and it is the reason to pick a second tag in the first place.
	c := newLoadedCache(t, 36)

	one, err := c.Find(SearchOptions{Tags: []string{"2d"}, Page: 1})
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"1", "2", "4"}, ids(one))

	two, err := c.Find(SearchOptions{Tags: []string{"2d", "icons"}, Page: 1})
	require.NoError(t, err)
	assert.Equal(t, []string{"4"}, ids(two))

	// An intersection nothing satisfies is empty, not everything.
	none, err := c.Find(SearchOptions{Tags: []string{"fonts", "music"}, Page: 1})
	require.NoError(t, err)
	assert.Empty(t, ids(none))
}

func TestPriceFilterSplitsTheCatalogue(t *testing.T) {
	c := newLoadedCache(t, 36)

	free, err := c.Find(SearchOptions{Price: models.PricingFree, Page: 1})
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"1", "4"}, ids(free))

	paid, err := c.Find(SearchOptions{Price: models.PricingPaid, Page: 1})
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"2", "3", "5"}, ids(paid))

	// The two halves must account for everything: an asset in neither would be
	// unreachable through the toggle in either position.
	assert.Equal(t, int64(len(fixtures())), free.Total+paid.Total)
}

func TestFiltersCombineWithASearch(t *testing.T) {
	// A price filter must decide membership, not merely boost. Both fixtures
	// match "pixel art"; only one of them is also tagged icons.
	c := newLoadedCache(t, 36)

	got, err := c.Find(SearchOptions{Query: "pixel art", Tags: []string{"icons"}, Page: 1})
	require.NoError(t, err)
	assert.Equal(t, []string{"4"}, ids(got))
}

func TestSortOrders(t *testing.T) {
	c := newLoadedCache(t, 36)

	popular, err := c.Find(SearchOptions{Price: models.PricingPaid, Sort: models.SortPopular, Page: 1})
	require.NoError(t, err)
	assert.Equal(t, []string{"2", "3", "5"}, ids(popular), "popularity is InvPopularity ascending")

	byTitle, err := c.Find(SearchOptions{Price: models.PricingPaid, Sort: models.SortTitle, Page: 1})
	require.NoError(t, err)
	// Ambient, Art Deco, Zebra. Sorted on the analysed Title field instead,
	// "Art Deco Fonts" would lead on the term "art".
	assert.Equal(t, []string{"3", "5", "2"}, ids(byTitle),
		"A-Z must order by the whole title, not by its first analysed term")
}

func TestRelevanceWithoutAQueryFallsBackToPopularity(t *testing.T) {
	// Every document scores identically against no query, so "best match" would
	// otherwise present index order as a ranking.
	c := newLoadedCache(t, 36)

	got, err := c.Find(SearchOptions{Sort: models.SortRelevance, Page: 1})
	require.NoError(t, err)
	assert.Equal(t, []string{"1", "2", "3", "4", "5"}, ids(got))
}

func TestUnfilteredBrowseSkipsTheIndexEntirely(t *testing.T) {
	// The front page is the most-requested view there is, and it is already
	// sitting sorted in memory. Proven by removing the index: if the browse
	// path touched it, this would fail rather than serve.
	c := newLoadedCache(t, 2)
	c.index = nil

	got, err := c.Find(SearchOptions{Page: 2})
	require.NoError(t, err)
	assert.Equal(t, []string{"3", "4"}, ids(got))
	assert.Equal(t, int64(5), got.Total)
	assert.NotEmpty(t, got.Tags, "the sidebar still needs its counts")
}

func TestFacetsCountTheWholeMatchSetNotThePage(t *testing.T) {
	// One asset per page, but the sidebar has to describe all three matches or
	// it is a description of what is on screen rather than a map of what is not.
	c := newLoadedCache(t, 1)

	got, err := c.Find(SearchOptions{Tags: []string{"2d"}, Page: 1})
	require.NoError(t, err)
	require.Len(t, got.Assets, 1)

	counts := map[string]int64{}
	for _, tag := range got.Tags {
		counts[tag.Slug] = tag.Count
	}
	assert.Equal(t, int64(2), counts["pixel-art"])
	assert.Equal(t, int64(1), counts["sprites"])
	assert.NotContains(t, counts, "2d", "an applied tag is shown as a chip, not offered again")
}

func TestGlobalTagCountsAreOrderedAndStable(t *testing.T) {
	got := countTags(fixtures())
	require.NotEmpty(t, got)
	assert.Equal(t, models.Tag{Slug: "2d", Count: 3}, got[0])
	for i := 1; i < len(got); i++ {
		assert.LessOrEqual(t, got[i].Count, got[i-1].Count, "counts must descend")
	}
}

func TestPagingIsBounded(t *testing.T) {
	c := newLoadedCache(t, 2)

	_, err := c.Find(SearchOptions{Page: 0})
	assert.Error(t, err, "page numbering starts at 1")

	// Past the end is an empty page rather than an error: that is how infinite
	// scroll knows to stop.
	past, err := c.Find(SearchOptions{Page: 99})
	require.NoError(t, err)
	assert.Empty(t, past.Assets)
}

func TestExcludedTagsRemoveRatherThanNarrow(t *testing.T) {
	// The point of an exclusion is to say what you do not want without having
	// to name what you do: "2d, but not pixel-art" has no positive phrasing.
	c := newLoadedCache(t, 36)

	got, err := c.Find(SearchOptions{Tags: []string{"2d"}, ExcludeTags: []string{"pixel-art"}, Page: 1})
	require.NoError(t, err)
	assert.Equal(t, []string{"2"}, ids(got))
}

func TestAnExclusionAloneStillMatchesEverythingElse(t *testing.T) {
	// A boolean query with only negative clauses matches nothing unless
	// something positive is asserted first. Without the explicit match-all,
	// excluding one tag would empty the catalogue instead of trimming it.
	c := newLoadedCache(t, 36)

	got, err := c.Find(SearchOptions{ExcludeTags: []string{"2d"}, Page: 1})
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"3", "5"}, ids(got))
}

func TestAuthorIsMatchedExactlyNotSearched(t *testing.T) {
	// AuthorKey is a keyword field for the same reason TagSlugs is: analysed,
	// an author called "el" would also be found by every description mentioning
	// the word, and a two-word name would match either half.
	c := newLoadedCache(t, 36)

	got, err := c.Find(SearchOptions{Author: "ana", Page: 1})
	require.NoError(t, err)
	assert.Equal(t, []string{"1"}, ids(got))

	// Case and surrounding whitespace are folded, so a name copied out of a
	// card still matches the document it came from.
	loose, err := c.Find(SearchOptions{Author: "  ANA ", Page: 1})
	require.NoError(t, err)
	assert.Equal(t, []string{"1"}, ids(loose))
}

func TestPriceCeilingsCompareAcrossCurrencies(t *testing.T) {
	// The fixtures are priced in dollars and euros. A ceiling that compared the
	// raw numbers would call 9.97 EUR cheaper than it is.
	c := newLoadedCache(t, 36)

	under5, err := c.Find(SearchOptions{Price: models.PricingUnder5, Page: 1})
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"2", "5"}, ids(under5), "$4.95 and $1, but not 9.97€")

	under20, err := c.Find(SearchOptions{Price: models.PricingUnder20, Page: 1})
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"2", "3", "5"}, ids(under20))
}

func TestACeilingDoesNotQuietlyIncludeTheFreeAssets(t *testing.T) {
	// "Under $5" sits beside a "Free" button. If it contained everything free
	// as well, the two controls would overlap and the cheaper-looking one would
	// be the larger set, which reads as broken.
	c := newLoadedCache(t, 36)

	got, err := c.Find(SearchOptions{Price: models.PricingUnder5, Page: 1})
	require.NoError(t, err)
	assert.NotContains(t, ids(got), "1")
	assert.NotContains(t, ids(got), "4")
}

func TestCheapestFirstPutsFreeAheadAndUnpricedLast(t *testing.T) {
	c := newLoadedCache(t, 36)

	got, err := c.Find(SearchOptions{Sort: models.SortPrice, Page: 1})
	require.NoError(t, err)
	// Free (1, 4 by popularity), then $1, $4.95, 9.97€.
	assert.Equal(t, []string{"1", "4", "5", "2", "3"}, ids(got))
}

func TestRecencyOrdersTheRankedAndParksTheRest(t *testing.T) {
	// Only assets seen in itch.io's newest view carry a rank - it stops at 200
	// pages - so everything else has to sort after them rather than mingle.
	c := newLoadedCache(t, 36)

	got, err := c.Find(SearchOptions{Sort: models.SortRecent, Page: 1})
	require.NoError(t, err)
	assert.Equal(t, []string{"4", "2"}, ids(got)[:2], "ranked assets lead, newest first")
	assert.ElementsMatch(t, []string{"1", "3", "5"}, ids(got)[2:], "the unranked follow, by popularity")
}

func TestRecencyFallsBackWhenTheDataCannotSupportIt(t *testing.T) {
	// An index published before the crawl collected ranks has none at all.
	// Ordering by "no rank" would present arbitrary order as a recency listing.
	c := newLoadedCache(t, 36)
	c.hasRecency = false

	got, err := c.Find(SearchOptions{Sort: models.SortRecent, Page: 1})
	require.NoError(t, err)
	assert.Equal(t, []string{"1", "2", "3", "4", "5"}, ids(got), "popularity order")
}

func TestQuotedTermsAreMatchedAsAPhrase(t *testing.T) {
	// Unquoted, these words match both pixel-art assets in any order. Quoted,
	// only the one whose title actually reads "Pixel Art Icons" should survive.
	c := newLoadedCache(t, 36)

	loose, err := c.Find(SearchOptions{Query: "art icons", Page: 1})
	require.NoError(t, err)
	assert.Greater(t, loose.Total, int64(1))

	phrase, err := c.Find(SearchOptions{Query: `"art icons"`, Page: 1})
	require.NoError(t, err)
	assert.Equal(t, []string{"4"}, ids(phrase))
}

func TestAnUnclosedQuoteIsOrdinaryText(t *testing.T) {
	// Half-typed queries arrive constantly. Treating one open quote as a phrase
	// running to the end of the input empties the results mid-keystroke.
	c := newLoadedCache(t, 36)

	got, err := c.Find(SearchOptions{Query: `"pixel art`, Page: 1})
	require.NoError(t, err)
	assert.NotEmpty(t, ids(got))
}
