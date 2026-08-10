package config

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestCrawlIntervalDefaultsToWeekly(t *testing.T) {
	assert.Equal(t, DefaultCrawlInterval, CrawlInterval())
}

func TestCrawlIntervalCanBeDisabled(t *testing.T) {
	// Zero is the only way back to the old behaviour - an index that refreshes
	// only when something calls /trigger-fetch - so it must survive parsing
	// rather than being treated as "unset" and replaced by the default.
	t.Setenv("CRAWL_INTERVAL", "0")
	assert.Equal(t, time.Duration(0), CrawlInterval())
}

func TestInvalidCrawlIntervalFallsBackRatherThanDisabling(t *testing.T) {
	// Silently reading a typo as "never crawl again" would freeze the index on
	// a deployment nobody is watching, which is the failure this whole schedule
	// exists to prevent.
	for _, v := range []string{"weekly", "-1h", "168"} {
		t.Setenv("CRAWL_INTERVAL", v)
		assert.Equal(t, DefaultCrawlInterval, CrawlInterval(), "CRAWL_INTERVAL=%q", v)
	}
}
