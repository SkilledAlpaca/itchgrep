package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"itchgrep/internal/cache"
	"itchgrep/pkg/models"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newTestRouter wires the same routes cmd/webserver does, minus the rate
// limiter, over a cache that has never loaded. That is enough to assert the
// routing, parsing, caching and escaping contracts; result correctness belongs
// to the cache package.
func newTestRouter() (*chi.Mux, *handler) {
	h := NewHandler(cache.NewCache(36))
	r := chi.NewRouter()
	r.Get("/", h.HandleIndex)
	r.Get("/results", h.HandleResults)
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

	body := w.Body.String()
	assert.Contains(t, body, `value="pixel art"`, "the query must be restored into the input")
	assert.Contains(t, body, "<title>", "a shared link renders the full page, not a fragment")
	assert.Contains(t, body, "pixel art — ITCHGREP", "the title should say what was searched for")
}

func TestAppliedFiltersSurviveANewSearch(t *testing.T) {
	// The search form posts to the same page, so anything it does not carry is
	// silently dropped. Losing the tags a visitor just picked because they then
	// typed a word would be the worst kind of surprise.
	r, _ := newTestRouter()
	body := get(t, r, "/?tags=2d,pixel-art&price=free").Body.String()

	assert.Contains(t, body, `<input type="hidden" name="tags" value="2d">`)
	assert.Contains(t, body, `<input type="hidden" name="tags" value="pixel-art">`)
	assert.Contains(t, body, `<input type="hidden" name="price" value="free">`)
}

func TestFiltersAreParsedFromEitherEncoding(t *testing.T) {
	// Links generate ?tags=a,b; a form submission of the hidden fields above
	// generates ?tags=a&tags=b. Both have to mean the same thing.
	comma := parseFilters(httptest.NewRequest(http.MethodGet, "/?tags=2d,pixel-art", nil))
	repeated := parseFilters(httptest.NewRequest(http.MethodGet, "/?tags=2d&tags=pixel-art", nil))

	assert.Equal(t, []string{"2d", "pixel-art"}, comma.Tags)
	assert.Equal(t, comma.Tags, repeated.Tags)
}

func TestJunkFilterValuesAreDropped(t *testing.T) {
	// They can only come from a hand-edited or stale URL. Answering with a 400
	// would put an error page behind a link with an obvious harmless reading.
	f := parseFilters(httptest.NewRequest(http.MethodGet,
		"/?tags=Pixel_Art!&price=cheap&sort=whatever", nil))

	assert.Empty(t, f.Tags, "a slug with characters itch.io never uses matches nothing")
	assert.Empty(t, f.Price)
	assert.Empty(t, f.Sort)
}

func TestTagsAreNormalisedAndDeduped(t *testing.T) {
	f := parseFilters(httptest.NewRequest(http.MethodGet, "/?tags=+2D+,2d,,pixel-art", nil))
	assert.Equal(t, []string{"2d", "pixel-art"}, f.Tags)
}

func TestValidFilterValuesAreKept(t *testing.T) {
	f := parseFilters(httptest.NewRequest(http.MethodGet, "/?price=paid&sort=title", nil))
	assert.Equal(t, models.PricingPaid, f.Price)
	assert.Equal(t, models.SortTitle, f.Sort)
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

func TestNotReadyIsNeverCached(t *testing.T) {
	// The cache has loaded nothing, so this is the 503 path. Caching it would
	// pin "not ready" in front of a site that has since come up.
	r, _ := newTestRouter()
	w := get(t, r, "/results?q=anything")

	require.Equal(t, http.StatusServiceUnavailable, w.Code)
	assert.Equal(t, "no-store", w.Header().Get("Cache-Control"))
	assert.Contains(t, w.Body.String(), "Index not ready")
}

func TestIndexStillRendersWhenThereIsNoIndexYet(t *testing.T) {
	// A visitor arriving during the first crawl should get the page and an
	// explanation, not a bare error - and nothing should cache that state.
	r, _ := newTestRouter()
	w := get(t, r, "/")

	assert.Equal(t, http.StatusServiceUnavailable, w.Code)
	assert.Equal(t, "no-store", w.Header().Get("Cache-Control"))
	assert.Contains(t, w.Body.String(), "<title>", "the shell must still render")
}

func TestResultsPushTheShareableUrl(t *testing.T) {
	// htmx swaps in a fragment from /results, but the address bar has to end up
	// showing the form a visitor can copy - filters included.
	r, _ := newTestRouter()
	w := get(t, r, "/results?q=pixel+art&tags=2d&price=free")
	assert.Equal(t, "/?price=free&q=pixel+art&tags=2d", w.Header().Get("HX-Push-Url"))
}

func TestResultsDoNotPushUrlForLaterPages(t *testing.T) {
	// Infinite scroll must not rewrite the address bar on every page it pulls.
	r, _ := newTestRouter()
	w := get(t, r, "/results?q=pixel+art&page=3")
	assert.Empty(t, w.Header().Get("HX-Push-Url"))
}

func TestResultsRejectAnInvalidPage(t *testing.T) {
	r, _ := newTestRouter()
	assert.Equal(t, http.StatusBadRequest, get(t, r, "/results?page=0").Code)
	assert.Equal(t, http.StatusBadRequest, get(t, r, "/results?page=abc").Code)
}

func TestQueryIsClamped(t *testing.T) {
	// A pathologically long query would otherwise be handed straight to the
	// fuzzy passes, which cost more the longer the input.
	long := strings.Repeat("a", maxQueryLen+50)
	assert.Len(t, clampQuery(long), maxQueryLen)
	assert.Equal(t, "short", clampQuery("  short  "))
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
