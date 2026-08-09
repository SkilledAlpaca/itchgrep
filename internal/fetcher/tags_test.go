package fetcher

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// withBaseURL points browse requests at a test server for the duration of a
// test, restoring the real origin afterwards.
func withBaseURL(t *testing.T, url string) {
	t.Helper()
	prev := baseURL
	baseURL = url
	t.Cleanup(func() { baseURL = prev })
}

// withSeedURLs points both seed sources at a test server.
func withSeedURLs(t *testing.T, dir, sitemap string) {
	t.Helper()
	prevDir, prevMap := tagDirectoryURL, browseSitemapURL
	tagDirectoryURL, browseSitemapURL = dir, sitemap
	t.Cleanup(func() { tagDirectoryURL, browseSitemapURL = prevDir, prevMap })
}

func TestDiscoverySeedPrefersTheTagDirectory(t *testing.T) {
	// The directory is the only seed that reaches the long tail; the sitemap
	// carries ~39 facets against its ~607. Preferring it is the whole point.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/tags/assets":
			fmt.Fprint(w, `<a href="/game-assets/tag-pixel-art">a</a>
				<a href="/game-assets/tag-isometric">b</a>`)
		case "/sitemaps/browse.xml":
			fmt.Fprint(w, `<loc>https://itch.io/game-assets/tag-sitemap-only</loc>`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	withSeedURLs(t, srv.URL+"/tags/assets", srv.URL+"/sitemaps/browse.xml")

	seed, err := discoverySeed()
	require.NoError(t, err)
	assert.Equal(t, []string{"pixel-art", "isometric"}, seed)
}

func TestDiscoverySeedFallsBackToTheSitemap(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/sitemaps/browse.xml" {
			fmt.Fprint(w, `<loc>https://itch.io/game-assets/tag-sitemap-only</loc>`)
			return
		}
		http.Error(w, "gone", http.StatusInternalServerError)
	}))
	defer srv.Close()
	withSeedURLs(t, srv.URL+"/tags/assets", srv.URL+"/sitemaps/browse.xml")

	seed, err := discoverySeed()
	require.NoError(t, err, "a missing tag directory must degrade, not abort the crawl")
	assert.Equal(t, []string{"sitemap-only"}, seed)
}

func TestFetchAssetPageTreatsA404AsExhausted(t *testing.T) {
	// itch.io serves at most MaxPagesPerView pages per view and 404s past the
	// end. That is a slice terminating normally, not a failure, and must not
	// be reported as one.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("page") == "1" {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"num_items":36,"page":1,"content":"<div class=\"game_cell\"></div>"}`)
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()
	withBaseURL(t, srv.URL)

	_, outcome := FetchAssetPage(Slice{}, 1)
	assert.Equal(t, FetchOK, outcome)

	_, outcome = FetchAssetPage(Slice{}, 2)
	assert.Equal(t, FetchExhausted, outcome, "past the end of a view is exhaustion, not failure")
}

func TestFetchAssetPageReportsRealFailures(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()
	withBaseURL(t, srv.URL)

	_, outcome := FetchAssetPage(Slice{}, 1)
	assert.Equal(t, FetchFailed, outcome, "a 500 must stay distinguishable from exhaustion")
}

func TestParseGameCount(t *testing.T) {
	tests := []struct {
		name string
		html string
		want int64
	}{
		{"div form", `<div class="game_count">108,578 results</div>`, 108578},
		{"nobr parenthesised form", `<nobr class="game_count"> (36,324 results)</nobr>`, 36324},
		{"no separators", `<div class="game_count">405 results</div>`, 405},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseGameCount(tt.html)
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestParseGameCountErrorsWhenAbsent(t *testing.T) {
	_, err := parseGameCount(`<div class="something_else">nope</div>`)
	assert.Error(t, err)
}

func TestUniqueTagSlugs(t *testing.T) {
	doc := `
		<a href="/game-assets/tag-pixel-art">pixel art</a>
		<a href="/game-assets/tag-16x16">16x16</a>
		<a href="/game-assets/tag-pixel-art">pixel art again</a>
		<a href="/game-assets/newest">not a tag</a>
		<a href="/games/tag-horror">different classification</a>
	`
	assert.Equal(t, []string{"pixel-art", "16x16"}, uniqueTagSlugs(doc))
}

func TestDiscoverTagsWalksCoTagsFromASeed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/game-assets/tag-pixel-art":
			fmt.Fprint(w, `<div class="game_count">36,324 results</div>
				<a href="/game-assets/tag-sprites">sprites</a>`)
		case "/game-assets/tag-sprites":
			fmt.Fprint(w, `<div class="game_count">15,486 results</div>
				<a href="/game-assets/tag-pixel-art">back-link</a>`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	withBaseURL(t, srv.URL)

	tags, err := DiscoverTags([]string{"pixel-art"}, 0)
	require.NoError(t, err)

	// Seeded with one tag, it should discover the co-tag and not loop on the
	// back-link.
	require.Len(t, tags, 2)
	assert.Equal(t, "pixel-art", tags[0].Slug, "tags come back largest first")
	assert.EqualValues(t, 36324, tags[0].Count)
	assert.Equal(t, "sprites", tags[1].Slug)
	assert.EqualValues(t, 15486, tags[1].Count)
}

func TestDiscoverTagsSurvivesAnUnreachableFacet(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/game-assets/tag-good" {
			fmt.Fprint(w, `<div class="game_count">100 results</div>`)
			return
		}
		http.Error(w, "nope", http.StatusInternalServerError)
	}))
	defer srv.Close()
	withBaseURL(t, srv.URL)

	tags, err := DiscoverTags([]string{"broken", "good"}, 0)
	require.NoError(t, err, "one bad facet must not discard a discovery pass costing hundreds of requests")
	require.Len(t, tags, 1)
	assert.Equal(t, "good", tags[0].Slug)
}

func TestDiscoverTagsRejectsAnEmptySeed(t *testing.T) {
	_, err := DiscoverTags([]string{}, 0)
	assert.Error(t, err)
}
