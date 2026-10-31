package templates

import (
	"fmt"
	"strconv"
	"time"

	"itchgrep/internal/metrics"
	"itchgrep/pkg/models"
)

// Traffic is the public /stats page's view model, in the style of Coverage
// and Freshness: a pure-Go read of a metrics.Snapshot resolved against a
// clock passed in, so a test can pin "now" instead of waiting on the wall
// clock. See internal/metrics for what is and is not counted - this type adds
// no numbers of its own, only formatting.
type Traffic struct {
	snap metrics.Snapshot
	now  time.Time
}

// NewTraffic builds the view model for a snapshot as of now.
func NewTraffic(s metrics.Snapshot, now time.Time) Traffic {
	return Traffic{snap: s, now: now}
}

func (t Traffic) Total() uint64    { return t.snap.Total }
func (t Traffic) Index() uint64    { return t.snap.Index }
func (t Traffic) Results() uint64  { return t.snap.Results }
func (t Traffic) About() uint64    { return t.snap.About }
func (t Traffic) Stats() uint64    { return t.snap.Stats }
func (t Traffic) Static() uint64   { return t.snap.Static }
func (t Traffic) New() uint64      { return t.snap.New }
func (t Traffic) Feed() uint64     { return t.snap.Feed }
func (t Traffic) Tag() uint64      { return t.snap.Tag }
func (t Traffic) Saved() uint64    { return t.snap.Saved }
func (t Traffic) Other() uint64    { return t.snap.Other }
func (t Traffic) Searches() uint64 { return t.snap.Searches }

// Uptime is how long this process has been running - not how long counting
// has spanned, which CountingSince answers and which can predate a restart.
func (t Traffic) Uptime() time.Duration {
	if t.snap.Started.IsZero() {
		return 0
	}
	return t.now.Sub(t.snap.Started)
}

