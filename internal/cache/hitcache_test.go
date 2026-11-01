package cache

import (
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"itchgrep/pkg/models"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHitCacheEvictsLeastRecentlyUsed(t *testing.T) {
	h := newHitCache(3)
	fill := func(n int) func() (*hitEntry, error) {
		return func() (*hitEntry, error) { return &hitEntry{total: int64(n)}, nil }
	}
	for i := 0; i < 3; i++ {
		_, err := h.get(fmt.Sprint(i), fill(i))
		require.NoError(t, err)
	}
	// Touch 0 so that 1 is the oldest.
	_, _ = h.get("0", fill(0))
	_, _ = h.get("3", fill(3))
	assert.Equal(t, 3, h.len())

	misses := h.misses.Load()
	_, _ = h.get("0", fill(0))
	_, _ = h.get("2", fill(2))
	_, _ = h.get("3", fill(3))
	assert.Equal(t, misses, h.misses.Load(), "0, 2 and 3 are still cached")
	_, _ = h.get("1", fill(1))
	assert.Equal(t, misses+1, h.misses.Load(), "1 was evicted")
}

func TestHitCacheStaysAtItsBound(t *testing.T) {
	h := newHitCache(hitCacheSize)
	for i := 0; i < hitCacheSize*3; i++ {
		_, _ = h.get(fmt.Sprint(i), func() (*hitEntry, error) { return &hitEntry{}, nil })
	}
	assert.Equal(t, hitCacheSize, h.len())
}

func TestHitCacheSingleFlight(t *testing.T) {
	h := newHitCache(8)
	var fills atomic.Int32
	release := make(chan struct{})
	var wg sync.WaitGroup
	results := make([]*hitEntry, 20)
	for i := range results {
		wg.Add(1)
		go func() {
			defer wg.Done()
			e, err := h.get("k", func() (*hitEntry, error) {
				fills.Add(1)
				<-release
				return &hitEntry{total: 9}, nil
			})
			assert.NoError(t, err)
			results[i] = e
		}()
	}
	// Wait for the one fill to start, then give the other nineteen time to
	// arrive behind it. Whatever the timing, a second fill would be a failure;
	// the pause is what makes the test exercise the waiting path at all.
	for {
		h.mu.Lock()
		n := len(h.flights)
		h.mu.Unlock()
		if n == 1 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	time.Sleep(50 * time.Millisecond)
	close(release)
	wg.Wait()
	assert.EqualValues(t, 1, fills.Load(), "twenty concurrent requests, one pass")
	for _, e := range results {
		assert.EqualValues(t, 9, e.total)
	}
}

func TestHitCacheDoesNotCacheErrors(t *testing.T) {
	h := newHitCache(8)
	_, err := h.get("k", func() (*hitEntry, error) { return nil, errors.New("boom") })
	require.Error(t, err)
	e, err := h.get("k", func() (*hitEntry, error) { return &hitEntry{total: 1}, nil })
	require.NoError(t, err)
	assert.EqualValues(t, 1, e.total)
}

func TestHitCacheResetDropsEntriesAndLateFills(t *testing.T) {
	h := newHitCache(8)
	_, _ = h.get("a", func() (*hitEntry, error) { return &hitEntry{}, nil })
	require.Equal(t, 1, h.len())
	h.reset()
	assert.Equal(t, 0, h.len())

	// A fill that started before the reset must not be stored after it.
	_, _ = h.get("b", func() (*hitEntry, error) {
		h.reset()
		return &hitEntry{}, nil
	})
	assert.Equal(t, 0, h.len(), "an answer computed from the previous dataset is not kept")
}

func TestHitKeyIgnoresPageAndListOrder(t *testing.T) {
	a := SearchOptions{Query: "pixel", Tags: []string{"a", "b"}, Page: 1}
	b := SearchOptions{Query: "pixel", Tags: []string{"b", "a"}, Page: 4, NoFacets: true}
	assert.Equal(t, hitKey(a), hitKey(b))
	assert.NotEqual(t, hitKey(a), hitKey(SearchOptions{Query: "pixel", Tags: []string{"a"}}))
	assert.NotEqual(t, hitKey(a), hitKey(SearchOptions{Query: "pixel", Tags: []string{"a", "b"}, Sort: models.SortTitle}))
	assert.Equal(t, hitKey(SearchOptions{Query: "x"}), hitKey(SearchOptions{Query: "x", Sort: models.SortRelevance}), "relevance is the default with a query")
}

var textQueries = []string{"pixel", "sword forest", "robot castle tile", "zebra", "pixl", "music loop"}

// TestTextSearchThroughHitCacheMatchesSearchIndex pages through text searches,
// with and without filters, and compares every page with a direct pass
// through the index.
func TestTextSearchThroughHitCacheMatchesSearchIndex(t *testing.T) {
	c := newLoadedCacheFromAssets(t, 36, parityAssets(3, 2500, true))
	for _, q := range textQueries {
		for _, filters := range []SearchOptions{
			{},
			{Tags: []string{"tag-00"}},
			{Price: models.PricingFree, Sort: models.SortTitle},
			{ExcludeTags: []string{"tag-01"}, Sort: models.SortPrice},
		} {
			for page := int64(1); page <= 12; page++ {
				opts := filters
				opts.Query = q
				opts.Page = page
				opts.NoFacets = page > 1
				got, err := c.Find(opts)
				require.NoError(t, err)
				want, err := c.searchIndex(opts, false)
				require.NoError(t, err)
				desc := fmt.Sprintf("%+v", opts)
				assert.Equal(t, ids(want), ids(got), desc)
				assert.Equal(t, want.Total, got.Total, desc)
				if page == 1 {
					assert.Equal(t, tagList(want), tagList(got), desc)
				} else {
					assert.Empty(t, got.Tags, "no sidebar was asked for: "+desc)
				}
			}
		}
	}
	assert.Positive(t, c.hits.hits.Load(), "later pages are served from the first page's entry")
	assert.LessOrEqual(t, c.hits.len(), hitCacheSize)
}

func TestTextSearchPagesShareOneIndexPass(t *testing.T) {
	c := newLoadedCacheFromAssets(t, 36, parityAssets(3, 2500, true))
	for page := int64(1); page <= 10; page++ {
		_, err := c.Find(SearchOptions{Query: "pixel", Page: page, NoFacets: page > 1})
		require.NoError(t, err)
	}
	assert.EqualValues(t, 1, c.hits.misses.Load(), "ten pages, one pass through the index")

	// Past the cached depth the request goes to the index and is not cached.
	_, err := c.Find(SearchOptions{Query: "pixel", Page: 11, NoFacets: true})
	require.NoError(t, err)
	assert.EqualValues(t, 1, c.hits.misses.Load())
	assert.Equal(t, 1, c.hits.len())
}

func TestFilterOnlyAndCappedRequestsSkipTheHitCache(t *testing.T) {
	c := newLoadedCacheFromAssets(t, 36, parityAssets(3, 500, true))
	_, err := c.Find(SearchOptions{Tags: []string{"tag-00"}, Page: 1})
	require.NoError(t, err)
	_, err = c.Find(SearchOptions{Query: "pixel", MinFirstSeen: 1, Sort: models.SortNew, Limit: 50, Page: 1, NoFacets: true})
	require.NoError(t, err)
	assert.Equal(t, 0, c.hits.len())
}

func TestHitCacheIsClearedByRefresh(t *testing.T) {
	c := loadedCacheWithTags(t, nil)
	_, err := c.Find(SearchOptions{Query: "pixel", Page: 1})
	require.NoError(t, err)
	require.Equal(t, 1, c.hits.len())

	require.NoError(t, c.RefreshDataCache())
	assert.Equal(t, 0, c.hits.len(), "a new dataset starts with no remembered answers")
}

// TestTextSearchIsRaceFree hammers the same and different searches from many
// goroutines while refreshes swap the dataset underneath. Meaningful under
// -race.
func TestTextSearchIsRaceFree(t *testing.T) {
	c := loadedCacheWithTags(t, nil)
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 40; i++ {
				q := []string{"pixel", "art", "music", "zebra"}[(g+i)%4]
				res, err := c.Find(SearchOptions{Query: q, Page: int64(1 + i%3), NoFacets: i%3 > 0})
				assert.NoError(t, err)
				assert.GreaterOrEqual(t, res.Total, int64(0))
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 5; i++ {
			// The index directory is the one already open, so this reuses it;
			// what changes is the generation of everything derived.
			assert.NoError(t, c.RefreshDataCache())
		}
	}()
	wg.Wait()
}
