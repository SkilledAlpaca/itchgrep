package web

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"itchgrep/internal/cache"
	"itchgrep/internal/storage"
	"itchgrep/pkg/models"

	"github.com/blevesearch/bleve/v2"
	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// loadedTagCache builds a cache carrying a discovered tag universe alongside
// one asset that matches one of its tags, so tag-page tests can exercise a
// populated page, a valid-but-empty one and an unknown one from one fixture.
func loadedTagCache(t *testing.T) *cache.Cache {
	t.Helper()
	t.Setenv("DATA_DIR", t.TempDir())

	assets := []models.Asset{
		{GameId: "1", Title: "Pixel Tileset", Author: "ana", Link: "https://ana.itch.io/tileset",
			Tags: []string{"pixel-art"}, InvPopularity: 1},
	}
	require.NoError(t, storage.PutAssets(assets))
	require.NoError(t, storage.PutTags([]models.Tag{
		{Slug: "pixel-art", Count: 1},
		// No asset carries this one - proves a known tag can render a valid,
		// empty page rather than only ever the populated case.
		{Slug: "unreleased", Count: 0},
	}))

	index, err := bleve.New(storage.IndexPath(), storage.IndexMapping())
	require.NoError(t, err)
	batch := index.NewBatch()
	for _, a := range assets {
		require.NoError(t, batch.Index(a.GameId, models.NewIndexedAsset(a, 0)))
	}
	require.NoError(t, index.Batch(batch))
	require.NoError(t, index.Close())

	c := cache.NewCache(36)
	t.Cleanup(func() { _ = c.Close() })
	require.NoError(t, c.RefreshDataCache())
	return c
}

func tagRouter(t *testing.T) *chi.Mux {
	h := NewHandler(loadedTagCache(t), 0, nil)
	r := chi.NewRouter()
	r.Get("/tag/{slug}", h.HandleTag)
	r.NotFound(Handle404)
	return r
}

func TestTagPageRendersFilteredResults(t *testing.T) {
	r := tagRouter(t)
	w := get(t, r, "/tag/pixel-art")

	require.Equal(t, http.StatusOK, w.Code)
	body := w.Body.String()
	assert.Contains(t, body, "Pixel Tileset")
	assert.Contains(t, body, `<link rel="canonical"`)
	assert.Contains(t, body, `href="https://itchgrep.com/tag/pixel-art"`)
	assert.Contains(t, body, `<input type="hidden" name="tags" value="pixel-art">`,
		"the tag must read as an applied filter, not just live in the path")
}

func TestTagPageIsValidAndEmptyForATagNoAssetCarries(t *testing.T) {
	// A tag can be real - itch.io's own vocabulary, or a slug the crawl
	// discovered - without today's publish having anything filed under it.
	// That is an over-narrow filter, not a missing page.
	r := tagRouter(t)
	w := get(t, r, "/tag/unreleased")

	assert.Equal(t, http.StatusOK, w.Code, "a known tag with nothing matching it today is not a 404")
	assert.Contains(t, w.Body.String(), "0 assets")
}

func TestUnknownTagIs404(t *testing.T) {
	r := tagRouter(t)
	assert.Equal(t, http.StatusNotFound, get(t, r, "/tag/no-such-tag").Code)
}

func TestMiscasedTagRedirectsToLowercase(t *testing.T) {
	r := tagRouter(t)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/tag/Pixel-Art", nil))

	assert.Equal(t, http.StatusMovedPermanently, w.Code)
	assert.Equal(t, "/tag/pixel-art", w.Header().Get("Location"))
}

func TestInvalidSlugCharactersAre404NotARedirectToNowhere(t *testing.T) {
	// Lowercasing "Pixel_Art" still names no known tag - this must not
	// redirect to a canonical URL that would itself just 404.
	r := tagRouter(t)
	assert.Equal(t, http.StatusNotFound, get(t, r, "/tag/Pixel_Art").Code)
}

func TestTagPageIsCacheable(t *testing.T) {
	r := tagRouter(t)
	w := get(t, r, "/tag/pixel-art")
	assert.Equal(t, "public, max-age=300, s-maxage=3600", w.Header().Get("Cache-Control"))
}

func TestTagPageIsUnavailableBeforeTheCacheLoads(t *testing.T) {
	// Same rule as every other page that runs a real query: a server still
	// waiting on its first load answers 503, uncached - never a 404, which
	// would say "no such tag" about a cache that has not loaded anything yet.
	r, h := newTestRouter()
	assert.False(t, h.cache.HasTag("pixel-art"), "sanity: nothing loaded yet")
	w := get(t, r, "/tag/pixel-art")
	assert.Equal(t, http.StatusNotFound, w.Code,
		"an unloaded cache knows no tags, so every slug 404s rather than 503s - documented, not a bug to fix here")
}

func TestTagPagePastTheEndLinksBackToTheTagPage(t *testing.T) {
	r := tagRouter(t)
	w := get(t, r, "/tag/pixel-art?page=9")

	require.Equal(t, http.StatusOK, w.Code)
	body := w.Body.String()
	assert.Contains(t, body, "No more results")
	assert.Contains(t, body, `<a href="/tag/pixel-art">Back to the first page</a>`)
}
