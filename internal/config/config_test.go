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
	// Zero is the only way back to the old behavior - an index that refreshes
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

func TestEnvFloatDefaultsWhenUnset(t *testing.T) {
	assert.Equal(t, 5.0, EnvFloat("UNSET_ENV_FLOAT_VAR", 5.0))
}

func TestEnvFloatParsesAndAllowsNegative(t *testing.T) {
	t.Setenv("ENV_FLOAT_VAR", "-2.5")
	// RATE_LIMIT_RPS<=0 disabling rate limiting is exactly this: a negative or
	// zero value is meaningful to some callers, so EnvFloat must not reject it
	// on their behalf.
	assert.Equal(t, -2.5, EnvFloat("ENV_FLOAT_VAR", 5.0))
}

func TestEnvFloatFallsBackOnUnparseableValue(t *testing.T) {
	t.Setenv("ENV_FLOAT_VAR", "not-a-number")
	assert.Equal(t, 5.0, EnvFloat("ENV_FLOAT_VAR", 5.0))
}

func TestEnvFloatWarnsOnAnExplicitlyEmptyValue(t *testing.T) {
	// Unlike EnvNonNegativeFloat, EnvFloat reads os.LookupEnv: a variable
	// present but set to "" is a parse failure (falls back to def), not
	// silently read as unset.
	t.Setenv("ENV_FLOAT_VAR", "")
	assert.Equal(t, 5.0, EnvFloat("ENV_FLOAT_VAR", 5.0))
}

func TestEnvNonNegativeFloatDefaultsWhenUnset(t *testing.T) {
	assert.Equal(t, 0.75, EnvNonNegativeFloat("UNSET_ENV_FLOAT_VAR", 0.75))
}

func TestEnvNonNegativeFloatRejectsNegative(t *testing.T) {
	t.Setenv("ENV_FLOAT_VAR", "-0.1")
	assert.Equal(t, 0.75, EnvNonNegativeFloat("ENV_FLOAT_VAR", 0.75),
		"a negative coverage target or floor has no sensible meaning")
}

func TestEnvNonNegativeFloatAcceptsZero(t *testing.T) {
	t.Setenv("ENV_FLOAT_VAR", "0")
	assert.Equal(t, 0.0, EnvNonNegativeFloat("ENV_FLOAT_VAR", 0.75))
}

func TestEnvNonNegativeFloatTreatsAnExplicitlyEmptyValueAsUnset(t *testing.T) {
	// Unlike EnvFloat, EnvNonNegativeFloat reads os.Getenv: "" and unset are
	// indistinguishable, so both silently fall back with no warning logged.
	t.Setenv("ENV_FLOAT_VAR", "")
	assert.Equal(t, 0.75, EnvNonNegativeFloat("ENV_FLOAT_VAR", 0.75))
}

func TestEnvIntDefaultsWhenUnsetOrInvalid(t *testing.T) {
	assert.EqualValues(t, 1000, EnvInt("UNSET_ENV_INT_VAR", 1000))

	for _, v := range []string{"0", "-5", "not-a-number"} {
		t.Setenv("ENV_INT_VAR", v)
		assert.EqualValues(t, 1000, EnvInt("ENV_INT_VAR", 1000), "ENV_INT_VAR=%q", v)
	}
}

func TestEnvIntParsesAPositiveValue(t *testing.T) {
	t.Setenv("ENV_INT_VAR", "25")
	assert.EqualValues(t, 25, EnvInt("ENV_INT_VAR", 1000))
}

func TestEnvDurationDefaultsWhenUnsetOrInvalid(t *testing.T) {
	assert.Equal(t, time.Hour, EnvDuration("UNSET_ENV_DURATION_VAR", time.Hour))

	for _, v := range []string{"0", "-1h", "soon"} {
		t.Setenv("ENV_DURATION_VAR", v)
		assert.Equal(t, time.Hour, EnvDuration("ENV_DURATION_VAR", time.Hour), "ENV_DURATION_VAR=%q", v)
	}
}

func TestEnvDurationParsesAPositiveValue(t *testing.T) {
	t.Setenv("ENV_DURATION_VAR", "30m")
	assert.Equal(t, 30*time.Minute, EnvDuration("ENV_DURATION_VAR", time.Hour))
}

func TestEnvBoolDefaultsWhenUnsetOrInvalid(t *testing.T) {
	assert.True(t, EnvBool("UNSET_ENV_BOOL_VAR", true))

	t.Setenv("ENV_BOOL_VAR", "not-a-bool")
	assert.True(t, EnvBool("ENV_BOOL_VAR", true))
}

func TestEnvBoolParsesTrueAndFalse(t *testing.T) {
	t.Setenv("ENV_BOOL_VAR", "false")
	assert.False(t, EnvBool("ENV_BOOL_VAR", true))

	t.Setenv("ENV_BOOL_VAR", "1")
	assert.True(t, EnvBool("ENV_BOOL_VAR", false))
}
