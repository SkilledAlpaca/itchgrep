package metrics

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"itchgrep/pkg/models"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRecordRequestUnderRace(t *testing.T) {
	// The request path is the one genuinely concurrent place in this server;
	// this is what -race exists to catch.
	c := New()
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			c.RecordRequest(routeResults, i%2 == 0, 200)
			c.RecordProbe()
		}(i)
	}
	wg.Wait()

	snap := c.Snapshot()
	assert.EqualValues(t, 100, snap.Total)
	assert.EqualValues(t, 100, snap.Results)
	assert.EqualValues(t, 50, snap.Searches)
}

func TestRingZeroesAStaleBucketOnDayRollover(t *testing.T) {
	c := New()
	dayOne := time.Date(2026, 1, 1, 3, 30, 0, 0, time.UTC)
	c.bumpDay(dayOne)
	c.bumpDay(dayOne.Add(10 * time.Hour)) // same UTC day

	idx := dayOne.Unix() / secondsPerDay % daysKept
	assert.EqualValues(t, 2, c.days[idx].Searches)

	// daysKept days later lands on the same ring slot but a different epoch
	// day; the stale count must not bleed into the new one.
	later := dayOne.AddDate(0, 0, daysKept)
	c.bumpDay(later)

	assert.EqualValues(t, 1, c.days[idx].Searches)
	assert.Equal(t, later.Unix()/secondsPerDay, c.days[idx].EpochDay)
}

func TestDaysSplitAtUTCMidnight(t *testing.T) {
	c := New()
	c.bumpDay(time.Date(2026, 3, 1, 23, 59, 59, 0, time.UTC))
	c.bumpDay(time.Date(2026, 3, 2, 0, 0, 0, 0, time.UTC))

	var days int
	for _, d := range c.Snapshot().Days {
		if d.Searches == 1 {
			days++
		}
	}
	assert.Equal(t, 2, days)
}

func TestRestoreDaysDropsBucketsOlderThanTheWindow(t *testing.T) {
	now := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
	today := now.Unix() / secondsPerDay

	var saved [daysKept]models.DayBucket
	saved[0] = models.DayBucket{EpochDay: today, Searches: 5}
	saved[1] = models.DayBucket{EpochDay: today - (daysKept - 1), Searches: 3} // oldest day still shown
	saved[2] = models.DayBucket{EpochDay: today - daysKept, Searches: 99}      // one day too old

	c := New()
	c.restoreDays(saved, now)

	var total uint64
	for _, d := range c.Snapshot().Days {
		total += d.Searches
	}
	assert.EqualValues(t, 8, total, "the out-of-window bucket must be dropped")

	// A restored day keeps counting where it left off rather than resetting.
	c.bumpDay(now)
	assert.EqualValues(t, 6, c.days[today%daysKept].Searches)
}

func TestDaysSurviveASnapshotRoundTrip(t *testing.T) {
	t.Setenv("DATA_DIR", t.TempDir())

	c := New()
	c.bumpDay(time.Now())
	c.bumpDay(time.Now())
	require.NoError(t, c.PersistNow())

	restored := Restore().Snapshot()
	var total uint64
	for _, d := range restored.Days {
		total += d.Searches
	}
	assert.EqualValues(t, 2, total)
}

func TestRestoreReadsALegacyFileWithHoursAndNoDays(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("DATA_DIR", dir)
	legacy := `{"FirstSeen":"2026-01-01T00:00:00Z","Total":7,"Searches":2,"Hours":[{"EpochHour":472392,"Searches":2}]}`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "traffic.json"), []byte(legacy), 0o644))

	snap := Restore().Snapshot()
	assert.EqualValues(t, 7, snap.Total)
	assert.EqualValues(t, 2, snap.Searches)
	for _, d := range snap.Days {
		assert.Zero(t, d.Searches, "a legacy file starts with an empty grid")
	}
}

func TestRecordRequestOnlyCountsSuccessfulSearches(t *testing.T) {
	c := New()
	c.RecordRequest(routeResults, true, 503) // index not ready yet: served nothing
	c.RecordRequest(routeIndex, true, 200)
	c.RecordRequest(routeResults, false, 200) // no query: not a search
	c.RecordRequest(routeIndex, true, 302)    // canonical redirect: the follow-up request does the work
	c.RecordRequest(routeIndex, true, 400)

	snap := c.Snapshot()
	assert.EqualValues(t, 1, snap.Searches)
	assert.EqualValues(t, 5, snap.Total)
	var days uint64
	for _, d := range snap.Days {
		days += uint64(d.Searches)
	}
	assert.EqualValues(t, 1, days, "the daily grid uses the same definition as the total")
}

func TestTagRouteWithAQueryStringCountsAsASearch(t *testing.T) {
	// /tag/{slug} carries its filter in the path, not the query string, but a
	// visitor can still add ?page=2 or a currency choice on top of it - and
	// that combination is a filtered browse the same way ?tags=... on / is.
	c := New()
	c.RecordRequest(routeTag, true, 200)
	c.RecordRequest(routeTag, false, 200) // the bare tag page: nothing to add to the query-present count

	snap := c.Snapshot()
	assert.EqualValues(t, 1, snap.Searches)
	assert.EqualValues(t, 2, snap.Tag, "both requests hit the tag route regardless of which counted as a search")
}

