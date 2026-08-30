package web

import (
	"encoding/xml"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"itchgrep/internal/cache"
	"itchgrep/internal/metrics"
	"itchgrep/internal/storage"
	"itchgrep/pkg/models"
	"itchgrep/pkg/money"

	"github.com/blevesearch/bleve"
	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newTestRouter wires the same routes cmd/webserver does, minus the rate
// limiter, over a cache that has never loaded. That is enough to assert the
// routing, parsing, caching and escaping contracts; result correctness belongs
// to the cache package.
func newTestRouter() (*chi.Mux, *handler) {
	h := NewHandler(cache.NewCache(36), 0, nil)
	r := chi.NewRouter()
	r.Get("/", h.HandleIndex)
	r.Get("/results", h.HandleResults)
	r.Get("/about", h.HandleAbout)
	r.Get("/stats", h.HandleStats)
	r.Get("/new", h.HandleNew)
	r.Get("/feed", h.HandleFeed)
	r.Head("/", h.HandleIndex)
	r.Head("/about", h.HandleAbout)
	r.Head("/stats", h.HandleStats)
	r.Head("/new", h.HandleNew)
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

func TestIndexAgeIsNotClaimedBeforeAnythingHasLoaded(t *testing.T) {
	// The test cache has never loaded, so its update time is the zero value.
	// Rendering that as an age would put "index updated 2025 years ago" in the
	// masthead of a server that is merely still starting up.
	r, _ := newTestRouter()

	assert.NotContains(t, get(t, r, "/").Body.String(), "index updated")
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
	comma := parseFilters(httptest.NewRequest(http.MethodGet, "/?tags=2d,pixel-art", nil), money.Fallback())
	repeated := parseFilters(httptest.NewRequest(http.MethodGet, "/?tags=2d&tags=pixel-art", nil), money.Fallback())

	assert.Equal(t, []string{"2d", "pixel-art"}, comma.Tags)
	assert.Equal(t, comma.Tags, repeated.Tags)
}

func TestJunkFilterValuesAreDropped(t *testing.T) {
	// They can only come from a hand-edited or stale URL. Answering with a 400
	// would put an error page behind a link with an obvious harmless reading.
	f := parseFilters(httptest.NewRequest(http.MethodGet,
		"/?tags=Pixel_Art!&price=cheap&sort=whatever", nil), money.Fallback())

	assert.Empty(t, f.Tags, "a slug with characters itch.io never uses matches nothing")
	assert.Empty(t, f.Price)
	assert.Empty(t, f.Sort)
}

func TestTagsAreNormalisedAndDeduped(t *testing.T) {
	f := parseFilters(httptest.NewRequest(http.MethodGet, "/?tags=+2D+,2d,,pixel-art", nil), money.Fallback())
	assert.Equal(t, []string{"2d", "pixel-art"}, f.Tags)
}

func TestValidFilterValuesAreKept(t *testing.T) {
	f := parseFilters(httptest.NewRequest(http.MethodGet, "/?price=paid&sort=title", nil), money.Fallback())
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

func TestAnOverlongQueryIsCutOnARuneBoundary(t *testing.T) {
	// itch.io is searched in every script there is. Slicing at byte 200 lands
	// mid-character for most of them, and the invalid UTF-8 then travels into
	// the query, the page title and the value of the search box.
	long := strings.Repeat("あ", 300) // 3 bytes each, so byte 200 is mid-rune

	got := clampQuery(long)

	assert.True(t, utf8.ValidString(got), "clamped query must still be valid UTF-8")
	assert.LessOrEqual(t, len(got), maxQueryLen)
	assert.Equal(t, strings.Repeat("あ", 66), got)
}

func TestHeadIsAnsweredRatherThanRejected(t *testing.T) {
	// Uptime monitors, link checkers and preview fetchers send HEAD first. chi
	// routes by method, so a GET-only registration answers them 405 - which
	// reads as "the site is down" to everything that asks that way.
	r, _ := newTestRouter()
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodHead, "/", nil))

	// The same status a GET gets: this cache has never loaded, so both are 503.
	// The point is that the method is routed at all, not what it answers.
	assert.NotEqual(t, http.StatusMethodNotAllowed, w.Code)
	assert.Equal(t, get(t, r, "/").Code, w.Code)
}

func TestStatsHandlerWorksWithNilMetrics(t *testing.T) {
	// newTestRouter wires the handler with a nil *metrics.Counters - the same
	// as any other caller that has no reason to exercise it. HandleStats must
	// render a page, not panic on the nil.
	r, _ := newTestRouter()
	w := get(t, r, "/stats")
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "Traffic")
}

