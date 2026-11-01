package web

import (
	"encoding/xml"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"itchgrep/internal/cache"
	"itchgrep/internal/metrics"
	"itchgrep/internal/storage"
	"itchgrep/internal/web/templates"
	"itchgrep/pkg/models"
	"itchgrep/pkg/money"

	"github.com/blevesearch/bleve/v2"
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
	r.Get("/results/tags", h.HandleResultsTags)
	r.Get("/about", h.HandleAbout)
	r.Get("/stats", h.HandleStats)
	r.Get("/new", h.HandleNew)
	r.Get("/feed", h.HandleFeed)
	r.Get("/tag/{slug}", h.HandleTag)
	r.Get("/sitemap.xml", h.HandleSitemap)
	r.Get("/opensearch.xml", HandleOpenSearch)
	r.Head("/", h.HandleIndex)
	r.Head("/about", h.HandleAbout)
	r.Head("/stats", h.HandleStats)
	r.Head("/new", h.HandleNew)
	r.Head("/tag/{slug}", h.HandleTag)
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
	body := get(t, r, "/?price=free&tags=2d%2Cpixel-art").Body.String()

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

func TestTagsAreNormalizedAndDeduped(t *testing.T) {
	f := parseFilters(httptest.NewRequest(http.MethodGet, "/?tags=+2D+,2d,,pixel-art", nil), money.Fallback())
	assert.Equal(t, []string{"2d", "pixel-art"}, f.Tags)
}

func TestLikeAcceptsOnlyDigits(t *testing.T) {
	// itch.io ids are decimal digits only (internal/fetcher.ParseAssetPage
	// reads data-game_id straight off the listing markup); anything else
	// cannot match a real asset and is a typo or a probe either way.
	valid := parseFilters(httptest.NewRequest(http.MethodGet, "/?like=2476825", nil), money.Fallback())
	assert.Equal(t, "2476825", valid.Like)

	for _, bad := range []string{"../etc", "1;drop", "12a", "-1", "1%202", ""} {
		f := parseFilters(httptest.NewRequest(http.MethodGet, "/?like="+bad, nil), money.Fallback())
		assert.Empty(t, f.Like, "%q must not be accepted as a GameId", bad)
	}
}

func TestLikeIsBoundedInLength(t *testing.T) {
	// Keeps a hand-written URL from turning into a pathologically long dataMap
	// key rather than restraining any real id - itch.io's are far shorter.
	tooLong := strings.Repeat("1", maxGameIdLen+1)
	f := parseFilters(httptest.NewRequest(http.MethodGet, "/?like="+tooLong, nil), money.Fallback())
	assert.Empty(t, f.Like)

	atLimit := strings.Repeat("1", maxGameIdLen)
	f = parseFilters(httptest.NewRequest(http.MethodGet, "/?like="+atLimit, nil), money.Fallback())
	assert.Equal(t, atLimit, f.Like)
}

// loadedLikeCache builds a cache with real, itch.io-shaped (numeric) GameIds,
// since ?like= is parsed off the URL and normalizeGameId only accepts
// digits - unlike most fixtures elsewhere in this file, which use readable
// ids that would never survive that parse.
func loadedLikeCache(t *testing.T) *cache.Cache { return loadedLikeCacheSized(t, 36) }

func loadedLikeCacheSized(t *testing.T, pageSize int64) *cache.Cache {
	t.Helper()
	t.Setenv("DATA_DIR", t.TempDir())

	assets := []models.Asset{
		{GameId: "1", Title: "Pixel Tileset", Author: "ana", Link: "https://ana.itch.io/t",
			Tags: []string{"2d", "pixel-art"}, InvPopularity: 1},
		{GameId: "2", Title: "Pixel Icon Pack", Author: "bo", Link: "https://bo.itch.io/i",
			Tags: []string{"2d", "icons"}, InvPopularity: 2},
		{GameId: "3", Title: "Ambient Loops", Author: "cy", Link: "https://cy.itch.io/a",
			Tags: []string{"music"}, InvPopularity: 3},
	}
	require.NoError(t, storage.PutAssets(assets))

	index, err := bleve.New(storage.IndexPath(), storage.IndexMapping())
	require.NoError(t, err)
	for _, a := range assets {
		require.NoError(t, index.Index(a.GameId, models.NewIndexedAsset(a, 0)))
	}
	require.NoError(t, index.Close())

	c := cache.NewCache(pageSize)
	t.Cleanup(func() { _ = c.Close() })
	require.NoError(t, c.RefreshDataCache())
	return c
}

func TestLikeFlowsThroughTheIndexHandler(t *testing.T) {
	// End-to-end through the router: ?like= narrows to the tag overlap with
	// the source asset, excludes the source itself, and the chip names it.
	c := loadedLikeCache(t)
	h := NewHandler(c, 0, nil)
	r := chi.NewRouter()
	r.Get("/", h.HandleIndex)

	w := get(t, r, "/?like=1")
	require.Equal(t, http.StatusOK, w.Code)
	body := w.Body.String()

	assert.Contains(t, body, ">Pixel Icon Pack<", "shares the 2d tag with the source")
	assert.NotContains(t, body, ">Pixel Tileset<", "the source asset must exclude itself")
	assert.NotContains(t, body, "Ambient Loops", "no tag overlap with the source")
	assert.Contains(t, body, "similar to Pixel Tileset", "the chip names the source asset")
}

func TestLikeOnAnUnknownIdRendersAnOrdinaryEmptyPage(t *testing.T) {
	// Never an error page - the same degradation a stale tag or author gets.
	c := loadedLikeCache(t)
	h := NewHandler(c, 0, nil)
	r := chi.NewRouter()
	r.Get("/", h.HandleIndex)

	w := get(t, r, "/?like=999999")
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "No assets match")
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
	assert.Equal(t, "public, max-age=300, s-maxage=3600", w.Header().Get("Cache-Control"),
		"browsers revalidate after five minutes; the edge may hold a page for an hour")
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

func TestResultsRejectAPageLargeEnoughToOverflowTheOffsetMath(t *testing.T) {
	// (page-1)*pageSize overflows int64 for a page this large, wrapping
	// negative and reaching the cache's slicing arithmetic as a negative
	// start/from - previously a panic, not a 400.
	r, _ := newTestRouter()
	assert.Equal(t, http.StatusBadRequest, get(t, r, "/?page=256204778801521552").Code)
	assert.Equal(t, http.StatusBadRequest, get(t, r, "/results?page=256204778801521552").Code)

	// Just over the cap, and at it, so the boundary itself is pinned down
	// rather than just an extreme value on one side of it.
	assert.Equal(t, http.StatusBadRequest, get(t, r, fmt.Sprintf("/results?page=%d", maxPage+1)).Code)
}

func TestStatusForReservesBadRequestForAnUnreachableCase(t *testing.T) {
	// ErrNotReady is the one error a visitor can cause (asking before the
	// first crawl has loaded) and it gets its own 503. Everything else out of
	// cache.Find - a corrupt segment, an IO failure - is the server's fault,
	// not the request's, and must count against 5xx monitoring rather than
	// hide as a 400. The invalid-page case that used to reach here never does
	// any more: pageParam rejects it before the cache is touched.
	assert.Equal(t, http.StatusServiceUnavailable, statusFor(cache.ErrNotReady))
	assert.Equal(t, http.StatusInternalServerError, statusFor(errors.New("corrupt segment")))
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

// machineTimestamps matches the stats page's machine-readable time: the
// counting-since ISO instant. It is digits chosen by the clock, so "429" turns
// up in it whenever the date happens to contain it, which says nothing about
// status codes.
var machineTimestamps = regexp.MustCompile(`datetime="[^"]*"`)

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

	lower := strings.ToLower(machineTimestamps.ReplaceAllString(body, ""))
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
	t.Cleanup(func() { _ = c.Close() })
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
	t.Cleanup(func() { _ = c.Close() })
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

func TestParsingStopsAtTheCaps(t *testing.T) {
	f := parseFilters(httptest.NewRequest(http.MethodGet,
		"/?tags=a,b,c,d,e,f&tags=g&not=h,i,j,k,l,m", nil), money.Fallback())

	assert.Equal(t, []string{"a", "b", "c", "d"}, f.Tags)
	assert.Equal(t, []string{"h", "i", "j", "k"}, f.NotTags)
}

func TestIndexRedirectsToItsCanonicalAddress(t *testing.T) {
	r, _ := newTestRouter()
	for target, want := range map[string]string{
		"/?tags=d,c,b,a":              "/?tags=a%2Cb%2Cc%2Cd",
		"/?tags=a,b,c,d,e,f":          "/?tags=a%2Cb%2Cc%2Cd",
		"/?q=&tags=a&tags=b":          "/?tags=a%2Cb",
		"/?tags=a,a,b":                "/?tags=a%2Cb",
		"/?page=1":                    "/",
		"/?utm_source=x":              "/",
		"/?price=cheap&sort=whatever": "/",
		"/?tags=b,a&page=2":           "/?page=2&tags=a%2Cb",
		"/?q=x&sort=relevance":        "/?q=x",
	} {
		w := get(t, r, target)
		assert.Equal(t, http.StatusFound, w.Code, target)
		assert.Equal(t, want, w.Header().Get("Location"), target)

		// The target must be a fixed point, or this is a redirect loop.
		assert.Equal(t, fmt.Sprintf("public, max-age=%d", fragmentMaxAge), w.Header().Get("Cache-Control"), target)
		again := get(t, r, w.Header().Get("Location"))
		assert.NotEqual(t, http.StatusFound, again.Code, "redirect loop from "+target)
	}
}

func TestCanonicalIndexAddressIsNotRedirected(t *testing.T) {
	r, _ := newTestRouter()
	for _, target := range []string{
		"/", "/?q=pixel+art", "/?tags=a%2Cb", "/?not=a&tags=b", "/?page=2&tags=a",
		"/?author=Ana", "/?price=free", "/?like=123", "/?q=x&sort=title",
	} {
		assert.NotEqual(t, http.StatusFound, get(t, r, target).Code, target)
	}
}

func TestResultsFragmentIsNeverRedirected(t *testing.T) {
	// htmx cannot follow a redirect to a full page, and the fragment's URL is
	// not the one shared or indexed.
	r, _ := newTestRouter()
	for _, target := range []string{"/results?tags=d,c,b,a&page=1", "/results?page=1&tags=a,b,c,d,e,f"} {
		w := get(t, r, target)
		assert.NotEqual(t, http.StatusFound, w.Code, target)
		assert.Empty(t, w.Header().Get("Location"), target)
	}
}

func TestCanonicalURLIsAFixedPoint(t *testing.T) {
	// parseFilters followed by PageURL must be idempotent for any input, or the
	// canonical redirect loops. Add a row for every new filter.
	for _, target := range []string{
		"/?tags=z,y,x,w,v&not=z,u,t,s,r,q",
		"/?tags=a&not=a",
		"/?tags=a,b,c,d,e&not=a",
		"/?tags=a,b,c,d&not=a,e,f,g,h,i",
		"/?q=%20%20spaced%20&author=%20Ana%20",
		"/?like=12&sort=relevance&price=under5&cur=eur&page=3",
		"/?sort=popular&q=x&sort=title",
		"/?tags=A,%20B,c_d,e!",
		// The 200-byte cut: a space lands at the boundary, then a multi-byte
		// rune straddles it, and a deep page rides along.
		"/?q=" + strings.Repeat("a", 199) + "+bbbb",
		"/?q=" + strings.Repeat("a", 198) + "+++bbbb&page=3",
		"/?q=" + strings.Repeat("a", 199) + "%C3%A9tail",
		"/?q=" + strings.Repeat("%E2%82%AC", 70) + "&page=7",
		"/?q=" + strings.Repeat("a", 200) + "+&tags=b,a&page=2",
	} {
		first := parseFilters(httptest.NewRequest(http.MethodGet, target, nil), money.Fallback())
		canonical := first.PageURL(1)
		second := parseFilters(httptest.NewRequest(http.MethodGet, canonical, nil), money.Fallback())
		assert.Equal(t, canonical, second.PageURL(1), target)
		assert.Equal(t, first.Query, second.Query, target)
		assert.True(t, utf8.ValidString(first.Query), target)
		assert.LessOrEqual(t, len(first.Query), maxQueryLen, target)
		assert.Equal(t, strings.TrimSpace(first.Query), first.Query, target)
	}
}

func TestStaticAssetCachingFollowsTheFingerprint(t *testing.T) {
	r := chi.NewRouter()
	r.Handle("/static/*", StaticHandler())
	do := func(path string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		return w
	}

	for _, name := range []string{"app.css", "app.js", "htmx.min.js"} {
		fingerprinted := do(templates.Asset(name))
		assert.Equal(t, http.StatusOK, fingerprinted.Code, name)
		assert.Equal(t, "public, max-age=31536000, immutable", fingerprinted.Header().Get("Cache-Control"), name)
		assert.NotEmpty(t, fingerprinted.Body.Bytes(), name)

		bare := do("/static/" + name)
		assert.Equal(t, http.StatusOK, bare.Code, name)
		assert.Equal(t, "public, max-age=300", bare.Header().Get("Cache-Control"), name)
		assert.Equal(t, fingerprinted.Body.String(), bare.Body.String(), name)
	}

	// HTML cached across a deploy names the previous hashes: serve the current
	// file, but never pin it under the wrong hash.
	current := do(templates.Asset("app.css"))
	stale := do("/static/app.000000000000.css")
	assert.Equal(t, http.StatusOK, stale.Code)
	assert.Equal(t, "public, max-age=300", stale.Header().Get("Cache-Control"))
	assert.Equal(t, current.Body.String(), stale.Body.String())

	for _, path := range []string{"/static/nope.css", "/static/app.notahash.css", "/static/app.000000000000.png", "/static/", "/static/static.go", "/static/VENDOR.md",
		"/static/static_test.go", "/static/app.css/", "/static/../static.go"} {
		assert.Equal(t, http.StatusNotFound, do(path).Code, path)
	}
}

func TestFingerprintedAssetsCountAsStaticTraffic(t *testing.T) {
	// The /stats breakdown is keyed on chi's route pattern, so the hash in the
	// path must not leak into the bucket.
	m := metrics.New()
	r := chi.NewRouter()
	r.Use(RequestLogger(false, nil, m))
	r.Handle("/static/*", StaticHandler())

	r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, templates.Asset("app.css"), nil))
	r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/static/app.js", nil))

	assert.Equal(t, uint64(2), m.Snapshot().Static)
}

