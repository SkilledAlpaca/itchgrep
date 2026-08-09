package cache

import (
	"testing"
	"time"

	"itchgrep/internal/storage"
	"itchgrep/pkg/models"

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
			Tags: []string{"2d", "sprites"}, Price: "$4.95", InvPopularity: 2},
		{GameId: "3", Title: "Ambient Music Loops", Author: "cy", Description: "audio",
			Tags: []string{"music"}, Price: "9.97€", InvPopularity: 3},
		{GameId: "4", Title: "Pixel Art Icons", Author: "dee", Description: "icons",
			Tags: []string{"2d", "pixel-art", "icons"}, InvPopularity: 4},
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
		require.NoError(t, batch.Index(a.GameId, models.NewIndexedAsset(a)))
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
	// Ahead of what is on disk, so the cache is never judged stale and never
	// reloads itself mid-test - which would quietly undo the index a test
	// deliberately removed.
	c.dataUpdatedTime = time.Now().Add(time.Hour)
	return c
}

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