func TestStatsPageCarriesNoPerClientInformation(t *testing.T) {
	// The counters never store an address, a user agent or a query string
	// (see internal/metrics), so this asserts the invariant holds all the way
	// through to the rendered page, not just at the counter.
	m := metrics.New()
	h := NewHandler(cache.NewCache(36), 0, m)
	r := chi.NewRouter()
	r.Get("/stats", h.HandleStats)

	req := httptest.NewRequest(http.MethodGet, "/results?q=pixel+art", nil)
	req.RemoteAddr = "203.0.113.7:54321"
	req.Header.Set("User-Agent", "Mozilla/5.0 (probe)")
	m.RecordRequest("/results", true, http.StatusOK)

	body := get(t, r, "/stats").Body.String()
	assert.NotContains(t, body, "203.0.113.7")
	assert.NotContains(t, body, "Mozilla")
	assert.NotContains(t, body, "q=pixel")
	assert.NotContains(t, body, "pixel art")

	lower := strings.ToLower(body)
	assert.NotContains(t, lower, "probe")
	assert.NotContains(t, lower, "429")
	assert.NotContains(t, lower, "5xx")
}

func TestNewIsUncachedAndUnavailableBeforeTheCacheLoads(t *testing.T) {
	// Same rule as every other page: a server still waiting on its first load
	// answers 503, and that answer must never be cached.
	r, _ := newTestRouter()
	w := get(t, r, "/new")

	assert.Equal(t, http.StatusServiceUnavailable, w.Code)
	assert.Equal(t, "no-store", w.Header().Get("Cache-Control"))
	assert.Contains(t, w.Body.String(), "<title>", "the shell must still render")
}

func TestNewExplainsAnEmptyIndexRatherThanShowingNothing(t *testing.T) {
	// A loaded index built before this feature shipped has no FirstSeen at
	// all, so every query for one comes back empty. That must read as "wait
	// for the next crawl", not as a blank page that looks broken.
	t.Setenv("DATA_DIR", t.TempDir())
	assets := []models.Asset{{GameId: "1", Title: "Old Stock"}}
	require.NoError(t, storage.PutAssets(assets))

	index, err := bleve.New(storage.IndexPath(), storage.IndexMapping())
	require.NoError(t, err)
	for _, a := range assets {
		require.NoError(t, index.Index(a.GameId, models.NewIndexedAsset(a, 0)))
	}
	require.NoError(t, index.Close())

	c := cache.NewCache(36)
	require.NoError(t, c.RefreshDataCache())
	h := NewHandler(c, 0, nil)
	r := chi.NewRouter()
	r.Get("/new", h.HandleNew)

	w := get(t, r, "/new")
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "next weekly crawl")
}

func TestStatsResponseIsCachedBriefly(t *testing.T) {
	r, _ := newTestRouter()
	w := get(t, r, "/stats")
	assert.Equal(t, "public, max-age=60", w.Header().Get("Cache-Control"))
}

// atomFeedXML is an independent decode target for the rendered feed body - it
// mirrors the public shape of an Atom document without depending on the
// templates package's own encoding types, so a successful parse here proves
// something an external feed reader would also accept, not just that Go's
// encoder can read back its own struct.
type atomFeedXML struct {
	XMLName xml.Name       `xml:"feed"`
	ID      string         `xml:"id"`
	Title   string         `xml:"title"`
	Updated string         `xml:"updated"`
	Links   []atomLinkXML  `xml:"link"`
	Entries []atomEntryXML `xml:"entry"`
}

