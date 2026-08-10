package templates

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

var published = time.Date(2026, 8, 9, 14, 30, 0, 0, time.UTC)

// at is an index published at `published` and read `offset` later, on a server
// with scheduled rebuilds switched off - so these cases say nothing about a
// next update.
func at(offset time.Duration) Freshness {
	return NewFreshness(published, published.Add(offset), 0)
}

// every is at() for a server that rebuilds on a schedule.
func every(offset, interval time.Duration) Freshness {
	return NewFreshness(published, published.Add(offset), interval)
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
	cold := NewFreshness(time.Time{}, published, 168*time.Hour)

	assert.False(t, cold.Known())
	assert.False(t, cold.Stale(), "unknown is not stale; there is nothing to be stale")
	assert.False(t, cold.DueKnown(), "an interval is not a due date without a publication to add it to")
	assert.Empty(t, cold.DateTime())
	assert.NotContains(t, render(t, IndexAge(cold, Coverage{})), "index updated")
}

func TestTheNextUpdateIsAnnouncedFromTheScheduleTheCrawlRunsOn(t *testing.T) {
	week := 168 * time.Hour
	cases := map[time.Duration]string{
		0:                              "in about 7 days",
		24 * time.Hour:                 "in about 6 days",
		5*24*time.Hour + time.Hour:     "tomorrow",
		6*24*time.Hour + time.Hour:     "in about 23 hours",
		167 * time.Hour:                "in about an hour",
		167*time.Hour + 30*time.Minute: "within the hour",
		// Past due is the ordinary state for a few minutes either side of the
		// scheduler's quarter-hourly check, and for the hours a crawl then
		// takes to finish. It is not an error to report.
		200 * time.Hour: "any time now",
	}
	for offset, want := range cases {
		assert.Equal(t, want, every(offset, week).DueLabel(), "at +%s", offset)
	}
}

func TestNoNextUpdateIsPromisedWhenNothingSchedulesOne(t *testing.T) {
	// CRAWL_INTERVAL=0 is a supported deployment: the index changes only when
	// somebody calls /trigger-fetch. Naming a date there would be a fabrication.
	manual := every(3*24*time.Hour, 0)

	assert.False(t, manual.DueKnown())
	assert.Empty(t, manual.DueLabel())
	assert.NotContains(t, render(t, IndexAge(manual, Coverage{})), "next update")
}

func TestTheDueTimeIsCheckableBehindTheRoundedOne(t *testing.T) {
	html := render(t, IndexAge(every(0, 168*time.Hour), Coverage{}))

	assert.Contains(t, html, "next update")
	assert.Contains(t, html, `datetime="2026-08-16T14:30:00Z"`)
	assert.Contains(t, html, `title="16 August 2026, 14:30 UTC"`)
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
