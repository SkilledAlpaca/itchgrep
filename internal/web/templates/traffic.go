package templates

import (
	"fmt"
	"strconv"
	"time"

	"itchgrep/internal/metrics"
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

// Bar is one hour of the histogram: how many searches it saw, and the label
// under it.
type Bar struct {
	Label string
	Count uint64
}

// CountAttr is Count as a string, for <meter value="...">. templ's dynamic
// attributes are strings; the conversion is done here rather than in the
// markup so stats.templ stays free of strconv calls.
func (b Bar) CountAttr() string { return strconv.FormatUint(b.Count, 10) }

// Title is the hover text for one bar - the only place the exact count
// appears, since the bar's height alone cannot be read precisely.
func (b Bar) Title() string {
	return fmt.Sprintf("%s — %s searches", b.Label, humaniseCount(int64(b.Count)))
}

// Bars returns 24 hourly buckets oldest-first, ending with the current hour -
// so the histogram always reads left-to-right as "a day ago" through "now",
// regardless of which ring slot in the snapshot each hour happens to occupy.
func (t Traffic) Bars() []Bar {
	nowHour := t.now.UTC().Unix() / 3600
	bars := make([]Bar, 24)
	for i := range bars {
		epochHour := nowHour - int64(23-i)
		var count uint64
		for _, hb := range t.snap.Hours {
			if hb.EpochHour == epochHour {
				count = hb.Searches
				break
			}
		}
		bars[i] = Bar{
			Label: time.Unix(epochHour*3600, 0).UTC().Format("15:04"),
			Count: count,
		}
	}
	return bars
}

// Peak is the tallest bar, floored at 1 so a <meter max={peak}> never divides
// by zero on a quiet day.
func (t Traffic) Peak() uint64 {
	var peak uint64 = 1
	for _, b := range t.Bars() {
		if b.Count > peak {
			peak = b.Count
		}
	}
	return peak
}

// PeakAttr is Peak as a string, for <meter max="...">.
func (t Traffic) PeakAttr() string { return strconv.FormatUint(t.Peak(), 10) }

// HumaneCount formats a scalar the same way the coverage figures are: grouped
// thousands, so a bare "96903" beside them does not read as a different kind
// of number.
func HumaneCount(n uint64) string { return humaniseCount(int64(n)) }
