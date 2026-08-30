package templates

import (
	"fmt"
	"time"

	"itchgrep/pkg/models"
)

// History is the /stats page's per-crawl log, in the style of Coverage and
// Traffic: a pure formatting layer over what storage.GetHistory returned, with
// deltas resolved at render time against the clock a test can pin.
type History struct {
	rows []models.CrawlRecord // newest first
}

// NewHistory takes the persisted history, oldest first as storage.GetHistory
// returns it, and orders it for display.
func NewHistory(records []models.CrawlRecord) History {
	rows := make([]models.CrawlRecord, len(records))
	for i, r := range records {
		rows[len(records)-1-i] = r
	}
	return History{rows: rows}
}

// Empty reports whether there is anything to show. A deploy before this
// feature shipped, or one with no crawl since, has nothing recorded - the
// section is omitted entirely rather than rendered empty.
func (h History) Empty() bool { return len(h.rows) == 0 }

// Rows returns one row per recorded crawl, newest first.
func (h History) Rows() []HistoryRow {
	rows := make([]HistoryRow, len(h.rows))
	for i, rec := range h.rows {
		row := HistoryRow{rec: rec}
		// The previous entry is the next-older one, i.e. the following index
		// in this newest-first slice - not h.rows[0], which is always the
		// newest crawl of all.
		if i+1 < len(h.rows) {
			row.prev = &h.rows[i+1]
		}
		rows[i] = row
	}
	return rows
}

// HistoryRow is one crawl's line and detail block on /stats.
type HistoryRow struct {
	rec  models.CrawlRecord
	prev *models.CrawlRecord // nil for the oldest recorded crawl
}

// When is the publish date, coarse on purpose: this is a log entry, not a
// timestamp anyone needs to the minute.
func (r HistoryRow) When() string { return r.rec.When.UTC().Format("Jan 2, 2006") }

// Indexed is the published asset count.
func (r HistoryRow) Indexed() string { return HumaneCount(uint64(r.rec.Indexed)) }

// DeltaLabel is the change in indexed assets since the previous recorded
// crawl, or a note that there is no previous one to compare against.
func (r HistoryRow) DeltaLabel() string {
	if r.prev == nil {
		return "first recorded crawl"
	}
	return formatDelta(r.rec.Indexed - r.prev.Indexed)
}

// Summary is the line shown on the collapsed <details> row.
func (r HistoryRow) Summary() string {
	return fmt.Sprintf("%s — %s assets (%s)", r.When(), r.Indexed(), r.DeltaLabel())
}

// formatDelta signs a change with a real minus sign, not a hyphen: an
// unfamiliar glyph in the negative case is a smaller cost than a figure that
// misreads as a range or a hyphenated word at a glance.
func formatDelta(delta int64) string {
	if delta < 0 {
		return "−" + HumaneCount(uint64(-delta))
	}
	return "+" + HumaneCount(uint64(delta))
}

// CoverageLabel is the crawl's own coverage reading, independent of whatever
// the site is currently serving - so an old row still says what it covered at
// the time, even after later crawls and catalogue growth have moved on.
func (r HistoryRow) CoverageLabel() string {
	if r.rec.Catalogue <= 0 {
		return "unknown"
	}
	pct := float64(r.rec.Indexed) / float64(r.rec.Catalogue) * 100
	return fmt.Sprintf("%.1f%% of %s", pct, HumaneCount(uint64(r.rec.Catalogue)))
}

// DurationLabel is how long the run took, from its own start to publish.
func (r HistoryRow) DurationLabel() string { return formatDuration(r.rec.DurationSeconds) }

func formatDuration(seconds int64) string {
	d := time.Duration(seconds) * time.Second
	hours := int(d / time.Hour)
	minutes := int((d % time.Hour) / time.Minute)
	if hours > 0 {
		return fmt.Sprintf("%dh %dm", hours, minutes)
	}
	return fmt.Sprintf("%dm", minutes)
}

// NewAssetsLabel is how many game ids this crawl saw for the first time.
func (r HistoryRow) NewAssetsLabel() string { return HumaneCount(uint64(r.rec.NewAssets)) }

// RemovedAssetsLabel is how many previously published game ids this crawl no
// longer found.
func (r HistoryRow) RemovedAssetsLabel() string { return HumaneCount(uint64(r.rec.RemovedAssets)) }

// FreePaidLabel is the free/paid split of the published index.
func (r HistoryRow) FreePaidLabel() string {
	paid := r.rec.Indexed - r.rec.FreeAssets
	return fmt.Sprintf("%s free / %s paid", HumaneCount(uint64(r.rec.FreeAssets)), HumaneCount(uint64(paid)))
}

// PagesLabel is how many pages this crawl fetched.
func (r HistoryRow) PagesLabel() string { return HumaneCount(uint64(r.rec.Pages)) }

// SlicesLabel is how many of the planned slices this crawl finished.
//
// Can read as less than 100% on a healthy crawl: the coverage target is
// reached before every slice runs, and the remainder are skipped on purpose
// rather than abandoned - see crawler.done in cmd/dataservice.
func (r HistoryRow) SlicesLabel() string {
	return fmt.Sprintf("%s/%s", HumaneCount(uint64(r.rec.SlicesDone)), HumaneCount(uint64(r.rec.SlicesPlanned)))
}

// RefusalsLabel is how many 429s itch.io returned during this crawl.
func (r HistoryRow) RefusalsLabel() string { return HumaneCount(uint64(r.rec.Refusals)) }

// TagsLabel is the size of the tag universe this crawl planned against.
func (r HistoryRow) TagsLabel() string { return HumaneCount(uint64(r.rec.Tags)) }

// Resumed reports whether this run picked up a checkpoint rather than
// starting fresh - shown only when true, since "did not resume" is the normal
// case and not worth a line.
func (r HistoryRow) Resumed() bool { return r.rec.Resumed }
