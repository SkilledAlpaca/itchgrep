package main

import (
	"itchgrep/pkg/models"
	"testing"

	"github.com/stretchr/testify/assert"
)

func newTestCrawler(total int64, cfg crawlConfig) *crawler {
	return &crawler{
		cfg:          cfg,
		itemsPerPage: 36,
		totalAssets:  total,
		seen:         make(map[string]struct{}),
	}
}

func asset(id string) models.Asset { return models.Asset{GameId: id} }

func TestRecordCountsOnlyUnseenAssets(t *testing.T) {
	// Assets carry ~9 tags each, so the same asset turns up in many slices.
	// Only the first sighting is new; the rest must not inflate the yield or
	// duplicate the asset into the index.
	c := newTestCrawler(100, crawlConfig{})

	assert.Equal(t, 3, c.record([]models.Asset{asset("a"), asset("b"), asset("c")}))
	assert.Equal(t, 1, c.record([]models.Asset{asset("b"), asset("c"), asset("d")}),
		"only the previously unseen asset counts")
	assert.Len(t, c.assets, 4, "duplicates must not be stored twice")
}

func TestRecordSkipsAssetsWithoutAnId(t *testing.T) {
	c := newTestCrawler(100, crawlConfig{})

	assert.Equal(t, 1, c.record([]models.Asset{asset(""), asset("a"), asset("")}))
	assert.Len(t, c.assets, 1)
}

func TestCoverageAndDone(t *testing.T) {
	c := newTestCrawler(100, crawlConfig{coverageTarget: 0.5})

	for i := 0; i < 49; i++ {
		c.record([]models.Asset{asset(string(rune('a' + i)))})
	}
	assert.InDelta(t, 0.49, c.coverage(), 0.001)
	assert.False(t, c.done())

	c.record([]models.Asset{asset("extra")})
	assert.True(t, c.done(), "the crawl stops once the coverage target is met")
}

func TestDoneRespectsThePageBudget(t *testing.T) {
	c := newTestCrawler(1000, crawlConfig{coverageTarget: 0.95, maxPages: 10})

	c.pagesFetched.Store(9)
	assert.False(t, c.done())

	c.pagesFetched.Store(10)
	assert.True(t, c.done(), "SCRAPE_MAX_PAGES must bound the whole crawl, not one view")
}

func TestCoverageIsZeroWhenTotalIsUnknown(t *testing.T) {
	c := newTestCrawler(0, crawlConfig{coverageTarget: 0.95})
	c.record([]models.Asset{asset("a")})

	assert.Equal(t, 0.0, c.coverage(), "must not divide by zero")
	assert.False(t, c.done())
}

func TestRankForKeepsRootRankingAheadOfSliceRanking(t *testing.T) {
	// InvPopularity drives search relevance. Root page numbers are a genuine
	// global popularity rank; page 3 of tag-fonts is not comparable to page 3
	// of tag-pixel-art, so slice-only assets must sort behind everything the
	// root view ranked.
	c := newTestCrawler(100, crawlConfig{})
	c.maxRootRank = 200

	assert.EqualValues(t, 3, c.rankFor(true, 3), "root pages keep their true rank")
	assert.EqualValues(t, 203, c.rankFor(false, 3), "slice pages rank after the whole root view")
	assert.Greater(t, c.rankFor(false, 1), c.rankFor(true, 200),
		"the worst root-ranked asset still outranks the best slice-only one")
}

func TestSliceSpent(t *testing.T) {
	const window = 5
	threshold := 0.05 * 36 // SLICE_MIN_YIELD against a 36-item page

	tests := []struct {
		name   string
		yields []int
		want   bool
	}{
		{"window not yet full", []int{0, 0, 0}, false},
		{"productive slice", []int{36, 30, 28, 31, 20}, false},
		{"exhausted slice", []int{1, 0, 0, 1, 0}, true},
		{"marginal but above threshold", []int{2, 2, 2, 2, 2}, false},
		{"one dense page cannot rescue a spent slice", []int{8, 0, 0, 0, 0}, true},
		{"exactly at the threshold is not spent", []int{9, 0, 0, 0, 0}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, sliceSpent(tt.yields, window, threshold))
		})
	}
}

func TestLoadCrawlConfigDefaults(t *testing.T) {
	cfg := loadCrawlConfig()

	assert.Equal(t, 0.95, cfg.coverageTarget)
	assert.Equal(t, 0.90, cfg.coverageFloor)
	assert.Equal(t, 0.05, cfg.minYield)
	assert.EqualValues(t, 0, cfg.maxPages, "unbounded unless SCRAPE_MAX_PAGES is set")
}

func TestScrapeMaxPagesDisarmsThePublishFloor(t *testing.T) {
	// A deliberately truncated smoke-test run would otherwise always trip the
	// floor and refuse to store anything, which defeats its purpose.
	t.Setenv("SCRAPE_MAX_PAGES", "20")

	cfg := loadCrawlConfig()

	assert.EqualValues(t, 20, cfg.maxPages)
	assert.Equal(t, 0.0, cfg.coverageFloor)
}

func TestInvalidConfigFallsBackToDefaults(t *testing.T) {
	t.Setenv("COVERAGE_TARGET", "not-a-number")
	t.Setenv("TAG_CACHE_MAX_AGE", "yesterday")

	cfg := loadCrawlConfig()

	assert.Equal(t, 0.95, cfg.coverageTarget)
	assert.EqualValues(t, 168*60*60, int64(cfg.tagCacheMaxAge.Seconds()))
}