func TestRecordRequestClassifiesByRoute(t *testing.T) {
	c := New()
	c.RecordRequest(routeIndex, false, 200)
	c.RecordRequest(routeAbout, false, 200)
	c.RecordRequest(routeStats, false, 200)
	c.RecordRequest(routeStatic, false, 200)
	c.RecordRequest(routeNew, false, 200)
	c.RecordRequest(routeFeed, false, 200)
	c.RecordRequest(routeTag, false, 200)
	c.RecordRequest(routeSaved, false, 200)
	c.RecordRequest("", false, 404) // unmatched path: falls into Other

	snap := c.Snapshot()
	assert.EqualValues(t, 1, snap.Index)
	assert.EqualValues(t, 1, snap.About)
	assert.EqualValues(t, 1, snap.Stats)
	assert.EqualValues(t, 1, snap.Static)
	assert.EqualValues(t, 1, snap.New)
	assert.EqualValues(t, 1, snap.Feed)
	assert.EqualValues(t, 1, snap.Tag)
	assert.EqualValues(t, 1, snap.Saved)
	assert.EqualValues(t, 1, snap.Other)
	assert.EqualValues(t, 9, snap.Total)
}

// TestBreakdownIsExhaustive is the point of routeOther: every request lands
// in exactly one named bucket, so the per-route figures on /stats can be
// summed and compared against Total without the gap an unclassified route
// used to leave.
func TestBreakdownIsExhaustive(t *testing.T) {
	c := New()
	for _, route := range []string{
		routeIndex, routeResults, routeAbout, routeStats, routeStatic,
		routeNew, routeFeed, routeTag, routeSaved,
		"/robots.txt", "/sitemap.xml", "/opensearch.xml", "/.well-known/security.txt", "",
	} {
		c.RecordRequest(route, false, 200)
	}

	snap := c.Snapshot()
	sum := snap.Index + snap.Results + snap.About + snap.Stats + snap.Static +
		snap.New + snap.Feed + snap.Tag + snap.Saved + snap.Other
	assert.Equal(t, snap.Total, sum, "the named buckets plus Other must account for every request")
	assert.EqualValues(t, 5, snap.Other, "the five routes with no dedicated bucket")
}

// A test asserting every rate-limited route classifies used to live here, as
// a hand-copied slice of the constants below asserted against itself - which
// passed regardless of what cmd/webserver/main.go's real router contained,
// since this package has no way to see chi's route table (and, by design,
// no dependency on internal/web or chi at all - see the package doc above).
// That check now lives in cmd/webserver/router_test.go's
// TestEveryRateLimitedRouteHasABreakdownClassification, which walks the
// actual router chi.Walk sees in production and can therefore catch a
// route added to main.go without a matching case in RecordRequest below -
// something no test in this package could ever do.

func TestNilCountersAreNoOps(t *testing.T) {
	var c *Counters
	assert.NotPanics(t, func() {
		c.RecordRequest(routeIndex, true, 200)
		c.RecordProbe()
		_ = c.Snapshot()
	})
	assert.Equal(t, Snapshot{}, c.Snapshot())
}

// TestPersistNowWritesCountersImmediately covers the shutdown path in
// cmd/webserver/main.go: a graceful stop calls this directly, outside
// StartSnapshotter's own ticker, and expects the write to have actually
// landed by the time the process exits.
func TestPersistNowWritesCountersImmediately(t *testing.T) {
	t.Setenv("DATA_DIR", t.TempDir())

	c := New()
	c.RecordRequest(routeIndex, false, 200)
	require.NoError(t, c.PersistNow())

	restored := Restore()
	assert.EqualValues(t, 1, restored.Snapshot().Total)
}

func TestPersistNowOnNilCountersIsANoOp(t *testing.T) {
	var c *Counters
	assert.NotPanics(t, func() {
		assert.NoError(t, c.PersistNow())
	})
}

func TestSnapshotAddsRestoredTotalsToLiveOnes(t *testing.T) {
	c := New()
	c.base = Snapshot{Total: 10, Searches: 3}
	c.RecordRequest(routeIndex, false, 200)

	snap := c.Snapshot()
	assert.EqualValues(t, 11, snap.Total)
	assert.EqualValues(t, 3, snap.Searches)
}

func TestTagTailIsOtherAndNeverASearch(t *testing.T) {
	// /results/tags is the sidebar's rows, fetched on demand. It is not a page
	// view, so it must not inflate the /results bucket, and it never counts as
	// a search however it is asked.
	c := New()
	c.RecordRequest("/results/tags", true, 200)
	snap := c.Snapshot()
	assert.EqualValues(t, 0, snap.Results)
	assert.EqualValues(t, 1, snap.Other)
	assert.EqualValues(t, 0, snap.Searches)
}
