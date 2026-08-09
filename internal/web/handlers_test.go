package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"itchgrep/internal/cache"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newTestRouter wires the same routes cmd/webserver does, minus the rate
// limiter, over a cache that has never loaded. That is enough to assert the
// routing, caching and escaping contracts; result correctness belongs to the
// cache package.
func newTestRouter() (*chi.Mux, *handler) {
	h := NewHandler(cache.NewCache(36))
	r := chi.NewRouter()
	r.Get("/", h.HandleIndex)
	r.Get("/assets/{page}", h.HandleGetAssetPage)
	r.Get("/search", h.HandleSearch)
	r.Get("/about", h.HandleAbout)
	r.NotFound(Handle404)
	return r, h
}

func get(t *testing.T, r *chi.Mux, target string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, target, nil))
	return w
}

func TestSearchQueryIsRenderedIntoThePage(t *testing.T) {
	// This is what makes /?q=... a shareable link: the recipient must land on a
	// populated search box, not an empty one.
	r, _ := newTestRouter()
	w := get(t, r, "/?q=pixel+art")

	require.Equal(t, http.StatusOK, w.Code)
	body := w.Body.String()
	assert.Contains(t, body, `value="pixel art"`, "the query must be restored into the input")
	assert.Contains(t, body, "/search?q=pixel+art&amp;page=1", "results must load for a shared link")
	assert.Contains(t, body, "<title>", "a shared link renders the full page, not a fragment")
}

func TestSuccessfulResponsesAreCacheable(t *testing.T) {
	// Without this every visitor's scroll reaches the origin. Result pages are
	// byte-identical for everyone between crawls, so a proxy must be allowed to
	// share them.
	_, h := newTestRouter()
	w := httptest.NewRecorder()
	h.cacheable(w)
	assert.Equal(t, "public, max-age=300", w.Header().Get("Cache-Control"))
}

func TestBrowsePageIsNotCachedWhenTheIndexIsMissing(t *testing.T) {
	// The opposite contract: a proxy caching this would pin "not ready" in
	// front of a site that has since published.
	r, _ := newTestRouter()
	w := get(t, r, "/assets/1")

	require.Equal(t, http.StatusServiceUnavailable, w.Code)
	assert.Equal(t, "no-store", w.Header().Get("Cache-Control"))
}

func TestIndexIsCacheable(t *testing.T) {
	r, _ := newTestRouter()
	w := get(t, r, "/")
	assert.Contains(t, w.Header().Get("Cache-Control"), "public")
}

func TestNotReadyIsNeverCached(t *testing.T) {
	// The cache has loaded nothing, so this is the 503 path. Caching it would
	// pin "not ready" in front of a site that has since come up.
	r, _ := newTestRouter()
	w := get(t, r, "/search?q=anything")

	require.Equal(t, http.StatusServiceUnavailable, w.Code)
	assert.Equal(t, "no-store", w.Header().Get("Cache-Control"))
	assert.Contains(t, w.Body.String(), "Index not ready")
}

func TestSearchPushesTheShareableUrl(t *testing.T) {
	// htmx swaps in a fragment from /search, but the address bar has to end up
	// showing the form a visitor can copy.
	r, _ := newTestRouter()
	w := get(t, r, "/search?q=pixel+art")
	assert.Equal(t, "/?q=pixel+art", w.Header().Get("HX-Push-Url"))
}

func TestSearchDoesNotPushUrlForLaterPages(t *testing.T) {
	// Infinite scroll must not rewrite the address bar on every page it pulls.
	r, _ := newTestRouter()
	w := get(t, r, "/search?q=pixel+art&page=3")
	assert.Empty(t, w.Header().Get("HX-Push-Url"))
}

func TestEmptySearchFallsBackToBrowse(t *testing.T) {
	r, _ := newTestRouter()
	w := get(t, r, "/search?q=")
	assert.Equal(t, http.StatusSeeOther, w.Code)
	assert.Equal(t, "/assets/1", w.Header().Get("Location"))
}

func TestSearchRejectsAnInvalidPage(t *testing.T) {
	r, _ := newTestRouter()
	assert.Equal(t, http.StatusBadRequest, get(t, r, "/search?q=x&page=0").Code)
	assert.Equal(t, http.StatusBadRequest, get(t, r, "/search?q=x&page=abc").Code)
}

func TestQueryIsClamped(t *testing.T) {
	// A pathologically long query would otherwise be handed straight to the
	// fuzzy passes, which cost more the longer the input.
	long := strings.Repeat("a", maxQueryLen+50)
	assert.Len(t, clampQuery(long), maxQueryLen)
	assert.Equal(t, "short", clampQuery("short"))
}

func TestNotFoundIsStyledAndUncached(t *testing.T) {
	r, _ := newTestRouter()
	w := get(t, r, "/no-such-page")
	assert.Equal(t, http.StatusNotFound, w.Code)
	assert.Equal(t, "no-store", w.Header().Get("Cache-Control"))
	assert.Contains(t, w.Body.String(), "404")
}

func TestStaticAssetsAreServedAndCached(t *testing.T) {
	r := chi.NewRouter()
	r.Handle("/static/*", StaticHandler())

	for _, path := range []string{"/static/app.css", "/static/htmx.min.js"} {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		assert.Equal(t, http.StatusOK, w.Code, "%s must be embedded in the binary", path)
		assert.NotEmpty(t, w.Body.Bytes())
		assert.Contains(t, w.Header().Get("Cache-Control"), "max-age")
	}
}
