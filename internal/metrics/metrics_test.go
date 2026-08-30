package metrics

import (
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
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

func TestRingZeroesAStaleBucketOnHourRollover(t *testing.T) {
	c := New()
	hourOne := time.Date(2026, 1, 1, 3, 30, 0, 0, time.UTC)
	c.bumpHour(hourOne)
	c.bumpHour(hourOne)

	idx := hourOne.Unix() / 3600 % hoursInDay
	assert.EqualValues(t, 2, c.hours[idx].Searches)

	// 24 hours later lands on the same ring slot but a different epoch hour;
	// the stale count must not bleed into the new one.
	dayLater := hourOne.Add(24 * time.Hour)
	c.bumpHour(dayLater)

	assert.EqualValues(t, 1, c.hours[idx].Searches)
	assert.Equal(t, dayLater.Unix()/3600, c.hours[idx].EpochHour)
}

func TestRecordRequestOnlyCountsSuccessfulSearches(t *testing.T) {
	c := New()
	c.RecordRequest(routeResults, true, 503) // index not ready yet: served nothing
	c.RecordRequest(routeIndex, true, 200)
	c.RecordRequest(routeResults, false, 200) // no query: not a search

	snap := c.Snapshot()
	assert.EqualValues(t, 1, snap.Searches)
	assert.EqualValues(t, 3, snap.Total)
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

// TestEveryRateLimitedRouteHasABreakdownClassification mirrors the routing
// table's rate-limited group in cmd/webserver/main.go: every GET registered
// there must classify into a named bucket, not fall through into Other,
// which exists for the routes deliberately mounted outside that group
// (robots.txt, sitemap.xml, opensearch.xml, security.txt) and for paths that
// never matched at all.
//
// The route list here has to be kept in step with main.go by hand - a
// *Counters has no way to see chi's route table - but a slice literal beside
// this comment is a three-line diff to keep in sync, and a divergence fails
// this test loudly rather than silently under-counting on /stats.
func TestEveryRateLimitedRouteHasABreakdownClassification(t *testing.T) {
	rateLimitedRoutes := []string{
		routeIndex, routeResults, routeAbout, routeStats,
		routeNew, routeFeed, routeTag, routeSaved,
	}

	for _, route := range rateLimitedRoutes {
		c := New()
		c.RecordRequest(route, false, 200)
		snap := c.Snapshot()
		assert.EqualValues(t, 0, snap.Other, "%s must have its own bucket, not fall through to Other", route)
	}
}

func TestNilCountersAreNoOps(t *testing.T) {
	var c *Counters
	assert.NotPanics(t, func() {
		c.RecordRequest(routeIndex, true, 200)
		c.RecordProbe()
		_ = c.Snapshot()
	})
	assert.Equal(t, Snapshot{}, c.Snapshot())
}

func TestSnapshotAddsRestoredTotalsToLiveOnes(t *testing.T) {
	c := New()
	c.base = Snapshot{Total: 10, Searches: 3}
	c.RecordRequest(routeIndex, false, 200)

	snap := c.Snapshot()
	assert.EqualValues(t, 11, snap.Total)
	assert.EqualValues(t, 3, snap.Searches)
}