type atomLinkXML struct {
	Rel  string `xml:"rel,attr"`
	Type string `xml:"type,attr"`
	Href string `xml:"href,attr"`
}

type atomEntryXML struct {
	ID     string `xml:"id"`
	Title  string `xml:"title"`
	Author struct {
		Name string `xml:"name"`
	} `xml:"author"`
	Updated string      `xml:"updated"`
	Link    atomLinkXML `xml:"link"`
	Summary struct {
		Type string `xml:"type,attr"`
		Text string `xml:",chardata"`
	} `xml:"summary"`
}

// loadedFeedCache builds a cache over a fixture set shaped for the feed
// tests: 60 genuine arrivals (one more than maxFeedEntries, to exercise the
// cap), one differently-tagged arrival (for filter passthrough), and one
// asset with no FirstSeen at all - old stock that must never appear in a
// feed no matter what else it matches.
func loadedFeedCache(t *testing.T) *cache.Cache {
	t.Helper()
	t.Setenv("DATA_DIR", t.TempDir())

	var assets []models.Asset
	for i := 1; i <= 60; i++ {
		assets = append(assets, models.Asset{
			GameId:        fmt.Sprintf("new-%d", i),
			Title:         fmt.Sprintf("Arrival %d", i),
			Author:        "ana",
			Description:   "a pack of assets",
			Link:          fmt.Sprintf("https://ana.itch.io/arrival-%d", i),
			Price:         "$1.00",
			Tags:          []string{"2d"},
			InvPopularity: int64(i),
			// Strictly increasing, so arrival 60 is always the newest.
			FirstSeen: time.Unix(int64(1_700_000_000+i), 0),
		})
	}
	assets = append(assets,
		models.Asset{
			GameId: "music-1", Title: "Ambient Loops", Author: "cy",
			Description: "audio", Link: "https://cy.itch.io/ambient",
			Tags: []string{"music"}, InvPopularity: 1,
			// Older than every "Arrival N" fixture, so it never displaces
			// arrival-60 as the newest entry - it only needs FirstSeen > 0 to
			// qualify for the feed at all.
			FirstSeen: time.Unix(1_600_000_000, 0),
		},
		models.Asset{
			GameId: "old-1", Title: "Old Stock", Author: "zz",
			Link: "https://zz.itch.io/old", Tags: []string{"2d"}, InvPopularity: 1,
			// FirstSeen left zero: predates the feature, must stay out of /feed.
		},
	)

	require.NoError(t, storage.PutAssets(assets))

	index, err := bleve.New(storage.IndexPath(), storage.IndexMapping())
	require.NoError(t, err)
	batch := index.NewBatch()
	for _, a := range assets {
		require.NoError(t, batch.Index(a.GameId, models.NewIndexedAsset(a, 0)))
	}
	require.NoError(t, index.Batch(batch))
	require.NoError(t, index.Close())

	c := cache.NewCache(36)
	require.NoError(t, c.RefreshDataCache())
	return c
}

func TestFeedIsUncachedAndUnavailableBeforeTheCacheLoads(t *testing.T) {
	// Same rule as every other page: a server still waiting on its first load
	// answers 503, and that answer must never be cached.
	r, _ := newTestRouter()
	w := get(t, r, "/feed")

	assert.Equal(t, http.StatusServiceUnavailable, w.Code)
	assert.Equal(t, "no-store", w.Header().Get("Cache-Control"))
}

