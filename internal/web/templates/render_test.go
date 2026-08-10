package templates

import (
	"context"
	"strings"
	"testing"
	"time"

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
	age := NewFreshness(time.Now().Add(-time.Hour), time.Now())
	html := render(t, IndexAge(age, Coverage{Indexed: 96903, Catalogue: 108808}))

	assert.Contains(t, html, "89% of catalogue indexable")
	// On its own line, not trailing the age: the two say different kinds of
	// thing, and run together they read as one sentence about neither.
	assert.Contains(t, html, `<p class="index-coverage"`)
	// The exact counts stay reachable on hover rather than in the line itself.
	assert.Contains(t, html, "96,903 of 108,808 assets")
	assert.NotContains(t, html, "visually-hidden", "the detail is not repeated inline")
}

func TestCoverageIsSilentWhenItWasNeverMeasured(t *testing.T) {
	// An index published before crawls recorded their completeness has no
	// figure. Rendering 0% would report a total failure that did not happen.
	age := NewFreshness(time.Now().Add(-time.Hour), time.Now())
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
