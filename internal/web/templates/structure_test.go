package templates

import (
	"strings"
	"testing"

	"itchgrep/pkg/models"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// pageHTML renders the search page inside the shared frame, the way a handler does.
func pageHTML(t *testing.T, shell Shell) string {
	t.Helper()
	return render(t, Layout("T — ITCHGREP", shell, Index(Filters{}, sampleResults())))
}

func TestSkipLinkIsTheFirstFocusableElementAndTargetsTheResults(t *testing.T) {
	html := pageHTML(t, Shell{Search: true})

	body := html[strings.Index(html, "<body>"):]
	assert.True(t, strings.HasPrefix(body, `<body><a class="skip-link" href="#results">Skip to results</a>`),
		"the skip link must be the first thing in <body>: %.120s", body)

	// Nothing focusable precedes it.
	before := body[:strings.Index(body, `<a class="skip-link"`)]
	assert.NotContains(t, before, "<a ")
	assert.NotContains(t, before, "<input")
	assert.NotContains(t, before, "<button")
}

func TestSkipLinkOnOtherPagesGoesToMain(t *testing.T) {
	html := render(t, Layout("About — ITCHGREP", Shell{}, About()))
	assert.Contains(t, html, `<a class="skip-link" href="#main">Skip to content</a>`)
	assert.Contains(t, html, `<main id="main"`)
}

func TestMastheadIsABannerOutsideMain(t *testing.T) {
	// A <header> nested in <main> is not a banner landmark, and "skip to main"
	// would land on the repeated masthead.
	for name, content := range map[string]string{
		"search": pageHTML(t, Shell{Search: true}),
		"about":  render(t, Layout("T", Shell{}, About())),
		"saved":  render(t, Layout("T", Shell{}, SavedPage())),
	} {
		header := strings.Index(content, `<header class="masthead">`)
		headerEnd := strings.Index(content, "</header>")
		main := strings.Index(content, "<main")
		require.True(t, header >= 0 && main >= 0, name)
		assert.True(t, header < headerEnd && headerEnd < main, "%s: masthead must close before <main> opens", name)
		assert.Equal(t, 1, strings.Count(content, `<header`), name+": exactly one header")
		assert.Equal(t, 1, strings.Count(content, "<main"), name)
	}
}

func TestPageContentIsNotWrappedTwice(t *testing.T) {
	// The frame owns <main>; pages no longer open a #page-content of their own.
	assert.NotContains(t, pageHTML(t, Shell{Search: true}), `id="page-content"`)
	assert.NotContains(t, render(t, Layout("T", Shell{}, About())), `id="page-content"`)
}

func TestResultsHeadingIsTheSkipTargetAndFocusTarget(t *testing.T) {
	html := render(t, ResultsRegion(Filters{}, sampleResults()))
	assert.Contains(t, html, `<h2 id="results" class="results-heading" tabindex="-1">Results</h2>`)

	// The heading precedes the first card, so a card's <h3> sits under it.
	assert.Less(t, strings.Index(html, `id="results"`), strings.Index(html, "<h3"))
}

func TestStatusRegionLivesOutsideTheSwappedRegion(t *testing.T) {
	html := render(t, Index(Filters{}, sampleResults()))

	status := strings.Index(html, `<p id="result-status"`)
	region := strings.Index(html, `id="results-region"`)
	require.True(t, status >= 0)
	assert.Less(t, status, region, "the live region must precede, not sit inside, the swap target")
	assert.Equal(t, 1, strings.Count(html, `id="result-status"`))
	assert.Contains(t, html, `<p id="result-status" class="visually-hidden" role="status" aria-live="polite"></p>`,
		"empty on first load, so a fresh page announces nothing")

	// The visible count must not also be a live region, or it announces twice.
	count := html[strings.Index(html, `class="result-count"`):]
	count = count[:strings.Index(count, "</span>")]
	assert.NotContains(t, count, "role=")
	assert.NotContains(t, html[:region], `<title>`, "no <title> in the body")
}

func TestFragmentUpdatesTheStatusOutOfBandAndCarriesTheTitle(t *testing.T) {
	f := Filters{Query: "knight"}.WithTag("pixel-art")
	html := render(t, ResultsFragment(f, sampleResults()))

	assert.True(t, strings.HasPrefix(html, "<title>knight, pixel-art — ITCHGREP</title>"), html[:80])
	// innerHTML, not true: the live-region node must be kept, only its text set.
	assert.Contains(t, html, `<p id="result-status" hx-swap-oob="innerHTML">2 assets for knight, pixel-art</p>`)
	assert.Contains(t, html, `class="results-main"`)
}

func TestFirstPageFragmentRefreshesTheSearchBoxFilters(t *testing.T) {
	// The search box lives outside the swapped region, so without this the next
	// submitted search would carry the filters the page first loaded with.
	f := Filters{Query: "knight", Sort: models.SortTitle, Currency: "EUR", Price: models.PricingFree, Like: "9"}.
		WithTag("pixel-art").WithNotTag("3d")
	html := render(t, ResultsFragment(f, sampleResults()))

	start := strings.Index(html, `<div id="search-filters"`)
	require.GreaterOrEqual(t, start, 0)
	block := html[start : start+strings.Index(html[start:], "</div>")]
	assert.Contains(t, block, `hx-swap-oob="true"`)
	assert.Contains(t, block, `data-query="knight"`)
	for _, want := range []string{
		`name="tags" value="pixel-art"`, `name="not" value="3d"`, `name="price" value="free"`,
		`name="sort" value="title"`, `name="cur" value="EUR"`,
	} {
		assert.Contains(t, block, want)
	}
	assert.NotContains(t, block, `name="like"`)
	assert.NotContains(t, block, `name="q"`)

	// Later pages are cards alone: they change no filter.
	later := sampleResults()
	later.Page = 2
	assert.NotContains(t, render(t, AssetPage(f, later)), "search-filters")
}

func TestRegionAloneHasNoTitleOrOutOfBandNode(t *testing.T) {
	// ResultsRegion is also rendered inside the full page, where either would
	// be a duplicate.
	html := render(t, ResultsRegion(Filters{}, sampleResults()))
	assert.NotContains(t, html, "<title")
	assert.NotContains(t, html, "hx-swap-oob")
	assert.NotContains(t, html, "result-status")
}

func TestPageTitleDescribesEveryFilter(t *testing.T) {
	assert.Equal(t, "ITCHGREP", PageTitle(Filters{}, ""))
	assert.Equal(t, "knight — ITCHGREP", PageTitle(Filters{Query: "knight"}, ""))
	assert.Equal(t, "pixel-art, not 3d, by LimeZu — ITCHGREP",
		PageTitle(Filters{}.WithTag("pixel-art").WithNotTag("3d").WithAuthor("LimeZu"), ""))
	assert.Equal(t, "Free — ITCHGREP", PageTitle(Filters{Price: models.PricingFree}, ""))
	assert.Equal(t, "similar to Tiny Swords — ITCHGREP", PageTitle(Filters{}.WithLike("42"), "Tiny Swords"))
	assert.Equal(t, "similar to game 42 — ITCHGREP", PageTitle(Filters{}.WithLike("42"), ""),
		"an unknown source still changes the page, so it still shows")
	// Sort and currency are reading aids, not a different result set.
	assert.Equal(t, "ITCHGREP", PageTitle(Filters{Sort: models.SortTitle, Currency: "EUR"}, ""))
}

func TestCardTitleIsAHeadingInsideTheLink(t *testing.T) {
	asset := models.Asset{GameId: "7", Title: "Tiny Swords", Description: "A kit.", Link: "https://a.itch.io/t"}
	html := render(t, AssetCard(Filters{}, asset, sampleResults().Rates))

	assert.Contains(t, html, `<h3 class="asset-title">Tiny Swords</h3>`)
	// The link is named by its content, which includes the visible description;
	// aria-labelledby would drop it and fail WCAG 2.5.3.
	assert.NotContains(t, html, "aria-labelledby")
	assert.Less(t, strings.Index(html, `class="asset-link"`), strings.Index(html, "<h3"))
	assert.Less(t, strings.Index(html, "<h3"), strings.Index(html, "</a>"))
}

func TestTagSidebarIsAnOpenDisclosure(t *testing.T) {
	// Open in the markup: with scripting off the list is simply there. app.js
	// folds it on a narrow screen; CSS hides the summary on a wide one.
	html := render(t, TagSidebar(Filters{}, sampleResults()))
	assert.Contains(t, html, `<details class="facets-details" open>`)
	assert.Contains(t, html, `<summary class="facets-toggle">Filter by tag</summary>`)
	assert.Less(t, strings.Index(html, "<summary"), strings.Index(html, "data-facet-search"))
	assert.Less(t, strings.Index(html, "data-facet-search"), strings.Index(html, "</details>"))
}

func TestLoadMoreHasNoIdSoHtmxLeavesFocusAlone(t *testing.T) {
	more := sampleResults()
	more.HasMore = true
	html := render(t, AssetPage(Filters{}, more))
	link := anchorsWith(html, "load-more")
	require.Len(t, link, 1)
	assert.NotContains(t, link[0], ` id=`)
	assert.Contains(t, link[0], `hx-target="this"`)
}

func TestSavedCardTemplateTitleIsAHeading(t *testing.T) {
	assert.Contains(t, render(t, SavedPage()), `<h3 class="asset-title" data-role="title"></h3>`)
}

func TestBadPageOffersTheWayBack(t *testing.T) {
	html := render(t, BadPage("/?tags=2d"))
	assert.Contains(t, html, `class="notice"`)
	assert.Contains(t, html, `<a href="/?tags=2d">Back to the first page</a>`)
}

func TestNotReadyNoticeGivesNoOperatorAdvice(t *testing.T) {
	html := render(t, NotReady())
	assert.NotContains(t, html, "docker")
	assert.Contains(t, html, "being built")
}

func TestFilterRequestsReplaceEachOtherAndScrollPagingNeverCancelsThem(t *testing.T) {
	// Without hx-sync two quick taps race and the slower response wins. The
	// page's own requests replace each other; the load-more trigger yields.
	assert.Contains(t, pageHTML(t, Shell{Search: true}), `hx-sync="this:replace"`)

	more := sampleResults()
	more.HasMore = true
	assert.Contains(t, render(t, AssetPage(Filters{}, more)), `hx-sync="closest .search-page:drop"`)
}
