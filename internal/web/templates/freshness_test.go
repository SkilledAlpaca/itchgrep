package templates

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

var published = time.Date(2026, 8, 9, 14, 30, 0, 0, time.UTC)

func at(offset time.Duration) Freshness {
	return NewFreshness(published, published.Add(offset))
}

func TestAgeReadsAsSomethingAPersonWouldSay(t *testing.T) {
	cases := map[time.Duration]string{
		30 * time.Second:    "less than an hour ago",
		59 * time.Minute:    "less than an hour ago",
		90 * time.Minute:    "1 hour ago",
		5 * time.Hour:       "5 hours ago",
		23 * time.Hour:      "23 hours ago",
		30 * time.Hour:      "yesterday",
		3 * 24 * time.Hour:  "3 days ago",
		40 * 24 * time.Hour: "40 days ago",
	}
	for offset, want := range cases {
		assert.Equal(t, want, at(offset).Label(), "at +%s", offset)
	}
}

func TestNothingIsClaimedBeforeAnIndexHasLoaded(t *testing.T) {
	// A zero timestamp means "no dataset yet", not "published in year 1". The
	// line has to disappear rather than announce the index is 2025 years old.
	cold := NewFreshness(time.Time{}, published)

	assert.False(t, cold.Known())
	assert.False(t, cold.Stale(), "unknown is not stale; there is nothing to be stale")
	assert.Empty(t, cold.DateTime())
	assert.NotContains(t, render(t, IndexAge(cold, Coverage{})), "index updated")
}

func TestAFutureTimestampIsClampedRatherThanRendered(t *testing.T) {
	// The dataservice and the webserver can disagree about the clock. Better a
	// slightly wrong "less than an hour ago" than a visible "-1 hours ago".
	skewed := at(-2 * time.Hour)

	assert.Equal(t, time.Duration(0), skewed.Age)
	assert.Equal(t, "less than an hour ago", skewed.Label())
}

func TestStalenessIsFlaggedOnlyOnceItMeansSomething(t *testing.T) {
	// Crawls are triggered by hand and take hours, so a two-day-old index is
	// ordinary. An indicator that is amber most of the time gets ignored.
	assert.False(t, at(2*24*time.Hour).Stale())
	assert.True(t, at(8*24*time.Hour).Stale())

	html := render(t, IndexAge(at(8*24*time.Hour), Coverage{}))
	assert.Contains(t, html, "is-stale")
	assert.Contains(t, html, "crawl may have stopped")
}

func TestTheExactTimeIsAlwaysAvailableBehindTheRoundedOne(t *testing.T) {
	// "3 days ago" is served from a cache for up to five minutes, so it is only
	// ever approximate. The machine-readable stamp is what is actually true.
	html := render(t, IndexAge(at(3*24*time.Hour), Coverage{}))

	assert.Contains(t, html, `datetime="2026-08-09T14:30:00Z"`)
	assert.Contains(t, html, `title="9 August 2026, 14:30 UTC"`)
	assert.Contains(t, html, "3 days ago")
}