// UptimeLabel is the coarse reading of Uptime, in the same spirit as
// Freshness.Label: these pages are cached for a minute, so second-level
// precision would be precision the response cannot honour.
func (t Traffic) UptimeLabel() string {
	d := t.Uptime()
	switch {
	case d < time.Minute:
		return "just started"
	case d < time.Hour:
		return fmt.Sprintf("%d minutes", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%d hours", int(d.Hours()))
	default:
		return fmt.Sprintf("%d days", int(d.Hours()/24))
	}
}

// CountingSince is when these totals started accumulating - which, thanks to
// traffic.json, is normally well before this process started.
func (t Traffic) CountingSince() string {
	if t.snap.FirstSeen.IsZero() {
		return "unknown"
	}
	return t.snap.FirstSeen.UTC().Format("2 January 2006, 15:04 UTC")
}

// CountingSinceDateTime is the machine-readable form for the <time>
// element's attribute, in the same spirit as Freshness.DateTime: app.js
// needs a parseable instant to rewrite into the viewer's own zone, and
// "unknown" is not one.
func (t Traffic) CountingSinceDateTime() string {
	if t.snap.FirstSeen.IsZero() {
		return ""
	}
	return t.snap.FirstSeen.UTC().Format(time.RFC3339)
}

// Activity-grid geometry: 14 weekly columns of 7 days, Sunday first like the
// git contribution graph. The window is exactly models.DaysKept days ending
// today. 14 columns always span at least 92 days (the last column starts on
// the current week's Sunday), so the whole window fits, and the slots older
// than it or after today render empty.
const (
	gridWeeks    = 14
	gridDays     = 7
	secondsInDay = 24 * 60 * 60
)

// ActivityCell is one day of the grid. A cell outside the window (after today
// in the last column, older than the window in the first) has no level and no
// text, and the markup hides it from assistive technology.
type ActivityCell struct {
	InWindow bool
	Level    int    // 0-4; 0 means no searches
	Count    uint64 // searches that day
	Label    string // "Oct 31, 2026: 1,234 searches" - hidden text and title
}

// Class is the level's CSS class, which carries the colour (see app.css).
func (c ActivityCell) Class() string {
	if !c.InWindow {
		return "activity-cell activity-out"
	}
	return "activity-cell activity-l" + strconv.Itoa(c.Level)
}

// ActivityRow is one weekday across all 14 weeks.
type ActivityRow struct {
	Name  string // "Monday", for assistive technology
	Short string // "Mon", shown only on Mon, Wed and Fri to keep the axis uncrowded
	Cells [gridWeeks]ActivityCell
}

// ShowShort reports whether the abbreviated day name is visible.
func (r ActivityRow) ShowShort() bool { return r.Short != "" }

// ActivityGrid is the whole 90-day view: the table's rows and month header,
// plus the figures the summary line reads aloud.
type ActivityGrid struct {
	Months  [gridWeeks]string // "Oct" over the column holding a 1st, else ""
	Rows    [gridDays]ActivityRow
	Total   uint64
	Busiest uint64
	// BusiestDay is zero when no search was recorded.
	BusiestDay time.Time
}

// Summary is the visible gist of the grid, for readers who skip the table.
func (g ActivityGrid) Summary() string {
	if g.Total == 0 {
		return "No searches recorded in the last 90 days yet."
	}
	return fmt.Sprintf("%s searches in the last 90 days. Busiest day: %s (%s).",
		humaniseCount(int64(g.Total)), g.BusiestDay.Format("Jan 2, 2006"), humaniseCount(int64(g.Busiest)))
}

// activityLevel buckets a day's count against the window's busiest day:
// ceil(4*count/max), so any nonzero day is at least level 1 and the busiest
// day is level 4. Zero searches, or an all-zero window, is level 0.
func activityLevel(count, peak uint64) int {
	if count == 0 || peak == 0 {
		return 0
	}
	return int((4*count + peak - 1) / peak)
}

func dayLabel(day time.Time, count uint64) string {
	noun := "searches"
	if count == 1 {
		noun = "search"
	}
	return fmt.Sprintf("%s: %s %s", day.Format("Jan 2, 2006"), humaniseCount(int64(count)), noun)
}

// Grid lays the snapshot's daily ring out as 13 weeks ending with the current
// one. All arithmetic is on UTC epoch days, so the grid reads the same
// whatever zone the server or the viewer is in.
func (t Traffic) Grid() ActivityGrid {
	today := t.now.UTC().Unix() / secondsInDay
	oldest := today - models.DaysKept + 1
	weekday := int64(time.Unix(today*secondsInDay, 0).UTC().Weekday())
	first := today - weekday - 7*(gridWeeks-1) // Sunday of the first column

	lo := oldest
	counts := make(map[int64]uint64, models.DaysKept)
	for _, d := range t.snap.Days {
		if d.EpochDay >= lo && d.EpochDay <= today {
			counts[d.EpochDay] = d.Searches
		}
	}

	// Ascending, with >=, so a tie goes to the most recent day.
	var g ActivityGrid
	var peak uint64
	for day := lo; day <= today; day++ {
		n := counts[day]
		g.Total += n
		if n > 0 && n >= peak {
			peak = n
			g.BusiestDay = time.Unix(day*secondsInDay, 0).UTC()
		}
	}
	g.Busiest = peak

	for r := range g.Rows {
		name := time.Weekday(r).String()
		g.Rows[r].Name = name
		if r == 1 || r == 3 || r == 5 {
			g.Rows[r].Short = name[:3]
		}
	}

	for c := 0; c < gridWeeks; c++ {
		for r := 0; r < gridDays; r++ {
			day := first + int64(7*c+r)
			when := time.Unix(day*secondsInDay, 0).UTC()
			if when.Day() == 1 && day >= oldest && day <= today {
				g.Months[c] = when.Format("Jan")
			}
			if day < oldest || day > today {
				continue // zero value: outside the window
			}
			n := counts[day]
			g.Rows[r].Cells[c] = ActivityCell{
				InWindow: true,
				Level:    activityLevel(n, peak),
				Count:    n,
				Label:    dayLabel(when, n),
			}
		}
	}
	return g
}

// HumaneCount formats a scalar the same way the coverage figures are: grouped
// thousands, so a bare "96903" beside them does not read as a different kind
// of number.
func HumaneCount(n uint64) string { return humaniseCount(int64(n)) }
