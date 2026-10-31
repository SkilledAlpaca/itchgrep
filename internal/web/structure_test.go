package web

import (
	"net/http"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEveryPageStartsWithASkipLinkAndAHeaderOutsideMain(t *testing.T) {
	r, _ := newTestRouter()
	for path, skip := range map[string]string{
		"/":      `href="#results">Skip to results`,
		"/about": `href="#main">Skip to content`,
		"/stats": `href="#main">Skip to content`,
		"/new":   `href="#main">Skip to content`,
	} {
		body := get(t, r, path).Body.String()
		assert.Contains(t, body, `<body><a class="skip-link" `+skip, path)
		assert.Less(t, strings.Index(body, "</header>"), strings.Index(body, "<main"), path+": header closes before main")
	}
}

func TestTagPageSkipLinkGoesToResults(t *testing.T) {
	body := get(t, tagRouter(t), "/tag/pixel-art").Body.String()
	assert.Contains(t, body, `<a class="skip-link" href="#results">Skip to results</a>`)
	assert.Contains(t, body, `<h2 id="results"`)
}

func TestFirstPageFragmentCarriesTitleAndStatusAndLaterPagesDoNot(t *testing.T) {
	c := loadedLikeCache(t)
	h := NewHandler(c, 0, nil)
	r := chi.NewRouter()
	r.Get("/results", h.HandleResults)

	body := get(t, r, "/results?tags=2d&not=music").Body.String()
	assert.True(t, strings.HasPrefix(body, "<title>2d, not music — ITCHGREP</title>"), body)
	assert.Contains(t, body, `<p id="result-status" hx-swap-oob="innerHTML">2 assets for 2d, not music</p>`)
	assert.NotContains(t, body, "role=\"status\"", "the live region itself is not re-created")

	// Later pages append cards to the existing list, and the title and count
	// are about the first page.
	later := get(t, r, "/results?tags=2d&page=2").Body.String()
	assert.NotContains(t, later, "<title")
	assert.NotContains(t, later, "result-status")
}

func TestFullPageTitleFollowsEveryFilter(t *testing.T) {
	c := loadedLikeCache(t)
	h := NewHandler(c, 0, nil)
	r := chi.NewRouter()
	r.Get("/", h.HandleIndex)

	body := get(t, r, "/?author=ana&price=free&tags=2d").Body.String()
	assert.Contains(t, body, "<title>2d, by ana, Free — ITCHGREP</title>")
	assert.Equal(t, 1, strings.Count(body, "<title"), "one <title> in the whole document")
	assert.Contains(t, body, `<p id="result-status" class="visually-hidden" role="status" aria-live="polite"></p>`)
}

func TestInvalidPageRendersTheStyledErrorPage(t *testing.T) {
	r, _ := newTestRouter()
	for _, path := range []string{"/?page=abc", "/?q=x&page=0"} {
		w := get(t, r, path)
		body := w.Body.String()
		assert.Equal(t, http.StatusBadRequest, w.Code, path)
		assert.Equal(t, "no-store", w.Header().Get("Cache-Control"), path)
		assert.NotEqual(t, "Invalid page\n", body, path)
		assert.Contains(t, body, `<header class="masthead">`, path)
		assert.Contains(t, body, `class="notice"`, path)
		assert.Contains(t, body, "That page number is not valid", path)
		assert.Contains(t, body, "<title>Invalid page — ITCHGREP</title>", path)
	}
	// The way back keeps the filters but drops the page.
	assert.Contains(t, get(t, r, "/?q=x&page=0").Body.String(), `<a href="/?q=x">Back to the first page</a>`)
}

func TestInvalidPageOnTheFragmentIsANoticeNotADocument(t *testing.T) {
	// htmx swaps this into the results region (app.js lets a 400 through), so a
	// full document would nest a page inside a page.
	r, _ := newTestRouter()
	w := get(t, r, "/results?tags=2d&page=abc")
	body := w.Body.String()

	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Contains(t, body, `class="notice"`)
	assert.Contains(t, body, `<a href="/?tags=2d">Back to the first page</a>`)
	assert.NotContains(t, body, "<html")
	assert.NotContains(t, body, "<header")
}

func TestInvalidPageOnATagPageLinksBackToTheTag(t *testing.T) {
	w := get(t, tagRouter(t), "/tag/pixel-art?page=zzz")
	require.Equal(t, http.StatusBadRequest, w.Code)
	assert.Contains(t, w.Body.String(), `<a href="/tag/pixel-art">Back to the first page</a>`)
}

func TestNoGlobalSlashShortcut(t *testing.T) {
	// WCAG 2.1.4: a single-character shortcut must be removable or remappable.
	// The decision was to remove it rather than build a toggle.
	js, err := staticFS.ReadFile("static/app.js")
	require.NoError(t, err)
	assert.NotContains(t, string(js), `e.key !== '/'`)
	assert.NotContains(t, string(js), `=== '/'`)
}

func TestTheStylesheetHasTheRulesTheTemplatesNeed(t *testing.T) {
	// Classes the templates emit that are easy to lose in a CSS tidy-up: each
	// has no other selector to fall back on, and a missing rule renders a
	// capped chip or facet with link styling and a hover fill.
	css, err := staticFS.ReadFile("static/app.css")
	require.NoError(t, err)
	for _, selector := range []string{
		".skip-link", ".facet.is-capped", ".tag.is-capped", ".tag-more",
		".facets-toggle", ".results-heading", ".asset-grid", ".asset-byline", ".load-more",
	} {
		assert.Contains(t, string(css), selector)
	}
}