func TestRelevanceWithoutAQueryIsDroppedAndRedirected(t *testing.T) {
	r, _ := newTestRouter()

	w := get(t, r, "/?sort=relevance")
	assert.Equal(t, http.StatusFound, w.Code)
	assert.Equal(t, "/", w.Header().Get("Location"))

	// With a query relevance is the default, so it is dropped from the address
	// as before; only the query remains.
	w = get(t, r, "/?q=x&sort=relevance")
	assert.Equal(t, "/?q=x", w.Header().Get("Location"))
}

func TestAPageBeyondTheLastRendersANoticeNotAnEmptyGrid(t *testing.T) {
	c := loadedLikeCache(t)
	h := NewHandler(c, 0, nil)
	r := chi.NewRouter()
	r.Get("/", h.HandleIndex)
	r.Get("/results", h.HandleResults)

	for _, target := range []string{"/?page=5", "/?page=5&tags=2d", "/results?page=5"} {
		w := get(t, r, target)
		assert.Equal(t, http.StatusOK, w.Code, target)
		body := w.Body.String()
		assert.Contains(t, body, "No more results", target)
		assert.Contains(t, body, "Back to the first page", target)
		assert.NotContains(t, body, `class="asset"`, target)
		assert.Equal(t, "public, max-age=300", w.Header().Get("Cache-Control"), target)
	}
}

