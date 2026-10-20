package templates

import (
	"context"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"itchgrep/internal/metrics"
	"itchgrep/pkg/models"
	"itchgrep/pkg/money"

	"github.com/a-h/templ"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func render(t *testing.T, c templ.Component) string {
	t.Helper()
	var sb strings.Builder
	require.NoError(t, c.Render(context.Background(), &sb))
	return sb.String()
}

func sampleResults() Results {
	return Results{
		Assets: []models.Asset{
			{GameId: "1", Title: "Pixel Tileset", Author: "ana", Link: "https://a.itch.io/t",
				Tags: []string{"2d", "pixel-art"}},
			{GameId: "2", Title: "Sprite Pack", Author: "bo", Link: "https://b.itch.io/s",
				Tags: []string{"2d"}, Price: "$4.95"},
		},
		Total: 2,
		Tags:  []models.Tag{{Slug: "2d", Count: 1740}, {Slug: "pixel-art", Count: 903}},
		Page:  1,
	}
}

func TestAttributionSurvivesOnEveryPage(t *testing.T) {
	// The masthead and the about page are the only two places a visitor is told
	// who wrote this and where to get the code. GPL-3.0 makes the source link
	// an obligation rather than a courtesy, and it is exactly the sort of thing
	// a later redesign drops without noticing.
	for name, html := range map[string]string{
		"masthead": render(t, Masthead(Freshness{}, Coverage{})),
		"about":    render(t, About()),
	} {
		assert.Contains(t, html, AuthorURL, name+" must credit the original author")
		assert.Contains(t, html, SourceURL, name+" must link the source it runs")
		assert.NotContains(t, html, "buymeacoffee", name+" no longer solicits donations")
	}
}

func TestStatsLinksToAboutAsAPlainNavigation(t *testing.T) {
	// The index page swaps /about into #page-content via htmx to keep the
	// search form's state alive. The stats page copied that link, but it has
	// no state to keep - the swap left visitors reading about-content under a
	// /stats URL, where refresh and share both resurrect the wrong page. Here
	// the link must be an ordinary navigation.
	html := render(t, StatsPage(Freshness{}, Coverage{}, NewTraffic(metrics.Snapshot{}, time.Now()), History{}))

	assert.Contains(t, html, `href="/about"`)
	assert.NotContains(t, html, `hx-get="/about"`,
		"stats must navigate to /about, not swap it in place")
}

func TestStatsPageRendersEveryBreakdownRow(t *testing.T) {
	// The breakdown is exhaustive at the counter (see internal/metrics), and
	// this asserts the page actually shows every bucket rather than stopping
	// short of the ones added alongside routeOther.
	snap := metrics.Snapshot{New: 12, Feed: 34, Tag: 56, Saved: 78, Other: 90}
	html := render(t, StatsPage(Freshness{}, Coverage{}, NewTraffic(snap, time.Now()), History{}))

	assert.Contains(t, html, "New arrivals page")
	assert.Contains(t, html, "12")
	assert.Contains(t, html, "Feed requests")
	assert.Contains(t, html, "34")
	assert.Contains(t, html, "Tag pages")
	assert.Contains(t, html, "56")
	assert.Contains(t, html, "Saved page")
	assert.Contains(t, html, "78")
	assert.Contains(t, html, ">Other<")
	assert.Contains(t, html, "90")
}

func TestStatsHeadlineRendersFourCellsInOrder(t *testing.T) {
	// The headline separates the four numbers a first glance wants - total
	// volume, search volume, how long this process has run, and how far
	// back the count goes - from the per-route breakdown below, so it reads
	// as a summary rather than one undifferentiated grid.
	snap := metrics.Snapshot{Total: 100, Searches: 40, Started: now.Add(-90 * time.Minute)}
	html := render(t, StatsPage(Freshness{}, Coverage{}, NewTraffic(snap, now), History{}))

	require.Contains(t, html, `<dl class="stats-grid stats-headline">`)
	headline := html[strings.Index(html, `<dl class="stats-grid stats-headline">`):]
	headline = headline[:strings.Index(headline, "</dl>")]

	total := strings.Index(headline, "Total requests")
	searches := strings.Index(headline, "Searches served")
	uptime := strings.Index(headline, "Uptime")
	countingSince := strings.Index(headline, "Counting since")
	require.True(t, total >= 0 && total < searches && searches < uptime && uptime < countingSince,
		"the headline cells must render in this order")
}

func TestStatsTableListsEveryRouteInAFixedOrder(t *testing.T) {
	// The order mirrors how a reader thinks about the site: whole pages
	// before the fragment and asset requests they trigger, then the
	// specialised pages, ending with the catch-all overflow bucket.
	html := render(t, StatsPage(Freshness{}, Coverage{}, NewTraffic(metrics.Snapshot{}, now), History{}))

	require.Contains(t, html, `<table class="stats-table">`)
	table := html[strings.Index(html, `<table class="stats-table">`):]
	labels := []string{
		"Home page", "Results fragment", "About page", "Static assets",
		"New arrivals page", "Feed requests", "Tag pages", "Saved page", "Other",
	}
	last := -1
	for _, label := range labels {
		idx := strings.Index(table, label)
		require.True(t, idx >= 0, label+" must appear in the route table")
		require.True(t, idx > last, label+" must appear after the previous row")
		last = idx
	}
}

func TestCountingSinceRendersAsALocalisableTimeElement(t *testing.T) {
	// app.js rewrites this to the viewer's own zone on load; it needs a real
	// datetime attribute to parse an instant from, and the js-local class is
	// what tells app.js this element's whole text, not just its title,
	// should be replaced.
	firstSeen := now.Add(-30 * 24 * time.Hour)
	tr := NewTraffic(metrics.Snapshot{FirstSeen: firstSeen}, now)
	html := render(t, StatsPage(Freshness{}, Coverage{}, tr, History{}))

	assert.Contains(t, html, `class="js-local"`)
	assert.Contains(t, html, `datetime="`+firstSeen.UTC().Format(time.RFC3339)+`"`)
}

func TestHistogramLabelsEveryThirdBar(t *testing.T) {
	// 24 bars with a label under each would crowd into illegibility; a label
	// under every third one keeps the axis readable while still spanning the
	// full day it describes.
	html := render(t, StatsPage(Freshness{}, Coverage{}, NewTraffic(metrics.Snapshot{}, now), History{}))

	assert.Equal(t, 24, strings.Count(html, `class="stats-bar-col"`), "one column per hour")
	assert.Equal(t, 8, strings.Count(html, `class="stats-bar-label"`), "a label on every third bar")

	epochs := regexp.MustCompile(`data-epoch="(\d+)"`).FindAllStringSubmatch(html, -1)
	require.Len(t, epochs, 8)
	for _, m := range epochs {
		epoch, err := strconv.ParseInt(m[1], 10, 64)
		require.NoError(t, err)
		assert.Zero(t, epoch%3600, "data-epoch must be an hour boundary in unix seconds")
	}
}

func TestEmptyHistoryRendersNoSection(t *testing.T) {
	// A deploy before this feature shipped, or one with no crawl since, has
	// nothing recorded - the section must not appear at all rather than
	// appear with nothing in it.
	html := render(t, StatsPage(Freshness{}, Coverage{}, NewTraffic(metrics.Snapshot{}, time.Now()), History{}))

	assert.NotContains(t, html, "Index history")
	assert.NotContains(t, html, "history-row")
}

func TestHistorySectionRendersRowsNewestFirstWithDeltas(t *testing.T) {
	history := NewHistory([]models.CrawlRecord{
		{When: time.Date(2026, 8, 20, 0, 0, 0, 0, time.UTC), Indexed: 95699, Catalogue: 108000,
			DurationSeconds: 3600, Pages: 4000, SlicesDone: 300, SlicesPlanned: 320},
		{When: time.Date(2026, 8, 29, 0, 0, 0, 0, time.UTC), Indexed: 96903, Catalogue: 108697,
			DurationSeconds: 3600 + 12*60, Pages: 4200, SlicesDone: 314, SlicesPlanned: 320, Resumed: true},
	})
	html := render(t, StatsPage(Freshness{}, Coverage{}, NewTraffic(metrics.Snapshot{}, time.Now()), history))

	assert.Contains(t, html, "Index history")
	assert.Contains(t, html, "Aug 29, 2026 — 96,903 assets (+1,204)",
		"the newest crawl's delta is against the previous one")
	assert.Contains(t, html, "first recorded crawl",
		"the oldest row has nothing to diff against")
	assert.Contains(t, html, "resumed from checkpoint")

	// Only the resumed row says so.
	newest := strings.Index(html, "Aug 29, 2026")
	oldest := strings.Index(html, "Aug 20, 2026")
	require.True(t, newest >= 0 && oldest >= 0 && newest < oldest,
		"newest row must render before the oldest")
}

func TestHistoryDeltaUsesARealMinusSign(t *testing.T) {
	history := NewHistory([]models.CrawlRecord{
		{Indexed: 96903},
		{Indexed: 95699},
	})
	html := render(t, StatsPage(Freshness{}, Coverage{}, NewTraffic(metrics.Snapshot{}, time.Now()), history))

	assert.Contains(t, html, "−1,204", "a shrinking index must not read as a hyphenated range")
	assert.NotContains(t, html, "-1,204")
}

func TestAboutDoesNotSpeakAsTheOriginalAuthor(t *testing.T) {
	// This page used to read "I built itchgrep.com" and list the original
	// author's email and socials. On a fork run by somebody else that claims
	// their identity and routes support at someone who cannot answer for it.
	html := render(t, About())

	assert.NotContains(t, html, "I built")
	assert.NotContains(t, html, "mailto:", "contact details belonged to the original author")
	assert.Contains(t, html, "not operated by the", "the split must be stated, not implied")
}

func TestSidebarRendersTagsWithCounts(t *testing.T) {
	html := render(t, ResultsRegion(Filters{}, sampleResults()))

	assert.Contains(t, html, `data-facet="pixel-art"`)
	assert.Contains(t, html, "1,740", "counts are what make the sidebar a map rather than a list")
	// The link must be navigable without scripting, and must also drive htmx.
	assert.Contains(t, html, `href="/?tags=2d"`)
	assert.Contains(t, html, `hx-get="/results?page=1&amp;tags=2d"`)
}

func TestSidebarRendersTheTailHiddenForTheTagSearch(t *testing.T) {
	// Only the head shows at rest, but every counted tag must be on the page,
	// or the find-a-tag box can only ever match the forty already visible.
	r := sampleResults()
	r.Tags = nil
	for i := 0; i <= sidebarTags; i++ {
		r.Tags = append(r.Tags, models.Tag{Slug: "tag-" + strconv.Itoa(i), Count: int64(100 - i)})
	}
	html := render(t, ResultsRegion(Filters{}, r))

	last := strconv.Itoa(sidebarTags - 1)
	assert.Contains(t, html, `<li data-facet="tag-`+last+`">`, "the head shows at rest")
	tail := strconv.Itoa(sidebarTags)
	assert.Contains(t, html, `<li data-facet="tag-`+tail+`" data-facet-tail hidden>`,
		"the tail is rendered, hidden, for the tag search to find")
}

func TestAppliedFiltersRenderAsRemovableChips(t *testing.T) {
	// Without these an applied tag is invisible once scrolled past, and an empty
	// result set looks like a broken search rather than an over-constrained one.
	f := Filters{Query: "tileset", Price: models.PricingFree}.WithTag("2d")
	html := render(t, ResultsRegion(f, sampleResults()))

	assert.Contains(t, html, "Filtering by")
	assert.Contains(t, html, `href="/?price=free&amp;q=tileset"`, "the 2d chip removes itself")
	assert.Contains(t, html, `href="/?q=tileset&amp;tags=2d"`, "the free chip removes itself")
	assert.Contains(t, html, "Clear filters")
}

func TestAnAppliedTagIsNotOfferedAgainOnACard(t *testing.T) {
	// A card offering a filter that would change nothing is a dead control.
	html := render(t, ResultsRegion(Filters{}.WithTag("2d"), sampleResults()))
	assert.Contains(t, html, `class="tag is-active"`)
}

func TestPriceBadgeDistinguishesFreeFromPaid(t *testing.T) {
	html := render(t, ResultsRegion(Filters{}, sampleResults()))
	assert.Contains(t, html, `class="asset-price is-free">Free`)
	assert.Contains(t, html, `class="asset-price">$4.95`)
}

func TestRelevanceIsOnlyOfferedWhenThereIsAQuery(t *testing.T) {
	// With nothing searched for, every document scores the same, so a "best
	// match" button would sort by nothing while claiming to rank.
	assert.NotContains(t, render(t, SortControl(Filters{}, false)), "Best match")
	assert.Contains(t, render(t, SortControl(Filters{Query: "x"}, false)), "Best match")
}

func TestTheLoadMoreTriggerOnlyAppearsWhenMoreExist(t *testing.T) {
	// Letting an empty response be the stop signal costs one wasted request at
	// the bottom of every list - a full index pass returning nothing.
	last := sampleResults()
	assert.NotContains(t, render(t, AssetPage(Filters{}, last)), "asset-load-trigger")

	more := sampleResults()
	more.HasMore = true
	html := render(t, AssetPage(Filters{}, more))
	assert.Contains(t, html, "asset-load-trigger")
	assert.Contains(t, html, `hx-get="/results?page=2"`)
}

func TestEmptyResultsExplainWhichWayToWiden(t *testing.T) {
	// The fix differs: a search that found nothing wants a broader term,
	// whereas one narrowed by four tags wants a tag removed.
	empty := Results{Page: 1}

	searched := render(t, AssetPage(Filters{Query: "zzz"}, empty))
	assert.Contains(t, searched, "No assets match")
	assert.Contains(t, searched, "broader term")

	narrowed := render(t, AssetPage(Filters{}.WithTag("fonts").WithTag("music"), empty))
	assert.Contains(t, narrowed, "combined with AND")
	assert.Contains(t, narrowed, "Clear filters")
}

func TestPayWhatYouWantIsNotJustCalledFree(t *testing.T) {
	// An author asking for a voluntary payment is doing something different
	// from giving the asset away, and "Free" hides the difference entirely.
	r := sampleResults()
	r.Assets = []models.Asset{
		{GameId: "1", Title: "Tip Jar Tiles", PayWhatYouWant: true},
		{GameId: "2", Title: "Minimum Pack", Price: "$4.95", PayWhatYouWant: true},
		{GameId: "3", Title: "Plain Free", Price: ""},
	}
	html := render(t, AssetPage(Filters{}, r))

	assert.Contains(t, html, "Name your price")
	assert.Contains(t, html, "$4.95+", "a minimum price is a floor, not the price")
	assert.Contains(t, html, ">Free<")
}

func TestConvertedPricesAreMarkedApproximateAndSourced(t *testing.T) {
	// A converted figure is this site's arithmetic, not the seller's offer. It
	// has to be visibly derived, and the original has to stay reachable.
	r := sampleResults()
	r.Rates = money.Fallback()
	html := render(t, ResultsRegion(Filters{Currency: "EUR"}, r))

	assert.Contains(t, html, "≈", "a conversion must never read as the quoted price")
	assert.Contains(t, html, "Listed at $4.95", "the figure itch.io quoted stays on hover")
	assert.Contains(t, html, money.Fallback().Date, "a rate without its date is a claim about today")
	assert.Contains(t, html, money.ECBSource, "and one without a source is unverifiable")
}

func TestPricesAreLeftAloneWithoutACurrencyChoice(t *testing.T) {
	r := sampleResults()
	r.Rates = money.Fallback()
	html := render(t, ResultsRegion(Filters{}, r))

	assert.Contains(t, html, ">$4.95<")
	assert.NotContains(t, html, "≈")
}

func TestRecencyIsOnlyOfferedWhenTheDataCarriesIt(t *testing.T) {
	// Same rule as relevance-without-a-query: an ordering the data cannot
	// support is worse than an ordering that is absent.
	assert.NotContains(t, render(t, SortControl(Filters{}, false)), "Recently added")
	assert.Contains(t, render(t, SortControl(Filters{}, true)), "Recently added")
}

func TestCoverageIsStatedWithItsDenominator(t *testing.T) {
	// "96,903 assets" with nothing to compare it against reads as the whole
	// catalogue, and a search that then finds nothing looks like proof the asset
	// does not exist rather than that it was never indexed.
	age := NewFreshness(time.Now().Add(-time.Hour), time.Now(), 0)
	html := render(t, IndexAge(age, Coverage{Indexed: 96903, Catalogue: 108808}))

	assert.Contains(t, html, "89% of catalogue indexable")
	// On its own line, not trailing the age: the two say different kinds of
	// thing, and run together they read as one sentence about neither. Styled
	// as the age is, because they are the same kind of caveat.
	assert.Contains(t, html, `<p class="index-age index-coverage">`)
	// The exact counts stay reachable on hover rather than in the line itself.
	assert.Contains(t, html, "96,903 of 108,808 assets")
	assert.NotContains(t, html, "visually-hidden", "the detail is not repeated inline")
}

func TestCoverageIsSilentWhenItWasNeverMeasured(t *testing.T) {
	// An index published before crawls recorded their completeness has no
	// figure. Rendering 0% would report a total failure that did not happen.
	age := NewFreshness(time.Now().Add(-time.Hour), time.Now(), 0)
	html := render(t, IndexAge(age, Coverage{}))

	assert.Contains(t, html, "index updated")
	assert.NotContains(t, html, "of catalogue indexable")
	assert.NotContains(t, html, "0%")
}

func TestCoverageNeverExceedsAHundredPercent(t *testing.T) {
	// The catalogue total is read when a crawl starts and the indexed count when
	// it ends, so a catalogue that shrinks in between can produce more than
	// 100%. That is the measurement, not a discovery.
	assert.Equal(t, 100, Coverage{Indexed: 110, Catalogue: 100}.Percent())
}

func TestMoreResultsAreReachableWithoutScrolling(t *testing.T) {
	// "revealed" only fires for someone scrolling a viewport. As a bare sentinel
	// div this left keyboard and screen-reader users unable to reach anything
	// past the first page at all.
	more := sampleResults()
	more.HasMore = true
	html := render(t, AssetPage(Filters{}.WithTag("2d"), more))

	assert.Contains(t, html, "Load more results", "it must be operable, not just observable")
	assert.Contains(t, html, `hx-trigger="revealed, click"`)
	assert.Contains(t, html, `href="/?page=2&amp;tags=2d"`, "and a real link with scripting off")
}

func TestBoundedPriceFiltersNeedConvertedPrices(t *testing.T) {
	// An index built before PriceUSD existed has no document carrying it, so a
	// numeric range over it matches nothing. Rendering the buttons anyway gives
	// two controls that always return an empty page - which reads as "itch.io
	// has no assets under $5" rather than "this index cannot answer that".
	// Free and paid come straight off the listing, so they survive either way.
	without := render(t, PriceControl(Filters{}, false))
	assert.NotContains(t, without, "Under $5")
	assert.NotContains(t, without, "Under $20")
	assert.Contains(t, without, "Free")
	assert.Contains(t, without, "Paid")

	with := render(t, PriceControl(Filters{}, true))
	assert.Contains(t, with, "Under $5")
	assert.Contains(t, with, "Under $20")
}

func TestExclusionsRenderAsTheirOwnKindOfChip(t *testing.T) {
	// An exclusion is the opposite of a filter, so it must not look like one -
	// otherwise "not pixel-art" reads as "pixel-art" at a glance.
	f := Filters{}.WithTag("2d").WithNotTag("pixel-art")
	html := render(t, ResultsRegion(f, sampleResults()))

	assert.Contains(t, html, "is-excluded")
	assert.Contains(t, html, "not pixel-art")
	assert.Contains(t, html, `href="/?not=pixel-art&amp;tags=2d"`, "the 2d chip removes only itself")
}

func TestEveryFacetOffersBothDirections(t *testing.T) {
	html := render(t, ResultsRegion(Filters{}, sampleResults()))

	assert.Contains(t, html, `href="/?tags=pixel-art"`, "narrow to the tag")
	assert.Contains(t, html, `href="/?not=pixel-art"`, "or away from it")
}

func TestTheAuthorOnACardFiltersToThatAuthor(t *testing.T) {
	html := render(t, ResultsRegion(Filters{}, sampleResults()))
	assert.Contains(t, html, `href="/?author=ana"`)
}

func TestArrivalsPageRendersCardsNewestFirst(t *testing.T) {
	assets := []models.Asset{
		{GameId: "1", Title: "Freshly Arrived", Link: "https://a.itch.io/f"},
	}
	age := NewFreshness(time.Date(2026, 8, 27, 0, 0, 0, 0, time.UTC), time.Date(2026, 8, 29, 0, 0, 0, 0, time.UTC), 0)
	html := render(t, ArrivalsPage(assets, money.Fallback(), age, Coverage{}))

	assert.Contains(t, html, "New this week")
	assert.Contains(t, html, "Freshly Arrived")
	assert.Contains(t, html, "2 days ago", "the window is stated from the actual crawl time, not asserted blindly")
}

func TestArrivalsPageExplainsAnEmptyIndex(t *testing.T) {
	// An index built before this feature shipped has nothing stamped yet. That
	// has to read as "wait for the next crawl", not as a page that looks
	// broken or forgot to render its results.
	html := render(t, ArrivalsPage(nil, money.Fallback(), Freshness{}, Coverage{}))

	assert.Contains(t, html, "next weekly crawl")
	assert.NotContains(t, html, `id="asset-list"`)
}

func TestSaveButtonCarriesTheFieldsNeededToRebuildTheCard(t *testing.T) {
	// /saved never queries the server - app.js rebuilds each card from
	// whatever the button that saved it was carrying, so every field it
	// reads has to actually be on the markup.
	r := sampleResults()
	r.Assets = []models.Asset{
		{GameId: "42", Title: "Pixel Tileset", Author: "ana", Link: "https://a.itch.io/t",
			ThumbUrl: "https://img.itch.zone/t.png", Price: "$4.95"},
	}
	html := render(t, AssetPage(Filters{}, r))

	assert.Contains(t, html, "data-save-toggle")
	assert.Contains(t, html, `data-game-id="42"`)
	assert.Contains(t, html, `data-title="Pixel Tileset"`)
	assert.Contains(t, html, `data-author="ana"`)
	assert.Contains(t, html, `data-link="https://a.itch.io/t"`)
	assert.Contains(t, html, `data-thumb="https://img.itch.zone/t.png"`)
	assert.Contains(t, html, `data-price="$4.95"`)
	assert.Contains(t, html, `aria-pressed="false"`, "unsaved is the only state the server can render")
}

func TestSaveButtonUsesTheDisplayedPriceNotTheRawOne(t *testing.T) {
	// A pay-what-you-want or free asset does not carry a plain price string -
	// the badge already resolves that, so the button has to read the same
	// resolved label rather than the raw (possibly empty) Price field.
	r := sampleResults()
	r.Assets = []models.Asset{{GameId: "1", Title: "Tip Jar", PayWhatYouWant: true}}
	html := render(t, AssetPage(Filters{}, r))

	assert.Contains(t, html, `data-price="Name your price"`)
}

func TestSavedPageStatesItsPrivacyPosture(t *testing.T) {
	// The one thing this page must never leave implicit: nothing saved here
	// is sent to, or knowable by, the server.
	html := render(t, SavedPage(Freshness{}, Coverage{}))

	assert.Contains(t, html, "never")
	assert.Contains(t, html, "this browser")
}

func TestSavedPageDegradesHonestlyWithoutScripting(t *testing.T) {
	// This is the one page on the site that cannot work with scripting off -
	// a saved list kept off the server has nowhere else to come from - so it
	// has to say that rather than render a silently empty grid.
	html := render(t, SavedPage(Freshness{}, Coverage{}))

	assert.Contains(t, html, "<noscript>")
	assert.Contains(t, html, "needs JavaScript")
}

func TestSavedPageShellHasAnEmptyGridAndACardTemplate(t *testing.T) {
	// Every card comes from localStorage via app.js; the server has nothing
	// of its own to put in the grid, only the empty container and the
	// <template> app.js clones from - so the grid it renders has to start
	// with exactly one child, the (initially hidden) empty-state notice.
	html := render(t, SavedPage(Freshness{}, Coverage{}))

	assert.Contains(t, html, `id="asset-list" data-saved-grid`)
	assert.Contains(t, html, `id="saved-card-template"`)
	assert.Contains(t, html, `id="saved-empty" class="notice" hidden`)

	gridStart := strings.Index(html, `id="asset-list"`)
	templateStart := strings.Index(html, "<template")
	require.True(t, gridStart >= 0 && templateStart > gridStart,
		"the grid must be empty of cards; the template lives outside it")
}

func TestAssetLinkAndThumbAreSanitizedAgainstAScriptScheme(t *testing.T) {
	// asset.Link and asset.ThumbUrl are scraped verbatim from itch.io's own
	// markup, not constructed by this site - exactly the untrusted input that
	// must go through templ.URL's sanitization rather than templ.SafeURL's
	// bare type assertion. A javascript: href or src must never survive
	// unchanged into the rendered page.
	//
	// The data-link and data-thumb attributes are a different matter: app.js
	// reads those to rebuild a saved card without ever navigating the browser
	// to them, so the raw value legitimately survives there - only the two
	// navigable attributes are asserted on here.
	r := sampleResults()
	r.Assets = []models.Asset{
		{GameId: "1", Title: "Hostile Listing",
			Link:     `javascript:alert(1)`,
			ThumbUrl: `javascript:alert(2)`},
	}
	html := render(t, AssetPage(Filters{}, r))

	assert.NotContains(t, html, `href="javascript:`)
	assert.NotContains(t, html, `src="javascript:`)
	assert.Contains(t, html, `href="about:invalid`, "templ.URL's sanitization sentinel replaces the unsafe href scheme")
	assert.Contains(t, html, `src="about:invalid`, "templ.URL's sanitization sentinel replaces the unsafe src scheme")
}

func TestEveryCardOffersASimilarLink(t *testing.T) {
	// "Similar" has to be reachable from any card, in the same visual family
	// as the tag chips it sits beside.
	html := render(t, AssetPage(Filters{}, sampleResults()))

	assert.Contains(t, html, `class="tag is-similar"`)
	assert.Contains(t, html, `href="/?like=1"`)
	assert.Contains(t, html, `href="/?like=2"`)
}

func TestSimilarLinkPreservesTheRestOfTheFilterState(t *testing.T) {
	// Clicking "similar" while browsing a tag keeps that tag applied - the
	// same "current state plus this change" behaviour every other card
	// control uses.
	f := Filters{}.WithTag("2d")
	html := render(t, AssetPage(f, sampleResults()))

	assert.Contains(t, html, `href="/?like=1&amp;tags=2d"`)
}

func TestLikeChipNamesTheSourceAssetAndIsRemovable(t *testing.T) {
	f := Filters{}.WithLike("1")
	r := sampleResults()
	r.LikeTitle = "Pixel Tileset"
	html := render(t, ResultsRegion(f, r))

	assert.Contains(t, html, "similar to Pixel Tileset")
	assert.Contains(t, html, `href="/"`, "removing the only filter returns to the front page")
}

func TestLikeChipIsSilentWhenTheIdMatchedNothing(t *testing.T) {
	// An unknown or stale ?like= id has no title to name. The empty-result
	// page already explains the miss; a chip reading "similar to  x" would
	// only confuse it further.
	f := Filters{}.WithLike("999")
	html := render(t, ResultsRegion(f, sampleResults()))

	assert.NotContains(t, html, "similar to")
}

func TestChangingCurrencyKeepsEveryOtherFilter(t *testing.T) {
	// The currency picker is a form, so anything it does not carry as a hidden
	// field is discarded the moment somebody uses it.
	f := Filters{Query: "tiles", Price: models.PricingFree}.WithTag("2d").WithNotTag("3d")
	f.Author = "ana"
	html := render(t, CurrencyControl(f, money.Fallback()))

	assert.Contains(t, html, `name="q" value="tiles"`)
	assert.Contains(t, html, `name="tags" value="2d"`)
	assert.Contains(t, html, `name="not" value="3d"`)
	assert.Contains(t, html, `name="author" value="ana"`)
	assert.Contains(t, html, `name="price" value="free"`)
}
