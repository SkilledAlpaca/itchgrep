package fetcher

import (
	"net/http"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// These tests drive rateLimiter instances directly rather than the
// package-level one, so they neither trip rateLimiterOnce nor depend on
// SCRAPE_RPS being set in the environment.

func TestNewRateLimiterSpacesAtTheConfiguredRate(t *testing.T) {
	assert.Equal(t, 250*time.Millisecond, newRateLimiter(4).interval)
}

func TestTheRateIsNotAdaptive(t *testing.T) {
	// itch.io's 429s are driven by the missing session cookie, not by our
	// request rate, so slowing down in response to them costs throughput and
	// buys nothing. Measured: 8/16 requests refused without a cookie jar
	// versus 0/16 with one, at the same one request per second.
	l := newRateLimiter(1)

	for i := 0; i < 10; i++ {
		l.pause(l.epoch, 0)
	}

	assert.Equal(t, time.Second, l.interval, "429s must never change the request rate")
}

func TestPauseAppliesOncePerRefusalEpisode(t *testing.T) {
	// The workers already in flight when itch.io starts refusing all report
	// 429 within moments of each other. Only the first should open a pause;
	// treating each echo as fresh cause would let one episode - or one page
	// retrying with a growing backoff - push the global pause out repeatedly.
	l := newRateLimiter(1000)
	epoch := l.acquire()

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			l.pause(epoch, 200*time.Millisecond)
		}()
	}
	wg.Wait()

	assert.LessOrEqual(t, time.Until(l.pausedUntil), 200*time.Millisecond,
		"eight echoes of one episode must not stack into a longer pause")
	assert.Equal(t, epoch+1, l.epoch, "the episode should have advanced exactly once")
}

func TestAFreshEpisodeOpensANewPause(t *testing.T) {
	l := newRateLimiter(1000)
	l.pause(l.acquire(), 0)

	// l.epoch is what a request issued *after* that pause would carry, so its
	// 429 is a genuinely new refusal rather than an echo.
	l.pause(l.epoch, 200*time.Millisecond)

	assert.GreaterOrEqual(t, time.Until(l.pausedUntil), 150*time.Millisecond)
}

func TestAPauseHoldsBackWorkersThatAlreadyHoldSlots(t *testing.T) {
	// A slot is reserved before the request's turn comes round, so by the time
	// a worker wakes up the fleet may have been told to back off. Firing on
	// the strength of a stale reservation would leak one request per worker
	// straight through a Retry-After cooldown.
	l := &rateLimiter{interval: 30 * time.Millisecond}

	start := time.Now()
	returned := make([]time.Duration, 8)
	var wg sync.WaitGroup
	for i := 0; i < len(returned); i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			l.acquire()
			returned[i] = time.Since(start)
		}(i)
	}

	// Long enough for every worker to have taken a slot, short enough that
	// only the first one's slot has actually come due.
	time.Sleep(10 * time.Millisecond)
	l.pause(l.epoch, 200*time.Millisecond)
	wg.Wait()

	held := 0
	for _, d := range returned {
		if d >= 200*time.Millisecond {
			held++
		}
	}
	assert.GreaterOrEqual(t, held, len(returned)-1,
		"every worker but the one already through must wait out the cooldown")
}

func TestPauseHoldsBackEveryWorker(t *testing.T) {
	// The whole point of the global limiter: a 429 seen by one worker must
	// pause the others, not just the one that saw it.
	l := newRateLimiter(1000) // 1ms spacing, so only the pause is measurable
	l.pause(l.epoch, 150*time.Millisecond)

	start := time.Now()
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			l.acquire()
		}()
	}
	wg.Wait()

	assert.GreaterOrEqual(t, time.Since(start), 150*time.Millisecond,
		"every worker should have waited out the cooldown")
}

func TestHTTPClientKeepsCookies(t *testing.T) {
	// itch.io hands out an itchio_token cookie and refuses cookieless clients
	// with 429 about half the time regardless of request rate. Losing the jar
	// would look exactly like an aggressive rate limit.
	require.NotNil(t, httpClient.Jar, "the fetcher must retain session cookies")

	u, err := url.Parse("https://itch.io/game-assets")
	require.NoError(t, err)

	httpClient.Jar.SetCookies(u, []*http.Cookie{{Name: "itchio_token", Value: "test"}})
	assert.Len(t, httpClient.Jar.Cookies(u), 1, "cookies set by itch.io must be sent back")
}

func TestAcquireSpacesConcurrentRequests(t *testing.T) {
	l := &rateLimiter{interval: 20 * time.Millisecond}
	const spacing = 20 * time.Millisecond

	start := time.Now()
	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			l.acquire()
		}()
	}
	wg.Wait()

	// Five reserved slots at 20ms apart: the first is immediate, so the last
	// starts at ~80ms.
	assert.GreaterOrEqual(t, time.Since(start), 4*spacing)
}

func TestParseRetryAfter(t *testing.T) {
	tests := []struct {
		name  string
		value string
		want  time.Duration
		ok    bool
	}{
		{name: "empty", value: "", ok: false},
		{name: "delta seconds", value: "30", want: 30 * time.Second, ok: true},
		{name: "delta seconds padded", value: " 30 ", want: 30 * time.Second, ok: true},
		{name: "zero is unusable", value: "0", ok: false},
		{name: "negative is unusable", value: "-5", ok: false},
		{name: "garbage", value: "soon", ok: false},
		{name: "clamped", value: "99999", want: maxRetryAfter, ok: true},
		{name: "past http date", value: "Mon, 02 Jan 2006 15:04:05 GMT", ok: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := parseRetryAfter(tt.value)
			assert.Equal(t, tt.ok, ok)
			if tt.ok {
				assert.Equal(t, tt.want, got)
			}
		})
	}
}

func TestParseRetryAfterHTTPDate(t *testing.T) {
	future := time.Now().Add(45 * time.Second).UTC().Format(http.TimeFormat)
	got, ok := parseRetryAfter(future)
	require.True(t, ok)
	assert.InDelta(t, (45 * time.Second).Seconds(), got.Seconds(), 2)
}

func TestCalculateBackoffIsBoundedAndNeverCollapses(t *testing.T) {
	base := 1 * time.Second

	for attempt := 0; attempt < 60; attempt++ {
		got := calculateBackoff(attempt, base)
		assert.LessOrEqual(t, got, maxBackoff, "attempt %d exceeded the cap", attempt)
		assert.Greater(t, got, time.Duration(0), "attempt %d collapsed to zero", attempt)
	}
}
