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
	"strconv"
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

// The functions below were previously copy-pasted, one small variation apiece,
// into cmd/dataservice and internal/web. They are collected here so both
// binaries read an environment variable the same way once - except where the
// two forks had actually come to disagree, which is called out on each
// function that does.

// EnvFloat reads name as a float64, falling back to def if the variable is
// unset or fails to parse. Any other value, including a negative one, is
// accepted as-is - callers for whom a negative or zero value is meaningful
// (RATE_LIMIT_RPS<=0 disables rate limiting, for instance) decide what to do
// with it themselves.
//
// A value that is set but does not parse is not treated the same as unset: it
// is almost always a typo, and a warning here is the only place that can say
// so once for this variable.
func EnvFloat(name string, def float64) float64 {
	v, ok := os.LookupEnv(name)
	if !ok {
		return def
	}
	parsed, err := strconv.ParseFloat(v, 64)
	if err != nil {
		logging.Warning("Ignoring invalid %s=%q, using %v", name, v, def)
		return def
	}
	return parsed
}

// EnvNonNegativeFloat is EnvFloat plus a floor at zero, for variables - crawl
// coverage targets and thresholds - that are meaningless if negative.
//
// It also differs from EnvFloat in how it treats an empty value: a variable
// explicitly set to "" is read the same as one left unset (silently falls
// back to def, no warning), rather than as a parse failure. That divergence
// is inherited from the two functions this was consolidated from rather than
// chosen on purpose - it is harmless in practice, since nothing sets these
// variables to an empty string on purpose - and is preserved rather than
// unified so this change carries no behaviour difference for the crawl
// tuning that already depends on it (COVERAGE_TARGET, COVERAGE_FLOOR,
// SLICE_MIN_YIELD).
func EnvNonNegativeFloat(name string, def float64) float64 {
	v := os.Getenv(name)
	if v == "" {
		return def
	}
	parsed, err := strconv.ParseFloat(v, 64)
	if err != nil || parsed < 0 {
		logging.Warning("Ignoring invalid %s=%q, using %v", name, v, def)
		return def
	}
	return parsed
}

// EnvInt reads name as an int64, falling back to def if the variable is
// unset, fails to parse, or is not strictly positive - a crawl-tuning
// variable set to zero or negative would otherwise mean something with no
// sensible interpretation (a cache TTL of zero days, a max-tags of -5), so it
// is treated the same as a typo.
func EnvInt(name string, def int64) int64 {
	v := os.Getenv(name)
	if v == "" {
		return def
	}
	parsed, err := strconv.ParseInt(v, 10, 64)
	if err != nil || parsed <= 0 {
		logging.Warning("Ignoring invalid %s=%q, using %d", name, v, def)
		return def
	}
	return parsed
}

// EnvDuration reads name as a time.Duration (Go's duration syntax, e.g.
// "24h"), falling back to def if the variable is unset, fails to parse, or is
// not strictly positive. Unlike CrawlInterval above, zero has no special
// meaning for the variables this serves (TAG_CACHE_MAX_AGE,
// CHECKPOINT_MAX_AGE), so it is rejected like any other invalid value rather
// than accepted as "disabled".
func EnvDuration(name string, def time.Duration) time.Duration {
	v := os.Getenv(name)
	if v == "" {
		return def
	}
	parsed, err := time.ParseDuration(v)
	if err != nil || parsed <= 0 {
		logging.Warning("Ignoring invalid %s=%q, using %v", name, v, def)
		return def
	}
	return parsed
}

// EnvBool reads name as a bool (strconv.ParseBool syntax: "1", "t", "TRUE",
// "false", ...), falling back to def if the variable is unset or fails to
// parse.
func EnvBool(name string, def bool) bool {
	v, ok := os.LookupEnv(name)
	if !ok {
		return def
	}
	parsed, err := strconv.ParseBool(v)
	if err != nil {
		logging.Warning("Ignoring invalid %s=%q, using %v", name, v, def)
		return def
	}
	return parsed
}
