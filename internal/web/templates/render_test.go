package templates

import (
	"context"
	"strings"
	"testing"

	"itchgrep/pkg/models"

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
		"masthead": render(t, Masthead()),
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
	assert.NotContains(t, render(t, SortControl(Filters{})), "Best match")
	assert.Contains(t, render(t, SortControl(Filters{Query: "x"})), "Best match")
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
