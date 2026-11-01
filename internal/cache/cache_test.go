package cache

import (
	"os"
	"testing"
	"time"

	"itchgrep/internal/storage"
	"itchgrep/pkg/models"

	"github.com/blevesearch/bleve/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// loadedCacheWithTags builds a cache the way RefreshDataCache actually would -
// unlike newLoadedCache in search_test.go, which constructs the Cache by hand
// and never exercises doRefresh's storage.GetTags read at all.
func loadedCacheWithTags(t *testing.T, tags []models.Tag) *Cache {
	t.Helper()
	t.Setenv("DATA_DIR", t.TempDir())

	assets := fixtures()
	require.NoError(t, storage.PutAssets(assets))
	if tags != nil {
		require.NoError(t, storage.PutTags(tags))
	}

	index, err := bleve.New(storage.IndexPath(), storage.IndexMapping())
	require.NoError(t, err)
	batch := index.NewBatch()
	for _, a := range assets {
		require.NoError(t, batch.Index(a.GameId, models.NewIndexedAsset(a, priceUSD(a))))
	}
	require.NoError(t, index.Batch(batch))
	require.NoError(t, index.Close())

	c := NewCache(36)
	require.NoError(t, c.RefreshDataCache())
	return c
}

func TestTagUniverseIsLoadedFromDiscoveryNotFromThisPublishsAssets(t *testing.T) {
	// tags.json is the crawl's own discovered vocabulary, independent of
	// whether the current publish happens to carry a matching asset - unlike
	// tagCounts (the sidebar histogram), which only ever lists tags this
	// publish's assets actually carry. "unreleased" proves the distinction:
	// no fixture asset carries it, yet it must still be a known tag.
	c := loadedCacheWithTags(t, []models.Tag{
		{Slug: "pixel-art", Count: 900},
		{Slug: "unreleased", Count: 0},
	})

	assert.Equal(t, []string{"pixel-art", "unreleased"}, c.TagSlugs(), "sorted, not tags.json's own order")
	assert.True(t, c.HasTag("pixel-art"))
	assert.True(t, c.HasTag("unreleased"))
	assert.False(t, c.HasTag("no-such-tag"))
}

func TestTagUniverseIsEmptyRatherThanErrorWhenNeverDiscovered(t *testing.T) {
	// An index published before tag discovery ever ran has no tags.json at
	// all. That must degrade to "no known tags" - /tag/{slug} all 404 and the
	// sitemap lists only the fixed pages - rather than failing the refresh
	// the same way a missing rates file does not.
	t.Setenv("DATA_DIR", t.TempDir())
	assets := fixtures()
	require.NoError(t, storage.PutAssets(assets))

	index, err := bleve.New(storage.IndexPath(), storage.IndexMapping())
	require.NoError(t, err)
	require.NoError(t, index.Close())

	c := NewCache(36)
	require.NoError(t, c.RefreshDataCache())

	assert.Empty(t, c.TagSlugs())
	assert.False(t, c.HasTag("pixel-art"))
}

func TestAFailedRefreshDoesNotBlockTheNextRefreshOrSearches(t *testing.T) {
	c := loadedCacheWithTags(t, nil)
	good := os.Getenv("DATA_DIR")

	t.Setenv("DATA_DIR", t.TempDir()) // nothing published here
	require.Error(t, c.RefreshDataCache())
	assert.True(t, c.Ready(), "the previously loaded data keeps serving")

	got, err := c.Find(SearchOptions{Query: "zebra", Page: 1})
	require.NoError(t, err)
	assert.Equal(t, []string{"2"}, ids(got))

	// The single-flight flag must have cleared, so this runs rather than waits.
	t.Setenv("DATA_DIR", good)
	done := make(chan error, 1)
	go func() { done <- c.RefreshDataCache() }()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(20 * time.Second):
		t.Fatal("refresh after a failed one did not return")
	}
}

func TestRefreshingTheDirectoryAlreadyOpenKeepsItInsteadOfFightingForTheLock(t *testing.T) {
	c := loadedCacheWithTags(t, nil)
	first := c.index

	start := time.Now()
	require.NoError(t, c.RefreshDataCache())
	assert.Same(t, first, c.index, "same directory, same index")
	assert.Less(t, time.Since(start), 4*time.Second, "no wait on the bolt lock timeout")

	got, err := c.Find(SearchOptions{Query: "zebra", Page: 1})
	require.NoError(t, err)
	assert.Equal(t, []string{"2"}, ids(got))
}

func TestACacheThatNeverLoadedIsNotReady(t *testing.T) {
	assert.False(t, NewCache(36).Ready())
}
