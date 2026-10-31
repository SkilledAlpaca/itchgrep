package templates

import (
	"testing"
	"time"

	"itchgrep/internal/metrics"
	"itchgrep/pkg/models"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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

func TestCountingSinceDateTimeIsRFC3339InUTC(t *testing.T) {
	// This is the machine-readable half of CountingSince, for app.js to
	// rewrite into the viewer's own zone - the prose form above it cannot be
	// parsed back into an instant.
	firstSeen := now.Add(-30 * 24 * time.Hour)
	tr := NewTraffic(metrics.Snapshot{Started: now, FirstSeen: firstSeen}, now)

	assert.Equal(t, firstSeen.UTC().Format(time.RFC3339), tr.CountingSinceDateTime())
}

func TestCountingSinceDateTimeIsEmptyWhenUnknown(t *testing.T) {
	// Mirrors Freshness.DateTime: an empty attribute rather than a formatted
	// zero time, so app.js's Date() parse fails closed instead of rendering
	// 1 January 1970.
	tr := NewTraffic(metrics.Snapshot{}, now)
	assert.Equal(t, "", tr.CountingSinceDateTime())
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

// gridNow is a Saturday, so the last column is full and the first column's
// leading cells fall outside the window.
var gridNow = time.Date(2026, 10, 31, 15, 0, 0, 0, time.UTC)

func dayOf(t time.Time) int64 { return t.UTC().Unix() / 86400 }

func TestGridIs13WeeksBy7DaysSundayFirstEndingToday(t *testing.T) {
	// Sunday 2026-10-25 .. Saturday 2026-10-31: today is the bottom of the
	// last column, and 12 earlier columns precede it.
	g := NewTraffic(metrics.Snapshot{}, gridNow).Grid()

	require.Len(t, g.Rows, 7)
	require.Len(t, g.Rows[0].Cells, 13)
	assert.Equal(t, "Sunday", g.Rows[0].Name)
	assert.Equal(t, "Saturday", g.Rows[6].Name)

	inWindow := 0
	for _, r := range g.Rows {
		for _, c := range r.Cells {
			if c.InWindow {
				inWindow++
			}
		}
	}
	// wd=6: the first column's Sunday is 90 days back, one day older than
	// the ring keeps, so 90 of the 91 slots hold a day.
	assert.Equal(t, 90, inWindow)
	assert.False(t, g.Rows[0].Cells[0].InWindow, "the Sunday 90 days back is outside the window")
	assert.True(t, g.Rows[1].Cells[0].InWindow)
	assert.True(t, g.Rows[6].Cells[12].InWindow, "today is the last cell of the last column")
}

func TestGridLeavesTheDaysAfterTodayOutsideTheWindow(t *testing.T) {
	wed := time.Date(2026, 10, 28, 9, 0, 0, 0, time.UTC)
	g := NewTraffic(metrics.Snapshot{}, wed).Grid()

	assert.True(t, g.Rows[3].Cells[12].InWindow, "Wednesday is today")
	for r := 4; r < 7; r++ {
		assert.False(t, g.Rows[r].Cells[12].InWindow, g.Rows[r].Name+" is in the future")
		assert.Contains(t, g.Rows[r].Cells[12].Class(), "activity-out")
	}
	// Four weeks into October: a Wednesday shows 85+3 = 88 days.
	total := 0
	for _, r := range g.Rows {
		for _, c := range r.Cells {
			if c.InWindow {
				total++
			}
		}
	}
	assert.Equal(t, 88, total)
}

func TestActivityLevelMath(t *testing.T) {
	assert.Equal(t, 0, activityLevel(0, 100), "zero searches is level 0")
	assert.Equal(t, 0, activityLevel(0, 0), "an all-zero window is level 0")
	assert.Equal(t, 1, activityLevel(1, 100), "any nonzero day is at least level 1")
	assert.Equal(t, 1, activityLevel(25, 100))
	assert.Equal(t, 2, activityLevel(26, 100), "ceil, not round")
	assert.Equal(t, 2, activityLevel(50, 100))
	assert.Equal(t, 3, activityLevel(51, 100))
	assert.Equal(t, 3, activityLevel(75, 100))
	assert.Equal(t, 4, activityLevel(76, 100))
	assert.Equal(t, 4, activityLevel(100, 100), "the busiest day is level 4")
}

func TestGridPlacesCountsLevelsAndHiddenText(t *testing.T) {
	today := dayOf(gridNow)
	snap := metrics.Snapshot{}
	snap.Days[0] = models.DayBucket{EpochDay: today, Searches: 1234}
	snap.Days[1] = models.DayBucket{EpochDay: today - 1, Searches: 1}
	snap.Days[2] = models.DayBucket{EpochDay: today - 100, Searches: 99999} // outside the window
	g := NewTraffic(snap, gridNow).Grid()

	last := g.Rows[6].Cells[12]
	assert.Equal(t, 4, last.Level)
	assert.Equal(t, "Oct 31, 2026: 1,234 searches", last.Label)

	friday := g.Rows[5].Cells[12]
	assert.Equal(t, 1, friday.Level)
	assert.Equal(t, "Oct 30, 2026: 1 search", friday.Label, "singular for one")

	assert.Equal(t, 0, g.Rows[4].Cells[12].Level)
	assert.EqualValues(t, 1235, g.Total, "the out-of-window bucket must not be counted")
	assert.EqualValues(t, 1234, g.Busiest)
}

func TestGridLabelsTheColumnHoldingEachFirstOfTheMonth(t *testing.T) {
	g := NewTraffic(metrics.Snapshot{}, gridNow).Grid()

	// Oct 25 is column 12, so Oct 1 (Thursday of the week of Sun Sep 27) is
	// column 8 and Sep 1 (Tuesday of the week of Sun Aug 30) is column 4.
	// Nov 1 is after today, and Aug 1 is before the window starts.
	want := map[int]string{8: "Oct", 4: "Sep", 0: ""}
	for col, label := range want {
		assert.Equal(t, label, g.Months[col], "column %d", col)
	}
	var labelled []string
	for _, m := range g.Months {
		if m != "" {
			labelled = append(labelled, m)
		}
	}
	assert.Equal(t, []string{"Sep", "Oct"}, labelled, "one label per first-of-month inside the window")
}

func TestGridSummaryCoversTheZeroAndNonzeroCases(t *testing.T) {
	zero := NewTraffic(metrics.Snapshot{}, gridNow).Grid().Summary()
	assert.Equal(t, "No searches recorded in the last 90 days yet.", zero)

	today := dayOf(gridNow)
	snap := metrics.Snapshot{}
	snap.Days[0] = models.DayBucket{EpochDay: today - 3, Searches: 1000}
	snap.Days[1] = models.DayBucket{EpochDay: today - 10, Searches: 234}
	got := NewTraffic(snap, gridNow).Grid().Summary()
	assert.Equal(t, "1,234 searches in the last 90 days. Busiest day: Oct 28, 2026 (1,000).", got)
}

func TestGridBusiestDayTieGoesToTheMostRecent(t *testing.T) {
	today := dayOf(gridNow)
	snap := metrics.Snapshot{}
	snap.Days[0] = models.DayBucket{EpochDay: today - 20, Searches: 5}
	snap.Days[1] = models.DayBucket{EpochDay: today - 2, Searches: 5}
	g := NewTraffic(snap, gridNow).Grid()
	assert.Equal(t, "Oct 29, 2026", g.BusiestDay.Format("Jan 2, 2006"))
}
