package web

import (
	"fmt"
	"net/http"
	"testing"

	"itchgrep/internal/cache"
	"itchgrep/internal/storage"
	"itchgrep/pkg/models"

	"github.com/blevesearch/bleve/v2"
	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// manyTagsCache loads 120 assets over 60 tags, so the sidebar has more tags
// than it renders at rest and the tail is not empty. Asset i carries three tags;
// the head tags are on more assets than the tail tags.
func manyTagsCache(t *testing.T) *cache.Cache {
	t.Helper()
	t.Setenv("DATA_DIR", t.TempDir())

	var assets []models.Asset
	for i := 0; i < 120; i++ {
		seen := map[string]bool{}
		var tags []string
		for _, j := range []int{i % 60, (i * 7) % 40, i % 5} {
			slug := fmt.Sprintf("tag-%02d", j)
			if !seen[slug] {
				seen[slug] = true
				tags = append(tags, slug)
			}
		}
		assets = append(assets, models.Asset{
			GameId: fmt.Sprint(1000 + i), Title: fmt.Sprintf("Pixel Asset %03d", i), Author: "ana",
			Link: "https://ana.itch.io/x", Tags: tags, InvPopularity: int64(i/36 + 1),
		})
	}
	require.NoError(t, storage.PutAssets(assets))
	var universe []models.Tag
	for j := 0; j < 60; j++ {
		universe = append(universe, models.Tag{Slug: fmt.Sprintf("tag-%02d", j), Count: 1})
	}
	require.NoError(t, storage.PutTags(universe))

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

func perfRouter(t *testing.T) (*chi.Mux, *cache.Cache) {
	r, c, _ := perfRouterHandler(t)
	return r, c
}

// perfRouterHandler is perfRouter that also hands back the handler, for tests
// that need to set its fields.
func perfRouterHandler(t *testing.T) (*chi.Mux, *cache.Cache, *handler) {
	t.Helper()
	c := manyTagsCache(t)
	h := NewHandler(c, 0, nil)
	r := chi.NewRouter()
	r.Get("/", h.HandleIndex)
	r.Get("/results", h.HandleResults)
	r.Head("/results", h.HandleResults)
	r.Get("/new", h.HandleNew)
	r.Get("/about", h.HandleAbout)
	r.Get("/saved", h.HandleSaved)
	r.Get("/stats", h.HandleStats)
	r.Get("/tag/{slug}", h.HandleTag)
	return r, c, h
}

func TestPagesSendALongEdgeLifetimeExceptStats(t *testing.T) {
	r, _ := perfRouter(t)
	for _, target := range []string{"/", "/?q=pixel", "/results?q=pixel", "/results?page=2", "/tag/tag-00", "/new", "/about", "/saved"} {
		w := get(t, r, target)
		require.Equal(t, http.StatusOK, w.Code, target)
		assert.Equal(t, "public, max-age=300, s-maxage=3600", w.Header().Get("Cache-Control"), target)
	}
	assert.Equal(t, "public, max-age=60", get(t, r, "/stats").Header().Get("Cache-Control"), "stats keeps its short lifetime")
}

func TestAPageBeyondTheLastDoesNotGetTheLongEdgeLifetime(t *testing.T) {
	// After a publish that shrinks the results, a long-lived "no more results"
	// would keep telling people the end is where it used to be.
	r, _ := perfRouter(t)
	for _, target := range []string{"/?page=9", "/results?page=9", "/tag/tag-00?page=9"} {
		w := get(t, r, target)
		require.Equal(t, http.StatusOK, w.Code, target)
		assert.Equal(t, "public, max-age=300", w.Header().Get("Cache-Control"), target)
	}
}
