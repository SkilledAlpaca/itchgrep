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

func TestRecordRequestClassifiesByRoute(t *testing.T) {
	c := New()
	c.RecordRequest(routeIndex, false, 200)
	c.RecordRequest(routeAbout, false, 200)
	c.RecordRequest(routeStats, false, 200)
	c.RecordRequest(routeStatic, false, 200)
	c.RecordRequest("", false, 404) // unmatched path: counted in Total only

	snap := c.Snapshot()
	assert.EqualValues(t, 1, snap.Index)
	assert.EqualValues(t, 1, snap.About)
	assert.EqualValues(t, 1, snap.Stats)
	assert.EqualValues(t, 1, snap.Static)
	assert.EqualValues(t, 5, snap.Total)
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