func TestFeedIsValidAtomCappedAtFiftyNewestArrivals(t *testing.T) {
	c := loadedFeedCache(t)
	h := NewHandler(c, 0, nil)
	r := chi.NewRouter()
	r.Get("/feed", h.HandleFeed)

	w := get(t, r, "/feed")
	require.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "application/atom+xml; charset=utf-8", w.Header().Get("Content-Type"))
	assert.Equal(t, "public, max-age=3600", w.Header().Get("Cache-Control"))

	var feed atomFeedXML
	require.NoError(t, xml.Unmarshal(w.Body.Bytes(), &feed), "the rendered body must be parseable Atom")

	require.Len(t, feed.Entries, 50, "capped at maxFeedEntries even though 61 assets carry a FirstSeen")
	assert.Equal(t, "https://ana.itch.io/arrival-60", feed.Entries[0].ID, "newest arrival first")
	assert.Equal(t, "Arrival 60", feed.Entries[0].Title)
	assert.Equal(t, "ana", feed.Entries[0].Author.Name)
	assert.Equal(t, "https://ana.itch.io/arrival-60", feed.Entries[0].Link.Href)
	assert.Equal(t, "alternate", feed.Entries[0].Link.Rel)
	assert.Equal(t, time.Unix(1_700_000_060, 0).UTC().Format(time.RFC3339), feed.Entries[0].Updated)
	assert.Contains(t, feed.Entries[0].Summary.Text, "a pack of assets", "description carried through")
	assert.Contains(t, feed.Entries[0].Summary.Text, "$1.00", "price appended to the summary")

	for _, e := range feed.Entries {
		assert.NotEqual(t, "https://zz.itch.io/old", e.ID, "old stock with no FirstSeen must never appear in a feed")
	}

	assert.Equal(t, feed.Entries[0].Updated, feed.Updated, "feed-level updated is the newest entry's FirstSeen")

	var self, alt bool
	for _, l := range feed.Links {
		switch l.Rel {
		case "self":
			self = true
			assert.Equal(t, "https://itchgrep.com/feed", l.Href)
			assert.Equal(t, "application/atom+xml", l.Type)
		case "alternate":
			alt = true
			assert.Equal(t, "https://itchgrep.com/", l.Href)
		}
	}
	assert.True(t, self, "feed must carry a self link")
	assert.True(t, alt, "feed must carry a link back to the html page it describes")
	assert.Equal(t, "https://itchgrep.com/feed", feed.ID)
}

func TestFeedFiltersPassThroughFromTheQueryString(t *testing.T) {
	// Any share URL's query string has to be a valid feed query: the same
	// tags that narrow /?tags=music narrow /feed?tags=music.
	c := loadedFeedCache(t)
	h := NewHandler(c, 0, nil)
	r := chi.NewRouter()
	r.Get("/feed", h.HandleFeed)

	w := get(t, r, "/feed?tags=music")
	require.Equal(t, http.StatusOK, w.Code)

	var feed atomFeedXML
	require.NoError(t, xml.Unmarshal(w.Body.Bytes(), &feed))

	require.Len(t, feed.Entries, 1)
	assert.Equal(t, "https://cy.itch.io/ambient", feed.Entries[0].ID)
}

func TestEmptyFeedIsValidNotAnError(t *testing.T) {
	// A filter matching nothing is a legitimate subscription state - a
	// birdwatcher's saved search for a rare tag combination sits empty for
	// months at a time - so this must render 200 and an empty, still-parseable
	// feed rather than treat "no matches" as a failure.
	c := loadedFeedCache(t)
	h := NewHandler(c, 0, nil)
	r := chi.NewRouter()
	r.Get("/feed", h.HandleFeed)

	w := get(t, r, "/feed?q=nonexistentnonsense")
	require.Equal(t, http.StatusOK, w.Code)

	var feed atomFeedXML
	require.NoError(t, xml.Unmarshal(w.Body.Bytes(), &feed))
	assert.Empty(t, feed.Entries)
	assert.NotEmpty(t, feed.Updated, "an empty feed still needs a valid updated timestamp")
	assert.Equal(t, c.DataUpdatedTime().UTC().Format(time.RFC3339), feed.Updated,
		"falls back to the newest crawl time when there is no entry to take it from")
}

func TestIndexAdvertisesTheFeedForTheCurrentFilters(t *testing.T) {
	// The auto-discovery link has to describe the page it is on, not always
	// the unfiltered feed - otherwise subscribing from a filtered search would
	// silently hand a reader everything instead.
	r, _ := newTestRouter()
	w := get(t, r, "/?tags=2d")

	body := w.Body.String()
	assert.Contains(t, body, `<link rel="alternate" type="application/atom+xml"`)
	assert.Contains(t, body, `href="/feed?tags=2d"`)
}
