// Package config holds the settings both binaries have to agree on.
//
// There is exactly one so far, and it earns the package: the dataservice
// decides when to rebuild the index from CRAWL_INTERVAL, and the webserver
// tells visitors when the next rebuild is due from the same variable. Two
// copies of the parsing would let a typo produce a site that confidently
// announces a schedule nothing is running on.
package config

import (
	"itchgrep/internal/logging"
	"os"
	"time"
)

// DefaultCrawlInterval is how long a published index is allowed to go without
// being rebuilt.
//
// A week, because the two costs point in opposite directions: a full crawl
// takes about six hours at the polite default rate, while the catalogue grows
// by a few dozen assets in that time. Daily would spend a quarter of every day
// re-fetching assets that have not changed, for a freshness nobody browsing an
// asset catalogue would notice.
const DefaultCrawlInterval = 168 * time.Hour

// CrawlInterval reads CRAWL_INTERVAL. Zero means scheduled rebuilds are off and
// the index only changes when something calls /trigger-fetch.
//
// A value that does not parse falls back to the default rather than to zero:
// reading a typo as "never crawl again" would freeze the index on a deployment
// nobody is watching, which is the failure the schedule exists to prevent.
func CrawlInterval() time.Duration {
	v := os.Getenv("CRAWL_INTERVAL")
	if v == "" {
		return DefaultCrawlInterval
	}
	parsed, err := time.ParseDuration(v)
	if err != nil || parsed < 0 {
		logging.Warning("Ignoring invalid CRAWL_INTERVAL=%q, using %v", v, DefaultCrawlInterval)
		return DefaultCrawlInterval
	}
	return parsed
}
