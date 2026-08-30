package templates

import (
	"testing"
	"time"

	"itchgrep/internal/metrics"
	"itchgrep/pkg/models"

	"github.com/stretchr/testify/assert"
)

var now = time.Date(2026, 8, 10, 15, 0, 0, 0, time.UTC)

func TestUptimeIsMeasuredFromProcessStart(t *testing.T) {
	started := now.Add(-90 * time.Minute)
	tr := NewTraffic(metrics.Snapshot{Started: started}, now)

	assert.Equal(t, 90*time.Minute, tr.Uptime())
	assert.Equal(t, "1 hours", tr.UptimeLabel())
}

func TestCountingSinceCanPredateTheProcess(t *testing.T) {
	firstSeen := now.Add(-30 * 24 * time.Hour)
	tr := NewTraffic(metrics.Snapshot{Started: now, FirstSeen: firstSeen}, now)

	assert.Equal(t, firstSeen.Format("2 January 2006, 15:04 UTC"), tr.CountingSince())
}

func TestCountingSinceIsUnknownWhenNothingHasBeenRestored(t *testing.T) {
	tr := NewTraffic(metrics.Snapshot{}, now)
	assert.Equal(t, "unknown", tr.CountingSince())
}

func TestBarsCoverTheTrailing24HoursOldestFirst(t *testing.T) {
	nowHour := now.Unix() / 3600
	snap := metrics.Snapshot{
		Hours: [24]models.HourBucket{
			0: {EpochHour: nowHour, Searches: 7},        // this hour
			1: {EpochHour: nowHour - 23, Searches: 3},   // 23 hours ago: oldest bar
			2: {EpochHour: nowHour - 100, Searches: 99}, // outside the window: must not appear
		},
	}
	tr := NewTraffic(snap, now)
	bars := tr.Bars()

	assert.Len(t, bars, 24)
	assert.EqualValues(t, 3, bars[0].Count, "oldest bar is 23 hours ago")
	assert.EqualValues(t, 7, bars[23].Count, "last bar is the current hour")

	var total uint64
	for _, b := range bars {
		total += b.Count
	}
	assert.EqualValues(t, 10, total, "the out-of-window bucket must not be counted")
}

func TestPeakIsFlooredAtOneOnAQuietDay(t *testing.T) {
	tr := NewTraffic(metrics.Snapshot{}, now)
	assert.EqualValues(t, 1, tr.Peak())
}

func TestTrafficExposesEveryBreakdownBucket(t *testing.T) {
	// One accessor per bucket metrics.Counters tracks, so stats.templ can
	// render a row for each without reaching into the snapshot directly.
	snap := metrics.Snapshot{New: 1, Feed: 2, Tag: 3, Saved: 4, Other: 5}
	tr := NewTraffic(snap, now)

	assert.EqualValues(t, 1, tr.New())
	assert.EqualValues(t, 2, tr.Feed())
	assert.EqualValues(t, 3, tr.Tag())
	assert.EqualValues(t, 4, tr.Saved())
	assert.EqualValues(t, 5, tr.Other())
}

func TestPeakIsTheTallestBar(t *testing.T) {
	nowHour := now.Unix() / 3600
	snap := metrics.Snapshot{
		Hours: [24]models.HourBucket{
			0: {EpochHour: nowHour - 5, Searches: 42},
		},
	}
	tr := NewTraffic(snap, now)
	assert.EqualValues(t, 42, tr.Peak())
}
