package web

import (
	"encoding/xml"
	"net/http"
	"testing"

	"itchgrep/internal/cache"
	"itchgrep/internal/storage"
	"itchgrep/pkg/models"

	"github.com/blevesearch/bleve"
	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// sitemapXML is an independent decode target - it mirrors the public shape
// of a sitemap without depending on the templates package's own encoding
// types, the same reasoning atomFeedXML uses for the feed in handlers_test.go.
type sitemapXML struct {
	XMLName xml.Name `xml:"urlset"`
	URLs    []struct {
		Loc string `xml:"loc"`
	} `xml:"url"`
}

func TestSitemapListsFixedPagesAndTags(t *testing.T) {
	t.Setenv("DATA_DIR", t.TempDir())
	require.NoError(t, storage.PutAssets([]models.Asset{{GameId: "1", Title: "A"}}))
	require.NoError(t, storage.PutTags([]models.Tag{{Slug: "pixel-art"}, {Slug: "music"}}))

	index, err := bleve.New(storage.IndexPath(), storage.IndexMapping())
	require.NoError(t, err)
	require.NoError(t, index.Close())

	c := cache.NewCache(36)
	require.NoError(t, c.RefreshDataCache())
	h := NewHandler(c, 0, nil)
	r := chi.NewRouter()
	r.Get("/sitemap.xml", h.HandleSitemap)

	w := get(t, r, "/sitemap.xml")
	require.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "application/xml; charset=utf-8", w.Header().Get("Content-Type"))
	assert.Equal(t, "public, max-age=86400", w.Header().Get("Cache-Control"))

	var doc sitemapXML
	require.NoError(t, xml.Unmarshal(w.Body.Bytes(), &doc), "the rendered body must be parseable XML")

	var locs []string
	for _, u := range doc.URLs {
		locs = append(locs, u.Loc)
	}
	assert.Contains(t, locs, "https://itchgrep.com/")
	assert.Contains(t, locs, "https://itchgrep.com/about")
	assert.Contains(t, locs, "https://itchgrep.com/stats")
	assert.Contains(t, locs, "https://itchgrep.com/new")
	assert.Contains(t, locs, "https://itchgrep.com/tag/pixel-art")
	assert.Contains(t, locs, "https://itchgrep.com/tag/music")
	assert.NotContains(t, locs, "https://itchgrep.com/results", "robots.txt disallows query strings; the sitemap must not advertise a path it tells crawlers to skip")
}

func TestSitemapIsAvailableBeforeTheCacheLoads(t *testing.T) {
	// Unlike a query page this runs no bleve search - a cold cache just has
	// an empty tag list to add to the fixed pages - so this renders 200, not
	// 503, even before the first crawl has published anything.
	r, _ := newTestRouter()
	w := get(t, r, "/sitemap.xml")

	assert.Equal(t, http.StatusOK, w.Code)
	var doc sitemapXML
	require.NoError(t, xml.Unmarshal(w.Body.Bytes(), &doc))
	var locs []string
	for _, u := range doc.URLs {
		locs = append(locs, u.Loc)
	}
	assert.Contains(t, locs, "https://itchgrep.com/about", "the fixed pages exist regardless of the cache")
}
