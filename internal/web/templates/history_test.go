package templates

import (
	"testing"
	"time"

	"itchgrep/pkg/models"

	"github.com/stretchr/testify/assert"
)

func TestHistoryOfNoRecordsIsEmpty(t *testing.T) {
	h := NewHistory(nil)
	assert.True(t, h.Empty())
	assert.Empty(t, h.Rows())
}

func TestHistoryRowsAreNewestFirst(t *testing.T) {
	// storage.GetHistory returns oldest first, newest last - the opposite of
	// what a reader wants to see at the top of the page.
	records := []models.CrawlRecord{
		{When: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), Indexed: 100},
		{When: time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC), Indexed: 110},
		{When: time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC), Indexed: 130},
	}
	h := NewHistory(records)
	require := assert.New(t)
	rows := h.Rows()
	require.Len(rows, 3)
	require.Equal("Mar 1, 2026", rows[0].When())
	require.Equal("Feb 1, 2026", rows[1].When())
	require.Equal("Jan 1, 2026", rows[2].When())
}

func TestOldestRowHasNoDelta(t *testing.T) {
	records := []models.CrawlRecord{
		{Indexed: 100},
		{Indexed: 110},
	}
	rows := NewHistory(records).Rows()
	assert.Equal(t, "first recorded crawl", rows[len(rows)-1].DeltaLabel())
}

func TestDeltaFormatting(t *testing.T) {
	cases := []struct {
		name string
		prev int64
		cur  int64
		want string
	}{
		{"growth", 95699, 96903, "+1,204"},
		{"shrinkage uses a real minus sign", 96903, 95699, "−1,204"},
		{"no change", 100, 100, "+0"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			records := []models.CrawlRecord{
				{Indexed: c.prev},
				{Indexed: c.cur},
			}
			rows := NewHistory(records).Rows()
			// rows[0] is the newest (c.cur), compared against rows[1] (c.prev).
			assert.Equal(t, c.want, rows[0].DeltaLabel())
		})
	}
}

func TestSummaryLine(t *testing.T) {
	records := []models.CrawlRecord{
		{When: time.Date(2026, 8, 20, 0, 0, 0, 0, time.UTC), Indexed: 95699},
		{When: time.Date(2026, 8, 29, 0, 0, 0, 0, time.UTC), Indexed: 96903},
	}
	rows := NewHistory(records).Rows()
	assert.Equal(t, "Aug 29, 2026 — 96,903 assets (+1,204)", rows[0].Summary())
}

func TestCoverageLabel(t *testing.T) {
	cases := []struct {
		name      string
		indexed   int64
		catalogue int64
		want      string
	}{
		{"typical crawl", 96903, 108697, "89.1% of 108,697"},
		{"unknown catalogue", 100, 0, "unknown"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			row := HistoryRow{rec: models.CrawlRecord{Indexed: c.indexed, Catalogue: c.catalogue}}
			assert.Equal(t, c.want, row.CoverageLabel())
		})
	}
}

func TestDurationLabel(t *testing.T) {
	cases := []struct {
		name    string
		seconds int64
		want    string
	}{
		{"hours and minutes", 6*3600 + 12*60, "6h 12m"},
		{"minutes only", 42 * 60, "42m"},
		{"zero", 0, "0m"},
		{"exact hour", 3600, "1h 0m"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			row := HistoryRow{rec: models.CrawlRecord{DurationSeconds: c.seconds}}
			assert.Equal(t, c.want, row.DurationLabel())
		})
	}
}

func TestFreePaidLabel(t *testing.T) {
	row := HistoryRow{rec: models.CrawlRecord{Indexed: 96903, FreeAssets: 83214}}
	assert.Equal(t, "83,214 free / 13,689 paid", row.FreePaidLabel())
}

func TestSlicesLabel(t *testing.T) {
	row := HistoryRow{rec: models.CrawlRecord{SlicesDone: 314, SlicesPlanned: 320}}
	assert.Equal(t, "314/320", row.SlicesLabel())
}

func TestResumedOnlyReportsWhenSet(t *testing.T) {
	assert.True(t, HistoryRow{rec: models.CrawlRecord{Resumed: true}}.Resumed())
	assert.False(t, HistoryRow{rec: models.CrawlRecord{Resumed: false}}.Resumed())
}
