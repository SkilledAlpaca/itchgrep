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

func countInWindow(g ActivityGrid) int {
	n := 0
	for _, r := range g.Rows {
		for _, c := range r.Cells {
			if c.InWindow {
				n++
			}
		}
	}
	return n
}

func TestGridIs14WeeksBy7DaysSundayFirstEndingToday(t *testing.T) {
	g := NewTraffic(metrics.Snapshot{}, gridNow).Grid() // Saturday

	require.Len(t, g.Rows, 7)
	require.Len(t, g.Rows[0].Cells, 14)
	assert.Equal(t, "Sunday", g.Rows[0].Name)
	assert.Equal(t, "Saturday", g.Rows[6].Name)
	assert.True(t, g.Rows[6].Cells[13].InWindow, "today is the last cell of the last column")
}

func TestGridHoldsExactly90DaysOnEveryWeekday(t *testing.T) {
	// Oct 25, 2026 is a Sunday; walk a full week of "today" values.
	for d := 0; d < 7; d++ {
		now := time.Date(2026, 10, 25+d, 12, 0, 0, 0, time.UTC)
		g := NewTraffic(metrics.Snapshot{}, now).Grid()
		assert.Equal(t, 90, countInWindow(g), now.Weekday().String())
		assert.True(t, g.Rows[d].Cells[13].InWindow, "today sits in its weekday row")
	}
}

func TestGridLeavesOutOfWindowSlotsEmpty(t *testing.T) {
	// Sunday: the last column holds only today; 6 future cells are empty.
	sun := NewTraffic(metrics.Snapshot{}, time.Date(2026, 10, 25, 9, 0, 0, 0, time.UTC)).Grid()
	for r := 1; r < 7; r++ {
		assert.False(t, sun.Rows[r].Cells[13].InWindow, sun.Rows[r].Name+" is in the future")
		assert.Contains(t, sun.Rows[r].Cells[13].Class(), "activity-out")
	}
	// Today minus 89 days is Tuesday Jul 28, so Sunday and Monday lead out.
	assert.False(t, sun.Rows[0].Cells[0].InWindow)
	assert.False(t, sun.Rows[1].Cells[0].InWindow)
	assert.True(t, sun.Rows[2].Cells[0].InWindow)

	// Saturday: the whole last column is in the window.
	sat := NewTraffic(metrics.Snapshot{}, gridNow).Grid()
	for r := 0; r < 7; r++ {
		assert.True(t, sat.Rows[r].Cells[13].InWindow)
	}
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

	last := g.Rows[6].Cells[13]
	assert.Equal(t, 4, last.Level)
	assert.Equal(t, "Oct 31, 2026: 1,234 searches", last.Label)

	friday := g.Rows[5].Cells[13]
	assert.Equal(t, 1, friday.Level)
	assert.Equal(t, "Oct 30, 2026: 1 search", friday.Label, "singular for one")

	assert.Equal(t, 0, g.Rows[4].Cells[13].Level)
	assert.EqualValues(t, 1235, g.Total, "the out-of-window bucket must not be counted")
	assert.EqualValues(t, 1234, g.Busiest)
}

func TestGridLabelsTheColumnHoldingEachFirstOfTheMonth(t *testing.T) {
	g := NewTraffic(metrics.Snapshot{}, gridNow).Grid()

	// Oct 25 is column 13, so Oct 1 (Thursday of the week of Sun Sep 27) is
	// column 9, and Sep 1 (Tuesday of the week of Sun Aug 30) is column 5.
	// Aug 1 is before the window, so the window's opening month is named on
	// its first column instead: the window starts Mon Aug 3, in column 1
	// (column 0 is entirely outside it on a Saturday).
	assert.Equal(t, "", g.Months[0])
	assert.Equal(t, "Aug", g.Months[1])
	assert.Equal(t, "Sep", g.Months[5])
	assert.Equal(t, "Oct", g.Months[9])
	var labelled int
	for _, m := range g.Months {
		if m != "" {
			labelled++
		}
	}
	assert.Equal(t, 3, labelled)
}

func TestGridLeavesTheOpeningMonthUnlabelledWhenTheNextIsClose(t *testing.T) {
	// Wed 2026-11-25 minus 89 days is Fri Aug 28, in column 0; Sep 1 is in
	// column 1, so an "Aug" label would overprint "Sep".
	g := NewTraffic(metrics.Snapshot{}, time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)).Grid()
	assert.Equal(t, "", g.Months[0])
	assert.Equal(t, "Sep", g.Months[1])
}

func TestGridLabelsAMonthThatStartsInTheLastColumn(t *testing.T) {
	// Sunday Nov 1 is today, alone in the last column.
	g := NewTraffic(metrics.Snapshot{}, time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)).Grid()
	assert.Equal(t, "Nov", g.Months[13])
	assert.Equal(t, "Oct", g.Months[8])
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

func TestGridDaysListsNonZeroInWindowDaysOldestFirst(t *testing.T) {
	today := dayOf(gridNow)
	snap := metrics.Snapshot{}
	snap.Days[0] = models.DayBucket{EpochDay: today, Searches: 1234}
	snap.Days[1] = models.DayBucket{EpochDay: today - 1, Searches: 1}
	snap.Days[2] = models.DayBucket{EpochDay: today - 40, Searches: 7}
	snap.Days[3] = models.DayBucket{EpochDay: today - 100, Searches: 99999} // outside the window
	days := NewTraffic(snap, gridNow).Grid().Days()

	var labels []string
	for _, d := range days {
		labels = append(labels, d.Label)
	}
	assert.Equal(t, []string{"Sep 21, 2026: 7 searches", "Oct 30, 2026: 1 search", "Oct 31, 2026: 1,234 searches"}, labels)
	assert.Empty(t, NewTraffic(metrics.Snapshot{}, gridNow).Grid().Days())
}