func TestResultsFragmentRefreshesSearchFiltersOnPageOneOnly(t *testing.T) {
	c := loadedLikeCache(t)
	h := NewHandler(c, 0, nil)
	r := chi.NewRouter()
	r.Get("/results", h.HandleResults)

	first := get(t, r, "/results?tags=2d&not=music&price=paid&sort=title&cur=EUR&q=pixel").Body.String()
	assert.Contains(t, first, `id="search-filters"`)
	assert.Contains(t, first, `hx-swap-oob="true"`)
	assert.Contains(t, first, `name="tags" value="2d"`)
	assert.Contains(t, first, `name="not" value="music"`)

	// A second page needs more results than the fixture has; the empty
	// out-of-range page is still a later page and must not carry the fields.
	later := get(t, r, "/results?tags=2d&page=2").Body.String()
	assert.NotContains(t, later, "search-filters")
}

func TestLoadMorePagesSkipTheFacetButFullPagesKeepTheSidebar(t *testing.T) {
	// One asset per page, three matching "2d"-or-music assets overall.
	h := NewHandler(loadedLikeCacheSized(t, 1), 0, nil)
	f := templates.Filters{Query: "pixel"}

	first, err := h.find(f, 1, true)
	require.NoError(t, err)
	assert.NotEmpty(t, first.Tags, "a first page replaces the sidebar")

	more, err := h.find(f, 2, false)
	require.NoError(t, err)
	require.NotEmpty(t, more.Assets)
	assert.Empty(t, more.Tags, "a load-more page appends cards only, so no facet work")

	deep, err := h.find(f, 2, true)
	require.NoError(t, err)
	assert.NotEmpty(t, deep.Tags, "a full page render of page 2 still has its sidebar")

	r := chi.NewRouter()
	r.Get("/", h.HandleIndex)
	assert.Contains(t, get(t, r, "/?page=2&q=pixel").Body.String(), `class="facets"`)
}

func TestAFailedFirstPageStillRefreshesTheSearchBoxFilters(t *testing.T) {
	// The unloaded test cache answers 503. The address was pushed, so the form
	// has to follow it.
	r, _ := newTestRouter()

	w := get(t, r, "/results?tags=2d&not=3d&sort=title&cur=EUR&q=knight")
	require.Equal(t, http.StatusServiceUnavailable, w.Code)
	body := w.Body.String()
	assert.Contains(t, body, `id="search-filters"`)
	assert.Contains(t, body, `hx-swap-oob="true"`)
	assert.Contains(t, body, `name="tags" value="2d"`)
	assert.Contains(t, body, `name="not" value="3d"`)
	assert.Contains(t, body, `name="sort" value="title"`)

	later := get(t, r, "/results?tags=2d&page=2")
	require.Equal(t, http.StatusServiceUnavailable, later.Code)
	assert.NotContains(t, later.Body.String(), "search-filters", "a later page changed no filter")
}
