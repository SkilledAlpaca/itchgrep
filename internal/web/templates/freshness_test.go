package templates

import (
	"encoding/json"
	"os"
	"testing"
	"time"

	"itchgrep/pkg/money"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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

func TestAHealthyScheduleNeverTripsTheStalenessWarning(t *testing.T) {
	week := 168 * time.Hour

	// The index reaches a full week of age at the exact moment the weekly
	// rebuild comes due, and stays that old for the hours the crawl then takes.
	// "The crawl may have stopped" next to "next update any time now" would be
	// the banner contradicting the schedule every single week.
	assert.False(t, every(week+5*time.Hour, week).Stale(),
		"hours past due is the rebuild window, not a stopped crawl")

	// A full day past due is more than any crawl explains.
	assert.True(t, every(week+25*time.Hour, week).Stale())

	// On a slow schedule the age alone says nothing: a 10-day-old index is
	// exactly what a monthly rebuild is supposed to serve.
	assert.False(t, every(10*24*time.Hour, 30*24*time.Hour).Stale())
}

func TestTheExactTimeIsAlwaysAvailableBehindTheRoundedOne(t *testing.T) {
	// "3 days ago" is served from a cache for up to five minutes, so it is only
	// ever approximate. The machine-readable stamp is what is actually true.
	html := render(t, IndexAge(at(3*24*time.Hour), Coverage{}))

	assert.Contains(t, html, `datetime="2026-08-09T14:30:00Z"`)
	assert.Contains(t, html, `title="9 August 2026, 14:30 UTC"`)
	assert.Contains(t, html, "3 days ago")
}

// wording is testdata/freshness_wording.json, the table app.js is held to as
// well (see the file's own comment).
type wording struct {
	Age                 [][2]any `json:"age"`
	Due                 [][2]any `json:"due"`
	StaleNoSchedule     [][2]any `json:"staleNoSchedule"`
	StaleWeeklySchedule [][2]any `json:"staleWeeklySchedule"`
}

func loadWording(t *testing.T) wording {
	t.Helper()
	raw, err := os.ReadFile("testdata/freshness_wording.json")
	require.NoError(t, err)
	var w wording
	require.NoError(t, json.Unmarshal(raw, &w))
	return w
}

func TestWordingTablePinsTheAgeAndDueLabels(t *testing.T) {
	w := loadWording(t)
	now := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
	for _, row := range w.Age {
		secs := time.Duration(row[0].(float64)) * time.Second
		f := NewFreshness(now.Add(-secs), now, 0)
		assert.Equal(t, row[1], f.Label(), "age %v", secs)
	}
	for _, row := range w.Due {
		until := time.Duration(row[0].(float64)) * time.Second
		f := NewFreshness(now.Add(-1000*time.Hour), now, 1000*time.Hour+until)
		assert.Equal(t, row[1], f.DueLabel(), "until %v", until)
	}
}

func TestWordingTablePinsTheStaleThreshold(t *testing.T) {
	w := loadWording(t)
	now := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
	for _, row := range w.StaleNoSchedule {
		age := time.Duration(row[0].(float64)) * time.Second
		f := NewFreshness(now.Add(-age), now, 0)
		assert.Equal(t, row[1], f.Stale(), "no schedule, age %v", age)
		// StaleAt is the first instant Stale turns true, which is what app.js
		// compares the visitor's clock with.
		at, err := time.Parse(time.RFC3339, f.StaleAt())
		require.NoError(t, err)
		assert.Equal(t, row[1], !now.Before(at), "StaleAt, no schedule, age %v", age)
	}
	for _, row := range w.StaleWeeklySchedule {
		age := time.Duration(row[0].(float64)) * time.Second
		f := NewFreshness(now.Add(-age), now, 7*24*time.Hour)
		assert.Equal(t, row[1], f.Stale(), "weekly schedule, age %v", age)
		at, err := time.Parse(time.RFC3339, f.StaleAt())
		require.NoError(t, err)
		assert.Equal(t, row[1], !now.Before(at), "StaleAt, weekly, age %v", age)
	}
	assert.Empty(t, Freshness{}.StaleAt())
}

func TestStalenessIsExposedForTheBrowserToDecide(t *testing.T) {
	now := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
	f := NewFreshness(now.Add(-24*time.Hour), now, 7*24*time.Hour)
	html := render(t, IndexAge(f, Coverage{}))
	assert.Contains(t, html, `data-stale-at="2026-11-07T12:00:00Z"`, "the due time plus the grace")
	assert.Contains(t, html, `<time data-relative="age"`)
	assert.Contains(t, html, `<time data-relative="due"`)
	assert.NotContains(t, html, "the crawl may have stopped", "fresh at render time: no server-rendered warning")

	old := NewFreshness(now.Add(-30*24*time.Hour), now, 7*24*time.Hour)
	assert.Contains(t, render(t, IndexAge(old, Coverage{})), "the crawl may have stopped", "the no-script fallback still warns")

	assert.Contains(t, render(t, ArrivalsPage(sampleResults().Assets, money.Fallback(), f)), `<time data-relative="age"`)
}
